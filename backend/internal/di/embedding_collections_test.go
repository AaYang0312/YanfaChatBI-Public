package di

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/qdrant/go-client/qdrant"
	"go.uber.org/dig"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"yanfachatbi/config"
	"yanfachatbi/internal/services"
	"yanfachatbi/pkg/embedding"
)

func TestCustomVectorizationStartupPropagatesReadinessFailure(t *testing.T) {
	// Loopback exists only inside the network-disabled test container. No model calls.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(_ context.Context, req any, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		switch req.(type) {
		case *pb.ListCollectionsRequest:
			return &pb.ListCollectionsResponse{Collections: []*pb.CollectionDescription{{Name: "schema_v2"}, {Name: "messages_v2"}}}, nil
		case *pb.GetCollectionInfoRequest:
			return &pb.GetCollectionInfoResponse{Result: &pb.CollectionInfo{Config: &pb.CollectionConfig{Params: &pb.CollectionParams{VectorsConfig: &pb.VectorsConfig{Config: &pb.VectorsConfig_Params{Params: &pb.VectorParams{Size: 768, Distance: pb.Distance_Cosine}}}}}}}, nil
		default:
			return nil, status.Error(codes.FailedPrecondition, "no mutation allowed for mismatched collection")
		}
	}))
	pb.RegisterCollectionsServer(server, &pb.UnimplementedCollectionsServer{})
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	provider, err := embedding.NewOpenAIProvider("synthetic-test-only", "text-embedding-3-small", "http://unused.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	env := config.Environment{QdrantHost: host, QdrantPort: port, EmbeddingSchemaCollection: "schema_v2", EmbeddingMessageCollection: "messages_v2"}
	container := dig.New()
	if err := container.Provide(func() (services.VectorizationService, error) { return initializeVectorization(env, provider) }); err != nil {
		t.Fatal(err)
	}
	invoked := false
	err = container.Invoke(func(services.VectorizationService) { invoked = true })
	if err == nil || !strings.Contains(err.Error(), "incompatible") || invoked {
		t.Fatalf("readiness failure did not stop DI startup: invoked=%v error=%v", invoked, err)
	}
}

func TestCustomVectorizationStartupRejectsMissingDependencies(t *testing.T) {
	provider, err := embedding.NewOpenAIProvider("synthetic-test-only", "text-embedding-3-small", "http://unused.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		env      config.Environment
		provider embedding.Provider
	}{
		{name: "missing host", env: config.Environment{EmbeddingSchemaCollection: "schema_v2", EmbeddingMessageCollection: "messages_v2"}, provider: provider},
		{name: "missing provider", env: config.Environment{QdrantHost: "unused.invalid", EmbeddingSchemaCollection: "schema_v2", EmbeddingMessageCollection: "messages_v2"}},
		{name: "partial pair", env: config.Environment{EmbeddingSchemaCollection: "schema_v2"}, provider: provider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if svc, err := initializeVectorization(tc.env, tc.provider); err == nil || svc != nil {
				t.Fatalf("missing dependency accepted: svc=%v err=%v", svc, err)
			}
		})
	}
}

func TestLegacyVectorizationStartupRetainsDisabledMode(t *testing.T) {
	if svc, err := initializeVectorization(config.Environment{}, nil); err != nil || svc != nil {
		t.Fatalf("legacy disabled startup changed: svc=%v err=%v", svc, err)
	}
}

