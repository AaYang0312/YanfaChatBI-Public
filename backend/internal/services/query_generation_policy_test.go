package services

import (
	"context"
	"yanfachatbi/internal/constants"
	"yanfachatbi/pkg/llm"
	"testing"
)

func TestGenerateOnlyFiltersAllDataTools(t *testing.T) {
	input := append(llm.GetChatTools(), llm.GetAssistantBusinessTools()...)
	input = append(input, llm.ToolDefinition{Name: "execute_dashboard_query"}, llm.ToolDefinition{Name: "get_knowledge_base"}, llm.ToolDefinition{Name: "sample_rows"}, llm.ToolDefinition{Name: "forged_data_tool"})
	filtered := NewGenerationPolicy(false).FilterTools(input)
	if len(filtered) != 2 || filtered[0].Name != "get_table_info" || filtered[1].Name != "generate_final_response" {
		t.Fatalf("strict tools = %v; want only structure and final", filtered)
	}
	if len(input) != 9 {
		t.Fatal("input catalog was changed")
	}
}

func TestGenerateOnlyMaliciousMixedToolCallsNeverReachDataExecutor(t *testing.T) {
	dataCalls, metadataCalls := 0, 0
	executor := NewGenerationPolicy(false).WrapExecutor(func(ctx context.Context, call llm.ToolCall) (*llm.ToolResult, error) {
		if call.Name == "get_table_info" {
			metadataCalls++
		} else {
			dataCalls++
		}
		return &llm.ToolResult{CallID: call.ID, Name: call.Name, Content: "safe metadata"}, nil
	})
	// A fake model ignores the offered list and forges execution/mode arguments.
	calls := []string{"execute_read_query", "analyze_product", "compare_sales", "execute_dashboard_query", "count_rows", "explain_query", "sample_rows", "visualize_sql", "retry_query", "get_knowledge_base", "GET_TABLE_INFO", "get_table_info ", "default_api.execute_read_query", "unknown_data_reader"}
	for _, name := range calls {
		result, err := executor(context.Background(), llm.ToolCall{ID: "forged", Name: name, Arguments: map[string]interface{}{"query": "SELECT * FROM secret", "auto_execute_query": true, "mode": "execute"}})
		if err != nil || result == nil || !result.IsError || result.CallID != "forged" {
			t.Errorf("%s not safely denied: result=%+v error=%v", name, result, err)
		}
	}
	result, err := executor(context.Background(), llm.ToolCall{ID: "schema", Name: "get_table_info"})
	if err != nil || result.IsError || metadataCalls != 1 {
		t.Errorf("metadata unavailable: %+v %v count=%d", result, err, metadataCalls)
	}
	final, err := executor(context.Background(), llm.ToolCall{ID: "final", Name: "generate_final_response", Arguments: map[string]interface{}{"queries": []interface{}{map[string]interface{}{"query": "SELECT 1"}}}})
	if err != nil || final == nil || final.IsError {
		t.Errorf("final SQL unavailable: %+v %v", final, err)
	}
	if dataCalls != 0 {
		t.Fatalf("business executor called %d times; want zero including final dispatch", dataCalls)
	}
}

func TestObjectExecutionModeRetainsExplicitServerPermission(t *testing.T) {
	setting := false
	strict := NewGenerationPolicy(setting)
	setting = true
	if strict.AllowsAutomaticDataExecution() || (GenerationPolicy{}).AllowsAutomaticDataExecution() {
		t.Fatal("policy not frozen or zero value permits execution")
	}
	allowed := NewGenerationPolicy(setting)
	calls := 0
	_, err := allowed.WrapExecutor(func(_ context.Context, c llm.ToolCall) (*llm.ToolResult, error) {
		calls++
		return &llm.ToolResult{Content: "synthetic rows"}, nil
	})(context.Background(), llm.ToolCall{Name: "execute_read_query"})
	if err != nil || calls != 1 {
		t.Fatal("server-authorized execution lost")
	}
	if got := allowed.FilterTools(llm.GetChatTools()); len(got) != 3 {
		t.Fatal("execution tool removed in allowed mode")
	}
}

func TestObjectTableInfoSchemaSupportsQualifiedReferences(t *testing.T) {
	schema := constants.GetTableInfoToolSchema
	props := schema["properties"].(map[string]interface{})
	refs, ok := props["object_refs"].(map[string]interface{})
	if !ok {
		t.Fatal("object_refs structured array missing")
	}
	if refs["type"] != "array" {
		t.Fatal("object_refs must be array")
	}
	item := refs["items"].(map[string]interface{})
	fields := item["properties"].(map[string]interface{})
	for _, key := range []string{"schema", "name", "kind"} {
		if fields[key] == nil {
			t.Errorf("missing %s", key)
		}
	}
	if props["table_names"] == nil {
		t.Fatal("legacy names removed")
	}
	if required, ok := schema["required"].([]interface{}); ok && len(required) > 0 {
		t.Fatal("one argument form incorrectly always required")
	}
}

func TestGenerateOnlyCanceledCallDoesNotRunMetadata(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewGenerationPolicy(false).WrapExecutor(func(context.Context, llm.ToolCall) (*llm.ToolResult, error) { calls++; return nil, nil })(ctx, llm.ToolCall{Name: "get_table_info"})
	if err != context.Canceled || calls != 0 {
		t.Fatalf("canceled call ran metadata: %v %d", err, calls)
	}
}
func TestGenerateOnlyAmbiguousOptionsCannotRelaxPolicy(t *testing.T) {
	calls := 0
	legacy := func(context.Context, llm.ToolCall) (*llm.ToolResult, error) { calls++; return &llm.ToolResult{}, nil }
	executor := applyGenerationOptions(legacy, []GenerationToolOptions{{Policy: NewGenerationPolicy(false)}, {Policy: NewGenerationPolicy(true)}})
	result, err := executor(context.Background(), llm.ToolCall{Name: "execute_read_query"})
	if err != nil || result == nil || !result.IsError || calls != 0 {
		t.Fatal("multiple options relaxed strict policy")
	}
	_, err = applyGenerationOptions(legacy, nil)(context.Background(), llm.ToolCall{Name: "execute_read_query"})
	if err != nil || calls != 1 {
		t.Fatal("legacy interface compatibility lost")
	}
}
