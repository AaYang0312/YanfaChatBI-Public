package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/mongodb"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func displayNameMongoService(t *testing.T) (*chatService, *mongo.Collection) {
	t.Helper()
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("persistence unverified: ASSISTANT_TEST_MONGO_URI is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).
		SetServerSelectionTimeout(5*time.Second).SetRegistry(mongodb.BSONRegistry()))
	if err != nil {
		t.Fatalf("connect isolated Mongo: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("ping isolated Mongo: %v", err)
	}
	databaseName := "chat_display_name_" + primitive.NewObjectID().Hex()
	database := client.Database(databaseName)
	t.Cleanup(func() { _ = database.Drop(context.Background()) })
	oldKey, oldMax := config.Env.SchemaEncryptionKey, config.Env.MaxChatsPerUser
	config.Env.SchemaEncryptionKey = "0123456789abcdef0123456789abcdef"
	config.Env.MaxChatsPerUser = 1
	t.Cleanup(func() {
		config.Env.SchemaEncryptionKey, config.Env.MaxChatsPerUser = oldKey, oldMax
	})
	repo := repositories.NewChatRepository(&mongodb.MongoDBClient{
		Client: client,
		Config: mongodb.MongoDbConfigModel{DatabaseName: databaseName},
	}, nil)
	// Nil connection/schema/model collaborators make accidental work on a name-only PATCH fail.
	return &chatService{chatRepo: repo, sourceAccess: NewUnconfiguredSourceAccessService(nil)}, database.Collection("chats")
}

func displayNameCreateRequest(t *testing.T, databaseType, database, metadata string) *dtos.CreateChatRequest {
	t.Helper()
	var request dtos.CreateChatRequest
	body := fmt.Sprintf(`{"connection":{"type":%q,"host":"127.0.0.1","port":"18538","username":"v15_reader","database":%q}%s}`, databaseType, database, metadata)
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	return &request
}

func displayNamePatch(t *testing.T, service *chatService, ownerID, chatID, body string) *dtos.ChatResponse {
	t.Helper()
	var request dtos.UpdateChatRequest
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("decode PATCH: %v", err)
	}
	response, status, err := service.Update(ownerID, chatID, &request)
	if err != nil || status != http.StatusOK {
		t.Fatalf("PATCH %s: status=%d error=%v", body, status, err)
	}
	return response
}

func displayNameStored(t *testing.T, collection *mongo.Collection, chatID string) models.Chat {
	t.Helper()
	id, err := primitive.ObjectIDFromHex(chatID)
	if err != nil {
		t.Fatal(err)
	}
	var stored models.Chat
	if err := collection.FindOne(context.Background(), bson.M{"_id": id}).Decode(&stored); err != nil {
		t.Fatalf("read persisted chat: %v", err)
	}
	return stored
}

func displayNameAssertReload(t *testing.T, service *chatService, collection *mongo.Collection, ownerID, chatID, name, database string) models.Chat {
	t.Helper()
	stored := displayNameStored(t, collection, chatID)
	if stored.DisplayName != name || stored.ID.Hex() != chatID || stored.UserID.Hex() != ownerID {
		t.Fatalf("persisted identity/name: id=%s owner=%s name=%q", stored.ID.Hex(), stored.UserID.Hex(), stored.DisplayName)
	}
	// A new service/repository read, not the create/PATCH response or an in-memory Chat.
	reopened := &chatService{chatRepo: service.chatRepo}
	got, status, err := reopened.GetByID(ownerID, chatID)
	if err != nil || status != http.StatusOK {
		t.Fatalf("reload: status=%d error=%v", status, err)
	}
	if got.DisplayName != name || got.Connection.Database != database || got.ID != chatID || got.UserID != ownerID {
		t.Fatalf("reloaded chat: %#v", got)
	}
	listed, status, err := reopened.List(ownerID, 1, 100)
	if err != nil || status != http.StatusOK {
		t.Fatalf("list: status=%d error=%v", status, err)
	}
	for _, row := range listed.Chats {
		if row.ID == chatID {
			if row.DisplayName != name || row.Connection.Database != database || row.UserID != ownerID {
				t.Fatalf("listed chat: %#v", row)
			}
			return stored
		}
	}
	t.Fatalf("persisted chat %s absent from owner list", chatID)
	return models.Chat{}
}

