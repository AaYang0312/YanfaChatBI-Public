package dbmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"yanfachatbi/pkg/schemaobjects"
	"sort"
	"strings"
	"time"
)

const queryObjectMetadataVersion = 2

// QueryObjects is an ephemeral, detached projection. Tables/Views remain the
// only stored authority. This does not replace a current reader permission check.
func (s *SchemaInfo) QueryObjects(sourceKey string) []schemaobjects.Object {
	if s == nil {
		return nil
	}
	objects := []schemaobjects.Object{}
	for key, t := range s.Tables {
		m := schemaobjects.Metadata{Ref: legacyObjectRef(key, schemaobjects.Table), DefinitionStatus: "not_collected"}
		if t.Metadata != nil {
			m = *t.Metadata
		}
		if m.Ref.Kind != schemaobjects.Table {
			continue
		}
		o := schemaobjects.Object{Metadata: m, Columns: projectColumns(t.Columns, t.Metadata != nil), Comment: t.Comment, RowCountStatus: t.RowCountStatus}
		if t.Metadata == nil {
			o.RowCountStatus = "known"
		}
		if o.RowCountStatus == "" {
			o.RowCountStatus = "unknown"
		}
		if o.RowCountStatus == "known" || o.RowCountStatus == "estimated" {
			n := t.RowCount
			o.RowCount = &n
		}
		for _, c := range t.Constraints {
			o.Constraints = append(o.Constraints, schemaobjects.Constraint{Name: c.Name, Type: c.Type, Columns: append([]string(nil), c.Columns...)})
		}
		for _, x := range t.Indexes {
			o.Indexes = append(o.Indexes, schemaobjects.Index{Name: x.Name, Columns: append([]string(nil), x.Columns...), Unique: x.IsUnique})
		}
		for _, f := range t.ForeignKeys {
			target := legacyObjectRef(f.RefTable, schemaobjects.Table)
			if f.Target != nil {
				target = *f.Target
			}
			cols, refs := f.Columns, f.RefColumns
			if len(cols) == 0 && f.ColumnName != "" {
				cols = []string{f.ColumnName}
			}
			if len(refs) == 0 && f.RefColumn != "" {
				refs = []string{f.RefColumn}
			}
			o.ForeignKeys = append(o.ForeignKeys, schemaobjects.ForeignKey{Name: f.Name, Columns: cols, Target: target, TargetColumns: refs, OnDelete: f.OnDelete, OnUpdate: f.OnUpdate})
		}
		objects = append(objects, o)
	}
	for _, v := range s.Views {
		// Legacy name/definition-only entries cannot become ready by adaptation.
		if s.MetadataVersion != queryObjectMetadataVersion || !s.MetadataComplete || v.Metadata == nil || v.Metadata.Ref.Kind != schemaobjects.View || len(v.Columns) == 0 {
			continue
		}
		cols := projectColumns(v.Columns, true)
		for i := range cols {
			cols[i].DefaultValue = ""
		}
		objects = append(objects, schemaobjects.Object{Metadata: *v.Metadata, Columns: cols, Comment: v.Comment, RowCountStatus: "unknown"})
	}
	objects, _ = schemaobjects.Select(objects, schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.Table, schemaobjects.View}})
	for i := range objects {
		o := &objects[i]
		o.Metadata.ObjectID = schemaobjects.ObjectID(sourceKey, o.Metadata.Ref)
		sort.Slice(o.Constraints, func(i, j int) bool { return o.Constraints[i].Name < o.Constraints[j].Name })
		sort.Slice(o.Indexes, func(i, j int) bool { return o.Indexes[i].Name < o.Indexes[j].Name })
		sort.Slice(o.ForeignKeys, func(i, j int) bool { return o.ForeignKeys[i].Name < o.ForeignKeys[j].Name })
		o.Metadata.Fingerprint = objectStructureChecksum(*o)
	}
	return objects
}
func projectColumns(columns map[string]ColumnInfo, modern bool) []schemaobjects.Column {
	result := make([]schemaobjects.Column, 0, len(columns))
	for name, c := range columns {
		if c.Name != "" {
			name = c.Name
		}
		var n *bool
		if modern {
			if c.Nullable != nil {
				b := *c.Nullable
				n = &b
			}
		} else {
			b := c.IsNullable
			n = &b
		}
		result = append(result, schemaobjects.Column{Name: name, Type: c.Type, Ordinal: c.Ordinal, Nullable: n, Comment: c.Comment, DefaultValue: c.DefaultValue})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Ordinal == result[j].Ordinal {
			return result[i].Name < result[j].Name
		}
		return result[i].Ordinal < result[j].Ordinal
	})
	if !modern {
		for i := range result {
			if result[i].Ordinal == 0 {
				result[i].Ordinal = i + 1
			}
		}
	}
	return result
}
func objectStructureChecksum(o schemaobjects.Object) string {
	o.Metadata.ObjectID = ""
	o.Metadata.InstanceVersion = ""
	o.Metadata.Fingerprint = ""
	o.RowCount = nil
	o.RowCountStatus = ""
	return objectDigest(o)
}
func objectDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func schemaFromQueryObjects(objects []schemaobjects.Object) *SchemaInfo {
	s := &SchemaInfo{Tables: map[string]TableSchema{}, Views: map[string]ViewSchema{}, MetadataVersion: queryObjectMetadataVersion, MetadataComplete: true, UpdatedAt: time.Now()}
	objects, _ = schemaobjects.Select(objects, schemaobjects.Selection{Version: 2, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.Table, schemaobjects.View}})
	for _, o := range objects {
		m := o.Metadata
		cols := map[string]ColumnInfo{}
		for _, c := range o.Columns {
			ci := ColumnInfo{Name: c.Name, Type: c.Type, Ordinal: c.Ordinal, Comment: c.Comment, DefaultValue: c.DefaultValue}
			if c.Nullable != nil {
				n := *c.Nullable
				ci.Nullable = &n
				ci.IsNullable = n
			}
			if m.Ref.Kind == schemaobjects.View {
				ci.DefaultValue = ""
			}
			cols[c.Name] = ci
		}
		name := objectStorageName(m.Ref)
		if m.Ref.Kind == schemaobjects.View {
			s.Views[name] = ViewSchema{Name: name, Metadata: &m, Columns: cols, Comment: o.Comment, RowCountStatus: "unknown", Checksum: objectStructureChecksum(o)}
			continue
		}
		t := TableSchema{Name: name, Metadata: &m, Columns: cols, Comment: o.Comment, RowCountStatus: o.RowCountStatus, Indexes: map[string]IndexInfo{}, Constraints: map[string]ConstraintInfo{}, ForeignKeys: map[string]ForeignKey{}}
		if t.RowCountStatus == "" {
			t.RowCountStatus = "unknown"
		}
		if o.RowCount != nil {
			t.RowCount = *o.RowCount
		}
		for _, c := range o.Constraints {
			t.Constraints[c.Name] = ConstraintInfo{Name: c.Name, Type: c.Type, Columns: append([]string(nil), c.Columns...)}
		}
		for _, x := range o.Indexes {
			t.Indexes[x.Name] = IndexInfo{Name: x.Name, Columns: append([]string(nil), x.Columns...), IsUnique: x.Unique}
		}
		for _, f := range o.ForeignKeys {
			target := f.Target
			fk := ForeignKey{Name: f.Name, Columns: append([]string(nil), f.Columns...), RefColumns: append([]string(nil), f.TargetColumns...), Target: &target, RefTable: objectStorageName(target), OnDelete: f.OnDelete, OnUpdate: f.OnUpdate}
			if len(f.Columns) == 1 {
				fk.ColumnName = f.Columns[0]
				fk.RefColumn = f.TargetColumns[0]
			}
			t.ForeignKeys[f.Name] = fk
		}
		t.Checksum = objectStructureChecksum(o)
		s.Tables[name] = t
	}
	s.Checksum = s.ObjectChecksum()
	return s
}

