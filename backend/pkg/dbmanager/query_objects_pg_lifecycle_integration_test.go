//go:build object_pg_integration

package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"yanfachatbi/pkg/schemaobjects"
)

type liveCatalogRecorder struct {
	db      *sql.DB
	queries []string
}

func (r *liveCatalogRecorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.queries = append(r.queries, query)
	return r.db.QueryContext(ctx, query, args...)
}
func lifecyclePG(t *testing.T, name string) *sql.DB {
	t.Helper()
	raw := os.Getenv(name)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Path != "/query_objects_test" || u.User == nil {
		t.Fatalf("%s must explicitly identify the isolated loopback query_objects_test fixture", name)
	}
	db, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func lifecycleExec(t *testing.T, db *sql.DB, ctx context.Context, statement string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
}
func lifecycleObject(t *testing.T, snapshot schemaobjects.Snapshot, ref schemaobjects.Ref) schemaobjects.Object {
	t.Helper()
	for _, object := range snapshot.Objects {
		if object.Metadata.Ref == ref {
			return object
		}
	}
	t.Fatalf("expected current object %+v", ref)
	return schemaobjects.Object{}
}
func lifecycleNoBaseAccess(t *testing.T, ctx context.Context, reader *sql.DB) {
	t.Helper()
	var elevated, base, usage bool
	err := reader.QueryRowContext(ctx, "SELECT r.rolsuper,pg_catalog.has_table_privilege(c.oid,'SELECT'),pg_catalog.has_schema_privilege(n.oid,'USAGE') FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_namespace n JOIN pg_catalog.pg_class c ON c.relnamespace=n.oid WHERE r.rolname=current_user AND n.nspname='private_base' AND c.relname='sales'").Scan(&elevated, &base, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if elevated || base || usage {
		t.Fatal("reader fixture must not have elevated or private base access")
	}
}

// Exercises real PostgreSQL OIDs/column ACLs, while the production collector is
// restricted to catalog queries. Owner DDL is confined to this test's new schema.
func TestPostgresQueryObjectsRealLifecycleAndInvoker(t *testing.T) {
	owner, reader := lifecyclePG(t, "QUERY_OBJECTS_TEST_PG_ADMIN_URL"), lifecyclePG(t, "QUERY_OBJECTS_TEST_PG_READER_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	lifecycleNoBaseAccess(t, ctx, reader)
	schema := fmt.Sprintf("query_objects_lifecycle_%d", time.Now().UnixNano())
	qs := pq.QuoteIdentifier(schema)
	lifecycleExec(t, owner, ctx, "CREATE SCHEMA "+qs)
	t.Cleanup(func() {
		if !strings.HasPrefix(schema, "query_objects_lifecycle_") {
			t.Error("unsafe cleanup")
			return
		}
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := owner.ExecContext(cleanup, "DROP SCHEMA "+qs+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	ref := schemaobjects.Ref{Schema: schema, Name: "Sales, \"life.cycle\"", Kind: schemaobjects.View}
	qv := qs + "." + pq.QuoteIdentifier(ref.Name)
	create := "CREATE VIEW " + qv + " AS SELECT id,amount FROM private_base.sales"
	grant := "GRANT SELECT ON " + qv + " TO query_objects_reader"
	lifecycleExec(t, owner, ctx, create)
	lifecycleExec(t, owner, ctx, "GRANT USAGE ON SCHEMA "+qs+" TO query_objects_reader")
	lifecycleExec(t, owner, ctx, grant)
	scope := schemaobjects.Scope{OwnerID: "lifecycle-owner", ChatID: "lifecycle-chat", SourceKey: "isolated-reader", SelectionRevision: 1}
	var registry schemaobjects.Registry
	collect := func() schemaobjects.Snapshot {
		t.Helper()
		recorder := &liveCatalogRecorder{db: reader}
		started := time.Now()
		objects, err := readQueryObjectsCatalog(ctx, recorder)
		if err != nil {
			t.Fatal(err)
		}
		if len(recorder.queries) != 4 {
			t.Fatalf("expected four metadata queries, got %d", len(recorder.queries))
		}
		for _, query := range recorder.queries {
			if !strings.HasPrefix(query, "/* query_objects_") || (!strings.Contains(query, "pg_catalog.") && !strings.Contains(query, "information_schema.")) {
				t.Fatalf("collector issued non-catalog query %q", query)
			}
		}
		snap, err := schemaobjects.Reconcile(scope, registry, objects)
		if err != nil {
			t.Fatal(err)
		}
		registry = snap.Registry
		t.Logf("catalog_queries=%d objects=%d elapsed=%s business_queries=0", len(recorder.queries), len(objects), time.Since(started))
		return snap
	}
	first := lifecycleObject(t, collect(), ref)
	if first.Metadata.CatalogOID == 0 || first.Metadata.InstanceVersion == "" || first.Metadata.DefinitionStatus != "unavailable" || len(first.Columns) != 2 {
		t.Fatalf("unexpected view evidence %+v", first)
	}
	var total string
	if err := reader.QueryRowContext(ctx, "SELECT SUM(amount)::text FROM "+qv+" WHERE id=1").Scan(&total); err != nil || total != "12.50" {
		t.Fatalf("authorized synthetic aggregate=%q err=%v", total, err)
	}
	lifecycleExec(t, owner, ctx, "DROP VIEW "+qv)
	lifecycleExec(t, owner, ctx, create)
	lifecycleExec(t, owner, ctx, grant)
	rebuilt := lifecycleObject(t, collect(), ref)
	if rebuilt.Metadata.ObjectID != first.Metadata.ObjectID || rebuilt.Metadata.CatalogOID == first.Metadata.CatalogOID || rebuilt.Metadata.InstanceVersion == first.Metadata.InstanceVersion {
		t.Fatal("real drop/recreate did not advance instance while retaining logical ID")
	}
	lifecycleExec(t, owner, ctx, "REVOKE SELECT ON "+qv+" FROM query_objects_reader")
	lifecycleExec(t, owner, ctx, "GRANT SELECT(id) ON "+qv+" TO query_objects_reader")
	partial := lifecycleObject(t, collect(), ref)
	if len(partial.Columns) != 1 || partial.Columns[0].Name != "id" || !partial.Metadata.PartialColumns || partial.Metadata.InstanceVersion != rebuilt.Metadata.InstanceVersion || partial.Metadata.Fingerprint == rebuilt.Metadata.Fingerprint {
		t.Fatal("column revoke leaked old columns or changed live instance")
	}
	lifecycleExec(t, owner, ctx, "REVOKE SELECT(id) ON "+qv+" FROM query_objects_reader")
	absent := collect()
	for _, o := range absent.Objects {
		if o.Metadata.Ref == ref {
			t.Fatal("revoked view remained readable")
		}
	}
	if absent.Registry[first.Metadata.ObjectID].Present {
		t.Fatal("revoked object not tombstoned")
	}
	lifecycleExec(t, owner, ctx, grant)
	returned := lifecycleObject(t, collect(), ref)
	if returned.Metadata.CatalogOID != rebuilt.Metadata.CatalogOID || returned.Metadata.InstanceVersion == rebuilt.Metadata.InstanceVersion {
		t.Fatal("unobservable continuity incorrectly reused prior instance")
	}
	invoker := qs + ".invoker_view"
	lifecycleExec(t, owner, ctx, "CREATE VIEW "+invoker+" WITH (security_invoker=true) AS SELECT id,amount FROM private_base.sales")
	lifecycleExec(t, owner, ctx, "GRANT SELECT ON "+invoker+" TO query_objects_reader")
	lifecycleObject(t, collect(), schemaobjects.Ref{Schema: schema, Name: "invoker_view", Kind: schemaobjects.View})
	var id int
	err := reader.QueryRowContext(ctx, "SELECT id FROM "+invoker).Scan(&id)
	pgErr, ok := err.(*pq.Error)
	if !ok || string(pgErr.Code) != "42501" {
		t.Fatalf("invoker should retain database permission denial: %v", err)
	}
	lifecycleNoBaseAccess(t, ctx, reader)
}

// Separate owner evidence verifies optional definition digests. It is never
// substituted into the reader's catalog or used to supplement reader metadata.
func TestPostgresQueryObjectsRealDefinitionVisibility(t *testing.T) {
	owner, reader := lifecyclePG(t, "QUERY_OBJECTS_TEST_PG_ADMIN_URL"), lifecyclePG(t, "QUERY_OBJECTS_TEST_PG_READER_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := fmt.Sprintf("query_objects_definition_%d", time.Now().UnixNano())
	qs := pq.QuoteIdentifier(schema)
	view := qs + ".same_columns"
	lifecycleExec(t, owner, ctx, "CREATE SCHEMA "+qs)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := owner.ExecContext(cleanup, "DROP SCHEMA "+qs+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	lifecycleExec(t, owner, ctx, "CREATE VIEW "+view+" AS SELECT id,amount FROM private_base.sales")
	lifecycleExec(t, owner, ctx, "GRANT USAGE ON SCHEMA "+qs+" TO query_objects_reader")
	lifecycleExec(t, owner, ctx, "GRANT SELECT ON "+view+" TO query_objects_reader")
	ref := schemaobjects.Ref{Schema: schema, Name: "same_columns", Kind: schemaobjects.View}
	collect := func(db *sql.DB, identity string, previous schemaobjects.Registry) (schemaobjects.Object, schemaobjects.Registry) {
		t.Helper()
		objects, err := readQueryObjectsCatalog(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := schemaobjects.Reconcile(schemaobjects.Scope{OwnerID: identity, ChatID: "definition", SourceKey: identity}, previous, objects)
		if err != nil {
			t.Fatal(err)
		}
		return lifecycleObject(t, snap, ref), snap.Registry
	}
	visible, ownerRegistry := collect(owner, "isolated-owner", nil)
	hidden, readerRegistry := collect(reader, "isolated-reader", nil)
	if visible.Metadata.DefinitionFingerprint == "" || hidden.Metadata.DefinitionFingerprint != "" {
		t.Fatal("definition visibility fixture is invalid")
	}
	lifecycleExec(t, owner, ctx, "CREATE OR REPLACE VIEW "+view+" AS SELECT id,amount FROM private_base.sales WHERE id > -1")
	visible2, _ := collect(owner, "isolated-owner", ownerRegistry)
	hidden2, _ := collect(reader, "isolated-reader", readerRegistry)
	if !reflect.DeepEqual(visible.Columns, visible2.Columns) || visible.Metadata.CatalogOID != visible2.Metadata.CatalogOID || visible.Metadata.InstanceVersion != visible2.Metadata.InstanceVersion || visible.Metadata.DefinitionFingerprint == visible2.Metadata.DefinitionFingerprint || visible.Metadata.Fingerprint == visible2.Metadata.Fingerprint {
		t.Fatal("same-column visible definition change not detected")
	}
	if hidden2.Metadata.DefinitionStatus != "unavailable" || hidden2.Metadata.DefinitionFingerprint != "" || hidden.Metadata.Fingerprint != hidden2.Metadata.Fingerprint {
		t.Fatal("reader falsely gained invisible definition evidence")
	}
	lifecycleNoBaseAccess(t, ctx, reader)
	t.Log("visible definition change detected; invisible same-column change remains explicitly unobservable")
}
