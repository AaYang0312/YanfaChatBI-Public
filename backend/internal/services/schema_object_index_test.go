package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var indexTestErr = errors.New("fixture failure")

type indexStateFake struct {
	mu            sync.Mutex
	value         models.State
	rejectPublish bool
}

func (s *indexStateFake) Read(ctx context.Context, _ schemaobjects.Scope) (*models.State, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.value
	v.Registry = cloneRegistry(s.value.Registry)
	return &v, nil
}
func cloneRegistry(r schemaobjects.Registry) schemaobjects.Registry {
	out := schemaobjects.Registry{}
	for k, v := range r {
		out[k] = v
	}
	return out
}
func (s *indexStateFake) CompareAndSwap(ctx context.Context, scope schemaobjects.Scope, rev int64, next models.State) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value.Revision != rev || next.Revision != rev+1 || next.Scope != scope || (s.rejectPublish && next.Manifest.Status == "ready") {
		return indexTestErr
	}
	next.Registry = cloneRegistry(next.Registry)
	s.value = next
	return nil
}

type indexEmbeddingFake struct {
	fail      bool
	dimension int
	inputs    []string
	before    func()
}

func (e *indexEmbeddingFake) GetDimension() int { return e.dimension }
func (e *indexEmbeddingFake) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	e.inputs = append(e.inputs, texts...)
	if e.before != nil {
		e.before()
	}
	if e.fail {
		return nil, indexTestErr
	}
	out := make([][]float32, len(texts))
	for n := range out {
		out[n] = []float32{1, 0, 0}
	}
	return out, ctx.Err()
}
func (e *indexEmbeddingFake) EmbedQuery(ctx context.Context, _ string) ([]float32, error) {
	if e.fail {
		return nil, indexTestErr
	}
	return []float32{1, 0, 0}, ctx.Err()
}

type indexVectorFake struct {
	points                                                                                               []vectordb.VectorPoint
	partial, missing, duplicate, wrongDimension, badPart, failEnsure, failScroll, failHybrid, failSearch bool
	deleted                                                                                              [][]vectordb.FilterCondition
	hybrid                                                                                               vectordb.HybridSearchRequest
	search                                                                                               vectordb.SearchRequest
	onScroll                                                                                             func()
	hits                                                                                                 []vectordb.SearchResult
}

