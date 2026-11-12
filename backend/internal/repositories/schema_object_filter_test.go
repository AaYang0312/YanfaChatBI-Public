package repositories

import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestObjectStateFilterFences(t *testing.T) {
	scope := schemaobjects.Scope{OwnerID: "507f1f77bcf86cd799439011", ChatID: "507f191e810c19729de860ea", SourceKey: "reader-source", BindingRevision: 2, ConnectionRevision: 4, SelectionRevision: 7}
	f, err := objectScopeFilter(scope)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"source_binding_revision": 2, "connection_revision": 4, "selection_revision": 7} {
		if f[key] != want {
			t.Fatalf("missing CAS fence %s: %#v", key, f)
		}
	}
	if f["user_id"] != (primitive.ObjectID{0x50, 0x7f, 0x1f, 0x77, 0xbc, 0xf8, 0x6c, 0xd7, 0x99, 0x43, 0x90, 0x11}) {
		t.Fatal("owner not matched")
	}
	raw, err := bson.Marshal(f)
	if err != nil || len(raw) == 0 {
		t.Fatal("invalid BSON filter")
	}
	scope.OwnerID = "other-owner"
	if _, err := objectScopeFilter(scope); err == nil {
		t.Fatal("invalid owner accepted")
	}
	scope.OwnerID = "507f1f77bcf86cd799439011"
	scope.SourceKey = ""
	if _, err := objectScopeFilter(scope); err == nil {
		t.Fatal("unbound scope accepted")
	}
}
func TestObjectStateLegacyRevisionFilter(t *testing.T) {
	f, err := objectScopeFilter(schemaobjects.Scope{OwnerID: "507f1f77bcf86cd799439011", ChatID: "507f191e810c19729de860ea", SourceKey: "source"})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := f["selection_revision"].(bson.M)
	if !ok {
		t.Fatal("missing legacy null/0 match")
	}
	a := v["$in"].(bson.A)
	if len(a) != 2 || a[0] != nil || a[1] != int64(0) {
		t.Fatalf("unexpected legacy filter %v", a)
	}
}

func TestObjectStateRebindingSelectionFields(t *testing.T) {
	previous := &models.Chat{SelectionRevision: 4, SelectedObjects: &schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.View}}}
	next := *previous
	if _, err := objectRebindingFields(previous, &next); err == nil {
		t.Fatal("v2 binding change silently inherited old selection")
	}
	next.SelectionRevision = 5
	next.SelectedCollections = "__QUERY_OBJECTS_V2__"
	fields, err := objectRebindingFields(previous, &next)
	if err != nil {
		t.Fatal(err)
	}
	if fields["selection_revision"] != int64(5) || fields["selected_objects"] == nil {
		t.Fatal("binding CAS missing new selection")
	}
	next.SelectedCollections = "ALL"
	if _, err := objectRebindingFields(previous, &next); err == nil {
		t.Fatal("v2 rebound to legacy ALL")
	}
}
