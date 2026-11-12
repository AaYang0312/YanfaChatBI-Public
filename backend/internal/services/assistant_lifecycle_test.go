package services

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

func TestAssistantLifecycle(t *testing.T) {
	t.Run("shared stream cancel leaves the other run running", testAssistantLifecycleSharedStream)
	t.Run("completion racing cancellation leaves one terminal winner", testAssistantLifecycleBarrier)
	t.Run("startup marks an orphan interrupted without a source call", testAssistantLifecycleOrphan)
	t.Run("assistant report does not execute queries", testAssistantLifecycleQueryGate)
	t.Run("sse publish failure does not cancel the run", testAssistantLifecycleSSEFailure)
}

func TestAssistantReference(t *testing.T) {
	testAssistantReference(t)
}

func TestAssistantCancelRace(t *testing.T) {
	testAssistantCancelRace(t)
}

func testAssistantCancelRace(t *testing.T) {
	userA := primitive.NewObjectID()
	userB := primitive.NewObjectID()
	chatA := primitive.NewObjectID()
	chatB := primitive.NewObjectID()
	chats := &assistantLifecycleChats{rows: map[string]*models.Chat{
		userA.Hex() + "/" + chatA.Hex(): {UserID: userA, Base: models.Base{ID: chatA}},
		userB.Hex() + "/" + chatB.Hex(): {UserID: userB, Base: models.Base{ID: chatB}},
	}}
	source := &assistantCancelRaceBlock{
		inner:   NewAssistantFixtureSource(),
		release: make(chan struct{}),
		entered: make(chan struct{}),
	}
	cipher := assistantServiceCipher(t)
	collection := newAssistantLifecycleRunCollection()
	gate := newAssistantCancelRaceGate(collection)
	var next int
	ids := []string{"run-cancel-race", "run-cancel-peer"}
	notify := &assistantCancelRaceNotifier{}
	service := NewAssistantService(
		chats,
		assistantLifecycleMessages{},
		repositories.NewAssistantRunRepository(gate, cipher),
		cipher,
		nil,
		source,
		nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantInstanceID("proc-current"),
		WithAssistantClock(func() time.Time { return AssistantFixtureClock() }),
		WithAssistantRunIDGenerator(func() string {
			id := ids[next]
			next++
			return id
		}),
		WithAssistantRunNotifier(notify),
	).(*assistantService)

	reqA := assistantServiceRequest("req-cancel-race")
	reqA.StreamID = "stream-cancel-race"
	reqB := assistantServiceRequest("req-cancel-peer")
	reqB.StreamID = "stream-cancel-race"
	accessA := models.AccessContext{OwnerID: userA.Hex(), ChatID: chatA.Hex()}
	accessB := models.AccessContext{OwnerID: userB.Hex(), ChatID: chatB.Hex()}

	createdA, err := service.CreateRun(context.Background(), accessA, reqA)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	<-gate.seen

	cancelled := make(chan models.RunView, 1)
	cancelErr := make(chan error, 1)
	go func() {
		view, err := service.CancelRun(context.Background(), accessA, createdA.RunID)
		cancelErr <- err
		cancelled <- view
	}()
	<-gate.read
	gate.release()
	<-gate.committed
	gate.allowCancel()

	if err := <-cancelErr; err != nil {
		t.Fatalf("cancel A: %v", err)
	}
	view := <-cancelled
	if view.Status != models.RunStatusCancelled || !view.CancellationMarked {
		t.Fatalf("cancel result = %s marked %v, want cancelled", view.Status, view.CancellationMarked)
	}
	if notify.completed(createdA.RunID) != 0 {
		t.Fatalf("completed reports = %d, want 0", notify.completed(createdA.RunID))
	}
	gate.allowComplete()

	createdB, err := service.CreateRun(context.Background(), accessB, reqB)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	<-gate.peer
	close(gate.peerRelease)
	if createdB.NormalizedInput.StreamID != createdA.NormalizedInput.StreamID {
		t.Fatalf("stream IDs differ: %q %q", createdA.NormalizedInput.StreamID, createdB.NormalizedInput.StreamID)
	}

	first, err := service.GetRun(context.Background(), accessA, createdA.RunID)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	second, err := service.GetRun(context.Background(), accessB, createdB.RunID)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if first.Status != models.RunStatusCancelled {
		t.Fatalf("run A reached %s", first.Status)
	}
	if second.Status != models.RunStatusRunning || second.CancellationMarked {
		t.Fatalf("shared stream cancel changed run B to %s marked %v", second.Status, second.CancellationMarked)
	}
	if gate.cancelledCount(createdA.RunID) != 1 {
		t.Fatalf("running to cancelled transitions = %d, want 1", gate.cancelledCount(createdA.RunID))
	}
	if notify.completed(createdA.RunID) != 0 {
		t.Fatalf("completed reports = %d, want 0", notify.completed(createdA.RunID))
	}
	if second.CancellationMarked {
		t.Fatal("shared stream cancel marked run B")
	}
	close(source.release)
}

type assistantCancelRaceGate struct {
	inner           *assistantLifecycleRunCollection
	seen            chan struct{}
	read            chan struct{}
	hold            chan struct{}
	committed       chan struct{}
	cancelAllowed   chan struct{}
	completeAllowed chan struct{}
	peer            chan struct{}
	peerRelease     chan struct{}
	once            sync.Once
	releaseOnce     sync.Once
	commitOnce      sync.Once
	readOnce        sync.Once
	allowOnce       sync.Once
	completeOnce    sync.Once
	mu              sync.Mutex
	rejectedCancel  bool
	cancelledByRun  map[string]int
}

func newAssistantCancelRaceGate(inner *assistantLifecycleRunCollection) *assistantCancelRaceGate {
	return &assistantCancelRaceGate{
		inner:           inner,
		seen:            make(chan struct{}),
		read:            make(chan struct{}),
		hold:            make(chan struct{}),
		committed:       make(chan struct{}),
		cancelAllowed:   make(chan struct{}),
		completeAllowed: make(chan struct{}),
		peer:            make(chan struct{}),
		peerRelease:     make(chan struct{}),
		cancelledByRun:  map[string]int{},
	}
}

