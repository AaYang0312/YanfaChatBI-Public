package dbmanager

import (
	"encoding/json"
	"yanfachatbi/pkg/schemaobjects"
	"testing"
)

func TestSchemaObjectsDefaultOnlyChangeInvalidates(t *testing.T) {
	makeSchema := func(value string) *SchemaInfo {
		s := schemaFromQueryObjects([]schemaobjects.Object{{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "public", Name: "quantity", Kind: schemaobjects.Table}, CatalogOID: 10}, Columns: []schemaobjects.Column{{Name: "amount", Type: "integer", Ordinal: 1}}}})
		table := s.Tables["quantity"]
		c := table.Columns["amount"]
		c.DefaultValue = value
		table.Columns["amount"] = c
		s.Tables["quantity"] = table
		return s
	}
	a, b := makeSchema("1"), makeSchema("2")
	diff, changed := compareObjectSchemas(a, b)
	if !changed || a.ObjectChecksum() == b.ObjectChecksum() {
		t.Fatalf("default-only change missed: changed=%v checksum_equal=%v diff=%+v", changed, a.ObjectChecksum() == b.ObjectChecksum(), diff)
	}
	if len(diff.ModifiedTables["quantity"].ModifiedColumns) != 1 {
		t.Fatal("default column diff missing", diff)
	}
	round := schemaFromQueryObjects(a.QueryObjects("source"))
	if round.Tables["quantity"].Columns["amount"].DefaultValue != "1" {
		t.Fatal("default lost in projection roundtrip")
	}
}
func TestSchemaObjectsViewDoesNotInheritDefault(t *testing.T) {
	s := schemaFromQueryObjects(objectFixture())
	for name, v := range s.Views {
		c := v.Columns["Amount"]
		c.DefaultValue = "secret_default()"
		v.Columns["Amount"] = c
		s.Views[name] = v
	}
	b, _ := json.Marshal(s.QueryObjects("source"))
	var objects []map[string]any
	_ = json.Unmarshal(b, &objects)
	cols := objects[0]["columns"].([]any)
	if v, ok := cols[0].(map[string]any)["default_value"]; ok && v != "" {
		t.Fatal("view default escaped", v)
	}
}
