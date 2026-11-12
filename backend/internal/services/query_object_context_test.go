package services

import (
	"context"
	"errors"
	"fmt"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"yanfachatbi/pkg/vectordb"
	"strings"
	"testing"
	"unicode/utf8"
)

func objectContextFixture(t *testing.T) schemaobjects.Snapshot {
	t.Helper()
	view := schemaobjects.Object{Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "reporting", Name: "sales_view", Kind: schemaobjects.View}, CatalogOID: 1}, Columns: []schemaobjects.Column{{Name: "net", Type: "numeric", Ordinal: 1}}}
	s, e := schemaobjects.Reconcile(schemaobjects.Scope{OwnerID: "owner", ChatID: "chat", SourceKey: "source"}, nil, []schemaobjects.Object{view})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestObjectContextEveryRetrievalBranchUsesCurrentView(t *testing.T) {
	snapshot := objectContextFixture(t)
	o := snapshot.Objects[0]
	payload := map[string]interface{}{"object_id": o.Metadata.ObjectID, "object_instance_version": o.Metadata.InstanceVersion, "schema_fingerprint": o.Metadata.Fingerprint, "kb_revision": "kb-current", "content": "OLD_SECRET old_column private_base"}
	for _, branch := range []string{"hit", "no-hit", "failed", "no-index", "stale-instance"} {
		t.Run(branch, func(t *testing.T) {
			reads := 0
			reader := func(context.Context) (schemaobjects.Snapshot, []models.TableDescription, string, error) {
				reads++
				return snapshot, nil, "kb-current", nil
			}
			var search objectContextSearch
			if branch != "no-index" {
				search = func(context.Context, schemaobjects.Snapshot, string, string) ([]vectordb.SearchResult, error) {
					if branch == "failed" {
						return nil, errors.New("offline")
					}
					if branch == "no-hit" {
						return nil, nil
					}
					p := map[string]interface{}{}
					for k, v := range payload {
						p[k] = v
					}
					if branch == "stale-instance" {
						p["object_instance_version"] = "old"
					}
					return []vectordb.SearchResult{{Payload: p}}, nil
				}
			}
			got, err := buildQueryObjectContext(context.Background(), reader, search, "sales")
			if err != nil || (search == nil && reads != 1) || (search != nil && reads != 2) || !strings.Contains(got, `"reporting"."sales_view"`) {
				t.Fatalf("%s %v reads=%d", got, err, reads)
			}
			if branch == "no-hit" || branch == "stale-instance" {
				if strings.Contains(got, `"net" numeric`) || !strings.Contains(got, "get_table_info") {
					t.Fatalf("no effective hit must use directory: %s", got)
				}
			} else if !strings.Contains(got, `"net" numeric`) {
				t.Fatalf("missing current column: %s", got)
			}
			for _, secret := range []string{"OLD_SECRET", "old_column", "private_base"} {
				if strings.Contains(got, secret) {
					t.Fatalf("historical payload leaked: %s", got)
				}
			}
		})
	}
}
func TestObjectContextUnavailableCannotFallback(t *testing.T) {
	reader := func(context.Context) (schemaobjects.Snapshot, []models.TableDescription, string, error) {
		return schemaobjects.Snapshot{}, nil, "", errors.New("revoked")
	}
	if text, err := buildQueryObjectContext(context.Background(), reader, nil, ""); err == nil || text != "" {
		t.Fatal("failed catalog became usable")
	}
}
func TestObjectContextOnlyCurrentMappedDescriptions(t *testing.T) {
	snapshot := objectContextFixture(t)
	o := snapshot.Objects[0]
	scope := snapshot.Scope
	rows := []models.TableDescription{{ObjectID: o.Metadata.ObjectID, InstanceVersion: o.Metadata.InstanceVersion, ObjectScope: &scope, State: models.KnowledgeMapped, Description: "current business meaning", FieldDescriptions: []models.FieldDescription{{FieldName: "net", ColumnType: "numeric", State: models.KnowledgeMapped, Description: "visible meaning"}, {FieldName: "revoked_column", ColumnType: "text", State: models.KnowledgeMapped, Description: "SECRET_COLUMN"}}}, {ObjectID: o.Metadata.ObjectID, InstanceVersion: "old", ObjectScope: &scope, State: models.KnowledgeMapped, Description: "SECRET_INSTANCE"}}
	reader := func(context.Context) (schemaobjects.Snapshot, []models.TableDescription, string, error) {
		return snapshot, rows, "kb", nil
	}
	got, err := buildQueryObjectContext(context.Background(), reader, nil, "")
	if err != nil || !strings.Contains(got, "current business meaning") || !strings.Contains(got, "visible meaning") || strings.Contains(got, "SECRET_") {
		t.Fatalf("%s %v", got, err)
	}
}

