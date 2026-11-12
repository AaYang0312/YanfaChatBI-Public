package services

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/llm"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestAssistantBusinessTools(t *testing.T) {
	assertAssistantBusinessToolSchemas(t)

	userID := primitive.NewObjectID()
	chatID := primitive.NewObjectID()
	chats := &assistantServiceChatLookup{chat: &models.Chat{
		UserID: userID,
		Base:   models.Base{ID: chatID},
	}}
	source := &assistantServiceCountingSource{inner: NewAssistantFixtureSource()}
	cipher := assistantServiceCipher(t)
	service := NewAssistantService(
		chats,
		&assistantToolMessages{},
		repositories.NewAssistantRunRepository(newAssistantHTTPRunCollection(), cipher),
		cipher,
		NewAssistantFixtureCatalog(),
		source,
		nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantClock(AssistantFixtureClock),
	)

	executor := BuildToolExecutor(nil, userID.Hex(), chatID.Hex(), "postgresql", service)
	ctx := context.Background()
	readsAtStart := source.calls()
	injected, err := executor(ctx, llm.ToolCall{
		ID:   "call-owner-arg",
		Name: llm.AnalyzeProductToolName,
		Arguments: map[string]interface{}{
			"request_id":    "req-owner-arg",
			"product_query": AssistantFixtureProduct,
			"owner":         userID.Hex(),
			"chat_id":       chatID.Hex(),
		},
	})
	if err != nil {
		t.Fatalf("owner argument call: %v", err)
	}
	injectedBody := decodeAssistantToolContent(t, injected)
	if !injected.IsError || injectedBody.RunID != "" {
		t.Fatalf("owner argument = %#v, want rejected without a run", injectedBody)
	}
	if source.calls() != readsAtStart {
		t.Fatalf("owner argument reads = %d, want %d", source.calls(), readsAtStart)
	}

	product, err := executor(ctx, llm.ToolCall{
		ID:   "call-product",
		Name: llm.AnalyzeProductToolName,
		Arguments: map[string]interface{}{
			"request_id":    "req-analyze-product",
			"product_query": AssistantFixtureProduct,
		},
	})
	if err != nil {
		t.Fatalf("analyze_product: %v", err)
	}
	compare, err := executor(ctx, llm.ToolCall{
		ID:   "call-compare",
		Name: llm.CompareSalesToolName,
		Arguments: map[string]interface{}{
			"request_id": "req-compare-sales",
			"group_by":   "platform",
		},
	})
	if err != nil {
		t.Fatalf("compare_sales: %v", err)
	}

	productBody := decodeAssistantToolContent(t, product)
	compareBody := decodeAssistantToolContent(t, compare)
	if product.IsError || compare.IsError {
		t.Fatalf("tools returned errors: product=%s compare=%s", product.Content, compare.Content)
	}
	if productBody.RunID == "" || compareBody.RunID == "" || productBody.RunID == compareBody.RunID {
		t.Fatalf("run ids = %q %q, want two distinct runs", productBody.RunID, compareBody.RunID)
	}
	if productBody.MessageID == "" || compareBody.MessageID == "" {
		t.Fatalf("message ids missing: %#v %#v", productBody, compareBody)
	}
	if productBody.Status != string(models.RunStatusCompleted) || compareBody.Status != string(models.RunStatusCompleted) {
		t.Fatalf("status = %q %q, want completed", productBody.Status, compareBody.Status)
	}
	assertAssistantToolTextBounded(t, product.Content)
	assertAssistantToolTextBounded(t, compare.Content)

	assertSharedFixtureTotals(t, productBody.Summary, compareBody.Summary)

	readsBeforeMismatch := source.calls()
	otherOwner := BuildToolExecutor(nil, primitive.NewObjectID().Hex(), chatID.Hex(), "postgresql", service)
	mismatch, err := otherOwner(ctx, llm.ToolCall{
		ID:   "call-mismatch",
		Name: llm.AnalyzeProductToolName,
		Arguments: map[string]interface{}{
			"request_id":    "req-mismatch-owner",
			"product_query": AssistantFixtureProduct,
		},
	})
	if err != nil {
		t.Fatalf("mismatched owner call: %v", err)
	}
	mismatchBody := decodeAssistantToolContent(t, mismatch)
	if !mismatch.IsError || mismatchBody.Status != "not_found" || mismatchBody.Code != "404" {
		t.Fatalf("mismatch = error:%v status:%s code:%s, want 404", mismatch.IsError, mismatchBody.Status, mismatchBody.Code)
	}
	if mismatchBody.RunID != "" {
		t.Fatalf("mismatch created run %s", mismatchBody.RunID)
	}
	if source.calls() != readsBeforeMismatch {
		t.Fatalf("mismatched owner reads = %d, started at %d", source.calls(), readsBeforeMismatch)
	}
}

