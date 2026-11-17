package vectordb

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"sync"
	"time"
	"yanfachatbi/internal/constants"

	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// QdrantConfig holds configuration for connecting to a Qdrant instance.
type QdrantConfig struct {
	Host              string
	Port              string
	APIKey            string
	UseTLS            bool
	SchemaBM25        bool // Requires Qdrant 1.20+.
	SchemaCollection  string
	MessageCollection string
}

// QdrantClient implements the Client interface using Qdrant's gRPC API.
type QdrantClient struct {
	conn              *grpc.ClientConn
	pointsClient      pb.PointsClient
	collectionsClient pb.CollectionsClient
	qdrantClient      pb.QdrantClient
	collectionNames   CollectionNames
	objectCollection  string // nonempty only on a dedicated v2 adapter
	schemaBM25        bool

	// Cached health status to avoid calling Qdrant on every request.
	healthMu      sync.RWMutex
	healthCached  bool // last known health status
	healthChecked time.Time
}

const (
	// healthCacheTTL controls how long a positive health check result is trusted.
	// During this window, IsHealthy returns the cached result without a gRPC call.
	healthCacheTTL = 60 * time.Second

	// healthCacheNegativeTTL is shorter so we re-check sooner after a failure,
	// allowing quick recovery once Qdrant comes back.
	healthCacheNegativeTTL = 10 * time.Second

	// healthCheckTimeout is the per-attempt timeout for the gRPC HealthCheck call.
	// Increased from 3s → 5s to handle cloud-hosted Qdrant cold-starts.
	healthCheckTimeout = 5 * time.Second
)

// NewQdrantClient creates a new Qdrant gRPC client.
func NewQdrantClient(cfg QdrantConfig) (*QdrantClient, error) {
	names, err := ResolveCollectionNames(cfg.SchemaCollection, cfg.MessageCollection)
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)

	opts := []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	if cfg.UseTLS {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	if cfg.APIKey != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(&apiKeyCredentials{apiKey: cfg.APIKey}))
	}

	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Qdrant at %s: %w", addr, err)
	}

	client := &QdrantClient{
		conn:              conn,
		collectionNames:   names,
		schemaBM25:        cfg.SchemaBM25,
		pointsClient:      pb.NewPointsClient(conn),
		collectionsClient: pb.NewCollectionsClient(conn),
		qdrantClient:      pb.NewQdrantClient(conn),
	}

	log.Printf("VectorDB -> Qdrant client created for %s (TLS: %v)", addr, cfg.UseTLS)

	return client, nil
}

// IsHealthy checks if Qdrant is reachable, with cached results to avoid
// hammering the server on every request. Positive results are cached for 60s,
// negative results for 10s (to allow quick recovery). Includes a single retry
// with a short backoff on failure.
func (q *QdrantClient) IsHealthy(ctx context.Context) bool {
	q.healthMu.RLock()
	if !q.healthChecked.IsZero() {
		ttl := healthCacheTTL
		if !q.healthCached {
			ttl = healthCacheNegativeTTL
		}
		if time.Since(q.healthChecked) < ttl {
			cached := q.healthCached
			q.healthMu.RUnlock()
			return cached
		}
	}
	q.healthMu.RUnlock()

	// Perform the actual health check (with one retry on failure).
	healthy := q.doHealthCheck(ctx)

	q.healthMu.Lock()
	q.healthCached = healthy
	q.healthChecked = time.Now()
	q.healthMu.Unlock()

	return healthy
}

// doHealthCheck performs the gRPC health check with a single retry + backoff.
func (q *QdrantClient) doHealthCheck(ctx context.Context) bool {
	if q.tryHealthCheck(ctx) {
		return true
	}

	// First attempt failed — retry once after a short backoff.
	// This handles transient network blips and Qdrant cold-start latency.
	time.Sleep(500 * time.Millisecond)

	if q.tryHealthCheck(ctx) {
		log.Printf("VectorDB -> Qdrant health check succeeded on retry")
		return true
	}

	return false
}

// tryHealthCheck performs a single gRPC HealthCheck call with timeout.
func (q *QdrantClient) tryHealthCheck(ctx context.Context) bool {
	hcCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()

	_, err := q.qdrantClient.HealthCheck(hcCtx, &pb.HealthCheckRequest{})
	if err != nil {
		log.Printf("VectorDB -> Qdrant health check failed: %v", err)
		return false
	}
	return true
}