func TestCustomVectorizationStartupAndCopyStayInPair(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	copies := make(chan *pb.UpsertPoints, 2)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(_ context.Context, req any, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		if named, ok := req.(interface{ GetCollectionName() string }); ok && named.GetCollectionName() != "schema_v2" && named.GetCollectionName() != "messages_v2" {
			return nil, status.Errorf(codes.PermissionDenied, "escaped custom pair: %s", named.GetCollectionName())
		}
		switch r := req.(type) {
		case *pb.ListCollectionsRequest:
			return &pb.ListCollectionsResponse{Collections: []*pb.CollectionDescription{{Name: "schema_v2"}, {Name: "messages_v2"}}}, nil
		case *pb.GetCollectionInfoRequest:
			schema := map[string]*pb.PayloadSchemaInfo{"chat_id": {DataType: pb.PayloadSchemaType_Keyword}, "type": {DataType: pb.PayloadSchemaType_Keyword}, "content": {DataType: pb.PayloadSchemaType_Text}, "table_name": {DataType: pb.PayloadSchemaType_Text}, "message_id": {DataType: pb.PayloadSchemaType_Keyword}, "role": {DataType: pb.PayloadSchemaType_Keyword}}
			return &pb.GetCollectionInfoResponse{Result: &pb.CollectionInfo{PayloadSchema: schema, Config: &pb.CollectionConfig{Params: &pb.CollectionParams{VectorsConfig: &pb.VectorsConfig{Config: &pb.VectorsConfig_Params{Params: &pb.VectorParams{Size: 1536, Distance: pb.Distance_Cosine}}}}}}}, nil
		case *pb.CreateFieldIndexCollection:
			return &pb.PointsOperationResponse{Result: &pb.UpdateResult{Status: pb.UpdateStatus_Completed}}, nil
		case *pb.ScrollPoints:
			vector := make([]float32, 1536)
			vector[0] = 1
			payload := map[string]*pb.Value{"chat_id": {Kind: &pb.Value_StringValue{StringValue: "source"}}, "type": {Kind: &pb.Value_StringValue{StringValue: "schema"}}, "table_name": {Kind: &pb.Value_StringValue{StringValue: "inventory"}}, "message_id": {Kind: &pb.Value_StringValue{StringValue: "old-message"}}}
			return &pb.ScrollResponse{Result: []*pb.RetrievedPoint{{Id: pb.NewIDUUID("0d2ae189-49fa-452c-bf81-0dc9a76e8a31"), Payload: payload, Vectors: &pb.VectorsOutput{VectorsOptions: &pb.VectorsOutput_Vector{Vector: &pb.VectorOutput{Vector: &pb.VectorOutput_Dense{Dense: &pb.DenseVector{Data: vector}}}}}}}}, nil
		case *pb.UpsertPoints:
			copies <- r
			return &pb.PointsOperationResponse{Result: &pb.UpdateResult{Status: pb.UpdateStatus_Completed}}, nil
		default:
			return nil, status.Errorf(codes.Unimplemented, "unexpected request %T", r)
		}
	}))
	pb.RegisterCollectionsServer(server, &pb.UnimplementedCollectionsServer{})
	pb.RegisterPointsServer(server, &pb.UnimplementedPointsServer{})
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	provider, err := embedding.NewOpenAIProvider("synthetic-test-only", "text-embedding-3-small", "http://unused.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	service, err := initializeVectorization(config.Environment{QdrantHost: host, QdrantPort: port, EmbeddingSchemaCollection: "schema_v2", EmbeddingMessageCollection: "messages_v2"}, provider)
	if err != nil || service == nil {
		t.Fatalf("compatible startup failed: %v", err)
	}
	if err := service.CopyVectorsForChat(context.Background(), "source", "target", true, map[string]string{"old-message": "new-message"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"schema_v2", "messages_v2"} {
		select {
		case request := <-copies:
			if request.GetCollectionName() != name || len(request.GetPoints()) != 1 {
				t.Fatalf("copy targeted wrong collection or lost points: %+v", request)
			}
			point := request.GetPoints()[0]
			if point.GetPayload()["chat_id"].GetStringValue() != "target" || len(point.GetVectors().GetVector().GetData()) != 1536 {
				t.Fatal("copied payload/vector lost")
			}
			if name == "messages_v2" && point.GetPayload()["message_id"].GetStringValue() != "new-message" {
				t.Fatal("message ID was not remapped")
			}
		default:
			t.Fatalf("no copied points in %s", name)
		}
	}
}

func TestLegacyVectorizationStartupDoesNotBlockOnReadinessFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan struct{}, 1)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(_ context.Context, _ any, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		observed <- struct{}{}
		return nil, status.Error(codes.Unavailable, "synthetic readiness failure")
	}))
	pb.RegisterCollectionsServer(server, &pb.UnimplementedCollectionsServer{})
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	provider, err := embedding.NewOpenAIProvider("synthetic-test-only", "text-embedding-3-small", "http://unused.invalid/v1")
	if err != nil {
		t.Fatal(err)
	}
	service, err := initializeVectorization(config.Environment{QdrantHost: host, QdrantPort: port}, provider)
	if err != nil || service == nil {
		t.Fatalf("legacy startup unexpectedly failed: %v", err)
	}
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy asynchronous readiness was not attempted")
	}
}
