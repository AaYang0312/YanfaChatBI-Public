package services

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"yanfachatbi/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func digestTestQuery(id primitive.ObjectID, purpose, tables, result string, executed bool, actionAt string) models.Query {
	q := models.Query{
		ID:          id,
		Description: purpose,
		IsExecuted:  executed,
	}
	if tables != "" {
		t := tables
		q.Tables = &t
	}
	if result != "" {
		r := result
		q.ExecutionResult = &r
	}
	if actionAt != "" {
		a := actionAt
		q.ActionAt = &a
	}
	return q
}

func digestRowsJSON(t *testing.T, rows []map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"results": rows})
	if err != nil {
		t.Fatalf("marshal test rows: %v", err)
	}
	return string(b)
}

// digestGridResult builds a {"results":[{...}]} payload with the given number
// of columns and rows, every cell holding the same string.
func digestGridResult(t *testing.T, columns, rows int, cell string) string {
	t.Helper()
	records := make([]map[string]any, 0, rows)
	for r := 0; r < rows; r++ {
		record := make(map[string]any, columns)
		for c := 0; c < columns; c++ {
			record[fmt.Sprintf("c%02d", c)] = cell
		}
		records = append(records, record)
	}
	return digestRowsJSON(t, records)
}

func TestBuildResultDigestsFiltersUnexecutedAndMissingResults(t *testing.T) {
	executedID := primitive.NewObjectID()
	emptyResult := ""

	queries := []models.Query{
		digestTestQuery(primitive.NewObjectID(), "not executed", "", `{"results":[{"a":"1"}]}`, false, ""),
		digestTestQuery(primitive.NewObjectID(), "nil result", "", "", true, ""),
		{
			ID:              primitive.NewObjectID(),
			Description:     "empty result string",
			IsExecuted:      true,
			ExecutionResult: &emptyResult,
		},
		digestTestQuery(primitive.NewObjectID(), "invalid json", "", `not-json`, true, ""),
		digestTestQuery(executedID, "executed", "bi.orders", `{"results":[{"a":"1"}]}`, true, ""),
	}

	digests := BuildResultDigests(queries)
	if len(digests) != 1 {
		t.Fatalf("expected exactly 1 digest, got %d: %+v", len(digests), digests)
	}
	if digests[0].QueryID != executedID.Hex() {
		t.Fatalf("unexpected query id: got %q want %q", digests[0].QueryID, executedID.Hex())
	}
	if digests[0].Purpose != "executed" {
		t.Fatalf("unexpected purpose: %q", digests[0].Purpose)
	}
}

func TestBuildResultDigestsParsesCommonResultShapes(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantCols  []string
		wantRows  [][]string
		wantCount int
	}{
		{
			name:      "top level array of objects",
			raw:       `[{"b":"x","a":"1"},{"b":"y","a":"2"}]`,
			wantCols:  []string{"a", "b"},
			wantRows:  [][]string{{"1", "x"}, {"2", "y"}},
			wantCount: 2,
		},
		{
			name:      "results key",
			raw:       `{"results":[{"a":"1","b":"x"},{"a":"2","b":"y"}]}`,
			wantCols:  []string{"a", "b"},
			wantRows:  [][]string{{"1", "x"}, {"2", "y"}},
			wantCount: 2,
		},
		{
			name:      "rows key with objects",
			raw:       `{"rows":[{"a":"1"}]}`,
			wantCols:  []string{"a"},
			wantRows:  [][]string{{"1"}},
			wantCount: 1,
		},
		{
			name:      "data key",
			raw:       `{"data":[{"a":"1"}]}`,
			wantCols:  []string{"a"},
			wantRows:  [][]string{{"1"}},
			wantCount: 1,
		},
		{
			name:      "columns with positional rows",
			raw:       `{"columns":["a","b"],"rows":[["1","2"],["3","4"]]}`,
			wantCols:  []string{"a", "b"},
			wantRows:  [][]string{{"1", "2"}, {"3", "4"}},
			wantCount: 2,
		},
		{
			name:      "positional rows without columns",
			raw:       `{"rows":[[10,20]]}`,
			wantCols:  []string{"column_1", "column_2"},
			wantRows:  [][]string{{"10", "20"}},
			wantCount: 1,
		},
		{
			name:      "single scalar object",
			raw:       `{"count":3}`,
			wantCols:  []string{"count"},
			wantRows:  [][]string{{"3"}},
			wantCount: 1,
		},
		{
			name:      "result wrapper",
			raw:       `{"result":[{"a":"1"}]}`,
			wantCols:  []string{"a"},
			wantRows:  [][]string{{"1"}},
			wantCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := digestTestQuery(primitive.NewObjectID(), "shape", "", tc.raw, true, "")
			digests := BuildResultDigests([]models.Query{q})
			if len(digests) != 1 {
				t.Fatalf("expected 1 digest, got %d", len(digests))
			}
			d := digests[0]
			if d.RowCount != tc.wantCount {
				t.Fatalf("row_count: got %d want %d", d.RowCount, tc.wantCount)
			}
			if !reflect.DeepEqual(d.Columns, tc.wantCols) {
				t.Fatalf("columns: got %#v want %#v", d.Columns, tc.wantCols)
			}
			if !reflect.DeepEqual(d.Rows, tc.wantRows) {
				t.Fatalf("rows: got %#v want %#v", d.Rows, tc.wantRows)
			}
		})
	}
}

