package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/services"
	"yanfachatbi/internal/utils"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestAssistantHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	harness := newAssistantHTTPHarness(t)

	t.Run("foreign user is not found before any source call", func(t *testing.T) {
		before := harness.source.calls()
		owner := harness.ownerA
		paths := []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodPost, "/api/chats/" + owner.chatID + "/assistant/resolve", assistantDraftBody("req-foreign", "\u53cc\u5c42\u73bb\u7483")},
			{http.MethodPost, "/api/chats/" + owner.chatID + "/assistant/runs", assistantAnalysisBody("req-foreign", services.AssistantFixtureProductID)},
			{http.MethodGet, "/api/chats/" + owner.chatID + "/assistant/runs/run-does-not-matter", ""},
			{http.MethodPost, "/api/chats/" + owner.chatID + "/assistant/runs/run-does-not-matter/cancel", ""},
		}
		for _, call := range paths {
			response := harness.do(t, harness.userB, call.method, call.path, call.body)
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404 body %s", call.method, call.path, response.Code, response.Body.String())
			}
		}
		if got := harness.source.calls() - before; got != 0 {
			t.Fatalf("source calls during foreign access = %d, want 0", got)
		}
		if harness.messages.count() != 0 {
			t.Fatalf("foreign access stored %d messages", harness.messages.count())
		}
	})

	t.Run("ambiguous product returns both candidates and reads no facts", func(t *testing.T) {
		before := harness.source.factReads()
		response := harness.do(t, harness.userA, http.MethodPost, "/api/chats/"+harness.ownerA.chatID+"/assistant/resolve", assistantDraftBody("req-ambiguous", "\u73bb\u7483\u676f"))
		if response.Code != http.StatusOK {
			t.Fatalf("resolve status = %d body %s", response.Code, response.Body.String())
		}
		var body struct {
			Success bool                     `json:"success"`
			Data    dtos.AssistantResolution `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode resolve: %v", err)
		}
		if body.Data.Status != string(models.ResolveStatusNeedsInput) {
			t.Fatalf("status = %q, want needs_input", body.Data.Status)
		}
		if body.Data.Request != nil {
			t.Fatalf("needs_input carried a request: %+v", body.Data.Request)
		}
		if len(body.Data.Candidates) != 2 {
			t.Fatalf("candidates = %#v, want both fixture products", body.Data.Candidates)
		}
		gotIDs := map[string]bool{}
		for _, candidate := range body.Data.Candidates {
			gotIDs[candidate.EntityID] = true
		}
		if !gotIDs["P-CUP-A"] || !gotIDs["P-CUP-B"] {
			t.Fatalf("candidate ids = %#v, want P-CUP-A and P-CUP-B", gotIDs)
		}
		if got := harness.source.factReads() - before; got != 0 {
			t.Fatalf("fact reads during ambiguity = %d, want 0", got)
		}
	})

	t.Run("same request replays and a changed body conflicts", func(t *testing.T) {
		body := assistantAnalysisBody("req-replay", services.AssistantFixtureProductID)
		first := harness.do(t, harness.userA, http.MethodPost, "/api/chats/"+harness.ownerA.chatID+"/assistant/runs", body)
		if first.Code != http.StatusAccepted {
			t.Fatalf("create status = %d body %s", first.Code, first.Body.String())
		}
		created := decodeAssistantAccepted(t, first.Body.Bytes())
		if created.RunID == "" || created.MessageID == "" || created.Status != string(models.RunStatusCreated) {
			t.Fatalf("accepted = %+v, want run_id, message_id, and created", created)
		}
		stored := harness.messages.byID(t, created.MessageID)
		if stored.Queries != nil {
			t.Fatalf("report message queries = %#v, want nil", stored.Queries)
		}
		if stored.AssistantRunID == nil || *stored.AssistantRunID != created.RunID {
			t.Fatalf("assistant_run_id = %#v, want %s", stored.AssistantRunID, created.RunID)
		}

		replay := harness.do(t, harness.userA, http.MethodPost, "/api/chats/"+harness.ownerA.chatID+"/assistant/runs", body)
		if replay.Code != http.StatusAccepted {
			t.Fatalf("replay status = %d body %s", replay.Code, replay.Body.String())
		}
		again := decodeAssistantAccepted(t, replay.Body.Bytes())
		if again.RunID != created.RunID || again.MessageID != created.MessageID {
			t.Fatalf("replay = %+v, want original %+v", again, created)
		}
		if harness.messages.count() != 1 {
			t.Fatalf("messages after replay = %d, want 1", harness.messages.count())
		}

		conflict := harness.do(t, harness.userA, http.MethodPost, "/api/chats/"+harness.ownerA.chatID+"/assistant/runs", assistantAnalysisBody("req-replay", "P-OTHER"))
		if conflict.Code != http.StatusConflict {
			t.Fatalf("changed body status = %d, want 409 body %s", conflict.Code, conflict.Body.String())
		}
		if harness.messages.count() != 1 {
			t.Fatalf("conflict stored another message, count = %d", harness.messages.count())
		}

		harness.waitSettled(t, created.RunID)
		got := harness.do(t, harness.userA, http.MethodGet, "/api/chats/"+harness.ownerA.chatID+"/assistant/runs/"+created.RunID, "")
		if got.Code != http.StatusOK {
			t.Fatalf("get status = %d body %s", got.Code, got.Body.String())
		}
		var fetched struct {
			Data dtos.AssistantRunView `json:"data"`
		}
		if err := json.Unmarshal(got.Body.Bytes(), &fetched); err != nil {
			t.Fatalf("decode get: %v", err)
		}
		if fetched.Data.RunID != created.RunID || fetched.Data.Status != string(models.RunStatusCompleted) {
			t.Fatalf("fetched = %+v, want completed original run", fetched.Data)
		}
		if len(fetched.Data.Report) == 0 || bytes.Contains(fetched.Data.Report, []byte("encrypted_result")) {
			t.Fatalf("report = %s, want decrypted report without ciphertext", fetched.Data.Report)
		}

		cancelled := harness.do(t, harness.userA, http.MethodPost, "/api/chats/"+harness.ownerA.chatID+"/assistant/runs/"+created.RunID+"/cancel", "")
		if cancelled.Code != http.StatusOK {
			t.Fatalf("cancel status = %d body %s", cancelled.Code, cancelled.Body.String())
		}
		var terminal struct {
			Data dtos.AssistantRunView `json:"data"`
		}
		if err := json.Unmarshal(cancelled.Body.Bytes(), &terminal); err != nil {
			t.Fatalf("decode cancel: %v", err)
		}
		if terminal.Data.Status != string(models.RunStatusCompleted) {
			t.Fatalf("terminal cancel status = %s, want completed", terminal.Data.Status)
		}
	})

	t.Run("old message omits assistant_run_id", func(t *testing.T) {
		old := &models.Message{
			UserID:  assistantMustObjectID(t, harness.userA),
			ChatID:  assistantMustObjectID(t, harness.ownerA.chatID),
			Type:    "assistant",
			Content: "old",
			Queries: &[]models.Query{{Query: "select 1"}},
			Base:    models.NewBase(),
		}
		response := services.BuildAssistantMessageResponse(old)
		if response.AssistantRunID != nil {
			t.Fatalf("old message assistant_run_id = %q, want absent", *response.AssistantRunID)
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("marshal old message: %v", err)
		}
		if bytes.Contains(encoded, []byte("assistant_run_id")) {
			t.Fatalf("old message JSON included assistant_run_id: %s", encoded)
		}
	})
}

type assistantHTTPUser struct {
	userID string
	chatID string
}

type assistantHTTPHarness struct {
	userA    string
	userB    string
	ownerA   assistantHTTPUser
	server   *httptest.Server
	source   *assistantHTTPSource
	messages *assistantHTTPMessages
	service  services.AssistantService
}

func newAssistantHTTPHarness(t *testing.T) *assistantHTTPHarness {
	t.Helper()
	userA := primitive.NewObjectID().Hex()
	userB := primitive.NewObjectID().Hex()
	chatA := primitive.NewObjectID()
	chats := &assistantHTTPChats{rows: map[string]*models.Chat{
		chatA.Hex(): {
			UserID: assistantMustObjectID(t, userA),
			Base:   models.Base{ID: chatA},
		},
	}}
	messages := newAssistantHTTPMessages()
	source := &assistantHTTPSource{inner: services.NewAssistantFixtureSource()}
	cipher := assistantMustCipher(t)
	repo := repositories.NewAssistantRunRepository(newAssistantHTTPRunCollection(), cipher)
	service := services.NewAssistantService(
		chats,
		messages,
		repo,
		cipher,
		assistantHTTPCatalog{},
		source,
		nil,
		services.NewUnconfiguredSourceAccessService(nil),
		services.WithAssistantClock(func() time.Time {
			return services.AssistantFixtureClock()
		}),
	)
	handler := NewAssistantHandler(service)
	router := gin.New()
	protected := router.Group("/api/chats")
	protected.Use(func(c *gin.Context) {
		userID := c.GetHeader("X-Test-User")
		if userID == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set("userID", userID)
		c.Next()
	})
	protected.POST("/:id/assistant/resolve", handler.Resolve)
	protected.POST("/:id/assistant/runs", handler.CreateRun)
	protected.GET("/:id/assistant/runs/:runID", handler.GetRun)
	protected.POST("/:id/assistant/runs/:runID/cancel", handler.CancelRun)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &assistantHTTPHarness{
		userA:    userA,
		userB:    userB,
		ownerA:   assistantHTTPUser{userID: userA, chatID: chatA.Hex()},
		server:   server,
		source:   source,
		messages: messages,
		service:  service,
	}
}

func (h *assistantHTTPHarness) do(t *testing.T, userID, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Test-User", userID)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	h.server.Config.Handler.ServeHTTP(recorder, request)
	return recorder
}

func (h *assistantHTTPHarness) waitSettled(t *testing.T, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := h.service.GetRun(context.Background(), models.AccessContext{
			OwnerID: h.userA,
			ChatID:  h.ownerA.chatID,
		}, runID)
		if err == nil && view.Status.IsTerminal() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach a terminal status", runID)
}

func assistantDraftBody(requestID, productQuery string) string {
	return `{"schema_version":1,"request_id":"` + requestID + `","workflow":"product_analysis","product_query":"` + productQuery + `","metrics":["quantity","sales"]}`
}

func assistantAnalysisBody(requestID, productID string) string {
	return `{"schema_version":1,"request_id":"` + requestID + `","workflow":"product_analysis","basis":"payment","period":{"start_date":"2026-09-01","end_date_exclusive":"2026-09-08","timezone":"Asia/Shanghai"},"scope":{"mode":"all"},"product":{"product_id":"` + productID + `"},"group_by":"shop","metrics":["quantity","sales","cost","margin"]}`
}

type assistantAcceptedBody struct {
	RunID     string `json:"run_id"`
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}

func decodeAssistantAccepted(t *testing.T, raw []byte) assistantAcceptedBody {
	t.Helper()
	var body struct {
		Data assistantAcceptedBody `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode accepted: %v", err)
	}
	return body.Data
}

func assistantMustObjectID(t *testing.T, hex string) primitive.ObjectID {
	t.Helper()
	id, err := primitive.ObjectIDFromHex(hex)
	if err != nil {
		t.Fatalf("object id %s: %v", hex, err)
	}
	return id
}

func assistantMustCipher(t *testing.T) repositories.ReportCipher {
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

type assistantHTTPChats struct {
	mu   sync.Mutex
	rows map[string]*models.Chat
}

func (c *assistantHTTPChats) FindOwnedChat(_ context.Context, ownerID, chatID string) (*models.Chat, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	chat := c.rows[chatID]
	if chat == nil || chat.UserID.Hex() != ownerID {
		return nil, services.AssistantRunNotFoundError{}
	}
	copied := *chat
	return &copied, nil
}

type assistantHTTPMessages struct {
	mu   sync.Mutex
	rows []*models.Message
}

func newAssistantHTTPMessages() *assistantHTTPMessages {
	return &assistantHTTPMessages{}
}

func (m *assistantHTTPMessages) InsertReportMessage(_ context.Context, message *models.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *message
	if message.AssistantRunID != nil {
		runID := *message.AssistantRunID
		copied.AssistantRunID = &runID
	}
	m.rows = append(m.rows, &copied)
	return nil
}

func (m *assistantHTTPMessages) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *assistantHTTPMessages) byID(t *testing.T, id string) *models.Message {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, message := range m.rows {
		if message.ID.Hex() == id {
			return message
		}
	}
	t.Fatalf("message %s not stored", id)
	return nil
}

type assistantHTTPCatalog struct{}

func (assistantHTTPCatalog) FindProductCandidates(_ context.Context, _ models.AccessContext, productQuery string) ([]models.Candidate, error) {
	switch productQuery {
	case "\u73bb\u7483\u676f":
		return []models.Candidate{
			{EntityType: models.EntityTypeProduct, EntityID: "P-CUP-A", Label: "\u53cc\u5c42\u73bb\u7483\u676f A", PlatformID: "PL-1", ShopID: "S1"},
			{EntityType: models.EntityTypeProduct, EntityID: "P-CUP-B", Label: "\u53cc\u5c42\u73bb\u7483\u676f B", PlatformID: "PL-1", ShopID: "S2"},
		}, nil
	default:
		return nil, nil
	}
}

type assistantHTTPSource struct {
	inner *services.AssistantFixtureSource
	mu    sync.Mutex
	all   int
	facts int
}

func (s *assistantHTTPSource) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all
}

func (s *assistantHTTPSource) factReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.facts
}

func (s *assistantHTTPSource) hit(fact bool) {
	s.mu.Lock()
	s.all++
	if fact {
		s.facts++
	}
	s.mu.Unlock()
}

func (s *assistantHTTPSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	s.hit(false)
	return s.inner.Capabilities(ctx)
}

func (s *assistantHTTPSource) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	s.hit(false)
	return s.inner.ResolveEntities(ctx, query)
}

func (s *assistantHTTPSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	s.hit(true)
	return s.inner.ReadCoverage(ctx, query)
}

func (s *assistantHTTPSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.hit(true)
	return s.inner.ReadSalesFacts(ctx, query)
}

func (s *assistantHTTPSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.hit(true)
	return s.inner.ReadRefundFacts(ctx, query)
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
