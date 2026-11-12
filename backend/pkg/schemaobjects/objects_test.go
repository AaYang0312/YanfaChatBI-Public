package schemaobjects

import (
	"encoding/json"
	"strings"
	"testing"
)

func obj(schema, name string, kind Kind, oid uint32, columns ...string) Object {
	o := Object{Metadata: Metadata{Ref: Ref{Schema: schema, Name: name, Kind: kind}, CatalogOID: oid, DefinitionStatus: "not_collected"}, RowCountStatus: "unknown"}
	for i, n := range columns {
		o.Columns = append(o.Columns, Column{Name: n, Type: "integer", Ordinal: i + 1})
	}
	return o
}
func scope() Scope {
	return Scope{OwnerID: "owner", ChatID: "chat", SourceKey: "source", BindingRevision: 1, SelectionRevision: 1}
}
func all() Selection { return Selection{Version: 2, Mode: "all", IncludeKinds: []Kind{Table, View}} }

func TestSelectionLegacyScope(t *testing.T) {
	objects := []Object{obj("public", "a", Table, 1, "id"), obj("public", "b", Table, 2, "id"), obj("public", "c", Table, 3, "id"), obj("public", "d", Table, 4, "id"), obj("public", "v", View, 5, "id")}
	for _, value := range []string{"", "ALL", "a,b,c,d"} {
		selection, err := LegacySelection(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Select(objects, selection)
		if err != nil || len(got) != 4 {
			t.Fatalf("%q: %v %v", value, got, err)
		}
		for _, o := range got {
			if o.Metadata.Ref.Kind != Table {
				t.Fatal("legacy scope expanded to view")
			}
		}
	}
	if _, err := LegacySelection("a,,b"); err == nil {
		t.Fatal("malformed legacy selection accepted")
	}
}
func TestSelectionExplicitEmptyRejected(t *testing.T) {
	for _, s := range []Selection{{Version: 2, Mode: "explicit"}, {Version: 2, Mode: "all"}, {Version: 2, Mode: "all", IncludeKinds: []Kind{"materialized_view"}}, {Version: 3, Mode: "all", IncludeKinds: []Kind{Table}}, {Version: 2, Mode: "explicit", Objects: []Ref{{Schema: "public", Name: "x", Kind: View}}, IncludeKinds: []Kind{Table}}} {
		if ValidateSelection(s) == nil {
			t.Fatalf("accepted invalid selection %#v", s)
		}
	}
	empty, err := Select(nil, all())
	if err != nil || len(empty) != 0 {
		t.Fatalf("complete empty directory must stay empty: %v", err)
	}
}
func TestResolveQualifiedAndAmbiguousNames(t *testing.T) {
	objects := []Object{obj("one", "same", View, 1, "id"), obj("two", "same", View, 2, "id")}
	if _, err := Resolve(objects, []string{"same"}, nil); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	objects = append(objects, obj("public", "same", Table, 3, "id"))
	got, err := Resolve(objects, []string{"same"}, nil)
	if err != nil || len(got) != 1 || got[0].Metadata.Ref.Schema != "public" {
		t.Fatalf("public priority lost: %v %v", got, err)
	}
	got, err = Resolve(objects, []string{"one.same"}, nil)
	if err != nil || got[0].Metadata.Ref.Schema != "one" {
		t.Fatalf("qualified resolution failed: %v", err)
	}
	if _, err = Resolve(objects, []string{"same"}, []Ref{{Schema: "one", Name: "same", Kind: View}}); err == nil {
		t.Fatal("conflicting input accepted")
	}
	if _, err = Resolve(objects, []string{"secret"}, nil); err == nil || err.Error() != "object unavailable" {
		t.Fatal("missing object must be safe unavailable")
	}
}
func TestIdentifierRoundTrip(t *testing.T) {
	r := Ref{Schema: "a.b", Name: "Sales, \"Q\"", Kind: View}
	if got := SQLIdentifier(r); got != "\"a.b\".\"Sales, \"\"Q\"\"\"" {
		t.Fatalf("bad SQL quoting: %s", got)
	}
	objects := []Object{obj(r.Schema, r.Name, r.Kind, 1, "a.b")}
	got, err := Resolve(objects, []string{"\"a.b\".\"Sales, \"\"Q\"\"\""}, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("quoted round trip: %v", err)
	}
	if ObjectID("a", r) == ObjectID("b", r) || ObjectID("a", Ref{Schema: "a", Name: "b." + r.Name, Kind: View}) == ObjectID("a", r) {
		t.Fatal("tuple identity collision")
	}
	b, err := json.Marshal(Selection{Version: 2, Mode: "explicit", Objects: []Ref{r}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\"selection_version\":2") || !strings.Contains(string(b), "\"kind\":\"view\"") {
		t.Fatalf("wrong wire: %s", b)
	}
}
func TestFingerprintStableAndPermissionSensitive(t *testing.T) {
	a := obj("public", "v", View, 10, "id", "secret")
	b := obj("public", "t", Table, 11, "id")
	one, err := Reconcile(scope(), nil, []Object{a, b})
	if err != nil {
		t.Fatal(err)
	}
	two, err := Reconcile(scope(), one.Registry, []Object{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if one.Fingerprint != two.Fingerprint {
		t.Fatal("enumeration order changed fingerprint")
	}
	a.Columns = a.Columns[:1]
	three, err := Reconcile(scope(), two.Registry, []Object{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if two.Fingerprint == three.Fingerprint {
		t.Fatal("revoked column did not invalidate")
	}
}
func TestReconcileRecreatedObject(t *testing.T) {
	a := obj("public", "v", View, 10, "id")
	first, err := Reconcile(scope(), nil, []Object{a})
	if err != nil {
		t.Fatal(err)
	}
	id := first.Objects[0].Metadata.ObjectID
	version := first.Objects[0].Metadata.InstanceVersion
	if version == "" {
		t.Fatal("observed instance missing version")
	}
	next, err := Reconcile(scope(), first.Registry, []Object{a})
	if err != nil || next.Objects[0].Metadata.InstanceVersion != version {
		t.Fatal("continuous instance changed")
	}
	a.Metadata.CatalogOID = 11
	rebuilt, _ := Reconcile(scope(), next.Registry, []Object{a})
	if rebuilt.Objects[0].Metadata.InstanceVersion == version {
		t.Fatal("OID change inherited old instance")
	}
	removed, _ := Reconcile(scope(), rebuilt.Registry, nil)
	if removed.Registry[id].Present {
		t.Fatal("deleted instance still present")
	}
	restored, _ := Reconcile(scope(), removed.Registry, []Object{a})
	if restored.Objects[0].Metadata.InstanceVersion == rebuilt.Objects[0].Metadata.InstanceVersion {
		t.Fatal("reappearance inherited instance")
	}
	a.Metadata.CatalogOID = 0
	unknown, _ := Reconcile(scope(), restored.Registry, []Object{a})
	if unknown.Objects[0].Metadata.InstanceVersion != "" {
		t.Fatal("missing evidence claimed continuity")
	}
	otherScope := scope()
	otherScope.SourceKey = "other"
	a.Metadata.CatalogOID = 11
	other, _ := Reconcile(otherScope, restored.Registry, []Object{a})
	if other.Objects[0].Metadata.InstanceVersion == restored.Objects[0].Metadata.InstanceVersion {
		t.Fatal("source change inherited registry")
	}
	if _, err := Reconcile(scope(), nil, []Object{obj("public", "broken", View, 1)}); err == nil {
		t.Fatal("incomplete object published as complete")
	}
}
func TestViewUnknownRowsAndFormatting(t *testing.T) {
	v := obj("reporting", "sales", View, 9, "amount")
	got, err := Select([]Object{v}, all())
	if err != nil {
		t.Fatal(err)
	}
	text := Format(got)
	for _, want := range []string{"View", "\"reporting\".\"sales\"", "amount", "integer", "unknown"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "COUNT(") || strings.Contains(text, "Definition:") {
		t.Fatal("metadata formatter leaked query/definition")
	}
}
func TestSelectedTableConstraintsAndRelationships(t *testing.T) {
	parent := obj("public", "parent", Table, 1, "id")
	child := obj("public", "child", Table, 2, "id", "parent_id")
	child.Constraints = []Constraint{{Name: "child_pk", Type: "PRIMARY KEY", Columns: []string{"id"}}}
	child.Indexes = []Index{{Name: "child_parent_idx", Columns: []string{"parent_id"}}}
	child.ForeignKeys = []ForeignKey{{Name: "child_parent_fk", Columns: []string{"parent_id"}, Target: parent.Metadata.Ref, TargetColumns: []string{"id"}}}
	both, err := Select([]Object{parent, child}, all())
	if err != nil {
		t.Fatal(err)
	}
	text := Format(both)
	for _, want := range []string{"child_pk", "child_parent_idx", "child_parent_fk"} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost existing table evidence %s: %s", want, text)
		}
	}
	only, err := Select([]Object{parent, child}, Selection{Version: 2, Mode: "explicit", Objects: []Ref{child.Metadata.Ref}})
	if err != nil {
		t.Fatal(err)
	}
	if len(only[0].ForeignKeys) != 0 || strings.Contains(Format(only), "child_parent_fk") {
		t.Fatal("unselected relation leaked")
	}
	before, _ := Reconcile(scope(), nil, both)
	after, _ := Reconcile(scope(), before.Registry, only)
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("relation removal did not invalidate")
	}
	parent.Columns = []Column{{Name: "safe", Type: "integer", Ordinal: 2}}
	narrowed, _ := Select([]Object{parent, child}, all())
	if strings.Contains(Format(narrowed), "child_parent_fk") {
		t.Fatal("revoked target column leaked")
	}
	child.Columns = child.Columns[:1]
	narrowed, _ = Select([]Object{parent, child}, all())
	if strings.Contains(Format(narrowed), "child_parent_idx") {
		t.Fatal("revoked source column index leaked")
	}
	child.Metadata.Ref.Kind = View
	views, _ := Select([]Object{child}, all())
	if len(views[0].Constraints)+len(views[0].Indexes)+len(views[0].ForeignKeys) != 0 {
		t.Fatal("view inherited base constraints")
	}
	if len(child.ForeignKeys) != 1 {
		t.Fatal("projection mutated authoritative source")
	}
}

func TestMetadataObjectTypeWire(t *testing.T) {
	for _, kind := range []Kind{Table, View} {
		raw, err := json.Marshal(Metadata{Ref: Ref{Schema: "public", Name: "object", Kind: kind}})
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if wire["object_type"] != string(kind) {
			t.Fatalf("missing derived object_type=%s: %s", kind, raw)
		}
		var roundTrip Metadata
		if err := json.Unmarshal(raw, &roundTrip); err != nil {
			t.Fatal(err)
		}
		if roundTrip.Ref.Kind != kind {
			t.Fatalf("canonical kind lost: %#v", roundTrip)
		}
	}
}