func TestBuildResultDigestsComputesRowsColumnsAndMetadata(t *testing.T) {
	id := primitive.NewObjectID()
	raw := `{"results":[
		{"shop_id":"S-1001","sales_quantity":"126","amount":12.5},
		{"shop_id":"S-1002","sales_quantity":"98","amount":2.25},
		{"shop_id":"S-1003","sales_quantity":"0","amount":0},
		{"shop_id":"S-1004","sales_quantity":"1","amount":1},
		{"shop_id":"S-1005","sales_quantity":"2","amount":2},
		{"shop_id":"S-1006","sales_quantity":"3","amount":3}
	]}`
	q := digestTestQuery(id, "近30天各店销量", "bi.orders,bi.shops", raw, true, "2026-11-12T10:00:00Z")

	digests := BuildResultDigests([]models.Query{q})
	if len(digests) != 1 {
		t.Fatalf("expected 1 digest, got %d", len(digests))
	}
	d := digests[0]

	if d.QueryID != id.Hex() {
		t.Fatalf("query_id: got %q want %q", d.QueryID, id.Hex())
	}
	if d.Purpose != "近30天各店销量" {
		t.Fatalf("purpose: got %q", d.Purpose)
	}
	if !reflect.DeepEqual(d.Tables, []string{"bi.orders", "bi.shops"}) {
		t.Fatalf("tables: got %#v", d.Tables)
	}
	if d.RowCount != 6 {
		t.Fatalf("row_count: got %d want 6", d.RowCount)
	}
	if len(d.Rows) != 6 {
		t.Fatalf("displayed rows: got %d want 6", len(d.Rows))
	}
	wantCols := []string{"amount", "sales_quantity", "shop_id"}
	if !reflect.DeepEqual(d.Columns, wantCols) {
		t.Fatalf("columns: got %#v want %#v", d.Columns, wantCols)
	}
	if !reflect.DeepEqual(d.Rows[0], []string{"12.5", "126", "S-1001"}) {
		t.Fatalf("first row: got %#v", d.Rows[0])
	}
	if d.Truncated {
		t.Fatalf("digest must not be truncated when it fits the row budget")
	}
	if d.ExecutedAt != "2026-11-12T10:00:00Z" {
		t.Fatalf("executed_at: got %q", d.ExecutedAt)
	}
}

