package services

import (
	"context"
	"encoding/json"
	"log"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// analyzeExecutedResults asks the model to interpret the executed results of one
// assistant turn and stores the interpretation as a separate assistant message.
// It is opt-in (chat.Settings.AutoAnalyzeResults) and never runs in strict
// generation-only chats. Failures are logged and never affect the main answer.
func (s *chatService) analyzeExecutedResults(ctx context.Context, userID, chatID, streamID string, chat *models.Chat, msgResp *dtos.MessageResponse, modelID string) {
	if s == nil || chat == nil || msgResp == nil || msgResp.Queries == nil {
		return
	}
	log.Printf("analyzeExecutedResults -> entry auto_execute=%v auto_analyze=%v queries=%d", chat.Settings.AutoExecuteQuery, chat.Settings.AutoAnalyzeResults, len(*msgResp.Queries))
	if !chat.Settings.AutoExecuteQuery || !chat.Settings.AutoAnalyzeResults {
		return
	}

	queries := make([]models.Query, 0, len(*msgResp.Queries))
	for _, query := range *msgResp.Queries {
		if !query.IsExecuted || len(query.ExecutionResult) == 0 {
			continue
		}
		raw, err := json.Marshal(query.ExecutionResult)
		if err != nil {
			continue
		}
		encoded := string(raw)
		id, _ := primitive.ObjectIDFromHex(query.ID)
		queries = append(queries, models.Query{
			ID: id, Query: query.Query, Description: query.Description,
			Tables: query.Tables, IsExecuted: true, IsCritical: query.IsCritical,
			CanRollback: query.CanRollback, ExecutionResult: &encoded, ActionAt: query.ActionAt,
		})
	}
	digests := BuildResultDigests(queries)
	block := RenderResultDigestBlock(digests)
	log.Printf("analyzeExecutedResults -> executed=%d digests=%d block=%d", len(queries), len(digests), len(block))
	if block == "" {
		return
	}

	client, rawModel, ok := s.resolveLLMSelection(ctx, modelID)
	if !ok {
		if s.llmClient == nil {
			return
		}
		client = s.llmClient
		rawModel = modelID
	}

	prompt := "下面是一次查询的真实执行结果摘要。请用简洁的中文解读关键数字（总量、分布、异常值或趋势），" +
		"不要重复 SQL，不要生成新的查询，也不要编造摘要中没有的数据。\n\n" + block

	// GenerateRawJSON tolerates endpoints that reject response_format and
	// returns plain text when the model does not wrap it in JSON.
	response, err := client.GenerateRawJSON(ctx,
		"你是数据分析助手。只输出一个 JSON 对象：{\"analysis\":\"用简洁中文解读关键数字的段落\"}。"+
			"不要生成 SQL，不要编造摘要中没有的数据。",
		prompt, rawModel)
	if err != nil {
		log.Printf("ChatService -> analyzeExecutedResults -> model call failed: %v", err)
		return
	}
	text := analysisTextFromResponse(response)
	if text == "" {
		log.Printf("ChatService -> analyzeExecutedResults -> empty analysis")
		return
	}

	owner, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return
	}
	chatObjID, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return
	}
	message := &models.Message{
		Base:        models.NewBase(),
		UserID:      owner,
		ChatID:      chatObjID,
		Content:     text,
		Type:        "assistant",
		NonTechMode: chat.Settings.NonTechMode,
	}
	if modelID != "" {
		message.LLMModel = &modelID
	}
	if err := s.chatRepo.CreateMessage(message); err != nil {
		log.Printf("ChatService -> analyzeExecutedResults -> save failed: %v", err)
		return
	}
	log.Printf("analyzeExecutedResults -> saved message %s (%d chars)", message.ID.Hex(), len(text))
	s.sendStreamEvent(userID, chatID, streamID, dtos.StreamResponse{
		Event: "ai-response",
		Data:  s.buildMessageResponse(message),
	})
}

// analysisTextFromResponse extracts the analysis field, falling back to the
// raw text when the model did not wrap its answer in JSON.
func analysisTextFromResponse(response string) string {
	if response == "" {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(response), &parsed); err == nil {
		if text, ok := parsed["analysis"].(string); ok && text != "" {
			return text
		}
		if text, ok := parsed["assistantMessage"].(string); ok && text != "" {
			return text
		}
	}
	return response
}

// selectedAutoRunModel returns the model recorded for the assistant turn.
func selectedAutoRunModel(msgResp *dtos.MessageResponse) string {
	if msgResp == nil {
		return ""
	}
	if msgResp.LLMModel != nil {
		return *msgResp.LLMModel
	}
	return ""
}
