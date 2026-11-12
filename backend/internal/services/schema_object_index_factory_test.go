package services

import (
	"context"
	"yanfachatbi/config"
	"yanfachatbi/internal/constants"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
	"testing"
)

type indexProviderFake struct{ indexEmbeddingFake }

func (e *indexProviderFake) Embed(ctx context.Context, text string) ([]float32, error) {
	return e.EmbedQuery(ctx, text)
}
func (e *indexProviderFake) GetProviderName() string { return "fake" }
func (e *indexProviderFake) GetModelName() string    { return "fake-dimension-3" }
func TestObjectIndexFactoryOptionalCapability(t *testing.T) {
	old := config.Env.EmbeddingObjectSchemaCollection
	t.Cleanup(func() { config.Env.EmbeddingObjectSchemaCollection = old })
	config.Env.EmbeddingObjectSchemaCollection = ""
	// grpc.NewClient is lazy: this constructor performs no RPC or DNS lookup.
	// No method that contacts Qdrant is invoked in this factory test.
	q, err := vectordb.NewQdrantClient(vectordb.QdrantConfig{Host: "unused.invalid", Port: "6334", SchemaCollection: "factory_schema", MessageCollection: "factory_messages"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	e := &indexProviderFake{indexEmbeddingFake: indexEmbeddingFake{dimension: 3}}
	service := NewVectorizationService(e, q)
	factory, ok := service.(ObjectIndexFactory)
	if !ok {
		t.Fatal("optional factory not exposed")
	}
	snapshot := indexSnapshot(t, 1)
	_, state, _, _ := indexFixture(t, snapshot)
	calls := 0
	fresh := func(context.Context) (schemaobjects.Snapshot, string, error) { calls++; return snapshot, "kb-1", nil }
	index, err := factory.NewObjectIndex(state, fresh)
	if err != nil || index == nil {
		t.Fatalf("factory: %v %v", index, err)
	}
	if calls != 0 || len(e.inputs) != 0 {
		t.Fatal("construction performed source/embedding work")
	}
	if _, err = factory.NewObjectIndex(state, nil); err == nil {
		t.Fatal("factory bypassed freshness requirement")
	}
	config.Env.EmbeddingObjectSchemaCollection = constants.MessageCollectionName
	if _, err = factory.NewObjectIndex(state, fresh); err == nil {
		t.Fatal("factory ignored object collection collision")
	}
}
func TestObjectIndexFactoryRejectsMissingObjectAdapter(t *testing.T) {
	s := &vectorizationService{embeddingProvider: &indexProviderFake{indexEmbeddingFake: indexEmbeddingFake{dimension: 3}}}
	if _, err := s.NewObjectIndex(&indexStateFake{}, func(context.Context) (schemaobjects.Snapshot, string, error) {
		return schemaobjects.Snapshot{}, "", nil
	}); err == nil {
		t.Fatal("missing object adapter accepted")
	}
}
