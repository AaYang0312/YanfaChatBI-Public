package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"yanfachatbi/pkg/schemaobjects"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Opt-in, READ-ONLY verification against the manually provisioned synthetic
// fixture. This test never creates roles, runs GRANT/REVOKE/DDL or changes roles.
// Supply an actual reader login inheriting query_objects_reader and no elevated
// roles. Do not point this at bi_agent or production. No test runner was available
// during T02 implementation; cross-compilation is not execution of this test.
func TestPostgresQueryObjectsRealReader(t *testing.T) {
	raw := os.Getenv("QUERY_OBJECTS_TEST_PG_READER_URL")
	if raw == "" {
		t.Skip("UNVERIFIED: isolated query_objects_test reader URL required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || strings.TrimPrefix(u.Path, "/") != "query_objects_test" || u.User == nil {
		t.Fatal("expected isolated query_objects_test PostgreSQL reader URL")
	}
	db, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var super, base, schemaUsage bool
	if err = db.QueryRowContext(ctx, `SELECT r.rolsuper,pg_catalog.has_table_privilege(c.oid,'SELECT'),pg_catalog.has_schema_privilege(n.oid,'USAGE') FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_namespace n JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid WHERE r.rolname=current_user AND n.nspname='private_base' AND c.relname='sales'`).Scan(&super, &base, &schemaUsage); err != nil {
		t.Fatal(err)
	}
	if super || base || schemaUsage {
		t.Fatal("fixture reader unexpectedly has base-table access")
	}
	acl := func() []string {
		var out []string
		err := catalogRowsEach(ctx, db, `SELECT n.nspname,c.relname,COALESCE(c.relacl::text,''),COALESCE(n.nspacl::text,'') FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','reporting','Reports.X','private_base') ORDER BY n.nspname,c.relname`, func(rows *sql.Rows) error {
			var ns, name, ca, na string
			if err := rows.Scan(&ns, &name, &ca, &na); err != nil {
				return err
			}
			out = append(out, fmt.Sprintf("%q:%q:%s:%s", ns, name, ca, na))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := acl()
	got, err := (&PostgresDriver{}).GetQueryObjects(ctx, objectTestExecutor{db: db})
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[schemaobjects.Ref]schemaobjects.Object{}
	for _, o := range got {
		byRef[o.Metadata.Ref] = o
		if o.Metadata.Ref.Schema == "private_base" || o.Metadata.Ref.Name == "materialized_sales" {
			t.Fatalf("ineligible object %+v", o.Metadata.Ref)
		}
	}
	view, ok := byRef[schemaobjects.Ref{Schema: "Reports.X", Name: "Sales, \"Q\"", Kind: schemaobjects.View}]
	if !ok || len(view.Columns) != 1 || view.Columns[0].Name != "Amount" || view.Columns[0].Type != "numeric(18,2)" || view.Columns[0].Nullable != nil || view.Metadata.DefinitionStatus != "unavailable" {
		t.Fatalf("view-only permission metadata wrong: %+v", view)
	}
	partial, ok := byRef[schemaobjects.Ref{Schema: "public", Name: "partial", Kind: schemaobjects.Table}]
	if !ok || len(partial.Columns) != 1 || partial.Columns[0].Name != "safe" || !partial.Metadata.PartialColumns || len(partial.Indexes) != 1 || partial.Indexes[0].Name != "partial_safe_idx" || len(partial.Constraints) != 0 {
		t.Fatalf("column permissions not enforced: %+v", partial)
	}
	parent := byRef[schemaobjects.Ref{Schema: "public", Name: "parent", Kind: schemaobjects.Table}]
	for _, idx := range parent.Indexes {
		if strings.Contains(idx.Name, "expression") || strings.Contains(idx.Name, "predicate") {
			t.Fatal("incomplete index evidence", idx)
		}
		if idx.Name == "parent_include_idx" && (len(idx.Columns) != 1 || idx.Columns[0] != "tenant") {
			t.Fatal("INCLUDE mistaken for key", idx)
		}
	}
	child := byRef[schemaobjects.Ref{Schema: "public", Name: "child", Kind: schemaobjects.Table}]
	if len(child.ForeignKeys) != 1 || strings.Join(child.ForeignKeys[0].Columns, ",") != "tenant,parent_id" || strings.Join(child.ForeignKeys[0].TargetColumns, ",") != "tenant,id" {
		t.Fatal("composite FK lost", child.ForeignKeys)
	}
	domain := byRef[schemaobjects.Ref{Schema: "public", Name: "default_domain", Kind: schemaobjects.Table}]
	for _, c := range domain.Columns {
		if c.Name == "required" && (c.Nullable == nil || *c.Nullable) {
			t.Fatal("domain NOT NULL reported nullable", c)
		}
		if c.Name == "quantity" && c.DefaultValue != "1" {
			t.Fatal("default missing", c)
		}
	}
	if len(domain.Columns) != 3 {
		t.Fatal("missing domain/default table")
	}
	domainView := byRef[schemaobjects.Ref{Schema: "reporting", Name: "domain_view", Kind: schemaobjects.View}]
	if len(domainView.Columns) != 2 {
		t.Fatal("missing domain view")
	}
	for _, c := range domainView.Columns {
		if c.Nullable != nil || c.DefaultValue != "" {
			t.Fatal("view inherited default/domain non-null", c)
		}
	}
	// Explicit synthetic data queries belong to the test, never to collection.
	rows, err := db.QueryContext(ctx, `SELECT id,amount FROM reporting.sales`)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `SELECT id FROM private_base.sales`)
	if rows != nil {
		rows.Close()
	}
	if err == nil {
		t.Fatal("reader unexpectedly queried base table")
	}
	if !reflect.DeepEqual(before, acl()) {
		t.Fatal("collection changed ACL snapshot")
	}
}
