package services

import (
	"context"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/config"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"net/http"
	"testing"
)

type knowledgeGuardChatRepo struct {
	repositories.ChatRepository
	chat *models.Chat
}

func (r *knowledgeGuardChatRepo) FindByID(id primitive.ObjectID) (*models.Chat, error) {
	if id == r.chat.ID {
		return r.chat, nil
	}
	return nil, nil
}

type knowledgeGuardKBRepo struct {
	repositories.KnowledgeBaseRepository
	kb            *models.KnowledgeBase
	reads, writes int
}

func (r *knowledgeGuardKBRepo) FindByChatID(context.Context, primitive.ObjectID) (*models.KnowledgeBase, error) {
	r.reads++
	return r.kb, nil
}
func (r *knowledgeGuardKBRepo) Upsert(context.Context, *models.KnowledgeBase) error {
	r.writes++
	return nil
}

// This file is compiled with the real Linux service package. Runtime execution
// is still required in a Linux runner; portable core tests are not this test.
func TestObjectKnowledgeServiceV2RejectsLegacyWriteAndSkipsAutomaticAI(t *testing.T) {
	previous := config.Env.QueryObjectsEnabled
	config.Env.QueryObjectsEnabled = true
	defer func() { config.Env.QueryObjectsEnabled = previous }()
	owner := primitive.NewObjectID()
	chat := models.NewChat(owner, models.Connection{Type: constants.DatabaseTypePostgreSQL, Host: "synthetic", Database: "synthetic"}, models.DefaultChatSettings())
	repo := &knowledgeGuardKBRepo{}
	service := &chatService{chatRepo: &knowledgeGuardChatRepo{chat: chat}, kbRepo: repo}
	if _, status, err := service.UpdateKnowledgeBase(context.Background(), owner.Hex(), chat.ID.Hex(), []models.TableDescription{{TableName: "old", Description: "unsafe"}}); err == nil || status != http.StatusConflict || repo.writes != 0 {
		t.Fatal("v2 accepted unversioned legacy PUT")
	}
	if err := service.syncKnowledgeBase(context.Background(), chat.ID.Hex(), "synthetic schema"); err != nil || repo.reads != 0 || repo.writes != 0 {
		t.Fatalf("v2 async sync touched models/storage: %v", err)
	}
	config.Env.QueryObjectsEnabled = false
	repo.kb = &models.KnowledgeBase{KnowledgeVersion: 2, ChatID: chat.ID, UserID: owner}
	if err := service.syncKnowledgeBase(context.Background(), chat.ID.Hex(), "synthetic schema"); err != nil || repo.writes != 0 {
		t.Fatalf("flag rollback generated AI for v2 KB: %v", err)
	}
}
func TestObjectKnowledgeServiceRejectsOwnerBeforeKBRead(t *testing.T) {
	owner := primitive.NewObjectID()
	chat := models.NewChat(owner, models.Connection{Type: constants.DatabaseTypePostgreSQL}, models.DefaultChatSettings())
	repo := &knowledgeGuardKBRepo{}
	service := &chatService{chatRepo: &knowledgeGuardChatRepo{chat: chat}, kbRepo: repo}
	if _, status, err := service.GetKnowledgeBaseView(context.Background(), primitive.NewObjectID().Hex(), chat.ID.Hex()); err == nil || status != http.StatusForbidden || repo.reads != 0 {
		t.Fatal("foreign owner reached KB storage")
	}
}
