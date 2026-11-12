package services_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/apis/handlers"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/services"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/sourcegate"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Real owned Chat HTTP + Mongo metadata. No PG/Redis/model/ERP is required or used.
func TestSourceClassificationOwnedChatHTTP(t *testing.T) {
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("UNVERIFIED: explicit isolated ASSISTANT_TEST_MONGO_URI is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(time.Second).SetRegistry(mongodb.BSONRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	database := client.Database("source_classification_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() { _ = database.Drop(context.Background()) })
	store := repositories.NewChatRepository(&mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: database.Name()}}, nil)
	oldKey := config.Env.SchemaEncryptionKey
	config.Env.SchemaEncryptionKey = "0123456789abcdef0123456789abcdef"
	t.Cleanup(func() { config.Env.SchemaEncryptionKey = oldKey })
	manager, err := dbmanager.NewManager(nil, config.Env.SchemaEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	gate, err := sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Close() })

	// A disconnected real registry makes any accidental classification-time ERP IO fail.
	registryClient, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	registry := repositories.NewERPSourceRepository(registryClient.Database(database.Name()))
	if err := registryClient.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	port, reader := "19999", "isolated_reader"
	connection := models.Connection{Type: "postgresql", Host: "erp-classification.invalid", Port: &port, Username: &reader, Database: "bi_agent"}
	sourceID, owner := uuid.NewString(), primitive.NewObjectID()
	policy, err := services.NewSourceAccessService(registry, gate, config.ERPSyncConfig{
		InstanceID: "classification-http", DeploymentUID: uint32(os.Getuid()), LockDirectory: directory,
		Associations: []config.ERPAssociation{{SourceID: sourceID, EndpointFingerprints: []string{services.EndpointFingerprint(connection)}, ApprovalRef: "isolated-classification-only"}},
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	service := services.NewChatService(store, manager, nil, nil, nil, nil, nil, nil, nil, nil, policy)
	handler := handlers.NewChatHandler(service)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("userID", c.GetHeader("X-Isolated-Owner")); c.Next() })
	router.GET("/api/chats", handler.List)
	router.GET("/api/chats/:id", handler.GetByID)
	router.POST("/api/chats/:id/connect", handler.ConnectDB)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	read := func(id string, viewer primitive.ObjectID) (int, dtos.ChatResponse) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, server.URL+"/api/chats/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Isolated-Owner", viewer.Hex())
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var envelope struct {
			Success bool              `json:"success"`
			Data    dtos.ChatResponse `json:"data"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, envelope.Data
	}
	for _, row := range []struct {
		name, kind string
		sticky     bool
		bound      bool
		different  bool
	}{
		{"registered", "registered_erp", true, true, false},
		{"directory_candidate_without_binding", "registered_erp", false, false, false},
		{"sticky_empty_id", "unresolved_erp", true, false, true},
		{"independent_during_registry_outage", "independent", false, false, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			plain := connection.Clone()
			if row.different {
				plain.Host = "unrelated-classification.invalid"
			}
			chat := models.NewChat(owner, plain, models.DefaultChatSettings())
			chat.ERPAssociated = row.sticky
			if row.bound {
				chat.SourceID, chat.SourceBindingRevision, chat.SourceBindingConfirmed = sourceID, 7, true
			}
			if err := utils.EncryptConnection(&chat.Connection); err != nil {
				t.Fatal(err)
			}
			if err := store.Create(chat); err != nil {
				t.Fatal(err)
			}
			status, dto := read(chat.ID.Hex(), owner)
			if status != http.StatusOK || dto.SourceClassification != row.kind || dto.SourceID != chat.SourceID || dto.SourceBindingRevision != chat.SourceBindingRevision || dto.Connection.Host != plain.Host {
				t.Fatalf("classification/identity HTTP status=%d dto=%+v", status, dto)
			}
			var stored models.Chat
			if err := database.Collection("chats").FindOne(ctx, bson.M{"_id": chat.ID}).Decode(&stored); err != nil {
				t.Fatal(err)
			}
			if stored.SourceID != chat.SourceID || stored.SourceBindingRevision != chat.SourceBindingRevision || stored.ERPAssociated != chat.ERPAssociated || stored.Connection.Host != chat.Connection.Host {
				t.Fatal("classification changed persisted binding or decrypted the stored connection in place")
			}
			if row.kind != "independent" {
				request, err := http.NewRequest(http.MethodPost, server.URL+"/api/chats/"+chat.ID.Hex()+"/connect", strings.NewReader(`{"stream_id":"isolated-classification"}`))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("X-Isolated-Owner", owner.Hex())
				request.Header.Set("Content-Type", "application/json")
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusConflict && response.StatusCode != http.StatusLocked && response.StatusCode != http.StatusServiceUnavailable {
					t.Fatalf("local classification granted execution during registry outage: %d", response.StatusCode)
				}
			}
			if row.kind == "unresolved_erp" {
				// A late metadata read still cannot downgrade sticky ERP with an empty ID.
				if _, err := database.Collection("chats").UpdateOne(ctx, bson.M{"_id": chat.ID}, bson.M{"$set": bson.M{"display_name": "普通连接"}}); err != nil {
					t.Fatal(err)
				}
				if _, late := read(chat.ID.Hex(), owner); late.SourceClassification != "unresolved_erp" || late.SourceID != "" {
					t.Fatal("late metadata granted independent authority")
				}
			}
		})
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/chats?page_size=100", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Isolated-Owner", owner.Hex())
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var list struct {
		Data dtos.ChatListResponse `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, item := range list.Data.Chats {
		kinds[item.SourceClassification]++
	}
	if response.StatusCode != http.StatusOK || kinds["registered_erp"] != 2 || kinds["unresolved_erp"] != 1 || kinds["independent"] != 1 {
		t.Fatalf("owned list classification during registry outage: status=%d kinds=%v", response.StatusCode, kinds)
	}
}
