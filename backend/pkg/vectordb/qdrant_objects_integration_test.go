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
	q, err := NewQdrantClient(QdrantConfig{Host: host, Port: port, SchemaCollection: prefix + "_schema", MessageCollection: prefix + "_messages", SchemaBM25: true})
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
	t.Run("exact object keyword works without semantic similarity", func(t *testing.T) {
		otherChat := "a3d4e0cb-d6cb-4aa1-b975-98de115f098a"
		oldGeneration := "c2cd949c-0a55-4d7c-8a99-bccde839ad71"
		makePoint := func(pointID, field, value string) VectorPoint {
			data := map[string]interface{}{"content": "Sales view amount column", "table_name": "Sales"}
			for k, v := range filter {
				data[k] = v
			}
			data[field] = value
			return VectorPoint{ID: pointID, Vector: []float32{0, 0, 1}, Payload: data}
		}
		if err := store.Upsert(ctx, []VectorPoint{
			makePoint(otherChat, "chat_id", "different-chat"),
			makePoint(oldGeneration, "generation_id", "40f744b6-973e-469d-b12e-b4f554793a84"),
		}); err != nil {
			t.Fatal(err)
		}
		question := "在 Sales 中查 amount，按什么口径统计？"
		hits, err := store.HybridSearch(ctx, HybridSearchRequest{
			Vector: []float32{0, 1, 0}, Filter: filter, TopK: 5, ScoreThreshold: 0.35,
			TextField: "content", TextQuery: question,
			ExtraTextLegs: []TextSearchLeg{{Field: "table_name", Query: question}},
		})
		if err != nil || len(hits) != 1 || hits[0].ID != id {
			t.Fatalf("expected only the in-scope exact-name match; got %+v, %v", hits, err)
		}
	})
	t.Run("populated collection switches ranking and restores legacy mode", func(t *testing.T) {
		if err := testBM25Cutover(ctx, store, filter, id); err != nil {
			t.Fatal(err)
		}
	})
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

func testBM25Cutover(ctx context.Context, store *QdrantObjectStore, active map[string]string, originalID string) error {
	// Switch all writers together. The real index replacement must keep stored
	// points and restore the original text-only filter when disabled.
	setMode := func(on bool) error {
		store.q.schemaBM25 = on
		if err := store.Ensure(ctx, 3); err != nil {
			return err
		}
		if err := store.q.validateBM25Indexes(ctx, store.collection); (err == nil) != on {
			return fmt.Errorf("scoring configuration for mode %t: %v", on, err)
		}
		return nil
	}
	filter := objectTestFilter()
	filter["chat_id"] = "rank-fixture"
	point := func(id, content, name string, vector []float32) VectorPoint {
		p := map[string]interface{}{"content": content, "table_name": name}
		for k, v := range filter {
			p[k] = v
		}
		return VectorPoint{ID: id, Vector: vector, Payload: p}
	}
	const lexical = "db36c77a-42be-4c43-a6c8-77fa377ce40b"
	const dense = "da8442d7-b3ae-4d03-8a88-956b3f7b175d"
	if err := store.Upsert(ctx, []VectorPoint{
		point(lexical, "invoice invoice invoice", `"reporting"."invoice_lines"`, []float32{0, 0, 1}),
		point(dense, "invoice optional historical archival reconciliation notes", `"reporting"."payment"`, []float32{0.2, 0.98, 0}),
	}); err != nil {
		return err
	}
	if err := setMode(false); err != nil {
		return err
	}
	request := HybridSearchRequest{Vector: []float32{1, 0, 0}, Filter: filter, TopK: 2, ScoreThreshold: .35, TextField: "content", TextQuery: "invoice"}
	check := func(r HybridSearchRequest, first string, length int) error {
		hits, err := store.HybridSearch(ctx, r)
		if err != nil || len(hits) != length || hits[0].ID != first {
			return fmt.Errorf("expected %d results with %s first, got %+v, %v", length, first, hits, err)
		}
		return nil
	}
	if err := check(request, dense, 2); err != nil {
		return err
	}
	if err := setMode(true); err != nil {
		return err
	}
	if err := check(request, lexical, 2); err != nil {
		return err
	}
	request.TextField, request.TextQuery = "table_name", `帮我查询 reporting.invoice_lines 的数据`
	if err := check(request, lexical, 2); err != nil {
		return err
	}
	if err := setMode(false); err != nil {
		return err
	}
	return check(HybridSearchRequest{Vector: []float32{1, 0, 0}, Filter: active, TopK: 5, TextField: "content", TextQuery: "Sales"}, originalID, 1)
}