// TestAssistantToolReadsPersistedReport pins the AC10/AC24 read path: the tool
// summary must equal the HTTP-loaded persisted report, so a persistence defect
// can no longer hide behind a snapshot recomputation.
func TestAssistantToolReadsPersistedReport(t *testing.T) {
	userID := primitive.NewObjectID()
	chatID := primitive.NewObjectID()
	chats := &assistantServiceChatLookup{chat: &models.Chat{
		UserID: userID,
		Base:   models.Base{ID: chatID},
	}}
	source := &assistantServiceCountingSource{inner: NewAssistantFixtureSource()}
	cipher := assistantServiceCipher(t)
	service := NewAssistantService(
		chats,
		&assistantToolMessages{},
		repositories.NewAssistantRunRepository(newAssistantHTTPRunCollection(), cipher),
		cipher,
		NewAssistantFixtureCatalog(),
		source,
		nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantClock(AssistantFixtureClock),
	)

	executor := BuildToolExecutor(nil, userID.Hex(), chatID.Hex(), "postgresql", service)
	result, err := executor(context.Background(), llm.ToolCall{
		ID:   "call-persisted-report",
		Name: llm.AnalyzeProductToolName,
		Arguments: map[string]interface{}{
			"request_id":    "req-tool-persisted-report",
			"product_query": AssistantFixtureProduct,
		},
	})
	if err != nil {
		t.Fatalf("analyze_product: %v", err)
	}
	body := decodeAssistantToolContent(t, result)
	if body.RunID == "" || body.Status != string(models.RunStatusCompleted) {
		t.Fatalf("tool result = %#v, want a completed run", body)
	}

	access := models.AccessContext{OwnerID: userID.Hex(), ChatID: chatID.Hex()}
	view, err := service.GetRun(context.Background(), access, body.RunID)
	if err != nil {
		t.Fatalf("get persisted run: %v", err)
	}
	if view.Report == nil {
		t.Fatal("persisted run has no decrypted report")
	}
	want := map[models.MetricID]string{
		models.MetricQuantity:     "3",
		models.MetricSales:        "120",
		models.MetricCost:         "72",
		models.MetricMargin:       "48",
		models.MetricMarginRate:   "0.4",
		models.MetricAveragePrice: "40",
	}
	for id, value := range want {
		total := regressionMetric(view.Report.Totals, id)
		if total.Value == nil || total.Value.String() != value {
			t.Fatalf("persisted %s = %v, want %s", id, total.Value, value)
		}
		if !strings.Contains(body.Summary, string(id)+"="+value) {
			t.Fatalf("summary %q does not carry persisted %s=%s", body.Summary, id, value)
		}
	}
	for name, bucket := range map[string][]models.CoverageEntity{
		"missing":  view.Report.Coverage.Missing,
		"stale":    view.Report.Coverage.Stale,
		"unknown":  view.Report.Coverage.Unknown,
		"excluded": view.Report.Coverage.Excluded,
	} {
		if bucket == nil {
			t.Fatalf("persisted coverage %s bucket is null; the report json boundary must emit arrays", name)
		}
	}
}

func decodeAssistantToolContent(t *testing.T, result *llm.ToolResult) assistantToolContent {
	t.Helper()
	if result == nil {
		t.Fatal("nil tool result")
	}
	var body assistantToolContent
	if err := json.Unmarshal([]byte(result.Content), &body); err != nil {
		t.Fatalf("decode tool content %s: %v", result.Content, err)
	}
	return body
}

