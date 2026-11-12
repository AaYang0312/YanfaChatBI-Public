package repositories

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/mongodb"

	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestAssistantRunRepository(t *testing.T) {
	t.Run("fake collection", func(t *testing.T) {
		testAssistantRunRepositoryFake(t)
	})
	t.Run("mongo", func(t *testing.T) {
		testAssistantRunRepositoryMongo(t)
	})
}

func TestAssistantPersistedThreshold(t *testing.T) {
	t.Run("production registry", func(t *testing.T) {
		repo := NewAssistantRunRepository(newFakeRunCollection(), mustTestCipher(t))
		testAssistantThresholdRoundTrip(t, repo)
	})
	t.Run("Mongo default client", func(t *testing.T) {
		uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
		if uri == "" {
			t.Skip("deployment unverified: ASSISTANT_TEST_MONGO_URI is not set; persisted threshold Mongo round-trip is not exercised")
		}
		client := mongodb.InitializeDatabaseConnection(mongodb.MongoDbConfigModel{
			ConnectionUrl: uri,
			DatabaseName:  "assistant_threshold_" + primitive.NewObjectID().Hex(),
		})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = client.Client.Database(client.Config.DatabaseName).Drop(ctx)
			_ = client.Client.Disconnect(ctx)
		})
		collection := client.GetCollectionByName("assistant_runs")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := EnsureAssistantRunIndexes(ctx, collection); err != nil {
			t.Fatalf("indexes: %v", err)
		}
		testAssistantThresholdRoundTrip(t, NewAssistantRunRepository(collection, mustTestCipher(t)))
	})
}

func testAssistantThresholdRoundTrip(t *testing.T, repo AssistantRunRepository) {
	t.Helper()
	for _, tc := range []struct {
		name string
		text string
	}{
		{name: "absent"},
		{name: "zero", text: "0"},
		{name: "threshold", text: "0.4"},
		{name: "precise", text: "0.123456789012345678901234567890123456789"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := runningRun("run-"+tc.name, "fp-"+tc.name)
			if tc.text != "" {
				threshold := decimal.RequireFromString(tc.text)
				record.NormalizedInput.LowMarginThreshold = &threshold
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := repo.CreateOrGet(ctx, record); err != nil {
				t.Fatalf("create: %v", err)
			}
			loaded, err := repo.GetOwned(ctx, record.OwnerID, record.ChatID, record.RunID)
			if err != nil {
				t.Fatalf("get persisted run: %v", err)
			}
			updated, err := repo.Transition(ctx, cancelTransition(loaded))
			if err != nil {
				t.Fatalf("transition persisted run: %v", err)
			}
			for _, got := range []models.RunRecord{loaded, updated} {
				threshold := got.NormalizedInput.LowMarginThreshold
				if tc.text == "" {
					if threshold != nil {
						t.Fatalf("%s absent threshold = %s, want nil", got.Status, threshold)
					}
				} else if threshold == nil || threshold.String() != tc.text {
					t.Fatalf("%s persisted low_margin_threshold = %v, want %s", got.Status, threshold, tc.text)
				}
			}
		})
	}
}

