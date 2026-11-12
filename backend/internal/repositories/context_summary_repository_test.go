package repositories

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"yanfachatbi/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// memoryContextSummaryStore is the in-memory storage seam used to exercise the
// repository's CAS and unique-chat_id intent without a live MongoDB.
type memoryContextSummaryStore struct {
	docs         map[primitive.ObjectID]*models.ChatContextSummary
	insertCalls  int
	casCalls     int
	indexCalls   int
	indexEnsured bool
	indexErr     error
}

func newMemoryContextSummaryStore() *memoryContextSummaryStore {
	return &memoryContextSummaryStore{docs: map[primitive.ObjectID]*models.ChatContextSummary{}}
}

func (s *memoryContextSummaryStore) FindByChatID(_ context.Context, chatID primitive.ObjectID) (*models.ChatContextSummary, error) {
	doc, ok := s.docs[chatID]
	if !ok {
		return nil, errContextSummaryMissing
	}
	clone := *doc
	return &clone, nil
}

func (s *memoryContextSummaryStore) Insert(_ context.Context, summary *models.ChatContextSummary) error {
	s.insertCalls++
	if _, exists := s.docs[summary.ChatID]; exists {
		return errContextSummaryDuplicate
	}
	clone := *summary
	s.docs[summary.ChatID] = &clone
	return nil
}

func (s *memoryContextSummaryStore) ReplaceCAS(_ context.Context, chatID primitive.ObjectID, previousRevision int64, summary *models.ChatContextSummary) (bool, error) {
	s.casCalls++
	current, ok := s.docs[chatID]
	if !ok || current.Revision != previousRevision {
		return false, nil
	}
	clone := *summary
	s.docs[chatID] = &clone
	return true, nil
}

func (s *memoryContextSummaryStore) EnsureUniqueChatID(_ context.Context) error {
	s.indexCalls++
	if s.indexErr != nil {
		return s.indexErr
	}
	s.indexEnsured = true
	return nil
}

func testContextSummary(chatID primitive.ObjectID, revision int64, rendered string) *models.ChatContextSummary {
	return &models.ChatContextSummary{
		ChatID:   chatID,
		Revision: revision,
		Summary:  models.ContextSummary{Goal: "goal-" + rendered},
		Rendered: rendered,
	}
}

func TestContextSummaryRepositoryCASAdvancesOnlyOnMatchingRevision(t *testing.T) {
	ctx := context.Background()
	store := newMemoryContextSummaryStore()
	repo := newContextSummaryRepository(store)
	chatID := primitive.NewObjectID()

	first := testContextSummary(chatID, 1, "rev-1")
	if err := repo.UpsertCAS(ctx, 0, first); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if first.ID.IsZero() || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Fatalf("repository did not stamp identity/timestamps: %+v", first.Base)
	}
	if store.insertCalls != 1 || store.casCalls != 0 {
		t.Fatalf("first insert used insert=%d cas=%d", store.insertCalls, store.casCalls)
	}

	second := testContextSummary(chatID, 2, "rev-2")
	if err := repo.UpsertCAS(ctx, 1, second); err != nil {
		t.Fatalf("revision 2 CAS: %v", err)
	}
	got, err := repo.GetByChatID(ctx, chatID)
	if err != nil {
		t.Fatalf("get revision 2: %v", err)
	}
	if got.Revision != 2 || got.Rendered != "rev-2" {
		t.Fatalf("revision 2 not stored: %+v", got)
	}

	stale := testContextSummary(chatID, 2, "stale-rev-2")
	if err := repo.UpsertCAS(ctx, 1, stale); !errors.Is(err, ErrContextSummaryConflict) {
		t.Fatalf("stale revision accepted or wrong error: %v", err)
	}
	if got, err = repo.GetByChatID(ctx, chatID); err != nil || got.Revision != 2 || got.Rendered != "rev-2" {
		t.Fatalf("stale CAS overwrote stored revision: %+v err=%v", got, err)
	}

	casCalls := store.casCalls
	skipped := testContextSummary(chatID, 4, "skipped-rev-3")
	if err := repo.UpsertCAS(ctx, 2, skipped); !errors.Is(err, ErrContextSummaryConflict) {
		t.Fatalf("non-consecutive revision accepted or wrong error: %v", err)
	}
	if store.casCalls != casCalls {
		t.Fatalf("revision arithmetic checked after touching storage: cas calls %d -> %d", casCalls, store.casCalls)
	}
}

func TestContextSummaryRepositoryFirstInsertRespectsUniqueChatID(t *testing.T) {
	ctx := context.Background()
	store := newMemoryContextSummaryStore()
	repo := newContextSummaryRepository(store)
	chatID := primitive.NewObjectID()

	if err := repo.UpsertCAS(ctx, 0, testContextSummary(chatID, 1, "first")); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := repo.UpsertCAS(ctx, 0, testContextSummary(chatID, 1, "duplicate")); !errors.Is(err, ErrContextSummaryConflict) {
		t.Fatalf("duplicate chat_id insert accepted or wrong error: %v", err)
	}
	if len(store.docs) != 1 {
		t.Fatalf("duplicate chat_id created %d documents, want 1", len(store.docs))
	}
	if err := repo.UpsertCAS(ctx, 0, nil); !errors.Is(err, ErrContextSummaryConflict) {
		t.Fatalf("nil summary accepted or wrong error: %v", err)
	}
	if err := repo.UpsertCAS(ctx, 0, testContextSummary(primitive.NilObjectID, 1, "no-chat")); !errors.Is(err, ErrContextSummaryConflict) {
		t.Fatalf("zero chat_id accepted or wrong error: %v", err)
	}

	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	if !store.indexEnsured || store.indexCalls != 1 {
		t.Fatalf("unique chat_id index not requested: ensured=%v calls=%d", store.indexEnsured, store.indexCalls)
	}

	if _, err := repo.GetByChatID(ctx, primitive.NewObjectID()); !errors.Is(err, ErrContextSummaryNotFound) {
		t.Fatalf("missing summary error = %v want ErrContextSummaryNotFound", err)
	}
}

func TestContextSummaryRepositoryCASOnMissingDocumentConflicts(t *testing.T) {
	ctx := context.Background()
	store := newMemoryContextSummaryStore()
	repo := newContextSummaryRepository(store)

	next := testContextSummary(primitive.NewObjectID(), 6, "rev-6")
	if err := repo.UpsertCAS(ctx, 5, next); !errors.Is(err, ErrContextSummaryConflict) {
		t.Fatalf("CAS on missing document = %v want ErrContextSummaryConflict", err)
	}
	if len(store.docs) != 0 {
		t.Fatalf("CAS on missing document created %d documents", len(store.docs))
	}
}

func TestContextSummaryCASFilterPinsChatAndRevision(t *testing.T) {
	chatID := primitive.NewObjectID()
	filter := contextSummaryCASFilter(chatID, 7)
	if !reflect.DeepEqual(filter["chat_id"], chatID) || filter["revision"] != int64(7) {
		t.Fatalf("CAS filter does not pin chat_id+revision: %+v", filter)
	}
}

func TestContextSummaryRepositoryEnsureIndexesSurfacesErrors(t *testing.T) {
	sentinel := errors.New("index build failed")
	store := newMemoryContextSummaryStore()
	store.indexErr = sentinel
	repo := newContextSummaryRepository(store)
	if err := repo.EnsureIndexes(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("EnsureIndexes error = %v want the storage error", err)
	}
}

var _ ContextSummaryRepository = (*contextSummaryRepository)(nil)
var _ contextSummaryStore = (*memoryContextSummaryStore)(nil)
