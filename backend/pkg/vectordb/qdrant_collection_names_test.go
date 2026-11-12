package vectordb

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"sync"
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type collectionRPCFixture struct {
	sparse         *pb.SparseVectorConfig
	schemas        map[string]map[string]*pb.PayloadSchemaInfo
	wrongIndexType bool
	mu             sync.Mutex
	requests       []any
	existing       map[string]*pb.VectorsConfig
	failIndex      string
	getError       bool
}

func denseConfig(size uint64, distance pb.Distance) *pb.VectorsConfig {
	return &pb.VectorsConfig{Config: &pb.VectorsConfig_Params{Params: &pb.VectorParams{Size: size, Distance: distance}}}
}

func (f *collectionRPCFixture) intercept(_ context.Context, request any, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	switch r := request.(type) {
	case *pb.ListCollectionsRequest:
		var cols []*pb.CollectionDescription
		for name := range f.existing {
			cols = append(cols, &pb.CollectionDescription{Name: name})
		}
		return &pb.ListCollectionsResponse{Collections: cols}, nil
	case *pb.GetCollectionInfoRequest:
		if f.getError {
			return nil, status.Error(codes.Unavailable, "fixture unavailable")
		}
		return &pb.GetCollectionInfoResponse{Result: &pb.CollectionInfo{PayloadSchema: f.schemas[r.GetCollectionName()], Config: &pb.CollectionConfig{Params: &pb.CollectionParams{VectorsConfig: f.existing[r.GetCollectionName()], SparseVectorsConfig: f.sparse}}}}, nil
	case *pb.CreateCollection:
		f.existing[r.GetCollectionName()] = r.GetVectorsConfig()
		return &pb.CollectionOperationResponse{Result: true}, nil
	case *pb.CreateFieldIndexCollection:
		if f.schemas == nil {
			f.schemas = map[string]map[string]*pb.PayloadSchemaInfo{}
		}
		if f.schemas[r.GetCollectionName()] == nil {
			f.schemas[r.GetCollectionName()] = map[string]*pb.PayloadSchemaInfo{}
		}
		kind := pb.PayloadSchemaType_Keyword
		if r.GetFieldType() == pb.FieldType_FieldTypeText {
			kind = pb.PayloadSchemaType_Text
		}
		if f.wrongIndexType {
			kind = pb.PayloadSchemaType_Integer
		}
		f.schemas[r.GetCollectionName()][r.GetFieldName()] = &pb.PayloadSchemaInfo{DataType: kind, Params: r.GetFieldIndexParams()}
		if r.GetFieldName() == f.failIndex {
			return nil, status.Error(codes.FailedPrecondition, "fixture index failed")
		}
		return &pb.PointsOperationResponse{Result: &pb.UpdateResult{Status: pb.UpdateStatus_Completed}}, nil
	case *pb.UpsertPoints, *pb.DeletePoints:
		return &pb.PointsOperationResponse{Result: &pb.UpdateResult{Status: pb.UpdateStatus_Completed}}, nil
	case *pb.SearchPoints:
		return &pb.SearchResponse{}, nil
	case *pb.QueryPoints:
		return &pb.QueryResponse{}, nil
	case *pb.CountPoints:
		return &pb.CountResponse{Result: &pb.CountResult{Count: 7}}, nil
	case *pb.ScrollPoints:
		return &pb.ScrollResponse{Result: []*pb.RetrievedPoint{{Id: pb.NewIDUUID("0d2ae189-49fa-452c-bf81-0dc9a76e8a31"), Payload: map[string]*pb.Value{"chat_id": {Kind: &pb.Value_StringValue{StringValue: "source"}}}, Vectors: &pb.VectorsOutput{VectorsOptions: &pb.VectorsOutput_Vector{Vector: &pb.VectorOutput{Vector: &pb.VectorOutput_Dense{Dense: &pb.DenseVector{Data: []float32{1, 2, 3}}}}}}}}}, nil
	default:
		return nil, status.Errorf(codes.Unimplemented, "unexpected fixture request %T", r)
	}
}