func TestBuildResultDigestsLimitsRowsToTen(t *testing.T) {
	raw := digestRowsJSON(t, func() []map[string]any {
		rows := make([]map[string]any, 0, 12)
		for i := 0; i < 12; i++ {
			rows = append(rows, map[string]any{"n": fmt.Sprintf("%d", i)})
		}
		return rows
	}())
	q := digestTestQuery(primitive.NewObjectID(), "twelve rows", "", raw, true, "")

	digests := BuildResultDigests([]models.Query{q})
	if len(digests) != 1 {
		t.Fatalf("expected 1 digest, got %d", len(digests))
	}
	d := digests[0]
	if d.RowCount != 12 {
		t.Fatalf("row_count: got %d want 12", d.RowCount)
	}
	if len(d.Rows) != ResultDigestRowsPerDigest {
		t.Fatalf("displayed rows: got %d want %d", len(d.Rows), ResultDigestRowsPerDigest)
	}
	if !d.Truncated {
		t.Fatalf("expected truncated=true for a 12-row result")
	}
	if d.Rows[0][0] != "0" || d.Rows[9][0] != "9" {
		t.Fatalf("expected first ten rows, got first=%#v last=%#v", d.Rows[0], d.Rows[9])
	}
}

func TestBuildResultDigestsTruncatesLongCellsAndKeepsUTF8(t *testing.T) {
	long := strings.Repeat("数", 250)
	raw := digestRowsJSON(t, []map[string]any{
		{"note": long, "short": "ok"},
	})
	q := digestTestQuery(primitive.NewObjectID(), "long cell", "", raw, true, "")

	digests := BuildResultDigests([]models.Query{q})
	if len(digests) != 1 {
		t.Fatalf("expected 1 digest, got %d", len(digests))
	}
	d := digests[0]
	if len(d.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(d.Rows))
	}
	note := d.Rows[0][0]
	if !utf8.ValidString(note) {
		t.Fatalf("truncated cell is not valid UTF-8: %q", note)
	}
	if !strings.HasSuffix(note, "…[truncated]") {
		t.Fatalf("truncated cell must carry a truncation marker: %q", note)
	}
	body := strings.TrimSuffix(note, "…[truncated]")
	if utf8.RuneCountInString(body) != ResultDigestMaxCellChars {
		t.Fatalf("cell body: got %d runes want %d", utf8.RuneCountInString(body), ResultDigestMaxCellChars)
	}
	if body != strings.Repeat("数", ResultDigestMaxCellChars) {
		t.Fatalf("cell body must keep the first %d runes intact", ResultDigestMaxCellChars)
	}
	if d.Rows[0][1] != "ok" {
		t.Fatalf("short cell must be unchanged: %q", d.Rows[0][1])
	}
	if !d.Truncated {
		t.Fatalf("expected truncated=true after cell truncation")
	}
}

func TestBuildResultDigestsAggregatesNumericColumns(t *testing.T) {
	raw := `{"results":[
		{"shop_id":"S-1001","sales_quantity":"126","amount":12.5,"label":"a"},
		{"shop_id":"S-1002","sales_quantity":"98","amount":2.25,"label":"b"},
		{"shop_id":"S-1003","sales_quantity":null,"amount":"1.5","label":null}
	]}`
	q := digestTestQuery(primitive.NewObjectID(), "aggregate", "", raw, true, "")

	digests := BuildResultDigests([]models.Query{q})
	if len(digests) != 1 {
		t.Fatalf("expected 1 digest, got %d", len(digests))
	}
	d := digests[0]
	if got := d.Aggregates["sales_quantity"].Sum; got != "224" {
		t.Fatalf("sales_quantity sum: got %q want %q", got, "224")
	}
	if got := d.Aggregates["amount"].Sum; got != "16.25" {
		t.Fatalf("amount sum: got %q want %q", got, "16.25")
	}
	if _, ok := d.Aggregates["label"]; ok {
		t.Fatalf("text column must not be aggregated")
	}
	if _, ok := d.Aggregates["shop_id"]; ok {
		t.Fatalf("non-numeric shop_id column must not be aggregated")
	}
}