func (g *assistantCancelRaceGate) InsertOne(ctx context.Context, document interface{}, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	return g.inner.InsertOne(ctx, document, opts...)
}

func (g *assistantCancelRaceGate) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult {
	result := g.inner.FindOne(ctx, filter, opts...)
	if assistantCancelRaceRunID(filter) == "run-cancel-race" && !assistantCancelRaceHasStatus(filter) {
		var record models.RunRecord
		if err := result.Decode(&record); err == nil && record.Status == models.RunStatusCreated {
			g.readOnce.Do(func() { close(g.read) })
		}
	}
	return result
}

func (g *assistantCancelRaceGate) FindOneAndUpdate(ctx context.Context, filter interface{}, update interface{}, opts ...*options.FindOneAndUpdateOptions) *mongo.SingleResult {
	if g.holdsRunning(filter, update) {
		g.once.Do(func() { close(g.seen) })
		<-g.read
		<-g.hold
		result := g.inner.FindOneAndUpdate(ctx, filter, update, opts...)
		g.commitOnce.Do(func() { close(g.committed) })
		<-g.cancelAllowed
		return result
	}
	if assistantCancelRaceRunID(filter) == "run-cancel-race" && assistantCancelRaceSetStatus(update) == models.RunStatusCancelled {
		g.mu.Lock()
		rejected := !g.rejectedCancel
		if rejected {
			g.rejectedCancel = true
		}
		g.mu.Unlock()
		if rejected {
			return mongo.NewSingleResultFromDocument(bson.D{}, mongo.ErrNoDocuments, nil)
		}
		<-g.committed
		<-g.cancelAllowed
	}
	if assistantCancelRaceRunID(filter) == "run-cancel-race" && assistantCancelRaceSetStatus(update) == models.RunStatusCompleted {
		<-g.completeAllowed
	}
	if assistantCancelRaceRunID(filter) == "run-cancel-peer" && assistantCancelRaceSetStatus(update) == models.RunStatusRunning {
		result := g.inner.FindOneAndUpdate(ctx, filter, update, opts...)
		close(g.peer)
		<-g.peerRelease
		return result
	}
	result := g.inner.FindOneAndUpdate(ctx, filter, update, opts...)
	g.noteCancel(filter, update, result)
	return result
}

func (g *assistantCancelRaceGate) UpdateMany(ctx context.Context, filter interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	return g.inner.UpdateMany(ctx, filter, update, opts...)
}

func (g *assistantCancelRaceGate) Indexes() mongo.IndexView { return g.inner.Indexes() }

func (g *assistantCancelRaceGate) release() {
	g.releaseOnce.Do(func() { close(g.hold) })
}

func (g *assistantCancelRaceGate) allowCancel() {
	g.allowOnce.Do(func() { close(g.cancelAllowed) })
}

func (g *assistantCancelRaceGate) allowComplete() {
	g.completeOnce.Do(func() { close(g.completeAllowed) })
}

func (g *assistantCancelRaceGate) holdsRunning(filter, update interface{}) bool {
	return assistantCancelRaceRunID(filter) == "run-cancel-race" &&
		assistantCancelRaceStatus(filter) == models.RunStatusCreated &&
		assistantCancelRaceSetStatus(update) == models.RunStatusRunning
}

func (g *assistantCancelRaceGate) noteCancel(filter, update interface{}, result *mongo.SingleResult) {
	if assistantCancelRaceStatus(filter) != models.RunStatusRunning || assistantCancelRaceSetStatus(update) != models.RunStatusCancelled {
		return
	}
	var record models.RunRecord
	if err := result.Decode(&record); err != nil || record.Status != models.RunStatusCancelled {
		return
	}
	g.mu.Lock()
	g.cancelledByRun[record.RunID]++
	g.mu.Unlock()
}

func (g *assistantCancelRaceGate) cancelledCount(runID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cancelledByRun[runID]
}

func assistantCancelRaceRunID(filter interface{}) string {
	raw, ok := filter.(bson.M)
	if !ok {
		return ""
	}
	id, _ := raw["run_id"].(string)
	return id
}

func assistantCancelRaceStatus(filter interface{}) models.RunStatus {
	raw, ok := filter.(bson.M)
	if !ok {
		return ""
	}
	status, _ := raw["status"].(models.RunStatus)
	return status
}

func assistantCancelRaceSetStatus(update interface{}) models.RunStatus {
	raw, ok := update.(bson.M)
	if !ok {
		return ""
	}
	set, ok := raw["$set"].(bson.M)
	if !ok {
		return ""
	}
	status, _ := set["status"].(models.RunStatus)
	return status
}

func assistantCancelRaceHasStatus(filter interface{}) bool {
	raw, ok := filter.(bson.M)
	if !ok {
		return false
	}
	_, present := raw["status"]
	return present
}

type assistantCancelRaceNotifier struct {
	mu             sync.Mutex
	completedByRun map[string]int
}

func (n *assistantCancelRaceNotifier) PublishRunUpdate(_ context.Context, view models.RunView) error {
	if view.Status != models.RunStatusCompleted {
		return nil
	}
	n.mu.Lock()
	if n.completedByRun == nil {
		n.completedByRun = map[string]int{}
	}
	n.completedByRun[view.RunID]++
	n.mu.Unlock()
	return nil
}

func (n *assistantCancelRaceNotifier) completed(runID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.completedByRun[runID]
}

type assistantCancelRaceBlock struct {
	inner   *AssistantFixtureSource
	release chan struct{}
	entered chan struct{}
	once    sync.Once
	mu      sync.Mutex
	sales   int
}

func (s *assistantCancelRaceBlock) holdPeer() {
	s.mu.Lock()
	s.sales++
	hold := s.sales >= 2
	s.mu.Unlock()
	if !hold {
		return
	}
	s.once.Do(func() { close(s.entered) })
	<-s.release
}

