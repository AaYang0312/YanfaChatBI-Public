package services

import (
	"yanfachatbi/config"
	"yanfachatbi/pkg/vectordb"
)

// ObjectIndexFactory is an optional capability; the legacy public service
// interface and existing fake implementations remain unchanged.
type ObjectIndexFactory interface {
	NewObjectIndex(ObjectIndexStateStore, FreshObjectSnapshot) (ObjectIndex, error)
}

// NewObjectIndex binds this operation's fresh callback to the existing provider
// and Qdrant connection. Construction does no source/embedding/vector I/O. The
// controller must hold its source lease through Build/Search and bind a new
// callback for each chat operation; never share a chat-capturing singleton.
func (v *vectorizationService) NewObjectIndex(state ObjectIndexStateStore, fresh FreshObjectSnapshot) (ObjectIndex, error) {
	if v == nil || v.embeddingProvider == nil || state == nil || fresh == nil {
		return nil, ErrObjectIndexInvalid
	}
	q, ok := v.vectorClient.(*vectordb.QdrantClient)
	if !ok || q == nil {
		return nil, ErrObjectIndexUnavailable
	}
	vectors, err := vectordb.NewQdrantObjectStore(q, config.Env.EmbeddingObjectSchemaCollection)
	if err != nil {
		return nil, err
	}
	return NewObjectIndex(state, vectors, v.embeddingProvider, fresh)
}

var _ ObjectIndexFactory = (*vectorizationService)(nil)
