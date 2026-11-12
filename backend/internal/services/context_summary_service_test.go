package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/schemaobjects"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// contextSummaryFakeGenerator is the narrow seam T03 consumes: only
// GenerateRawJSON is used, so tests do not need a full llm.Client.
type contextSummaryFakeGenerator struct {
	response     string
	err          error
	calls        int
	systemPrompt string
	userMessage  string
	modelID      string
	deadline     time.Time
	hasDeadline  bool
}

func (f *contextSummaryFakeGenerator) GenerateRawJSON(ctx context.Context, systemPrompt, userMessage string, modelID ...string) (string, error) {
	f.calls++
	f.systemPrompt = systemPrompt
	f.userMessage = userMessage
	if len(modelID) > 0 {
		f.modelID = modelID[0]
	}
	f.deadline, f.hasDeadline = ctx.Deadline()
	return f.response, f.err
}

func contextSummaryTestChat() *models.Chat {
	chat := models.NewChat(primitive.NewObjectID(), models.Connection{Database: "bi_agent"}, models.DefaultChatSettings())
	chat.ID = primitive.NewObjectID()
	chat.SourceID = "source-1"
	chat.SourceBindingRevision = 7
	chat.SelectionRevision = 5
	chat.SelectedObjects = &schemaobjects.Selection{
		Version: 2,
		Mode:    "v2",
		Objects: []schemaobjects.Ref{
			{Schema: "reporting", Name: "sales", Kind: schemaobjects.View},
			{Schema: "bi", Name: "orders", Kind: schemaobjects.Table},
		},
	}
	return chat
}

func contextSummaryTestMessages(chatID primitive.ObjectID) []*models.Message {
	firstAt := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(40 * time.Minute)
	first := &models.Message{Type: "user", ChatID: chatID, Content: "帮我分析最近30天各店铺销量趋势"}
	first.ID = primitive.NewObjectID()
	first.CreatedAt = firstAt
	second := &models.Message{
		Type:    "assistant",
		ChatID:  chatID,
		Content: `{"response":"已生成查询","query":[{"description":"近30天各店销量","query":"SELECT secret FROM bi.orders","query_type":"SELECT","tables":"bi.orders","is_executed":true}]}`,
	}
	second.ID = primitive.NewObjectID()
	second.CreatedAt = secondAt
	return []*models.Message{first, second}
}

func withContextSummaryMaxTokens(t *testing.T, value int) {
	t.Helper()
	previous := config.Env.ContextSummaryMaxTokens
	config.Env.ContextSummaryMaxTokens = value
	t.Cleanup(func() { config.Env.ContextSummaryMaxTokens = previous })
}

func assertContainsAll(t *testing.T, label, text string, want []string) {
	t.Helper()
	for _, fragment := range want {
		if !strings.Contains(text, fragment) {
			t.Errorf("%s missing %q", label, fragment)
		}
	}
}

