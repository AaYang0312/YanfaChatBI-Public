package services

import (
	"yanfachatbi/pkg/schemaobjects"
	"strings"
	"testing"
)

func TestQueryObjectToolResolvesOnlyCurrentProjection(t *testing.T) {
	ref := schemaobjects.Ref{Schema: `sales.audit`, Name: `Order,"Report`, Kind: schemaobjects.View}
	obj := schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: ref, CatalogOID: 7}, Columns: []schemaobjects.Column{{Name: "visible", Type: "text", Ordinal: 1}}}
	snapshot, err := schemaobjects.Reconcile(schemaobjects.Scope{OwnerID: "owner", ChatID: "chat", SourceKey: "source"}, nil, []schemaobjects.Object{obj})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]interface{}{
		{"object_refs": []schemaobjects.Ref{ref}},
		{"table_names": []string{schemaobjects.SQLIdentifier(ref)}},
	} {
		got, err := resolveQueryObjectArguments(snapshot, args)
		if err != nil || len(got) != 1 || got[0].Metadata.Ref != ref {
			t.Fatalf("resolution: %+v %v", got, err)
		}
	}
	for _, args := range []map[string]interface{}{
		{}, {"table_names": []string{}},
		{"table_names": []string{"private_base"}},
		{"table_names": []string{schemaobjects.SQLIdentifier(ref)}, "object_refs": []schemaobjects.Ref{}},
		{"object_refs": []map[string]interface{}{{"schema": ref.Schema, "name": ref.Name, "kind": "table"}}},
		{"object_refs": []map[string]interface{}{{"schema": ref.Schema, "name": ref.Name, "kind": "view", "ignore_scope": true}}},
		{"table_names": "ALL"}, {"sql": "SELECT * FROM private_base"},
	} {
		got, err := resolveQueryObjectArguments(snapshot, args)
		if err == nil || len(got) > 0 || strings.Contains(err.Error(), "private_base") {
			t.Fatalf("unsafe resolution for %+v: %+v %v", args, got, err)
		}
	}
	snapshot.Complete = false
	if got, err := resolveQueryObjectArguments(snapshot, map[string]interface{}{"object_refs": []schemaobjects.Ref{ref}}); err == nil || len(got) > 0 {
		t.Fatal("incomplete snapshot usable")
	}
}
