package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"yanfachatbi/config"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/mongodb"
	redisrepo "yanfachatbi/pkg/redis"
	"yanfachatbi/pkg/sourcegate"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	assistantPrivacyChildEnv = "ASSISTANT_PRIVACY_AC33_CHILD"
	assistantPrivacyDone     = "AC33 runtime boundaries exercised"
	assistantPrivacySales    = "918273645.37"
	assistantPrivacyCost     = "765432109.19"
	assistantPrivacyBusiness = "ac33-private-wholesale-margin-b482e6d9"
	assistantPrivacyToken    = "ac33-synthetic-credential-7fa312c8e69b"
)

// Each scenario runs in a fresh copy of this test binary. CombinedOutput captures
// actual stdout/stderr, including the unchanged process-global loggers, without
// redirecting a logger used by another test or supplying synthetic log output.
func TestAssistantPrivacyRuntimeAC33(t *testing.T) {
	if scenario := os.Getenv(assistantPrivacyChildEnv); scenario != "" {
		testAssistantPrivacyScenario(t, scenario)
		t.Log(assistantPrivacyDone)
		return
	}
	for _, scenario := range []string{"source_error", "encryption_error", "storage_error", "corrupt_ciphertext"} {
		t.Run(scenario, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatalf("locate test binary: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestAssistantPrivacyRuntimeAC33$", "-test.count=1", "-test.v")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, assistantPrivacyChildEnv+"=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, assistantPrivacyChildEnv+"="+scenario)
			output, runErr := cmd.CombinedOutput()
			assertAssistantPrivacyAbsent(t, "process stdout/stderr/log output", output)
			if runErr != nil {
				t.Fatalf("privacy child failed: %v\n%s", runErr, output)
			}
			if !bytes.Contains(output, []byte(assistantPrivacyDone)) {
				t.Fatal("child did not finish the runtime boundary assertions")
			}
		})
	}
}

