package services

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/schemaobjects"
)

func knowledgeFixture(t *testing.T) (schemaobjects.Snapshot, *models.KnowledgeBase) {
	t.Helper()
	owner, _ := primitive.ObjectIDFromHex("111111111111111111111111")
	chat, _ := primitive.ObjectIDFromHex("222222222222222222222222")
	scope := schemaobjects.Scope{OwnerID: owner.Hex(), ChatID: chat.Hex(), SourceKey: "source-A", BindingRevision: 2, ConnectionRevision: 3, SelectionRevision: 4}
	snap, err := schemaobjects.Reconcile(scope, nil, []schemaobjects.Object{{
		Metadata: schemaobjects.Metadata{Ref: schemaobjects.Ref{Schema: "reporting", Name: "sales", Kind: schemaobjects.View}, CatalogOID: 7, DefinitionStatus: "unavailable"},
		Columns:  []schemaobjects.Column{{Name: "amount", Type: "numeric", Ordinal: 1}, {Name: "code", Type: "text", Ordinal: 2}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return snap, &models.KnowledgeBase{UserID: owner, ChatID: chat, TableDescriptions: []models.TableDescription{}}
}

func boundKnowledge(s schemaobjects.Snapshot, text string, provenance models.KnowledgeProvenance) models.TableDescription {
	scope := s.Scope
	return models.TableDescription{
		TableName: "legacy.display", ObjectID: s.Objects[0].Metadata.ObjectID,
		InstanceVersion: s.Objects[0].Metadata.InstanceVersion, ObjectScope: &scope,
		Description: text, Provenance: provenance, State: models.KnowledgeMapped,
		UpdatedAt:         time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC),
		FieldDescriptions: []models.FieldDescription{{FieldName: "amount", ColumnType: "numeric", Description: " 人工金额 ", Provenance: models.KnowledgeUserEdit, State: models.KnowledgeMapped}},
	}
}

func projectKnowledge(t *testing.T, s schemaobjects.Snapshot, kb *models.KnowledgeBase) ObjectKnowledgeProjection {
	t.Helper()
	got, err := ProjectObjectKnowledge(s, kb)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestObjectKnowledgeCurrentInstanceAndReadableColumns(t *testing.T) {
	s, kb := knowledgeFixture(t)
	kb.TableDescriptions = []models.TableDescription{boundKnowledge(s, " 原始人工说明 ", models.KnowledgeUserEdit)}
	kb.TableDescriptions[0].FieldDescriptions = append(kb.TableDescriptions[0].FieldDescriptions,
		models.FieldDescription{FieldName: "removed", ColumnType: "text", Description: "撤权原文", Provenance: models.KnowledgeUserEdit, State: models.KnowledgeMapped})
	before, _ := json.Marshal(kb)
	got := projectKnowledge(t, s, kb)
	if got.KBRevision == "" || len(got.Effective) != 1 || len(got.History) != 1 {
		t.Fatalf("missing projection: %+v", got)
	}
	row := got.Effective[0]
	if row.Description != " 原始人工说明 " || row.TableName != "\"reporting\".\"sales\"" || row.InstanceVersion != s.Objects[0].Metadata.InstanceVersion {
		t.Fatalf("wrong current enrichment: %+v", row)
	}
	if len(row.FieldDescriptions) != 1 || row.FieldDescriptions[0].Description != " 人工金额 " {
		t.Fatalf("unreadable field retained or text lost: %+v", row.FieldDescriptions)
	}
	if got.History[0].FieldDescriptions[1].Description != "撤权原文" || got.History[0].FieldDescriptions[1].State != models.KnowledgePending {
		t.Fatal("removed field history lost")
	}
	after, _ := json.Marshal(kb)
	if string(before) != string(after) {
		t.Fatal("projection mutated input")
	}
	got.History[0].FieldDescriptions[0].Description = "mutated"
	got.Effective[0].ObjectScope.SourceKey = "mutated"
	if kb.TableDescriptions[0].FieldDescriptions[0].Description != " 人工金额 " || kb.TableDescriptions[0].ObjectScope.SourceKey != "source-A" {
		t.Fatal("projection aliases input")
	}
}

func TestObjectKnowledgeUnprovenAssociationStaysPending(t *testing.T) {
	cases := []struct {
		name string
		edit func(*schemaobjects.Snapshot, *models.KnowledgeBase)
	}{
		{"legacy", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].ObjectID = ""
			k.TableDescriptions[0].InstanceVersion = ""
			k.TableDescriptions[0].ObjectScope = nil
			k.TableDescriptions[0].Provenance = ""
			k.TableDescriptions[0].State = ""
		}},
		{"old-instance", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].InstanceVersion = "old-instance"
		}},
		{"missing-instance", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) { k.TableDescriptions[0].InstanceVersion = "" }},
		{"unknown-current-instance", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			s.Objects[0].Metadata.InstanceVersion = ""
			k.TableDescriptions[0].InstanceVersion = ""
		}},
		{"unknown-scope", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) { k.TableDescriptions[0].ObjectScope = nil }},
		{"other-source", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].ObjectScope.SourceKey = "source-B"
		}},
		{"other-owner", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].ObjectScope.OwnerID = "other-owner"
		}},
		{"other-chat", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].ObjectScope.ChatID = "other-chat"
		}},
		{"old-binding", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].ObjectScope.BindingRevision--
		}},
		{"old-connection", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].ObjectScope.ConnectionRevision--
		}},
		{"unselected", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) { s.Objects = nil }},
		{"pending", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].State = models.KnowledgePending
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, kb := knowledgeFixture(t)
			kb.TableDescriptions = []models.TableDescription{boundKnowledge(s, "历史原文", models.KnowledgeUserEdit)}
			tc.edit(&s, kb)
			got := projectKnowledge(t, s, kb)
			for _, row := range got.Effective {
				if row.Description != "" || len(row.FieldDescriptions) != 0 {
					t.Fatalf("historical text became effective: %+v", row)
				}
			}
			if len(got.History) != 1 || got.History[0].Description != "历史原文" || got.History[0].State != models.KnowledgePending {
				t.Fatalf("history not preserved pending: %+v", got.History)
			}
			if tc.name == "legacy" && got.History[0].Provenance != models.KnowledgeLegacyUnknown {
				t.Fatal("legacy origin invented")
			}
		})
	}
}