func TestGenerateContextSummarySuccess(t *testing.T) {
	withContextSummaryMaxTokens(t, 100000)
	chat := contextSummaryTestChat()
	messages := contextSummaryTestMessages(chat.ID)

	response := `{
		"goal": "分析最近30天各店铺销量趋势",
		"data_source": {"source_id": "spoofed", "binding_revision": 1, "database": "spoofed_db"},
		"schema_facts": [
			{"object": "reporting.sales", "kind": "view", "key_columns": ["shop_id", "amount"], "notes": "按店铺汇总"}
		],
		"queries": [
			{"purpose": "近30天各店销量", "tables": ["bi.orders"], "sql_digest": "a1b2c3", "row_count": 6, "key_numbers": ["合计 126 件"], "status": "executed"}
		],
		"errors": [
			{"what": "json_schema unsupported", "cause": "provider", "fix": "drop response_format", "resolved": true}
		],
		"user_preferences": {"execution_mode": "normal", "non_tech_mode": false, "language": "zh-CN"},
		"decisions": ["口径按 paid_at 计算"],
		"open_questions": ["是否需要按商品维度拆分"],
		"provenance": {"message_ids": ["000000000000000000000000"], "model": "spoofed"}
	}`
	gen := &contextSummaryFakeGenerator{response: "```json\n" + response + "\n```"}

	got, err := GenerateContextSummary(context.Background(), gen, chat, nil, messages, "compact/model")
	if err != nil {
		t.Fatalf("GenerateContextSummary: %v", err)
	}
	if got == nil {
		t.Fatal("nil summary")
	}
	if gen.calls != 1 {
		t.Fatalf("generator calls = %d, want 1", gen.calls)
	}
	if gen.modelID != "compact/model" {
		t.Fatalf("model passed to generator = %q", gen.modelID)
	}
	if got.ChatID != chat.ID {
		t.Fatalf("chat id = %v, want %v", got.ChatID, chat.ID)
	}
	if got.Revision != 1 {
		t.Fatalf("revision = %d, want 1", got.Revision)
	}
	if got.Summary.Goal != "分析最近30天各店铺销量趋势" {
		t.Fatalf("goal = %q", got.Summary.Goal)
	}
	if got.StaleSource {
		t.Fatal("fresh summary must not be stale")
	}
	if got.Model != "compact/model" {
		t.Fatalf("model = %q", got.Model)
	}
	if len(got.CoveredMessageIDs) != 2 || got.CoveredMessageIDs[0] != messages[0].ID || got.CoveredMessageIDs[1] != messages[1].ID {
		t.Fatalf("covered ids = %v", got.CoveredMessageIDs)
	}
	if got.CoveredUntil == nil || !got.CoveredUntil.Equal(messages[len(messages)-1].CreatedAt) {
		t.Fatalf("covered until = %v, want %v", got.CoveredUntil, messages[len(messages)-1].CreatedAt)
	}
	if got.Summary.Provenance.CoveredCount != 2 || len(got.Summary.Provenance.MessageIDs) != 2 {
		t.Fatalf("provenance = %+v", got.Summary.Provenance)
	}
	if got.Summary.Provenance.Model != "compact/model" {
		t.Fatalf("provenance model = %q", got.Summary.Provenance.Model)
	}
	if len(got.Summary.Provenance.Range) != 2 ||
		got.Summary.Provenance.Range[0] != "2026-11-12T03:00:00Z" ||
		got.Summary.Provenance.Range[1] != "2026-11-12T03:40:00Z" {
		t.Fatalf("provenance range = %v", got.Summary.Provenance.Range)
	}
	// Source identity comes from the chat, never from model output.
	if got.Summary.DataSource.ChatID != chat.ID ||
		got.Summary.DataSource.SourceID != chat.SourceID ||
		got.Summary.DataSource.BindingRevision != chat.SourceBindingRevision ||
		got.Summary.DataSource.Database != "bi_agent" {
		t.Fatalf("data source = %+v", got.Summary.DataSource)
	}
	if got.Summary.DataSource.Selection == nil ||
		got.Summary.DataSource.Selection.Revision != chat.SelectionRevision ||
		len(got.Summary.DataSource.Selection.Objects) != 2 ||
		got.Summary.DataSource.Selection.Objects[0] != "reporting.sales" {
		t.Fatalf("selection = %+v", got.Summary.DataSource.Selection)
	}
	if got.TokenEstimate != llm.EstimateTokens(got.Rendered) {
		t.Fatalf("token estimate = %d, want %d", got.TokenEstimate, llm.EstimateTokens(got.Rendered))
	}
	assertContainsAll(t, "rendered", got.Rendered, []string{
		"目标", "数据源", "已确认结构", "已执行查询", "错误与修复", "用户偏好", "决策", "未决问题",
		"get_table_info", "可能过期", "a1b2c3",
	})
	assertContainsAll(t, "system prompt", gen.systemPrompt, []string{
		"JSON", "SQL", "结果数据行", "凭据", "schema_facts", "get_table_info", "goal", "open_questions",
	})
	if !strings.Contains(gen.userMessage, "帮我分析最近30天各店铺销量趋势") {
		t.Fatalf("user message missing folded message content: %s", gen.userMessage)
	}
	if strings.Contains(gen.userMessage, "SELECT secret") {
		t.Fatal("user message must not carry the full SQL body")
	}
}

