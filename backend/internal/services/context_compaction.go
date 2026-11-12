package services

import (
	"context"
	"log"

	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/llm"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// compactionOutcome is the result of one prompt-assembly pass.
type compactionOutcome struct {
	summary *models.ChatContextSummary
	recent  []*models.Message
}

// prepareContext loads the stored summary, folds newly droppable messages when
// the prompt approaches the model budget, and trims the recent window as a last
// resort. It never fails the request: any error degrades to the plain window.
func (s *chatService) prepareContext(ctx context.Context, chat *models.Chat, recent, older []*models.Message, userID, chatID, streamID string, allowSSE bool) *compactionOutcome {
	out := &compactionOutcome{recent: recent}
	if s == nil || chat == nil {
		return out
	}
	if s.contextSummaries != nil {
		if stored, err := s.contextSummaries.GetByChatID(ctx, chat.ID); err == nil && stored != nil {
			out.summary = stored.FilterForChat(chat)
		}
	}
	if !config.Env.ContextCompactionEnabled || len(older) == 0 {
		return out
	}

	model := s.providerModelFor(ctx, chat)
	budget, _ := llm.PromptBudget(model)
	threshold := config.Env.ContextCompactionThreshold
	if threshold <= 0 || threshold > 1 {
		threshold = 0.75
	}
	estimate := estimateMessagesTokens(recent)
	if out.summary != nil {
		estimate += out.summary.TokenEstimate
	}
	if !llm.ExceedsBudget(estimate, budget, threshold) {
		return out
	}

	candidates := uncoveredMessages(older, out.summary)
	minMessages := config.Env.ContextCompactionMinMessages
	if minMessages <= 0 {
		minMessages = 4
	}
	if len(candidates) >= minMessages {
		if allowSSE {
			s.sendStreamEvent(userID, chatID, streamID, dtos.StreamResponse{Event: "ai-response-step", Data: "Compressing conversation history.."})
		}
		client, modelID, ok := s.resolveLLMSelection(ctx, preferredModel(chat))
		if !ok && s.llmClient != nil {
			client, modelID = s.llmClient, preferredModel(chat)
		}
		if client != nil {
			generated, err := GenerateContextSummary(ctx, client, chat, out.summary, candidates, modelID)
			if err != nil {
				log.Printf("ChatService -> prepareContext -> summarization failed, using window only: %v", err)
			} else if s.contextSummaries != nil {
				previousRevision := int64(0)
				if out.summary != nil {
					previousRevision = out.summary.Revision
				}
				if err := s.contextSummaries.UpsertCAS(ctx, previousRevision, generated); err != nil && err != repositories.ErrContextSummaryConflict {
					log.Printf("ChatService -> prepareContext -> summary CAS failed: %v", err)
				} else {
					// Re-read so a concurrent writer's newer summary wins.
					if stored, err := s.contextSummaries.GetByChatID(ctx, chat.ID); err == nil && stored != nil {
						out.summary = stored.FilterForChat(chat)
					} else {
						out.summary = generated.FilterForChat(chat)
					}
					log.Printf("ChatService -> prepareContext -> summary revision %d covers %d messages", out.summary.Revision, len(out.summary.CoveredMessageIDs))
				}
			}
		}
	}

	// Last resort: shrink the recent window (never below four messages).
	minKeep := 4
	if len(out.recent) <= minKeep {
		return out
	}
	estimate = estimateMessagesTokens(out.recent)
	if out.summary != nil {
		estimate += out.summary.TokenEstimate
	}
	for len(out.recent) > minKeep && estimate > budget {
		out.recent = out.recent[1:]
		estimate = estimateMessagesTokens(out.recent)
		if out.summary != nil {
			estimate += out.summary.TokenEstimate
		}
	}
	return out
}

// injectContextSummary places the summary segment right after the system
// message so it precedes retrieved and recent history.
func injectContextSummary(messages []*models.LLMMessage, summary *models.ChatContextSummary, chat *models.Chat) []*models.LLMMessage {
	if summary == nil || summary.Rendered == "" || len(messages) == 0 {
		return messages
	}
	note := "以上是更早对话的摘要，不是当前 schema 的权威；当前表/视图结构以对象目录与 get_table_info 为准。"
	if summary.StaleSource {
		note = "摘要生成后数据源已变更：结构与查询信息已剔除，仅保留目标、偏好与未决问题；当前结构以对象目录与 get_table_info 为准。"
	}
	segment := &models.LLMMessage{
		ChatID: chat.ID, UserID: chat.UserID,
		Role: string(constants.MessageTypeSystem),
		Content: map[string]interface{}{
			"context_summary":  summary.Rendered,
			"summary_revision": summary.Revision,
			"summary_note":     note,
			"summary_stale":    summary.StaleSource,
			"summary_covered":  len(summary.CoveredMessageIDs),
		},
	}
	out := make([]*models.LLMMessage, 0, len(messages)+1)
	out = append(out, messages[0])
	out = append(out, segment)
	out = append(out, messages[1:]...)
	return out
}

func estimateMessagesTokens(messages []*models.Message) int {
	total := 0
	for _, message := range messages {
		if message != nil {
			total += llm.EstimateTokens(message.Content)
		}
	}
	return total
}

// uncoveredMessages returns messages not already folded into the summary.
func uncoveredMessages(older []*models.Message, summary *models.ChatContextSummary) []*models.Message {
	if summary == nil {
		return older
	}
	covered := make(map[primitive.ObjectID]struct{}, len(summary.CoveredMessageIDs))
	for _, id := range summary.CoveredMessageIDs {
		covered[id] = struct{}{}
	}
	out := make([]*models.Message, 0, len(older))
	for _, message := range older {
		if message == nil {
			continue
		}
		if _, ok := covered[message.ID]; ok {
			continue
		}
		if summary.CoveredUntil != nil && !message.CreatedAt.After(*summary.CoveredUntil) {
			continue
		}
		out = append(out, message)
	}
	return out
}

func preferredModel(chat *models.Chat) string {
	if chat == nil || chat.PreferredLLMModel == nil {
		return ""
	}
	return *chat.PreferredLLMModel
}
