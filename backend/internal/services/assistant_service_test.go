package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestAssistantHTTP(t *testing.T) {
	t.Run("message insert failure does not execute", func(t *testing.T) {
		userID := primitive.NewObjectID()
		chatID := primitive.NewObjectID()
		chats := &assistantServiceChatLookup{chat: &models.Chat{
			UserID: userID,
			Base:   models.Base{ID: chatID},
		}}
		source := &assistantServiceCountingSource{inner: NewAssistantFixtureSource()}
		executor := &assistantServiceRecordingExecutor{}
		cipher := assistantServiceCipher(t)
		service := NewAssistantService(
			chats,
			assistantServiceFailingMessages{},
			repositories.NewAssistantRunRepository(newAssistantHTTPRunCollection(), cipher),
			cipher,
			nil,
			source,
			executor,
			NewUnconfiguredSourceAccessService(nil),
			WithAssistantRunIDGenerator(func() string { return "run-message-failed" }),
			WithAssistantClock(func() time.Time { return AssistantFixtureClock() }),
		)
		_, err := service.CreateRun(context.Background(), models.AccessContext{
			OwnerID: userID.Hex(),
			ChatID:  chatID.Hex(),
		}, assistantServiceRequest("req-fail"))
		if err == nil {
			t.Fatal("create returned nil, want the message insert error")
		}
		executor.wait(t)
		if executor.calls() != 0 {
			t.Fatalf("executor calls = %d, want 0", executor.calls())
		}
		if source.calls() != 0 {
			t.Fatalf("source calls = %d, want 0", source.calls())
		}
		view, getErr := service.GetRun(context.Background(), models.AccessContext{
			OwnerID: userID.Hex(),
			ChatID:  chatID.Hex(),
		}, "run-message-failed")
		if getErr != nil {
			t.Fatalf("get failed run: %v", getErr)
		}
		if view.Status != models.RunStatusFailed {
			t.Fatalf("status = %s, want failed", view.Status)
		}
	})
}

type assistantServiceChatLookup struct {
	chat *models.Chat
}

func (c assistantServiceChatLookup) FindOwnedChat(_ context.Context, ownerID, chatID string) (*models.Chat, error) {
	if c.chat == nil || c.chat.UserID.Hex() != ownerID || c.chat.ID.Hex() != chatID {
		return nil, AssistantRunNotFoundError{}
	}
	copied := *c.chat
	return &copied, nil
}

type assistantServiceFailingMessages struct{}

func (assistantServiceFailingMessages) InsertReportMessage(context.Context, *models.Message) error {
	return errors.New("message insert failed")
}

type assistantServiceRecordingExecutor struct {
	mu      sync.Mutex
	started chan struct{}
	runID   string
	count   int
}

func (e *assistantServiceRecordingExecutor) Execute(_ context.Context, runID string) error {
	e.mu.Lock()
	e.count++
	e.runID = runID
	if e.started != nil {
		close(e.started)
		e.started = nil
	}
	e.mu.Unlock()
	return nil
}

func (e *assistantServiceRecordingExecutor) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

func (e *assistantServiceRecordingExecutor) wait(t *testing.T) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
}

type assistantServiceCountingSource struct {
	inner *AssistantFixtureSource
	mu    sync.Mutex
	count int
}

func (s *assistantServiceCountingSource) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *assistantServiceCountingSource) hit() {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
}

func (s *assistantServiceCountingSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	s.hit()
	return s.inner.Capabilities(ctx)
}

func (s *assistantServiceCountingSource) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	s.hit()
	return s.inner.ResolveEntities(ctx, query)
}

func (s *assistantServiceCountingSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	s.hit()
	return s.inner.ReadCoverage(ctx, query)
}

func (s *assistantServiceCountingSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.hit()
	return s.inner.ReadSalesFacts(ctx, query)
}

func (s *assistantServiceCountingSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.hit()
	return s.inner.ReadRefundFacts(ctx, query)
}

