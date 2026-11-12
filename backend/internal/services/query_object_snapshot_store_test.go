package services

import (
	"context"
	"errors"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

type snapshotStateFake struct {
	state    models.State
	conflict bool
}

func (f *snapshotStateFake) Read(context.Context, schemaobjects.Scope) (*models.State, error) {
	copy := f.state
	return &copy, nil
}
func (f *snapshotStateFake) CompareAndSwap(_ context.Context, _ schemaobjects.Scope, revision int64, next models.State) error {
	if f.conflict || revision != f.state.Revision {
		return errors.New("CAS conflict")
	}
	f.state = next
	return nil
}
func TestObjectStateSnapshotPublication(t *testing.T) {
	scope := schemaobjects.Scope{OwnerID: "owner", ChatID: "chat", SourceKey: "source", SelectionRevision: 1}
	selection := schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.View}}
	objects := []schemaobjects.Object{{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "public", Name: "v", Kind: schemaobjects.View}, CatalogOID: 7}, Columns: []schemaobjects.Column{{Name: "safe", Type: "integer", Ordinal: 1}, {Name: "private", Type: "text", Ordinal: 2}}}}
	state := &snapshotStateFake{}
	first, err := publishQueryObjectSnapshot(context.Background(), state, scope, selection, observedSnapshotState(state), objects)
	if err != nil {
		t.Fatal(err)
	}
	state.state.Manifest = models.Manifest{Status: "ready", ActiveGeneration: "old", Scope: scope, SchemaFingerprint: first.Fingerprint}
	objects[0].Columns = objects[0].Columns[:1]
	second, err := publishQueryObjectSnapshot(context.Background(), state, scope, selection, observedSnapshotState(state), objects)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint || state.state.Manifest.Status != "stale" || len(second.Objects[0].Columns) != 1 {
		t.Fatal("revoked column failed to invalidate published content")
	}
	state.conflict = true
	if snapshot, err := publishQueryObjectSnapshot(context.Background(), state, scope, selection, observedSnapshotState(state), objects); err == nil || snapshot != nil {
		t.Fatal("CAS failure returned usable snapshot")
	}
	state.conflict = false
	empty, err := publishQueryObjectSnapshot(context.Background(), state, scope, selection, observedSnapshotState(state), nil)
	if err != nil || !empty.Complete || len(empty.Objects) != 0 {
		t.Fatal("complete empty snapshot fell back to old objects")
	}
	for _, instance := range state.state.Registry {
		if instance.Present {
			t.Fatal("successful deletion not recorded")
		}
	}
}

func observedSnapshotState(state *snapshotStateFake) *models.State { copy := state.state; return &copy }
func TestObjectStateLateCatalogCannotRestoreNewerObservation(t *testing.T) {
	for _, kind := range []string{"deleted", "recreated", "revoked-column"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			scope := schemaobjects.Scope{OwnerID: "o", ChatID: "c", SourceKey: "s"}
			selection := schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.View}}
			catalog := []schemaobjects.Object{{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "reporting", Name: "v", Kind: schemaobjects.View}, CatalogOID: 7}, Columns: []schemaobjects.Column{{Name: "id", Type: "int", Ordinal: 1}, {Name: "sensitive", Type: "text", Ordinal: 2}}}}
			state := &snapshotStateFake{}
			first, err := publishQueryObjectSnapshot(ctx, state, scope, selection, observedSnapshotState(state), catalog)
			if err != nil {
				t.Fatal(err)
			}
			observedBeforeOldCatalog := observedSnapshotState(state)
			newer := append([]schemaobjects.Object(nil), catalog...)
			switch kind {
			case "deleted":
				newer = nil
			case "recreated":
				newer[0].Metadata.CatalogOID = 8
			case "revoked-column":
				newer[0].Columns = newer[0].Columns[:1]
			}
			current, err := publishQueryObjectSnapshot(ctx, state, scope, selection, observedSnapshotState(state), newer)
			if err != nil {
				t.Fatal(err)
			}
			revision := state.state.Revision
			late, err := publishQueryObjectSnapshot(ctx, state, scope, selection, observedBeforeOldCatalog, catalog)
			if err == nil || late != nil || state.state.Revision != revision {
				t.Fatalf("late %s catalog accepted: revision=%d, prior=%d", kind, state.state.Revision, revision)
			}
			id := first.Objects[0].Metadata.ObjectID
			if kind == "deleted" && state.state.Registry[id].Present {
				t.Fatal("tombstone resurrected")
			}
			if kind == "recreated" && state.state.Registry[id].Version != current.Registry[id].Version {
				t.Fatal("recreated instance overwritten")
			}
		})
	}
}