func TestBuildResultDigestsEnforcesPerDigestCharBudget(t *testing.T) {
	raw := digestGridResult(t, 10, 10, strings.Repeat("x", 200))
	q := digestTestQuery(primitive.NewObjectID(), "wide", "", raw, true, "")

	digests := BuildResultDigests([]models.Query{q})
	if len(digests) != 1 {
		t.Fatalf("expected 1 digest, got %d", len(digests))
	}
	d := digests[0]
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal digest: %v", err)
	}
	if n := utf8.RuneCount(encoded); n > ResultDigestMaxChars {
		t.Fatalf("digest JSON is %d chars, want <= %d", n, ResultDigestMaxChars)
	}
	if !json.Valid(encoded) {
		t.Fatalf("truncated digest must stay valid JSON")
	}
	if !d.Truncated {
		t.Fatalf("expected truncated=true after budget trimming")
	}
	if d.RowCount != 10 {
		t.Fatalf("row_count must describe the source result, got %d", d.RowCount)
	}
}

func TestBuildResultDigestsEnforcesTotalBudgetKeepingNewest(t *testing.T) {
	const queryCount = 20
	cell := strings.Repeat("x", 32)
	ids := make([]primitive.ObjectID, 0, queryCount)
	queries := make([]models.Query, 0, queryCount)
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < queryCount; i++ {
		id := primitive.NewObjectID()
		ids = append(ids, id)
		raw := digestGridResult(t, 10, 10, cell)
		queries = append(queries, digestTestQuery(
			id,
			fmt.Sprintf("query-%02d", i),
			"",
			raw,
			true,
			base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339),
		))
	}

	digests := BuildResultDigests(queries)
	if len(digests) == 0 {
		t.Fatalf("expected at least one digest to survive the total budget")
	}
	if len(digests) >= queryCount {
		t.Fatalf("expected the total budget to drop old digests, got all %d", len(digests))
	}
	if digests[0].QueryID != ids[queryCount-1].Hex() {
		t.Fatalf("newest query must be retained first, got %q", digests[0].QueryID)
	}

	total := 0
	for _, d := range digests {
		encoded, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal digest: %v", err)
		}
		if n := utf8.RuneCount(encoded); n > ResultDigestMaxChars {
			t.Fatalf("digest %q is %d chars, want <= %d", d.QueryID, n, ResultDigestMaxChars)
		}
		total += utf8.RuneCount(encoded)
	}
	if total > ResultDigestTotalMaxChars {
		t.Fatalf("digest JSON total is %d chars, want <= %d", total, ResultDigestTotalMaxChars)
	}
	block := RenderResultDigestBlock(digests)
	if n := utf8.RuneCountInString(block); n > ResultDigestTotalMaxChars {
		t.Fatalf("rendered digest block is %d chars, want <= %d", n, ResultDigestTotalMaxChars)
	}

	firstRetained := queryCount - len(digests)
	for _, d := range digests {
		idx := -1
		for i, id := range ids {
			if id.Hex() == d.QueryID {
				idx = i
				break
			}
		}
		if idx < firstRetained {
			t.Fatalf("old digest retained while newer ones fit: %q (index %d)", d.QueryID, idx)
		}
	}
}

func TestBuildResultDigestsOrdersByExecutionTimeDescending(t *testing.T) {
	oldID := primitive.NewObjectID()
	midID := primitive.NewObjectID()
	newID := primitive.NewObjectID()
	unknownID := primitive.NewObjectID()

	queries := []models.Query{
		digestTestQuery(midID, "mid", "", `{"results":[{"a":"1"}]}`, true, "2026-11-12T10:05:00Z"),
		digestTestQuery(oldID, "old", "", `{"results":[{"a":"1"}]}`, true, "2026-11-12T10:00:00Z"),
		digestTestQuery(unknownID, "unknown", "", `{"results":[{"a":"1"}]}`, true, ""),
		digestTestQuery(newID, "new", "", `{"results":[{"a":"1"}]}`, true, "2026-11-12T10:10:00Z"),
	}

	digests := BuildResultDigests(queries)
	if len(digests) != 4 {
		t.Fatalf("expected 4 digests, got %d", len(digests))
	}
	want := []string{newID.Hex(), midID.Hex(), oldID.Hex(), unknownID.Hex()}
	got := []string{digests[0].QueryID, digests[1].QueryID, digests[2].QueryID, digests[3].QueryID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order: got %#v want %#v", got, want)
	}
}