func testAssistantRunRepositoryFake(t *testing.T) {
	t.Run("concurrent completed and cancelled leave one terminal state", func(t *testing.T) {
		repo, collection, cipher := newFakeRunRepo(t)
		seeded := seedRunningRun(t, repo, "run-race", "fp-race")

		var wg sync.WaitGroup
		wg.Add(2)
		errorsOut := make(chan error, 2)
		go func() {
			defer wg.Done()
			_, err := repo.Transition(context.Background(), completeTransition(seeded, []byte(`{"report_id":"run-race","outcome":"complete"}`)))
			errorsOut <- err
		}()
		go func() {
			defer wg.Done()
			_, err := repo.Transition(context.Background(), cancelTransition(seeded))
			errorsOut <- err
		}()
		wg.Wait()
		close(errorsOut)

		var failures int
		for err := range errorsOut {
			if errors.Is(err, ErrTransitionConflict) {
				failures++
				continue
			}
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
		}
		if failures != 1 {
			t.Fatalf("conditional update failures = %d, want 1", failures)
		}

		stored := collection.mustOne(t)
		if !stored.Status.IsTerminal() {
			t.Fatalf("status = %s, want one terminal state", stored.Status)
		}
		if stored.Status != models.RunStatusCompleted && stored.Status != models.RunStatusCancelled {
			t.Fatalf("status = %s, want completed or cancelled", stored.Status)
		}
		if stored.Revision != seeded.Revision+1 {
			t.Fatalf("revision = %d, want %d", stored.Revision, seeded.Revision+1)
		}
		late, err := repo.Transition(context.Background(), lateTransition(stored))
		if !errors.Is(err, ErrTransitionConflict) || late.RunID != "" {
			t.Fatalf("late writer returned (%s, %v), want transition conflict", late.RunID, err)
		}
		after := collection.mustOne(t)
		if after.Status != stored.Status || after.Revision != stored.Revision {
			t.Fatalf("late writer changed terminal row: status %s revision %d", after.Status, after.Revision)
		}
		if stored.Status == models.RunStatusCompleted {
			plain, err := cipher.DecryptReport(stored.EncryptedResult)
			if err != nil {
				t.Fatalf("decrypt winner: %v", err)
			}
			if !bytes.Contains(plain, []byte(`"outcome":"complete"`)) {
				t.Fatalf("winner plaintext = %s", plain)
			}
		}
	})

	t.Run("same fingerprint returns the original and a different fingerprint conflicts", func(t *testing.T) {
		repo, collection, _ := newFakeRunRepo(t)
		original := seedRunningRun(t, repo, "run-fp", "fp-same")
		replay := original.Clone()
		replay.RunID = "run-other"
		replay.MessageID = "msg-other"
		got, err := repo.CreateOrGet(context.Background(), replay)
		if err != nil {
			t.Fatalf("same fingerprint: %v", err)
		}
		if got.RunID != original.RunID || got.RequestFingerprint != "fp-same" {
			t.Fatalf("replay returned %s/%s, want original %s", got.RunID, got.RequestFingerprint, original.RunID)
		}
		if collection.count() != 1 {
			t.Fatalf("documents = %d, want 1", collection.count())
		}

		conflict := original.Clone()
		conflict.RunID = "run-conflict"
		conflict.RequestFingerprint = "fp-other"
		if _, err := repo.CreateOrGet(context.Background(), conflict); !isRequestConflict(err) {
			t.Fatalf("different fingerprint error = %v, want request_conflict", err)
		}
		if collection.count() != 1 {
			t.Fatalf("conflict inserted a second document, count = %d", collection.count())
		}
	})

	t.Run("nil crypto or encryption failure writes nothing", func(t *testing.T) {
		collection := newFakeRunCollection()
		repo := NewAssistantRunRepository(collection, nil)
		run := runningRun("run-nil-crypto", "fp-nil")
		run.ReportPlaintext = []byte(`{"report_id":"run-nil-crypto"}`)
		if _, err := repo.CreateOrGet(context.Background(), run); err == nil {
			t.Fatal("nil crypto inserted a run")
		}
		if collection.count() != 0 {
			t.Fatalf("nil crypto documents = %d, want 0", collection.count())
		}

		failing := &failingReportCipher{err: errors.New("encrypt failed")}
		repo = NewAssistantRunRepository(collection, failing)
		if _, err := repo.CreateOrGet(context.Background(), run); err == nil || !errors.Is(err, failing.err) {
			t.Fatalf("encryption failure = %v, want %v", err, failing.err)
		}
		if collection.count() != 0 {
			t.Fatalf("encryption failure documents = %d, want 0", collection.count())
		}

		seeded := seedRunningRun(t, NewAssistantRunRepository(collection, &recordingReportCipher{}), "run-transition-crypto", "fp-transition-crypto")
		before := collection.count()
		repo = NewAssistantRunRepository(collection, nil)
		_, err := repo.Transition(context.Background(), completeTransition(seeded, []byte(`{"sales":"120"}`)))
		if err == nil {
			t.Fatal("nil crypto completed a run")
		}
		stored := collection.mustOne(t)
		if stored.Status != models.RunStatusRunning || collection.count() != before {
			t.Fatalf("nil crypto transition mutated the row: status=%s count=%d", stored.Status, collection.count())
		}
		if bytes.Contains(storedJSON(t, stored), []byte(`"sales":"120"`)) || bytes.Contains(stored.EncryptedResult, []byte("sales")) {
			t.Fatalf("plaintext leaked into stored run")
		}
	})

	t.Run("decrypt failure is an error and not report json", func(t *testing.T) {
		repo, collection, _ := newFakeRunRepo(t)
		seeded := seedRunningRun(t, repo, "run-bad-cipher", "fp-bad-cipher")
		completed, err := repo.Transition(context.Background(), completeTransition(seeded, []byte(`{"report_id":"run-bad-cipher","outcome":"complete"}`)))
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		broken := &failingReportCipher{err: errors.New("decrypt failed")}
		report, err := DecryptReport(broken, completed)
		if err == nil || report != nil {
			t.Fatalf("decrypt failure report=%v err=%v", report, err)
		}
		if bytes.Contains([]byte(err.Error()), completed.EncryptedResult) {
			t.Fatalf("decrypt error exposed ciphertext: %v", err)
		}
		stored := collection.mustOne(t)
		if jsonObject(stored.EncryptedResult) {
			t.Fatalf("stored ciphertext is raw JSON: %s", stored.EncryptedResult)
		}
	})

	t.Run("interrupt orphans does not read a source and keeps terminal rows", func(t *testing.T) {
		repo, collection, _ := newFakeRunRepo(t)
		if _, err := repo.CreateOrGet(context.Background(), runningRun("run-orphan", "fp-orphan")); err != nil {
			t.Fatalf("orphan: %v", err)
		}
		own := runningRun("run-own", "fp-own")
		own.ExecutingInstance = "proc-current"
		if _, err := repo.CreateOrGet(context.Background(), own); err != nil {
			t.Fatalf("own run: %v", err)
		}
		done := runningRun("run-done", "fp-done")
		done.Status = models.RunStatusCompleted
		done.Stage = models.RunStageCompleted
		collection.insertStored(t, done)

		source := &countingSource{}
		modified, err := repo.InterruptOrphans(context.Background(), "proc-current")
		if err != nil {
			t.Fatalf("InterruptOrphans: %v", err)
		}
		if modified != 1 {
			t.Fatalf("modified = %d, want 1", modified)
		}
		if source.calls != 0 {
			t.Fatalf("source reads = %d, want 0", source.calls)
		}
		gotOrphan, err := repo.GetOwned(context.Background(), "owner-1", "chat-1", "run-orphan")
		if err != nil {
			t.Fatalf("get orphan: %v", err)
		}
		if gotOrphan.Status != models.RunStatusInterrupted {
			t.Fatalf("orphan status = %s", gotOrphan.Status)
		}
		gotOwn, err := repo.GetOwned(context.Background(), "owner-1", "chat-1", "run-own")
		if err != nil {
			t.Fatalf("get own: %v", err)
		}
		if gotOwn.Status != models.RunStatusRunning {
			t.Fatalf("current instance status = %s", gotOwn.Status)
		}
		gotDone, err := repo.GetOwned(context.Background(), "owner-1", "chat-1", "run-done")
		if err != nil {
			t.Fatalf("get done: %v", err)
		}
		if gotDone.Status != models.RunStatusCompleted {
			t.Fatalf("terminal status = %s", gotDone.Status)
		}
		if _, err := repo.GetOwned(context.Background(), "owner-2", "chat-1", "run-orphan"); !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("foreign owner error = %v, want not found", err)
		}
	})
}

