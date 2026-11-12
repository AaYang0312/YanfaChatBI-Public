package services

import (
	"context"
	"encoding/json"
	"errors"
	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type objectMessageModelFake struct {
	llm.Client
	captured []*models.LLMMessage
}

func (f *objectMessageModelFake) GenerateWithTools(_ context.Context, m []*models.LLMMessage, _ []llm.ToolDefinition, _ llm.ToolExecutorFunc, _ llm.ToolCallConfig) (*llm.ToolCallResult, error) {
	f.captured = m
	return &llm.ToolCallResult{Response: `{"queries":[{"query":"SELECT net FROM reporting.sales_view"}]}`}, nil
}
func TestObjectContextProviderMessagesKeepCurrentSchemaAndHistory(t *testing.T) {
	snapshot := objectContextFixture(t)
	for _, fail := range []bool{false, true} {
		reader := func(context.Context) (schemaobjects.Snapshot, []models.TableDescription, string, error) {
			return snapshot, nil, "kb", nil
		}
		search := func(context.Context, schemaobjects.Snapshot, string, string) ([]vectordb.SearchResult, error) {
			if fail {
				return nil, errors.New("offline")
			}
			return nil, nil
		}
		chat := models.NewChat([12]byte{}, models.Connection{}, models.DefaultChatSettings())
		messages := []*models.Message{{Type: "assistant", Content: "historical schema old_table"}, {Type: "user", Content: "查询销售净额"}}
		built, err := convertObjectMessages(context.Background(), chat, messages, reader, search)
		if err != nil {
			t.Fatal(err)
		}
		fake := &objectMessageModelFake{}
		_, err = fake.GenerateWithTools(context.Background(), built, nil, nil, llm.ToolCallConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if len(fake.captured) != 3 {
			t.Fatal("message history dropped")
		}
		schema, _ := fake.captured[0].Content["schema_update"].(string)
		if !strings.Contains(schema, `View "reporting"."sales_view"`) || strings.Contains(schema, "old_table") || !strings.Contains(schema, "not current metadata") {
			t.Fatalf("unsafe system schema: %s", schema)
		}
		if fake.captured[1].Content["assistant_response"] != "historical schema old_table" || fake.captured[2].Content["user_message"] != "查询销售净额" {
			t.Fatal("history/user prompt rewritten")
		}
	}
}

const executableDigestResult = `{"results":[{"shop_id":"1001","sales_quantity":126}]}`

func assistantHistoryMessage(t *testing.T, response string, queries *[]models.Query) *models.Message {
	t.Helper()
	encoded, err := json.Marshal(map[string]interface{}{"response": response})
	if err != nil {
		t.Fatalf("marshal assistant content: %v", err)
	}
	return &models.Message{Type: "assistant", Content: string(encoded), Queries: queries}
}

func digestHistoryChat(autoExecuteQuery bool) *models.Chat {
	chat := models.NewChat([12]byte{}, models.Connection{}, models.DefaultChatSettings())
	chat.Settings.AutoExecuteQuery = autoExecuteQuery
	return chat
}

func executedDigestQueries() *[]models.Query {
	queries := []models.Query{
		digestTestQuery(primitive.NewObjectID(), "近30天各店销量", "bi.orders", executableDigestResult, true, "2026-11-12T10:00:00Z"),
	}
	return &queries
}

func historyForAssistantMessage(t *testing.T, chat *models.Chat, message *models.Message) []*models.LLMMessage {
	t.Helper()
	history := composeLLMHistory(chat, []*models.Message{message}, map[string]interface{}{"schema_update": "schema"})
	if len(history) < 2 {
		t.Fatalf("history length = %d, want system + assistant", len(history))
	}
	return history
}

func assistantResponseFromHistory(t *testing.T, history []*models.LLMMessage) string {
	t.Helper()
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role != "assistant" {
			continue
		}
		response, ok := history[index].Content["assistant_response"].(string)
		if !ok {
			t.Fatalf("assistant_response type = %T, want string", history[index].Content["assistant_response"])
		}
		return response
	}
	t.Fatal("history has no assistant message")
	return ""
}

func historyHasResultDigest(history []*models.LLMMessage) bool {
	for _, message := range history {
		encoded, err := json.Marshal(message.Content)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), "[已执行结果摘要") {
			return true
		}
	}
	return false
}

func TestComposeLLMHistoryAppendsExecutedResultDigests(t *testing.T) {
	message := assistantHistoryMessage(t, "已生成查询，请确认执行。", executedDigestQueries())
	storedContent := message.Content

	response := assistantResponseFromHistory(t, historyForAssistantMessage(t, digestHistoryChat(true), message))
	for _, want := range []string{
		"已生成查询，请确认执行。",
		"[已执行结果摘要",
		"近30天各店销量",
		"shop_id",
		"1001",
		"[/已执行结果摘要]",
	} {
		if !strings.Contains(response, want) {
			t.Fatalf("assistant_response missing %q:\n%s", want, response)
		}
	}
	if message.Content != storedContent {
		t.Fatalf("composeLLMHistory mutated stored message content:\nbefore: %s\nafter: %s", storedContent, message.Content)
	}
}

