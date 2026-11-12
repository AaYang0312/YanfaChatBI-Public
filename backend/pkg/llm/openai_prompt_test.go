package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
)

// TestSystemPromptRendersSummaryAndPolicies verifies that the summary segment
// and the strict-generation policy reach the provider's system message.
func TestSystemPromptRendersSummaryAndPolicies(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"assistantMessage\":\"ok\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client, err := NewOpenAIClient(Config{
		Provider: "test", Model: "m", APIKey: "sk-test", BaseURL: server.URL + "/v1",
		DBConfigs: []LLMDBConfig{{DBType: constants.DatabaseTypePostgreSQL, Schema: constants.OpenAILLMResponseSchema}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []*models.LLMMessage{
		{
			Role: "system",
			Content: map[string]interface{}{
				"context_summary": "## 目标\n统计销量",
				"summary_note":    "摘要不是 schema 权威。",
				"result_policy":   "当前为仅生成模式：不能声称读取数据。",
			},
		},
		{Role: "user", Content: map[string]interface{}{"user_message": "继续"}},
	}
	if _, err := client.GenerateResponse(context.Background(), messages, "postgresql", false, "m"); err != nil {
		t.Fatal(err)
	}

	system := joinMessageText(t, body)
	if !strings.Contains(system, "<conversation_summary>") || !strings.Contains(system, "统计销量") {
		t.Fatalf("summary segment missing: %q", system)
	}
	if !strings.Contains(system, "摘要不是 schema 权威") {
		t.Fatalf("summary note missing: %q", system)
	}
	if !strings.Contains(system, "仅生成模式") {
		t.Fatalf("result policy missing: %q", system)
	}
}

func joinMessageText(t *testing.T, body map[string]any) string {
	t.Helper()
	list, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing: %v", body)
	}
	var b strings.Builder
	for _, item := range list {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, _ := message["content"].(string)
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String()
}