func testAssistantRunRepositoryMongo(t *testing.T) {
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("deployment unverified: ASSISTANT_TEST_MONGO_URI is not set; assistant_runs Mongo conditional updates are not exercised")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetRegistry(mongodb.BSONRegistry()))
	if err != nil {
		t.Fatalf("connect ASSISTANT_TEST_MONGO_URI: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Disconnect(context.Background())
	})
	db := client.Database("assistant_run_repository_test")
	collection := db.Collection("assistant_runs_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() {
		_ = collection.Drop(context.Background())
	})
	if err := EnsureAssistantRunIndexes(ctx, collection); err != nil {
		t.Fatalf("indexes: %v", err)
	}
	repo := NewAssistantRunRepository(collection, mustTestCipher(t))

	seeded := seedRunningRun(t, repo, "run-mongo-race", "fp-mongo-race")
	var wg sync.WaitGroup
	wg.Add(2)
	errorsOut := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, err := repo.Transition(ctx, completeTransition(seeded, []byte(`{"report_id":"run-mongo-race"}`)))
		errorsOut <- err
	}()
	go func() {
		defer wg.Done()
		_, err := repo.Transition(ctx, cancelTransition(seeded))
		errorsOut <- err
	}()
	wg.Wait()
	close(errorsOut)
	failures := 0
	for err := range errorsOut {
		if errors.Is(err, ErrTransitionConflict) {
			failures++
			continue
		}
		if err != nil {
			t.Fatalf("mongo transition: %v", err)
		}
	}
	if failures != 1 {
		t.Fatalf("mongo conditional update failures = %d, want 1", failures)
	}
	got, err := repo.GetOwned(ctx, seeded.OwnerID, seeded.ChatID, seeded.RunID)
	if err != nil {
		t.Fatalf("get mongo winner: %v", err)
	}
	if !got.Status.IsTerminal() || (got.Status != models.RunStatusCompleted && got.Status != models.RunStatusCancelled) {
		t.Fatalf("mongo status = %s", got.Status)
	}
}