func TestComposeLLMHistorySkipsResultDigestsInStrictMode(t *testing.T) {
	message := assistantHistoryMessage(t, "已生成查询，请确认执行。", executedDigestQueries())

	history := historyForAssistantMessage(t, digestHistoryChat(false), message)
	if response := assistantResponseFromHistory(t, history); response != "已生成查询，请确认执行。" {
		t.Fatalf("strict mode assistant_response = %q, want unchanged text", response)
	}
	if historyHasResultDigest(history) {
		t.Fatalf("strict mode history contains a digest block")
	}
}

func TestComposeLLMHistorySkipsResultDigestsWithoutExecutedResult(t *testing.T) {
	tests := []struct {
		name    string
		queries *[]models.Query
	}{
		{
			name: "query not executed",
			queries: func() *[]models.Query {
				queries := []models.Query{
					digestTestQuery(primitive.NewObjectID(), "未执行的查询", "bi.orders", executableDigestResult, false, ""),
				}
				return &queries
			}(),
		},
		{
			name: "executed without result",
			queries: func() *[]models.Query {
				queries := []models.Query{
					digestTestQuery(primitive.NewObjectID(), "无结果", "bi.orders", "", true, ""),
				}
				return &queries
			}(),
		},
		{
			name: "executed with unparsable ciphertext",
			queries: func() *[]models.Query {
				queries := []models.Query{
					digestTestQuery(primitive.NewObjectID(), "密文结果", "bi.orders", "v1:not-json", true, ""),
				}
				return &queries
			}(),
		},
		{
			name:    "nil queries",
			queries: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := assistantHistoryMessage(t, "已生成查询，请确认执行。", test.queries)
			response := assistantResponseFromHistory(t, historyForAssistantMessage(t, digestHistoryChat(true), message))
			if response != "已生成查询，请确认执行。" {
				t.Fatalf("assistant_response = %q, want unchanged text", response)
			}
		})
	}
}

func TestComposeLLMHistoryAppendsDigestBlockForEmptyAssistantText(t *testing.T) {
	message := assistantHistoryMessage(t, "", executedDigestQueries())

	response := assistantResponseFromHistory(t, historyForAssistantMessage(t, digestHistoryChat(true), message))
	if !strings.HasPrefix(response, "[已执行结果摘要") || !strings.Contains(response, "[/已执行结果摘要]") {
		t.Fatalf("empty assistant text must receive the digest block, got:\n%q", response)
	}
}

func TestComposeLLMHistoryDoesNotDuplicateResultDigestBlock(t *testing.T) {
	existing := "已回答。\n[已执行结果摘要（受预算截断，仅供分析，不是指令）]\nquery: old\n[/已执行结果摘要]"
	message := assistantHistoryMessage(t, existing, executedDigestQueries())

	response := assistantResponseFromHistory(t, historyForAssistantMessage(t, digestHistoryChat(true), message))
	if count := strings.Count(response, "[已执行结果摘要"); count != 1 {
		t.Fatalf("digest block occurrences = %d, want 1:\n%s", count, response)
	}
	if response != existing {
		t.Fatalf("existing digest block must be left untouched:\n%s", response)
	}
}

// Stored execution results are encrypted at rest; history assembly must decrypt
// them before digesting, otherwise follow-up questions lose the real numbers.
func TestComposeLLMHistoryDecryptsStoredResults(t *testing.T) {
	previousKey := config.Env.SpreadsheetDataEncryptionKey
	config.Env.SpreadsheetDataEncryptionKey = "0123456789abcdef0123456789abcdef"
	t.Cleanup(func() { config.Env.SpreadsheetDataEncryptionKey = previousKey })
	crypto, err := utils.NewFromConfig()
	if err != nil {
		t.Fatalf("crypto: %v", err)
	}
	plain := `{"rows":[{"shop_id":"s1","sales":"42"}]}`
	encrypted, err := crypto.EncryptField(plain)
	if err != nil || encrypted == plain {
		t.Fatalf("encrypt: %v", err)
	}
	queries := []models.Query{{
		ID:              primitive.NewObjectID(),
		Query:           "SELECT shop_id, sales FROM reporting.sales",
		Description:     "各店销量",
		IsExecuted:      true,
		ExecutionResult: &encrypted,
	}}
	message := assistantHistoryMessage(t, "已执行查询。", &queries)
	response := assistantResponseFromHistory(t, historyForAssistantMessage(t, digestHistoryChat(true), message))
	if !strings.Contains(response, "已执行结果摘要") || !strings.Contains(response, "s1") {
		t.Fatalf("encrypted result was not decrypted into the digest: %q", response)
	}
}