func TestGenerateContextSummaryRevisionAdvancesAndMergesPrevious(t *testing.T) {
	withContextSummaryMaxTokens(t, 100000)
	chat := contextSummaryTestChat()
	messages := contextSummaryTestMessages(chat.ID)

	previous := &models.ChatContextSummary{
		ChatID:   chat.ID,
		Revision: 3,
		Summary: models.ContextSummary{
			Goal:            "旧目标：按店铺看销量",
			SchemaFacts:     []models.ContextSummarySchemaFact{{Object: "reporting.sales", Kind: "view", Notes: "旧的结构事实"}},
			OpenQuestions:   []string{"旧问题1", "旧问题2"},
			Decisions:       []string{"旧决策"},
			UserPreferences: models.ContextSummaryPreferences{ExecutionMode: "strict", Language: "zh-CN"},
			Provenance:      models.ContextSummaryProvenance{CoveredCount: 10},
		},
	}
	response := `{
		"goal": "",
		"schema_facts": [{"object": "bi.orders", "kind": "table", "notes": "新事实"}],
		"queries": [],
		"errors": [],
		"user_preferences": {"language": "en"},
		"decisions": ["新决策"],
		"open_questions": ["新问题"]
	}`
	gen := &contextSummaryFakeGenerator{response: response}

	got, err := GenerateContextSummary(context.Background(), gen, chat, previous, messages, "m")
	if err != nil {
		t.Fatalf("GenerateContextSummary: %v", err)
	}
	if got.Revision != 4 {
		t.Fatalf("revision = %d, want 4", got.Revision)
	}
	if got.Summary.Goal != "旧目标：按店铺看销量" {
		t.Fatalf("goal not preserved: %q", got.Summary.Goal)
	}
	questions := strings.Join(got.Summary.OpenQuestions, "|")
	for _, want := range []string{"旧问题1", "旧问题2", "新问题"} {
		if !strings.Contains(questions, want) {
			t.Fatalf("open questions %q missing %q", questions, want)
		}
	}
	decisions := strings.Join(got.Summary.Decisions, "|")
	for _, want := range []string{"旧决策", "新决策"} {
		if !strings.Contains(decisions, want) {
			t.Fatalf("decisions %q missing %q", decisions, want)
		}
	}
	if got.Summary.UserPreferences.ExecutionMode != "strict" {
		t.Fatalf("execution mode lost: %+v", got.Summary.UserPreferences)
	}
	if got.Summary.UserPreferences.Language != "en" {
		t.Fatalf("language update lost: %+v", got.Summary.UserPreferences)
	}
	objects := make([]string, 0, len(got.Summary.SchemaFacts))
	for _, fact := range got.Summary.SchemaFacts {
		objects = append(objects, fact.Object)
	}
	if !strings.Contains(strings.Join(objects, "|"), "reporting.sales") || !strings.Contains(strings.Join(objects, "|"), "bi.orders") {
		t.Fatalf("schema facts not merged: %v", objects)
	}
	if previous.Revision != 3 || len(previous.Summary.OpenQuestions) != 2 || len(previous.Summary.SchemaFacts) != 1 {
		t.Fatalf("previous summary mutated: %+v", previous.Summary)
	}
}

func TestGenerateContextSummaryRejectsInvalidJSON(t *testing.T) {
	withContextSummaryMaxTokens(t, 100000)
	chat := contextSummaryTestChat()
	gen := &contextSummaryFakeGenerator{response: "sorry, I cannot produce JSON"}

	got, err := GenerateContextSummary(context.Background(), gen, chat, nil, contextSummaryTestMessages(chat.ID), "m")
	if err == nil {
		t.Fatalf("expected error, got summary %+v", got)
	}
	if got != nil {
		t.Fatalf("record returned on parse failure: %+v", got)
	}
}

func TestGenerateContextSummaryEmptyMessages(t *testing.T) {
	withContextSummaryMaxTokens(t, 100000)
	chat := contextSummaryTestChat()
	gen := &contextSummaryFakeGenerator{response: `{"goal":"x"}`}

	for _, messages := range [][]*models.Message{nil, {}} {
		got, err := GenerateContextSummary(context.Background(), gen, chat, nil, messages, "m")
		if err == nil {
			t.Fatalf("expected error for %d messages, got %+v", len(messages), got)
		}
	}
	if gen.calls != 0 {
		t.Fatalf("generator called %d times for empty input", gen.calls)
	}
}

func TestGenerateContextSummaryGeneratorFailure(t *testing.T) {
	withContextSummaryMaxTokens(t, 100000)
	chat := contextSummaryTestChat()
	boom := errors.New("provider timeout")
	gen := &contextSummaryFakeGenerator{err: boom}

	got, err := GenerateContextSummary(context.Background(), gen, chat, nil, contextSummaryTestMessages(chat.ID), "m")
	if err == nil {
		t.Fatal("expected generator error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error %v does not wrap %v", err, boom)
	}
	if got != nil {
		t.Fatalf("record returned on generator failure: %+v", got)
	}
}