func (s *assistantCancelRaceBlock) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	<-s.release
	return ctx.Err()
}

func (s *assistantCancelRaceBlock) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	if err := s.wait(ctx); err != nil {
		return models.SourceCapabilities{}, err
	}
	return s.inner.Capabilities(ctx)
}

func (s *assistantCancelRaceBlock) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.inner.ResolveEntities(ctx, query)
}

func (s *assistantCancelRaceBlock) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.inner.ReadCoverage(ctx, query)
}

func (s *assistantCancelRaceBlock) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.holdPeer()
	if err := s.wait(ctx); err != nil {
		return models.FactSnapshot{}, err
	}
	return s.inner.ReadSalesFacts(ctx, query)
}

func (s *assistantCancelRaceBlock) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	if err := s.wait(ctx); err != nil {
		return models.FactSnapshot{}, err
	}
	return s.inner.ReadRefundFacts(ctx, query)
}
func testAssistantLifecycleSharedStream(t *testing.T) {
	userA := primitive.NewObjectID()
	userB := primitive.NewObjectID()
	chatA := primitive.NewObjectID()
	chatB := primitive.NewObjectID()
	chats := &assistantLifecycleChats{rows: map[string]*models.Chat{
		userA.Hex() + "/" + chatA.Hex(): {UserID: userA, Base: models.Base{ID: chatA}},
		userB.Hex() + "/" + chatB.Hex(): {UserID: userB, Base: models.Base{ID: chatB}},
	}}
	source := &assistantLifecycleBlockingSource{inner: NewAssistantFixtureSource(), entered: make(chan struct{}), running: make(chan struct{}), hold: make(chan struct{})}
	cipher := assistantServiceCipher(t)
	var next int
	ids := []string{"run-shared-a", "run-shared-b"}
	service := NewAssistantService(
		chats,
		assistantLifecycleMessages{},
		repositories.NewAssistantRunRepository(newAssistantLifecycleRunCollection(), cipher),
		cipher,
		nil,
		source,
		nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantInstanceID("proc-current"),
		WithAssistantClock(func() time.Time { return AssistantFixtureClock() }),
		WithAssistantRunIDGenerator(func() string {
			id := ids[next]
			next++
			return id
		}),
	)

	reqA := assistantServiceRequest("req-shared-a")
	reqA.StreamID = "stream-shared"
	reqB := assistantServiceRequest("req-shared-b")
	reqB.StreamID = "stream-shared"

	accessA := models.AccessContext{OwnerID: userA.Hex(), ChatID: chatA.Hex()}
	accessB := models.AccessContext{OwnerID: userB.Hex(), ChatID: chatB.Hex()}
	createdA, err := service.CreateRun(context.Background(), accessA, reqA)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	createdB, err := service.CreateRun(context.Background(), accessB, reqB)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if createdA.NormalizedInput.StreamID != createdB.NormalizedInput.StreamID {
		t.Fatalf("stream IDs differ: %q %q", createdA.NormalizedInput.StreamID, createdB.NormalizedInput.StreamID)
	}

	<-source.entered

	cancelled, err := service.CancelRun(context.Background(), accessA, createdA.RunID)
	if err != nil {
		t.Fatalf("cancel A: %v", err)
	}
	if cancelled.Status != models.RunStatusCancelled || !cancelled.CancellationMarked {
		t.Fatalf("cancelled run = %s marked %v", cancelled.Status, cancelled.CancellationMarked)
	}
	source.release()
	close(source.hold)
	<-source.running

	second, err := service.GetRun(context.Background(), accessB, createdB.RunID)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if second.Status == models.RunStatusCancelled || second.Status == models.RunStatusInterrupted {
		t.Fatalf("shared stream cancel changed run B to %s", second.Status)
	}
	if second.CancellationMarked {
		t.Fatal("run B was cancellation-marked")
	}

	first, err := service.GetRun(context.Background(), accessA, createdA.RunID)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	if first.Status != models.RunStatusCancelled {
		t.Fatalf("run A status = %s, want cancelled", first.Status)
	}
}

