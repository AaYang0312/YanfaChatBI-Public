package services

import (
	"context"
	"errors"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
)

type queryObjectSnapshotStore interface {
	Read(context.Context, schemaobjects.Scope) (*models.State, error)
	CompareAndSwap(context.Context, schemaobjects.Scope, int64, models.State) error
}

func publishQueryObjectSnapshot(ctx context.Context, store queryObjectSnapshotStore, scope schemaobjects.Scope, selection schemaobjects.Selection, observed *models.State, catalog []schemaobjects.Object) (*schemaobjects.Snapshot, error) {
	if store == nil {
		return nil, errors.New("query object state repository unavailable")
	}
	// observed was read before catalog collection. Never rebase an old catalog on
	// a newer state revision: its CAS must lose to any newer publication.
	state := observed
	if state == nil {
		return nil, errors.New("query object state unavailable")
	}
	snapshot, err := projectQueryCatalog(scope, state.Registry, catalog, selection)
	if err != nil {
		return nil, err
	}
	next := *state
	next.Scope = scope
	next.Registry = snapshot.Registry
	next.Revision = state.Revision + 1
	if next.Manifest.Status == "" {
		next.Manifest.Status = "not_ready"
	}
	mismatch := next.Manifest.Scope != scope || next.Manifest.SchemaFingerprint != snapshot.Fingerprint
	if next.Manifest.ActiveGeneration != "" && mismatch {
		next.Manifest.Status = "stale"
	}
	if next.Manifest.BuildingGeneration != "" && mismatch {
		next.Manifest.Status = "stale"
		next.Manifest.BuildingGeneration = ""
	}
	if err = store.CompareAndSwap(ctx, scope, state.Revision, next); err != nil {
		return nil, err
	}
	return snapshot, nil
}
