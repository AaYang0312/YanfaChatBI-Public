package services

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// BSON round trips simulate the repository boundary without contacting MongoDB.
type resultSharingChatRepo struct {
	repositories.ChatRepository
	stored bson.Raw
}

func (r *resultSharingChatRepo) Create(chat *models.Chat) error {
	data, err := bson.Marshal(chat)
	r.stored = data
	return err
}

func (r *resultSharingChatRepo) FindByID(primitive.ObjectID) (*models.Chat, error) {
	var chat models.Chat
	err := bson.Unmarshal(r.stored, &chat)
	return &chat, err
}

func (r *resultSharingChatRepo) ReadCurrent(_ context.Context, _ primitive.ObjectID, id primitive.ObjectID) (*models.Chat, error) {
	return r.FindByID(id)
}

func (r *resultSharingChatRepo) PatchMetadata(_ context.Context, _ primitive.ObjectID, id primitive.ObjectID, patch repositories.ChatMetadataPatch) error {
	chat, err := r.FindByID(id)
	if err != nil {
		return err
	}
	chat.Settings.NonTechMode = *patch.NonTechMode
	return r.Create(chat)
}

func TestDeprecatedResultSharing(t *testing.T) {
	originalKey, originalMax := config.Env.SchemaEncryptionKey, config.Env.MaxChatsPerUser
	config.Env.SchemaEncryptionKey, config.Env.MaxChatsPerUser = "0123456789abcdef0123456789abcdef", 1
	t.Cleanup(func() {
		config.Env.SchemaEncryptionKey, config.Env.MaxChatsPerUser = originalKey, originalMax
	})

	repo := &resultSharingChatRepo{}
	service := &chatService{chatRepo: repo, sourceAccess: NewUnconfiguredSourceAccessService(nil)}
	owner := primitive.NewObjectID().Hex()

	t.Run("legacy create value is inert", func(t *testing.T) {
		var req dtos.CreateChatRequest
		body := "{\"connection\":{\"type\":\"spreadsheet\"},\"settings\":{\"share_data_with_ai\":true,\"auto_execute_query\":false}}"
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		response, status, err := service.Create(owner, &req)
		if err != nil || status != http.StatusCreated {
			t.Fatalf("create: status=%d err=%v", status, err)
		}
		if response.Settings.ShareDataWithAI || response.Settings.AutoExecuteQuery {
			t.Fatalf("unexpected create settings: %+v", response.Settings)
		}
		if share, ok := repo.stored.Lookup("settings", "share_data_with_ai").BooleanOK(); ok && share {
			t.Fatal("legacy create persisted result sharing as enabled")
		}
	})

	t.Run("legacy stored value and edit are inert", func(t *testing.T) {
		chatID := primitive.NewObjectID()
		ownerID, _ := primitive.ObjectIDFromHex(owner)
		stored, err := bson.Marshal(bson.M{
			"_id": chatID, "user_id": ownerID, "connection": bson.M{"type": "spreadsheet"},
			"settings": bson.M{"share_data_with_ai": true},
		})
		if err != nil {
			t.Fatal(err)
		}
		repo.stored = stored
		response, status, err := service.GetByID(owner, chatID.Hex())
		if err != nil || status != http.StatusOK || response.Settings.ShareDataWithAI {
			t.Fatalf("legacy read: status=%d response=%+v err=%v", status, response, err)
		}

		var req dtos.UpdateChatRequest
		if err := json.Unmarshal([]byte("{\"settings\":{\"share_data_with_ai\":true,\"non_tech_mode\":true}}"), &req); err != nil {
			t.Fatal(err)
		}
		response, status, err = service.Update(owner, chatID.Hex(), &req)
		if err != nil || status != http.StatusOK || response.Settings.ShareDataWithAI || !response.Settings.NonTechMode {
			t.Fatalf("legacy edit: status=%d response=%+v err=%v", status, response, err)
		}
	})
}