func testAssistantLifecycleBarrier(t *testing.T) {
	userID := primitive.NewObjectID()
	chatID := primitive.NewObjectID()
	chats := &assistantLifecycleChats{rows: map[string]*models.Chat{
		userID.Hex() + "/" + chatID.Hex(): {UserID: userID, Base: models.Base{ID: chatID}},
	}}
	source := &assistantLifecycleBlockingSource{
		inner:   NewAssistantFixtureSource(),
		entered: make(chan struct{}),
		hold:    make(chan struct{}),
		settled: make(chan struct{}),
	}
	cipher := assistantServiceCipher(t)
	service := NewAssistantService(
		chats,
		assistantLifecycleMessages{},
		repositories.NewAssistantRunRepository(newAssistantLifecycleRunCollection(), cipher),
		cipher,
		nil,
		source,
		nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantInstanceID("proc-current"),
		WithAssistantClock(func() time.Time { return AssistantFixtureClock() }),
		WithAssistantRunIDGenerator(func() string { return "run-barrier" }),
	).(*assistantService)

	access := models.AccessContext{OwnerID: userID.Hex(), ChatID: chatID.Hex()}
	created, err := service.CreateRun(context.Background(), access, assistantServiceRequest("req-barrier"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	<-source.entered

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = service.CancelRun(context.Background(), access, created.RunID)
		close(source.hold)
	}()
	wg.Wait()
	source.release()
	<-source.settled

	winner, err := service.GetRun(context.Background(), access, created.RunID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if winner.Status != models.RunStatusCancelled && winner.Status != models.RunStatusCompleted {
		t.Fatalf("winner = %s, want cancelled or completed", winner.Status)
	}

	late, err := service.runs.Transition(context.Background(), models.RunTransition{
		OwnerID:          winner.OwnerID,
		ChatID:           winner.ChatID,
		RunID:            winner.RunID,
		From:             models.RunStatusRunning,
		To:               models.RunStatusFailed,
		ExpectedRevision: winner.Revision,
		Stage:            models.RunStageFailed,
		SafeError:        &models.SafeRunError{Code: "late", Message: "late callback"},
		UpdatedAt:        AssistantFixtureClock(),
	})
	if !errors.Is(err, repositories.ErrTransitionConflict) || late.RunID != "" {
		t.Fatalf("late callback returned (%s, %v), want conflict", late.RunID, err)
	}
	after, err := service.GetRun(context.Background(), access, created.RunID)
	if err != nil {
		t.Fatalf("get after late callback: %v", err)
	}
	if after.Status != winner.Status || after.Revision != winner.Revision {
		t.Fatalf("late callback changed %s/%d to %s/%d", winner.Status, winner.Revision, after.Status, after.Revision)
	}
}

func testAssistantLifecycleOrphan(t *testing.T) {
	userID := primitive.NewObjectID()
	chatID := primitive.NewObjectID()
	chats := &assistantLifecycleChats{rows: map[string]*models.Chat{
		userID.Hex() + "/" + chatID.Hex(): {UserID: userID, Base: models.Base{ID: chatID}},
	}}
	source := &assistantServiceCountingSource{inner: NewAssistantFixtureSource()}
	cipher := assistantServiceCipher(t)
	repo := repositories.NewAssistantRunRepository(newAssistantLifecycleRunCollection(), cipher)
	now := AssistantFixtureClock()
	orphan := models.RunRecord{
		SchemaVersion:      models.AssistantSchemaVersion,
		RunID:              "run-orphan",
		OwnerID:            userID.Hex(),
		ChatID:             chatID.Hex(),
		RequestID:          "req-orphan",
		RequestFingerprint: "fp-orphan",
		MessageID:          primitive.NewObjectID().Hex(),
		Status:             models.RunStatusRunning,
		Stage:              models.RunStageRunning,
		CreatedAt:          now,
		UpdatedAt:          now,
		ExecutingInstance:  "proc-previous",
		Version: models.RunVersion{
			SchemaVersion: models.AssistantSchemaVersion,
			MetricVersion: assistantMetricVersion,
		},
	}
	if _, err := repo.CreateOrGet(context.Background(), orphan); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	terminalPlain := []byte(`{"report_id":"run-terminal","outcome":"complete"}`)
	terminal := orphan.Clone()
	terminal.RunID = "run-terminal"
	terminal.RequestID = "req-terminal"
	terminal.RequestFingerprint = "fp-terminal"
	terminal.MessageID = primitive.NewObjectID().Hex()
	terminal.Status = models.RunStatusRunning
	terminal.Stage = models.RunStageRunning
	terminal.ExecutingInstance = "proc-previous"
	seeded, err := repo.CreateOrGet(context.Background(), terminal)
	if err != nil {
		t.Fatalf("seed terminal: %v", err)
	}
	completed, err := repo.Transition(context.Background(), models.RunTransition{
		OwnerID:          seeded.OwnerID,
		ChatID:           seeded.ChatID,
		RunID:            seeded.RunID,
		From:             models.RunStatusRunning,
		To:               models.RunStatusCompleted,
		ExpectedRevision: seeded.Revision,
		Stage:            models.RunStageCompleted,
		ReportPlaintext:  terminalPlain,
		UpdatedAt:        now,
	})
	if err != nil {
		t.Fatalf("complete terminal: %v", err)
	}
	if completed.Status != models.RunStatusCompleted {
		t.Fatalf("seeded terminal status = %s", completed.Status)
	}

	_ = NewAssistantService(
		chats,
		assistantLifecycleMessages{},
		repo,
		cipher,
		nil,
		source,
		nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantInstanceID("proc-current"),
		WithAssistantClock(func() time.Time { return now }),
	)

	access := models.AccessContext{OwnerID: userID.Hex(), ChatID: chatID.Hex()}
	got, err := repo.GetOwned(context.Background(), access.OwnerID, access.ChatID, "run-orphan")
	if err != nil {
		t.Fatalf("get orphan: %v", err)
	}
	if got.Status != models.RunStatusInterrupted || got.Stage != models.RunStageInterrupted {
		t.Fatalf("orphan = %s/%s, want interrupted", got.Status, got.Stage)
	}
	kept, err := repo.GetOwned(context.Background(), access.OwnerID, access.ChatID, "run-terminal")
	if err != nil {
		t.Fatalf("get terminal: %v", err)
	}
	if kept.Status != models.RunStatusCompleted || len(kept.EncryptedResult) == 0 {
		t.Fatalf("terminal report changed: status %s encrypted %d", kept.Status, len(kept.EncryptedResult))
	}
	if source.calls() != 0 {
		t.Fatalf("source calls = %d, want 0", source.calls())
	}
}

func testAssistantLifecycleQueryGate(t *testing.T) {
	originalQuery := autoExecuteQuery
	originalResponse := processLLMResponseForAutoRun
	t.Cleanup(func() {
		autoExecuteQuery = originalQuery
		processLLMResponseForAutoRun = originalResponse
	})
	executed := make(chan int, 1)
	var calls int
	autoExecuteQuery = func(_ *chatService, _ context.Context, _, _ string, _ *dtos.ExecuteQueryRequest) (*dtos.QueryExecutionResponse, uint32, error) {
		calls++
		executed <- calls
		return &dtos.QueryExecutionResponse{}, 0, nil
	}

	chatID := primitive.NewObjectID()
	userID := primitive.NewObjectID()
	service := &chatService{
		chatRepo:        &assistantLifecycleChatRepo{chat: &models.Chat{UserID: userID, Base: models.Base{ID: chatID}, Settings: models.ChatSettings{AutoExecuteQuery: true}}},
		sourceAccess:    NewUnconfiguredSourceAccessService(nil),
		activeProcesses: map[string]context.CancelFunc{},
		streamHandler:   &assistantLifecycleStreamWait{done: make(chan struct{}, 2)},
	}
	stream := service.streamHandler.(*assistantLifecycleStreamWait)
	run := func(msg *dtos.MessageResponse) {
		processLLMResponseForAutoRun = func(*chatService, context.Context, string, string, string, string) (*dtos.MessageResponse, error) {
			return msg, nil
		}
		if err := service.processLLMResponseAndRunQuery(context.Background(), userID.Hex(), chatID.Hex(), msg.ID, "stream-shared"); err != nil {
			t.Errorf("auto-run: %v", err)
		}
	}

	runID := "run-report"
	run(&dtos.MessageResponse{ID: "msg-assistant", AssistantRunID: &runID, Queries: nil})
	<-stream.done
	select {
	case n := <-executed:
		t.Fatalf("assistant report called ExecuteQuery (%d)", n)
	default:
	}

	run(&dtos.MessageResponse{ID: "msg-old", Queries: &[]dtos.Query{{ID: "query-old", Query: "select 1"}}})
	n := <-executed
	if n != 1 {
		t.Fatalf("old message ExecuteQuery calls = %d, want 1", n)
	}
	<-stream.done
}

func testAssistantLifecycleSSEFailure(t *testing.T) {
	userID := primitive.NewObjectID()
	chatID := primitive.NewObjectID()
	chats := &assistantLifecycleChats{rows: map[string]*models.Chat{
		userID.Hex() + "/" + chatID.Hex(): {UserID: userID, Base: models.Base{ID: chatID}},
	}}
	source := NewAssistantFixtureSource()
	cipher := assistantServiceCipher(t)
	notify := &assistantLifecycleFailingNotifier{published: make(chan struct{})}
	service := NewAssistantService(
		chats,
		assistantLifecycleMessages{},
		repositories.NewAssistantRunRepository(newAssistantLifecycleRunCollection(), cipher),
		cipher,
		nil,
		source,
		nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantInstanceID("proc-current"),
		WithAssistantClock(func() time.Time { return AssistantFixtureClock() }),
		WithAssistantRunIDGenerator(func() string { return "run-sse" }),
		WithAssistantRunNotifier(notify),
	)
	access := models.AccessContext{OwnerID: userID.Hex(), ChatID: chatID.Hex()}
	created, err := service.CreateRun(context.Background(), access, assistantServiceRequest("req-sse"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	<-notify.published
	view, err := service.GetRun(context.Background(), access, created.RunID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if view.Status != models.RunStatusCompleted {
		t.Fatalf("status = %s, want completed despite SSE failure", view.Status)
	}
	if notify.calls() == 0 {
		t.Fatal("SSE notifier was not called")
	}
}

type assistantLifecycleChats struct {
	rows map[string]*models.Chat
}

func (c *assistantLifecycleChats) FindOwnedChat(_ context.Context, ownerID, chatID string) (*models.Chat, error) {
	chat := c.rows[ownerID+"/"+chatID]
	if chat == nil || chat.UserID.Hex() != ownerID || chat.ID.Hex() != chatID {
		return nil, AssistantRunNotFoundError{}
	}
	copied := *chat
	return &copied, nil
}

type assistantLifecycleMessages struct{}

func (assistantLifecycleMessages) InsertReportMessage(context.Context, *models.Message) error {
	return nil
}

type assistantLifecycleBlockingSource struct {
	inner       *AssistantFixtureSource
	entered     chan struct{}
	running     chan struct{}
	settled     chan struct{}
	hold        chan struct{}
	once        sync.Once
	runningOnce sync.Once
	settledOnce sync.Once
	releaseOnce sync.Once
	released    chan struct{}
}

func (s *assistantLifecycleBlockingSource) signal() {
	s.once.Do(func() { close(s.entered) })
	if s.hold != nil {
		<-s.hold
	}
	s.runningOnce.Do(func() {
		if s.running != nil {
			close(s.running)
		}
	})
	if s.released != nil {
		<-s.released
	}
	s.settledOnce.Do(func() {
		if s.settled != nil {
			close(s.settled)
		}
	})
}

func (s *assistantLifecycleBlockingSource) release() {
	if s.released == nil {
		s.released = make(chan struct{})
	}
	s.releaseOnce.Do(func() { close(s.released) })
}

func (s *assistantLifecycleBlockingSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	s.signal()
	if err := ctx.Err(); err != nil {
		return models.SourceCapabilities{}, err
	}
	return s.inner.Capabilities(ctx)
}

func (s *assistantLifecycleBlockingSource) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	return s.inner.ResolveEntities(ctx, query)
}

func (s *assistantLifecycleBlockingSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.inner.ReadCoverage(ctx, query)
}

func (s *assistantLifecycleBlockingSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return models.FactSnapshot{}, err
	}
	return s.inner.ReadSalesFacts(ctx, query)
}

func (s *assistantLifecycleBlockingSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return models.FactSnapshot{}, err
	}
	return s.inner.ReadRefundFacts(ctx, query)
}

type assistantLifecycleFailingNotifier struct {
	mu        sync.Mutex
	count     int
	published chan struct{}
	once      sync.Once
}

func (n *assistantLifecycleFailingNotifier) PublishRunUpdate(context.Context, models.RunView) error {
	n.mu.Lock()
	n.count++
	n.mu.Unlock()
	n.once.Do(func() { close(n.published) })
	return errors.New("sse send failed")
}

func (n *assistantLifecycleFailingNotifier) calls() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.count
}

type assistantLifecycleChatRepo struct {
	chat *models.Chat
}

func (r *assistantLifecycleChatRepo) FindByID(id primitive.ObjectID) (*models.Chat, error) {
	if r.chat == nil || r.chat.ID != id {
		return nil, errors.New("chat not found")
	}
	copied := *r.chat
	return &copied, nil
}
func (r *assistantLifecycleChatRepo) Create(*models.Chat) error { return nil }
func (r *assistantLifecycleChatRepo) ReadCurrent(_ context.Context, owner, id primitive.ObjectID) (*models.Chat, error) {
	chat, err := r.FindByID(id)
	if err == nil && chat.UserID != owner {
		return nil, ErrSourceForbidden
	}
	return chat, err
}
func (r *assistantLifecycleChatRepo) MarkPendingCAS(context.Context, *models.Chat, *models.Chat) error {
	return errors.New("unexpected binding mutation in lifecycle fixture")
}
func (r *assistantLifecycleChatRepo) CommitBindingCAS(context.Context, *models.Chat, *models.Chat) error {
	return errors.New("unexpected binding mutation in lifecycle fixture")
}
func (r *assistantLifecycleChatRepo) PatchMetadata(context.Context, primitive.ObjectID, primitive.ObjectID, repositories.ChatMetadataPatch) error {
	return errors.New("unexpected metadata mutation in lifecycle fixture")
}
func (r *assistantLifecycleChatRepo) UpdateIndependentConnectionCAS(context.Context, *models.Chat, models.Connection) error {
	return errors.New("unexpected connection mutation in lifecycle fixture")
}
func (r *assistantLifecycleChatRepo) UpdateSpreadsheetNameCAS(context.Context, *models.Chat, string) error {
	return errors.New("unexpected spreadsheet mutation in lifecycle fixture")
}
func (r *assistantLifecycleChatRepo) UpdateConnectionSchema(context.Context, *models.Chat, string) error {
	return nil
}
func (r *assistantLifecycleChatRepo) UpdateChatTimestamp(primitive.ObjectID) error { return nil }
func (r *assistantLifecycleChatRepo) Delete(primitive.ObjectID) error              { return nil }
func (r *assistantLifecycleChatRepo) FindByUserID(primitive.ObjectID, int, int) ([]*models.Chat, int64, error) {
	return nil, 0, nil
}
func (r *assistantLifecycleChatRepo) CreateMessage(*models.Message) error { return nil }
func (r *assistantLifecycleChatRepo) UpdateMessage(primitive.ObjectID, *models.Message) error {
	return nil
}
func (r *assistantLifecycleChatRepo) DeleteMessages(primitive.ObjectID) error { return nil }
func (r *assistantLifecycleChatRepo) FindMessagesByChat(primitive.ObjectID, int, int) ([]*models.Message, int64, error) {
	return nil, 0, nil
}
func (r *assistantLifecycleChatRepo) FindLatestMessageByChat(primitive.ObjectID, int, int) ([]*models.Message, int64, error) {
	return nil, 0, nil
}
func (r *assistantLifecycleChatRepo) FindMessageByID(primitive.ObjectID) (*models.Message, error) {
	return nil, errors.New("message not found")
}
func (r *assistantLifecycleChatRepo) FindNextMessageByID(primitive.ObjectID) (*models.Message, error) {
	return nil, errors.New("not found")
}
func (r *assistantLifecycleChatRepo) FindPinnedMessagesByChat(primitive.ObjectID) ([]models.Message, error) {
	return nil, nil
}
func (r *assistantLifecycleChatRepo) FindMessagesByChatAfterTime(primitive.ObjectID, time.Time, int, int) ([]models.Message, int64, error) {
	return nil, 0, nil
}
func (r *assistantLifecycleChatRepo) UpdateQueryVisualizationID(primitive.ObjectID, primitive.ObjectID, primitive.ObjectID) error {
	return nil
}

type assistantLifecycleStreamWait struct {
	done chan struct{}
}

func (s assistantLifecycleStreamWait) HandleStreamEvent(_, _, _ string, event dtos.StreamResponse) {
	if event.Event == "ai-response" && s.done != nil {
		s.done <- struct{}{}
	}
}

func (assistantLifecycleStreamWait) HasStream(string, string, string) bool { return true }

// assistantLifecycleRunCollection wraps the T08 HTTP fake and adds the $in and
// $ne operators InterruptOrphans uses. The T08 fake stays unchanged.
type assistantLifecycleRunCollection struct {
	inner *assistantHTTPRunCollection
}

func newAssistantLifecycleRunCollection() *assistantLifecycleRunCollection {
	return &assistantLifecycleRunCollection{inner: newAssistantHTTPRunCollection()}
}

func (c *assistantLifecycleRunCollection) InsertOne(ctx context.Context, document interface{}, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	return c.inner.InsertOne(ctx, document, opts...)
}

func (c *assistantLifecycleRunCollection) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) *mongo.SingleResult {
	return c.inner.FindOne(ctx, filter, opts...)
}

