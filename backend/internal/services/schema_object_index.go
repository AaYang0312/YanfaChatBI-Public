package services

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
)

type ObjectIndex interface {
	Build(context.Context, schemaobjects.Snapshot, string, []SchemaChunk) error
	Search(context.Context, schemaobjects.Snapshot, string, string, int) ([]vectordb.SearchResult, error)
	Status(context.Context, schemaobjects.Scope) (models.Manifest, error)
}

// ObjectIndexStateStore matches repositories.ObjectStateRepository without
// importing its platform-specific source-access implementation.
type ObjectIndexStateStore interface {
	Read(context.Context, schemaobjects.Scope) (*models.State, error)
	CompareAndSwap(context.Context, schemaobjects.Scope, int64, models.State) error
}
type ObjectIndexEmbedding interface {
	EmbedBatch(context.Context, []string) ([][]float32, error)
	EmbedQuery(context.Context, string) ([]float32, error)
	GetDimension() int
}

// FreshObjectSnapshot must retain the caller's source-access context/lease,
// recollect the current selected/readable catalog, and load effective KB revision.
// It may CAS the registry; Build consequently reads State AFTER this callback.
type FreshObjectSnapshot func(context.Context) (schemaobjects.Snapshot, string, error)

type objectIndex struct {
	state     ObjectIndexStateStore
	vectors   vectordb.ObjectStore
	embedding ObjectIndexEmbedding
	fresh     FreshObjectSnapshot
}

var ErrObjectIndexInvalid = errors.New("invalid object index input")
var ErrObjectIndexConflict = errors.New("object index superseded")
var ErrObjectIndexUnavailable = errors.New("object index unavailable")

func NewObjectIndex(state ObjectIndexStateStore, vectors vectordb.ObjectStore, embedding ObjectIndexEmbedding, fresh FreshObjectSnapshot) (ObjectIndex, error) {
	if state == nil || vectors == nil || embedding == nil || fresh == nil || embedding.GetDimension() <= 0 {
		return nil, ErrObjectIndexInvalid
	}
	return &objectIndex{state: state, vectors: vectors, embedding: embedding, fresh: fresh}, nil
}
func (i *objectIndex) Status(ctx context.Context, scope schemaobjects.Scope) (models.Manifest, error) {
	state, err := i.state.Read(ctx, scope)
	if err != nil {
		return models.Manifest{}, err
	}
	if state == nil {
		return models.Manifest{}, ErrObjectIndexUnavailable
	}
	if state.Manifest.Scope != scope {
		return models.Manifest{FormatVersion: 2, Status: "stale", Scope: scope}, nil
	}
	return state.Manifest, nil
}
func manifestMatches(m models.Manifest, s schemaobjects.Snapshot, kb string) bool {
	return m.FormatVersion == 2 && m.Scope == s.Scope && m.SchemaFingerprint == s.Fingerprint && m.KBRevision == kb
}
func registryMatches(state *models.State, s schemaobjects.Snapshot) bool {
	if state == nil || state.Scope != s.Scope {
		return false
	}
	for _, o := range s.Objects {
		entry, ok := state.Registry[o.Metadata.ObjectID]
		if !ok || !entry.Present || entry.CatalogOID != o.Metadata.CatalogOID || entry.Version != o.Metadata.InstanceVersion {
			return false
		}
	}
	return true
}
func ownsBuild(state *models.State, s schemaobjects.Snapshot, kb, generation string) bool {
	return registryMatches(state, s) && manifestMatches(state.Manifest, s, kb) &&
		state.Manifest.Status == "building" && state.Manifest.BuildingGeneration == generation
}