func (v *indexVectorFake) Ensure(context.Context, int) error {
	if v.failEnsure {
		return indexTestErr
	}
	return nil
}
func (v *indexVectorFake) Upsert(_ context.Context, p []vectordb.VectorPoint) error {
	if v.partial {
		v.points = append(v.points, p[0])
		return indexTestErr
	}
	v.points = append(v.points, p...)
	return nil
}
func payloadMatches(p map[string]interface{}, f map[string]string) bool {
	for k, s := range f {
		if p[k] != s {
			return false
		}
	}
	return true
}
func filtersMap(f []vectordb.FilterCondition) map[string]string {
	m := map[string]string{}
	for _, p := range f {
		m[p.Key] = p.Value
	}
	return m
}
func (v *indexVectorFake) Scroll(_ context.Context, f []vectordb.FilterCondition) ([]vectordb.VectorPoint, error) {
	if v.onScroll != nil {
		v.onScroll()
	}
	if v.failScroll {
		return nil, indexTestErr
	}
	var out []vectordb.VectorPoint
	for _, p := range v.points {
		if payloadMatches(p.Payload, filtersMap(f)) {
			b, _ := json.Marshal(p)
			var c vectordb.VectorPoint
			_ = json.Unmarshal(b, &c)
			out = append(out, c)
		}
	}
	if len(out) > 0 {
		if v.missing {
			out = out[1:]
		} else if v.duplicate {
			out = append(out, out[0])
		} else if v.wrongDimension {
			out[0].Vector = []float32{1}
		} else if v.badPart {
			out[0].Payload["part_index"] = "999"
		}
	}
	return out, nil
}
func (v *indexVectorFake) results(f map[string]string) []vectordb.SearchResult {
	if v.hits != nil {
		return v.hits
	}
	var out []vectordb.SearchResult
	for _, p := range v.points {
		if payloadMatches(p.Payload, f) {
			out = append(out, vectordb.SearchResult{ID: p.ID, Score: 0.9, Payload: p.Payload})
		}
	}
	return out
}
func (v *indexVectorFake) HybridSearch(_ context.Context, r vectordb.HybridSearchRequest) ([]vectordb.SearchResult, error) {
	v.hybrid = r
	if v.failHybrid {
		return nil, indexTestErr
	}
	return v.results(r.Filter), nil
}
func (v *indexVectorFake) Search(_ context.Context, r vectordb.SearchRequest) ([]vectordb.SearchResult, error) {
	v.search = r
	if v.failSearch {
		return nil, indexTestErr
	}
	return v.results(r.Filter), nil
}
func (v *indexVectorFake) DeleteGeneration(_ context.Context, f []vectordb.FilterCondition) error {
	v.deleted = append(v.deleted, f)
	return nil
}
func indexSnapshot(t *testing.T, cols int) schemaobjects.Snapshot {
	t.Helper()
	o := schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "reports", Name: "Sales.View", Kind: schemaobjects.View}, CatalogOID: 12, DefinitionStatus: "unavailable"}, RowCountStatus: "unknown"}
	for n := 0; n < cols; n++ {
		o.Columns = append(o.Columns, schemaobjects.Column{Name: fmt.Sprintf("column_%d", n), Type: "numeric(12,2)", Ordinal: n + 1})
	}
	s, e := schemaobjects.Reconcile(schemaobjects.Scope{OwnerID: "owner", ChatID: "chat", SourceKey: "source", BindingRevision: 2, ConnectionRevision: 3, SelectionRevision: 4}, nil, []schemaobjects.Object{o})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func indexFixture(t *testing.T, s schemaobjects.Snapshot) (ObjectIndex, *indexStateFake, *indexVectorFake, *indexEmbeddingFake) {
	t.Helper()
	state := &indexStateFake{value: models.State{Scope: s.Scope, Registry: s.Registry}}
	v := &indexVectorFake{}
	e := &indexEmbeddingFake{dimension: 3}
	i, err := NewObjectIndex(state, v, e, func(context.Context) (schemaobjects.Snapshot, string, error) { return s, "kb-1", nil })
	if err != nil {
		t.Fatal(err)
	}
	return i, state, v, e
}
func chunksFor(t *testing.T, s schemaobjects.Snapshot) []SchemaChunk {
	t.Helper()
	c, e := BuildObjectSchemaChunks(s, "kb-1", nil)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestObjectIndexPublishesVerifiedGenerationAndRestarts(t *testing.T) {
	s := indexSnapshot(t, 55)
	i, state, v, e := indexFixture(t, s)
	chunks := chunksFor(t, s)
	if len(chunks) < 2 {
		t.Fatal("wide object must have multiple parts")
	}
	e.before = func() {
		m, _ := i.Status(context.Background(), s.Scope)
		if m.Status != "building" || m.BuildingGeneration == "" || m.ActiveGeneration != "" {
			t.Fatalf("not durably building before embedding: %+v", m)
		}
	}
	if err := i.Build(context.Background(), s, "kb-1", chunks); err != nil {
		t.Fatal(err)
	}
	m, _ := i.Status(context.Background(), s.Scope)
	if _, err := uuid.Parse(m.ActiveGeneration); err != nil {
		t.Fatal(err)
	}
	if m.Status != "ready" || m.BuildingGeneration != "" || m.ExpectedPoints != len(v.points) {
		t.Fatalf("manifest %+v", m)
	}
	for _, p := range v.points {
		if _, err := uuid.Parse(p.ID); err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(p.Payload)
		if !strings.Contains(string(wire), "\"object_type\":\"view\"") {
			t.Fatalf("type missing: %s", wire)
		}
	}
	restarted, err := NewObjectIndex(state, v, e, func(context.Context) (schemaobjects.Snapshot, string, error) { return s, "kb-1", nil })
	if err != nil {
		t.Fatal(err)
	}
	hits, err := restarted.Search(context.Background(), s, "kb-1", "Sales", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("restart hits %v %v", hits, err)
	}
	if v.hybrid.Filter["generation_id"] != m.ActiveGeneration || v.hybrid.TextField != "content" || len(v.hybrid.ExtraTextLegs) == 0 {
		t.Fatalf("hybrid lost legs: %+v", v.hybrid)
	}
}
func TestObjectIndexFailuresNeverPublish(t *testing.T) {
	for _, mode := range []string{"embedding", "partial", "missing", "duplicate", "dimension", "part", "ensure", "scroll", "cas"} {
		t.Run(mode, func(t *testing.T) {
			s := indexSnapshot(t, 55)
			i, state, v, e := indexFixture(t, s)
			chunks := chunksFor(t, s)
			switch mode {
			case "embedding":
				e.fail = true
			case "partial":
				v.partial = true
			case "missing":
				v.missing = true
			case "duplicate":
				v.duplicate = true
			case "dimension":
				v.wrongDimension = true
			case "part":
				v.badPart = true
			case "ensure":
				v.failEnsure = true
			case "scroll":
				v.failScroll = true
			case "cas":
				state.rejectPublish = true
			}
			if err := i.Build(context.Background(), s, "kb-1", chunks); err == nil {
				t.Fatal("failed candidate published")
			}
			m, _ := i.Status(context.Background(), s.Scope)
			if m.ActiveGeneration != "" || m.Status != "failed" || m.FailureCode == "" {
				t.Fatalf("unsafe failure %+v", m)
			}
			if len(v.deleted) > 0 {
				t.Fatal("deleted before successful publication")
			}
		})
	}
}
func TestObjectIndexFreshCallbackThenRereadCAS(t *testing.T) {
	s := indexSnapshot(t, 1)
	_, state, v, e := indexFixture(t, s)
	i, err := NewObjectIndex(state, v, e, func(ctx context.Context) (schemaobjects.Snapshot, string, error) {
		current, _ := state.Read(ctx, s.Scope)
		current.Revision++
		if err := state.CompareAndSwap(ctx, s.Scope, current.Revision-1, *current); err != nil {
			t.Fatal(err)
		}
		return s, "kb-1", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = i.Build(context.Background(), s, "kb-1", chunksFor(t, s)); err != nil {
		t.Fatalf("own callback revision incorrectly rejected: %v", err)
	}
	if state.value.Revision != 3 || state.value.Manifest.Status != "ready" {
		t.Fatalf("state %+v", state.value)
	}
}
func TestObjectIndexRejectsFreshChangesAndLateTasks(t *testing.T) {
	for _, mode := range []string{"kb", "columns", "scope", "new-generation", "callback-error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s := indexSnapshot(t, 2)
			_, state, v, e := indexFixture(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			i, _ := NewObjectIndex(state, v, e, func(context.Context) (schemaobjects.Snapshot, string, error) {
				fresh := s
				kb := "kb-1"
				switch mode {
				case "kb":
					kb = "kb-2"
				case "columns":
					o := s.Objects[0]
					o.Columns = o.Columns[:1]
					fresh, _ = schemaobjects.Reconcile(s.Scope, s.Registry, []schemaobjects.Object{o})
				case "scope":
					fresh.Scope.SelectionRevision++
				case "new-generation":
					state.mu.Lock()
					state.value.Revision++
					state.value.Manifest.BuildingGeneration = "newer"
					state.mu.Unlock()
				case "callback-error":
					return fresh, kb, indexTestErr
				case "cancel":
					cancel()
				}
				return fresh, kb, nil
			})
			if err := i.Build(ctx, s, "kb-1", chunksFor(t, s)); err == nil {
				t.Fatal("outdated task published")
			}
			if state.value.Manifest.ActiveGeneration != "" {
				t.Fatal("outdated generation became active")
			}
			if mode == "new-generation" && state.value.Manifest.BuildingGeneration != "newer" {
				t.Fatal("late failure clobbered newer task")
			}
		})
	}
}
func TestObjectIndexChunksCannotExpandOrForgeSnapshot(t *testing.T) {
	for _, mode := range []string{"missing-part", "duplicate-part", "foreign-object", "stale-text", "stale-snapshot", "foreign-column", "foreign-kb"} {
		t.Run(mode, func(t *testing.T) {
			s := indexSnapshot(t, 55)
			i, _, _, e := indexFixture(t, s)
			c := chunksFor(t, s)
			switch mode {
			case "missing-part":
				c = c[:1]
			case "duplicate-part":
				c = append(c, c[0])
			case "foreign-object":
				c[0].Metadata["object_id"] = "not-selected"
			case "stale-text":
				c[0].Content += " secret_column"
			case "stale-snapshot":
				s.Objects[0].Columns[0].Name = "changed-without-fingerprint"
			case "foreign-column":
				c[0].ColumnNames = append(c[0].ColumnNames, "secret")
			case "foreign-kb":
				c[0].Metadata["kb_revision"] = "kb-old"
			}
			if err := i.Build(context.Background(), s, "kb-1", c); err == nil {
				t.Fatal("invalid model input accepted")
			}
			if len(e.inputs) != 0 {
				t.Fatal("unvalidated content reached embedding")
			}
		})
	}
}
func TestObjectIndexEmptyGenerationRetiresOldPoints(t *testing.T) {
	s := indexSnapshot(t, 1)
	i, state, v, e := indexFixture(t, s)
	if err := i.Build(context.Background(), s, "kb-1", chunksFor(t, s)); err != nil {
		t.Fatal(err)
	}
	old := state.value.Manifest.ActiveGeneration
	empty, err := schemaobjects.Reconcile(s.Scope, s.Registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.value.Registry = empty.Registry
	next, _ := NewObjectIndex(state, v, e, func(context.Context) (schemaobjects.Snapshot, string, error) { return empty, "kb-1", nil })
	if err = next.Build(context.Background(), empty, "kb-1", chunksFor(t, empty)); err != nil {
		t.Fatal(err)
	}
	if state.value.Manifest.ActiveGeneration == old || state.value.Manifest.ExpectedPoints != 0 {
		t.Fatal("empty generation not published")
	}
	hits, err := next.Search(context.Background(), empty, "kb-1", "Sales", 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("old points visible: %v %v", hits, err)
	}
	for _, f := range v.deleted {
		m := filtersMap(f)
		if m["generation_id"] != old || m["chat_id"] != "chat" || m["owner_id"] != "owner" {
			t.Fatalf("unsafe cleanup: %v", f)
		}
	}
}
func TestObjectIndexFallbackUsesSameActiveGeneration(t *testing.T) {
	s := indexSnapshot(t, 1)
	i, state, v, _ := indexFixture(t, s)
	if err := i.Build(context.Background(), s, "kb-1", chunksFor(t, s)); err != nil {
		t.Fatal(err)
	}
	v.failHybrid = true
	hits, err := i.Search(context.Background(), s, "kb-1", "Sales", 3)
	if err != nil || len(hits) != 1 {
		t.Fatalf("fallback %v %v", hits, err)
	}
	if !reflect.DeepEqual(v.hybrid.Filter, v.search.Filter) || v.search.Filter["generation_id"] != state.value.Manifest.ActiveGeneration {
		t.Fatal("fallback changed visibility fence")
	}
}
func TestObjectIndexStalePayloadDegradesToCurrentStructure(t *testing.T) {
	for _, field := range []string{"object_instance_version", "schema_fingerprint", "kb_revision", "object_type"} {
		t.Run(field, func(t *testing.T) {
			s := indexSnapshot(t, 2)
			i, _, v, _ := indexFixture(t, s)
			if err := i.Build(context.Background(), s, "kb-1", chunksFor(t, s)); err != nil {
				t.Fatal(err)
			}
			v.points[0].Payload[field] = "stale"
			v.points[0].Payload["content"] = "DO NOT RETURN SECRET"
			hits, err := i.Search(context.Background(), s, "kb-1", "Sales", 5)
			if err != nil || len(hits) != 1 {
				t.Fatalf("fallback missing %v %v", hits, err)
			}
			text, _ := hits[0].Payload["content"].(string)
			if strings.Contains(text, "SECRET") || !strings.Contains(text, "column_0") || hits[0].Payload["needs_rebuild"] != true {
				t.Fatalf("unsafe text: %v", hits)
			}
		})
	}
}
func TestObjectIndexRejectsMissingFreshCallback(t *testing.T) {
	s := indexSnapshot(t, 1)
	_, state, v, e := indexFixture(t, s)
	if _, err := NewObjectIndex(state, v, e, nil); err == nil {
		t.Fatal("nil freshness callback accepted")
	}
}