func TestObjectContextRefreshesAfterRetrievalRevocation(t *testing.T) {
	before := objectContextFixture(t)
	after, err := schemaobjects.Reconcile(before.Scope, before.Registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	reader := func(context.Context) (schemaobjects.Snapshot, []models.TableDescription, string, error) {
		calls++
		if calls == 1 {
			return before, nil, "old", nil
		}
		return after, nil, "new", nil
	}
	search := func(context.Context, schemaobjects.Snapshot, string, string) ([]vectordb.SearchResult, error) {
		return nil, errors.New("index failed while permission changed")
	}
	got, err := buildQueryObjectContext(context.Background(), reader, search, "")
	if err != nil || strings.Contains(got, "sales_view") || !strings.Contains(got, "No selected objects") {
		t.Fatalf("revoked object remained: %s %v", got, err)
	}
}

func TestObjectContextWideFallbackHasUTF8Budget(t *testing.T) {
	snapshot := objectContextFixture(t)
	object := snapshot.Objects[0]
	object.Columns = nil
	for n := 0; n < 1000; n++ {
		object.Columns = append(object.Columns, schemaobjects.Column{Name: fmt.Sprintf("column_%04d", n), Type: "numeric", Ordinal: n + 1, Comment: strings.Repeat("可见结构备注", 80)})
	}
	snapshot, err := schemaobjects.Reconcile(snapshot.Scope, snapshot.Registry, []schemaobjects.Object{object})
	if err != nil {
		t.Fatal(err)
	}
	reader := func(context.Context) (schemaobjects.Snapshot, []models.TableDescription, string, error) {
		return snapshot, nil, "current", nil
	}
	for _, branch := range []string{"no-hit", "failed", "no-index", "hit"} {
		t.Run(branch, func(t *testing.T) {
			var search objectContextSearch
			if branch != "no-index" {
				search = func(context.Context, schemaobjects.Snapshot, string, string) ([]vectordb.SearchResult, error) {
					if branch == "failed" {
						return nil, errors.New("offline")
					}
					if branch == "no-hit" {
						return nil, nil
					}
					m := snapshot.Objects[0].Metadata
					return []vectordb.SearchResult{{Payload: map[string]interface{}{"object_id": m.ObjectID, "object_instance_version": m.InstanceVersion, "schema_fingerprint": m.Fingerprint, "kb_revision": "current"}}}, nil
				}
			}
			text, err := buildQueryObjectContext(context.Background(), reader, search, "synthetic")
			if err != nil || len(text) > 64*1024 || !utf8.ValidString(text) || !strings.Contains(text, "get_table_info") {
				t.Fatalf("bounded context: bytes=%d valid=%v hint=%v err=%v", len(text), utf8.ValidString(text), strings.Contains(text, "get_table_info"), err)
			}
			if branch == "no-hit" && strings.Contains(text, "column_0000") {
				t.Fatal("no-hit sent full columns")
			}
			if branch != "no-hit" && !strings.Contains(text, "column_0000") {
				t.Fatal("fallback lost every current column")
			}
			if !strings.Contains(text, `"reporting"."sales_view"`) {
				t.Fatal("view not discoverable")
			}
		})
	}
}