func assertAssistantToolTextBounded(t *testing.T, content string) {
	t.Helper()
	var body assistantToolContent
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		t.Fatalf("decode bounded content: %v", err)
	}
	if len(body.Summary) > constants.AssistantToolSummaryLimit {
		t.Fatalf("summary length = %d, limit %d", len(body.Summary), constants.AssistantToolSummaryLimit)
	}
	lower := strings.ToLower(content)
	for _, forbidden := range []string{"\"queries\"", "chart_id", "points", "business_date", "window_start"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("tool content contains %s: %s", forbidden, content)
		}
	}
}

func assertSharedFixtureTotals(t *testing.T, product, compare string) {
	t.Helper()
	want := []string{
		"quantity=3",
		"sales=120",
		"cost=72",
		"margin=48",
		"margin_rate=0.4",
		"avg_price=40",
	}
	for _, item := range want {
		if !strings.Contains(product, item) || !strings.Contains(compare, item) {
			t.Fatalf("shared total %s missing\nproduct %s\ncompare %s", item, product, compare)
		}
	}
}

func assertAssistantBusinessToolSchemas(t *testing.T) {
	t.Helper()
	tools := llm.GetAssistantBusinessTools()
	if len(tools) != 2 {
		t.Fatalf("business tools = %d, want 2", len(tools))
	}

	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Name != llm.AnalyzeProductToolName && tool.Name != llm.CompareSalesToolName {
			t.Fatalf("unexpected business tool %q", tool.Name)
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate business tool %q", tool.Name)
		}
		seen[tool.Name] = true
		assertAssistantToolSchema(t, tool)
	}
	if !seen[llm.AnalyzeProductToolName] || !seen[llm.CompareSalesToolName] {
		t.Fatalf("business tools = %v, want analyze_product and compare_sales", seen)
	}

	for _, tool := range llm.GetChatTools() {
		if tool.Name == llm.AnalyzeProductToolName || tool.Name == llm.CompareSalesToolName {
			t.Fatalf("%s must be appended by the chat executor, not GetChatTools", tool.Name)
		}
	}
}

func assertAssistantToolSchema(t *testing.T, tool llm.ToolDefinition) {
	t.Helper()
	body, err := json.Marshal(tool.Parameters)
	if err != nil {
		t.Fatalf("%s marshal schema: %v", tool.Name, err)
	}
	lower := strings.ToLower(string(body))
	for _, key := range []string{
		`"owner"`, `"owner_id"`, `"user_id"`, `"userid"`,
		`"chat"`, `"chat_id"`, `"chatid"`,
		`"sql"`, `"query"`, `"schema"`, `"schema_name"`, `"credentials"`, `"password"`,
	} {
		if strings.Contains(lower, key) {
			t.Fatalf("%s schema exposes %s: %s", tool.Name, key, body)
		}
	}
	schema := tool.Parameters
	if schema == nil {
		t.Fatalf("%s parameters missing", tool.Name)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("%s additionalProperties = %#v, want false", tool.Name, schema["additionalProperties"])
	}
	required, _ := schema["required"].([]interface{})
	if len(required) != 1 || required[0] != "request_id" {
		t.Fatalf("%s required = %#v, want [request_id]", tool.Name, required)
	}
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s properties missing", tool.Name)
	}
	if _, ok := properties["request_id"]; !ok {
		t.Fatalf("%s missing request_id", tool.Name)
	}
	if _, ok := properties["product_query"]; !ok {
		t.Fatalf("%s missing product_query", tool.Name)
	}
}

type assistantToolMessages struct {
	rows []*models.Message
}

func (m *assistantToolMessages) InsertReportMessage(_ context.Context, message *models.Message) error {
	copied := *message
	if message.AssistantRunID != nil {
		runID := *message.AssistantRunID
		copied.AssistantRunID = &runID
	}
	m.rows = append(m.rows, &copied)
	return nil
}

func (m *assistantToolMessages) FindReportMessage(_ context.Context, messageID string) (*models.Message, bool, error) {
	for _, message := range m.rows {
		if message.ID.Hex() == messageID {
			return message, true, nil
		}
	}
	return nil, false, nil
}