func testAssistantPrivacyScenario(t *testing.T, scenario string) {
	ctx := context.Background()
	cipher := &assistantPrivacyCipher{inner: assistantServiceCipher(t)}
	source := &assistantPrivacySource{AssistantSourceRepository: NewAssistantFixtureSource()}
	store := &assistantPrivacyCollection{regressionRunStore: newRegressionRunStore()}
	switch scenario {
	case "source_error":
		source.failure = errors.New("source rejected finance_account=" + assistantPrivacyBusiness + " credential=" + assistantPrivacyToken)
	case "encryption_error":
		cipher.failEncrypt = true
	case "storage_error":
		store.failure = errors.New("write failed mongodb://" + assistantPrivacyBusiness + ":" + assistantPrivacyToken + "@isolated.invalid/assistant")
	case "corrupt_ciphertext":
	default:
		t.Fatalf("unknown privacy scenario %q", scenario)
	}
	ownerID, chatID := primitive.NewObjectID(), primitive.NewObjectID()
	access := models.AccessContext{OwnerID: ownerID.Hex(), ChatID: chatID.Hex()}
	messages := &regressionMessages{}
	notifier := &regressionNotifier{}
	repo := repositories.NewAssistantRunRepository(store, cipher)
	service := NewAssistantService(
		&assistantServiceChatLookup{chat: &models.Chat{UserID: ownerID, Base: models.Base{ID: chatID}}},
		messages, repo, cipher, nil, source, nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantClock(AssistantFixtureClock),
		WithAssistantInstanceID("proc-privacy"),
		WithAssistantRunNotifier(notifier),
	).(*assistantService)

	// Seed a valid created run through the production repository, then invoke
	// Execute synchronously so every output and persistence assertion happens
	// after the error consumer has finished; no goroutine polling is needed.
	record, err := repo.CreateOrGet(ctx, models.RunRecord{
		SchemaVersion: models.AssistantSchemaVersion,
		RunID:         "run-privacy-" + scenario, OwnerID: access.OwnerID, ChatID: access.ChatID,
		RequestID: "req-privacy-" + scenario, RequestFingerprint: "fp-privacy-" + scenario,
		NormalizedInput: assistantFixtureWorkflowRequest(), MessageID: primitive.NewObjectID().Hex(),
		Status: models.RunStatusCreated, Stage: models.RunStageCreated,
		CreatedAt: AssistantFixtureClock(), UpdatedAt: AssistantFixtureClock(),
		ExecutingInstance: "proc-privacy",
		Version:           models.RunVersion{SchemaVersion: models.AssistantSchemaVersion, MetricVersion: assistantMetricVersion},
	})
	if err != nil {
		t.Fatalf("seed created run: %v", err)
	}
	messageID, err := primitive.ObjectIDFromHex(record.MessageID)
	if err != nil {
		t.Fatalf("seed message id: %v", err)
	}
	if err := service.persistReportMessage(ctx, access, record, messageID); err != nil {
		t.Fatalf("persist report reference: %v", err)
	}
	service.located.Remember(record)
	execErr := service.Execute(ctx, record.RunID)
	stored := store.must(t, record.RunID)
	if source.reads != 1 {
		t.Fatalf("sales source reads = %d, want 1", source.reads)
	}
	assertAssistantPrivacyAbsent(t, "stored BSON", store.raw(t, record.RunID))
	assertAssistantPrivacyAbsent(t, "stored report message", assistantPrivacyJSON(t, messages.byRun(t, record.RunID)))
	if len(stored.ReportPlaintext) != 0 {
		t.Fatal("stored run retained transient report plaintext")
	}

	if scenario == "corrupt_ciphertext" {
		if execErr != nil || stored.Status != models.RunStatusCompleted || len(stored.EncryptedResult) == 0 {
			t.Fatal("successful control did not persist an encrypted completed report")
		}
		assertAssistantPrivacyReport(t, cipher.plain)
		view, err := service.GetRun(ctx, access, record.RunID)
		if err != nil || view.Report == nil {
			t.Fatal("successful encrypted control could not be read")
		}
		assertAssistantPrivacyReport(t, assistantPrivacyJSON(t, view.Report))
		events := notifier.published()
		if len(events) != 1 || events[0].Status != models.RunStatusCompleted || events[0].Report == nil {
			t.Fatal("successful control did not exercise the report notification boundary")
		}
		assertAssistantPrivacyReport(t, assistantPrivacyJSON(t, events[0].Report))

		// Alter the authenticated AES ciphertext in the stored row, rather than
		// making a fake decryptor return arbitrary JSON or an invented error.
		stored.EncryptedResult = append([]byte(nil), stored.EncryptedResult...)
		stored.EncryptedResult[len(stored.EncryptedResult)-1] ^= 0x80
		raw, err := bson.Marshal(stored)
		if err != nil {
			t.Fatalf("encode corrupt row: %v", err)
		}
		store.original[record.RunID] = stored.Clone()
		store.rows[record.RunID] = raw
		cipher.decrypts = 0
		before := append([]byte(nil), raw...)
		for _, read := range []func() (models.RunView, error){
			func() (models.RunView, error) { return service.GetRun(ctx, access, record.RunID) },
			func() (models.RunView, error) { return service.CancelRun(ctx, access, record.RunID) },
		} {
			view, readErr := read()
			if readErr == nil || view.RunID != "" || view.Report != nil {
				t.Fatal("corrupt ciphertext returned a run or report instead of an error")
			}
			assertAssistantPrivacyAbsent(t, "service read error", []byte(readErr.Error()))
			assertAssistantPrivacyToolError(t, readErr)
		}
		service.publishRunUpdate(ctx, stored)
		if cipher.decrypts != 3 || cipher.decryptFailure == nil {
			t.Fatal("read/cancel/notification did not reach the actual AES decryption failure")
		}
		if len(notifier.published()) != 1 {
			t.Fatal("corrupt report emitted another SSE notification")
		}
		if !bytes.Equal(before, store.raw(t, record.RunID)) {
			t.Fatal("corrupt report read changed the persisted terminal row")
		}
		return
	}

	if execErr == nil {
		t.Fatal("injected dependency failure did not fail Execute")
	}
	assertAssistantPrivacyToolError(t, execErr)
	if len(stored.EncryptedResult) != 0 {
		t.Fatal("failed execution stored report bytes")
	}
	if len(notifier.published()) != 0 {
		t.Fatal("failed execution emitted a completed report notification")
	}
	view, err := service.GetRun(ctx, access, record.RunID)
	if err != nil || view.Report != nil {
		t.Fatal("failed execution could not be read safely without a report")
	}
	if scenario == "source_error" {
		if !errors.Is(execErr, source.failure) || stored.Status != models.RunStatusFailed || stored.Stage != models.RunStageFailed || stored.SafeError == nil || stored.SafeError.Code != "failed" {
			t.Fatal("source error did not reach a failed terminal row with a safe error")
		}
		if cipher.encrypts != 0 {
			t.Fatal("source failure reached report encryption")
		}
		// Exercise the same status publication consumer on the stored failure,
		// including SafeError, even though Execute currently sends no failed frame.
		service.publishRunUpdate(ctx, stored)
		events := notifier.published()
		if len(events) != 1 || events[0].Status != models.RunStatusFailed || events[0].Report != nil || events[0].SafeError == nil {
			t.Fatal("failed status publication lost its safe error boundary")
		}
		assertAssistantPrivacyAbsent(t, "failed SSE DTO", assistantPrivacyJSON(t, dtos.NewRunView(events[0])))
	} else {
		// Failed encryption/write must leave the last durable running revision;
		// no completed status or fabricated terminal result may escape.
		if stored.Status != models.RunStatusRunning || stored.Stage != models.RunStageRunning || stored.Revision != record.Revision+1 {
			t.Fatal("failed report persistence changed the durable running revision")
		}
		if cipher.encrypts != 1 {
			t.Fatal("report persistence did not reach encryption exactly once")
		}
		assertAssistantPrivacyReport(t, cipher.plain)
		if scenario == "encryption_error" {
			if !errors.Is(execErr, cipher.encryptFailure) || store.completionWrites != 0 {
				t.Fatal("encryption failure reached storage or lost its actual crypto cause")
			}
		} else if !errors.Is(execErr, store.failure) || store.completionWrites != 1 {
			t.Fatal("storage failure did not reach the collection update")
		}
		assertAssistantPrivacyAbsent(t, "attempted persistence update", store.completionUpdate)
	}
	assertAssistantPrivacyAbsent(t, "GET run DTO", assistantPrivacyJSON(t, dtos.NewRunView(view)))
	assertAssistantPrivacyAbsent(t, "persisted safe run fields", assistantPrivacyJSON(t, stored))
}

