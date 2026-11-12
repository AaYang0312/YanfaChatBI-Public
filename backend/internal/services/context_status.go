package services

import (
	"context"
	"errors"
	"net/http"
	"time"

	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/llm"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// SetContextSummaryRepository wires the conversation summary store.
func (s *chatService) SetContextSummaryRepository(repo repositories.ContextSummaryRepository) {
	s.contextSummaries = repo
}

// GetContextStatus reports the compaction state for one chat. It is a read-only
// diagnostic: it never generates a summary nor calls a model.
func (s *chatService) GetContextStatus(ctx context.Context, userID, chatID string) (*dtos.ContextStatusResponse, uint32, error) {
	owner, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, http.StatusBadRequest, errors.New("invalid user ID format")
	}
	id, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		return nil, http.StatusBadRequest, errors.New("invalid chat ID format")
	}
	chat, err := s.chatRepo.ReadCurrent(ctx, owner, id)
	if err != nil {
		return nil, http.StatusNotFound, err
	}

	budget, _ := llm.PromptBudget(s.providerModelFor(ctx, chat))

	keepTurns := config.Env.ContextCompactionKeepTurns
	if keepTurns <= 0 {
		keepTurns = 6
	}
	estimated := 0
	if messages, _, err := s.chatRepo.FindMessagesByChat(id, 1, keepTurns*2); err == nil {
		for _, message := range messages {
			if message == nil {
				continue
			}
			estimated += llm.EstimateTokens(message.Content)
		}
	}

	status := &dtos.ContextStatusResponse{
		PromptBudget:          budget,
		EstimatedPromptTokens: estimated,
		CompactionEnabled:     config.Env.ContextCompactionEnabled,
	}
	if s.contextSummaries != nil {
		if summary, err := s.contextSummaries.GetByChatID(ctx, id); err == nil && summary != nil {
			status.SummaryRevision = summary.Revision
			status.CoveredCount = int64(len(summary.CoveredMessageIDs))
			status.SummaryTokens = summary.TokenEstimate
			status.StaleSource = summary.StaleSource
			if summary.CoveredUntil != nil {
				formatted := summary.CoveredUntil.UTC().Format(time.RFC3339)
				status.CoveredUntil = &formatted
			}
			estimated += summary.TokenEstimate
			status.EstimatedPromptTokens = estimated
		}
	}
	return status, http.StatusOK, nil
}

// providerModelFor resolves the provider model backing a chat, if configured.
func (s *chatService) providerModelFor(ctx context.Context, chat *models.Chat) *models.ProviderModel {
	if s.modelProviders == nil || chat == nil {
		return nil
	}
	providers, err := s.modelProviders.List(ctx)
	if err != nil {
		return nil
	}
	modelID := ""
	if chat.PreferredLLMModel != nil {
		modelID = *chat.PreferredLLMModel
	}
	slug, raw := pickModelEntry(providers, modelID)
	for _, provider := range providers {
		if provider.Slug != slug {
			continue
		}
		for i := range provider.Models {
			if provider.Models[i].ID == raw {
				model := provider.Models[i]
				return &model
			}
		}
	}
	return nil
}
