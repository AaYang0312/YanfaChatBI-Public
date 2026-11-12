package repositories

import (
	"context"
	"errors"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/mongodb"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var (
	// ErrContextSummaryNotFound marks a chat without a stored summary.
	ErrContextSummaryNotFound = errors.New("CONTEXT_SUMMARY_NOT_FOUND")
	// ErrContextSummaryConflict marks a stale revision, a duplicate chat_id or
	// otherwise illegal CAS transition.
	ErrContextSummaryConflict = errors.New("CONTEXT_SUMMARY_CONFLICT")
)

const contextSummaryCollectionName = "chat_context_summaries"

// contextSummaryMissing and contextSummaryDuplicate are storage-level sentinels
// so the repository can map them to the public errors above.
var (
	errContextSummaryMissing   = errors.New("context summary missing")
	errContextSummaryDuplicate = errors.New("context summary chat_id already exists")
)

// ContextSummaryRepository stores the single per-chat conversation summary.
type ContextSummaryRepository interface {
	// GetByChatID loads the summary for a chat.
	GetByChatID(ctx context.Context, chatID primitive.ObjectID) (*models.ChatContextSummary, error)
	// UpsertCAS inserts revision 1 when previousRevision is 0 and otherwise
	// replaces the document only while its stored revision equals
	// previousRevision. next.Revision must equal previousRevision+1.
	UpsertCAS(ctx context.Context, previousRevision int64, next *models.ChatContextSummary) error
	// EnsureIndexes creates the unique chat_id index.
	EnsureIndexes(ctx context.Context) error
}

// contextSummaryStore is the storage seam. Production uses MongoDB; tests use
// an in-memory implementation to exercise CAS and uniqueness behavior.
type contextSummaryStore interface {
	FindByChatID(ctx context.Context, chatID primitive.ObjectID) (*models.ChatContextSummary, error)
	Insert(ctx context.Context, summary *models.ChatContextSummary) error
	ReplaceCAS(ctx context.Context, chatID primitive.ObjectID, previousRevision int64, summary *models.ChatContextSummary) (bool, error)
	EnsureUniqueChatID(ctx context.Context) error
}

type contextSummaryRepository struct {
	store contextSummaryStore
}

// NewContextSummaryRepository creates a repository backed by the
// `chat_context_summaries` collection.
func NewContextSummaryRepository(mongoClient *mongodb.MongoDBClient) ContextSummaryRepository {
	return newContextSummaryRepository(&mongoContextSummaryStore{
		collection: mongoClient.Client.Database(mongoClient.Config.DatabaseName).Collection(contextSummaryCollectionName,
			options.Collection().SetReadPreference(readpref.Primary())),
	})
}

func newContextSummaryRepository(store contextSummaryStore) ContextSummaryRepository {
	return &contextSummaryRepository{store: store}
}

// GetByChatID returns the chat's current summary.
func (r *contextSummaryRepository) GetByChatID(ctx context.Context, chatID primitive.ObjectID) (*models.ChatContextSummary, error) {
	if chatID.IsZero() {
		return nil, ErrContextSummaryNotFound
	}
	summary, err := r.store.FindByChatID(ctx, chatID)
	if errors.Is(err, errContextSummaryMissing) {
		return nil, ErrContextSummaryNotFound
	}
	if err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, ErrContextSummaryNotFound
	}
	return summary, nil
}

// UpsertCAS inserts the first revision or advances the stored revision by one.
func (r *contextSummaryRepository) UpsertCAS(ctx context.Context, previousRevision int64, next *models.ChatContextSummary) error {
	if next == nil || next.ChatID.IsZero() || previousRevision < 0 {
		return ErrContextSummaryConflict
	}
	if next.Revision != previousRevision+1 {
		return ErrContextSummaryConflict
	}

	now := time.Now().UTC()
	next.UpdatedAt = now
	if previousRevision == 0 {
		if next.CreatedAt.IsZero() {
			next.CreatedAt = now
		}
		if next.ID.IsZero() {
			next.ID = primitive.NewObjectID()
		}
		if err := r.store.Insert(ctx, next); err != nil {
			if errors.Is(err, errContextSummaryDuplicate) {
				return ErrContextSummaryConflict
			}
			return err
		}
		return nil
	}

	matched, err := r.store.ReplaceCAS(ctx, next.ChatID, previousRevision, next)
	if err != nil {
		return err
	}
	if !matched {
		return ErrContextSummaryConflict
	}
	return nil
}

// EnsureIndexes creates the unique chat_id index.
func (r *contextSummaryRepository) EnsureIndexes(ctx context.Context) error {
	return r.store.EnsureUniqueChatID(ctx)
}

// contextSummaryCASFilter pins the CAS update to one chat and one revision.
func contextSummaryCASFilter(chatID primitive.ObjectID, previousRevision int64) bson.M {
	return bson.M{"chat_id": chatID, "revision": previousRevision}
}

type mongoContextSummaryStore struct {
	collection *mongo.Collection
}

func (s *mongoContextSummaryStore) FindByChatID(ctx context.Context, chatID primitive.ObjectID) (*models.ChatContextSummary, error) {
	var summary models.ChatContextSummary
	err := s.collection.FindOne(ctx, bson.M{"chat_id": chatID}).Decode(&summary)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, errContextSummaryMissing
	}
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

func (s *mongoContextSummaryStore) Insert(ctx context.Context, summary *models.ChatContextSummary) error {
	if _, err := s.collection.InsertOne(ctx, summary); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return errContextSummaryDuplicate
		}
		return err
	}
	return nil
}

func (s *mongoContextSummaryStore) ReplaceCAS(ctx context.Context, chatID primitive.ObjectID, previousRevision int64, summary *models.ChatContextSummary) (bool, error) {
	result, err := s.collection.ReplaceOne(ctx, contextSummaryCASFilter(chatID, previousRevision), summary,
		options.Replace().SetUpsert(false))
	if err != nil {
		return false, err
	}
	return result.MatchedCount == 1, nil
}

func (s *mongoContextSummaryStore) EnsureUniqueChatID(ctx context.Context) error {
	_, err := s.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "chat_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	return err
}
