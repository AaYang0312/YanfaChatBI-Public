package models

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func testContextSummaryRecord() *ChatContextSummary {
	chatID := primitive.NewObjectID()
	createdAt := time.Date(2026, 11, 12, 3, 40, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 11, 12, 4, 5, 0, 0, time.UTC)
	coveredUntil := time.Date(2026, 11, 12, 3, 40, 0, 0, time.UTC)
	messageIDs := []primitive.ObjectID{primitive.NewObjectID(), primitive.NewObjectID()}
	return &ChatContextSummary{
		Base:     Base{ID: primitive.NewObjectID(), CreatedAt: createdAt, UpdatedAt: updatedAt},
		ChatID:   chatID,
		Revision: 3,
		Summary: ContextSummary{
			Goal: "近30天各店销量对比",
			DataSource: ContextSummaryDataSource{
				ChatID:          chatID,
				SourceID:        "src-erp-1",
				BindingRevision: 3,
				Database:        "bi_agent",
				Selection: &ContextSummarySelection{
					Kind:     "v2",
					Revision: 7,
					Objects:  []string{"reporting.sales", "bi.orders"},
				},
			},
			SchemaFacts: []ContextSummarySchemaFact{{
				Object:     "reporting.sales",
				Kind:       "view",
				KeyColumns: []string{"shop_id", "amount"},
				Notes:      "按店铺汇总",
			}},
			Queries: []ContextSummaryQuery{{
				Purpose:    "近30天各店销量",
				Tables:     []string{"bi.orders"},
				SQLDigest:  "a1b2c3",
				RowCount:   6,
				KeyNumbers: []string{"合计 126 件"},
				Status:     "executed",
			}},
			Errors: []ContextSummaryError{{
				What:     "response_format 不支持",
				Cause:    "DeepSeek 无 json_schema",
				Fix:      "去掉 response_format 重试",
				Resolved: true,
			}},
			UserPreferences: ContextSummaryPreferences{
				ExecutionMode:  "strict",
				NonTechMode:    false,
				Model:          "deepseek/deepseek-v4-flash",
				ReasoningLevel: "off",
				Language:       "zh-CN",
			},
			Decisions:     []string{"口径按 paid_at 计算", "仅生成不执行"},
			OpenQuestions: []string{"是否需要按商品维度拆分"},
			Provenance: ContextSummaryProvenance{
				MessageIDs:   messageIDs,
				Range:        []string{"2026-11-12T03:00Z", "2026-11-12T03:40Z"},
				CoveredCount: 42,
				Model:        "default/glm-5.3-flash",
				CreatedAt:    createdAt,
			},
		},
		Rendered:          "## 目标\n近30天各店销量对比",
		TokenEstimate:     820,
		CoveredMessageIDs: messageIDs,
		CoveredUntil:      &coveredUntil,
		Model:             "default/glm-5.3-flash",
		StaleSource:       true,
	}
}

func assertJSONKeys(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire keys = %v want %v", got, want)
	}
}