func TestGenerateContextSummaryAppliesDeadline(t *testing.T) {
	withContextSummaryMaxTokens(t, 100000)
	chat := contextSummaryTestChat()
	gen := &contextSummaryFakeGenerator{response: `{"goal":"x"}`}

	if _, err := GenerateContextSummary(context.Background(), gen, chat, nil, contextSummaryTestMessages(chat.ID), "m"); err != nil {
		t.Fatalf("GenerateContextSummary: %v", err)
	}
	if !gen.hasDeadline {
		t.Fatal("summary call ran without a deadline")
	}
	if remaining := time.Until(gen.deadline); remaining <= 0 || remaining > ContextSummaryTimeout {
		t.Fatalf("deadline remaining = %v, want (0, %v]", remaining, ContextSummaryTimeout)
	}
}

func TestGenerateContextSummaryCapsFieldsAndArrays(t *testing.T) {
	withContextSummaryMaxTokens(t, 1000000)
	chat := contextSummaryTestChat()

	schemaFacts := make([]map[string]any, 0, 60)
	for i := 0; i < 60; i++ {
		schemaFacts = append(schemaFacts, map[string]any{
			"object": fmt.Sprintf("t%02d", i),
			"kind":   "table",
			"notes":  strings.Repeat("n", 3000),
		})
	}
	queries := make([]map[string]any, 0, 60)
	for i := 0; i < 60; i++ {
		queries = append(queries, map[string]any{"purpose": fmt.Sprintf("q%02d", i), "status": "executed"})
	}
	errorsList := make([]map[string]any, 0, 60)
	for i := 0; i < 60; i++ {
		errorsList = append(errorsList, map[string]any{"what": fmt.Sprintf("e%02d", i)})
	}
	payload, err := json.Marshal(map[string]any{
		"goal":         strings.Repeat("g", 3000),
		"schema_facts": schemaFacts,
		"queries":      queries,
		"errors":       errorsList,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	gen := &contextSummaryFakeGenerator{response: string(payload)}

	got, err := GenerateContextSummary(context.Background(), gen, chat, nil, contextSummaryTestMessages(chat.ID), "m")
	if err != nil {
		t.Fatalf("GenerateContextSummary: %v", err)
	}
	if n := utf8.RuneCountInString(got.Summary.Goal); n != ContextSummaryGoalMaxChars {
		t.Fatalf("goal length = %d, want %d", n, ContextSummaryGoalMaxChars)
	}
	if len(got.Summary.SchemaFacts) != ContextSummaryMaxSchemaFacts {
		t.Fatalf("schema facts = %d, want %d", len(got.Summary.SchemaFacts), ContextSummaryMaxSchemaFacts)
	}
	if len(got.Summary.Queries) != ContextSummaryMaxQueries {
		t.Fatalf("queries = %d, want %d", len(got.Summary.Queries), ContextSummaryMaxQueries)
	}
	if len(got.Summary.Errors) != ContextSummaryMaxErrors {
		t.Fatalf("errors = %d, want %d", len(got.Summary.Errors), ContextSummaryMaxErrors)
	}
	if n := utf8.RuneCountInString(got.Summary.SchemaFacts[0].Notes); n != ContextSummaryNotesMaxChars {
		t.Fatalf("notes length = %d, want %d", n, ContextSummaryNotesMaxChars)
	}
}

func TestGenerateContextSummaryCompactsOverBudget(t *testing.T) {
	withContextSummaryMaxTokens(t, 600)
	chat := contextSummaryTestChat()

	schemaFacts := make([]map[string]any, 0, 40)
	for i := 0; i < 40; i++ {
		schemaFacts = append(schemaFacts, map[string]any{
			"object": fmt.Sprintf("reporting.t%02d", i),
			"kind":   "view",
			"notes":  strings.Repeat("x", 1000),
		})
	}
	queries := make([]map[string]any, 0, 10)
	for i := 0; i < 10; i++ {
		queries = append(queries, map[string]any{
			"purpose":     fmt.Sprintf("query-%02d", i),
			"status":      "executed",
			"row_count":   i,
			"key_numbers": []string{strings.Repeat("9", 50)},
		})
	}
	payload, err := json.Marshal(map[string]any{
		"goal":             "分析各店铺销量趋势",
		"schema_facts":     schemaFacts,
		"queries":          queries,
		"open_questions":   []string{"需要按商品拆分吗"},
		"user_preferences": map[string]any{"language": "zh-CN"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	gen := &contextSummaryFakeGenerator{response: string(payload)}

	got, err := GenerateContextSummary(context.Background(), gen, chat, nil, contextSummaryTestMessages(chat.ID), "m")
	if err != nil {
		t.Fatalf("GenerateContextSummary: %v", err)
	}
	if got.TokenEstimate > 600 {
		t.Fatalf("token estimate = %d, want <= 600", got.TokenEstimate)
	}
	if llm.EstimateTokens(got.Rendered) != got.TokenEstimate {
		t.Fatalf("token estimate mismatch: %d vs %d", got.TokenEstimate, llm.EstimateTokens(got.Rendered))
	}
	if len(got.Summary.SchemaFacts) > 0 {
		for _, fact := range got.Summary.SchemaFacts {
			if fact.Notes != "" || len(fact.KeyColumns) > 0 {
				t.Fatalf("schema fact kept detail after compaction: %+v", fact)
			}
		}
	}
	if len(got.Summary.Queries) > 3 {
		t.Fatalf("queries = %d, want <= 3", len(got.Summary.Queries))
	}
	if len(got.Summary.Queries) == 3 {
		if got.Summary.Queries[2].Purpose != "query-09" {
			t.Fatalf("compaction kept wrong queries: %+v", got.Summary.Queries)
		}
	}
}

func TestCompactContextSummaryPrioritizesAndFitsBudget(t *testing.T) {
	summary := models.ContextSummary{
		Goal: "分析各店铺销量趋势",
		SchemaFacts: []models.ContextSummarySchemaFact{
			{Object: "reporting.sales", Kind: "view", KeyColumns: []string{"shop_id"}, Notes: strings.Repeat("旧细节", 200)},
			{Object: "bi.orders", Kind: "table", KeyColumns: []string{"id"}, Notes: strings.Repeat("更多细节", 200)},
		},
		Queries: []models.ContextSummaryQuery{
			{Purpose: "q-old", Status: "executed", KeyNumbers: []string{strings.Repeat("8", 100)}},
			{Purpose: "q-2", Status: "executed"},
			{Purpose: "q-3", Status: "executed"},
			{Purpose: "q-latest", Status: "executed"},
		},
		Errors: []models.ContextSummaryError{
			{What: "resolved-error", Resolved: true},
			{What: "open-error", Resolved: false},
		},
		UserPreferences: models.ContextSummaryPreferences{ExecutionMode: "strict", Language: "zh-CN"},
		Decisions:       []string{"旧决策", "新决策"},
		OpenQuestions:   []string{"是否需要按商品维度拆分"},
	}
	compacted, rendered := CompactContextSummary(summary, 400)

	if tokens := llm.EstimateTokens(rendered); tokens > 400 {
		t.Fatalf("rendered tokens = %d, want <= 400", tokens)
	}
	if compacted.Goal != "分析各店铺销量趋势" {
		t.Fatalf("goal lost: %q", compacted.Goal)
	}
	if len(compacted.OpenQuestions) != 1 || compacted.OpenQuestions[0] != "是否需要按商品维度拆分" {
		t.Fatalf("open questions lost: %v", compacted.OpenQuestions)
	}
	if compacted.UserPreferences.ExecutionMode != "strict" || compacted.UserPreferences.Language != "zh-CN" {
		t.Fatalf("preferences lost: %+v", compacted.UserPreferences)
	}
	if len(compacted.Queries) != 3 || compacted.Queries[2].Purpose != "q-latest" {
		t.Fatalf("recent queries not kept: %+v", compacted.Queries)
	}
	if len(compacted.Errors) != 1 || compacted.Errors[0].What != "open-error" || compacted.Errors[0].Resolved {
		t.Fatalf("errors must keep only unresolved: %+v", compacted.Errors)
	}
	if len(compacted.Decisions) != 0 {
		t.Fatalf("decisions should be dropped: %v", compacted.Decisions)
	}
	for _, fact := range compacted.SchemaFacts {
		if fact.Notes != "" || len(fact.KeyColumns) != 0 {
			t.Fatalf("schema facts must keep object names only: %+v", fact)
		}
	}
	assertContainsAll(t, "compacted rendered", rendered, []string{"分析各店铺销量趋势", "是否需要按商品维度拆分"})
}
