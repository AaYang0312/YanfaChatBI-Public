package services

import (
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestQueryObjectProjectionDoesNotTreatDeselectionAsDeletion(t *testing.T) {
	scope := schemaobjects.Scope{OwnerID: "owner", ChatID: "chat", SourceKey: "source"}
	catalog := []schemaobjects.Object{
		{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "public", Name: "a", Kind: schemaobjects.Table}, CatalogOID: 1}, Columns: []schemaobjects.Column{{Name: "id", Type: "integer", Ordinal: 1}}},
		{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "public", Name: "b", Kind: schemaobjects.View}, CatalogOID: 2}, Columns: []schemaobjects.Column{{Name: "id", Type: "integer", Ordinal: 1}}},
	}
	all := schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.Table, schemaobjects.View}}
	initial, err := projectQueryCatalog(scope, nil, catalog, all)
	if err != nil {
		t.Fatal(err)
	}
	selected := schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "public", Name: "b", Kind: schemaobjects.View}}}
	narrowed, err := projectQueryCatalog(scope, initial.Registry, catalog, selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrowed.Objects) != 1 || narrowed.Objects[0].Metadata.Ref.Name != "b" {
		t.Fatal("selection not applied")
	}
	for id, entry := range initial.Registry {
		if !narrowed.Registry[id].Present || narrowed.Registry[id].Version != entry.Version {
			t.Fatal("deselected live object recorded as deletion")
		}
	}
	restored, err := projectQueryCatalog(scope, narrowed.Registry, catalog, all)
	if err != nil {
		t.Fatal(err)
	}
	for id, entry := range initial.Registry {
		if restored.Registry[id].Version != entry.Version {
			t.Fatal("selection toggle destroyed instance continuity")
		}
	}
	if _, err := projectQueryCatalog(scope, initial.Registry, []schemaobjects.Object{{Metadata: catalog[0].Metadata}}, all); err == nil {
		t.Fatal("incomplete collection accepted")
	}
}