func TestObjectKnowledgeColumnChangeStaysPending(t *testing.T) {
	for _, change := range []string{"type", "missing-type", "pending", "name"} {
		t.Run(change, func(t *testing.T) {
			s, kb := knowledgeFixture(t)
			kb.TableDescriptions = []models.TableDescription{boundKnowledge(s, "对象仍有效", models.KnowledgeUserEdit)}
			switch change {
			case "type":
				s.Objects[0].Columns[0].Type = "text"
			case "missing-type":
				kb.TableDescriptions[0].FieldDescriptions[0].ColumnType = ""
			case "pending":
				kb.TableDescriptions[0].FieldDescriptions[0].State = models.KnowledgePending
			case "name":
				s.Objects[0].Columns[0].Name = "renamed"
			}
			got := projectKnowledge(t, s, kb)
			if len(got.Effective) != 1 || got.Effective[0].Description != "对象仍有效" || len(got.Effective[0].FieldDescriptions) != 0 {
				t.Fatalf("old column description leaked: %+v", got.Effective)
			}
			if got.History[0].FieldDescriptions[0].State != models.KnowledgePending {
				t.Fatal("changed field not pending")
			}
		})
	}
}

func TestObjectKnowledgeScopeAndIncompleteSnapshotRejected(t *testing.T) {
	for _, which := range []string{"owner", "chat", "no-owner", "no-source", "incomplete", "metadata-version", "forged-id", "duplicate"} {
		t.Run(which, func(t *testing.T) {
			s, kb := knowledgeFixture(t)
			switch which {
			case "owner":
				kb.UserID = primitive.NewObjectID()
			case "chat":
				kb.ChatID = primitive.NewObjectID()
			case "no-owner":
				s.Scope.OwnerID = ""
			case "no-source":
				s.Scope.SourceKey = ""
			case "incomplete":
				s.Complete = false
			case "metadata-version":
				s.MetadataVersion = 1
			case "forged-id":
				s.Objects[0].Metadata.ObjectID = "forged"
			case "duplicate":
				s.Objects = append(s.Objects, s.Objects[0])
			}
			got, err := ProjectObjectKnowledge(s, kb)
			if err == nil || len(got.Effective) != 0 || len(got.History) != 0 {
				t.Fatalf("unsafe snapshot accepted: %+v %v", got, err)
			}
		})
	}
}