func legacyQueryObjectSchema(objects []schemaobjects.Object, names []string) (*SchemaInfo, error) {
	selection := schemaobjects.Selection{Version: 1, Mode: "all", IncludeKinds: []schemaobjects.Kind{schemaobjects.Table}}
	if len(names) > 0 && !(len(names) == 1 && (names[0] == "ALL" || names[0] == "")) {
		selection.Mode = "explicit"
		selection.IncludeKinds = nil
		for _, name := range names {
			ns, bare, err := splitObjectName(name)
			if err != nil {
				return nil, err
			}
			selection.Objects = append(selection.Objects, schemaobjects.Ref{Schema: ns, Name: bare, Kind: schemaobjects.Table})
		}
	}
	chosen, err := schemaobjects.Select(objects, selection)
	if err != nil {
		return nil, err
	}
	s := schemaFromQueryObjects(chosen)
	s.ObjectSelection = &selection
	s.Checksum = s.ObjectChecksum()
	return s, nil
}

func (s *SchemaInfo) QueryObjectsReady() bool {
	if s == nil {
		return false
	}
	if s.MetadataVersion == 0 {
		return isLegacySchemaType(s.legacyDatabaseType) && len(s.Tables) > 0
	}
	if s.MetadataVersion != queryObjectMetadataVersion || !s.MetadataComplete {
		return false
	}
	if len(s.Tables)+len(s.Views) == 0 {
		return false
	}
	for _, t := range s.Tables {
		if !validObjectMetadata(t.Metadata, schemaobjects.Table) || !validObjectColumns(t.Columns) {
			return false
		}
	}
	for _, v := range s.Views {
		if !validObjectMetadata(v.Metadata, schemaobjects.View) || !validObjectColumns(v.Columns) {
			return false
		}
	}
	return true
}
func validObjectMetadata(m *schemaobjects.Metadata, k schemaobjects.Kind) bool {
	return m != nil && m.Ref.Kind == k && m.Ref.Schema != "" && m.Ref.Name != "" && m.CatalogOID != 0
}
func validObjectColumns(cols map[string]ColumnInfo) bool {
	if len(cols) == 0 {
		return false
	}
	seen := map[int]bool{}
	for _, c := range cols {
		if c.Name == "" || c.Type == "" || c.Ordinal < 1 || seen[c.Ordinal] {
			return false
		}
		seen[c.Ordinal] = true
	}
	return true
}
func (s *SchemaInfo) ObjectChecksum() string {
	if s == nil {
		return ""
	}
	// Raw legacy view entries are included for invalidation, never for formatting.
	sums := s.objectChecksums()
	if s.MetadataVersion != queryObjectMetadataVersion {
		for name, v := range s.Views {
			sums["view:"+name] = objectDigest(v)
		}
	}
	return objectDigest(struct {
		Version   int
		Complete  bool
		Selection int64
		Scope     schemaobjects.Scope
		Intent    *schemaobjects.Selection
		Objects   map[string]string
	}{s.MetadataVersion, s.MetadataComplete, s.SelectionRevision, s.ObjectScope, s.ObjectSelection, sums})
}
func (s *SchemaInfo) objectChecksums() map[string]string {
	out := map[string]string{}
	for _, o := range s.QueryObjects(s.ObjectScope.SourceKey) {
		out[string(o.Metadata.Ref.Kind)+":"+objectStorageName(o.Metadata.Ref)] = objectStructureChecksum(o)
	}
	return out
}

