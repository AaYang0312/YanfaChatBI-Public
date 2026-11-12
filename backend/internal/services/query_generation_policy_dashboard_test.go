package services

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"
	"testing"
)

func TestGenerateOnlyDashboardRejectsMissingSafeProjection(t *testing.T) {
	for _, mode := range []bool{false, true} {
		for _, loader := range []DashboardProjectionLoader{nil, func(context.Context, *models.Chat) (*DashboardProjection, error) { return nil, nil }, func(context.Context, *models.Chat) (*DashboardProjection, error) {
			return &DashboardProjection{Context: "old"}, nil
		}} {
			_, err := prepareDashboardGeneration(context.Background(), &models.Chat{}, NewGenerationPolicy(mode), true, loader, nil, nil)
			if err == nil {
				t.Errorf("v2 with missing safe callbacks admitted (auto=%v)", mode)
			}
		}
	}
}
func TestGenerateOnlyDashboardUsesCurrentProjectionForEveryMetadataCall(t *testing.T) {
	calls, legacyCalls := 0, 0
	version := "current view metadata"
	loader := func(_ context.Context, chat *models.Chat) (*DashboardProjection, error) {
		return &DashboardProjection{Context: "current scoped schema and KB", GetTableInfo: func(_ context.Context, c llm.ToolCall) (*llm.ToolResult, error) {
			calls++
			return &llm.ToolResult{Content: version}, nil
		}, GetKnowledgeBase: func(_ context.Context, c llm.ToolCall) (*llm.ToolResult, error) {
			calls++
			return &llm.ToolResult{Content: "current KB"}, nil
		}}, nil
	}
	legacy := func(_ context.Context, c llm.ToolCall) (*llm.ToolResult, error) {
		legacyCalls++
		return &llm.ToolResult{Content: "stale secret"}, nil
	}
	available := []llm.ToolDefinition{{Name: "get_table_info"}, {Name: "get_knowledge_base"}, {Name: "execute_dashboard_query"}, {Name: "generate_final_response"}}
	for _, auto := range []bool{false, true} {
		prepared, err := prepareDashboardGeneration(context.Background(), &models.Chat{}, NewGenerationPolicy(auto), true, loader, available, legacy)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.Context != "current scoped schema and KB" {
			t.Errorf("unsafe/missing context: %q", prepared.Context)
		}
		if !auto && (len(prepared.Tools) != 2 || prepared.Tools[0].Name != "get_table_info" || prepared.Tools[1].Name != "generate_final_response") {
			t.Error("strict dashboard tool list leaked data/KB tools")
		}
		for _, expected := range []string{"current view metadata", "revoked column removed"} {
			version = expected
			r, e := prepared.Executor(context.Background(), llm.ToolCall{Name: "get_table_info", Arguments: map[string]interface{}{"object_refs": []interface{}{map[string]interface{}{"schema": "public", "name": "v", "kind": "view"}}}})
			if e != nil || r == nil || r.Content != expected {
				t.Errorf("metadata not current: %+v %v", r, e)
			}
		}
		r, e := prepared.Executor(context.Background(), llm.ToolCall{Name: "get_knowledge_base"})
		if e != nil || r == nil || (auto && r.Content != "current KB") || (!auto && !r.IsError) {
			t.Errorf("KB routing unsafe: %+v %v", r, e)
		}
	}
	if legacyCalls != 0 || calls != 5 {
		t.Fatalf("metadata routing: legacy=%d callbacks=%d", legacyCalls, calls)
	}
}
func TestGenerateOnlyDashboardProjectionErrorNeverFallsBack(t *testing.T) {
	denied := errors.New("current projection denied")
	_, err := prepareDashboardGeneration(context.Background(), &models.Chat{}, NewGenerationPolicy(true), true, func(context.Context, *models.Chat) (*DashboardProjection, error) { return nil, denied }, nil, nil)
	if !errors.Is(err, denied) {
		t.Fatalf("projection failure lost: %v", err)
	}
}
func TestGenerateOnlyExecutorRequiresSafeMetadataCallback(t *testing.T) {
	legacyCalls := 0
	legacy := func(_ context.Context, c llm.ToolCall) (*llm.ToolResult, error) {
		legacyCalls++
		return &llm.ToolResult{Content: "legacy rows"}, nil
	}
	for _, auto := range []bool{false, true} {
		for _, v2 := range []bool{false, true} {
			exec := WrapGenerationExecutor(legacy, GenerationToolOptions{Policy: NewGenerationPolicy(auto), UseCurrentProjection: v2})
			r, err := exec(context.Background(), llm.ToolCall{Name: "get_table_info"})
			if err != nil || r == nil || r.IsError == (auto && !v2) {
				t.Errorf("metadata fail-closed wrong auto=%v v2=%v: %+v %v", auto, v2, r, err)
			}
		}
	}
	if legacyCalls != 1 {
		t.Fatalf("legacy metadata called %d; want only allowed legacy mode once", legacyCalls)
	}
}
func TestGenerateOnlyPolicyGuardsOptionalCallbacks(t *testing.T) {
	calls := 0
	callback := func(_ context.Context, c llm.ToolCall) (*llm.ToolResult, error) {
		calls++
		return &llm.ToolResult{Content: "rows"}, nil
	}
	exec := WrapGenerationExecutor(callback, GenerationToolOptions{GetTableInfo: callback, GetKnowledgeBase: callback})
	for _, name := range []string{"get_knowledge_base", "execute_dashboard_query", "analyze_product", "compare_sales", "forged"} {
		r, err := exec(context.Background(), llm.ToolCall{Name: name})
		if err != nil || r == nil || !r.IsError {
			t.Errorf("callback bypass for %s", name)
		}
	}
	if calls != 0 {
		t.Fatal("strict guard bypassed by optional callback")
	}
}