func TestObjectKnowledgeEmptyDescriptionAndDatabaseComments(t *testing.T) {
	s, kb := knowledgeFixture(t)
	got := projectKnowledge(t, s, kb)
	if len(got.Effective) != 1 || got.KBRevision == "" || got.Effective[0].Description != "" {
		t.Fatalf("empty KB blocked structural object: %+v", got)
	}
	nilKB := projectKnowledge(t, s, nil)
	if nilKB.KBRevision != got.KBRevision {
		t.Fatal("empty and absent KB revisions differ")
	}
	s.Objects[0].Comment = "目录注释"
	s.Objects[0].Columns[0].Comment = "目录金额"
	got = projectKnowledge(t, s, kb)
	if got.Effective[0].Description != "目录注释" || got.Effective[0].Provenance != models.KnowledgeDatabaseComment || got.Effective[0].FieldDescriptions[0].Provenance != models.KnowledgeDatabaseComment || len(got.History) != 0 {
		t.Fatalf("catalog comments not separately sourced: %+v", got)
	}
	s.Objects[0].Metadata.InstanceVersion = ""
	got = projectKnowledge(t, s, kb)
	if got.Effective[0].Description != "" || len(got.Effective[0].FieldDescriptions) != 0 {
		t.Fatal("enrichment without an instance")
	}
}

func TestObjectKnowledgeSameNameObjectsNeverShareDescriptions(t *testing.T) {
	s, kb := knowledgeFixture(t)
	kb.TableDescriptions = []models.TableDescription{boundKnowledge(s, "视图专用", models.KnowledgeUserEdit)}
	other := s.Objects[0]
	other.Metadata.Ref.Schema = "public"
	other.Metadata.Ref.Kind = schemaobjects.Table
	other.Metadata.ObjectID = schemaobjects.ObjectID(s.Scope.SourceKey, other.Metadata.Ref)
	other.Metadata.InstanceVersion = "other"
	s.Objects = append(s.Objects, other)
	got := projectKnowledge(t, s, kb)
	if len(got.Effective) != 2 {
		t.Fatalf("wrong object count: %+v", got)
	}
	for _, row := range got.Effective {
		if row.ObjectID == other.Metadata.ObjectID && (row.Description != "" || len(row.FieldDescriptions) != 0) {
			t.Fatal("same-name object inherited text")
		}
	}
}

func TestObjectKnowledgeManualWinsAndAIIsNotCertified(t *testing.T) {
	s, kb := knowledgeFixture(t)
	ai := boundKnowledge(s, "AI文本", models.KnowledgeAIGenerated)
	ai.FieldDescriptions = []models.FieldDescription{{FieldName: "amount", ColumnType: "numeric", Description: "AI金额", Provenance: models.KnowledgeAIGenerated, State: models.KnowledgeMapped}, {FieldName: "code", ColumnType: "text", Description: "AI编码", Provenance: models.KnowledgeAIGenerated, State: models.KnowledgeMapped}}
	manual := boundKnowledge(s, "人工原文", models.KnowledgeUserEdit)
	kb.TableDescriptions = []models.TableDescription{ai, manual}
	got := projectKnowledge(t, s, kb)
	if len(got.Effective) != 1 || got.Effective[0].Description != "人工原文" {
		t.Fatalf("AI overwrote manual text: %+v", got.Effective)
	}
	fields := map[string]models.FieldDescription{}
	for _, field := range got.Effective[0].FieldDescriptions {
		fields[field.FieldName] = field
	}
	if fields["amount"].Description != " 人工金额 " || fields["code"].Description != "AI编码" || fields["code"].Provenance != models.KnowledgeAIGenerated {
		t.Fatalf("AI must only fill empty descriptions: %+v", fields)
	}
	wire, _ := json.Marshal(got.Effective)
	if strings.Contains(string(wire), "certified") || strings.Contains(string(wire), "approved") {
		t.Fatal("AI represented as certification")
	}
}