// EnsureCollection creates a collection if it doesn't already exist, with the given payload indexes.
func (q *QdrantClient) EnsureCollection(ctx context.Context, collection string, dimension int) error {
	return q.ensureCollectionWithIndexes(ctx, collection, dimension, nil)
}

// EnsureSchemaCollection creates the schema collection with schema-specific payload indexes,
// including a full-text index on the "content" field for hybrid search.
func (q *QdrantClient) EnsureSchemaCollection(ctx context.Context, dimension int) error {
	indexes := []string{"chat_id", "type", "table_name"}
	if q.usesCustomCollections() || q.schemaBM25 {
		// Qdrant allows one payload type per field; hybrid retrieval uses table_name as text.
		indexes = []string{"chat_id", "type"}
	}
	if err := q.ensureCollectionWithIndexes(ctx, constants.SchemaCollectionName, dimension, indexes); err != nil {
		return err
	}
	// Create full-text index on "content" for hybrid keyword search (idempotent)
	if err := q.ensureTextIndex(ctx, constants.SchemaCollectionName, "content"); err != nil {
		if q.usesCustomCollections() || q.schemaBM25 {
			return err
		}
		log.Printf("VectorDB -> Warning: failed to create text index on 'content' in schema collection: %v", err)
	}
	// Create full-text index on "table_name" for direct table name matching
	if err := q.ensureTextIndex(ctx, constants.SchemaCollectionName, "table_name"); err != nil {
		if q.usesCustomCollections() || q.schemaBM25 {
			return err
		}
		log.Printf("VectorDB -> Warning: failed to create text index on 'table_name' in schema collection: %v", err)
	}
	if q.schemaBM25 {
		if err := q.validateBM25Indexes(ctx, constants.SchemaCollectionName); err != nil {
			return err
		}
	}
	if q.usesCustomCollections() {
		return q.validatePayloadIndexes(ctx, constants.SchemaCollectionName, map[string]pb.PayloadSchemaType{
			"chat_id": pb.PayloadSchemaType_Keyword, "type": pb.PayloadSchemaType_Keyword,
			"content": pb.PayloadSchemaType_Text, "table_name": pb.PayloadSchemaType_Text,
		})
	}
	return nil
}

// EnsureMessageCollection creates the message collection with message-specific payload indexes.
func (q *QdrantClient) EnsureMessageCollection(ctx context.Context, dimension int) error {
	indexes := []string{"chat_id", "message_id", "role"}
	if err := q.ensureCollectionWithIndexes(ctx, constants.MessageCollectionName, dimension, indexes); err != nil {
		return err
	}
	if q.usesCustomCollections() {
		return q.validatePayloadIndexes(ctx, constants.MessageCollectionName, map[string]pb.PayloadSchemaType{
			"chat_id": pb.PayloadSchemaType_Keyword, "message_id": pb.PayloadSchemaType_Keyword, "role": pb.PayloadSchemaType_Keyword,
		})
	}
	return nil
}

