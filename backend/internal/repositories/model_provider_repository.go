package repositories

import (
	"context"
	"errors"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/mongodb"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var (
	// ErrModelProviderNotFound marks a missing provider record.
	ErrModelProviderNotFound = errors.New("MODEL_PROVIDER_NOT_FOUND")
	// ErrModelProviderConflict marks a duplicate provider name or slug.
	ErrModelProviderConflict = errors.New("MODEL_PROVIDER_CONFLICT")
)

// ModelProviderRepository stores OpenAI-compatible provider configurations.
type ModelProviderRepository interface {
	List(ctx context.Context) ([]*models.ModelProvider, error)
	Get(ctx context.Context, id primitive.ObjectID) (*models.ModelProvider, error)
	GetByName(ctx context.Context, name string) (*models.ModelProvider, error)
	Create(ctx context.Context, provider *models.ModelProvider) error
	Update(ctx context.Context, provider *models.ModelProvider) error
	Delete(ctx context.Context, id primitive.ObjectID) error
	EnsureIndexes(ctx context.Context) error
}

type modelProviderRepository struct {
	collection *mongo.Collection
}

// NewModelProviderRepository creates a repository backed by the
// `model_providers` collection.
func NewModelProviderRepository(mongoClient *mongodb.MongoDBClient) ModelProviderRepository {
	return &modelProviderRepository{
		collection: mongoClient.Client.Database(mongoClient.Config.DatabaseName).Collection("model_providers",
			options.Collection().SetReadPreference(readpref.Primary())),
	}
}

// EnsureIndexes creates the case-insensitive unique name index.
func (r *modelProviderRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "name", Value: 1}},
		Options: options.Index().SetUnique(true).SetCollation(&options.Collation{
			Locale: "en", Strength: 2,
		}),
	})
	return err
}

// List returns all providers ordered by creation time.
func (r *modelProviderRepository) List(ctx context.Context) ([]*models.ModelProvider, error) {
	cursor, err := r.collection.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var out []*models.ModelProvider
	if err := cursor.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Get loads one provider by ID.
func (r *modelProviderRepository) Get(ctx context.Context, id primitive.ObjectID) (*models.ModelProvider, error) {
	var provider models.ModelProvider
	err := r.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&provider)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrModelProviderNotFound
	}
	if err != nil {
		return nil, err
	}
	return &provider, nil
}

// GetByName loads one provider by its case-insensitive display name.
func (r *modelProviderRepository) GetByName(ctx context.Context, name string) (*models.ModelProvider, error) {
	opts := options.FindOne().SetCollation(&options.Collation{Locale: "en", Strength: 2})
	var provider models.ModelProvider
	err := r.collection.FindOne(ctx, bson.M{"name": name}, opts).Decode(&provider)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrModelProviderNotFound
	}
	if err != nil {
		return nil, err
	}
	return &provider, nil
}

// Create inserts a new provider; duplicate names surface as a conflict.
func (r *modelProviderRepository) Create(ctx context.Context, provider *models.ModelProvider) error {
	if _, err := r.collection.InsertOne(ctx, provider); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrModelProviderConflict
		}
		return err
	}
	return nil
}

// Update replaces a provider document by ID.
func (r *modelProviderRepository) Update(ctx context.Context, provider *models.ModelProvider) error {
	result, err := r.collection.ReplaceOne(ctx, bson.M{"_id": provider.ID}, provider)
	if mongo.IsDuplicateKeyError(err) {
		return ErrModelProviderConflict
	}
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return ErrModelProviderNotFound
	}
	return nil
}

// Delete removes a provider; a missing record is reported.
func (r *modelProviderRepository) Delete(ctx context.Context, id primitive.ObjectID) error {
	result, err := r.collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if result.DeletedCount == 0 {
		return ErrModelProviderNotFound
	}
	return nil
}