func TestObjectKnowledgeRevisionStableAndCoversChanges(t *testing.T) {
	s, kb := knowledgeFixture(t)
	kb.TableDescriptions = []models.TableDescription{boundKnowledge(s, "原文", models.KnowledgeUserEdit)}
	base := projectKnowledge(t, s, kb).KBRevision
	kb.KBRevision = "untrusted-cache"
	kb.Base.UpdatedAt = time.Now()
	s.Objects[0].Columns[0], s.Objects[0].Columns[1] = s.Objects[0].Columns[1], s.Objects[0].Columns[0]
	s.Registry["unselected"] = schemaobjects.Instance{Version: "irrelevant"}
	if projectKnowledge(t, s, kb).KBRevision != base {
		t.Fatal("revision depends on cache, timestamp, registry or catalog order")
	}
	changes := []struct {
		name string
		edit func(*schemaobjects.Snapshot, *models.KnowledgeBase)
	}{
		{"text", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].Description = "新版"
		}},
		{"field", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].FieldDescriptions[0].Description = "新版金额"
		}},
		{"provenance", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions[0].Provenance = models.KnowledgeAIGenerated
		}},
		{"instance", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			s.Objects[0].Metadata.InstanceVersion = "new-instance"
		}},
		{"column-access", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			s.Objects[0].Columns = s.Objects[0].Columns[:1]
		}},
		{"selection", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) { s.Scope.SelectionRevision++ }},
		{"pending-edit", func(s *schemaobjects.Snapshot, k *models.KnowledgeBase) {
			k.TableDescriptions = append(k.TableDescriptions, models.TableDescription{TableName: "old", Description: "历史修改"})
		}},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			s, kb := knowledgeFixture(t)
			kb.TableDescriptions = []models.TableDescription{boundKnowledge(s, "原文", models.KnowledgeUserEdit)}
			before := projectKnowledge(t, s, kb).KBRevision
			tc.edit(&s, kb)
			if projectKnowledge(t, s, kb).KBRevision == before {
				t.Fatal("revision did not cover change")
			}
		})
	}
}