func TestChatDisplayNameCreatePersistence(t *testing.T) {
	service, collection := displayNameMongoService(t)
	ownerID := primitive.NewObjectID().Hex()
	for _, entry := range []struct {
		name     string
		create   func(string, *dtos.CreateChatRequest) (*dtos.ChatResponse, uint32, error)
		dbType   string
		database string
	}{
		// Create's existing Google Sheets branch needs no external connection or credentials.
		{"Create", service.Create, "google_sheets", "sheet_fixture"},
		{"CreateWithoutConnectionPing", service.CreateWithoutConnectionPing, "postgresql", "bi_agent"},
	} {
		t.Run(entry.name, func(t *testing.T) {
			for _, test := range []struct {
				name     string
				metadata string
				want     string
			}{
				{"omitted", "", ""},
				{"null", `,"display_name":null`, ""},
				{"empty", `,"display_name":""`, ""},
				{"blank", `,"display_name":" \t\n　 "`, ""},
				{"trimmed", `,"display_name":"　 yanfa-erp \n"`, "yanfa-erp"},
			} {
				t.Run(test.name, func(t *testing.T) {
					response, status, err := entry.create(ownerID, displayNameCreateRequest(t, entry.dbType, entry.database, test.metadata))
					if err != nil || status != http.StatusCreated {
						t.Fatalf("create: status=%d error=%v", status, err)
					}
					displayNameAssertReload(t, service, collection, ownerID, response.ID, test.want, entry.database)
				})
			}
		})
	}
}

func TestChatDisplayNamePatchPersistence(t *testing.T) {
	service, collection := displayNameMongoService(t)
	ownerID := primitive.NewObjectID().Hex()
	created, status, err := service.CreateWithoutConnectionPing(ownerID,
		displayNameCreateRequest(t, "postgresql", "bi_agent", `,"display_name":"yanfa-erp"`))
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create: status=%d error=%v", status, err)
	}
	baseline := displayNameStored(t, collection, created.ID)
	if _, err := collection.UpdateOne(context.Background(), bson.M{"_id": baseline.ID}, bson.M{"$set": bson.M{
		"selected_collections":         "orders",
		"preferred_llm_model":          "fixture-model",
		"connection.current_schema":    "fixture schema must remain cached",
		"connection.schema_updated_at": primitive.NewDateTimeFromTime(time.Unix(1700000000, 0)),
	}}); err != nil {
		t.Fatal(err)
	}
	baseline = displayNameStored(t, collection, created.ID)
	for _, step := range []struct {
		name    string
		body    string
		want    string
		nonTech bool
	}{
		{"omitted", `{}`, "yanfa-erp", false},
		{"null_with_settings", `{"display_name":null,"settings":{"non_tech_mode":true}}`, "yanfa-erp", true},
		{"trimmed_with_unchanged_schema_model", `{"display_name":" 新名称 \n","selected_collections":"orders","preferred_llm_model":"fixture-model"}`, "新名称", true},
		{"omitted_with_settings", `{"settings":{"non_tech_mode":false}}`, "新名称", false},
		{"blank_clears", `{"display_name":" \t\n　 "}`, "", false},
		{"null_after_clear", `{"display_name":null}`, "", false},
		{"set_again", `{"display_name":"yanfa-erp"}`, "yanfa-erp", false},
		{"empty_clears", `{"display_name":""}`, "", false},
	} {
		t.Run(step.name, func(t *testing.T) {
			response := displayNamePatch(t, service, ownerID, created.ID, step.body)
			if response.DisplayName != step.want {
				t.Fatalf("PATCH response name=%q, want %q", response.DisplayName, step.want)
			}
			stored := displayNameAssertReload(t, service, collection, ownerID, created.ID, step.want, "bi_agent")
			if !reflect.DeepEqual(stored.Connection, baseline.Connection) || stored.SelectedCollections != baseline.SelectedCollections ||
				!reflect.DeepEqual(stored.PreferredLLMModel, baseline.PreferredLLMModel) {
				t.Fatal("metadata PATCH changed encrypted physical identity, cached schema, selected collections or preferred model")
			}
			if stored.SelectionRevision != baseline.SelectionRevision || response.SelectionRevision != baseline.SelectionRevision {
				t.Fatalf("metadata PATCH advanced selection revision: stored=%d response=%d baseline=%d", stored.SelectionRevision, response.SelectionRevision, baseline.SelectionRevision)
			}
			wantSettings := baseline.Settings
			wantSettings.NonTechMode = step.nonTech
			if stored.Settings != wantSettings {
				t.Fatalf("settings=%#v, want %#v", stored.Settings, wantSettings)
			}
		})
	}
}

