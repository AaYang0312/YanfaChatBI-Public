package dbmanager

import (
	"encoding/json"
	"yanfachatbi/pkg/schemaobjects"
	"strings"
	"testing"
)

func objectFixture() []schemaobjects.Object {
	return []schemaobjects.Object{
		{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "Reports.X", Name: "Sales, \"Q\"", Kind: schemaobjects.View}, CatalogOID: 10, DefinitionStatus: "unavailable"}, Columns: []schemaobjects.Column{{Name: "Amount", Type: "numeric(18,2)", Ordinal: 1}}, RowCountStatus: "unknown"},
	}
}
func TestSchemaObjectsProjectionAndReadiness(t *testing.T) {
	s := schemaFromQueryObjects(objectFixture())
	if len(s.Tables) != 0 || len(s.Views) != 1 || !s.QueryObjectsReady() {
		t.Fatalf("view-only not ready: %+v", s)
	}
	got := s.QueryObjects("source")
	if len(got) != 1 || got[0].Metadata.ObjectID == "" || got[0].Columns[0].Nullable != nil {
		t.Fatal(got)
	}
	got[0].Columns[0].Name = "mutation"
	if s.QueryObjects("source")[0].Columns[0].Name != "Amount" {
		t.Fatal("projection mutates authority")
	}
	b, _ := json.Marshal(s)
	var decoded SchemaInfo
	if err := json.Unmarshal(b, &decoded); err != nil || !decoded.QueryObjectsReady() {
		t.Fatalf("JSON roundtrip %v", err)
	}
	if strings.Contains(string(b), `"objects":`) {
		t.Fatal("third schema authority")
	}
	old := &SchemaInfo{Views: map[string]ViewSchema{"v": {Name: "v", Definition: "SELECT secret"}}}
	if old.QueryObjectsReady() || len(old.QueryObjects("source")) != 0 {
		t.Fatal("legacy incomplete view ready")
	}
	s.MetadataVersion = 1
	if s.QueryObjectsReady() {
		t.Fatal("old metadata ready")
	}
}
func TestSchemaObjectsChecksumDiffAndScope(t *testing.T) {
	a := schemaFromQueryObjects(objectFixture())
	b := schemaFromQueryObjects(objectFixture())
	if a.ObjectChecksum() != b.ObjectChecksum() {
		t.Fatal("unstable checksum")
	}
	for key, v := range b.Views {
		v.Columns["Amount"] = ColumnInfo{Name: "Amount", Type: "text", Ordinal: 1}
		b.Views[key] = v
	}
	diff, changed := compareObjectSchemas(a, b)
	if !changed || len(diff.ModifiedViews) != 1 || a.ObjectChecksum() == b.ObjectChecksum() {
		t.Fatal("view column change missed")
	}
	b = schemaFromQueryObjects(objectFixture())
	b.MetadataVersion = 1
	if a.ObjectChecksum() == b.ObjectChecksum() {
		t.Fatal("version omitted")
	}
	b = schemaFromQueryObjects(objectFixture())
	b.SelectionRevision = 2
	if a.ObjectChecksum() == b.ObjectChecksum() {
		t.Fatal("selection revision omitted")
	}
	if a.ObjectCacheKey("base") == b.ObjectCacheKey("base") {
		t.Fatal("cache scope collision")
	}
	b = schemaFromQueryObjects(nil)
	diff, changed = compareObjectSchemas(a, b)
	if !changed || len(diff.RemovedViews) != 1 {
		t.Fatal("view deletion missed")
	}
}
func TestQueryObjectsLegacySelectionAndEvidence(t *testing.T) {
	objs := objectFixture()
	objs = append(objs, schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "public", Name: "t", Kind: schemaobjects.Table}, CatalogOID: 12}, Columns: []schemaobjects.Column{{Name: "id", Type: "int", Ordinal: 1}}})
	s, err := legacyQueryObjectSchema(objs, []string{"ALL"})
	if err != nil || len(s.Tables) != 1 || len(s.Views) != 0 {
		t.Fatalf("ALL widened %v %+v", err, s)
	}
	s, err = legacyQueryObjectSchema(objs, []string{"missing"})
	if err != nil || len(s.Tables) != 0 || len(s.Views) != 0 {
		t.Fatal("missing selection expanded")
	}
	selected, err := schemaobjects.Select(objs, schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{objs[0].Metadata.Ref}})
	if err != nil {
		t.Fatal(err)
	}
	s = schemaFromQueryObjects(selected)
	if len(s.Views) != 1 || len(s.Tables) != 0 {
		t.Fatal("view-only lost")
	}
}

func TestSchemaObjectsMetadataFormatting(t *testing.T) {
	s := schemaFromQueryObjects(objectFixture())
	text := formatQueryObjectSchema(s)
	if !strings.Contains(text, "View") || !strings.Contains(text, "nullable=unknown") || strings.Contains(text, "SELECT") {
		t.Fatal(text)
	}
	s.MetadataComplete = false
	if strings.Contains(formatQueryObjectSchema(s), "Amount") {
		t.Fatal("incomplete snapshot formatted")
	}
}
func TestSchemaObjectsCacheSelectionAndNames(t *testing.T) {
	a := schemaFromQueryObjects(objectFixture())
	b := schemaFromQueryObjects(nil)
	if a.ObjectCacheKey("base") == b.ObjectCacheKey("base") {
		t.Fatal("different selected objects share cache identity")
	}
	ref := schemaobjects.Ref{Schema: "Reports.X", Name: "Sales, \"Q\"", Kind: schemaobjects.View}
	ns, name, err := splitObjectName(objectStorageName(ref))
	if err != nil || ns != ref.Schema || name != ref.Name {
		t.Fatalf("name roundtrip %q %q %v", ns, name, err)
	}
}

