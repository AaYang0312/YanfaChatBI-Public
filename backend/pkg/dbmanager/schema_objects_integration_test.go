package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/sourcegate"
	"strings"
	"testing"
)

// Full-package tests: cross-compiled on Windows, must be EXECUTED on Linux.
type objectTestExecutor struct {
	DBExecutor
	db *sql.DB
}

func (e objectTestExecutor) GetDB() *sql.DB { return e.db }
func TestPostgresQueryObjectsDriverAndLegacyEntrypoints(t *testing.T) {
	f := &catalogFixture{}
	db := sql.OpenDB(catalogConnector{f})
	defer db.Close()
	e := objectTestExecutor{db: db}
	d := &PostgresDriver{}
	all, err := d.GetQueryObjects(context.Background(), e)
	if err != nil || len(all) != 5 {
		t.Fatalf("candidate catalog %v %v", all, err)
	}
	for _, fetch := range []func(context.Context, DBExecutor, []string) (*SchemaInfo, error){d.GetSchema, (&PostgresSchemaFetcher{db: e}).GetSchema} {
		s, err := fetch(context.Background(), e, []string{"ALL"})
		if err != nil || len(s.Views) != 0 || len(s.Tables) != 4 {
			t.Fatalf("legacy widened %v %+v", err, s)
		}
		s, err = fetch(context.Background(), e, []string{"partial"})
		if err != nil || len(s.Tables) != 1 || len(s.Views) != 0 {
			t.Fatalf("selection widened %v %+v", err, s)
		}
	}
}
func TestSchemaObjectsManagerFormattingAndNoExamples(t *testing.T) {
	s := schemaFromQueryObjects(objectFixture())
	m := &SchemaManager{dbManager: &Manager{connections: map[string]*Connection{"chat": {ChatID: "chat", UserID: "owner", Status: StatusConnected, Config: ConnectionConfig{Type: "postgresql"}}}}, schemaCache: map[string]*SchemaInfo{"chat:metadata-v2": s}}
	for _, format := range []string{m.FormatSchemaForLLM(s), m.FormatSchemaForLLMWithExamples(&SchemaStorage{FullSchema: s})} {
		if !strings.Contains(format, "View") || !strings.Contains(format, "numeric(18,2)") || !strings.Contains(format, "nullable=unknown") {
			t.Fatal(format)
		}
	}
	llm, err := m.createLLMSchemaWithExamples(context.Background(), s, "postgresql", nil)
	if err != nil || llm == nil {
		t.Fatalf("metadata attempted sampling: %v", err)
	}
	if !m.IsSchemaReady(context.Background(), "chat") {
		t.Fatal("view-only not ready")
	}
	legacy := &SchemaInfo{Views: map[string]ViewSchema{"v": {Name: "v", Definition: "SELECT sensitive_base"}}}
	if strings.Contains(m.FormatSchemaForLLMWithExamples(&SchemaStorage{FullSchema: legacy, LLMSchema: &LLMSchemaInfo{Tables: map[string]LLMTableInfo{}}}), "sensitive_base") {
		t.Fatal("legacy definition leaked")
	}
	s.MetadataVersion = 1
	if m.IsSchemaReady(context.Background(), "chat") {
		t.Fatal("old version ready")
	}
}
func TestSchemaObjectsManagerViewDiff(t *testing.T) {
	a := schemaFromQueryObjects(objectFixture())
	b := schemaFromQueryObjects(nil)
	m := &SchemaManager{}
	for _, compare := range []func(*SchemaInfo, *SchemaInfo) (*SchemaDiff, bool){m.CompareSchemas, m.CompareSchemasDetailed} {
		d, changed := compare(a, b)
		if !changed || len(d.RemovedViews) != 1 {
			t.Fatal("view diff lost")
		}
	}
}
func TestQueryObjectsSourceGateRejectsUnadmitted(t *testing.T) {
	// The source executor has no usable pool and rejects missing access before
	// opening SQL. GetDB bypass would return a generic pool error instead.
	e := &sourceExecutor{ctx: context.Background(), manager: &Manager{}, config: ConnectionConfig{SourceChat: &models.Chat{SourceID: "isolated", ERPAssociated: true}}}
	if _, err := (&PostgresDriver{}).GetQueryObjects(context.Background(), e); !errors.Is(err, sourcegate.ErrMissing) {
		t.Fatalf("source gate bypassed: %v", err)
	}
	// Cancellation must stop before any pool access, including standalone calls.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&PostgresDriver{}).GetQueryObjects(ctx, e); err != context.Canceled {
		t.Fatalf("cancelled source admitted: %v", err)
	}
}
func TestSchemaObjectsSelectionFiltersCompositeRelationships(t *testing.T) {
	f := &catalogFixture{}
	objects, err := readFixture(t, f)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := schemaobjects.Select(objects, schemaobjects.Selection{Version: 2, Mode: "explicit", Objects: []schemaobjects.Ref{{Schema: "reporting", Name: "child", Kind: schemaobjects.Table}}})
	if err != nil {
		t.Fatal(err)
	}
	s := schemaFromQueryObjects(selected)
	for _, o := range s.QueryObjects("source") {
		if len(o.ForeignKeys) != 0 {
			t.Fatal("unselected target leaked")
		}
	}
}

func TestSchemaObjectsPublicCacheRejectsMissingConnection(t *testing.T) {
	ctx := context.Background()
	old := legacyMixedCache()
	for _, m := range []*SchemaManager{{schemaCache: map[string]*SchemaInfo{"chat": old.FullSchema}}, {dbManager: &Manager{connections: map[string]*Connection{}}, schemaCache: map[string]*SchemaInfo{"chat": old.FullSchema}}} {
		if m.IsSchemaReady(ctx, "chat") {
			t.Fatal("restart restored legacy ready")
		}
		if got, err := m.GetStoredSchemaInfo(ctx, "chat"); err == nil || got != nil {
			t.Fatal("unknown connection returned cached schema")
		}
		if got, err := m.GetStoredSchemaStorage(ctx, "chat"); err == nil || got != nil {
			t.Fatal("unknown connection returned cached samples")
		}
		if out := m.FormatSchemaForLLMWithExamples(old); strings.Contains(out, "old_sample_secret") || strings.Contains(out, "private_base") {
			t.Fatal("public formatter leaked", out)
		}
	}
}
