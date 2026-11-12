package repositories

import (
	"context"
	"fmt"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/mongodb"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// KnowledgeBaseRepository defines operations for knowledge base persistence.
type KnowledgeBaseRepository interface {
	Upsert(ctx context.Context, kb *models.KnowledgeBase) error
	FindByChatID(ctx context.Context, chatID primitive.ObjectID) (*models.KnowledgeBase, error)
	DeleteByChatID(ctx context.Context, chatID primitive.ObjectID) error
	GetTableDescriptions(ctx context.Context, chatID primitive.ObjectID, tableNames []string) ([]models.TableDescription, error)
}

type knowledgeBaseRepository struct {
	collection *mongo.Collection
}

// NewKnowledgeBaseRepository creates a new repository backed by the `knowledge_bases` MongoDB collection.
func NewKnowledgeBaseRepository(mongoClient *mongodb.MongoDBClient) KnowledgeBaseRepository {
	repo := &knowledgeBaseRepository{
		collection: mongoClient.Client.Database(mongoClient.Config.DatabaseName).Collection("knowledge_bases", options.Collection().SetReadPreference(readpref.Primary())),
	}

	return repo
}

// Upsert creates or updates the knowledge base for a chat.
func (r *knowledgeBaseRepository) Upsert(ctx context.Context, kb *models.KnowledgeBase) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := writeLegacyKnowledge(ctx, r.collection, r.ensureUniqueKnowledgeChat, kb, now); err != nil {
		return err
	}
	kb.UpdatedAt = now
	return nil
}

func (r *knowledgeBaseRepository) ensureUniqueKnowledgeChat(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "chat_id", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return err
}

func (r *knowledgeBaseRepository) CompareAndSwapKnowledge(ctx context.Context, previous, next *models.KnowledgeBase) (bool, error) {
	return writeObjectKnowledgeCAS(ctx, r.collection, r.ensureUniqueKnowledgeChat, previous, next)
}

// FindByChatID retrieves the knowledge base for a specific chat.
func (r *knowledgeBaseRepository) FindByChatID(ctx context.Context, chatID primitive.ObjectID) (*models.KnowledgeBase, error) {
	var kb models.KnowledgeBase
	err := r.collection.FindOne(ctx, bson.M{"chat_id": chatID}).Decode(&kb)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil // No KB yet — this is normal
		}
		return nil, fmt.Errorf("failed to find knowledge base for chat %s: %w", chatID.Hex(), err)
	}

	return &kb, nil
}

// DeleteByChatID removes the knowledge base when a chat is deleted.
func (r *knowledgeBaseRepository) DeleteByChatID(ctx context.Context, chatID primitive.ObjectID) error {
	_, err := r.collection.DeleteOne(ctx, bson.M{"chat_id": chatID})
	if err != nil {
		return fmt.Errorf("failed to delete knowledge base for chat %s: %w", chatID.Hex(), err)
	}

	return nil
}

// GetTableDescriptions returns descriptions only for the requested tables.
// Useful for RAG context enrichment — only fetch descriptions for tables in the query.
func (r *knowledgeBaseRepository) GetTableDescriptions(ctx context.Context, chatID primitive.ObjectID, tableNames []string) ([]models.TableDescription, error) {
	kb, err := r.FindByChatID(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if kb == nil || kb.KnowledgeVersion >= 2 {
		return nil, nil
	}

	// Build a set of requested table names for quick lookup
	requested := make(map[string]bool, len(tableNames))
	for _, name := range tableNames {
		requested[name] = true
	}

	result := make([]models.TableDescription, 0)
	for _, td := range kb.TableDescriptions {
		if requested[td.TableName] {
			result = append(result, td)
		}
	}

	return result, nil
}