// Embedding the interface makes every unexpected provider operation fail;
// only this fake tool round-trip exists, without any real model connection.
type generationMaliciousClient struct {
	llm.Client
	t *testing.T
}

func (f generationMaliciousClient) GenerateWithTools(ctx context.Context, _ []*models.LLMMessage, tools []llm.ToolDefinition, executor llm.ToolExecutorFunc, _ llm.ToolCallConfig) (*llm.ToolCallResult, error) {
	if len(tools) != 2 || tools[0].Name != "get_table_info" || tools[1].Name != "generate_final_response" {
		f.t.Fatal("provider received a data tool")
	}
	for _, name := range []string{"execute_read_query", "execute_dashboard_query", "sample_rows", "count_rows", "explain_query", "analyze_product", "compare_sales", "visualize_sql", "retry_query", "unadvertised_tool"} {
		result, err := executor(ctx, llm.ToolCall{ID: name, Name: name, Arguments: map[string]interface{}{"auto_execute_query": true, "query": "SELECT * FROM public.secret"}})
		if err != nil || result == nil || !result.IsError {
			f.t.Fatalf("malicious provider executed %s", name)
		}
	}
	result, err := executor(ctx, llm.ToolCall{Name: "get_table_info"})
	if err != nil || result == nil || result.Content != "public.safe_view: amount numeric" {
		f.t.Fatal("safe metadata unavailable")
	}
	return &llm.ToolCallResult{Response: `{"assistantMessage":"SQL generated, not executed","queries":[{"query":"SELECT amount FROM public.safe_view"}]}`}, nil
}
func TestGenerateOnlyDashboardMaliciousProviderHasZeroDataCalls(t *testing.T) {
	dataCalls := 0
	loader := func(context.Context, *models.Chat) (*DashboardProjection, error) {
		metadata := func(context.Context, llm.ToolCall) (*llm.ToolResult, error) {
			return &llm.ToolResult{Content: "public.safe_view: amount numeric"}, nil
		}
		return &DashboardProjection{Context: "only the current safe view", GetTableInfo: metadata, GetKnowledgeBase: metadata}, nil
	}
	tools := []llm.ToolDefinition{{Name: "execute_dashboard_query"}, {Name: "get_table_info"}, {Name: "get_knowledge_base"}, {Name: "generate_final_response"}}
	prepared, err := prepareDashboardGeneration(context.Background(), &models.Chat{}, NewGenerationPolicy(false), true, loader, tools, func(context.Context, llm.ToolCall) (*llm.ToolResult, error) {
		dataCalls++
		return &llm.ToolResult{Content: "business rows"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (generationMaliciousClient{t: t}).GenerateWithTools(context.Background(), nil, prepared.Tools, prepared.Executor, llm.ToolCallConfig{})
	if err != nil || result.Response == "" || dataCalls != 0 {
		t.Fatalf("strict generation result=%+v err=%v data calls=%d", result, err, dataCalls)
	}
}

type generationContextProviderFunc func(context.Context, string, string, map[string]interface{}) (string, error)

func (f generationContextProviderFunc) QueryObjectStructure(ctx context.Context, userID, chatID string, args map[string]interface{}) (string, error) {
	return f(ctx, userID, chatID, args)
}
func TestObjectContextDashboardProviderReloadsAndKeepsServerIdentity(t *testing.T) {
	chat := &models.Chat{}
	chat.UserID = primitive.ObjectID{1}
	chat.ID = primitive.ObjectID{2}
	calls := 0
	revision := "current safe structure and KB"
	var receivedArgs []map[string]interface{}
	provider := generationContextProviderFunc(func(_ context.Context, userID, chatID string, args map[string]interface{}) (string, error) {
		calls++
		if userID != "010000000000000000000000" || chatID != "020000000000000000000000" {
			t.Fatal("model or mutable chat changed server identity")
		}
		receivedArgs = append(receivedArgs, args)
		return revision, nil
	})
	projection, err := NewDashboardProjectionLoader(provider)(context.Background(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Context != "current safe structure and KB" || calls != 1 || receivedArgs[0] != nil {
		t.Fatal("initial context was not current all-safe projection")
	}
	chat.UserID = primitive.ObjectID{3}
	chat.ID = primitive.ObjectID{4}
	revision = "after column revocation"
	result, err := projection.GetTableInfo(context.Background(), llm.ToolCall{ID: "table", Name: "get_table_info", Arguments: map[string]interface{}{"object_refs": []interface{}{map[string]interface{}{"schema": "safe", "name": "view", "kind": "view"}}, "user_id": "forged"}})
	if err != nil || result == nil || result.Content != revision || result.CallID != "table" || receivedArgs[1]["object_refs"] == nil {
		t.Fatal("table callback did not reload current projection with original arguments")
	}
	result, err = projection.GetKnowledgeBase(context.Background(), llm.ToolCall{Name: "get_knowledge_base", Arguments: map[string]interface{}{"user_id": "forged", "query": "SELECT * FROM secret"}})
	if err != nil || result == nil || result.Content != revision || receivedArgs[2] != nil {
		t.Fatal("KB callback did not reload safe all projection")
	}
	_, err = projection.GetTableInfo(context.Background(), llm.ToolCall{Name: "get_table_info"})
	if err != nil || receivedArgs[3] == nil || calls != 4 {
		t.Fatal("nil table args must be validated as a tool request, not expanded to all objects")
	}
}
func TestObjectContextDashboardProviderPreservesFailure(t *testing.T) {
	denied := errors.New("owner/scope rejected")
	if _, err := NewDashboardProjectionLoader(nil)(context.Background(), &models.Chat{}); err == nil {
		t.Fatal("nil provider accepted")
	}
	provider := generationContextProviderFunc(func(context.Context, string, string, map[string]interface{}) (string, error) { return "", denied })
	if _, err := NewDashboardProjectionLoader(provider)(context.Background(), &models.Chat{}); !errors.Is(err, denied) {
		t.Fatalf("initial projection denial lost: %v", err)
	}
	calls := 0
	provider = generationContextProviderFunc(func(context.Context, string, string, map[string]interface{}) (string, error) {
		calls++
		if calls > 1 {
			return "", denied
		}
		return "initial safe", nil
	})
	projection, err := NewDashboardProjectionLoader(provider)(context.Background(), &models.Chat{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = projection.GetTableInfo(context.Background(), llm.ToolCall{Name: "get_table_info"}); !errors.Is(err, denied) {
		t.Fatal("tool scope revocation was swallowed")
	}
}
