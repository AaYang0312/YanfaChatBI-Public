package vectordb

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/protobuf/proto"
)

// Opt-in fixture contract: only the isolated network-none test namespace supplies
// this loopback address. Never discover or load application credentials/config.
func TestCustomPairIsolatedQdrantAcceptance(t *testing.T) {
	addr := os.Getenv("SOURCE_ACCESS_TEST_QDRANT_ADDR")
	if addr == "" {
		t.Skip("isolated Qdrant fixture not configured")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("isolated Qdrant fixture must use an explicit IPv4 loopback address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("embedding_test_%d", time.Now().UnixNano())
	q, err := NewQdrantClient(QdrantConfig{Host: host, Port: port, SchemaCollection: prefix + "_schema", MessageCollection: prefix + "_messages"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	owned := []string{prefix + "_schema", prefix + "_messages", prefix + "_wrong_size", prefix + "_wrong_distance", prefix + "_with_sparse"}
	list, err := q.collectionsClient.List(ctx, &pb.ListCollectionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range list.GetCollections() {
		for _, name := range owned {
			if col.GetName() == name {
				t.Fatal("test collection name unexpectedly exists")
			}
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, name := range owned {
			// All cleanup targets have the unique prefix reserved above, never legacy names.
			if _, err := q.collectionsClient.Delete(cleanup, &pb.DeleteCollection{CollectionName: name}); err != nil {
				t.Errorf("cleanup owned collection %s: %v", name, err)
			}
		}
	})
	type legacyState struct {
		config *pb.CollectionConfig
		count  uint64
	}
	captureLegacy := func() map[string]legacyState {
		t.Helper()
		result := map[string]legacyState{}
		cols, err := q.collectionsClient.List(ctx, &pb.ListCollectionsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, col := range cols.GetCollections() {
			name := col.GetName()
			if name != "neobase_schema" && name != "neobase_messages" {
				continue
			}
			info, err := q.collectionsClient.Get(ctx, &pb.GetCollectionInfoRequest{CollectionName: name})
			if err != nil {
				t.Fatal(err)
			}
			exact := true
			count, err := q.pointsClient.Count(ctx, &pb.CountPoints{CollectionName: name, Exact: &exact})
			if err != nil {
				t.Fatal(err)
			}
			result[name] = legacyState{config: info.GetResult().GetConfig(), count: count.GetResult().GetCount()}
		}
		return result
	}
	before := captureLegacy()
	if err := q.EnsureSchemaCollection(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err := q.EnsureMessageCollection(ctx, 3); err != nil {
		t.Fatal(err)
	}
	// Repeat readiness against existing targets, exercising dense and index inspection.
	if err := q.EnsureSchemaCollection(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err := q.EnsureMessageCollection(ctx, 3); err != nil {
		t.Fatal(err)
	}
	for name, fields := range map[string]map[string]pb.PayloadSchemaType{
		prefix + "_schema":   {"chat_id": pb.PayloadSchemaType_Keyword, "type": pb.PayloadSchemaType_Keyword, "content": pb.PayloadSchemaType_Text, "table_name": pb.PayloadSchemaType_Text},
		prefix + "_messages": {"chat_id": pb.PayloadSchemaType_Keyword, "message_id": pb.PayloadSchemaType_Keyword, "role": pb.PayloadSchemaType_Keyword},
	} {
		info, err := q.collectionsClient.Get(ctx, &pb.GetCollectionInfoRequest{CollectionName: name})
		if err != nil {
			t.Fatal(err)
		}
		for field, kind := range fields {
			if info.GetResult().GetPayloadSchema()[field].GetDataType() != kind {
				t.Fatalf("%s.%s has wrong index type", name, field)
			}
		}
	}
	points := []VectorPoint{
		{ID: "inventory", Vector: []float32{1, 0, 0}, Payload: map[string]any{"chat_id": "fixture", "type": "schema", "content": "Inventory quantity records", "table_name": "inventory"}},
		{ID: "orders", Vector: []float32{1, 0, 0}, Payload: map[string]any{"chat_id": "fixture", "type": "schema", "content": "Order records", "table_name": "orders"}},
	}
	if err := q.Upsert(ctx, "neobase_schema", points); err != nil {
		t.Fatal(err)
	}
	results, err := q.HybridSearch(ctx, "neobase_schema", HybridSearchRequest{Vector: []float32{1, 0, 0}, TopK: 2, Filter: map[string]string{"chat_id": "fixture", "type": "schema"}, ExtraTextLegs: []TextSearchLeg{{Field: "table_name", Query: "inventory"}}})
	if err != nil || len(results) != 2 || results[0].Payload["table_name"] != "inventory" || results[0].Score <= results[1].Score {
		t.Fatalf("table_name fusion did not boost matching table: results=%+v err=%v", results, err)
	}
	for _, tc := range []struct {
		name     string
		size     uint64
		distance pb.Distance
		sparse   *pb.SparseVectorConfig
	}{{prefix + "_wrong_size", 768, pb.Distance_Cosine, nil}, {prefix + "_wrong_distance", 3, pb.Distance_Dot, nil}, {prefix + "_with_sparse", 3, pb.Distance_Cosine, &pb.SparseVectorConfig{Map: map[string]*pb.SparseVectorParams{"sparse": {}}}}} {
		if _, err := q.collectionsClient.Create(ctx, &pb.CreateCollection{CollectionName: tc.name, VectorsConfig: denseConfig(tc.size, tc.distance), SparseVectorsConfig: tc.sparse}); err != nil {
			t.Fatal(err)
		}
		bad, err := NewQdrantClient(QdrantConfig{Host: host, Port: port, SchemaCollection: tc.name, MessageCollection: prefix + "_messages"})
		if err != nil {
			t.Fatal(err)
		}
		err = bad.EnsureSchemaCollection(ctx, 3)
		bad.Close()
		if err == nil {
			t.Fatalf("accepted mismatched collection %s", tc.name)
		}
		info, err := q.collectionsClient.Get(ctx, &pb.GetCollectionInfoRequest{CollectionName: tc.name})
		if err != nil {
			t.Fatal(err)
		}
		params := info.GetResult().GetConfig().GetParams().GetVectorsConfig().GetParams()
		if params.GetSize() != tc.size || params.GetDistance() != tc.distance || len(info.GetResult().GetPayloadSchema()) != 0 || len(info.GetResult().GetConfig().GetParams().GetSparseVectorsConfig().GetMap()) != len(tc.sparse.GetMap()) {
			t.Fatal("mismatched collection was mutated")
		}
	}
	after := captureLegacy()
	if len(before) != len(after) {
		t.Fatal("legacy collection existence changed")
	}
	for name, old := range before {
		now, ok := after[name]
		if !ok || old.count != now.count || !proto.Equal(old.config, now.config) {
			t.Fatalf("legacy collection %s changed", name)
		}
	}
}