func TestObjectKnowledgeConfirmMappingUsesServerInstanceAndPreservesHistory(t *testing.T) {
	s, kb := knowledgeFixture(t)
	kb.TableDescriptions = []models.TableDescription{{TableName: "old-name", Description: " 原文 ", FieldDescriptions: []models.FieldDescription{{FieldName: "amount", Description: " 旧金额 "}, {FieldName: "removed", Description: "撤列原文"}}}}
	before, _ := json.Marshal(kb)
	revision := projectKnowledge(t, s, kb).KBRevision
	mapped, err := ConfirmObjectKnowledgeMapping(s, kb, revision, 0, s.Objects[0].Metadata.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if mapped == nil || len(mapped.TableDescriptions) != 2 {
		t.Fatalf("mapping must retain history and append server-bound copy: %+v", mapped)
	}
	if !reflect.DeepEqual(mapped.TableDescriptions[0], kb.TableDescriptions[0]) {
		t.Fatal("original text/history not preserved")
	}
	current := mapped.TableDescriptions[1]
	if current.InstanceVersion != s.Objects[0].Metadata.InstanceVersion || current.ObjectScope == nil || *current.ObjectScope != s.Scope || current.Provenance != models.KnowledgeLegacyUnknown || current.FieldDescriptions[0].ColumnType != "numeric" {
		t.Fatalf("mapping fabricated origin or trusted stale instance: %+v", current)
	}
	got := projectKnowledge(t, s, mapped)
	if len(got.Effective) != 1 || got.Effective[0].Description != " 原文 " || len(got.Effective[0].FieldDescriptions) != 1 || got.Effective[0].FieldDescriptions[0].Description != " 旧金额 " {
		t.Fatalf("explicit mapping not effective: %+v", got)
	}
	if mapped.KBRevision != got.KBRevision || mapped.KBRevision == revision {
		t.Fatal("mapping revision not renewed")
	}
	if current.FieldDescriptions[1].Description != "撤列原文" || current.FieldDescriptions[1].State != models.KnowledgePending {
		t.Fatal("unmatched field lost")
	}
	after, _ := json.Marshal(kb)
	if string(before) != string(after) {
		t.Fatal("confirmation mutated input")
	}
	mapped.TableDescriptions[0].FieldDescriptions[0].Description = "mutation"
	if kb.TableDescriptions[0].FieldDescriptions[0].Description != " 旧金额 " {
		t.Fatal("confirmation aliases input")
	}
}

func TestObjectKnowledgeConfirmMappingRejectsStaleRevisionAndUnsafeTargets(t *testing.T) {
	for _, which := range []string{"stale", "blank-revision", "edited-history", "reordered-history", "source", "owner", "chat", "missing-target", "empty-instance", "negative-index", "large-index", "already-mapped"} {
		t.Run(which, func(t *testing.T) {
			s, kb := knowledgeFixture(t)
			kb.TableDescriptions = []models.TableDescription{{TableName: "old", Description: "历史"}, {TableName: "other", Description: "其他历史"}}
			revision := projectKnowledge(t, s, kb).KBRevision
			id := s.Objects[0].Metadata.ObjectID
			index := 0
			want := ErrObjectKnowledgeMapping
			switch which {
			case "stale":
				revision = "old"
				want = ErrObjectKnowledgeRevision
			case "blank-revision":
				revision = ""
				want = ErrObjectKnowledgeRevision
			case "edited-history":
				kb.TableDescriptions[0].Description = "edited"
				want = ErrObjectKnowledgeRevision
			case "reordered-history":
				kb.TableDescriptions[0], kb.TableDescriptions[1] = kb.TableDescriptions[1], kb.TableDescriptions[0]
				want = ErrObjectKnowledgeRevision
			case "source", "owner", "chat":
				scope := s.Scope
				if which == "source" {
					scope.SourceKey = "other"
				} else if which == "owner" {
					scope.OwnerID = "other"
				} else {
					scope.ChatID = "other"
				}
				kb.TableDescriptions[0].ObjectScope = &scope
				revision = projectKnowledge(t, s, kb).KBRevision
			case "missing-target":
				id = "not-selected"
			case "empty-instance":
				s.Objects[0].Metadata.InstanceVersion = ""
				revision = projectKnowledge(t, s, kb).KBRevision
			case "negative-index":
				index = -1
			case "large-index":
				index = 2
			case "already-mapped":
				kb.TableDescriptions[0] = boundKnowledge(s, "current", models.KnowledgeUserEdit)
				revision = projectKnowledge(t, s, kb).KBRevision
			}
			before, _ := json.Marshal(kb)
			result, err := ConfirmObjectKnowledgeMapping(s, kb, revision, index, id)
			if !errors.Is(err, want) || result != nil {
				t.Fatalf("unsafe mapping accepted: %v %+v; want %v", err, result, want)
			}
			after, _ := json.Marshal(kb)
			if string(before) != string(after) {
				t.Fatal("failed confirmation changed input")
			}
		})
	}
}

func TestObjectKnowledgeConfirmCannotReplaceNonEmptyManualDescription(t *testing.T) {
	s, kb := knowledgeFixture(t)
	manual := boundKnowledge(s, "当前人工", models.KnowledgeUserEdit)
	legacy := models.TableDescription{TableName: "legacy", Description: "另一个人工", Provenance: models.KnowledgeUserEdit, FieldDescriptions: []models.FieldDescription{{FieldName: "amount", Description: "另一个金额", Provenance: models.KnowledgeUserEdit}, {FieldName: "code", Description: "新增编码", Provenance: models.KnowledgeAIGenerated}}}
	kb.TableDescriptions = []models.TableDescription{legacy, manual}
	mapped, err := ConfirmObjectKnowledgeMapping(s, kb, projectKnowledge(t, s, kb).KBRevision, 0, s.Objects[0].Metadata.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	got := projectKnowledge(t, s, mapped)
	if len(got.Effective) != 1 || got.Effective[0].Description != "当前人工" {
		t.Fatalf("mapped history replaced nonempty manual text: %+v", got)
	}
	fields := map[string]string{}
	for _, f := range got.Effective[0].FieldDescriptions {
		fields[f.FieldName] = f.Description
	}
	if fields["amount"] != " 人工金额 " || fields["code"] != "新增编码" {
		t.Fatalf("manual field overwrite or missing fill: %+v", fields)
	}
}

func TestObjectKnowledgeRevisionSurvivesBSONTimestampPrecision(t *testing.T) {
	s, kb := knowledgeFixture(t)
	row := boundKnowledge(s, "原文", models.KnowledgeUserEdit)
	row.UpdatedAt = time.Date(2026, 11, 11, 9, 0, 0, 123456789, time.FixedZone("HK", 8*60*60))
	row.FieldDescriptions[0].UpdatedAt = row.UpdatedAt
	kb.TableDescriptions = []models.TableDescription{row}
	before := projectKnowledge(t, s, kb).KBRevision
	data, err := bson.Marshal(kb)
	if err != nil {
		t.Fatal(err)
	}
	var stored models.KnowledgeBase
	if err := bson.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	after := projectKnowledge(t, s, &stored).KBRevision
	if before != after {
		t.Fatalf("persisting BSON changed KB revision: %s != %s", before, after)
	}
}

func TestObjectKnowledgeInvalidTimestampCannotCollapseRevisions(t *testing.T) {
	s, kb := knowledgeFixture(t)
	row := boundKnowledge(s, "原文", models.KnowledgeUserEdit)
	row.UpdatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	kb.TableDescriptions = []models.TableDescription{row}
	got, err := ProjectObjectKnowledge(s, kb)
	if err == nil || got.KBRevision != "" || len(got.Effective) != 0 {
		t.Fatalf("unserializable evidence produced usable revision: %+v %v", got, err)
	}
}

func TestObjectKnowledgeAIOnlyFillsEmptyIncludingCatalogComments(t *testing.T) {
	s, kb := knowledgeFixture(t)
	s.Objects[0].Comment = "当前目录说明"
	s.Objects[0].Columns[0].Comment = "当前目录金额"
	ai := boundKnowledge(s, "AI建议", models.KnowledgeAIGenerated)
	ai.FieldDescriptions = []models.FieldDescription{
		{FieldName: "amount", ColumnType: "numeric", Description: "AI金额", Provenance: models.KnowledgeAIGenerated, State: models.KnowledgeMapped},
		{FieldName: "code", ColumnType: "text", Description: "AI编码", Provenance: models.KnowledgeAIGenerated, State: models.KnowledgeMapped},
	}
	kb.TableDescriptions = []models.TableDescription{ai}
	got := projectKnowledge(t, s, kb)
	if got.Effective[0].Description != "当前目录说明" || got.Effective[0].Provenance != models.KnowledgeDatabaseComment {
		t.Errorf("AI replaced nonempty catalog description: %+v", got.Effective[0])
	}
	fields := map[string]models.FieldDescription{}
	for _, field := range got.Effective[0].FieldDescriptions {
		fields[field.FieldName] = field
	}
	if fields["amount"].Description != "当前目录金额" || fields["amount"].Provenance != models.KnowledgeDatabaseComment || fields["code"].Description != "AI编码" {
		t.Errorf("AI must fill only empty columns: %+v", fields)
	}
	stale := boundKnowledge(s, "旧目录说明", models.KnowledgeDatabaseComment)
	stale.FieldDescriptions[0].Description = "旧目录金额"
	stale.FieldDescriptions[0].Provenance = models.KnowledgeDatabaseComment
	kb.TableDescriptions = []models.TableDescription{stale}
	got = projectKnowledge(t, s, kb)
	if got.Effective[0].Description != "当前目录说明" || got.Effective[0].FieldDescriptions[0].Description != "当前目录金额" {
		t.Fatal("stored stale catalog text reused")
	}
}

func TestObjectKnowledgeConfirmPreservesStoredMappingsAcrossVisibility(t *testing.T) {
	for _, visibility := range []string{"unselected-object", "revoked-column"} {
		t.Run(visibility, func(t *testing.T) {
			s, kb := knowledgeFixture(t)
			original := boundKnowledge(s, "A manual", models.KnowledgeUserEdit)
			aID := original.ObjectID
			b := s.Objects[0]
			b.Metadata.Ref.Name = "other"
			b.Metadata.CatalogOID = 8
			bID := schemaobjects.ObjectID(s.Scope.SourceKey, b.Metadata.Ref)
			full, err := schemaobjects.Reconcile(s.Scope, s.Registry, []schemaobjects.Object{s.Objects[0], b})
			if err != nil {
				t.Fatal(err)
			}
			kb.TableDescriptions = []models.TableDescription{original, {TableName: "legacy-b", Description: "B legacy"}}

			narrow := full
			narrow.Scope.SelectionRevision++
			narrow.Objects = nil
			for _, object := range full.Objects {
				if object.Metadata.ObjectID == aID {
					if visibility == "unselected-object" {
						continue
					}
					// Keep A readable through code, but amount is no longer readable.
					object.Columns = []schemaobjects.Column{{Name: "code", Type: "text", Ordinal: 2}}
				}
				narrow.Objects = append(narrow.Objects, object)
			}
			current := projectKnowledge(t, narrow, kb)
			if current.History[0].FieldDescriptions[0].State != models.KnowledgePending {
				t.Fatal("unavailable field should be pending only in the current projection")
			}
			for _, row := range current.Effective {
				if row.ObjectID == aID && (visibility == "unselected-object" || len(row.FieldDescriptions) != 0) {
					t.Fatal("A or its unavailable field leaked before confirmation")
				}
			}
			before, _ := json.Marshal(kb)
			updated, err := ConfirmObjectKnowledgeMapping(narrow, kb, current.KBRevision, 1, bID)
			if err != nil {
				t.Fatal(err)
			}
			if len(updated.TableDescriptions) != len(kb.TableDescriptions)+1 {
				t.Fatal("confirmation must only append one bound copy")
			}
			if !reflect.DeepEqual(updated.TableDescriptions[:len(kb.TableDescriptions)], kb.TableDescriptions) {
				t.Errorf("confirming B changed stored source associations: A state=%s field=%s",
					updated.TableDescriptions[0].State, updated.TableDescriptions[0].FieldDescriptions[0].State)
			}
			after, _ := json.Marshal(kb)
			if string(before) != string(after) {
				t.Fatal("confirmation mutated input KB")
			}
			stillNarrow := projectKnowledge(t, narrow, updated)
			for _, row := range stillNarrow.Effective {
				if row.ObjectID == aID && (visibility == "unselected-object" || len(row.FieldDescriptions) != 0) {
					t.Fatal("preserving source evidence exposed A while still unavailable")
				}
			}
			if updated.KBRevision != stillNarrow.KBRevision || updated.KBRevision == current.KBRevision {
				t.Fatal("confirmation did not derive the new current revision")
			}

			for _, restoration := range []string{"same-instance-and-columns", "rebuilt-instance", "changed-column-type", "column-still-revoked"} {
				t.Run(restoration, func(t *testing.T) {
					restored := full
					restored.Scope.SelectionRevision = narrow.Scope.SelectionRevision + 1
					restored.Objects = append([]schemaobjects.Object(nil), full.Objects...)
					for i := range restored.Objects {
						object := &restored.Objects[i]
						if object.Metadata.ObjectID != aID {
							continue
						}
						switch restoration {
						case "rebuilt-instance":
							object.Metadata.InstanceVersion = "rebuilt-A"
						case "changed-column-type":
							object.Columns = append([]schemaobjects.Column(nil), object.Columns...)
							for j := range object.Columns {
								if object.Columns[j].Name == "amount" {
									object.Columns[j].Type = "text"
								}
							}
						case "column-still-revoked":
							object.Columns = []schemaobjects.Column{{Name: "code", Type: "text", Ordinal: 2}}
						}
					}
					got := projectKnowledge(t, restored, updated)
					found := false
					for _, row := range got.Effective {
						if row.ObjectID != aID {
							continue
						}
						found = true
						wantDescription := "A manual"
						wantFields := 0
						if restoration == "same-instance-and-columns" {
							wantFields = 1
						}
						if restoration == "rebuilt-instance" {
							wantDescription = ""
						}
						if row.Description != wantDescription || len(row.FieldDescriptions) != wantFields {
							t.Errorf("restoration %s: description=%q fields=%d, want %q/%d",
								restoration, row.Description, len(row.FieldDescriptions), wantDescription, wantFields)
						}
						if wantFields == 1 && len(row.FieldDescriptions) == 1 && row.FieldDescriptions[0].Description != " 人工金额 " {
							t.Error("restored manual field text changed")
						}
					}
					if !found {
						t.Fatal("restored structural A missing")
					}
				})
			}
		})
	}
}
