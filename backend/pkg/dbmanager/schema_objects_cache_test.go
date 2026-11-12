package dbmanager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func legacyMixedCache() *SchemaStorage {
	return &SchemaStorage{
		FullSchema: &SchemaInfo{Tables: map[string]TableSchema{"selected_table": {Name: "selected_table", Columns: map[string]ColumnInfo{"id": {Name: "id", Type: "integer"}}}}, Views: map[string]ViewSchema{"unselected_view": {Name: "unselected_view", Definition: "SELECT * FROM private_base.sales"}}},
		LLMSchema:  &LLMSchemaInfo{Tables: map[string]LLMTableInfo{"selected_table": {Name: "selected_table", Columns: []LLMColumnInfo{{Name: "id", Type: "integer"}}, ExampleRecords: []map[string]interface{}{{"id": "old_sample_secret"}}}}},
	}
}
func TestSchemaObjectsLegacyMixedUnknownNotReady(t *testing.T) {
	s := legacyMixedCache()
	if s.FullSchema.QueryObjectsReady() {
		t.Fatal("unknown legacy mixed schema is ready")
	}
}
func TestSchemaObjectsLegacyMixedFormatUnknown(t *testing.T) {
	s := legacyMixedCache()
	for _, out := range []string{formatSchemaForLLM(s.FullSchema), formatSchemaForLLMWithExamples(s)} {
		for _, secret := range []string{"selected_table", "unselected_view", "private_base.sales", "old_sample_secret"} {
			if strings.Contains(out, secret) {
				t.Fatalf("untrusted legacy metadata escaped formatter: %s", out)
			}
		}
	}
}
func TestSchemaObjectsCacheReadScopeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		access   schemaCacheAccess
		wantRead bool
		wantOK   bool
	}{
		{name: "restart_unknown", access: schemaCacheAccess{Key: "chat"}},
		{name: "unknown_type", access: schemaCacheAccess{Key: "chat", IdentityKnown: true}},
		{name: "unknown_identity", access: schemaCacheAccess{Key: "chat", DatabaseType: "mysql"}},
		{name: "postgres_old_mixed", access: schemaCacheAccess{Key: "chat:metadata-v2", DatabaseType: "postgresql", IdentityKnown: true}, wantRead: true},
		{name: "known_mysql_legacy", access: schemaCacheAccess{Key: "chat:connection:1", DatabaseType: "mysql", IdentityKnown: true}, wantRead: true, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := loadSchemaCache(context.Background(), tc.access, func(context.Context, string) (*SchemaStorage, error) { calls++; return legacyMixedCache(), nil })
			if (calls > 0) != tc.wantRead {
				t.Fatalf("reads=%d wantRead=%v", calls, tc.wantRead)
			}
			if (err == nil) != tc.wantOK {
				t.Fatalf("err=%v wantOK=%v", err, tc.wantOK)
			}
			if !tc.wantOK && got != nil {
				t.Fatal("rejected storage returned")
			}
			if tc.wantOK {
				if !got.FullSchema.QueryObjectsReady() || !strings.Contains(formatSchemaForLLMWithExamples(got), "old_sample_secret") {
					t.Fatal("known non-PG legacy behavior lost")
				}
			}
		})
	}
}

func TestSchemaObjectsCacheNamespaceAndLegacyProvenance(t *testing.T) {
	if got := newSchemaCacheAccess("chat", "postgresql", false); got.Key != "" || got.valid() {
		t.Fatal("unknown identity fell back", got)
	}
	if got := newSchemaCacheAccess("chat", "", true); got.Key != "" || got.valid() {
		t.Fatal("unknown type fell back", got)
	}
	for _, kind := range []string{"postgresql", "yugabytedb", "timescaledb"} {
		a := newSchemaCacheAccess("chat:connection:2", kind, true)
		if a.Key != "chat:connection:2:metadata-v2" || !a.valid() {
			t.Fatal("PG namespace not fixed", a)
		}
	}
	a := schemaCacheAccess{Key: "chat", DatabaseType: "postgresql", IdentityKnown: true}
	calls := 0
	if got, err := loadSchemaCache(context.Background(), a, func(context.Context, string) (*SchemaStorage, error) { calls++; return legacyMixedCache(), nil }); err == nil || got != nil || calls != 0 {
		t.Fatal("PG read old namespace")
	}
	for _, kind := range []string{"mysql", "starrocks", "clickhouse", "mongodb", "spreadsheet", "google_sheets"} {
		t.Run(kind, func(t *testing.T) {
			access := newSchemaCacheAccess("chat:connection:2", kind, true)
			loaded, err := loadSchemaCache(context.Background(), access, func(context.Context, string) (*SchemaStorage, error) { return legacyMixedCache(), nil })
			if err != nil || !loaded.FullSchema.QueryObjectsReady() {
				t.Fatal("legal legacy rejected", err)
			}
			data, _ := json.Marshal(loaded)
			var decoded SchemaStorage
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.FullSchema.QueryObjectsReady() {
				t.Fatal("legacy provenance persisted as authority")
			}
			if strings.Contains(formatSchemaForLLMWithExamples(&decoded), "old_sample_secret") {
				t.Fatal("JSON roundtrip restored untrusted sample")
			}
		})
	}
	var forged SchemaInfo
	if err := json.Unmarshal([]byte(`{"legacyDatabaseType":"mysql","legacy_database_type":"mysql","tables":{"t":{"name":"t"}}}`), &forged); err != nil {
		t.Fatal(err)
	}
	if forged.QueryObjectsReady() {
		t.Fatal("JSON forged legacy authority")
	}
}