// The synthetic source changes actual financial inputs, so logging a computed
// report, table row or metric value is detectable even without logging an error.
type assistantPrivacySource struct {
	repositories.AssistantSourceRepository
	reads   int
	failure error
}

func (s *assistantPrivacySource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.reads++
	if s.failure != nil {
		return models.FactSnapshot{}, s.failure
	}
	snapshot, err := s.AssistantSourceRepository.ReadSalesFacts(ctx, query)
	if err == nil && len(snapshot.Rows) > 0 {
		snapshot.Rows[0].Sales = decimal.RequireFromString(assistantPrivacySales)
		snapshot.Rows[0].Cost = decimal.RequireFromString(assistantPrivacyCost)
	}
	return snapshot, err
}

// Diagnostic wrapping models dependencies that include a request body or a
// connection credential in an error. AES itself still performs encryption and
// authentication; the test never supplies fake report ciphertext or fake logs.
type assistantPrivacyCipher struct {
	inner          repositories.ReportCipher
	plain          []byte
	failEncrypt    bool
	encrypts       int
	decrypts       int
	encryptFailure error
	decryptFailure error
}

func (c *assistantPrivacyCipher) EncryptReport(plain []byte) ([]byte, error) {
	c.encrypts++
	c.plain = append([]byte(nil), plain...)
	if c.failEncrypt {
		_, c.encryptFailure = (&repositories.AESReportCipher{}).EncryptReport(plain)
		return nil, fmt.Errorf("encrypt finance_account=%s credential=%s report=%s: %w", assistantPrivacyBusiness, assistantPrivacyToken, plain, c.encryptFailure)
	}
	return c.inner.EncryptReport(plain)
}

