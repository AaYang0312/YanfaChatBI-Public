package repositories

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"math"
	"yanfachatbi/internal/models"
	"time"
)

var ErrKnowledgeStorageConflict = errors.New("knowledge storage revision conflict")

// Optional: old read-only/legacy fakes need not implement v2 writes.
type ObjectKnowledgeRepository interface {
	CompareAndSwapKnowledge(context.Context, *models.KnowledgeBase, *models.KnowledgeBase) (bool, error)
}
type objectKnowledgeWriteCollection interface {
	UpdateOne(context.Context, any, any, ...*options.UpdateOptions) (*mongo.UpdateResult, error)
	InsertOne(context.Context, any, ...*options.InsertOneOptions) (*mongo.InsertOneResult, error)
}

func writeObjectKnowledgeCAS(ctx context.Context, w objectKnowledgeWriteCollection, ensure func(context.Context) error, previous, next *models.KnowledgeBase) (bool, error) {
	if next == nil || next.ID.IsZero() || next.ChatID.IsZero() || next.UserID.IsZero() || next.KnowledgeVersion != 2 {
		return false, ErrKnowledgeStorageConflict
	}
	expected := int64(0)
	if previous != nil {
		expected = previous.StorageRevision
		if previous.ID != next.ID || previous.ChatID != next.ChatID || (!previous.UserID.IsZero() && previous.UserID != next.UserID) {
			return false, ErrKnowledgeStorageConflict
		}
	}
	if expected < 0 || expected == math.MaxInt64 || next.StorageRevision != expected+1 {
		return false, ErrKnowledgeStorageConflict
	}
	// A successful unique chat_id index is a prerequisite, not an async hint.
	// Without it concurrent legacy/v2 inserts could create separate documents.
	if err := ensure(ctx); err != nil {
		return false, err
	}
	if previous == nil {
		_, err := w.InsertOne(ctx, next)
		if mongo.IsDuplicateKeyError(err) {
			return false, nil
		}
		return err == nil, err
	}
	var revision any = expected
	if expected == 0 {
		revision = bson.M{"$in": bson.A{nil, int64(0)}}
	}
	var owner any = previous.UserID
	if previous.UserID.IsZero() {
		owner = bson.M{"$in": bson.A{nil, primitive.NilObjectID}}
	}
	filter := bson.M{"_id": previous.ID, "chat_id": previous.ChatID, "user_id": owner, "storage_revision": revision}
	update := bson.M{"$set": bson.M{
		"user_id": next.UserID, "storage_revision": next.StorageRevision, "knowledge_version": 2,
		"kb_revision": next.KBRevision, "table_descriptions": next.TableDescriptions, "updated_at": next.UpdatedAt,
	}}
	result, err := w.UpdateOne(ctx, filter, update, options.Update().SetUpsert(false))
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return result != nil && result.MatchedCount == 1, nil
}

func writeLegacyKnowledge(ctx context.Context, w objectKnowledgeWriteCollection, ensure func(context.Context) error, kb *models.KnowledgeBase, now time.Time) error {
	if kb == nil || kb.ID.IsZero() || kb.ChatID.IsZero() || kb.KnowledgeVersion >= 2 {
		return ErrKnowledgeStorageConflict
	}
	if err := ensure(ctx); err != nil {
		return err
	}
	filter := bson.M{"chat_id": kb.ChatID, "knowledge_version": bson.M{"$ne": 2},
		"user_id": bson.M{"$in": bson.A{nil, primitive.NilObjectID, kb.UserID}}}
	update := bson.M{
		"$set":         bson.M{"table_descriptions": kb.TableDescriptions, "user_id": kb.UserID, "updated_at": now},
		"$inc":         bson.M{"storage_revision": int64(1)},
		"$setOnInsert": bson.M{"_id": kb.ID, "chat_id": kb.ChatID, "created_at": kb.CreatedAt},
	}
	_, err := w.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	// A v2 document fails the predicate; its unique chat_id fences the attempted
	// upsert. A legacy writer before promotion advances StorageRevision instead.
	if mongo.IsDuplicateKeyError(err) {
		return ErrKnowledgeStorageConflict
	}
	return err
}