type countingSource struct {
	calls int
}

func (s *countingSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	s.calls++
	return models.SourceCapabilities{}, nil
}

func (s *countingSource) ResolveEntities(ctx context.Context, query AssistantEntityQuery) ([]models.Candidate, error) {
	s.calls++
	return nil, nil
}

func (s *countingSource) ReadCoverage(ctx context.Context, query AssistantCoverageQuery) ([]AssistantCoverage, error) {
	s.calls++
	return nil, nil
}

func (s *countingSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.calls++
	return models.FactSnapshot{}, nil
}

func (s *countingSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.calls++
	return models.FactSnapshot{}, nil
}

type recordingReportCipher struct {
	encrypts int
}

func (c *recordingReportCipher) EncryptReport(plain []byte) ([]byte, error) {
	c.encrypts++
	return append([]byte("enc:"), plain...), nil
}

func (c *recordingReportCipher) DecryptReport(ciphertext []byte) ([]byte, error) {
	const prefix = "enc:"
	if !bytes.HasPrefix(ciphertext, []byte(prefix)) {
		return nil, errors.New("ciphertext is not an encrypted report")
	}
	return append([]byte(nil), ciphertext[len(prefix):]...), nil
}

type failingReportCipher struct {
	err error
}

func (c *failingReportCipher) EncryptReport(plain []byte) ([]byte, error) {
	return nil, c.err
}

func (c *failingReportCipher) DecryptReport(ciphertext []byte) ([]byte, error) {
	return nil, c.err
}

func newFakeRunRepo(t *testing.T) (AssistantRunRepository, *fakeRunCollection, *recordingReportCipher) {
	t.Helper()
	collection := newFakeRunCollection()
	cipher := &recordingReportCipher{}
	repo := NewAssistantRunRepository(collection, cipher).(*assistantRunRepository)
	repo.now = func() time.Time { return time.Date(2026, 9, 8, 0, 30, 0, 0, time.UTC) }
	return repo, collection, cipher
}

func seedRunningRun(t *testing.T, repo AssistantRunRepository, runID, fingerprint string) models.RunRecord {
	t.Helper()
	stored, err := repo.CreateOrGet(context.Background(), runningRun(runID, fingerprint))
	if err != nil {
		t.Fatalf("seed %s: %v", runID, err)
	}
	return stored
}

func runningRun(runID, fingerprint string) models.RunRecord {
	return models.RunRecord{
		SchemaVersion:      models.AssistantSchemaVersion,
		RunID:              runID,
		OwnerID:            "owner-1",
		ChatID:             "chat-1",
		RequestID:          "req-" + runID,
		RequestFingerprint: fingerprint,
		NormalizedInput: models.AnalysisRequest{
			SchemaVersion: models.AssistantSchemaVersion,
			RequestID:     "req-" + runID,
			Workflow:      models.WorkflowSalesComparison,
			Basis:         models.BasisPayment,
		},
		MessageID:         "msg-" + runID,
		Status:            models.RunStatusRunning,
		Stage:             models.RunStageRunning,
		ExecutingInstance: "proc-old",
		Version:           models.RunVersion{SchemaVersion: models.AssistantSchemaVersion, MetricVersion: "t07"},
	}
}

