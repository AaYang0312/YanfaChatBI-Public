package dtos

import (
	"encoding/json"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestQueryObjectDirectoryWire(t *testing.T) {
	object := TableInfo{Name: "\"reporting\".\"v\"", ObjectID: "id", SchemaName: "reporting", ObjectName: "v", ObjectType: schemaobjects.View, SQLIdentifier: "\"reporting\".\"v\"", RowCountStatus: "unknown"}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["object_type"] != "view" || wire["row_count"] != nil || wire["object_id"] != "id" {
		t.Fatalf("view directory wire lost semantics: %s", raw)
	}
	object = TableInfo{Name: "legacy_table", RowCount: 42}
	raw, _ = json.Marshal(object)
	json.Unmarshal(raw, &wire)
	if wire["row_count"] != float64(42) {
		t.Fatal("legacy table row count changed")
	}
}