// ensureCollectionWithIndexes is a reusable helper that creates a collection (if absent)
// and ensures the given keyword payload indexes exist.
func (q *QdrantClient) ensureCollectionWithIndexes(ctx context.Context, collection string, dimension int, indexFields []string) error {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return err
	}

	if q.usesCustomCollections() && dimension <= 0 {
		return fmt.Errorf("custom embedding collection dimension must be positive")
	}
	// Check if collection exists
	listResp, err := q.collectionsClient.List(ctx, &pb.ListCollectionsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list collections: %w", err)
	}

	exists := false
	for _, col := range listResp.GetCollections() {
		if col.GetName() == collection {
			exists = true
			break
		}
	}

	if !exists {
		// Create collection with cosine distance
		dim := uint64(dimension)
		_, err = q.collectionsClient.Create(ctx, &pb.CreateCollection{
			CollectionName: collection,
			VectorsConfig: &pb.VectorsConfig{
				Config: &pb.VectorsConfig_Params{
					Params: &pb.VectorParams{
						Size:     dim,
						Distance: pb.Distance_Cosine,
					},
				},
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create collection '%s': %w", collection, err)
		}
		log.Printf("VectorDB -> Created collection '%s' with dimension %d", collection, dimension)
	} else {
		if q.usesCustomCollections() {
			if err := q.validateCollection(ctx, collection, dimension); err != nil {
				return err
			}
		}
		log.Printf("VectorDB -> Collection '%s' already exists", collection)
	}

	// Create payload indexes (idempotent — Qdrant ignores duplicates)
	boolTrue := true
	for _, field := range indexFields {
		_, indexErr := q.pointsClient.CreateFieldIndex(ctx, &pb.CreateFieldIndexCollection{
			CollectionName: collection,
			FieldName:      field,
			FieldType:      pb.FieldType_FieldTypeKeyword.Enum(),
			Wait:           &boolTrue,
		})
		if indexErr != nil {
			if q.usesCustomCollections() {
				return fmt.Errorf("failed to create required index %q on %q: %w", field, collection, indexErr)
			}
			log.Printf("VectorDB -> Warning: failed to create '%s' index on '%s': %v", field, collection, indexErr)
		} else {
			log.Printf("VectorDB -> Ensured payload index '%s' on collection '%s'", field, collection)
		}
	}

	return nil
}

// ensureTextIndex creates a full-text payload index on a field (idempotent).
// Uses word tokenizer with lowercase for best keyword matching.
func (q *QdrantClient) ensureTextIndex(ctx context.Context, collection string, fieldName string) error {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return err
	}

	boolTrue := true
	lowercase := true
	fieldType := pb.FieldType_FieldTypeText
	params := &pb.TextIndexParams{Tokenizer: pb.TokenizerType_Word, Lowercase: &lowercase}
	if q.schemaBM25 {
		params.Tokenizer = pb.TokenizerType_Multilingual
		if fieldName == "table_name" {
			params.Tokenizer = pb.TokenizerType_Code
		}
		params.Scoring = &pb.TextScoringParams{Type: pb.TextScoringType_Bm25}
	}

	_, err = q.pointsClient.CreateFieldIndex(ctx, &pb.CreateFieldIndexCollection{
		CollectionName: collection,
		FieldName:      fieldName,
		FieldType:      &fieldType,
		Wait:           &boolTrue,
		FieldIndexParams: &pb.PayloadIndexParams{
			IndexParams: &pb.PayloadIndexParams_TextIndexParams{
				TextIndexParams: params,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create text index '%s' on '%s': %w", fieldName, collection, err)
	}
	log.Printf("VectorDB -> Ensured text index '%s' on collection '%s'", fieldName, collection)
	return nil
}

// Upsert inserts or updates vector points in a collection.
func (q *QdrantClient) Upsert(ctx context.Context, collection string, points []VectorPoint) error {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return err
	}

	if len(points) == 0 {
		return nil
	}

	pbPoints := make([]*pb.PointStruct, 0, len(points))
	for _, pt := range points {
		// Convert payload to Qdrant Value format
		payload := make(map[string]*pb.Value)
		for k, v := range pt.Payload {
			payload[k] = toQdrantValue(v)
		}

		pbPoints = append(pbPoints, &pb.PointStruct{
			Id: &pb.PointId{
				PointIdOptions: &pb.PointId_Uuid{
					Uuid: q.storagePointID(pt.ID),
				},
			},
			Vectors: &pb.Vectors{
				VectorsOptions: &pb.Vectors_Vector{
					Vector: &pb.Vector{
						Data: pt.Vector,
					},
				},
			},
			Payload: payload,
		})
	}

	boolTrue := true
	_, err = q.pointsClient.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: collection,
		Points:         pbPoints,
		Wait:           &boolTrue,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert %d points: %w", len(points), err)
	}

	return nil
}

// Search performs similarity search within a collection.
func (q *QdrantClient) Search(ctx context.Context, collection string, req SearchRequest) ([]SearchResult, error) {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return nil, err
	}

	topK := uint64(req.TopK)
	if topK == 0 {
		topK = uint64(constants.DefaultTopK)
	}

	searchReq := &pb.SearchPoints{
		CollectionName: collection,
		Vector:         req.Vector,
		Limit:          topK,
		WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
		ScoreThreshold: &req.ScoreThreshold,
	}

	// Build filter from key-value pairs
	if len(req.Filter) > 0 {
		conditions := make([]*pb.Condition, 0, len(req.Filter))
		for key, value := range req.Filter {
			conditions = append(conditions, &pb.Condition{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: key,
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: value,
							},
						},
					},
				},
			})
		}
		searchReq.Filter = &pb.Filter{
			Must: conditions,
		}
	}

	resp, err := q.pointsClient.Search(ctx, searchReq)
	if err != nil {
		return nil, fmt.Errorf("qdrant search error: %w", err)
	}

	results := make([]SearchResult, 0, len(resp.GetResult()))
	for _, r := range resp.GetResult() {
		payload := make(map[string]interface{})
		for k, v := range r.GetPayload() {
			payload[k] = fromQdrantValue(v)
		}

		pointID := ""
		if r.GetId().GetUuid() != "" {
			pointID = r.GetId().GetUuid()
		}

		results = append(results, SearchResult{
			ID:      pointID,
			Score:   r.GetScore(),
			Payload: payload,
		})
	}

	return results, nil
}

