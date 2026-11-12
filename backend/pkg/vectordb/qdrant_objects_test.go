package vectordb

import (
	"context"
	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"net"
	"reflect"
	"testing"
)

func TestObjectCollectionEnsureIsolationAndDimensions(t *testing.T) {
	f := &collectionRPCFixture{}
	q := newCollectionRPCClient(t, "custom_schema", "custom_messages", f)
	store, err := NewQdrantObjectStore(q, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Ensure(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if len(f.existing) != 1 || f.existing["custom_schema_objects_v2"] == nil {
		t.Fatalf("touched legacy/message %v", f.existing)
	}
	if f.existing["custom_schema_objects_v2"].GetParams().GetSize() != 3 {
		t.Fatal("dimension changed")
	}
	for _, field := range []string{"object_type", "object_id", "generation_id", "kb_revision", "owner_id", "source_binding_revision"} {
		if f.schemas["custom_schema_objects_v2"][field].GetDataType() != pb.PayloadSchemaType_Keyword {
			t.Fatalf("missing keyword %s", field)
		}
	}
	for _, field := range []string{"table_name", "content"} {
		if f.schemas["custom_schema_objects_v2"][field].GetDataType() != pb.PayloadSchemaType_Text {
			t.Fatalf("missing text %s", field)
		}
	}
	// A bound object adapter must not widen the existing pair client's allowlist.
	if _, err = q.resolveCollection("custom_schema_objects_v2"); err == nil {
		t.Fatal("legacy custom pair allowlist widened")
	}
}
func TestObjectCollectionEnsureRejectsIncompatibleServer(t *testing.T) {
	for _, mode := range []string{"dimension", "distance", "index-error", "index-type", "get-error", "zero-dimension"} {
		t.Run(mode, func(t *testing.T) {
			f := &collectionRPCFixture{existing: map[string]*pb.VectorsConfig{"neobase_schema_objects_v2": denseConfig(3, pb.Distance_Cosine)}}
			// Resolve the actual default instead of relying on the constant's spelling.
			name, err := ResolveObjectCollectionName("", "", "")
			if err != nil {
				t.Fatal(err)
			}
			f.existing = map[string]*pb.VectorsConfig{name: denseConfig(3, pb.Distance_Cosine)}
			dimension := 3
			switch mode {
			case "dimension":
				f.existing[name] = denseConfig(8, pb.Distance_Cosine)
			case "distance":
				f.existing[name] = denseConfig(3, pb.Distance_Dot)
			case "index-error":
				f.failIndex = "object_type"
			case "index-type":
				f.wrongIndexType = true
			case "get-error":
				f.getError = true
			case "zero-dimension":
				dimension = 0
			}
			q := newCollectionRPCClient(t, "", "", f)
			store, err := NewQdrantObjectStore(q, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Ensure(context.Background(), dimension); err == nil {
				t.Fatal("incompatible server accepted")
			}
		})
	}
}

type objectRPCFixture struct {
	collection collectionRPCFixture
	points     []*pb.PointStruct
	requests   []any
}

func (f *objectRPCFixture) intercept(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	f.requests = append(f.requests, req)
	switch r := req.(type) {
	case *pb.UpsertPoints:
		f.points = append(f.points, r.GetPoints()...)
		return &pb.PointsOperationResponse{Result: &pb.UpdateResult{Status: pb.UpdateStatus_Completed}}, nil
	case *pb.ScrollPoints:
		start := 0
		if r.GetOffset() != nil {
			start = 1
		}
		out := &pb.ScrollResponse{}
		if start < len(f.points) {
			p := f.points[start]
			out.Result = []*pb.RetrievedPoint{{Id: p.Id, Payload: p.Payload, Vectors: &pb.VectorsOutput{VectorsOptions: &pb.VectorsOutput_Vector{Vector: &pb.VectorOutput{Vector: &pb.VectorOutput_Dense{Dense: &pb.DenseVector{Data: p.GetVectors().GetVector().GetData()}}}}}}}
			if start+1 < len(f.points) {
				out.NextPageOffset = p.Id
			}
		}
		return out, nil
	case *pb.QueryPoints:
		return &pb.QueryResponse{}, nil
	case *pb.SearchPoints:
		return &pb.SearchResponse{}, nil
	case *pb.DeletePoints:
		return &pb.PointsOperationResponse{Result: &pb.UpdateResult{Status: pb.UpdateStatus_Completed}}, nil
	default:
		return f.collection.intercept(ctx, req, info, handler)
	}
}
func objectRPCClient(t *testing.T, f *objectRPCFixture) *QdrantClient {
	t.Helper()
	f.collection.existing = map[string]*pb.VectorsConfig{}
	l := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(f.intercept))
	pb.RegisterCollectionsServer(server, &pb.UnimplementedCollectionsServer{})
	pb.RegisterPointsServer(server, &pb.UnimplementedPointsServer{})
	go server.Serve(l)
	conn, err := grpc.NewClient("passthrough:///objects", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); server.Stop(); l.Close() })
	return &QdrantClient{conn: conn, pointsClient: pb.NewPointsClient(conn), collectionsClient: pb.NewCollectionsClient(conn), collectionNames: CollectionNames{Schema: "schema_custom", Messages: "message_custom"}}
}
func objectTestFilter() map[string]string {
	return map[string]string{"owner_id": "owner", "chat_id": "chat", "source_key": "source", "source_binding_revision": "1", "connection_revision": "2", "selection_revision": "3", "index_format_version": "2", "generation_id": "9cd9632a-8771-4c83-b234-8da0572c4111"}
}
func objectConditions(m map[string]string) []FilterCondition {
	var out []FilterCondition
	for k, v := range m {
		out = append(out, FilterCondition{Key: k, Value: v})
	}
	return out
}
func assertObjectWireFilter(t *testing.T, f *pb.Filter) {
	t.Helper()
	actual := map[string]string{}
	for _, c := range f.GetMust() {
		field := c.GetField()
		if _, ok := field.GetMatch().GetMatchValue().(*pb.Match_Keyword); ok {
			actual[field.GetKey()] = field.GetMatch().GetKeyword()
		}
	}
	if !reflect.DeepEqual(actual, objectTestFilter()) {
		t.Fatalf("missing visibility fence: %v", actual)
	}
}
func TestObjectStoreWireRoundtripAndHybridGeneration(t *testing.T) {
	f := &objectRPCFixture{}
	q := objectRPCClient(t, f)
	s, err := NewQdrantObjectStore(q, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.Ensure(ctx, 3); err != nil {
		t.Fatal(err)
	}
	points := []VectorPoint{
		{ID: "ab64c93e-b3bb-5bca-ae77-884ca939be11", Vector: []float32{1, 2, 3}, Payload: map[string]interface{}{"object_type": "view", "part_index": "1"}},
		{ID: "20c225bc-dc63-571c-a073-e615fa55d75b", Vector: []float32{3, 2, 1}, Payload: map[string]interface{}{"object_type": "table", "part_index": "2"}},
	}
	if err = s.Upsert(ctx, points); err != nil {
		t.Fatal(err)
	}
	got, err := s.Scroll(ctx, objectConditions(objectTestFilter()))
	if err != nil || !reflect.DeepEqual(got, points) {
		t.Fatalf("point IDs/payload/vector roundtrip: %+v %v", got, err)
	}
	filter := objectTestFilter()
	_, err = s.HybridSearch(ctx, HybridSearchRequest{Vector: []float32{1, 2, 3}, Filter: filter, TopK: 4, TextField: "content", TextQuery: "Sales", ExtraTextLegs: []TextSearchLeg{{Field: "table_name", Query: "Sales"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Search(ctx, SearchRequest{Vector: []float32{1, 2, 3}, Filter: filter, TopK: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteGeneration(ctx, objectConditions(filter)); err != nil {
		t.Fatal(err)
	}
	querySeen, searchSeen, deleteSeen, scrollCount := false, false, false, 0
	for _, raw := range f.requests {
		switch r := raw.(type) {
		case *pb.QueryPoints:
			querySeen = true
			if r.GetCollectionName() != "schema_custom_objects_v2" || len(r.Prefetch) != 3 || !proto.Equal(r.Query, pb.NewQueryFusion(pb.Fusion_RRF)) {
				t.Fatalf("lost RRF/name/text: %+v", r)
			}
			for _, leg := range r.Prefetch {
				assertObjectWireFilter(t, leg.Filter)
			}
		case *pb.SearchPoints:
			searchSeen = true
			assertObjectWireFilter(t, r.Filter)
		case *pb.ScrollPoints:
			scrollCount++
			assertObjectWireFilter(t, r.Filter)
			if !r.GetWithVectors().GetEnable() {
				t.Fatal("verification did not request actual vectors")
			}
		case *pb.DeletePoints:
			deleteSeen = true
			assertObjectWireFilter(t, r.GetPoints().GetFilter())
			if r.CollectionName != "schema_custom_objects_v2" {
				t.Fatal("deleted wrong collection")
			}
		}
	}
	if !querySeen || !searchSeen || !deleteSeen || scrollCount != 2 {
		t.Fatalf("missing RPCs %v %v %v %d", querySeen, searchSeen, deleteSeen, scrollCount)
	}
	if err = s.DeleteGeneration(ctx, []FilterCondition{{Key: "chat_id", Value: "chat"}}); err == nil {
		t.Fatal("broad deletion accepted")
	}
	if err = s.Upsert(ctx, []VectorPoint{{ID: "legacy_logical_id"}}); err == nil {
		t.Fatal("non UUID point accepted")
	}
}
