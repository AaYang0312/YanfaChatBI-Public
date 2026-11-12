package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/services"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/sourcegate"
)

func TestDataSyncHTTPReplayPermissionsAndSafeStatus(t *testing.T) {
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("UNVERIFIED: isolated ASSISTANT_TEST_MONGO_URI required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	if client.Ping(ctx, nil) != nil {
		t.Fatal("isolated Mongo unavailable")
	}
	database := client.Database("data_sync_http_" + primitive.NewObjectID().Hex())
	defer database.Drop(context.Background())
	chats := repositories.NewChatRepository(&mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: database.Name()}}, nil)
	registry := repositories.NewERPSourceRepository(database)
	if registry.EnsureIndexes(ctx) != nil {
		t.Fatal("registry index setup failed")
	}
	jobs := repositories.NewDataSyncRepository(database)
	if jobs.EnsureIndexes(ctx) != nil {
		t.Fatal("job index setup failed")
	}
	directory := t.TempDir()
	os.Chmod(directory, 0700)
	gate, err := sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	cfg := config.ERPSyncConfig{InstanceID: "t39-http-isolated", DeploymentUID: uint32(os.Getuid()), LockDirectory: directory}
	policy, err := services.NewSourceAccessService(registry, gate, cfg, chats)
	if err != nil {
		t.Fatal(err)
	}
	owner, viewer := primitive.NewObjectID(), primitive.NewObjectID()
	port, reader := "18538", "isolated-reader"
	connection := models.Connection{Type: "postgresql", Host: "127.0.0.1", Port: &port, Username: &reader, Database: "bi_agent"}
	fingerprint := services.ConnectionFingerprint(connection)
	source, err := policy.RegisterApproved(ctx, models.ERPSource{EnvironmentRef: "isolated-http", PhysicalDatabase: "bi_agent", ApprovedEndpointAliases: []string{services.EndpointFingerprint(connection)}, ApprovedConnectionFingerprints: []string{fingerprint}, ReaderRoleRefs: []string{"isolated-reader-ref"}, WriterRoleRef: "isolated-writer-ref", ComponentManifestRef: "no-official-release", ApprovalRefs: map[string]string{"Q15-02": "test-only"}, ViewAccountIDs: []primitive.ObjectID{owner, viewer}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Associations = []config.ERPAssociation{{SourceID: source.SourceID, EndpointFingerprints: source.ApprovedEndpointAliases, ApprovalRef: "isolated"}}
	policy, err = services.NewSourceAccessService(registry, gate, cfg, chats)
	if err != nil {
		t.Fatal(err)
	}
	makeChat := func(account primitive.ObjectID) *models.Chat {
		chat := models.NewChat(account, connection, models.DefaultChatSettings())
		chat.SourceID = source.SourceID
		chat.ERPAssociated = true
		chat.SourceBindingConfirmed = true
		chat.SourceBindingRevision = 1
		chat.SourceConnectionFingerprint = fingerprint
		binding := models.ERPChatBinding{ChatID: chat.ID, OwnerID: account, ConnectionFingerprint: fingerprint, BindingRevision: 1, OperationID: uuid.NewString()}
		if registry.PutBindingCAS(ctx, source.SourceID, source.ConfigRevision, nil, binding) != nil || registry.ConfirmBindingCAS(ctx, source.SourceID, source.ConfigRevision, binding) != nil || chats.Create(chat) != nil {
			t.Fatal("binding fixture failed")
		}
		return chat
	}
	first, second := makeChat(owner), makeChat(viewer)
	request := uuid.NewString()
	job := &models.DataSyncJob{SchemaVersion: 1, JobID: uuid.NewString(), SourceID: source.SourceID, InitiatingOwnerID: owner, InitiatingChatID: first.ID, RequestID: request, RequestFingerprint: strings.Repeat("a", 64), RunnerVersion: models.DataSyncRunnerVersion, Mode: "incremental", ComponentManifestRef: "old-formal-reference", ComponentDigest: strings.Repeat("b", 64), BridgeVersion: "historical/1", ConfigRevision: 1, ScopeDigest: strings.Repeat("c", 64), ExpectedShops: []string{"1", "2", "3", "4", "5", "6", "7"}, SourceMapping: map[string]string{"1": "orders", "2": "orders", "3": "orders", "4": "orders", "5": "orders", "6": "orders", "7": "orders"}, AftersaleSource: "aftersales", InstanceID: "predecessor", OperationID: uuid.NewString(), BindingRevision: 1, ConnectionFingerprint: fingerprint, Status: models.DataSyncCreated, Stage: "preparing", CreatedAt: time.Now().Add(-time.Hour)}
	if jobs.Create(ctx, job) != nil {
		t.Fatal("durable historical job setup failed")
	}
	next := *job
	next.Status = models.DataSyncInterrupted
	next.Stage = "verifying"
	next.Revision++
	next.ExitConfirmed = true
	next.PendingOutcome = next.Status
	finished := time.Now().Add(-time.Minute)
	next.FinishedAt = &finished
	if jobs.Advance(ctx, job, &next) != nil {
		t.Fatal("historical terminal setup failed")
	}
	job = &next
	if registry.UpdateStateCAS(ctx, source.SourceID, 0, "", repositories.ERPSourceState{Phase: models.ERPSourceIdle, LastReleasedOperationID: job.OperationID, RecoveryVerifiedInstanceID: cfg.InstanceID}) != nil || jobs.ConfirmRelease(ctx, job) != nil {
		t.Fatal("historical release setup failed")
	}
	service := services.NewDeploymentDataSyncService(policy, jobs)
	defer service.Shutdown(context.Background())
	handler := NewDataSyncHandler(service)
	router := gin.New()
	// Auth middleware supplies this field in production; this fixture only sets
	// authenticated identities and exercises the actual handlers/service/Mongo.
	router.Use(func(c *gin.Context) {
		if value := c.GetHeader("X-Test-Owner"); value != "" {
			c.Set("userID", value)
		}
	})
	router.GET("/api/chats/:id/data-sync/status", handler.Status)
	router.POST("/api/chats/:id/data-sync/jobs", handler.CreateJob)
	router.GET("/api/chats/:id/data-sync/jobs/:jobID", handler.GetJob)
	server := httptest.NewServer(router)
	defer server.Close()
	call := func(account, method, path, body string) (int, map[string]interface{}) {
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if account != "" {
			req.Header.Set("X-Test-Owner", account)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Error(err)
		}
		var envelope map[string]interface{}
		if json.Unmarshal(raw, &envelope) != nil {
			t.Error("invalid actual envelope")
		}
		if strings.Contains(string(raw), "initiating_owner_id") || strings.Contains(string(raw), "predecessor") || strings.Contains(string(raw), "component_manifest_ref") {
			t.Error("private job metadata leaked")
		}
		return response.StatusCode, envelope
	}
	root := "/api/chats/" + first.ID.Hex() + "/data-sync"
	other := "/api/chats/" + second.ID.Hex() + "/data-sync"
	if code, _ := call("", "GET", root+"/status", ""); code != 401 {
		t.Fatalf("auth=%d", code)
	}
	if code, _ := call(viewer.Hex(), "GET", root+"/status", ""); code != 404 {
		t.Fatalf("foreign chat=%d", code)
	}
	for _, body := range []string{`{}`, `{"request_id":"bad"}`, `{"request_id":"` + request + `","mode":"incremental"}`, `{"request_id":"` + request + `","request_id":"` + request + `"}`, `{"request_id":"` + strings.ToUpper(request) + `"}`} {
		if code, _ := call(owner.Hex(), "POST", root+"/jobs", body); code != 400 {
			t.Fatalf("strict request accepted: %d", code)
		}
	}
	if code, _ := call(viewer.Hex(), "POST", other+"/jobs", `{"request_id":"`+uuid.NewString()+`"}`); code != 403 {
		t.Fatalf("view/copy became start permission: %d", code)
	}
	var work sync.WaitGroup
	for range 8 {
		work.Add(1)
		go func() {
			defer work.Done()
			code, envelope := call(owner.Hex(), "POST", root+"/jobs", `{"request_id":"`+request+`"}`)
			if code != 202 || envelope["data"].(map[string]interface{})["job_id"] != job.JobID {
				t.Error("terminal replay did not precede default deny")
			}
		}()
	}
	work.Wait()
	code, envelope := call(viewer.Hex(), "GET", other+"/jobs/"+job.JobID, "")
	if code != 200 {
		t.Fatalf("same-source view=%d", code)
	}
	data := envelope["data"].(map[string]interface{})
	if _, ok := data["request_id"]; ok {
		t.Fatal("other owner received request_id")
	}
	if _, ok := data["initiating_chat_id"]; ok {
		t.Fatal("other owner received initiating Chat")
	}
	code, envelope = call(viewer.Hex(), "GET", other+"/status?request_id="+request, "")
	if code != 200 || envelope["data"].(map[string]interface{})["request_match"] != "not_found" {
		t.Fatal("request lookup was not owner/source exact")
	}
	for _, query := range []string{"?request_id=", "?request_id=" + request + "&request_id=" + request, "?source_id=forged"} {
		if code, _ := call(owner.Hex(), "GET", root+"/status"+query, ""); code != 400 {
			t.Fatal("invalid status request_id/target was accepted")
		}
	}
	foreign := *job
	foreign.JobID = uuid.NewString()
	foreign.SourceID = uuid.NewString()
	foreign.RequestID = uuid.NewString()
	foreign.Status = models.DataSyncCreated
	foreign.Stage = "preparing"
	foreign.Revision = 0
	foreign.PendingOutcome = ""
	foreign.ExitConfirmed = false
	foreign.FinishedAt = nil
	foreign.ReleaseConfirmed = false
	if err := jobs.Create(ctx, &foreign); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(owner.Hex(), "GET", root+"/jobs/"+foreign.JobID, ""); code != 404 {
		t.Fatal("cross-source job was exposed")
	}
	current, err := registry.Get(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	current.ConfigRevision++
	current.ComponentManifestRef = "changed-no-release"
	if registry.UpdateConfigCAS(ctx, current.SourceID, current.ConfigRevision-1, current) != nil {
		t.Fatal("config fixture change failed")
	}
	code, envelope = call(owner.Hex(), "POST", root+"/jobs", `{"request_id":"`+request+`"}`)
	if code != 202 || envelope["data"].(map[string]interface{})["job_id"] != job.JobID {
		t.Fatal("late POST reinterpreted old request after config change")
	}
	stored, err := jobs.Get(ctx, job.JobID)
	if err != nil || stored.Revision != job.Revision || stored.ComponentManifestRef != job.ComponentManifestRef {
		t.Fatal("HTTP replay mutated durable terminal")
	}
}