func TestChatDisplayNameLegacyPersistence(t *testing.T) {
	service, collection := displayNameMongoService(t)
	ownerID := primitive.NewObjectID().Hex()
	for _, legacy := range []struct {
		name   string
		update bson.M
	}{
		{"missing", bson.M{"$unset": bson.M{"display_name": ""}}},
		{"null", bson.M{"$set": bson.M{"display_name": nil}}},
	} {
		t.Run(legacy.name, func(t *testing.T) {
			created, status, err := service.CreateWithoutConnectionPing(ownerID,
				displayNameCreateRequest(t, "postgresql", "bi_agent", ""))
			if err != nil || status != http.StatusCreated {
				t.Fatalf("create: status=%d error=%v", status, err)
			}
			stored := displayNameStored(t, collection, created.ID)
			if _, err := collection.UpdateOne(context.Background(), bson.M{"_id": stored.ID}, legacy.update); err != nil {
				t.Fatal(err)
			}
			displayNameAssertReload(t, service, collection, ownerID, created.ID, "", "bi_agent")
			displayNamePatch(t, service, ownerID, created.ID, `{"display_name":null,"settings":{"auto_execute_query":false}}`)
			displayNameAssertReload(t, service, collection, ownerID, created.ID, "", "bi_agent")
			displayNamePatch(t, service, ownerID, created.ID, `{"display_name":"yanfa-erp"}`)
			displayNameAssertReload(t, service, collection, ownerID, created.ID, "yanfa-erp", "bi_agent")
		})
	}
}

func TestChatDisplayNameDuplicateOwnership(t *testing.T) {
	service, collection := displayNameMongoService(t)
	ownerID, foreignOwnerID := primitive.NewObjectID().Hex(), primitive.NewObjectID().Hex()
	original, status, err := service.CreateWithoutConnectionPing(ownerID,
		displayNameCreateRequest(t, "postgresql", "bi_agent", `,"display_name":"yanfa-erp"`))
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create original: status=%d error=%v", status, err)
	}
	foreign, status, err := service.CreateWithoutConnectionPing(foreignOwnerID,
		displayNameCreateRequest(t, "postgresql", "independent_fixture", `,"display_name":"yanfa-erp"`))
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create foreign: status=%d error=%v", status, err)
	}
	duplicate, status, err := service.Duplicate(ownerID, original.ID, false, false)
	if err != nil || status != http.StatusOK {
		t.Fatalf("duplicate: status=%d error=%v", status, err)
	}
	if duplicate.ID == original.ID || duplicate.ID == foreign.ID {
		t.Fatal("same display names merged Chat identities")
	}
	originalStored := displayNameAssertReload(t, service, collection, ownerID, original.ID, "yanfa-erp", "bi_agent")
	duplicateStored := displayNameAssertReload(t, service, collection, ownerID, duplicate.ID, "yanfa-erp", "bi_agent")
	displayNameAssertReload(t, service, collection, foreignOwnerID, foreign.ID, "yanfa-erp", "independent_fixture")
	if !reflect.DeepEqual(duplicateStored.Connection, originalStored.Connection) {
		t.Fatal("duplicate changed encrypted physical connection")
	}
	for _, target := range []string{original.ID, duplicate.ID} {
		if _, status, err := service.GetByID(foreignOwnerID, target); status != http.StatusForbidden || err == nil {
			t.Fatalf("same name granted read access: status=%d error=%v", status, err)
		}
		var request dtos.UpdateChatRequest
		if err := json.Unmarshal([]byte(`{"display_name":"foreign rename"}`), &request); err != nil {
			t.Fatal(err)
		}
		if _, status, err := service.Update(foreignOwnerID, target, &request); status != http.StatusForbidden || err == nil {
			t.Fatalf("same name granted update access: status=%d error=%v", status, err)
		}
		if _, status, err := service.Duplicate(foreignOwnerID, target, false, false); status != http.StatusForbidden || err == nil {
			t.Fatalf("same name granted duplicate access: status=%d error=%v", status, err)
		}
	}
	listed, status, err := service.List(ownerID, 1, 100)
	if err != nil || status != http.StatusOK || listed.Total != 2 || len(listed.Chats) != 2 {
		t.Fatalf("owner list merged names or included foreign Chat: status=%d error=%v response=%#v", status, err, listed)
	}
	displayNamePatch(t, service, ownerID, duplicate.ID, `{"display_name":""}`)
	displayNameAssertReload(t, service, collection, ownerID, duplicate.ID, "", "bi_agent")
	displayNameAssertReload(t, service, collection, ownerID, original.ID, "yanfa-erp", "bi_agent")
	displayNameAssertReload(t, service, collection, foreignOwnerID, foreign.ID, "yanfa-erp", "independent_fixture")
}