func newCollectionRPCClient(t *testing.T, schema, messages string, f *collectionRPCFixture) *QdrantClient {
	t.Helper()
	if f.existing == nil {
		f.existing = map[string]*pb.VectorsConfig{}
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(f.intercept))
	pb.RegisterCollectionsServer(server, &pb.UnimplementedCollectionsServer{})
	pb.RegisterPointsServer(server, &pb.UnimplementedPointsServer{})
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///fixture", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	q, err := NewQdrantClient(QdrantConfig{Host: "unused.invalid", Port: "6334", SchemaCollection: schema, MessageCollection: messages})
	if err != nil {
		t.Fatal(err)
	}
	q.conn.Close()
	q.conn = conn
	q.pointsClient = pb.NewPointsClient(conn)
	q.collectionsClient = pb.NewCollectionsClient(conn)
	t.Cleanup(func() { q.Close(); server.Stop(); listener.Close() })
	return q
}

func (f *collectionRPCFixture) snapshot() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.requests...)
}

func TestCustomPairRoutesEveryOperation(t *testing.T) {
	f := &collectionRPCFixture{}
	q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
	ctx := context.Background()
	for _, pair := range []struct{ logical, physical string }{{"neobase_schema", "schema_v2"}, {"neobase_messages", "messages_v2"}} {
		for _, target := range []string{pair.logical, pair.physical} {
			start := len(f.snapshot())
			filters := []FilterCondition{{Key: "chat_id", Value: "source"}}
			if err := q.EnsureCollection(ctx, target, 3); err != nil {
				t.Fatal(err)
			}
			if err := q.Upsert(ctx, target, []VectorPoint{{ID: "source", Vector: []float32{1, 2, 3}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := q.Search(ctx, target, SearchRequest{Vector: []float32{1, 2, 3}}); err != nil {
				t.Fatal(err)
			}
			if _, err := q.HybridSearch(ctx, target, HybridSearchRequest{Vector: []float32{1, 2, 3}, TextField: "content", TextQuery: "table"}); err != nil {
				t.Fatal(err)
			}
			if count, err := q.Count(ctx, target, filters); err != nil || count != 7 {
				t.Fatalf("count=%d err=%v", count, err)
			}
			if err := q.Delete(ctx, target, []PointID{"source"}); err != nil {
				t.Fatal(err)
			}
			if err := q.DeleteByFilter(ctx, target, filters); err != nil {
				t.Fatal(err)
			}
			copied, err := q.ScrollByFilter(ctx, target, filters, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(copied) != 1 || !reflect.DeepEqual(copied[0].Vector, []float32{1, 2, 3}) {
				t.Fatalf("copy source=%+v", copied)
			}
			copied[0].ID = "target"
			if err := q.Upsert(ctx, target, copied); err != nil {
				t.Fatal(err)
			}
			for _, r := range f.snapshot()[start:] {
				if named, ok := r.(interface{ GetCollectionName() string }); ok && named.GetCollectionName() != pair.physical {
					t.Errorf("%T targeted %q, want %q", r, named.GetCollectionName(), pair.physical)
				}
			}
		}
	}
}

func TestCustomPairRejectsUnknownTargetsWithoutRPC(t *testing.T) {
	f := &collectionRPCFixture{}
	q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
	ctx := context.Background()
	for _, target := range []string{"other", "", "schema_v3"} {
		checks := []func() error{
			func() error { return q.EnsureCollection(ctx, target, 3) },
			func() error { return q.Upsert(ctx, target, nil) },
			func() error { return q.Upsert(ctx, target, []VectorPoint{{ID: "x"}}) },
			func() error { _, e := q.Search(ctx, target, SearchRequest{}); return e },
			func() error { _, e := q.HybridSearch(ctx, target, HybridSearchRequest{}); return e },
			func() error { _, e := q.Count(ctx, target, nil); return e },
			func() error { return q.Delete(ctx, target, nil) },
			func() error { return q.Delete(ctx, target, []PointID{"x"}) },
			func() error {
				return q.DeleteByFilter(ctx, target, []FilterCondition{{Key: "chat_id", Value: "source"}})
			},
			func() error {
				_, e := q.ScrollByFilter(ctx, target, []FilterCondition{{Key: "chat_id", Value: "source"}}, true)
				return e
			},
			func() error { return q.ensureTextIndex(ctx, target, "content") },
		}
		for i, check := range checks {
			if err := check(); err == nil {
				t.Errorf("operation %d accepted %q", i, target)
			}
		}
	}
	if got := f.snapshot(); len(got) != 0 {
		t.Fatalf("unknown targets issued %d RPCs", len(got))
	}
}

func TestCustomPairEnsuresRequiredIndexes(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			f := &collectionRPCFixture{existing: map[string]*pb.VectorsConfig{"neobase_schema": denseConfig(768, pb.Distance_Cosine), "neobase_messages": denseConfig(768, pb.Distance_Cosine)}}
			if existing {
				f.existing["schema_v2"] = denseConfig(3, pb.Distance_Cosine)
				f.existing["messages_v2"] = denseConfig(3, pb.Distance_Cosine)
			}
			q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
			if err := q.EnsureSchemaCollection(context.Background(), 3); err != nil {
				t.Fatal(err)
			}
			if err := q.EnsureMessageCollection(context.Background(), 3); err != nil {
				t.Fatal(err)
			}
			var indexes []string
			created := 0
			checked := 0
			for _, r := range f.snapshot() {
				if named, ok := r.(interface{ GetCollectionName() string }); ok && named.GetCollectionName() != "schema_v2" && named.GetCollectionName() != "messages_v2" {
					t.Fatalf("touched old or unknown collection: %T %s", r, named.GetCollectionName())
				}
				switch v := r.(type) {
				case *pb.CreateCollection:
					created++
					if v.GetVectorsConfig().GetParams().GetSize() != 3 || v.GetVectorsConfig().GetParams().GetDistance() != pb.Distance_Cosine {
						t.Fatalf("wrong created config: %+v", v)
					}
				case *pb.GetCollectionInfoRequest:
					checked++
				case *pb.CreateFieldIndexCollection:
					indexes = append(indexes, fmt.Sprintf("%s:%s:%s", v.GetCollectionName(), v.GetFieldName(), v.GetFieldType()))
					if !v.GetWait() {
						t.Fatal("indexes must be awaited")
					}
					if v.GetFieldType() == pb.FieldType_FieldTypeText {
						p := v.GetFieldIndexParams().GetTextIndexParams()
						if p.GetTokenizer() != pb.TokenizerType_Word || !p.GetLowercase() {
							t.Fatal("text index options lost")
						}
					}
				}
			}
			want := []string{"schema_v2:chat_id:FieldTypeKeyword", "schema_v2:type:FieldTypeKeyword", "schema_v2:content:FieldTypeText", "schema_v2:table_name:FieldTypeText", "messages_v2:chat_id:FieldTypeKeyword", "messages_v2:message_id:FieldTypeKeyword", "messages_v2:role:FieldTypeKeyword"}
			if !reflect.DeepEqual(indexes, want) {
				t.Fatalf("indexes=%v want=%v", indexes, want)
			}
			if existing && (created != 0 || checked != 4) {
				t.Fatalf("existing collections created=%d validated=%d", created, checked)
			}
			if !existing && created != 2 {
				t.Fatalf("created %d collections", created)
			}
		})
	}
}

func TestCustomPairRejectsIncompatibleExistingCollection(t *testing.T) {
	multi := denseConfig(3, pb.Distance_Cosine)
	multi.GetParams().MultivectorConfig = &pb.MultiVectorConfig{}
	for name, vectors := range map[string]*pb.VectorsConfig{
		"size": denseConfig(768, pb.Distance_Cosine), "distance": denseConfig(3, pb.Distance_Dot), "missing": nil, "named": {Config: &pb.VectorsConfig_ParamsMap{ParamsMap: &pb.VectorParamsMap{Map: map[string]*pb.VectorParams{"dense": {Size: 3, Distance: pb.Distance_Cosine}}}}}, "multivector": multi,
	} {
		t.Run(name, func(t *testing.T) {
			f := &collectionRPCFixture{existing: map[string]*pb.VectorsConfig{"schema_v2": vectors}}
			q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
			if err := q.EnsureSchemaCollection(context.Background(), 3); err == nil {
				t.Fatal("incompatible collection was accepted")
			}
			for _, r := range f.snapshot() {
				switch r.(type) {
				case *pb.ListCollectionsRequest, *pb.GetCollectionInfoRequest:
				default:
					t.Fatalf("unsafe mutation after failed validation: %T", r)
				}
			}
		})
	}
}

func TestCustomPairFailsClosedOnInspectionAndIndexErrors(t *testing.T) {
	for _, field := range []string{"chat_id", "type", "content", "table_name", "message_id", "role", "get"} {
		t.Run(field, func(t *testing.T) {
			f := &collectionRPCFixture{existing: map[string]*pb.VectorsConfig{"schema_v2": denseConfig(3, pb.Distance_Cosine), "messages_v2": denseConfig(3, pb.Distance_Cosine)}, failIndex: field, getError: field == "get"}
			q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
			var err error
			if field == "message_id" || field == "role" {
				err = q.EnsureMessageCollection(context.Background(), 3)
			} else {
				err = q.EnsureSchemaCollection(context.Background(), 3)
			}
			if err == nil {
				t.Fatal("readiness silently ignored a required operation failure")
			}
		})
	}
}

func TestLegacyClientRetainsGenericAndWarningOnlyBehavior(t *testing.T) {
	f := &collectionRPCFixture{existing: map[string]*pb.VectorsConfig{"neobase_schema": denseConfig(768, pb.Distance_Dot)}, failIndex: "chat_id"}
	q := newCollectionRPCClient(t, "", "", f)
	if err := q.EnsureSchemaCollection(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if err := q.EnsureCollection(context.Background(), "generic_collection", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Count(context.Background(), "generic_collection", nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.snapshot() {
		if _, ok := r.(*pb.GetCollectionInfoRequest); ok {
			t.Fatal("legacy readiness gained compatibility validation")
		}
	}
}

func TestCustomPairVerifiesResultingIndexTypes(t *testing.T) {
	for _, schema := range []bool{true, false} {
		f := &collectionRPCFixture{wrongIndexType: true}
		q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
		var err error
		if schema {
			err = q.EnsureSchemaCollection(context.Background(), 3)
		} else {
			err = q.EnsureMessageCollection(context.Background(), 3)
		}
		if err == nil {
			t.Fatal("wrong payload index types counted as ready")
		}
	}
}

func TestCustomPairRejectsNonPositiveDimensionBeforeRPC(t *testing.T) {
	f := &collectionRPCFixture{}
	q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
	for _, dimension := range []int{0, -1} {
		if err := q.EnsureCollection(context.Background(), "neobase_schema", dimension); err == nil {
			t.Fatal("invalid dimension accepted")
		}
	}
	if len(f.snapshot()) != 0 {
		t.Fatal("invalid dimension issued RPC")
	}
}

func TestCustomPairRejectsSparseLayoutBeforeMutation(t *testing.T) {
	for _, schema := range []bool{true, false} {
		f := &collectionRPCFixture{existing: map[string]*pb.VectorsConfig{"schema_v2": denseConfig(3, pb.Distance_Cosine), "messages_v2": denseConfig(3, pb.Distance_Cosine)}, sparse: &pb.SparseVectorConfig{Map: map[string]*pb.SparseVectorParams{"sparse": {}}}}
		q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
		var err error
		if schema {
			err = q.EnsureSchemaCollection(context.Background(), 3)
		} else {
			err = q.EnsureMessageCollection(context.Background(), 3)
		}
		if err == nil {
			t.Fatal("dense-plus-sparse layout counted as ready")
		}
		for _, request := range f.snapshot() {
			switch request.(type) {
			case *pb.ListCollectionsRequest, *pb.GetCollectionInfoRequest:
			default:
				t.Fatalf("sparse layout mutated by %T", request)
			}
		}
	}
}

func TestCustomPairAcceptsEmptySparseConfiguration(t *testing.T) {
	f := &collectionRPCFixture{existing: map[string]*pb.VectorsConfig{"schema_v2": denseConfig(3, pb.Distance_Cosine)}, sparse: &pb.SparseVectorConfig{}}
	q := newCollectionRPCClient(t, "schema_v2", "messages_v2", f)
	if err := q.EnsureSchemaCollection(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
}