func TestSchemaObjectsLegacyTableCompatibility(t *testing.T) {
	s := &SchemaInfo{Tables: map[string]TableSchema{"legacy": {Name: "legacy", Columns: map[string]ColumnInfo{"id": {Name: "id", Type: "int"}}}}, Views: map[string]ViewSchema{"v": {Name: "v"}}}
	s.legacyDatabaseType = "mysql"
	if !s.QueryObjectsReady() {
		t.Fatal("legacy table readiness regressed for non-PG database")
	}
}
func TestSchemaObjectsTableDiffEvidence(t *testing.T) {
	o := schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "public", Name: "t", Kind: schemaobjects.Table}, CatalogOID: 1}, Columns: []schemaobjects.Column{{Name: "old", Type: "int", Ordinal: 1}}, Indexes: []schemaobjects.Index{{Name: "idx", Columns: []string{"old"}}}}
	a := schemaFromQueryObjects([]schemaobjects.Object{o})
	o.Columns = []schemaobjects.Column{{Name: "new", Type: "text", Ordinal: 2}}
	o.Indexes = nil
	b := schemaFromQueryObjects([]schemaobjects.Object{o})
	d, changed := compareObjectSchemas(a, b)
	if !changed {
		t.Fatal("change missed")
	}
	td := d.ModifiedTables["t"]
	if len(td.AddedColumns) != 1 || td.AddedColumns[0] != "new" || len(td.RemovedColumns) != 1 || td.RemovedColumns[0] != "old" || len(td.RemovedIndexes) != 1 {
		t.Fatalf("lost diff detail %+v", td)
	}
}
func TestSchemaObjectsDefinitionOIDAndPermissionInvalidation(t *testing.T) {
	a := schemaFromQueryObjects(objectFixture())
	original := a.ObjectChecksum()
	for name, v := range a.Views {
		v.Metadata.DefinitionFingerprint = "changed"
		a.Views[name] = v
	}
	if a.ObjectChecksum() == original {
		t.Fatal("visible definition digest ignored")
	}
	a = schemaFromQueryObjects(objectFixture())
	for name, v := range a.Views {
		v.Metadata.CatalogOID++
		a.Views[name] = v
	}
	if a.ObjectChecksum() == original {
		t.Fatal("OID recreation ignored")
	}
	a = schemaFromQueryObjects(objectFixture())
	for name, v := range a.Views {
		v.Metadata.PartialColumns = true
		a.Views[name] = v
	}
	if a.ObjectChecksum() == original {
		t.Fatal("permission projection ignored")
	}
}

func TestSchemaObjectsAllSelectionProvenance(t *testing.T) {
	o := schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "public", Name: "t", Kind: schemaobjects.Table}, CatalogOID: 1}, Columns: []schemaobjects.Column{{Name: "id", Type: "int", Ordinal: 1}}}
	all, err := legacyQueryObjectSchema([]schemaobjects.Object{o}, []string{"ALL"})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := legacyQueryObjectSchema([]schemaobjects.Object{o}, []string{"t"})
	if err != nil {
		t.Fatal(err)
	}
	if all.ObjectCacheKey("base") == explicit.ObjectCacheKey("base") {
		t.Fatal("ALL intent lost when current set equals explicit selection")
	}
}

func TestSchemaObjectsCompositeEvidenceRoundTrip(t *testing.T) {
	parent := schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "public", Name: "parent", Kind: schemaobjects.Table}, CatalogOID: 20}, Columns: []schemaobjects.Column{{Name: "tenant", Type: "int", Ordinal: 1}, {Name: "id", Type: "int", Ordinal: 2}}, Constraints: []schemaobjects.Constraint{{Name: "pk", Type: "PRIMARY KEY", Columns: []string{"id", "tenant"}}}, Indexes: []schemaobjects.Index{{Name: "idx", Unique: true, Columns: []string{"id", "tenant"}}}}
	child := schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "reporting", Name: "child", Kind: schemaobjects.Table}, CatalogOID: 21}, Columns: []schemaobjects.Column{{Name: "tenant", Type: "int", Ordinal: 1}, {Name: "parent_id", Type: "int", Ordinal: 2}}, ForeignKeys: []schemaobjects.ForeignKey{{Name: "fk", Columns: []string{"parent_id", "tenant"}, Target: parent.Metadata.Ref, TargetColumns: []string{"id", "tenant"}}}}
	s := schemaFromQueryObjects([]schemaobjects.Object{child, parent})
	projection := s.QueryObjects("source")
	for _, o := range projection {
		if o.Metadata.Ref.Name == "parent" {
			if len(o.Constraints) != 1 || strings.Join(o.Constraints[0].Columns, ",") != "id,tenant" || strings.Join(o.Indexes[0].Columns, ",") != "id,tenant" {
				t.Fatal("PK/index order lost", o)
			}
		} else {
			if len(o.ForeignKeys) != 1 || o.ForeignKeys[0].Target.Schema != "public" || strings.Join(o.ForeignKeys[0].Columns, ",") != "parent_id,tenant" || strings.Join(o.ForeignKeys[0].TargetColumns, ",") != "id,tenant" {
				t.Fatal("FK order/target lost", o)
			}
		}
	}
	before := s.ObjectChecksum()
	p := s.Tables["parent"]
	delete(p.Columns, "tenant")
	s.Tables["parent"] = p
	for _, o := range s.QueryObjects("source") {
		if len(o.ForeignKeys)+len(o.Constraints)+len(o.Indexes) != 0 {
			t.Fatal("revoked-column evidence survived", o)
		}
	}
	if s.ObjectChecksum() == before {
		t.Fatal("permission-filtered evidence did not invalidate checksum")
	}
}
