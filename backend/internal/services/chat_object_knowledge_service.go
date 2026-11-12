package services

import (
	"context"
	"errors"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/schemaobjects"
	"net/http"
	"time"
)

type ObjectKnowledgeService interface {
	GetKnowledgeBaseView(context.Context, string, string) (*dtos.KnowledgeBaseResponse, uint32, error)
	UpdateKnowledgeBaseWithRevision(context.Context, string, string, dtos.UpdateKnowledgeBaseRequest) (*dtos.KnowledgeBaseResponse, uint32, error)
	ConfirmKnowledgeBaseMappings(context.Context, string, string, dtos.ConfirmKnowledgeBaseMappingsRequest) (*dtos.KnowledgeBaseResponse, uint32, error)
}
type knowledgeStorageAdapter struct {
	repositories.KnowledgeBaseRepository
}

func (a knowledgeStorageAdapter) CompareAndSwapKnowledge(ctx context.Context, previous, next *models.KnowledgeBase) (bool, error) {
	writer, ok := a.KnowledgeBaseRepository.(repositories.ObjectKnowledgeRepository)
	if !ok {
		return false, ErrObjectKnowledgeStore
	}
	return writer.CompareAndSwapKnowledge(ctx, previous, next)
}
func knowledgeHTTPStatus(err error) uint32 {
	switch {
	case errors.Is(err, ErrObjectKnowledgeInput):
		return http.StatusBadRequest
	case errors.Is(err, ErrObjectKnowledgeRevision), errors.Is(err, ErrObjectKnowledgeMapping), errors.Is(err, ErrSelectionConflict), errors.Is(err, ErrQueryObjectsDisabled):
		return http.StatusConflict
	case errors.Is(err, ErrObjectKnowledgeScope):
		return http.StatusForbidden
	case errors.Is(err, ErrObjectKnowledgeStore):
		return http.StatusServiceUnavailable
	default:
		return sourceHTTPStatus(err)
	}
}
func legacyKnowledgeResponse(kb *models.KnowledgeBase, saved bool) *dtos.KnowledgeBaseResponse {
	return &dtos.KnowledgeBaseResponse{ChatID: kb.ChatID.Hex(), TableDescriptions: kb.TableDescriptions, KBRevision: kb.KBRevision,
		CreatedAt: kb.CreatedAt.Format(time.RFC3339), UpdatedAt: kb.UpdatedAt.Format(time.RFC3339), Saved: saved, IndexStatus: "not_checked"}
}

