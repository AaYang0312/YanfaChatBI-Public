package vectordb

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"
	"strconv"
)

// QdrantObjectStore borrows the existing connection, but confines every operation
// to one isolated collection. The original client retains its legacy pair policy
// and owns Close. No health mutex/cache is copied.
type QdrantObjectStore struct {
	q          *QdrantClient
	collection string
}

func NewQdrantObjectStore(base *QdrantClient, name string) (*QdrantObjectStore, error) {
	if base == nil || base.objectCollection != "" {
		return nil, fmt.Errorf("object store needs an existing schema/message client")
	}
	name, err := ResolveObjectCollectionName(base.collectionNames.Schema, base.collectionNames.Messages, name)
	if err != nil {
		return nil, err
	}
	q := &QdrantClient{conn: base.conn, pointsClient: base.pointsClient, collectionsClient: base.collectionsClient,
		qdrantClient: base.qdrantClient, collectionNames: base.collectionNames, objectCollection: name, schemaBM25: base.schemaBM25}
	return &QdrantObjectStore{q: q, collection: name}, nil
}
func (s *QdrantObjectStore) Ensure(ctx context.Context, dimension int) error {
	if dimension <= 0 {
		return fmt.Errorf("object embedding dimension must be positive")
	}
	keyword := []string{"owner_id", "chat_id", "source_key", "source_binding_revision", "connection_revision", "selection_revision",
		"index_format_version", "generation_id", "object_id", "object_instance_version", "object_type", "schema_name", "object_name",
		"schema_fingerprint", "snapshot_fingerprint", "kb_revision", "part_index", "total_parts", "type"}
	if err := s.q.ensureCollectionWithIndexes(ctx, s.collection, dimension, keyword); err != nil {
		return err
	}
	expected := map[string]pb.PayloadSchemaType{}
	for _, key := range keyword {
		expected[key] = pb.PayloadSchemaType_Keyword
	}
	for _, field := range []string{"content", "table_name"} {
		if err := s.q.ensureTextIndex(ctx, s.collection, field); err != nil {
			return err
		}
		expected[field] = pb.PayloadSchemaType_Text
	}
	if err := s.q.validateCollection(ctx, s.collection, dimension); err != nil {
		return err
	}
	if s.q.schemaBM25 {
		if err := s.q.validateBM25Indexes(ctx, s.collection); err != nil {
			return err
		}
	}
	return s.q.validatePayloadIndexes(ctx, s.collection, expected)
}
func (s *QdrantObjectStore) Upsert(ctx context.Context, points []VectorPoint) error {
	for _, p := range points {
		if _, err := uuid.Parse(p.ID); err != nil {
			return fmt.Errorf("object point ID must be UUID")
		}
	}
	return s.q.Upsert(ctx, s.collection, points)
}
func (s *QdrantObjectStore) Scroll(ctx context.Context, filters []FilterCondition) ([]VectorPoint, error) {
	if err := validateObjectGenerationFilter(filters); err != nil {
		return nil, err
	}
	return s.q.ScrollByFilter(ctx, s.collection, filters, true)
}
func (s *QdrantObjectStore) Search(ctx context.Context, request SearchRequest) ([]SearchResult, error) {
	if err := validateObjectGenerationFilter(objectRequestFilters(request.Filter)); err != nil {
		return nil, err
	}
	return s.q.Search(ctx, s.collection, request)
}
func (s *QdrantObjectStore) HybridSearch(ctx context.Context, request HybridSearchRequest) ([]SearchResult, error) {
	if err := validateObjectGenerationFilter(objectRequestFilters(request.Filter)); err != nil {
		return nil, err
	}
	return s.q.HybridSearch(ctx, s.collection, request)
}
func (s *QdrantObjectStore) DeleteGeneration(ctx context.Context, filters []FilterCondition) error {
	if err := validateObjectGenerationFilter(filters); err != nil {
		return err
	}
	return s.q.DeleteByFilter(ctx, s.collection, filters)
}
func objectRequestFilters(m map[string]string) []FilterCondition {
	out := make([]FilterCondition, 0, len(m))
	for k, v := range m {
		out = append(out, FilterCondition{Key: k, Value: v})
	}
	return out
}
func validateObjectGenerationFilter(filters []FilterCondition) error {
	values := map[string]string{}
	for _, f := range filters {
		if _, exists := values[f.Key]; exists {
			return fmt.Errorf("duplicate object filter")
		}
		values[f.Key] = f.Value
	}
	for _, key := range []string{"owner_id", "chat_id", "source_key"} {
		if values[key] == "" {
			return fmt.Errorf("object generation filter requires %s", key)
		}
	}
	if values["index_format_version"] != "2" {
		return fmt.Errorf("object generation filter requires format 2")
	}
	if _, err := uuid.Parse(values["generation_id"]); err != nil {
		return fmt.Errorf("object generation filter requires generation UUID")
	}
	for _, key := range []string{"source_binding_revision", "connection_revision", "selection_revision"} {
		n, err := strconv.ParseInt(values[key], 10, 64)
		if err != nil || n < 0 {
			return fmt.Errorf("object generation filter requires %s", key)
		}
	}
	return nil
}

// Legacy logical IDs keep their existing mapping; v2 UUIDs are already the
// durable IDs verified by Scroll and must not be hashed a second time.
func (q *QdrantClient) storagePointID(id PointID) string {
	if q.objectCollection != "" {
		return id
	}
	return pointIDToUUID(id)
}

var _ ObjectStore = (*QdrantObjectStore)(nil)