func completeTransition(run models.RunRecord, plain []byte) models.RunTransition {
	return models.RunTransition{
		OwnerID:          run.OwnerID,
		ChatID:           run.ChatID,
		RunID:            run.RunID,
		From:             models.RunStatusRunning,
		To:               models.RunStatusCompleted,
		ExpectedRevision: run.Revision,
		Stage:            models.RunStageCompleted,
		ReportPlaintext:  plain,
	}
}

func cancelTransition(run models.RunRecord) models.RunTransition {
	marked := true
	return models.RunTransition{
		OwnerID:            run.OwnerID,
		ChatID:             run.ChatID,
		RunID:              run.RunID,
		From:               models.RunStatusRunning,
		To:                 models.RunStatusCancelled,
		ExpectedRevision:   run.Revision,
		Stage:              models.RunStageCancelled,
		CancellationMarked: &marked,
	}
}

func lateTransition(stored models.RunRecord) models.RunTransition {
	late := completeTransition(stored, []byte(`{"report_id":"late","outcome":"empty"}`))
	late.From = stored.Status
	late.ExpectedRevision = stored.Revision
	if stored.Status == models.RunStatusCompleted {
		late.To = models.RunStatusCancelled
		late.Stage = models.RunStageCancelled
		late.ReportPlaintext = nil
	}
	return late
}

func isRequestConflict(err error) bool {
	validation, ok := models.AsValidationError(err)
	return ok && validation.Code == models.ErrorCodeRequestConflict
}

func storedJSON(t *testing.T, record models.RunRecord) []byte {
	t.Helper()
	raw, err := bson.MarshalExtJSON(record, true, false)
	if err != nil {
		t.Fatalf("marshal stored run: %v", err)
	}
	return raw
}

func jsonObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func mustTestCipher(t *testing.T) ReportCipher {
	t.Helper()
	crypto, err := utils.NewAESGCMCrypto("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	wrapped, err := NewAESReportCipher(crypto)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	return wrapped
}

type fakeRunCollection struct {
	mu   sync.Mutex
	rows []bson.Raw
}

func newFakeRunCollection() *fakeRunCollection {
	return &fakeRunCollection{}
}

func (c *fakeRunCollection) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.rows)
}

func (c *fakeRunCollection) mustOne(t *testing.T) models.RunRecord {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.rows) != 1 {
		t.Fatalf("documents = %d, want 1", len(c.rows))
	}
	return decodeRun(t, c.rows[0])
}

func (c *fakeRunCollection) insertStored(t *testing.T, record models.RunRecord) {
	t.Helper()
	if _, err := c.InsertOne(context.Background(), record); err != nil {
		t.Fatalf("insert stored: %v", err)
	}
}

func (c *fakeRunCollection) InsertOne(ctx context.Context, document interface{}, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	raw, err := bson.MarshalWithRegistry(mongodb.BSONRegistry(), document)
	if err != nil {
		return nil, err
	}
	record := decodeRun(nil, raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.rows {
		got := decodeRun(nil, existing)
		if got.OwnerID == record.OwnerID && got.ChatID == record.ChatID && (got.RequestID == record.RequestID || got.RunID == record.RunID) {
			return nil, mongo.WriteException{WriteErrors: mongo.WriteErrors{{Index: 0, Code: 11000, Message: "E11000 duplicate key error"}}}
		}
	}
	c.rows = append(c.rows, raw)
	return &mongo.InsertOneResult{InsertedID: record.RunID}, nil
}

func (c *fakeRunCollection) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	match := bsonFilter(filter)
	for _, raw := range c.rows {
		record := decodeRun(nil, raw)
		if match(&record) {
			return mongo.NewSingleResultFromDocument(bson.Raw(append([]byte(nil), raw...)), nil, mongodb.BSONRegistry())
		}
	}
	return mongo.NewSingleResultFromDocument(bson.D{}, mongo.ErrNoDocuments, nil)
}