// HybridSearch performs a server-side hybrid search using Qdrant's Query API.
// The dense leg always uses a similarity threshold. When SchemaBM25 is on,
// the text legs are independently ranked by BM25; otherwise they filter
// dense searches by text. All legs share the visibility filter and use RRF.
func (q *QdrantClient) HybridSearch(ctx context.Context, collection string, req HybridSearchRequest) ([]SearchResult, error) {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return nil, err
	}

	topK := uint64(req.TopK)
	if topK == 0 {
		topK = uint64(constants.DefaultTopK)
	}

	// --- Build shared filter (e.g., chat_id) ---
	var sharedConditions []*pb.Condition
	for key, value := range req.Filter {
		sharedConditions = append(sharedConditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: key,
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{
							Keyword: value,
						},
					},
				},
			},
		})
	}

	var sharedFilter *pb.Filter
	if len(sharedConditions) > 0 {
		sharedFilter = &pb.Filter{Must: sharedConditions}
	}

	// --- Prefetch Leg 1: Pure semantic vector search (broad) ---
	prefetchLimit := topK * 3 // fetch more candidates for better fusion
	semanticPrefetch := &pb.PrefetchQuery{
		Query:          pb.NewQueryDense(req.Vector),
		Filter:         sharedFilter,
		Limit:          &prefetchLimit,
		ScoreThreshold: &req.ScoreThreshold,
	}

	prefetches := []*pb.PrefetchQuery{semanticPrefetch}

	addTextLeg := func(field, query string) {
		if field == "" || query == "" {
			return
		}
		if q.schemaBM25 {
			// A lexical match can enter the candidate set without any dense score.
			prefetches = append(prefetches, &pb.PrefetchQuery{
				Query: pb.NewQueryText(pb.NewTextQueryBM25(query, &pb.Bm25Params{})),
				Using: &field, Filter: sharedFilter, Limit: &prefetchLimit,
			})
			return
		}
		legConditions := make([]*pb.Condition, 0, len(sharedConditions)+1)
		legConditions = append(legConditions, sharedConditions...)
		legConditions = append(legConditions, pb.NewMatchText(field, query))

		legFilter := &pb.Filter{Must: legConditions}

		legPrefetch := &pb.PrefetchQuery{
			Query:  pb.NewQueryDense(req.Vector),
			Filter: legFilter,
			Limit:  &prefetchLimit,
		}
		prefetches = append(prefetches, legPrefetch)
	}
	addTextLeg(req.TextField, req.TextQuery)
	for _, leg := range req.ExtraTextLegs {
		addTextLeg(leg.Field, leg.Query)
	}

	// --- Top-level: RRF Fusion ---
	queryReq := &pb.QueryPoints{
		CollectionName: collection,
		Prefetch:       prefetches,
		Query:          pb.NewQueryFusion(pb.Fusion_RRF),
		Limit:          &topK,
		WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
	}

	resp, err := q.pointsClient.Query(ctx, queryReq)
	if err != nil {
		return nil, fmt.Errorf("qdrant hybrid search error: %w", err)
	}

	results := make([]SearchResult, 0, len(resp.GetResult()))
	for _, r := range resp.GetResult() {
		payload := make(map[string]interface{})
		for k, v := range r.GetPayload() {
			payload[k] = fromQdrantValue(v)
		}

		pointID := ""
		if r.GetId().GetUuid() != "" {
			pointID = r.GetId().GetUuid()
		}

		results = append(results, SearchResult{
			ID:      pointID,
			Score:   r.GetScore(),
			Payload: payload,
		})
	}

	return results, nil
}