func TestBuildResultDigestsNoData(t *testing.T) {
	if digests := BuildResultDigests(nil); len(digests) != 0 {
		t.Fatalf("nil input must produce no digests, got %d", len(digests))
	}
	queries := []models.Query{
		digestTestQuery(primitive.NewObjectID(), "not executed", "", `{"results":[{"a":"1"}]}`, false, ""),
		digestTestQuery(primitive.NewObjectID(), "null result", "", "null", true, ""),
		digestTestQuery(primitive.NewObjectID(), "broken result", "", `{"results":`, true, ""),
	}
	if digests := BuildResultDigests(queries); len(digests) != 0 {
		t.Fatalf("queries without usable results must produce no digests, got %d", len(digests))
	}
}

func TestBuildResultDigestsKeepsEmptyResultSet(t *testing.T) {
	q := digestTestQuery(primitive.NewObjectID(), "empty set", "", `{"results":[]}`, true, "")
	digests := BuildResultDigests([]models.Query{q})
	if len(digests) != 1 {
		t.Fatalf("expected 1 digest for an executed empty result, got %d", len(digests))
	}
	if digests[0].RowCount != 0 || len(digests[0].Rows) != 0 {
		t.Fatalf("empty result must have zero rows, got count=%d rows=%d", digests[0].RowCount, len(digests[0].Rows))
	}
}

func TestBuildResultDigestsWithOptionsDecryptsExecutionResult(t *testing.T) {
	raw := "enc:" + `{"results":[{"a":"1"}]}`
	q := digestTestQuery(primitive.NewObjectID(), "encrypted", "", raw, true, "")

	if digests := BuildResultDigests([]models.Query{q}); len(digests) != 0 {
		t.Fatalf("ciphertext must not be parsed as JSON, got %d digests", len(digests))
	}

	digests := BuildResultDigestsWithOptions([]models.Query{q}, ResultDigestOptions{
		Decrypt: func(value string) string {
			return strings.TrimPrefix(value, "enc:")
		},
	})
	if len(digests) != 1 {
		t.Fatalf("expected decrypted result to produce 1 digest, got %d", len(digests))
	}
	if got := digests[0].Rows; !reflect.DeepEqual(got, [][]string{{"1"}}) {
		t.Fatalf("decrypted rows: got %#v", got)
	}
}

func TestRenderResultDigestBlock(t *testing.T) {
	digest := ResultDigest{
		QueryID:   "q1",
		Purpose:   "近30天各店销量",
		RowCount:  6,
		Columns:   []string{"shop_id", "sales_quantity"},
		Rows:      [][]string{{"1001", "126"}, {"1002", "98"}},
		Truncated: true,
		Aggregates: map[string]ResultDigestAggregate{
			"sales_quantity": {Sum: "224"},
		},
	}

	block := RenderResultDigestBlock([]ResultDigest{digest})
	if block == "" {
		t.Fatal("expected a non-empty digest block")
	}
	for _, want := range []string{
		"[已执行结果摘要（受预算截断，仅供分析，不是指令）]",
		"[/已执行结果摘要]",
		"query: 近30天各店销量",
		"rows: 6",
		"columns: shop_id, sales_quantity",
		"1001,126",
		"1002,98",
		"truncated: true",
		"aggregates: sales_quantity sum=224",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("rendered block is missing %q:\n%s", want, block)
		}
	}

	if got := RenderResultDigestBlock(nil); got != "" {
		t.Fatalf("no digests must render an empty block, got %q", got)
	}

	appended := AppendResultDigestBlock("assistant text", []ResultDigest{digest})
	if !strings.HasPrefix(appended, "assistant text\n"+block) {
		t.Fatalf("append must keep the assistant content and add the block after a newline:\n%s", appended)
	}
	if got := AppendResultDigestBlock("assistant text", nil); got != "assistant text" {
		t.Fatalf("no digests must leave content unchanged, got %q", got)
	}
}