func TestContextSummaryJSONWireShape(t *testing.T) {
	record := testContextSummaryRecord()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}

	assertJSONKeys(t, wire, "id", "chat_id", "revision", "summary", "rendered", "token_estimate",
		"covered_message_ids", "covered_until", "model", "stale_source", "created_at", "updated_at")
	if wire["chat_id"] != record.ChatID.Hex() || wire["revision"] != float64(3) ||
		wire["rendered"] != record.Rendered || wire["token_estimate"] != float64(820) ||
		wire["model"] != "default/glm-5.3-flash" || wire["stale_source"] != true {
		t.Fatalf("record fields lost: %s", raw)
	}

	summary := wire["summary"].(map[string]any)
	assertJSONKeys(t, summary, "goal", "data_source", "schema_facts", "queries", "errors",
		"user_preferences", "decisions", "open_questions", "provenance")
	if summary["goal"] != "近30天各店销量对比" {
		t.Fatalf("goal lost: %s", raw)
	}

	dataSource := summary["data_source"].(map[string]any)
	assertJSONKeys(t, dataSource, "chat_id", "source_id", "binding_revision", "database", "selection")
	if dataSource["chat_id"] != record.ChatID.Hex() || dataSource["source_id"] != "src-erp-1" ||
		dataSource["binding_revision"] != float64(3) || dataSource["database"] != "bi_agent" {
		t.Fatalf("data_source lost: %s", raw)
	}
	selection := dataSource["selection"].(map[string]any)
	assertJSONKeys(t, selection, "kind", "revision", "objects")
	if selection["kind"] != "v2" || selection["revision"] != float64(7) {
		t.Fatalf("selection lost: %s", raw)
	}
	if !reflect.DeepEqual(selection["objects"], []any{"reporting.sales", "bi.orders"}) {
		t.Fatalf("selection objects lost: %s", raw)
	}

	schemaFact := summary["schema_facts"].([]any)[0].(map[string]any)
	assertJSONKeys(t, schemaFact, "object", "kind", "key_columns", "notes")
	if schemaFact["object"] != "reporting.sales" || schemaFact["kind"] != "view" || schemaFact["notes"] != "按店铺汇总" {
		t.Fatalf("schema fact lost: %s", raw)
	}

	query := summary["queries"].([]any)[0].(map[string]any)
	assertJSONKeys(t, query, "purpose", "tables", "sql_digest", "row_count", "key_numbers", "status")
	if query["purpose"] != "近30天各店销量" || query["sql_digest"] != "a1b2c3" ||
		query["row_count"] != float64(6) || query["status"] != "executed" {
		t.Fatalf("query digest fields lost: %s", raw)
	}
	if _, ok := query["sql"]; ok {
		t.Fatalf("full SQL leaked into summary: %s", raw)
	}

	assertJSONKeys(t, summary["errors"].([]any)[0].(map[string]any), "what", "cause", "fix", "resolved")
	assertJSONKeys(t, summary["user_preferences"].(map[string]any), "execution_mode", "non_tech_mode",
		"model", "reasoning_level", "language")
	assertJSONKeys(t, summary["provenance"].(map[string]any), "message_ids", "range", "covered_count",
		"model", "created_at")

	var restored ChatContextSummary
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if restored.ChatID != record.ChatID || restored.Revision != record.Revision ||
		restored.Rendered != record.Rendered || restored.TokenEstimate != record.TokenEstimate ||
		restored.Model != record.Model || restored.StaleSource != record.StaleSource {
		t.Fatalf("JSON round-trip lost record fields: %+v", restored)
	}
	if !reflect.DeepEqual(restored.CoveredMessageIDs, record.CoveredMessageIDs) {
		t.Fatalf("JSON round-trip lost covered_message_ids: %+v", restored.CoveredMessageIDs)
	}
	if restored.CoveredUntil == nil || !restored.CoveredUntil.Equal(*record.CoveredUntil) {
		t.Fatalf("JSON round-trip lost covered_until: %+v", restored.CoveredUntil)
	}
	if !reflect.DeepEqual(restored.Summary.SchemaFacts, record.Summary.SchemaFacts) ||
		!reflect.DeepEqual(restored.Summary.Queries, record.Summary.Queries) ||
		!reflect.DeepEqual(restored.Summary.Errors, record.Summary.Errors) ||
		!reflect.DeepEqual(restored.Summary.UserPreferences, record.Summary.UserPreferences) ||
		!reflect.DeepEqual(restored.Summary.Decisions, record.Summary.Decisions) ||
		!reflect.DeepEqual(restored.Summary.OpenQuestions, record.Summary.OpenQuestions) ||
		!reflect.DeepEqual(restored.Summary.DataSource, record.Summary.DataSource) {
		t.Fatalf("JSON round-trip lost summary fields: %+v", restored.Summary)
	}
	if !reflect.DeepEqual(restored.Summary.Provenance.MessageIDs, record.Summary.Provenance.MessageIDs) ||
		restored.Summary.Provenance.CoveredCount != 42 ||
		restored.Summary.Provenance.Model != "default/glm-5.3-flash" ||
		!reflect.DeepEqual(restored.Summary.Provenance.Range, record.Summary.Provenance.Range) {
		t.Fatalf("JSON round-trip lost provenance: %+v", restored.Summary.Provenance)
	}
}

func TestContextSummaryBSONRoundTrip(t *testing.T) {
	record := testContextSummaryRecord()
	raw, err := bson.Marshal(record)
	if err != nil {
		t.Fatalf("bson marshal: %v", err)
	}
	var restored ChatContextSummary
	if err := bson.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("bson unmarshal: %v", err)
	}
	if restored.ID != record.ID || restored.ChatID != record.ChatID ||
		restored.Revision != record.Revision || restored.Rendered != record.Rendered ||
		restored.TokenEstimate != record.TokenEstimate || restored.Model != record.Model ||
		restored.StaleSource != record.StaleSource {
		t.Fatalf("BSON round-trip lost record fields: %+v", restored)
	}
	if !restored.CreatedAt.Equal(record.CreatedAt) || !restored.UpdatedAt.Equal(record.UpdatedAt) {
		t.Fatalf("BSON round-trip lost timestamps: %+v", restored.Base)
	}
	if !reflect.DeepEqual(restored.CoveredMessageIDs, record.CoveredMessageIDs) {
		t.Fatalf("BSON round-trip lost covered_message_ids: %+v", restored.CoveredMessageIDs)
	}
	if restored.CoveredUntil == nil || !restored.CoveredUntil.Equal(*record.CoveredUntil) {
		t.Fatalf("BSON round-trip lost covered_until: %+v", restored.CoveredUntil)
	}
	wantSummary := record.Summary
	wantSummary.Provenance.CreatedAt = time.Time{}
	gotSummary := restored.Summary
	gotSummary.Provenance.CreatedAt = time.Time{}
	if !reflect.DeepEqual(gotSummary, wantSummary) {
		t.Fatalf("BSON round-trip lost summary fields:\n got %+v\nwant %+v", gotSummary, wantSummary)
	}
}

