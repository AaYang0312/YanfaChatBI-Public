package repositories

import (
	"yanfachatbi/internal/models"
	"testing"
)

func TestLegacyRebindingFieldsPersistSelectionAtomically(t *testing.T) {
	for _, selected := range []string{"", "ALL", "sales,items", "orders,items,shops,refunds"} {
		t.Run(selected, func(t *testing.T) {
			previous := &models.Chat{SelectedCollections: "orders,items,shops,refunds", SelectionRevision: 7}
			next := *previous
			next.SelectedCollections, next.SelectionRevision = selected, 8
			fields, err := objectRebindingFields(previous, &next)
			if err != nil {
				t.Fatal(err)
			}
			if fields["selected_collections"] != selected || fields["selection_revision"] != int64(8) {
				t.Fatalf("connection commit omits target selection: %#v", fields)
			}
			value, exists := fields["selected_objects"]
			if !exists || value != nil {
				t.Fatalf("legacy authority not explicit: %#v", fields)
			}
		})
	}
}

func TestLegacyRebindingFieldsRejectUnversionedChanges(t *testing.T) {
	previous := &models.Chat{SelectedCollections: "orders", SelectionRevision: 7}
	for _, revision := range []int64{6, 7, 9} {
		next := *previous
		next.SelectedCollections, next.SelectionRevision = "", revision
		if _, err := objectRebindingFields(previous, &next); err == nil {
			t.Fatalf("unversioned selection change accepted: revision=%d", revision)
		}
	}
}

func TestLegacyFirstERPBindingKeepsInitialSelection(t *testing.T) {
	previous := &models.Chat{SelectedCollections: "orders,items"}
	next := *previous
	fields, err := objectRebindingFields(previous, &next)
	if err != nil {
		t.Fatal(err)
	}
	// First creation already persisted this exact legacy selection; it need not
	// fabricate a new revision merely to confirm the initial source binding.
	if len(fields) != 0 {
		t.Fatalf("initial binding rewrites selection: %#v", fields)
	}
}