// ObjectCacheKey separates metadata/selection/source revisions. It is a cache
// identity, not authorization: callers still collect with the current reader.
func (s *SchemaInfo) ObjectCacheKey(base string) string {
	refs := []schemaobjects.Ref{}
	for _, o := range s.QueryObjects(s.ObjectScope.SourceKey) {
		refs = append(refs, o.Metadata.Ref)
	}
	return base + ":query-objects:" + objectDigest(struct {
		Version   int
		Selection int64
		Scope     schemaobjects.Scope
		Intent    *schemaobjects.Selection
		Refs      []schemaobjects.Ref
	}{s.MetadataVersion, s.SelectionRevision, s.ObjectScope, s.ObjectSelection, refs})
}

func compareObjectSchemas(a, b *SchemaInfo) (*SchemaDiff, bool) {
	d := &SchemaDiff{ModifiedTables: map[string]TableDiff{}, UpdatedAt: time.Now()}
	if a == nil || b == nil {
		d.MetadataChanged = true
		return d, true
	}
	d.MetadataChanged = a.MetadataVersion != b.MetadataVersion || a.MetadataComplete != b.MetadataComplete || a.SelectionRevision != b.SelectionRevision || a.ObjectScope != b.ObjectScope || objectDigest(a.ObjectSelection) != objectDigest(b.ObjectSelection)
	old, new := a.objectChecksums(), b.objectChecksums()
	for key, sum := range new {
		prior, ok := old[key]
		kind, name, _ := strings.Cut(key, ":")
		if !ok {
			if kind == "view" {
				d.AddedViews = append(d.AddedViews, name)
			} else {
				d.AddedTables = append(d.AddedTables, name)
			}
		} else if prior != sum {
			if kind == "view" {
				d.ModifiedViews = append(d.ModifiedViews, name)
			} else {
				d.ModifiedTables[name] = diffObjectTable(a.Tables[name], b.Tables[name])
			}
		}
	}
	for key := range old {
		if _, ok := new[key]; !ok {
			kind, name, _ := strings.Cut(key, ":")
			if kind == "view" {
				d.RemovedViews = append(d.RemovedViews, name)
			} else {
				d.RemovedTables = append(d.RemovedTables, name)
			}
		}
	}
	for _, names := range [][]string{d.AddedTables, d.RemovedTables, d.AddedViews, d.RemovedViews, d.ModifiedViews} {
		sort.Strings(names)
	}
	changed := d.MetadataChanged || len(d.AddedTables)+len(d.RemovedTables)+len(d.ModifiedTables)+len(d.AddedViews)+len(d.RemovedViews)+len(d.ModifiedViews) > 0
	if !changed {
		return nil, false
	}
	return d, true
}
func diffObjectTable(a, b TableSchema) TableDiff {
	d := TableDiff{MetadataChanged: objectDigest(a.Metadata) != objectDigest(b.Metadata) || a.Comment != b.Comment}
	for n, c := range b.Columns {
		old, ok := a.Columns[n]
		if !ok {
			d.AddedColumns = append(d.AddedColumns, n)
		} else if objectDigest(old) != objectDigest(c) {
			d.ModifiedColumns = append(d.ModifiedColumns, n)
		}
	}
	for n := range a.Columns {
		if _, ok := b.Columns[n]; !ok {
			d.RemovedColumns = append(d.RemovedColumns, n)
		}
	}
	d.AddedIndexes, d.RemovedIndexes = changedObjectEvidence(a.Indexes, b.Indexes)
	d.AddedFKs, d.RemovedFKs = changedObjectEvidence(a.ForeignKeys, b.ForeignKeys)
	d.AddedConstraints, d.RemovedConstraints = changedObjectEvidence(a.Constraints, b.Constraints)
	for _, names := range [][]string{d.AddedColumns, d.RemovedColumns, d.ModifiedColumns} {
		sort.Strings(names)
	}
	return d
}
func changedObjectEvidence[T any](a, b map[string]T) (added, removed []string) {
	for n, v := range b {
		old, ok := a[n]
		if !ok || objectDigest(old) != objectDigest(v) {
			added = append(added, n)
			if ok {
				removed = append(removed, n)
			}
		}
	}
	for n := range a {
		if _, ok := b[n]; !ok {
			removed = append(removed, n)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}

func objectStorageName(r schemaobjects.Ref) string {
	simple := func(s string) bool {
		if s == "" {
			return false
		}
		for i, c := range s {
			if !(c == '_' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
				return false
			}
		}
		return true
	}
	if simple(r.Name) && (r.Schema == "" || r.Schema == "public") {
		return r.Name
	}
	if simple(r.Schema) && simple(r.Name) {
		return r.Schema + "." + r.Name
	}
	if r.Schema == "" {
		r.Schema = "public"
	}
	return schemaobjects.SQLIdentifier(r)
}
func legacyObjectRef(name string, kind schemaobjects.Kind) schemaobjects.Ref {
	ns, bare, err := splitObjectName(name)
	if err != nil {
		return schemaobjects.Ref{}
	}
	if ns == "" {
		ns = "public"
	}
	return schemaobjects.Ref{Schema: ns, Name: bare, Kind: kind}
}
func splitObjectName(s string) (string, string, error) {
	var parts []string
	var b strings.Builder
	quoted, closed := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0 {
			return "", "", fmt.Errorf("invalid object name")
		}
		if quoted {
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte(c)
					i++
				} else {
					quoted = false
					closed = true
				}
			} else {
				b.WriteByte(c)
			}
		} else {
			switch c {
			case '"':
				if b.Len() != 0 || closed {
					return "", "", fmt.Errorf("invalid object name")
				}
				quoted = true
			case '.':
				if b.Len() == 0 {
					return "", "", fmt.Errorf("invalid object name")
				}
				parts = append(parts, b.String())
				b.Reset()
				closed = false
			default:
				if closed {
					return "", "", fmt.Errorf("invalid object name")
				}
				b.WriteByte(c)
			}
		}
	}
	if quoted || b.Len() == 0 || len(parts) > 1 {
		return "", "", fmt.Errorf("invalid object name")
	}
	if len(parts) == 0 {
		return "", b.String(), nil
	}
	return parts[0], b.String(), nil
}

func formatQueryObjectSchema(s *SchemaInfo) string {
	if s == nil {
		return ""
	}
	if !s.QueryObjectsReady() {
		return "Query object metadata unavailable; refresh required.\n"
	}
	return schemaobjects.Format(s.QueryObjects(s.ObjectScope.SourceKey))
}