func TestContextSummaryParsesSpecForm(t *testing.T) {
	raw := []byte(`{
		"goal": "近30天各店销量",
		"data_source": {
			"chat_id": "64b000000000000000000001", "source_id": "source-1", "binding_revision": 3,
			"database": "bi_agent",
			"selection": {"kind": "v2", "revision": 7, "objects": ["reporting.sales", "bi.orders"]}
		},
		"schema_facts": [{"object": "reporting.sales", "kind": "view", "key_columns": ["shop_id", "amount"], "notes": "按店铺汇总"}],
		"queries": [{"purpose": "近30天各店销量", "tables": ["bi.orders"], "sql_digest": "a1b2c3",
			"row_count": 6, "key_numbers": ["合计 126 件"], "status": "executed"}],
		"errors": [{"what": "response_format 不支持", "cause": "DeepSeek 无 json_schema", "fix": "去掉 response_format 重试", "resolved": true}],
		"user_preferences": {"execution_mode": "strict", "non_tech_mode": false, "model": "deepseek/deepseek-v4-flash",
			"reasoning_level": "off", "language": "zh-CN"},
		"decisions": ["口径按 paid_at 计算", "仅生成不执行"],
		"open_questions": ["是否需要按商品维度拆分"],
		"provenance": {"message_ids": ["64b000000000000000000002"], "range": ["2026-11-12T03:00Z", "2026-11-12T03:40Z"],
			"covered_count": 42, "model": "default/glm-5.3-flash", "created_at": "2026-11-12T03:40:00Z"}
	}`)
	var summary ContextSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("spec form unmarshal: %v", err)
	}
	if summary.Goal != "近30天各店销量" || summary.DataSource.SourceID != "source-1" ||
		summary.DataSource.BindingRevision != 3 || summary.DataSource.Selection == nil ||
		summary.DataSource.Selection.Revision != 7 {
		t.Fatalf("data source fields not parsed: %+v", summary.DataSource)
	}
	if len(summary.Queries) != 1 || summary.Queries[0].SQLDigest != "a1b2c3" ||
		summary.Queries[0].RowCount != 6 || summary.Queries[0].Status != "executed" {
		t.Fatalf("query fields not parsed: %+v", summary.Queries)
	}
	if summary.UserPreferences.ExecutionMode != "strict" || summary.UserPreferences.Language != "zh-CN" {
		t.Fatalf("preferences not parsed: %+v", summary.UserPreferences)
	}
	if summary.Provenance.CoveredCount != 42 || len(summary.Provenance.MessageIDs) != 1 ||
		len(summary.Provenance.Range) != 2 || summary.Provenance.Range[0] != "2026-11-12T03:00Z" {
		t.Fatalf("provenance not parsed: %+v", summary.Provenance)
	}
}

func TestFilterForChatKeepsOnlySourceAgnosticFields(t *testing.T) {
	summary := &ChatContextSummary{
		Revision: 3,
		Summary: ContextSummary{
			Goal:            "统计近30天销量",
			SchemaFacts:     []ContextSummarySchemaFact{{Object: "reporting.sales"}},
			Queries:         []ContextSummaryQuery{{Purpose: "销量", SQLDigest: "abc"}},
			Errors:          []ContextSummaryError{{What: "权限不足"}},
			Decisions:       []string{"按 paid_at 计算"},
			OpenQuestions:   []string{"是否按商品拆分"},
			UserPreferences: ContextSummaryPreferences{ExecutionMode: "strict"},
			DataSource:      ContextSummaryDataSource{SourceID: "src-a", BindingRevision: 2},
		},
		Rendered:    "## 目标 ...",
		StaleSource: false,
	}
	sameSource := &Chat{SourceID: "src-a", SourceBindingRevision: 2}
	filtered := summary.FilterForChat(sameSource)
	if filtered.StaleSource || filtered.Rendered == "" || len(filtered.Summary.SchemaFacts) != 1 {
		t.Fatalf("same source must keep the summary intact: %+v", filtered)
	}

	rebound := &Chat{SourceID: "src-b", SourceBindingRevision: 1}
	filtered = summary.FilterForChat(rebound)
	if !filtered.StaleSource {
		t.Fatal("rebind must mark the summary stale")
	}
	if filtered.Summary.Goal != "统计近30天销量" || len(filtered.Summary.OpenQuestions) != 1 {
		t.Fatal("goal and open questions must survive a rebind")
	}
	if len(filtered.Summary.SchemaFacts) != 0 || len(filtered.Summary.Queries) != 0 || len(filtered.Summary.Errors) != 0 || len(filtered.Summary.Decisions) != 0 {
		t.Fatalf("structural fields must be dropped: %+v", filtered.Summary)
	}
	if filtered.Rendered != "" {
		t.Fatal("stale rendered text must be cleared")
	}
	// The original record is not mutated.
	if len(summary.Summary.Queries) != 1 || summary.StaleSource {
		t.Fatal("filter mutated the stored summary")
	}
}
