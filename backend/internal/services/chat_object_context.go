package services

import (
	"context"
	"fmt"
	"log"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
	"time"
)

// KB failure affects enrichment only. Catalog/source failure never falls back
// to CurrentSchema, a previous catalog, history or an old vector payload.
func (s *chatService) currentObjectKnowledge(ctx context.Context, chat *models.Chat) (schemaobjects.Snapshot, ObjectKnowledgeProjection, error) {
	snapshot, err := s.loadQueryObjectSnapshot(ctx, chat)
	if err != nil {
		return schemaobjects.Snapshot{}, ObjectKnowledgeProjection{}, err
	}
	var kb *models.KnowledgeBase
	if s.kbRepo != nil {
		kb, err = s.kbRepo.FindByChatID(ctx, chat.ID)
		if err != nil {
			kb = nil
		}
	}
	knowledge, err := ProjectObjectKnowledge(*snapshot, kb)
	if err != nil {
		knowledge, err = ProjectObjectKnowledge(*snapshot, nil)
	}
	return *snapshot, knowledge, err
}
func (s *chatService) queryObjectReader(chat *models.Chat) objectContextRead {
	return func(ctx context.Context) (schemaobjects.Snapshot, []models.TableDescription, string, error) {
		snapshot, knowledge, err := s.currentObjectKnowledge(ctx, chat)
		return snapshot, knowledge.Effective, knowledge.KBRevision, err
	}
}
func (s *chatService) queryObjectIndex(chat *models.Chat) (ObjectIndex, error) {
	factory, ok := s.vectorizationSvc.(ObjectIndexFactory)
	if !ok || s.objectState == nil {
		return nil, ErrObjectIndexUnavailable
	}
	return factory.NewObjectIndex(s.objectState, func(ctx context.Context) (schemaobjects.Snapshot, string, error) {
		snapshot, knowledge, err := s.currentObjectKnowledge(ctx, chat)
		return snapshot, knowledge.KBRevision, err
	})
}
func (s *chatService) queryObjectMessages(ctx context.Context, chat *models.Chat, messages []*models.Message) ([]*models.LLMMessage, error) {
	current, read, err := acquireChatSource(ctx, s.chatRepo, s.sourceAccess, chat.UserID.Hex(), chat.ID.Hex())
	if err != nil {
		return nil, err
	}
	defer read.Close()
	if !sameChatBinding(current, chat) || current.SelectionRevision != chat.SelectionRevision {
		return nil, ErrSelectionConflict
	}
	index, indexErr := s.queryObjectIndex(current)
	var search objectContextSearch
	if indexErr == nil {
		search = func(ctx context.Context, snapshot schemaobjects.Snapshot, kb, query string) ([]vectordb.SearchResult, error) {
			if query == "" {
				query = "main objects relationships key columns"
			}
			return index.Search(ctx, snapshot, kb, query, 10)
		}
	}
	started := time.Now()
	catalogReads := 0
	baseRead := s.queryObjectReader(current)
	countedRead := func(ctx context.Context) (schemaobjects.Snapshot, []models.TableDescription, string, error) {
		catalogReads++
		return baseRead(ctx)
	}
	converted, err := convertObjectMessages(read.Context, current, messages, countedRead, search)
	chars := 0
	if err == nil && len(converted) > 0 {
		if text, ok := converted[0].Content["schema_update"].(string); ok {
			chars = len([]rune(text))
		}
	}
	log.Printf("query object context: catalog_reads=%d context_chars=%d elapsed=%s success=%t", catalogReads, chars, time.Since(started), err == nil)
	return converted, err
}
func (s *chatService) queryObjectTableInfo(ctx context.Context, chat *models.Chat, args map[string]interface{}) (string, error) {
	snapshot, knowledge, err := s.currentObjectKnowledge(ctx, chat)
	if err != nil {
		return "", err
	}
	objects, err := resolveQueryObjectArguments(snapshot, args)
	if err != nil {
		return "", schemaobjects.ErrUnavailable
	}
	return formatQueryObjectDetails(snapshot.Scope, objects, knowledge.Effective), nil
}

// QueryObjectStructure is the shared dashboard metadata capability. The caller
// supplies an owner/chat identity, never a trusted snapshot or payload scope.
func (s *chatService) QueryObjectStructure(ctx context.Context, userID, chatID string, args map[string]interface{}) (string, error) {
	chat, read, err := acquireChatSource(ctx, s.chatRepo, s.sourceAccess, userID, chatID)
	if err != nil {
		return "", err
	}
	defer read.Close()
	if !queryObjectsEnabled(chat) {
		if chat.SelectedObjects != nil || chat.Settings.AutoExecuteQuery {
			return "", ErrQueryObjectsDisabled
		}
		return s.generationMetadata(read.Context, chat, args)
	}
	if args != nil {
		return s.queryObjectTableInfo(read.Context, chat, args)
	}
	return buildQueryObjectContext(read.Context, s.queryObjectReader(chat), nil, "")
}
func (s *chatService) rebuildQueryObjectIndex(ctx context.Context, chat *models.Chat) error {
	current, read, err := acquireChatSource(ctx, s.chatRepo, s.sourceAccess, chat.UserID.Hex(), chat.ID.Hex())
	if err != nil {
		return err
	}
	defer read.Close()
	if !sameChatBinding(current, chat) || current.SelectionRevision != chat.SelectionRevision {
		return ErrSelectionConflict
	}
	snapshot, knowledge, err := s.currentObjectKnowledge(read.Context, current)
	if err != nil {
		return err
	}
	index, err := s.queryObjectIndex(current)
	if err != nil {
		return fmt.Errorf("structure available; object index unavailable: %w", err)
	}
	chunks, err := BuildObjectSchemaChunks(snapshot, knowledge.KBRevision, objectIndexEnrichments(knowledge.Effective))
	if err != nil {
		return err
	}
	return index.Build(read.Context, snapshot, knowledge.KBRevision, chunks)
}

func (s *chatService) schemaReadyForGeneration(ctx context.Context, chat *models.Chat) (bool, error) {
	if queryObjectsEnabled(chat) {
		snapshot, err := s.loadQueryObjectSnapshot(ctx, chat)
		if err != nil {
			return false, err
		}
		return snapshot.Complete && len(snapshot.Objects) > 0, nil
	}
	if chat.SelectedObjects != nil {
		return false, ErrQueryObjectsDisabled
	}
	if !frozenGenerationPolicy(ctx, chat).AllowsAutomaticDataExecution() && isPostgresGenerationType(chat.Connection.Type) {
		snapshot, err := s.legacyPostgresGenerationSnapshot(ctx, chat)
		if err != nil {
			return false, err
		}
		return snapshot.Complete && len(snapshot.Objects) > 0, nil
	}
	return s.dbManager.GetSchemaManager().IsSchemaReady(ctx, chat.ID.Hex()), nil
}