// Keep the source lease until storage CAS and index invalidation have completed.
// Snapshot callbacks re-read the actual chat binding/selection every time.
func (s *chatService) openObjectKnowledgeSession(ctx context.Context, userID, chatID string) (context.Context, objectKnowledgeSession, func(), error) {
	chat, read, err := acquireChatSource(ctx, s.chatRepo, s.sourcePolicy(), userID, chatID)
	if err != nil {
		return ctx, objectKnowledgeSession{}, func() {}, err
	}
	closeRead := func() { _ = read.Close() }
	if !queryObjectsEnabled(chat) {
		closeRead()
		return ctx, objectKnowledgeSession{}, func() {}, ErrQueryObjectsDisabled
	}
	if s.kbRepo == nil {
		closeRead()
		return ctx, objectKnowledgeSession{}, func() {}, ErrObjectKnowledgeStore
	}
	session := objectKnowledgeSession{Store: knowledgeStorageAdapter{s.kbRepo}, Now: time.Now().UTC().Truncate(time.Millisecond)}
	session.Snapshot = func(ctx context.Context) (schemaobjects.Snapshot, error) {
		current, err := s.chatRepo.ReadCurrent(ctx, chat.UserID, chat.ID)
		if err != nil || current == nil {
			return schemaobjects.Snapshot{}, ErrSourceStateUnavailable
		}
		if !sameChatBinding(chat, current) || current.SelectionRevision != chat.SelectionRevision || !queryObjectsEnabled(current) {
			return schemaobjects.Snapshot{}, ErrSelectionConflict
		}
		snapshot, err := s.loadQueryObjectSnapshot(ctx, current)
		if err != nil {
			return schemaobjects.Snapshot{}, err
		}
		if snapshot == nil {
			return schemaobjects.Snapshot{}, ErrObjectKnowledgeSnapshot
		}
		return *snapshot, nil
	}
	session.Invalidate = func(ctx context.Context, scope schemaobjects.Scope) error {
		current, err := s.chatRepo.ReadCurrent(ctx, chat.UserID, chat.ID)
		if err != nil || current == nil || !sameChatBinding(chat, current) || current.SelectionRevision != scope.SelectionRevision {
			return ErrSelectionConflict
		}
		return invalidateObjectKnowledgeIndex(ctx, s.objectState, scope)
	}
	return read.Context, session, closeRead, nil
}
func (s *chatService) getQueryObjectKnowledgeBase(ctx context.Context, userID, chatID string) (*dtos.KnowledgeBaseResponse, uint32, error) {
	readContext, session, closeRead, err := s.openObjectKnowledgeSession(ctx, userID, chatID)
	if err != nil {
		return nil, knowledgeHTTPStatus(err), err
	}
	defer closeRead()
	response, err := session.get(readContext)
	if err != nil {
		return nil, knowledgeHTTPStatus(err), err
	}
	return response, http.StatusOK, nil
}
func (s *chatService) GetKnowledgeBaseView(ctx context.Context, userID, chatID string) (*dtos.KnowledgeBaseResponse, uint32, error) {
	chat, err := ownedSourceChat(ctx, s.chatRepo, userID, chatID)
	if err != nil {
		return nil, knowledgeHTTPStatus(err), err
	}
	if queryObjectsEnabled(chat) {
		return s.getQueryObjectKnowledgeBase(ctx, userID, chatID)
	}
	kb, status, err := s.GetKnowledgeBase(ctx, userID, chatID)
	if err != nil {
		return nil, status, err
	}
	return legacyKnowledgeResponse(kb, false), status, nil
}
func (s *chatService) UpdateKnowledgeBaseWithRevision(ctx context.Context, userID, chatID string, request dtos.UpdateKnowledgeBaseRequest) (*dtos.KnowledgeBaseResponse, uint32, error) {
	chat, err := ownedSourceChat(ctx, s.chatRepo, userID, chatID)
	if err != nil {
		return nil, knowledgeHTTPStatus(err), err
	}
	if !queryObjectsEnabled(chat) {
		if request.ExpectedKBRevision != "" {
			return nil, http.StatusConflict, ErrQueryObjectsDisabled
		}
		for _, edit := range request.TableDescriptions {
			if edit.DescriptionIndex != nil || edit.ObjectID != "" {
				return nil, http.StatusConflict, ErrQueryObjectsDisabled
			}
		}
		kb, status, err := s.UpdateKnowledgeBase(ctx, userID, chatID, request.LegacyDescriptions())
		if err != nil {
			return nil, status, err
		}
		return legacyKnowledgeResponse(kb, true), status, nil
	}
	readContext, session, closeRead, err := s.openObjectKnowledgeSession(ctx, userID, chatID)
	if err != nil {
		return nil, knowledgeHTTPStatus(err), err
	}
	defer closeRead()
	response, err := session.save(readContext, request.ExpectedKBRevision, func(snapshot schemaobjects.Snapshot, kb *models.KnowledgeBase, revision string) (*models.KnowledgeBase, error) {
		return applyObjectKnowledgeEdits(snapshot, kb, revision, request.TableDescriptions, session.Now)
	})
	if err != nil {
		return nil, knowledgeHTTPStatus(err), err
	}
	return response, http.StatusOK, nil
}
func (s *chatService) ConfirmKnowledgeBaseMappings(ctx context.Context, userID, chatID string, request dtos.ConfirmKnowledgeBaseMappingsRequest) (*dtos.KnowledgeBaseResponse, uint32, error) {
	readContext, session, closeRead, err := s.openObjectKnowledgeSession(ctx, userID, chatID)
	if err != nil {
		return nil, knowledgeHTTPStatus(err), err
	}
	defer closeRead()
	response, err := session.save(readContext, request.ExpectedKBRevision, func(snapshot schemaobjects.Snapshot, kb *models.KnowledgeBase, _ string) (*models.KnowledgeBase, error) {
		return applyObjectKnowledgeMappings(snapshot, kb, request)
	})
	if err != nil {
		return nil, knowledgeHTTPStatus(err), err
	}
	return response, http.StatusOK, nil
}