func (c *assistantPrivacyCipher) DecryptReport(ciphertext []byte) ([]byte, error) {
	c.decrypts++
	plain, err := c.inner.DecryptReport(ciphertext)
	if err != nil {
		c.decryptFailure = err
		return nil, fmt.Errorf("decrypt finance_account=%s credential=%s: %w", assistantPrivacyBusiness, assistantPrivacyToken, err)
	}
	return plain, nil
}

// Only the completed write fails. The repository must encrypt before reaching
// this Mongo collection boundary, and must never publish the failed update.
type assistantPrivacyCollection struct {
	*regressionRunStore
	failure          error
	completionWrites int
	completionUpdate []byte
}

func (s *assistantPrivacyCollection) FindOneAndUpdate(ctx context.Context, filter interface{}, update interface{}, opts ...*options.FindOneAndUpdateOptions) *mongo.SingleResult {
	if set, ok := update.(bson.M)["$set"].(bson.M); ok && set["status"] == models.RunStatusCompleted {
		s.completionWrites++
		raw, err := bson.Marshal(update)
		if err != nil {
			return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
		}
		s.completionUpdate = raw
		if s.failure != nil {
			return mongo.NewSingleResultFromDocument(bson.D{}, s.failure, nil)
		}
	}
	return s.regressionRunStore.FindOneAndUpdate(ctx, filter, update, opts...)
}

func assertAssistantPrivacyToolError(t *testing.T, err error) {
	t.Helper()
	tool := assistantToolServiceError(llm.ToolCall{ID: "call-privacy", Name: llm.AnalyzeProductToolName}, err)
	if tool == nil || !tool.IsError {
		t.Fatal("service failure did not produce an error tool result")
	}
	var content assistantToolContent
	if json.Unmarshal([]byte(tool.Content), &content) != nil || content.Status != "error" || content.Summary == "" || content.Message == "" {
		t.Fatal("service failure did not produce a safe structured tool error")
	}
	assertAssistantPrivacyAbsent(t, "tool error JSON", []byte(tool.Content))
}

func assistantPrivacyJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal privacy boundary: %v", err)
	}
	return raw
}

func assertAssistantPrivacyReport(t *testing.T, plain []byte) {
	t.Helper()
	var report models.AnalysisReport
	if err := json.Unmarshal(plain, &report); err != nil {
		t.Fatal("cipher did not receive a serialized service report")
	}
	for _, canary := range []string{assistantPrivacySales, assistantPrivacyCost} {
		if !bytes.Contains(plain, []byte(canary)) {
			t.Fatal("actual computed report did not carry a financial canary")
		}
	}
	if report.ReportID == "" || len(report.Table.Rows) == 0 {
		t.Fatal("financial canaries were not part of a populated report")
	}
}

func assertAssistantPrivacyAbsent(t *testing.T, boundary string, raw []byte) {
	t.Helper()
	for _, canary := range []string{
		assistantPrivacyBusiness, assistantPrivacyToken, assistantPrivacySales, assistantPrivacyCost,
		"918273705.37", "765432151.19", "152841536.18", "152841554.18",
	} {
		if bytes.Contains(raw, []byte(canary)) {
			t.Fatalf("sensitive synthetic canary escaped through %s", boundary)
		}
	}
}

const assistantPrivacyBackgroundChildEnv = "ASSISTANT_PRIVACY_BACKGROUND_CHILD"