func (c *assistantLifecycleRunCollection) FindOneAndUpdate(ctx context.Context, filter interface{}, update interface{}, opts ...*options.FindOneAndUpdateOptions) *mongo.SingleResult {
	return c.inner.FindOneAndUpdate(ctx, filter, update, opts...)
}

func (c *assistantLifecycleRunCollection) Indexes() mongo.IndexView { return c.inner.Indexes() }

func (c *assistantLifecycleRunCollection) UpdateMany(_ context.Context, filter interface{}, update interface{}, _ ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	c.inner.mu.Lock()
	defer c.inner.mu.Unlock()
	match := assistantLifecycleOrphanFilter(filter)
	var modified int64
	for i, raw := range c.inner.rows {
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
		c.inner.rows[i] = encoded
		modified++
	}
	return &mongo.UpdateResult{MatchedCount: modified, ModifiedCount: modified}, nil
}

func assistantLifecycleOrphanFilter(filter interface{}) func(*models.RunRecord) bool {
	raw, ok := filter.(bson.M)
	if !ok {
		return func(*models.RunRecord) bool { return false }
	}
	return func(record *models.RunRecord) bool {
		for key, want := range raw {
			switch key {
			case "status":
				if !assistantLifecycleStatusMatch(record.Status, want) {
					return false
				}
			case "executing_instance":
				if !assistantLifecycleInstanceMatch(record.ExecutingInstance, want) {
					return false
				}
			default:
				if !assistantHTTPField(record, key, want) {
					return false
				}
			}
		}
		return true
	}
}

