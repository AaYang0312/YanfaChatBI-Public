package vectordb

import "context"

// ObjectStore is exclusively bound to a v2 physical collection.
type ObjectStore interface {
	Ensure(context.Context, int) error
	Upsert(context.Context, []VectorPoint) error
	Scroll(context.Context, []FilterCondition) ([]VectorPoint, error)
	Search(context.Context, SearchRequest) ([]SearchResult, error)
	HybridSearch(context.Context, HybridSearchRequest) ([]SearchResult, error)
	DeleteGeneration(context.Context, []FilterCondition) error
}