func TestAssistantPrivacyBackgroundRuntimeAC33(t *testing.T) {
	if os.Getenv("ASSISTANT_TEST_MONGO_URI") == "" || os.Getenv("SOURCE_ACCESS_TEST_REDIS_ADDR") == "" {
		t.Skip("UNVERIFIED: background privacy requires explicit isolated Mongo and Redis inputs")
	}
	if scenario := os.Getenv(assistantPrivacyBackgroundChildEnv); scenario != "" {
		testAssistantPrivacyBackgroundScenario(t, scenario)
		t.Log(assistantPrivacyDone)
		return
	}
	for _, scenario := range []string{"source_error", "encryption_error", "storage_error", "gate_storage_error"} {
		t.Run(scenario, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestAssistantPrivacyBackgroundRuntimeAC33$", "-test.count=1", "-test.v")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, assistantPrivacyBackgroundChildEnv+"=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, assistantPrivacyBackgroundChildEnv+"="+scenario)
			output, runErr := cmd.CombinedOutput()
			assertAssistantPrivacyAbsent(t, "CreateRun background stdout/stderr/log output", output)
			if runErr != nil {
				t.Fatalf("background privacy child failed: %v\n%s", runErr, output)
			}
			if !bytes.Contains(output, []byte(assistantPrivacyDone)) {
				t.Fatal("background privacy child did not finish the real cleanup assertions")
			}
			if !bytes.Contains(output, []byte("run-background-privacy-"+scenario)) {
				t.Fatal("background failure lost its safe run diagnostic")
			}
		})
	}
}

type assistantPrivacyBackgroundExecutor struct {
	inner   AssistantExecutor
	entered chan context.Context
	resume  chan struct{}
	result  chan error
	calls   int
}

func (e *assistantPrivacyBackgroundExecutor) Execute(ctx context.Context, runID string) error {
	e.calls++
	e.entered <- ctx
	<-e.resume
	err := e.inner.Execute(ctx, runID)
	e.result <- err
	return err
}

// Normal calls use the actual Mongo collection. The selected write uses a
// separate client for that same collection, disconnected at the fault boundary;
// the diagnostic wraps the real driver's error, never a fabricated write result.
type assistantPrivacyBackgroundCollection struct {
	*mongo.Collection
	faultClient      *mongo.Client
	faultCollection  *mongo.Collection
	target           models.RunStatus
	entered          chan struct{}
	write            chan struct{}
	returned         chan error
	finish           chan struct{}
	failure          error
	attemptedUpdate  []byte
	runningWrites    int
	completionWrites int
	failureWrites    int
}

func (s *assistantPrivacyBackgroundCollection) FindOneAndUpdate(ctx context.Context, filter interface{}, update interface{}, opts ...*options.FindOneAndUpdateOptions) *mongo.SingleResult {
	set := update.(bson.M)["$set"].(bson.M)
	status, _ := set["status"].(models.RunStatus)
	switch status {
	case models.RunStatusRunning:
		s.runningWrites++
	case models.RunStatusCompleted:
		s.completionWrites++
	case models.RunStatusFailed:
		s.failureWrites++
	}
	if status != s.target {
		return s.Collection.FindOneAndUpdate(ctx, filter, update, opts...)
	}
	s.attemptedUpdate, _ = bson.Marshal(update)
	close(s.entered)
	<-s.write
	if err := s.faultClient.Disconnect(context.Background()); err != nil {
		s.returned <- err
		<-s.finish
		return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
	}
	actual := s.faultCollection.FindOneAndUpdate(ctx, filter, update, opts...).Err()
	if actual == nil {
		s.returned <- nil
		<-s.finish
		return mongo.NewSingleResultFromDocument(bson.D{}, errors.New("disconnected Mongo write unexpectedly succeeded"), nil)
	}
	s.failure = fmt.Errorf("write failed mongodb://%s:%s@isolated.invalid/assistant: %w", assistantPrivacyBusiness, assistantPrivacyToken, actual)
	s.returned <- actual
	<-s.finish
	return mongo.NewSingleResultFromDocument(bson.D{}, s.failure, nil)
}

