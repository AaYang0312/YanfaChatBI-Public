//go:build object_qdrant_integration

package vectordb

import (
	"context"
	"fmt"
	pb "github.com/qdrant/go-client/qdrant"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Explicitly tagged acceptance test; a missing isolated runner is a failure,
// never a skip reported as successful integration coverage. No app config,
// credentials or production collection names are loaded.
func TestObjectStoreIsolatedQdrantAcceptance(t *testing.T) {
	address := os.Getenv("SOURCE_ACCESS_TEST_QDRANT_ADDR")
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("requires an explicitly provisioned isolated Qdrant at SOURCE_ACCESS_TEST_QDRANT_ADDR=127.0.0.1:port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("object_index_test_%d", time.Now().UnixNano())
	q, err := NewQdrantClient(QdrantConfig{Host: host, Port: port, SchemaCollection: prefix + "_schema", MessageCollection: prefix + "_messages"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	objectName, err := ResolveObjectCollectionName(prefix+"_schema", prefix+"_messages", "")
	if err != nil {
		t.Fatal(err)
	}
	owned := []string{prefix + "_schema", prefix + "_messages", objectName}
	list, err := q.collectionsClient.List(ctx, &pb.ListCollectionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, collection := range list.GetCollections() {
		for _, name := range owned {
			if collection.GetName() == name {
				t.Fatal("isolated collection unexpectedly exists")
			}
		}
	}
	t.Cleanup(func() {
		cleanup, closeCleanup := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCleanup()
		for _, name := range owned {
			if !strings.HasPrefix(name, prefix+"_") {
				t.Error("refusing non-owned cleanup")
				continue
			}
			if _, err := q.collectionsClient.Delete(cleanup, &pb.DeleteCollection{CollectionName: name}); err != nil {
				t.Errorf("isolated cleanup %s: %v", name, err)
			}
		}
	})
	if err = q.EnsureSchemaCollection(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err = q.EnsureMessageCollection(ctx, 3); err != nil {
		t.Fatal(err)
	}
	for _, name := range owned[:2] {
		if err = q.Upsert(ctx, name, []VectorPoint{{ID: "legacy-sentinel", Vector: []float32{1, 0, 0}, Payload: map[string]interface{}{"chat_id": "sentinel"}}}); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewQdrantObjectStore(q, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Ensure(ctx, 3); err != nil {
		t.Fatal(err)
	}
	filter := objectTestFilter()
	payload := map[string]interface{}{"content": "Sales view amount column", "table_name": "Sales", "object_type": "view", "part_index": "1"}
	for k, v := range filter {
		payload[k] = v
	}
	id := "ab64c93e-b3bb-5bca-ae77-884ca939be11"
	if err = store.Upsert(ctx, []VectorPoint{{ID: id, Vector: []float32{1, 0, 0}, Payload: payload}}); err != nil {
		t.Fatal(err)
	}
	points, err := store.Scroll(ctx, objectConditions(filter))
	if err != nil || len(points) != 1 || points[0].ID != id || len(points[0].Vector) != 3 || points[0].Payload["object_type"] != "view" {
		t.Fatalf("roundtrip %+v %v", points, err)
	}
	hits, err := store.HybridSearch(ctx, HybridSearchRequest{Vector: []float32{1, 0, 0}, Filter: filter, TopK: 5, TextField: "content", TextQuery: "Sales", ExtraTextLegs: []TextSearchLeg{{Field: "table_name", Query: "Sales"}}})
	if err != nil || len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("hybrid %+v %v", hits, err)
	}
	if err = store.DeleteGeneration(ctx, objectConditions(filter)); err != nil {
		t.Fatal(err)
	}
	points, err = store.Scroll(ctx, objectConditions(filter))
	if err != nil || len(points) != 0 {
		t.Fatalf("retired points %+v %v", points, err)
	}
	for _, name := range owned[:2] {
		count, err := q.Count(ctx, name, []FilterCondition{{Key: "chat_id", Value: "sentinel"}})
		if err != nil || count != 1 {
			t.Fatalf("legacy/message sentinel removed %s %d %v", name, count, err)
		}
	}
}
