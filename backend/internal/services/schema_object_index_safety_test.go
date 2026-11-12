package services

import (
	"context"
	"encoding/json"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
	"strings"
	"testing"
	"time"
)

func TestObjectIndexUnknownInstanceUsesCurrentStructure(t *testing.T) {
	original := indexSnapshot(t, 1)
	o := original.Objects[0]
	o.Metadata.CatalogOID = 0
	s, err := schemaobjects.Reconcile(original.Scope, nil, []schemaobjects.Object{o})
	if err != nil {
		t.Fatal(err)
	}
	i, _, _, _ := indexFixture(t, s)
	if err = i.Build(context.Background(), s, "kb-1", chunksFor(t, s)); err != nil {
		t.Fatal(err)
	}
	hits, err := i.Search(context.Background(), s, "kb-1", "Sales", 5)
	if err != nil || len(hits) != 1 || hits[0].Payload["needs_rebuild"] != true {
		t.Fatalf("unknown continuity reused stored text: %+v %v", hits, err)
	}
}
func TestObjectIndexConcurrentLaterBuildWins(t *testing.T) {
	s := indexSnapshot(t, 2)
	_, state, v, oldEmbedding := indexFixture(t, s)
	chunks := chunksFor(t, s)
	started, release := make(chan struct{}), make(chan struct{})
	oldEmbedding.before = func() { close(started); <-release }
	fresh := func(context.Context) (schemaobjects.Snapshot, string, error) { return s, "kb-1", nil }
	old, _ := NewObjectIndex(state, v, oldEmbedding, fresh)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- old.Build(ctx, s, "kb-1", chunks) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	newer, _ := NewObjectIndex(state, v, &indexEmbeddingFake{dimension: 3}, fresh)
	err := newer.Build(ctx, s, "kb-1", chunks)
	m, _ := newer.Status(ctx, s.Scope)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("late task replaced newer publication")
	}
	after, _ := newer.Status(ctx, s.Scope)
	if after.ActiveGeneration != m.ActiveGeneration || after.Status != "ready" {
		t.Fatalf("late task changed manifest %+v", after)
	}
	hits, err := newer.Search(ctx, s, "kb-1", "Sales", 5)
	if err != nil || len(hits) != 1 || hits[0].Payload["generation_id"] != m.ActiveGeneration {
		t.Fatalf("candidate leak %+v %v", hits, err)
	}
}
func TestObjectIndexFailedRebuildKeepsOnlyCompatibleActive(t *testing.T) {
	s := indexSnapshot(t, 1)
	i, state, v, e := indexFixture(t, s)
	chunks := chunksFor(t, s)
	if err := i.Build(context.Background(), s, "kb-1", chunks); err != nil {
		t.Fatal(err)
	}
	active := state.value.Manifest.ActiveGeneration
	v.partial = true
	if err := i.Build(context.Background(), s, "kb-1", chunks); err == nil {
		t.Fatal("partial rebuild succeeded")
	}
	if state.value.Manifest.ActiveGeneration != active || state.value.Manifest.Status != "failed" {
		t.Fatal("compatible old publication lost")
	}
	e.fail = false
	hits, err := i.Search(context.Background(), s, "kb-1", "Sales", 5)
	if err != nil || len(hits) != 1 || hits[0].Payload["generation_id"] != active {
		t.Fatalf("failed candidate visible %+v %v", hits, err)
	}
}
func TestObjectIndexCurrentScopeAndColumnsFenceHistoricalText(t *testing.T) {
	for _, mode := range []string{"columns", "kb", "scope", "recreated", "unavailable", "payload-scope"} {
		t.Run(mode, func(t *testing.T) {
			s := indexSnapshot(t, 2)
			i, _, v, e := indexFixture(t, s)
			enrich := map[string]ObjectIndexEnrichment{s.Objects[0].Metadata.ObjectID: {Description: "HISTORICAL BUSINESS PROSE", Fields: map[string]string{"column_1": "SECRET COLUMN"}}}
			chunks, err := BuildObjectSchemaChunks(s, "kb-1", enrich)
			if err != nil {
				t.Fatal(err)
			}
			if err = i.Build(context.Background(), s, "kb-1", chunks); err != nil {
				t.Fatal(err)
			}
			current := s
			kb := "kb-1"
			switch mode {
			case "columns":
				o := s.Objects[0]
				o.Columns = o.Columns[:1]
				current, err = schemaobjects.Reconcile(s.Scope, s.Registry, []schemaobjects.Object{o})
			case "kb":
				kb = "kb-2"
			case "scope":
				scope := s.Scope
				scope.SelectionRevision++
				current, err = schemaobjects.Reconcile(scope, s.Registry, s.Objects)
			case "recreated":
				o := s.Objects[0]
				o.Metadata.CatalogOID++
				current, err = schemaobjects.Reconcile(s.Scope, s.Registry, []schemaobjects.Object{o})
			case "unavailable":
				e.fail = true
				v.failSearch = true
				v.failHybrid = true
			case "payload-scope":
				v.points[0].Payload["owner_id"] = "another"
			}
			if err != nil {
				t.Fatal(err)
			}
			hits, err := i.Search(context.Background(), current, kb, "Sales", 5)
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(hits)
			if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "HISTORICAL") {
				t.Fatalf("old text leaked: %s", wire)
			}
			if mode == "columns" && strings.Contains(string(wire), "column_1") {
				t.Fatalf("revoked column leaked: %s", wire)
			}
			if mode == "payload-scope" && len(hits) != 0 {
				t.Fatal("foreign scope returned")
			}
			if mode != "payload-scope" && len(hits) == 0 {
				t.Fatal("current structure missing")
			}
		})
	}
}
func TestObjectIndexGroupingKeepsSameNameObjectsAndUnknownRowsNeutral(t *testing.T) {
	s := indexSnapshot(t, 55)
	view := s.Objects[0]
	table := view
	table.Metadata.Ref.Schema = "public"
	table.Metadata.Ref.Kind = schemaobjects.Table
	table.Metadata.CatalogOID = 99
	rows := int64(500)
	table.RowCount = &rows
	table.RowCountStatus = "known"
	s, err := schemaobjects.Reconcile(s.Scope, s.Registry, []schemaobjects.Object{view, table})
	if err != nil {
		t.Fatal(err)
	}
	i, _, v, _ := indexFixture(t, s)
	if err = i.Build(context.Background(), s, "kb-1", chunksFor(t, s)); err != nil {
		t.Fatal(err)
	}
	v.hits = []vectordb.SearchResult{}
	for _, kind := range []string{"view", "table"} {
		for _, p := range v.points {
			if p.Payload["object_type"] == kind {
				v.hits = append(v.hits, vectordb.SearchResult{ID: p.ID, Score: 0.8, Payload: p.Payload})
			}
		}
	}
	// Duplicate point must not consume the object's part budget.
	v.hits = append(v.hits, v.hits[0])
	hits, err := i.Search(context.Background(), s, "kb-1", "Sales", 2)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[any]bool{}
	seen := map[string]bool{}
	for _, h := range hits {
		ids[h.Payload["object_id"]] = true
		if seen[h.ID] {
			t.Fatal("duplicate point")
		}
		seen[h.ID] = true
	}
	if len(ids) != 2 || hits[0].Payload["object_type"] != "view" {
		t.Fatalf("name grouping or unknown-row penalty: %v", hits)
	}
}
func TestObjectIndexRelationshipsAndEffectiveEnrichment(t *testing.T) {
	s := indexSnapshot(t, 2)
	source := s.Objects[0]
	source.Metadata.Ref.Kind = schemaobjects.Table
	source.Metadata.Ref.Name = "orders"
	target := source
	target.Metadata.CatalogOID = 77
	target.Metadata.Ref.Name = "accounts"
	source.ForeignKeys = []schemaobjects.ForeignKey{{Name: "accounts_fk", Columns: []string{"column_0"}, Target: target.Metadata.Ref, TargetColumns: []string{"column_0"}}}
	s, err := schemaobjects.Reconcile(s.Scope, nil, []schemaobjects.Object{source, target})
	if err != nil {
		t.Fatal(err)
	}
	id := schemaobjects.ObjectID(s.Scope.SourceKey, source.Metadata.Ref)
	chunks, err := BuildObjectSchemaChunks(s, "kb-1", map[string]ObjectIndexEnrichment{id: {Description: "approved description", Fields: map[string]string{"column_0": "account key"}}})
	if err != nil {
		t.Fatal(err)
	}
	relations := 0
	for _, c := range chunks {
		if c.Metadata["type"] == "relationship" {
			relations++
			if !strings.Contains(c.Content, "accounts_fk") {
				t.Fatal("lost relation evidence")
			}
		}
	}
	if relations != 1 {
		t.Fatalf("relation count %d", relations)
	}
	i, _, _, e := indexFixture(t, s)
	if err = i.Build(context.Background(), s, "kb-1", chunks); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(e.inputs, "\n"), "account key") {
		t.Fatal("effective KB not embedded")
	}
	if _, err = BuildObjectSchemaChunks(s, "kb-1", map[string]ObjectIndexEnrichment{id: {Fields: map[string]string{"hidden_column": "SECRET"}}}); err == nil {
		t.Fatal("out-of-range KB field accepted")
	}
	// Reconcile itself removes FK evidence when the target leaves the projection.
	selected, err := schemaobjects.Reconcile(s.Scope, s.Registry, []schemaobjects.Object{source})
	if err != nil {
		t.Fatal(err)
	}
	current := chunksFor(t, selected)
	for _, c := range current {
		if c.Metadata["type"] == "relationship" || strings.Contains(c.Content, "accounts_fk") {
			t.Fatal("unselected relationship survived")
		}
	}
}
