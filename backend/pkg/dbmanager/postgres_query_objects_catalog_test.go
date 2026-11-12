package dbmanager

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"yanfachatbi/pkg/schemaobjects"
	"strings"
	"testing"
)

type catalogFixture struct {
	queries    []string
	fail       string
	empty      bool
	definition driver.Value
	rowErr     bool
}
type catalogConnector struct{ f *catalogFixture }

func (c catalogConnector) Connect(context.Context) (driver.Conn, error) {
	return &catalogConn{f: c.f}, nil
}
func (c catalogConnector) Driver() driver.Driver { return catalogDriver{} }

type catalogDriver struct{}

func (catalogDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type catalogConn struct{ f *catalogFixture }

func (*catalogConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepares") }
func (*catalogConn) Close() error                        { return nil }
func (*catalogConn) Begin() (driver.Tx, error)           { return nil, errors.New("no transactions") }

type catalogRows struct {
	cols []string
	rows [][]driver.Value
	n    int
	fail bool
}

func (r *catalogRows) Columns() []string { return r.cols }
func (r *catalogRows) Close() error      { return nil }
func (r *catalogRows) Next(dest []driver.Value) error {
	if r.n >= len(r.rows) {
		if r.fail {
			return errors.New("stream failed")
		}
		return io.EOF
	}
	copy(dest, r.rows[r.n])
	r.n++
	return nil
}
func (c *catalogConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.f.queries = append(c.f.queries, q)
	if c.f.fail != "" && strings.Contains(q, c.f.fail) {
		return nil, errors.New("catalog unavailable")
	}
	// These assertions inspect SQL emitted by the production boundary, not SQL execution.
	// A real PG runner must still verify privilege and catalog semantics.
	for _, bad := range []string{"COUNT(", "pg_get_viewdef", "SELECT * FROM", "LIMIT "} {
		if strings.Contains(q, bad) {
			return nil, errors.New("business/definition query forbidden: " + bad)
		}
	}
	var out catalogRows
	switch {
	case strings.Contains(q, "query_objects_columns_v2"):
		for _, required := range []string{"has_schema_privilege", "has_column_privilege", "attisdropped", "relpersistence", "format_type", "$1", "pg_catalog.pg_type", "t.typnotnull", "pg_catalog.pg_attrdef", "pg_get_expr", "attgenerated"} {
			if !strings.Contains(q, required) {
				return nil, errors.New("missing catalog guard: " + required)
			}
		}
		if len(args) != 3 {
			return nil, errors.New("expected parameterized kinds")
		}
		out = catalogRows{cols: []string{"oid", "schema_name", "object_name", "kind", "object_comment", "column_name", "column_type", "ordinal", "attnotnull", "type_kind", "typnotnull", "column_comment", "default_value", "partial_columns"}, rows: [][]driver.Value{
			{int64(10), "Reports.X", "Sales, \"Q\"", "v", "", "Amount", "numeric(18,2)", int64(1), false, "d", true, "", "view_default_must_not_escape()", false},
			{int64(11), "public", "partial", "r", "", "safe", "timestamp with time zone", int64(2), false, "b", false, "", "CURRENT_TIMESTAMP", true},
			{int64(12), "public", "parent", "p", "", "tenant", "integer", int64(1), true, "b", false, "", "1", false},
			{int64(12), "public", "parent", "p", "", "id", "integer", int64(2), false, "d", true, "", "", false},
			{int64(13), "reporting", "child", "r", "", "parent_id", "integer", int64(1), false, "b", false, "", "", false},
			{int64(13), "reporting", "child", "r", "", "tenant", "integer", int64(2), false, "b", false, "", "", false},
			{int64(14), "other", "parent", "r", "", "id", "integer", int64(1), false, "b", false, "", "", false},
		}}
	case strings.Contains(q, "query_objects_constraints_v2"):
		if !strings.Contains(q, "WITH ORDINALITY") {
			return nil, errors.New("constraint order missing")
		}
		out = catalogRows{cols: []string{"oid", "name", "kind", "columns", "target_schema", "target_name", "target_columns", "on_delete", "on_update"}, rows: [][]driver.Value{
			{int64(11), "hidden_key", "p", "[\"hidden\"]", "", "", "[]", "", ""},
			{int64(12), "parent_pk", "p", "[\"tenant\",\"id\"]", "", "", "[]", "", ""},
			{int64(13), "parent_fk", "f", "[\"tenant\",\"parent_id\"]", "public", "parent", "[\"tenant\",\"id\"]", "CASCADE", "NO ACTION"},
			{int64(13), "hidden_target", "f", "[\"parent_id\"]", "private_base", "sales", "[\"id\"]", "", ""},
		}}
	case strings.Contains(q, "query_objects_indexes_v2"):
		for _, required := range []string{"indexprs IS NULL", "indpred IS NULL", "indisvalid", "indnkeyatts", "WITH ORDINALITY", "has_column_privilege"} {
			if !strings.Contains(q, required) {
				return nil, errors.New("unsafe index evidence: " + required)
			}
		}
		out = catalogRows{cols: []string{"oid", "name", "columns", "unique"}, rows: [][]driver.Value{
			{int64(11), "visible_idx", "[\"safe\"]", false}, {int64(11), "hidden_idx", "[\"hidden\"]", true},
			{int64(12), "composite_idx", "[\"tenant\",\"id\"]", true},
		}}
	case strings.Contains(q, "query_objects_definitions_v2"):
		if !strings.Contains(q, "information_schema.views") {
			return nil, errors.New("definition must use reader visibility")
		}
		out = catalogRows{cols: []string{"schema", "name", "definition"}, rows: [][]driver.Value{{"Reports.X", "Sales, \"Q\"", c.f.definition}}}
	default:
		return nil, errors.New("unexpected query")
	}
	if c.f.empty {
		out.rows = nil
	}
	out.fail = c.f.rowErr
	return &out, nil
}
func readFixture(t *testing.T, f *catalogFixture) ([]schemaobjects.Object, error) {
	t.Helper()
	db := sql.OpenDB(catalogConnector{f})
	defer db.Close()
	return readQueryObjectsCatalog(context.Background(), db)
}
func TestPostgresQueryObjectsCatalog(t *testing.T) {
	f := &catalogFixture{}
	got, err := readFixture(t, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d objects", len(got))
	}
	for _, o := range got {
		switch o.Metadata.Ref.Name {
		case "Sales, \"Q\"":
			if o.Metadata.Ref.Schema != "Reports.X" || o.Metadata.Ref.Kind != "view" || o.Metadata.CatalogOID != 10 || o.Columns[0].Type != "numeric(18,2)" || o.Columns[0].Nullable != nil || o.RowCount != nil || o.RowCountStatus != "unknown" {
				t.Fatalf("view structure lost: %#v", o)
			}
			if o.Metadata.DefinitionStatus != "unavailable" || o.Metadata.DefinitionFingerprint != "" {
				t.Fatal("NULL definition required or invented")
			}
		case "partial":
			if !o.Metadata.PartialColumns || len(o.Constraints) != 0 || len(o.Indexes) != 1 || o.Indexes[0].Name != "visible_idx" || o.Columns[0].Ordinal != 2 {
				t.Fatalf("column permission lost: %#v", o)
			}
		case "parent":
			if o.Metadata.Ref.Schema == "public" && (len(o.Constraints) != 1 || strings.Join(o.Constraints[0].Columns, ",") != "tenant,id" || strings.Join(o.Indexes[0].Columns, ",") != "tenant,id") {
				t.Fatalf("composite order lost: %#v", o)
			}
		case "child":
			if len(o.ForeignKeys) != 1 || strings.Join(o.ForeignKeys[0].Columns, ",") != "tenant,parent_id" || o.ForeignKeys[0].Target.Schema != "public" || strings.Join(o.ForeignKeys[0].TargetColumns, ",") != "tenant,id" {
				t.Fatalf("FK scope/order lost: %#v", o)
			}
		default:
			t.Fatal("unreadable base object leaked")
		}
	}
	if len(f.queries) != 4 {
		t.Fatalf("unexpected query count %d", len(f.queries))
	}
}
func TestPostgresQueryObjectsCatalogFailures(t *testing.T) {
	for _, stage := range []string{"columns_v2", "constraints_v2", "indexes_v2"} {
		t.Run(stage, func(t *testing.T) {
			got, err := readFixture(t, &catalogFixture{fail: stage})
			if err == nil || got != nil {
				t.Fatalf("failure accepted as snapshot: %v %v", got, err)
			}
		})
	}
	got, err := readFixture(t, &catalogFixture{rowErr: true})
	if err == nil || got != nil {
		t.Fatal("partial rows accepted")
	}
	got, err = readFixture(t, &catalogFixture{empty: true})
	if err != nil || len(got) != 0 {
		t.Fatalf("successful empty lost: %v %v", got, err)
	}
}
func TestPostgresQueryObjectsOptionalDefinition(t *testing.T) {
	for _, f := range []*catalogFixture{{fail: "definitions_v2"}, {definition: "SELECT secret FROM private_base.sales"}} {
		got, err := readFixture(t, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range got {
			if o.Metadata.Ref.Kind == schemaobjects.View {
				if f.definition == nil {
					if o.Metadata.DefinitionStatus != "unavailable" {
						t.Fatal(o.Metadata)
					}
				} else if o.Metadata.DefinitionStatus != "visible" || len(o.Metadata.DefinitionFingerprint) != 64 {
					t.Fatal(o.Metadata)
				}
				if strings.Contains(schemaobjects.Format([]schemaobjects.Object{o}), "private_base") {
					t.Fatal("SQL definition leaked")
				}
			}
		}
	}
}
func TestPostgresQueryObjectsCatalogCancelled(t *testing.T) {
	f := &catalogFixture{}
	db := sql.OpenDB(catalogConnector{f})
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := readQueryObjectsCatalog(ctx, db)
	if !errors.Is(err, context.Canceled) || got != nil || len(f.queries) != 0 {
		t.Fatalf("cancel lost %v %v", got, err)
	}
}

func TestPostgresQueryObjectsTableNullability(t *testing.T) {
	for _, tc := range []struct {
		name, kind, typeKind          string
		attNotNull, typeNotNull, want bool
		unknown                       bool
	}{
		{name: "domain_not_null", kind: "r", typeKind: "d", typeNotNull: true, want: false},
		{name: "nullable_domain", kind: "r", typeKind: "d", want: true},
		{name: "column_not_null", kind: "p", typeKind: "b", attNotNull: true, want: false},
		{name: "nullable_base", kind: "r", typeKind: "b", want: true},
		{name: "view_domain_unknown", kind: "v", typeKind: "d", typeNotNull: true, unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := postgresColumnNullable(tc.kind, tc.attNotNull, tc.typeKind, tc.typeNotNull)
			if tc.unknown {
				if got != nil {
					t.Fatal("view inferred nullability")
				}
			} else if got == nil || *got != tc.want {
				t.Fatalf("nullable=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestPostgresQueryObjectsCatalogDefaults(t *testing.T) {
	got, err := readFixture(t, &catalogFixture{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range got {
		switch o.Metadata.Ref.Name {
		case "parent":
			if o.Metadata.Ref.Schema == "public" && (o.Columns[1].Nullable == nil || *o.Columns[1].Nullable) {
				t.Fatal("catalog domain NOT NULL lost during scan", o.Columns)
			}
		case "partial":
			if o.Columns[0].DefaultValue != "CURRENT_TIMESTAMP" {
				t.Fatal("table default missing", o.Columns)
			}
		case "Sales, \"Q\"":
			if o.Columns[0].DefaultValue != "" {
				t.Fatal("view inherited default")
			}
		}
	}
}