// Delete removes points by their IDs from a collection.
func (q *QdrantClient) Delete(ctx context.Context, collection string, ids []PointID) error {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return err
	}

	if len(ids) == 0 {
		return nil
	}

	pbIDs := make([]*pb.PointId, 0, len(ids))
	for _, id := range ids {
		pbIDs = append(pbIDs, &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{
				Uuid: q.storagePointID(id),
			},
		})
	}

	boolTrue := true
	_, err = q.pointsClient.Delete(ctx, &pb.DeletePoints{
		CollectionName: collection,
		Wait:           &boolTrue,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Points{
				Points: &pb.PointsIdsList{
					Ids: pbIDs,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete points: %w", err)
	}

	return nil
}

// DeleteByFilter removes all points matching the filter conditions.
func (q *QdrantClient) DeleteByFilter(ctx context.Context, collection string, filters []FilterCondition) error {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return err
	}

	if len(filters) == 0 {
		return fmt.Errorf("at least one filter condition is required for DeleteByFilter")
	}

	conditions := make([]*pb.Condition, 0, len(filters))
	for _, f := range filters {
		conditions = append(conditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: f.Key,
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{
							Keyword: f.Value,
						},
					},
				},
			},
		})
	}

	boolTrue := true
	_, err = q.pointsClient.Delete(ctx, &pb.DeletePoints{
		CollectionName: collection,
		Wait:           &boolTrue,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{
				Filter: &pb.Filter{
					Must: conditions,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete by filter: %w", err)
	}

	return nil
}

// Count returns the number of points matching the filter in a collection.
func (q *QdrantClient) Count(ctx context.Context, collection string, filters []FilterCondition) (int64, error) {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return 0, err
	}

	exact := true
	countReq := &pb.CountPoints{
		CollectionName: collection,
		Exact:          &exact,
	}

	if len(filters) > 0 {
		conditions := make([]*pb.Condition, 0, len(filters))
		for _, f := range filters {
			conditions = append(conditions, &pb.Condition{
				ConditionOneOf: &pb.Condition_Field{
					Field: &pb.FieldCondition{
						Key: f.Key,
						Match: &pb.Match{
							MatchValue: &pb.Match_Keyword{
								Keyword: f.Value,
							},
						},
					},
				},
			})
		}
		countReq.Filter = &pb.Filter{
			Must: conditions,
		}
	}

	resp, err := q.pointsClient.Count(ctx, countReq)
	if err != nil {
		return 0, fmt.Errorf("failed to count points: %w", err)
	}

	return int64(resp.GetResult().GetCount()), nil
}

// ScrollByFilter retrieves all points matching the filter from a collection.
// When withVectors is true, the response includes the embedding vectors (needed for copying).
func (q *QdrantClient) ScrollByFilter(ctx context.Context, collection string, filters []FilterCondition, withVectors bool) ([]VectorPoint, error) {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return nil, err
	}

	if len(filters) == 0 {
		return nil, fmt.Errorf("at least one filter condition is required for ScrollByFilter")
	}

	conditions := make([]*pb.Condition, 0, len(filters))
	for _, f := range filters {
		conditions = append(conditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: f.Key,
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{
							Keyword: f.Value,
						},
					},
				},
			},
		})
	}

	var allPoints []VectorPoint
	var offset *pb.PointId
	batchSize := uint32(100)

	for {
		scrollReq := &pb.ScrollPoints{
			CollectionName: collection,
			Filter:         &pb.Filter{Must: conditions},
			Limit:          &batchSize,
			WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
			WithVectors: &pb.WithVectorsSelector{
				SelectorOptions: &pb.WithVectorsSelector_Enable{Enable: withVectors},
			},
		}
		if offset != nil {
			scrollReq.Offset = offset
		}

		resp, err := q.pointsClient.Scroll(ctx, scrollReq)
		if err != nil {
			return nil, fmt.Errorf("qdrant scroll error: %w", err)
		}

		for _, r := range resp.GetResult() {
			payload := make(map[string]interface{})
			for k, v := range r.GetPayload() {
				payload[k] = fromQdrantValue(v)
			}

			pt := VectorPoint{
				Payload: payload,
			}

			// Extract point ID
			if r.GetId().GetUuid() != "" {
				pt.ID = PointID(r.GetId().GetUuid())
			}

			// Extract vector if requested
			if withVectors {
				if vec := r.GetVectors().GetVector(); vec != nil {
					pt.Vector = vec.GetDenseVector().GetData()
				}
			}

			allPoints = append(allPoints, pt)
		}

		// Check if there's a next page
		nextPage := resp.GetNextPageOffset()
		if nextPage == nil {
			break
		}
		offset = nextPage
	}

	return allPoints, nil
}