func assistantServiceCipher(t *testing.T) repositories.ReportCipher {
	t.Helper()
	crypto, err := utils.NewAESGCMCrypto("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	cipher, err := repositories.NewAESReportCipher(crypto)
	if err != nil {
		t.Fatalf("report cipher: %v", err)
	}
	return cipher
}

func assistantServiceRequest(requestID string) models.AnalysisRequest {
	return models.AnalysisRequest{
		SchemaVersion: models.AssistantSchemaVersion,
		RequestID:     requestID,
		Workflow:      models.WorkflowProductAnalysis,
		Basis:         models.BasisPayment,
		Period: models.Period{
			DateRange: models.DateRange{StartDate: "2026-09-01", EndDateExclusive: "2026-09-08"},
			Timezone:  models.AssistantDefaultTimezone,
		},
		Scope:   models.Scope{Mode: models.ScopeModeAll},
		Product: models.ProductSelection{ProductID: AssistantFixtureProductID},
		GroupBy: models.GroupByShop,
		Metrics: []models.MetricID{models.MetricQuantity, models.MetricSales},
	}
}

type assistantHTTPRunCollection struct {
	mu   sync.Mutex
	rows []bson.Raw
}

func newAssistantHTTPRunCollection() *assistantHTTPRunCollection {
	return &assistantHTTPRunCollection{}
}

func (c *assistantHTTPRunCollection) InsertOne(_ context.Context, document interface{}, _ ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	raw, err := bson.Marshal(document)
	if err != nil {
		return nil, err
	}
	var record models.RunRecord
	if err := bson.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.rows {
		var got models.RunRecord
		if err := bson.Unmarshal(existing, &got); err != nil {
			return nil, err
		}
		if got.OwnerID == record.OwnerID && got.ChatID == record.ChatID && (got.RequestID == record.RequestID || got.RunID == record.RunID) {
			return nil, mongo.WriteException{WriteErrors: mongo.WriteErrors{{Index: 0, Code: 11000, Message: "E11000 duplicate key error"}}}
		}
	}
	c.rows = append(c.rows, raw)
	return &mongo.InsertOneResult{InsertedID: record.RunID}, nil
}

func (c *assistantHTTPRunCollection) FindOne(_ context.Context, filter interface{}, _ ...*options.FindOneOptions) *mongo.SingleResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	match := assistantHTTPFilter(filter)
	for _, raw := range c.rows {
		var record models.RunRecord
		if err := bson.Unmarshal(raw, &record); err != nil {
			return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
		}
		if match(&record) {
			return mongo.NewSingleResultFromDocument(bson.Raw(append([]byte(nil), raw...)), nil, nil)
		}
	}
	return mongo.NewSingleResultFromDocument(bson.D{}, mongo.ErrNoDocuments, nil)
}

func (c *assistantHTTPRunCollection) FindOneAndUpdate(_ context.Context, filter interface{}, update interface{}, _ ...*options.FindOneAndUpdateOptions) *mongo.SingleResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	match := assistantHTTPFilter(filter)
	for i, raw := range c.rows {
		var record models.RunRecord
		if err := bson.Unmarshal(raw, &record); err != nil {
			return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
		}
		if !match(&record) {
			continue
		}
		assistantHTTPApply(&record, update)
		encoded, err := bson.Marshal(record)
		if err != nil {
			return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
		}
		c.rows[i] = encoded
		return mongo.NewSingleResultFromDocument(bson.Raw(encoded), nil, nil)
	}
	return mongo.NewSingleResultFromDocument(bson.D{}, mongo.ErrNoDocuments, nil)
}

func (c *assistantHTTPRunCollection) UpdateMany(_ context.Context, filter interface{}, update interface{}, _ ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	match := assistantHTTPFilter(filter)
	var modified int64
	for i, raw := range c.rows {
		var record models.RunRecord
		if err := bson.Unmarshal(raw, &record); err != nil {
			return nil, err
		}
		if !match(&record) {
			continue
		}
		assistantHTTPApply(&record, update)
		encoded, err := bson.Marshal(record)
		if err != nil {
			return nil, err
		}
		c.rows[i] = encoded
		modified++
	}
	return &mongo.UpdateResult{MatchedCount: modified, ModifiedCount: modified}, nil
}

func (c *assistantHTTPRunCollection) Indexes() mongo.IndexView { return mongo.IndexView{} }

func assistantHTTPFilter(filter interface{}) func(*models.RunRecord) bool {
	raw, ok := filter.(bson.M)
	if !ok {
		return func(*models.RunRecord) bool { return false }
	}
	return func(record *models.RunRecord) bool {
		for key, want := range raw {
			if !assistantHTTPField(record, key, want) {
				return false
			}
		}
		return true
	}
}

func assistantHTTPField(record *models.RunRecord, key string, want interface{}) bool {
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
		return string(record.Status) == string(want.(models.RunStatus))
	case "revision":
		return record.Revision == want.(int64)
	default:
		return false
	}
}

func assistantHTTPApply(record *models.RunRecord, update interface{}) {
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