func testAssistantPrivacyBackgroundScenario(t *testing.T, scenario string) {
	ctx := context.Background()
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetRegistry(mongodb.BSONRegistry()).SetServerSelectionTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Disconnect(context.Background()) })
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	database := client.Database("assistant_privacy_background_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() { database.Drop(context.Background()) })
	redisClient := goredis.NewClient(&goredis.Options{Addr: os.Getenv("SOURCE_ACCESS_TEST_REDIS_ADDR")})
	t.Cleanup(func() { redisClient.Close() })
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	chats := repositories.NewChatRepository(&mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: database.Name()}}, redisrepo.NewRedisRepositories(redisClient))
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	gate, err := sourcegate.New(directory, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() })
	registry := repositories.NewERPSourceRepository(database)
	if err := registry.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := config.ERPSyncConfig{InstanceID: "t38-background-privacy", DeploymentUID: uint32(os.Getuid()), LockDirectory: directory}
	policy, err := NewSourceAccessService(registry, gate, cfg, chats)
	if err != nil {
		t.Fatal(err)
	}
	owner := primitive.NewObjectID()
	connection := models.Connection{Type: "postgresql", Host: "isolated.invalid", Database: "bi_agent"}
	sourceRecord, err := policy.RegisterApproved(ctx, models.ERPSource{EnvironmentRef: "isolated-only", PhysicalDatabase: "bi_agent",
		ApprovedEndpointAliases: []string{EndpointFingerprint(connection)}, ApprovedConnectionFingerprints: []string{ConnectionFingerprint(connection)},
		ReaderRoleRefs: []string{"isolated-reader"}, WriterRoleRef: "isolated-writer", ComponentManifestRef: "isolated-only",
		ApprovalRefs: map[string]string{"Q15-02": "isolated-only"}, ViewAccountIDs: []primitive.ObjectID{owner}})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.UpdateStateCAS(ctx, sourceRecord.SourceID, 0, "", repositories.ERPSourceState{Phase: models.ERPSourceIdle, RecoveryVerifiedInstanceID: cfg.InstanceID}); err != nil {
		t.Fatal(err)
	}
	sourceRecord, err = registry.Get(ctx, sourceRecord.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	chat := models.NewChat(owner, connection, models.ChatSettings{})
	chat.ERPAssociated, chat.SourceBindingConfirmed, chat.SourceID, chat.SourceBindingRevision = true, true, sourceRecord.SourceID, 1
	chat.SourceConnectionFingerprint = ConnectionFingerprint(connection)
	binding := models.ERPChatBinding{ChatID: chat.ID, OwnerID: owner, ConnectionFingerprint: chat.SourceConnectionFingerprint, BindingRevision: 1, OperationID: uuid.NewString()}
	if err := registry.PutBindingCAS(ctx, sourceRecord.SourceID, sourceRecord.ConfigRevision, nil, binding); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfirmBindingCAS(ctx, sourceRecord.SourceID, sourceRecord.ConfigRevision, binding); err != nil {
		t.Fatal(err)
	}
	if err := chats.Create(chat); err != nil {
		t.Fatal(err)
	}
	cfg.Associations = []config.ERPAssociation{{SourceID: sourceRecord.SourceID, EndpointFingerprints: sourceRecord.ApprovedEndpointAliases, ApprovalRef: "isolated-only"}}
	policy, err = NewSourceAccessService(registry, gate, cfg, chats)
	if err != nil {
		t.Fatal(err)
	}
	cipher := &assistantPrivacyCipher{inner: assistantServiceCipher(t)}
	source := &assistantPrivacySource{AssistantSourceRepository: NewAssistantFixtureSource()}
	store := &assistantPrivacyBackgroundCollection{Collection: database.Collection("assistant_runs"), entered: make(chan struct{}), write: make(chan struct{}), returned: make(chan error, 1), finish: make(chan struct{})}
	switch scenario {
	case "source_error":
		source.failure = errors.New("source rejected finance_account=" + assistantPrivacyBusiness + " credential=" + assistantPrivacyToken)
	case "encryption_error":
		cipher.failEncrypt = true
	case "storage_error":
		store.target = models.RunStatusCompleted
	case "gate_storage_error":
		store.target = models.RunStatusFailed
	default:
		t.Fatal("unknown background privacy scenario")
	}
	if store.target != "" {
		store.faultClient, err = mongo.Connect(ctx, options.Client().ApplyURI(uri).SetRegistry(mongodb.BSONRegistry()).SetServerSelectionTimeout(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.faultClient.Disconnect(context.Background()) })
		if err := store.faultClient.Ping(ctx, nil); err != nil {
			t.Fatal(err)
		}
		store.faultCollection = store.faultClient.Database(database.Name()).Collection("assistant_runs")
	}
	if err := repositories.EnsureAssistantRunIndexes(ctx, store); err != nil {
		t.Fatal(err)
	}
	repo := repositories.NewAssistantRunRepository(store, cipher)
	notifier := &regressionNotifier{}
	executor := &assistantPrivacyBackgroundExecutor{entered: make(chan context.Context, 1), resume: make(chan struct{}), result: make(chan error, 1)}
	var beginOnce, writeOnce, finishOnce sync.Once
	begin := func() { beginOnce.Do(func() { close(executor.resume) }) }
	write := func() { writeOnce.Do(func() { close(store.write) }) }
	finish := func() { finishOnce.Do(func() { close(store.finish) }) }
	t.Cleanup(func() { begin(); write(); finish() })
	runID := "run-background-privacy-" + scenario
	service := NewAssistantService(NewAssistantChatRepository(chats), NewAssistantMessageRepository(chats), repo, cipher, NewAssistantFixtureCatalog(), source, executor, policy,
		WithAssistantClock(AssistantFixtureClock), WithAssistantInstanceID("t38-background-privacy"), WithAssistantRunIDGenerator(func() string { return runID }), WithAssistantRunNotifier(notifier)).(*assistantService)
	executor.inner = service
	access := models.AccessContext{OwnerID: owner.Hex(), ChatID: chat.ID.Hex()}
	request := assistantFixtureWorkflowRequest()
	request.RequestID = "req-background-privacy-" + scenario
	accepted, err := service.CreateRun(ctx, access, request)
	if err != nil || accepted.Status != models.RunStatusCreated || accepted.RunID != runID {
		t.Fatal("real CreateRun did not persist and accept the background run")
	}
	var runCtx context.Context
	select {
	case runCtx = <-executor.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("real startExecution did not call its transparent executor wrapper")
	}
	load := func() models.RunRecord {
		record, err := repo.GetOwned(ctx, access.OwnerID, access.ChatID, runID)
		if err != nil {
			t.Fatal("real run repository could not reload the background record")
		}
		return record
	}
	created := load()
	messageID, err := primitive.ObjectIDFromHex(created.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := chats.FindMessageByID(messageID)
	if err != nil || message == nil || message.AssistantRunID == nil || *message.AssistantRunID != runID || message.UserID != owner || message.ChatID != chat.ID {
		t.Fatal("background run lacks its real persistent report reference")
	}
	tryWriter := func() bool {
		lease, err := gate.Acquire(ctx, sourcegate.Target{SourceID: sourceRecord.SourceID, Device: sourceRecord.LockDevice, Inode: sourceRecord.LockInode}, sourcegate.Exclusive,
			sourcegate.Identity{SourceID: sourceRecord.SourceID, OperationID: uuid.NewString()})
		if errors.Is(err, sourcegate.ErrBusy) {
			return false
		}
		if err != nil {
			t.Fatal("real background source gate could not acquire EX")
		}
		lease.Close()
		return true
	}
	if tryWriter() {
		t.Fatal("background run released its original source reference before Execute")
	}
	if scenario == "gate_storage_error" {
		current, err := registry.Get(ctx, sourceRecord.SourceID)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.UpdateStateCAS(ctx, current.SourceID, current.StateRevision, "", repositories.ERPSourceState{Phase: models.ERPSourceRecovering, RecoveryVerifiedInstanceID: cfg.InstanceID}); err != nil {
			t.Fatal(err)
		}
	}
	begin()
	if store.target != "" {
		select {
		case <-store.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("background execution did not reach the real repository fault boundary")
		}
		before := load()
		if tryWriter() || len(notifier.published()) != 0 {
			t.Fatal("background write released its lease or published before persistence")
		}
		write()
		select {
		case actual := <-store.returned:
			if !errors.Is(actual, mongo.ErrClientDisconnected) {
				t.Fatal("storage fault did not execute the real disconnected Mongo write")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("actual Mongo storage failure did not return")
		}
		if after := load(); after.Status != before.Status || after.Revision != before.Revision || len(after.EncryptedResult) != 0 {
			t.Fatal("failed real Mongo write fabricated a persistent terminal result")
		}
		if tryWriter() || len(notifier.published()) != 0 {
			t.Fatal("storage error ended source lifetime before repository cleanup")
		}
		finish()
	}
	var execErr error
	select {
	case execErr = <-executor.result:
	case <-time.After(10 * time.Second):
		t.Fatal("real background Execute did not return")
	}
	if execErr == nil {
		t.Fatal("background dependency failure was not returned by actual Execute")
	}
	select {
	case <-runCtx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("production background error consumer did not finish cleanup")
	}
	deadline, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !tryWriter() {
		select {
		case <-deadline.Done():
			t.Fatal("background error consumer retained the source after actual cleanup")
		case <-ticker.C:
		}
	}
	service.mu.Lock()
	_, started := service.started[runID]
	_, cancellable := service.cancels[runID]
	service.mu.Unlock()
	if started || cancellable || executor.calls != 1 || len(notifier.published()) != 0 {
		t.Fatal("background failure left active work or fabricated a terminal notification")
	}
	stored := load()
	wantStatus, wantRevision := models.RunStatusRunning, created.Revision+1
	switch scenario {
	case "source_error":
		wantStatus, wantRevision = models.RunStatusFailed, created.Revision+2
		if !errors.Is(execErr, source.failure) || source.reads != 1 || cipher.encrypts != 0 || stored.SafeError == nil || stored.SafeError.Code != "failed" {
			t.Fatal("real background source failure lost its safe persistent boundary")
		}
	case "encryption_error":
		if !errors.Is(execErr, cipher.encryptFailure) || source.reads != 1 || cipher.encrypts != 1 || store.completionWrites != 0 {
			t.Fatal("real background cipher failure did not stop before Mongo completion write")
		}
		assertAssistantPrivacyReport(t, cipher.plain)
	case "storage_error":
		if !errors.Is(execErr, store.failure) || source.reads != 1 || cipher.encrypts != 1 || store.completionWrites != 1 {
			t.Fatal("real background storage failure did not follow actual report encryption")
		}
		assertAssistantPrivacyReport(t, cipher.plain)
	case "gate_storage_error":
		wantStatus, wantRevision = models.RunStatusCreated, created.Revision
		if !errors.Is(execErr, ErrSourceRecovering) || source.reads != 0 || cipher.encrypts != 0 || store.runningWrites != 0 || store.failureWrites != 1 {
			t.Fatal("gate refusal did not reach its single real failed-CAS I/O boundary")
		}
	}
	if stored.Status != wantStatus || stored.Revision != wantRevision || len(stored.EncryptedResult) != 0 || len(stored.ReportPlaintext) != 0 {
		t.Fatal("background failure misrepresented the actual persistent run state")
	}
	view, err := service.GetRun(ctx, access, runID)
	if err != nil || view.Status != wantStatus || view.MessageID != created.MessageID || view.Report != nil {
		t.Fatal("real persistent GET lost the failed/unpersisted run and report reference")
	}
	assertAssistantPrivacyAbsent(t, "background GET DTO", assistantPrivacyJSON(t, dtos.NewRunView(view)))
	assertAssistantPrivacyAbsent(t, "background persistent run", assistantPrivacyJSON(t, stored))
	assertAssistantPrivacyAbsent(t, "background report reference", assistantPrivacyJSON(t, message))
	assertAssistantPrivacyAbsent(t, "background attempted Mongo update", store.attemptedUpdate)
	var raw bson.Raw
	if err := database.Collection("assistant_runs").FindOne(ctx, bson.M{"run_id": runID}).Decode(&raw); err != nil {
		t.Fatal("background run was not actually stored in Mongo")
	}
	assertAssistantPrivacyAbsent(t, "background stored BSON", raw)
}