func (c *fakeRunCollection) FindOneAndUpdate(ctx context.Context, filter interface{}, update interface{}, opts ...*options.FindOneAndUpdateOptions) *mongo.SingleResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	match := bsonFilter(filter)
	for i, raw := range c.rows {
		record := decodeRun(nil, raw)
		if !match(&record) {
			continue
		}
		applyUpdate(&record, update)
		encoded, err := bson.MarshalWithRegistry(mongodb.BSONRegistry(), record)
		if err != nil {
			return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
		}
		c.rows[i] = encoded
		return mongo.NewSingleResultFromDocument(bson.Raw(encoded), nil, mongodb.BSONRegistry())
	}
	return mongo.NewSingleResultFromDocument(bson.D{}, mongo.ErrNoDocuments, nil)
}

func (c *fakeRunCollection) UpdateMany(ctx context.Context, filter interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	match := bsonFilter(filter)
	var modified int64
	for i, raw := range c.rows {
		record := decodeRun(nil, raw)
		if !match(&record) {
			continue
		}
		applyUpdate(&record, update)
		encoded, err := bson.MarshalWithRegistry(mongodb.BSONRegistry(), record)
		if err != nil {
			return nil, err
		}
		c.rows[i] = encoded
		modified++
	}
	return &mongo.UpdateResult{MatchedCount: modified, ModifiedCount: modified}, nil
}

func (c *fakeRunCollection) Indexes() mongo.IndexView {
	return mongo.IndexView{}
}

func decodeRun(t *testing.T, raw bson.Raw) models.RunRecord {
	var record models.RunRecord
	if err := bson.UnmarshalWithRegistry(mongodb.BSONRegistry(), raw, &record); err != nil && t != nil {
		t.Fatalf("decode stored run: %v", err)
	}
	return record
}

func bsonFilter(filter interface{}) func(*models.RunRecord) bool {
	raw, ok := filter.(bson.M)
	if !ok {
		return func(*models.RunRecord) bool { return false }
	}
	return func(record *models.RunRecord) bool {
		for key, want := range raw {
			if !fieldMatches(record, key, want) {
				return false
			}
		}
		return true
	}
}

func fieldMatches(record *models.RunRecord, key string, want interface{}) bool {
	switch key {
	case "owner_id":
		return record.OwnerID == want.(string)
	case "chat_id":
		return record.ChatID == want.(string)
	case "run_id":
		return record.RunID == want.(string)
	case "request_id":
		return record.RequestID == want.(string)
	case "status":
		return statusMatches(record.Status, want)
	case "revision":
		return record.Revision == want.(int64)
	case "executing_instance":
		return compareOperator(record.ExecutingInstance, want)
	default:
		return false
	}
}

func statusMatches(status models.RunStatus, want interface{}) bool {
	switch typed := want.(type) {
	case models.RunStatus:
		return status == typed
	case string:
		return string(status) == typed
	case bson.M:
		allowed, ok := typed["$in"].(bson.A)
		if !ok {
			return false
		}
		for _, item := range allowed {
			if statusMatches(status, item) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func compareOperator(got string, want interface{}) bool {
	switch typed := want.(type) {
	case string:
		return got == typed
	case bson.M:
		other, ok := typed["$ne"].(string)
		return ok && got != other
	default:
		return false
	}
}

func applyUpdate(record *models.RunRecord, update interface{}) {
	raw, ok := update.(bson.M)
	if !ok {
		return
	}
	if set, ok := raw["$set"].(bson.M); ok {
		for key, value := range set {
			switch key {
			case "status":
				record.Status = value.(models.RunStatus)
			case "stage":
				record.Stage = value.(models.RunStage)
			case "updated_at":
				record.UpdatedAt = value.(time.Time)
			case "executing_instance":
				record.ExecutingInstance = value.(string)
			case "cancellation_marked":
				record.CancellationMarked = value.(bool)
			case "message_id":
				record.MessageID = value.(string)
			case "version":
				record.Version = value.(models.RunVersion)
			case "safe_error":
				copied := value.(models.SafeRunError)
				record.SafeError = &copied
			case "encrypted_result":
				record.EncryptedResult = append([]byte(nil), value.([]byte)...)
			}
		}
	}
	if inc, ok := raw["$inc"].(bson.M); ok {
		if delta, ok := inc["revision"].(int64); ok {
			record.Revision += delta
		}
	}
}