// Close cleans up the gRPC connection.
func (q *QdrantClient) Close() error {
	if q.conn != nil {
		return q.conn.Close()
	}
	return nil
}

// usesCustomCollections leaves zero-value internal clients and legacy clients generic.
func (q *QdrantClient) usesCustomCollections() bool {
	return q.objectCollection != "" || (q.collectionNames != (CollectionNames{}) && !q.collectionNames.IsDefault())
}

// resolveCollection maps legacy logical names and confines an opted-in client to its pair.
func (q *QdrantClient) resolveCollection(collection string) (string, error) {
	if q.objectCollection != "" {
		if collection != q.objectCollection {
			return "", fmt.Errorf("collection outside the object index")
		}
		return collection, nil
	}
	if !q.usesCustomCollections() {
		return collection, nil
	}
	switch collection {
	case constants.SchemaCollectionName, q.collectionNames.Schema:
		return q.collectionNames.Schema, nil
	case constants.MessageCollectionName, q.collectionNames.Messages:
		return q.collectionNames.Messages, nil
	default:
		return "", fmt.Errorf("collection %q is outside the configured embedding collection pair", collection)
	}
}

func (q *QdrantClient) validateCollection(ctx context.Context, collection string, dimension int) error {
	response, err := q.collectionsClient.Get(ctx, &pb.GetCollectionInfoRequest{CollectionName: collection})
	if err != nil {
		return fmt.Errorf("failed to inspect embedding collection %q: %w", collection, err)
	}
	collectionParams := response.GetResult().GetConfig().GetParams()
	vectors := collectionParams.GetVectorsConfig()
	params := vectors.GetParams()
	if params == nil || params.GetMultivectorConfig() != nil || len(collectionParams.GetSparseVectorsConfig().GetMap()) != 0 {
		return fmt.Errorf("embedding collection %q must use a single unnamed dense vector", collection)
	}
	if params.GetSize() != uint64(dimension) || params.GetDistance() != pb.Distance_Cosine {
		return fmt.Errorf("embedding collection %q is incompatible: expected dimension %d and Cosine, got dimension %d and %s", collection, dimension, params.GetSize(), params.GetDistance())
	}
	return nil
}

// Validate the resulting server schema, not just successful index RPC responses.
func (q *QdrantClient) validatePayloadIndexes(ctx context.Context, collection string, expected map[string]pb.PayloadSchemaType) error {
	collection, err := q.resolveCollection(collection)
	if err != nil {
		return err
	}
	response, err := q.collectionsClient.Get(ctx, &pb.GetCollectionInfoRequest{CollectionName: collection})
	if err != nil {
		return fmt.Errorf("failed to inspect required indexes on %q: %w", collection, err)
	}
	actual := response.GetResult().GetPayloadSchema()
	for field, kind := range expected {
		if actual[field].GetDataType() != kind {
			return fmt.Errorf("embedding collection %q requires %s index %q, got %s", collection, kind, field, actual[field].GetDataType())
		}
	}
	return nil
}

func (q *QdrantClient) validateBM25Indexes(ctx context.Context, collection string) error {
	name, err := q.resolveCollection(collection)
	if err != nil {
		return err
	}
	info, err := q.collectionsClient.Get(ctx, &pb.GetCollectionInfoRequest{CollectionName: name})
	if err != nil {
		return fmt.Errorf("inspect BM25 indexes on %q: %w", name, err)
	}
	for _, field := range []string{"content", "table_name"} {
		params := info.GetResult().GetPayloadSchema()[field].GetParams().GetTextIndexParams()
		scoring := params.GetScoring()
		if scoring == nil || scoring.GetType() != pb.TextScoringType_Bm25 {
			return fmt.Errorf("collection %q field %q lacks BM25 scoring; Qdrant 1.20+ is required", name, field)
		}
	}
	return nil
}
