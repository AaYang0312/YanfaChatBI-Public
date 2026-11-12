package services

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin/binding"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
	"reflect"
	"strings"
	"testing"
	"time"
)

func kbText(s string) *string { return &s }
func kbIndex(i int) *int      { return &i }

func TestObjectKnowledgeOwnerMigration(t *testing.T) {
	s, kb := knowledgeFixture(t)
	kb.UserID = primitive.NilObjectID
	got, err := NormalizeObjectKnowledgeOwner(s, kb)
	if err != nil || got == nil || got.UserID.Hex() != s.Scope.OwnerID || !kb.UserID.IsZero() {
		t.Fatalf("legacy owner migration unsafe: %+v %v", got, err)
	}
	kb.UserID = primitive.NewObjectID()
	if _, err = NormalizeObjectKnowledgeOwner(s, kb); err == nil {
		t.Fatal("foreign owner accepted")
	}
	kb.UserID = primitive.NilObjectID
	kb.ChatID = primitive.NewObjectID()
	if _, err = NormalizeObjectKnowledgeOwner(s, kb); err == nil {
		t.Fatal("foreign chat accepted")
	}
}

func TestObjectKnowledgeEditsPreserveHistoryAndStampCurrentEvidence(t *testing.T) {
	s, kb := knowledgeFixture(t)
	old := boundKnowledge(s, "old manual", models.KnowledgeUserEdit)
	old.FieldDescriptions = append(old.FieldDescriptions, models.FieldDescription{FieldName: "removed", Description: "keep removed", State: models.KnowledgeMapped, ColumnType: "text"})
	kb.TableDescriptions = []models.TableDescription{old, {TableName: "legacy", Description: "unmapped"}}
	rev := projectKnowledge(t, s, kb).KBRevision
	now := time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC)
	next, err := applyObjectKnowledgeEdits(s, kb, rev, []dtos.KnowledgeBaseDescriptionEdit{{ObjectID: old.ObjectID, Description: kbText("new manual"), FieldDescriptions: []dtos.KnowledgeBaseFieldEdit{{FieldName: "amount", Description: "new amount"}}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	p := projectKnowledge(t, s, next)
	if len(p.Effective) != 1 || p.Effective[0].Description != "new manual" || p.Effective[0].Provenance != models.KnowledgeUserEdit || p.Effective[0].FieldDescriptions[0].Description != "new amount" {
		t.Fatalf("edit not current: %+v", p)
	}
	if len(next.TableDescriptions) < 3 || next.TableDescriptions[1].Description != "unmapped" || next.TableDescriptions[0].FieldDescriptions[1].Description != "keep removed" || kb.TableDescriptions[0].Description != "old manual" {
		t.Fatal("history/old fields were lost or input mutated")
	}
	found := false
	for _, r := range next.TableDescriptions {
		if r.Description == "old manual" && r.State == models.KnowledgePending {
			found = true
		}
	}
	if !found {
		t.Fatal("old manual original missing from history")
	}
	pending, err := applyObjectKnowledgeEdits(s, next, p.KBRevision, []dtos.KnowledgeBaseDescriptionEdit{{DescriptionIndex: kbIndex(1), Description: kbText("edited pending")}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if projectKnowledge(t, s, pending).History[1].State != models.KnowledgePending {
		t.Fatal("pending history implicitly mapped")
	}
	if _, err := applyObjectKnowledgeEdits(s, kb, "old", []dtos.KnowledgeBaseDescriptionEdit{{ObjectID: old.ObjectID, Description: kbText("bad")}}, now); !errors.Is(err, ErrObjectKnowledgeRevision) {
		t.Fatal("stale edit accepted")
	}
	if _, err := applyObjectKnowledgeEdits(s, kb, rev, []dtos.KnowledgeBaseDescriptionEdit{{ObjectID: old.ObjectID, FieldDescriptions: []dtos.KnowledgeBaseFieldEdit{{FieldName: "secret", Description: "bad"}}}}, now); err == nil {
		t.Fatal("unreadable new field accepted")
	}
}

func TestObjectKnowledgeMappingsAreAtomicOriginalIndexBatch(t *testing.T) {
	s, kb := knowledgeFixture(t)
	kb.TableDescriptions = []models.TableDescription{{TableName: "old", Description: "first"}, {TableName: "old2", Description: "second"}}
	rev := projectKnowledge(t, s, kb).KBRevision
	req := dtos.ConfirmKnowledgeBaseMappingsRequest{ExpectedKBRevision: rev, Mappings: []dtos.KnowledgeBaseMapping{{DescriptionIndex: kbIndex(0), ObjectID: s.Objects[0].Metadata.ObjectID}, {DescriptionIndex: kbIndex(1), ObjectID: "missing"}}}
	if next, err := applyObjectKnowledgeMappings(s, kb, req); err == nil || next != nil {
		t.Fatal("partially invalid batch accepted")
	}
	req.Mappings[1].ObjectID = req.Mappings[0].ObjectID
	next, err := applyObjectKnowledgeMappings(s, kb, req)
	if err != nil || next == nil || len(next.TableDescriptions) != 4 || !reflect.DeepEqual(next.TableDescriptions[:2], kb.TableDescriptions) {
		t.Fatalf("valid batch failed: %+v %v", next, err)
	}
	req.Mappings[1].DescriptionIndex = kbIndex(0)
	if _, err := applyObjectKnowledgeMappings(s, kb, req); err == nil {
		t.Fatal("duplicate row accepted")
	}
}

type knowledgeMemoryStore struct {
	kb            *models.KnowledgeBase
	reads, writes int
	failRead      bool
	conflict      bool
}

func (m *knowledgeMemoryStore) FindByChatID(context.Context, primitive.ObjectID) (*models.KnowledgeBase, error) {
	m.reads++
	if m.failRead {
		return nil, errors.New("offline")
	}
	return m.kb, nil
}
func (m *knowledgeMemoryStore) CompareAndSwapKnowledge(_ context.Context, previous, next *models.KnowledgeBase) (bool, error) {
	m.writes++
	if m.conflict {
		return false, nil
	}
	if previous == nil && m.kb != nil {
		return false, nil
	}
	if previous != nil && (m.kb == nil || previous.StorageRevision != m.kb.StorageRevision) {
		return false, nil
	}
	m.kb = next
	return true, nil
}
func TestObjectKnowledgeSessionRechecksScopeCASAndSeparatesIndexFailure(t *testing.T) {
	for _, mode := range []string{"success", "scope-change", "storage-conflict", "index-failure", "read-failure", "post-save-change"} {
		t.Run(mode, func(t *testing.T) {
			snap, kb := knowledgeFixture(t)
			store := &knowledgeMemoryStore{kb: kb, conflict: mode == "storage-conflict", failRead: mode == "read-failure"}
			reads := 0
			invalidations := 0
			session := objectKnowledgeSession{Store: store, Now: time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC), Snapshot: func(context.Context) (schemaobjects.Snapshot, error) {
				reads++
				out := snap
				if mode == "scope-change" && reads >= 2 || mode == "post-save-change" && reads >= 3 {
					out.Scope.SelectionRevision++
				}
				return out, nil
			}, Invalidate: func(context.Context, schemaobjects.Scope) error {
				invalidations++
				if mode == "index-failure" {
					return errors.New("state CAS conflict")
				}
				return nil
			}}
			expected := projectKnowledge(t, snap, kb).KBRevision
			out, err := session.save(context.Background(), expected, func(s schemaobjects.Snapshot, k *models.KnowledgeBase, r string) (*models.KnowledgeBase, error) {
				return applyObjectKnowledgeEdits(s, k, r, []dtos.KnowledgeBaseDescriptionEdit{{ObjectID: s.Objects[0].Metadata.ObjectID, Description: kbText("saved")}}, session.Now)
			})
			if mode == "scope-change" || mode == "storage-conflict" || mode == "read-failure" {
				if err == nil || out != nil {
					t.Fatal("invalid write accepted")
				}
				if (mode == "scope-change" || mode == "read-failure") && store.writes != 0 {
					t.Fatal("wrote after failed fresh validation")
				}
				return
			}
			if err != nil || out == nil || !out.Saved || store.kb.StorageRevision != 1 || store.kb.KnowledgeVersion != 2 {
				t.Fatalf("save failed: %+v %v", out, err)
			}
			want := "stale"
			if mode == "index-failure" || mode == "post-save-change" {
				want = "invalidation_failed"
			}
			if out.IndexStatus != want {
				t.Fatalf("save/index conflated: %+v", out)
			}
			if mode == "post-save-change" {
				if !out.RefreshRequired || out.KBRevision != "" {
					t.Fatal("stale scope returned usable token")
				}
			} else if invalidations != 1 {
				t.Fatal("index was not invalidated")
			}
		})
	}
}

type kbStateFake struct {
	state  models.State
	next   models.State
	fail   bool
	writes int
}

func (f *kbStateFake) Read(context.Context, schemaobjects.Scope) (*models.State, error) {
	return &f.state, nil
}
func (f *kbStateFake) CompareAndSwap(_ context.Context, _ schemaobjects.Scope, revision int64, next models.State) error {
	f.writes++
	if revision != f.state.Revision || next.Revision != revision+1 {
		return errors.New("wrong CAS")
	}
	if f.fail {
		return errors.New("conflict")
	}
	f.next = next
	return nil
}
func TestObjectKnowledgeInvalidationCancelsOldBuild(t *testing.T) {
	s, _ := knowledgeFixture(t)
	f := &kbStateFake{state: models.State{Revision: 8, Scope: s.Scope, Registry: s.Registry, Manifest: models.Manifest{Status: "ready", Scope: s.Scope, ActiveGeneration: "active", BuildingGeneration: "old-worker", KBRevision: "old"}}}
	if err := invalidateObjectKnowledgeIndex(context.Background(), f, s.Scope); err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 || f.next.Manifest.Status != "stale" || f.next.Manifest.BuildingGeneration != "" || f.next.Manifest.ActiveGeneration != "" || f.next.Manifest.Scope != s.Scope || f.next.Revision != 9 {
		t.Fatalf("old worker not fenced: %+v", f.next)
	}
	if !reflect.DeepEqual(f.next.Registry, f.state.Registry) {
		t.Fatal("invalidating index changed instance registry")
	}
	f.fail = true
	if err := invalidateObjectKnowledgeIndex(context.Background(), f, s.Scope); err == nil {
		t.Fatal("state CAS failure hidden")
	}
}

func TestObjectKnowledgeGetPreservesLegacyOwnerlessHistory(t *testing.T) {
	snap, kb := knowledgeFixture(t)
	kb.UserID = primitive.NilObjectID
	kb.TableDescriptions = []models.TableDescription{{TableName: "legacy", Description: " original "}}
	store := &knowledgeMemoryStore{kb: kb}
	session := objectKnowledgeSession{Store: store, Snapshot: func(context.Context) (schemaobjects.Snapshot, error) { return snap, nil }}
	got, err := session.get(context.Background())
	if err != nil || got == nil || got.KBRevision == "" || len(got.TableDescriptions) != 1 || got.TableDescriptions[0].Description != " original " || got.TableDescriptions[0].State != models.KnowledgePending || len(got.EffectiveDescriptions) != 1 || got.Saved {
		t.Fatalf("legacy GET lost history or forged mapping: %+v %v", got, err)
	}
	if store.writes != 0 || !kb.UserID.IsZero() {
		t.Fatal("GET rewrote legacy owner")
	}
	store.failRead = true
	if _, err := session.get(context.Background()); !errors.Is(err, ErrObjectKnowledgeStore) {
		t.Fatal("KB read error accepted")
	}
}

func TestObjectKnowledgeDTOIgnoresClientEvidenceAndKeepsLegacyText(t *testing.T) {
	var request dtos.UpdateKnowledgeBaseRequest
	raw := []byte(`{"expected_kb_revision":"current","table_descriptions":[{"object_id":"target","table_name":"legacy-name","description":" text ","object_instance_version":"forged","object_scope":{"owner_id":"foreign"},"provenance":"ai_generated","state":"mapped","field_descriptions":[{"field_name":"amount","description":" amount ","column_type":"forged","provenance":"user_edit","state":"mapped"}]}]}`)
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"object_instance_version", "object_scope", "provenance", "state", "column_type"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("DTO accepted evidence %s: %s", forbidden, wire)
		}
	}
	legacy := request.LegacyDescriptions()
	if len(legacy) != 1 || legacy[0].Description != " text " || legacy[0].TableName != "legacy-name" || legacy[0].ObjectID != "" || legacy[0].FieldDescriptions[0].Description != " amount " {
		t.Fatal("legacy conversion lost text or retained forged evidence")
	}
	var mapping dtos.ConfirmKnowledgeBaseMappingsRequest
	if err := json.Unmarshal([]byte(`{"expected_kb_revision":"current","mappings":[{"description_index":0,"object_id":"current"}]}`), &mapping); err != nil {
		t.Fatal(err)
	}
	if err := binding.Validator.ValidateStruct(mapping); err != nil {
		t.Fatal(err)
	}
	mapping.Mappings[0].DescriptionIndex = nil
	if err := binding.Validator.ValidateStruct(mapping); err == nil {
		t.Fatal("missing batch description_index accepted")
	}
}

func TestObjectKnowledgeSaveRevalidatesAfterInvalidation(t *testing.T) {
	for _, mode := range []string{"binding-change", "selection-change", "read-current-failure", "manifest-cas-failure", "success"} {
		t.Run(mode, func(t *testing.T) {
			snap, kb := knowledgeFixture(t)
			kb.TableDescriptions = []models.TableDescription{boundKnowledge(snap, "prior manual", models.KnowledgeUserEdit)}
			store := &knowledgeMemoryStore{kb: kb}
			invalidations, reads := 0, 0
			session := objectKnowledgeSession{
				Store: store,
				Now:   time.Date(2026, 11, 11, 5, 0, 0, 0, time.UTC),
				Snapshot: func(context.Context) (schemaobjects.Snapshot, error) {
					reads++
					current := snap
					// All checks before Invalidate see the original valid scope.
					// Model its final ReadCurrent observing a change or read failure.
					if invalidations > 0 {
						switch mode {
						case "binding-change":
							current.Scope.BindingRevision++
						case "selection-change":
							current.Scope.SelectionRevision++
						case "read-current-failure":
							return schemaobjects.Snapshot{}, errors.New("current chat unavailable")
						}
					}
					return current, nil
				},
				Invalidate: func(context.Context, schemaobjects.Scope) error {
					invalidations++
					if store.writes != 1 || store.kb.StorageRevision != 1 {
						t.Fatal("invalidation must occur after the durable KB CAS")
					}
					switch mode {
					case "binding-change", "selection-change", "read-current-failure":
						return errors.New("selection revision conflict")
					case "manifest-cas-failure":
						return errors.New("manifest CAS conflict")
					default:
						return nil
					}
				},
			}
			out, err := session.save(context.Background(), projectKnowledge(t, snap, kb).KBRevision,
				func(s schemaobjects.Snapshot, k *models.KnowledgeBase, r string) (*models.KnowledgeBase, error) {
					return applyObjectKnowledgeEdits(s, k, r, []dtos.KnowledgeBaseDescriptionEdit{
						{ObjectID: s.Objects[0].Metadata.ObjectID, Description: kbText("saved manual")},
					}, session.Now)
				})
			if err != nil || out == nil || !out.Saved || store.writes != 1 || store.kb.StorageRevision != 1 || invalidations != 1 {
				t.Fatalf("durable save must remain successful: out=%+v err=%v writes=%d invalidations=%d", out, err, store.writes, invalidations)
			}
			if len(store.kb.TableDescriptions) != 2 || store.kb.TableDescriptions[0].Description != "saved manual" ||
				store.kb.TableDescriptions[1].Description != "prior manual" || kb.TableDescriptions[0].Description != "prior manual" {
				t.Fatal("saved edit or original history was rolled back or overwritten")
			}
			switch mode {
			case "binding-change", "selection-change", "read-current-failure":
				if !out.RefreshRequired || out.IndexStatus != "invalidation_failed" || out.KBRevision != "" ||
					len(out.TableDescriptions) != 0 || len(out.EffectiveDescriptions) != 0 {
					t.Fatalf("unverified scope returned old payload: saved=%v refresh=%v index=%s token_present=%v history=%d effective=%d snapshot_reads=%d",
						out.Saved, out.RefreshRequired, out.IndexStatus, out.KBRevision != "", len(out.TableDescriptions), len(out.EffectiveDescriptions), reads)
				}
			default:
				wantStatus := "stale"
				if mode == "manifest-cas-failure" {
					wantStatus = "invalidation_failed"
				}
				if out.RefreshRequired || out.IndexStatus != wantStatus || out.KBRevision == "" ||
					len(out.TableDescriptions) != 2 || len(out.EffectiveDescriptions) != 1 || out.EffectiveDescriptions[0].Description != "saved manual" {
					t.Fatalf("valid scope lost its saved result: %+v", out)
				}
			}
		})
	}
}