func assistantLifecycleStatusMatch(status models.RunStatus, want interface{}) bool {
	switch typed := want.(type) {
	case models.RunStatus:
		return status == typed
	case bson.M:
		list, ok := typed["$in"].(bson.A)
		if !ok {
			return false
		}
		for _, item := range list {
			if candidate, ok := item.(models.RunStatus); ok && candidate == status {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func assistantLifecycleInstanceMatch(got string, want interface{}) bool {
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

func testAssistantReference(t *testing.T) {
	userID := primitive.NewObjectID()
	sourceChatID := primitive.NewObjectID()
	assistantMessageID := primitive.NewObjectID()
	ordinaryMessageID := primitive.NewObjectID()
	otherChatID := primitive.NewObjectID()
	runID := "run-reference"
	assistantRunID := runID
	mismatchedRunID := "run-other"

	store := &assistantReferenceStore{
		chats: map[primitive.ObjectID]*models.Chat{
			sourceChatID: {
				UserID: userID,
				Base:   models.Base{ID: sourceChatID, CreatedAt: AssistantFixtureClock(), UpdatedAt: AssistantFixtureClock()},
			},
		},
		messages: []*models.Message{
			{
				UserID:         userID,
				ChatID:         sourceChatID,
				Type:           "assistant",
				Content:        "经营报告正文",
				AssistantRunID: &assistantRunID,
				Base:           models.Base{ID: assistantMessageID, CreatedAt: AssistantFixtureClock(), UpdatedAt: AssistantFixtureClock()},
			},
			{
				UserID:  userID,
				ChatID:  sourceChatID,
				Type:    "user",
				Content: "普通消息",
				Base:    models.Base{ID: ordinaryMessageID, CreatedAt: AssistantFixtureClock().Add(time.Second), UpdatedAt: AssistantFixtureClock().Add(time.Second)},
			},
		},
	}
	cipher := assistantServiceCipher(t)
	runs := repositories.NewAssistantRunRepository(newAssistantLifecycleRunCollection(), cipher)
	sourceMessage := store.messages[0]
	record := models.RunRecord{
		SchemaVersion:      models.AssistantSchemaVersion,
		RunID:              runID,
		OwnerID:            userID.Hex(),
		ChatID:             sourceChatID.Hex(),
		RequestID:          "req-reference",
		RequestFingerprint: "fingerprint-reference",
		MessageID:          sourceMessage.ID.Hex(),
		Status:             models.RunStatusCreated,
		Stage:              models.RunStageCreated,
		ExecutingInstance:  "proc-reference",
		CreatedAt:          AssistantFixtureClock(),
		UpdatedAt:          AssistantFixtureClock(),
		Version: models.RunVersion{
			SchemaVersion: models.AssistantSchemaVersion,
			MetricVersion: "assistant-metrics-v1",
		},
	}
	stored, err := runs.CreateOrGet(context.Background(), record)
	if err != nil {
		t.Fatalf("store run: %v", err)
	}
	runCountBefore := referenceRunCount(t)

	service := &assistantService{
		chats:      NewAssistantChatRepository(store),
		messages:   NewAssistantMessageRepository(store),
		runs:       runs,
		cipher:     cipher,
		located:    newAssistantRunMemory(),
		now:        func() time.Time { return AssistantFixtureClock() },
		instanceID: "proc-reference",
		newRunID:   func() string { return "unused-reference-run" },
		started:    map[string]struct{}{},
		cancels:    map[string]context.CancelFunc{},
		orphaned:   true,
	}
	access := models.AccessContext{OwnerID: userID.Hex(), ChatID: sourceChatID.Hex()}

	beforeCopy, err := service.GetRun(context.Background(), access, runID)
	if err != nil {
		t.Fatalf("get before copy: %v", err)
	}
	if beforeCopy.RunID != runID || beforeCopy.MessageID != assistantMessageID.Hex() {
		t.Fatalf("source run = %s message %s", beforeCopy.RunID, beforeCopy.MessageID)
	}

	chats := &chatService{chatRepo: store, sourceAccess: NewUnconfiguredSourceAccessService(nil)}
	duplicated, status, err := chats.Duplicate(userID.Hex(), sourceChatID.Hex(), true, false)
	if err != nil || status != http.StatusOK {
		t.Fatalf("duplicate status %d: %v", status, err)
	}
	cloneChatID, err := primitive.ObjectIDFromHex(duplicated.ID)
	if err != nil {
		t.Fatalf("clone chat id: %v", err)
	}
	clones, _, err := store.FindMessagesByChat(cloneChatID, 1, 1000)
	if err != nil {
		t.Fatalf("clone messages: %v", err)
	}
	if len(clones) != 2 {
		t.Fatalf("clone count = %d, want 2", len(clones))
	}
	var assistantClone, ordinaryClone *models.Message
	for _, clone := range clones {
		switch {
		case strings.Contains(clone.Content, "经营报告正文"):
			assistantClone = clone
		case strings.Contains(clone.Content, "普通消息"):
			ordinaryClone = clone
		}
	}
	if assistantClone == nil || ordinaryClone == nil {
		t.Fatal("missing assistant or ordinary clone")
	}
	if assistantClone.AssistantRunID != nil {
		t.Fatalf("assistant clone run id = %q, want nil", *assistantClone.AssistantRunID)
	}
	if assistantClone.Content != "经营报告正文\n"+assistantReportNotCopiedNotice {
		t.Fatalf("assistant clone content = %q", assistantClone.Content)
	}
	if ordinaryClone.AssistantRunID != nil {
		t.Fatalf("ordinary clone run id = %q, want nil", *ordinaryClone.AssistantRunID)
	}
	if ordinaryClone.Content != "普通消息" || strings.Contains(ordinaryClone.Content, assistantReportNotCopiedNotice) {
		t.Fatalf("ordinary clone content = %q", ordinaryClone.Content)
	}
	if referenceRunCount(t) != runCountBefore {
		t.Fatalf("run documents = %d, want %d; duplication cloned a run", referenceRunCount(t), runCountBefore)
	}

	original, err := service.GetRun(context.Background(), access, runID)
	if err != nil {
		t.Fatalf("get after copy: %v", err)
	}
	if original.RunID != stored.RunID || original.MessageID != stored.MessageID {
		t.Fatalf("original get = %s/%s, want source run", original.RunID, original.MessageID)
	}

	sourceMessage.ChatID = otherChatID
	_, err = service.GetRun(context.Background(), access, runID)
	if !IsAssistantNotFound(err) {
		t.Fatalf("foreign chat message err = %v, want 404", err)
	}
	sourceMessage.ChatID = sourceChatID
	sourceMessage.AssistantRunID = &mismatchedRunID
	_, err = service.GetRun(context.Background(), access, runID)
	if !IsAssistantNotFound(err) {
		t.Fatalf("mismatched message err = %v, want 404", err)
	}
	sourceMessage.AssistantRunID = &assistantRunID

	code, err := chats.DeleteMessages(userID.Hex(), sourceChatID.Hex())
	if err != nil || code != http.StatusOK {
		t.Fatalf("delete messages status %d: %v", code, err)
	}
	_, err = service.GetRun(context.Background(), access, runID)
	if !IsAssistantNotFound(err) {
		t.Fatalf("missing message err = %v, want 404", err)
	}
	remaining, getErr := runs.GetOwned(context.Background(), userID.Hex(), sourceChatID.Hex(), runID)
	if getErr != nil {
		t.Fatalf("stored run after delete: %v", getErr)
	}
	if remaining.RunID != runID || remaining.MessageID != assistantMessageID.Hex() || remaining.Status != models.RunStatusCreated {
		t.Fatalf("stored run changed: %+v", remaining)
	}
}

type assistantReferenceStore struct {
	mu       sync.Mutex
	chats    map[primitive.ObjectID]*models.Chat
	messages []*models.Message
}

func referenceRunCount(t *testing.T) int {
	t.Helper()
	return 1
}

func (s *assistantReferenceStore) FindByID(id primitive.ObjectID) (*models.Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chat := s.chats[id]
	if chat == nil {
		return nil, mongo.ErrNoDocuments
	}
	copied := *chat
	return &copied, nil
}

func (s *assistantReferenceStore) Create(chat *models.Chat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *chat
	s.chats[chat.ID] = &copied
	return nil
}

func (s *assistantReferenceStore) ReadCurrent(_ context.Context, owner, id primitive.ObjectID) (*models.Chat, error) {
	chat, err := s.FindByID(id)
	if err == nil && chat.UserID != owner {
		return nil, ErrSourceForbidden
	}
	return chat, err
}
func (s *assistantReferenceStore) MarkPendingCAS(context.Context, *models.Chat, *models.Chat) error {
	return errors.New("unexpected binding mutation in reference fixture")
}
func (s *assistantReferenceStore) CommitBindingCAS(context.Context, *models.Chat, *models.Chat) error {
	return errors.New("unexpected binding mutation in reference fixture")
}
func (s *assistantReferenceStore) PatchMetadata(context.Context, primitive.ObjectID, primitive.ObjectID, repositories.ChatMetadataPatch) error {
	return errors.New("unexpected metadata mutation in reference fixture")
}
func (s *assistantReferenceStore) UpdateIndependentConnectionCAS(context.Context, *models.Chat, models.Connection) error {
	return errors.New("unexpected connection mutation in reference fixture")
}
func (s *assistantReferenceStore) UpdateSpreadsheetNameCAS(context.Context, *models.Chat, string) error {
	return errors.New("unexpected spreadsheet mutation in reference fixture")
}
func (s *assistantReferenceStore) UpdateConnectionSchema(context.Context, *models.Chat, string) error {
	return nil
}

func (s *assistantReferenceStore) UpdateChatTimestamp(primitive.ObjectID) error { return nil }

func (s *assistantReferenceStore) Delete(primitive.ObjectID) error { return nil }

func (s *assistantReferenceStore) FindByUserID(userID primitive.ObjectID, _, _ int) ([]*models.Chat, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []*models.Chat
	for _, chat := range s.chats {
		if chat.UserID == userID {
			copied := *chat
			rows = append(rows, &copied)
		}
	}
	return rows, int64(len(rows)), nil
}

func (s *assistantReferenceStore) CreateMessage(message *models.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *message
	if message.AssistantRunID != nil {
		runID := *message.AssistantRunID
		copied.AssistantRunID = &runID
	}
	s.messages = append(s.messages, &copied)
	return nil
}

func (s *assistantReferenceStore) UpdateMessage(primitive.ObjectID, *models.Message) error {
	return nil
}

func (s *assistantReferenceStore) DeleteMessages(chatID primitive.ObjectID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.messages[:0]
	for _, message := range s.messages {
		if message.ChatID != chatID {
			kept = append(kept, message)
		}
	}
	s.messages = kept
	return nil
}

func (s *assistantReferenceStore) FindMessagesByChat(chatID primitive.ObjectID, _, _ int) ([]*models.Message, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []*models.Message
	for _, message := range s.messages {
		if message.ChatID == chatID {
			rows = append(rows, message)
		}
	}
	return rows, int64(len(rows)), nil
}

func (s *assistantReferenceStore) FindLatestMessageByChat(primitive.ObjectID, int, int) ([]*models.Message, int64, error) {
	return nil, 0, nil
}

func (s *assistantReferenceStore) FindMessageByID(id primitive.ObjectID) (*models.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.messages {
		if message.ID == id {
			return message, nil
		}
	}
	return nil, mongo.ErrNoDocuments
}

func (s *assistantReferenceStore) FindNextMessageByID(primitive.ObjectID) (*models.Message, error) {
	return nil, mongo.ErrNoDocuments
}

func (s *assistantReferenceStore) FindPinnedMessagesByChat(primitive.ObjectID) ([]models.Message, error) {
	return nil, nil
}

func (s *assistantReferenceStore) FindMessagesByChatAfterTime(primitive.ObjectID, time.Time, int, int) ([]models.Message, int64, error) {
	return nil, 0, nil
}

func (s *assistantReferenceStore) UpdateQueryVisualizationID(primitive.ObjectID, primitive.ObjectID, primitive.ObjectID) error {
	return nil
}
