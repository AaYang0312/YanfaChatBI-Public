package services

import (
	"context"
	"encoding/json"
	"log"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/llm"
	"strings"
	"time"
)

// generationOnlyResultPolicy is injected in strict generation-only chats so the
// model cannot claim to have read query results.
const generationOnlyResultPolicy = "当前为仅生成模式（auto_execute_query=false）：你没有执行任何查询，不能声称看到或分析具体数值。用户询问具体数字时，明确说明仅生成模式不会读取数据，并给出可执行的查询。"

// resultDigestBlockMarker opens the runtime-only executed result digest block.
// It is also used to detect a block that is already present in a message.
const resultDigestBlockMarker = "[已执行结果摘要"

// This converter is shared by current-object and legacy request builders. It
// preserves history as conversation content; only systemContent supplies schema.
func composeLLMHistory(chat *models.Chat, messages []*models.Message, systemContent map[string]interface{}) []*models.LLMMessage {
	now := time.Now()
	systemMessage := &models.LLMMessage{
		ChatID:      chat.ID,
		UserID:      chat.UserID,
		Role:        string(constants.MessageTypeSystem),
		Content:     systemContent,
		IsEdited:    false,
		NonTechMode: chat.Settings.NonTechMode,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Step 3: Convert messages to LLM format
	llmMessages := make([]*models.LLMMessage, 0, len(messages)+2)
	llmMessages = append(llmMessages, systemMessage) // Prepend schema
	// Strict generation-only mode: the model must not imply it read any data.
	if !chat.Settings.AutoExecuteQuery {
		llmMessages = append(llmMessages, &models.LLMMessage{
			ChatID:      chat.ID,
			UserID:      chat.UserID,
			Role:        string(constants.MessageTypeSystem),
			Content:     map[string]interface{}{"result_policy": generationOnlyResultPolicy},
			NonTechMode: chat.Settings.NonTechMode,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}

	for _, msg := range messages {
		var contentMap map[string]interface{}

		if string(msg.Type) == string(constants.MessageTypeUser) {
			// User message
			contentMap = map[string]interface{}{
				"user_message": msg.Content,
			}
		} else {
			// Assistant message - parse the content
			var parsedContent map[string]interface{}
			if err := json.Unmarshal([]byte(msg.Content), &parsedContent); err != nil {
				log.Printf("Warning: Failed to parse assistant message content: %v", err)
				// Try to extract meaningful content from old-format text tool calls
				// (e.g., <ctrl42>call\nprint(default_api.generate_final_response(...)))
				extractedContent := msg.Content
				if parsed, ok := llm.TryParseTextToolCall(msg.Content); ok {
					var parsedMap map[string]interface{}
					if json.Unmarshal([]byte(parsed), &parsedMap) == nil {
						if assistantMsg, hasMsg := parsedMap["assistantMessage"].(string); hasMsg {
							extractedContent = assistantMsg
							log.Printf("convertMessagesToLLMFormat -> Extracted assistantMessage from old-format text tool call")
						}
					}
				}
				contentMap = map[string]interface{}{
					"assistant_response": appendExecutedResultDigests(chat, msg, extractedContent),
				}
			} else {
				// Extract response and queries
				response := parsedContent["response"]
				if text, ok := response.(string); ok {
					response = appendExecutedResultDigests(chat, msg, text)
				}
				contentMap = map[string]interface{}{
					"assistant_response": response,
				}
				if queries, ok := parsedContent["query"]; ok {
					contentMap["queries"] = queries
				}
				if buttons, ok := parsedContent["button_prompts"]; ok {
					contentMap["buttons"] = buttons
				}
			}

		}

		llmMessage := &models.LLMMessage{
			MessageID:   msg.ID,
			ChatID:      msg.ChatID,
			UserID:      msg.UserID,
			Role:        string(msg.Type),
			Content:     contentMap,
			IsEdited:    msg.IsEdited,
			NonTechMode: chat.Settings.NonTechMode,
			CreatedAt:   msg.CreatedAt,
			UpdatedAt:   msg.UpdatedAt,
		}

		llmMessages = append(llmMessages, llmMessage)
	}

	return llmMessages
}

// resultDigestDecryptor decrypts execution results read from storage. Query
// results are encrypted at rest; legacy plaintext values are returned as-is so
// both shapes remain digestible.
func resultDigestDecryptor() func(string) string {
	crypto, err := utils.NewFromConfig()
	if err != nil {
		return nil
	}
	return func(value string) string {
		plain, decryptErr := crypto.DecryptField(value)
		if decryptErr != nil {
			return value
		}
		return plain
	}
}

// appendExecutedResultDigests appends a runtime-computed result digest block for
// the executed queries of one assistant message. Strict generation-only chats
// never append digests, messages without executed results keep their text
// unchanged, and a block that is already present is not appended again. The
// block exists only in the provider history; the message itself is never
// rewritten.
func appendExecutedResultDigests(chat *models.Chat, msg *models.Message, text string) string {
	if chat == nil || msg == nil || !chat.Settings.AutoExecuteQuery || msg.Queries == nil {
		return text
	}
	if strings.Contains(text, resultDigestBlockMarker) {
		return text
	}
	return AppendResultDigestBlock(text, BuildResultDigestsWithOptions(*msg.Queries, ResultDigestOptions{Decrypt: resultDigestDecryptor()}))
}

func convertObjectMessages(ctx context.Context, chat *models.Chat, messages []*models.Message, read objectContextRead, search objectContextSearch) ([]*models.LLMMessage, error) {
	query := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type == string(constants.MessageTypeUser) {
			query = messages[i].Content
			break
		}
	}
	text, err := buildQueryObjectContext(ctx, read, search, query)
	if err != nil {
		return nil, err
	}
	return composeLLMHistory(chat, messages, map[string]interface{}{"schema_update": text}), nil
}