func (i *objectIndex) Build(ctx context.Context, s schemaobjects.Snapshot, kb string, chunks []SchemaChunk) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	s, err = checkedObjectSnapshot(s)
	if err != nil {
		return err
	}
	chunks, err = checkedObjectChunks(s, kb, chunks)
	if err != nil {
		return err
	}
	state, err := i.state.Read(ctx, s.Scope)
	if err != nil {
		return err
	}
	if !registryMatches(state, s) {
		return ErrObjectIndexConflict
	}
	old := state.Manifest
	generation := uuid.NewString()
	active := ""
	if manifestMatches(old, s, kb) {
		active = old.ActiveGeneration
	}
	pending := *state
	pending.Revision++
	pending.Manifest = models.Manifest{FormatVersion: 2, Status: "building", Scope: s.Scope, SchemaFingerprint: s.Fingerprint,
		KBRevision: kb, BuildingGeneration: generation, ActiveGeneration: active, ExpectedPoints: len(chunks)}
	if err = i.state.CompareAndSwap(ctx, s.Scope, state.Revision, pending); err != nil {
		return ErrObjectIndexConflict
	}
	failure := "build_failed"
	defer func() {
		if err != nil {
			i.failBuild(ctx, s, kb, generation, failure)
		}
	}()

	failure = "collection_unavailable"
	dimension := i.embedding.GetDimension()
	if err = i.vectors.Ensure(ctx, dimension); err != nil {
		return indexBuildError(failure)
	}
	texts := make([]string, len(chunks))
	for n, c := range chunks {
		texts[n] = c.Content
	}
	var vectors [][]float32
	if len(texts) > 0 {
		failure = "embedding_failed"
		vectors, err = i.embedding.EmbedBatch(ctx, texts)
		if err != nil {
			return indexBuildError(failure)
		}
	}
	if len(vectors) != len(chunks) {
		failure = "embedding_invalid"
		return indexBuildError(failure)
	}
	points := make([]vectordb.VectorPoint, len(chunks))
	byID := map[string]schemaobjects.Object{}
	for _, o := range s.Objects {
		byID[o.Metadata.ObjectID] = o
	}
	for n, c := range chunks {
		if !validObjectVector(vectors[n], dimension) {
			failure = "embedding_invalid"
			return indexBuildError(failure)
		}
		o := byID[c.Metadata["object_id"]]
		points[n] = vectordb.VectorPoint{ID: objectPointID(s.Scope, generation, o, c.Metadata["part_index"]), Vector: vectors[n],
			Payload: objectChunkPayload(s, kb, generation, o, c)}
	}
	failure = "upsert_failed"
	if len(points) > 0 {
		if err = i.vectors.Upsert(ctx, points); err != nil {
			return indexBuildError(failure)
		}
	}
	failure = "verification_failed"
	stored, err := i.vectors.Scroll(ctx, objectFilterConditions(s.Scope, generation))
	if err != nil || !completeObjectPoints(points, stored, dimension) {
		return indexBuildError(failure)
	}

	failure = "freshness_failed"
	fresh, freshKB, freshErr := i.fresh(ctx)
	if freshErr != nil {
		return indexBuildError(failure)
	}
	fresh, freshErr = checkedObjectSnapshot(fresh)
	if freshErr != nil || fresh.Scope != s.Scope || fresh.Fingerprint != s.Fingerprint || freshKB != kb {
		return ErrObjectIndexConflict
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Do not reuse the pre-callback revision: the callback may have saved Registry.
	current, readErr := i.state.Read(ctx, s.Scope)
	if readErr != nil || !ownsBuild(current, s, kb, generation) {
		return ErrObjectIndexConflict
	}
	next := *current
	next.Revision++
	next.Manifest.Status = "ready"
	next.Manifest.ActiveGeneration = generation
	next.Manifest.BuildingGeneration = ""
	next.Manifest.FailureCode = ""
	failure = "publish_conflict"
	if err = i.state.CompareAndSwap(ctx, s.Scope, current.Revision, next); err != nil {
		return ErrObjectIndexConflict
	}
	// Only retire a known former active generation. Never sweep a whole chat:
	// another writer may already be building a newer generation. Cleanup failure
	// leaves an invisible orphan and cannot undo a successful publication.
	if old.ActiveGeneration != "" && old.ActiveGeneration != generation {
		_ = i.vectors.DeleteGeneration(ctx, objectFilterConditions(old.Scope, old.ActiveGeneration))
	}
	return nil
}
func indexBuildError(code string) error { return fmt.Errorf("%w: %s", ErrObjectIndexUnavailable, code) }
func (i *objectIndex) failBuild(ctx context.Context, s schemaobjects.Snapshot, kb, generation, code string) {
	// Keep the source lease/cancellation fence. A crashed/cancelled task may remain
	// building, but no reader treats that candidate as active, including on restart.
	current, err := i.state.Read(ctx, s.Scope)
	if err != nil || !ownsBuild(current, s, kb, generation) {
		return
	}
	next := *current
	next.Revision++
	next.Manifest.Status = "failed"
	next.Manifest.FailureCode = code
	next.Manifest.BuildingGeneration = ""
	_ = i.state.CompareAndSwap(ctx, s.Scope, current.Revision, next)
}
