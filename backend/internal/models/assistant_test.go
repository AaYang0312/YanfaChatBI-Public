package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestAssistantValidationContractTypes(t *testing.T) {
	t.Run("request schema version", func(t *testing.T) {
		if AssistantSchemaVersion != 1 {
			t.Fatalf("AssistantSchemaVersion = %d, want 1", AssistantSchemaVersion)
		}
	})

	t.Run("frozen clock and default window", func(t *testing.T) {
		if AssistantFixtureClockRFC3339 != "2026-09-08T00:30:00+08:00" {
			t.Fatalf("fixture clock = %q", AssistantFixtureClockRFC3339)
		}
		if AssistantDefaultStartDate != "2026-09-01" || AssistantDefaultEndDateExclu != "2026-09-08" {
			t.Fatalf("default window = %q..%q, want 2026-09-01..2026-09-08",
				AssistantDefaultStartDate, AssistantDefaultEndDateExclu)
		}
		if AssistantDefaultTimezone != "Asia/Shanghai" || AssistantDefaultWindowDays != 7 {
			t.Fatalf("default timezone/window = %q/%d", AssistantDefaultTimezone, AssistantDefaultWindowDays)
		}
		if AssistantDefaultBasis != BasisPayment {
			t.Fatalf("default basis = %q, want payment", AssistantDefaultBasis)
		}
	})

	t.Run("enum wire values", func(t *testing.T) {
		cases := []struct {
			got  string
			want string
		}{
			{string(WorkflowProductAnalysis), "product_analysis"},
			{string(WorkflowSalesComparison), "sales_comparison"},
			{string(BasisPayment), "payment"},
			{string(BasisOutstock), "outstock"},
			{string(ScopeModeAll), "all"},
			{string(ScopeModeSelected), "selected"},
			{string(GroupByPlatform), "platform"},
			{string(GroupByShop), "shop"},
			{string(MetricAveragePrice), "avg_price"},
			{string(EntityTypeProduct), "product"},
		}
		for _, c := range cases {
			if c.got != c.want {
				t.Fatalf("enum = %q, want %q", c.got, c.want)
			}
		}

		wantMetrics := []MetricID{MetricQuantity, MetricSales, MetricCost, MetricMargin,
			MetricMarginRate, MetricAveragePrice, MetricRefund}
		if got := SupportedMetrics(); !reflect.DeepEqual(got, wantMetrics) {
			t.Fatalf("SupportedMetrics() = %v, want %v", got, wantMetrics)
		}
		if MetricID("shop_password").Valid() {
			t.Fatal("a column-like metric identifier must not be valid")
		}
		if !MetricMarginRate.Valid() {
			t.Fatal("margin_rate must be a supported metric")
		}
	})

	t.Run("run statuses", func(t *testing.T) {
		want := []RunStatus{RunStatusCreated, RunStatusRunning, RunStatusCompleted,
			RunStatusFailed, RunStatusCancelled, RunStatusInterrupted}
		if got := SupportedRunStatuses(); !reflect.DeepEqual(got, want) {
			t.Fatalf("SupportedRunStatuses() = %v, want %v", got, want)
		}
		if RunStatus("needs_input").Valid() {
			t.Fatal("needs_input is a resolve status, never a run status")
		}
		for _, terminal := range []RunStatus{RunStatusCompleted, RunStatusFailed, RunStatusCancelled, RunStatusInterrupted} {
			if !terminal.IsTerminal() {
				t.Fatalf("%s must be terminal", terminal)
			}
		}
		for _, open := range []RunStatus{RunStatusCreated, RunStatusRunning} {
			if open.IsTerminal() {
				t.Fatalf("%s must not be terminal", open)
			}
		}
	})

	t.Run("completed outcomes", func(t *testing.T) {
		want := []RunOutcome{OutcomeComplete, OutcomePartial, OutcomeEmpty, OutcomeUnavailable}
		if got := SupportedRunOutcomes(); !reflect.DeepEqual(got, want) {
			t.Fatalf("SupportedRunOutcomes() = %v, want %v", got, want)
		}
	})

	t.Run("metric statuses", func(t *testing.T) {
		want := []MetricStatus{MetricStatusAvailable, MetricStatusPartial, MetricStatusUnavailable}
		if got := SupportedMetricStatuses(); !reflect.DeepEqual(got, want) {
			t.Fatalf("SupportedMetricStatuses() = %v, want %v", got, want)
		}
	})

	t.Run("fact row vocabulary", func(t *testing.T) {
		wantCoverage := []CoverageStatus{CoverageStatusCovered, CoverageStatusMissing,
			CoverageStatusStale, CoverageStatusUnknown}
		if got := SupportedCoverageStatuses(); !reflect.DeepEqual(got, wantCoverage) {
			t.Fatalf("SupportedCoverageStatuses() = %v, want %v", got, wantCoverage)
		}
		wantBundle := []BundleRole{BundleRoleNone, BundleRoleParent, BundleRoleChild, BundleRoleGift}
		if got := SupportedBundleRoles(); !reflect.DeepEqual(got, wantBundle) {
			t.Fatalf("SupportedBundleRoles() = %v, want %v", got, wantBundle)
		}
	})

	t.Run("error codes and http mapping", func(t *testing.T) {
		want := []ValidationErrorCode{
			"invalid_schema", "invalid_date_range", "invalid_enum", "unknown_field",
			"ambiguous_product", "missing_field", "conflicting_scope", "unauthorized_scope",
			"request_conflict", "capability_unavailable", "source_unavailable", "timeout",
		}
		if got := SupportedValidationErrorCodes(); !reflect.DeepEqual(got, want) {
			t.Fatalf("SupportedValidationErrorCodes() = %v, want %v", got, want)
		}
		mapped := map[ValidationErrorCode]int{
			ErrorCodeInvalidSchema:     400,
			ErrorCodeUnknownField:      400,
			ErrorCodeInvalidDateRange:  422,
			ErrorCodeInvalidEnum:       422,
			ErrorCodeMissingField:      422,
			ErrorCodeConflictingScope:  422,
			ErrorCodeAmbiguousProduct:  422,
			ErrorCodeUnauthorizedScope: 403,
			ErrorCodeRequestConflict:   409,
		}
		for code, wantStatus := range mapped {
			got, ok := code.HTTPStatus()
			if !ok || got != wantStatus {
				t.Fatalf("%s.HTTPStatus() = %d,%v want %d,true", code, got, ok, wantStatus)
			}
		}
		for _, code := range []ValidationErrorCode{ErrorCodeCapabilityUnavailable,
			ErrorCodeSourceUnavailable, ErrorCodeTimeout} {
			if got, ok := code.HTTPStatus(); ok {
				t.Fatalf("%s must stay unmapped at request validation, got %d", code, got)
			}
		}
	})

	t.Run("access context never binds client json", func(t *testing.T) {
		var access AccessContext
		raw := `{"OwnerID":"evil","ChatID":"evil","ConnectionID":"evil","ApprovedScope":{"All":true}}`
		if err := json.Unmarshal([]byte(raw), &access); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !reflect.DeepEqual(access, AccessContext{}) {
			t.Fatalf("client-supplied identity bound into AccessContext: %+v", access)
		}
	})

	t.Run("needs input resolution has no metric or report field", func(t *testing.T) {
		resolution := Resolution{
			Status:        ResolveStatusNeedsInput,
			MissingFields: []string{"product.product_id"},
			Candidates:    []Candidate{{EntityType: EntityTypeProduct, EntityID: "P2", Label: "商品二"}},
		}
		raw, err := json.Marshal(resolution)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := sortedKeys(keys); !reflect.DeepEqual(got, []string{"candidates", "missing_fields", "status"}) {
			t.Fatalf("needs_input keys = %v", got)
		}
	})
}

func TestAssistantValidationDecimalJSON(t *testing.T) {
	rate := decimal.RequireFromString("0.4")
	zero := decimal.Zero

	available, err := json.Marshal(MetricValue{
		MetricID: MetricMarginRate, Status: MetricStatusAvailable, Value: &rate,
	})
	if err != nil {
		t.Fatalf("marshal available: %v", err)
	}
	if string(available) != `{"metric_id":"margin_rate","status":"available","value":"0.4"}` {
		t.Fatalf("available metric json = %s", available)
	}

	zeroed, err := json.Marshal(MetricValue{
		MetricID: MetricMarginRate, Status: MetricStatusAvailable, Value: &zero,
	})
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	if string(zeroed) != `{"metric_id":"margin_rate","status":"available","value":"0"}` {
		t.Fatalf("zero metric json = %s, want value \"0\" not null", zeroed)
	}

	nullValue, err := json.Marshal(MetricValue{
		MetricID: MetricMarginRate, Status: MetricStatusUnavailable, Value: nil,
		ReasonCodes: []string{"divide_by_zero"}, CoverageRef: "cov-1",
	})
	if err != nil {
		t.Fatalf("marshal null: %v", err)
	}
	if string(nullValue) != `{"metric_id":"margin_rate","status":"unavailable","value":null,"reason_codes":["divide_by_zero"],"coverage_ref":"cov-1"}` {
		t.Fatalf("null metric json = %s", nullValue)
	}

	nine := decimal.RequireFromString("0.30000000000000004")
	exact, err := json.Marshal(MetricValue{
		MetricID: MetricMarginRate, Status: MetricStatusPartial, Value: &nine, Currency: "CNY",
	})
	if err != nil {
		t.Fatalf("marshal exact: %v", err)
	}
	if string(exact) != `{"metric_id":"margin_rate","status":"partial","value":"0.30000000000000004","currency":"CNY"}` {
		t.Fatalf("percent/ratio json must stay an exact decimal string, got %s", exact)
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func TestAssistantReportShapes(t *testing.T) {
	t.Run("named types exist with contract json keys", func(t *testing.T) {
		zero := decimal.Zero
		rate := decimal.RequireFromString("0.4")
		asOf := time.Date(2026, 9, 8, 0, 30, 0, 0, time.FixedZone("CST", 8*3600))
		report := AnalysisReport{
			ReportID:           "run-1",
			SchemaVersion:      AssistantSchemaVersion,
			Workflow:           WorkflowProductAnalysis,
			Basis:              BasisPayment,
			RequestedScope:     Scope{Mode: ScopeModeAll},
			ActualScope:        Scope{Mode: ScopeModeSelected, ShopIDs: []string{"S1"}},
			Window:             Period{DateRange: DateRange{StartDate: AssistantDefaultStartDate, EndDateExclusive: AssistantDefaultEndDateExclu}, Timezone: AssistantDefaultTimezone},
			WindowStart:        asOf.AddDate(0, 0, -7),
			WindowEndExclusive: asOf.Truncate(24 * time.Hour),
			ContractVersion:    "fixture-v1",
			Outcome:            OutcomeComplete,
			Coverage: Coverage{
				Requested: []CoverageEntity{{EntityType: EntityTypeShop, EntityID: "S1", Status: CoverageStatusCovered, CoverageRef: "cov:S1"}},
				Included:  []CoverageEntity{{EntityType: EntityTypeShop, EntityID: "S1", Status: CoverageStatusCovered, CoverageRef: "cov:S1"}},
				Excluded:  []CoverageEntity{},
				DataAsOf:  asOf,
				Ref:       "cov:payment",
			},
			Totals: []MetricValue{
				{MetricID: MetricSales, Status: MetricStatusAvailable, Value: decimalPtr("120")},
				{MetricID: MetricMargin, Status: MetricStatusAvailable, Value: decimalPtr("48")},
				{MetricID: MetricMarginRate, Status: MetricStatusAvailable, Value: &rate},
				{MetricID: MetricAveragePrice, Status: MetricStatusUnavailable, Value: nil, ReasonCodes: []string{ReasonDivideByZero}},
			},
			Table: ReportTable{
				Dimension: "shop",
				Rows: []ReportRow{{
					EntityType: EntityTypeShop, EntityID: "S1", Rank: 1,
					Metrics: []MetricValue{{MetricID: MetricSales, Status: MetricStatusAvailable, Value: &zero}},
				}},
			},
			Charts: []ChartSpec{{
				ChartID: "chart-bar", Kind: ChartKindBar, Title: "商品销售额",
				MetricIDs: []MetricID{MetricSales}, Dimension: "entity", DataRef: "table.rows",
				Points: []ChartPoint{{Dimension: "S1", EntityID: "S1", MetricID: MetricSales, Value: nil, Status: MetricStatusUnavailable}},
			}},
			Refunds: RefundSummary{
				Label: AssistantMarginRefundLabel, Amount: decimalPtr("10"), Status: MetricStatusAvailable,
				OccurrenceWindow: DateRange{StartDate: AssistantDefaultStartDate, EndDateExclusive: AssistantDefaultEndDateExclu},
			},
			Evidence:    []EvidenceRef{{RefID: "snap:fixture-v1", Kind: EvidenceKindSnapshot, ContractVersion: "fixture-v1"}},
			DataAsOf:    asOf,
			MarginLabel: AssistantMarginRefundLabel,
		}

		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		body := string(raw)
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, want := range []string{
			"report_id", "workflow", "basis", "requested_scope", "actual_scope",
			"window", "window_start", "window_end_exclusive", "outcome", "coverage",
			"totals", "table", "charts", "refunds", "margin_label",
		} {
			if _, ok := keys[want]; !ok {
				t.Fatalf("AnalysisReport missing json key %s; keys=%v", want, sortedKeys(keys))
			}
		}
		for _, banned := range []string{
			"owner_id", "chat_id", "request_fingerprint", "fingerprint", "message_id",
			"stage", "revision", "executing_instance", "cancelled", "ciphertext",
			"encrypted_result", "bson", "mongo",
		} {
			if _, ok := keys[banned]; ok {
				t.Fatalf("run persistence field %s leaked into AnalysisReport json", banned)
			}
			if strings.Contains(body, `"`+banned+`"`) {
				t.Fatalf("run persistence token %s present in json %s", banned, body)
			}
		}
		if !strings.Contains(body, `"value":"120"`) || !strings.Contains(body, `"value":"0"`) {
			t.Fatalf("decimal strings missing: %s", body)
		}
		if !strings.Contains(body, `"value":null`) {
			t.Fatalf("null value missing: %s", body)
		}
		if strings.Contains(body, `"value":120`) || strings.Contains(body, `"value":0.4`) {
			t.Fatalf("numeric decimal leaked: %s", body)
		}
		if !strings.Contains(body, `"未扣退款"`) && !strings.Contains(body, "未扣退款") {
			if report.MarginLabel != "未扣退款" {
				t.Fatalf("margin label = %q", report.MarginLabel)
			}
		}
		if AssistantMarginRefundLabel != "未扣退款" {
			t.Fatalf("AssistantMarginRefundLabel = %q", AssistantMarginRefundLabel)
		}
	})

	t.Run("chart kinds are bar or line only", func(t *testing.T) {
		if got := SupportedChartKinds(); !reflect.DeepEqual(got, []ChartKind{ChartKindBar, ChartKindLine}) {
			t.Fatalf("SupportedChartKinds() = %v", got)
		}
		if ChartKind("pie").Valid() {
			t.Fatal("pie must not be a valid chart kind")
		}
	})

	t.Run("null is distinct from zero on chart points", func(t *testing.T) {
		zero := decimal.Zero
		nullJSON, err := json.Marshal(ChartPoint{Dimension: "S3", MetricID: MetricSales, Status: MetricStatusUnavailable})
		if err != nil {
			t.Fatalf("marshal null point: %v", err)
		}
		zeroJSON, err := json.Marshal(ChartPoint{Dimension: "S1", MetricID: MetricSales, Status: MetricStatusAvailable, Value: &zero})
		if err != nil {
			t.Fatalf("marshal zero point: %v", err)
		}
		if !strings.Contains(string(nullJSON), `"value":null`) {
			t.Fatalf("null point json = %s", nullJSON)
		}
		if !strings.Contains(string(zeroJSON), `"value":"0"`) {
			t.Fatalf("zero point json = %s", zeroJSON)
		}
		if string(nullJSON) == string(zeroJSON) {
			t.Fatal("null and zero chart points marshaled identically")
		}
	})

	t.Run("coverage includes included and excluded", func(t *testing.T) {
		raw, err := json.Marshal(Coverage{
			Included: []CoverageEntity{{EntityID: "S1", EntityType: EntityTypeShop, Status: CoverageStatusCovered, CoverageRef: "cov:S1"}},
			Excluded: []CoverageEntity{{EntityID: "S3", EntityType: EntityTypeShop, Status: CoverageStatusUnknown, CoverageRef: "cov:S3", ReasonCodes: []string{ReasonCoverageMissing}}},
		})
		if err != nil {
			t.Fatalf("marshal coverage: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		for _, want := range []string{"requested", "expected", "covered", "missing", "stale", "unknown", "included", "excluded"} {
			if _, ok := keys[want]; !ok {
				t.Fatalf("Coverage missing %s", want)
			}
		}
	})
}

func decimalPtr(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

func TestAssistantExplanationShape(t *testing.T) {
	chart := ChartSpec{
		ChartID:         "chart-bar",
		Kind:            ChartKindBar,
		MetricIDs:       []MetricID{MetricSales},
		ExplanationRefs: []string{"explanation:sales"},
	}
	report := AnalysisReport{
		ExplanationStatus: ExplanationStatusAvailable,
		Explanations: []Explanation{{
			RefID:        "explanation:sales",
			MetricID:     MetricSales,
			Value:        "120",
			Text:         "销售额为120。",
			EvidenceRefs: []string{"snap:fixture-v1"},
		}},
		Charts: []ChartSpec{chart},
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal explanation report: %v", err)
	}
	var shape struct {
		ExplanationStatus ExplanationStatus `json:"explanation_status"`
		Explanations      []Explanation     `json:"explanations"`
		Charts            []struct {
			ExplanationRefs []string `json:"explanation_refs"`
		} `json:"charts"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("unmarshal explanation report: %v", err)
	}
	if shape.ExplanationStatus != ExplanationStatusAvailable || !shape.ExplanationStatus.Valid() {
		t.Fatalf("explanation_status = %q", shape.ExplanationStatus)
	}
	if len(shape.Explanations) != 1 || shape.Explanations[0].Value != "120" || shape.Explanations[0].MetricID != MetricSales {
		t.Fatalf("explanations = %#v", shape.Explanations)
	}
	if len(shape.Charts) != 1 || !reflect.DeepEqual(shape.Charts[0].ExplanationRefs, []string{"explanation:sales"}) {
		t.Fatalf("chart explanation refs = %#v", shape.Charts)
	}
	if ExplanationStatus("unknown").Valid() {
		t.Fatal("unknown explanation status must be invalid")
	}
}

// TestAssistantReportJSONDoesNotMutateSource pins the JSON boundary contract:
// encoding normalizes the wire output (required lists as [], evidence times in
// UTC, exact decimals) without writing to the encoded report or any report
// sharing its slice backing arrays.
func TestAssistantReportJSONDoesNotMutateSource(t *testing.T) {
	exact := decimal.RequireFromString("120.000000000000000000000000000000000000001")
	readAt := time.Date(2026, 9, 8, 0, 30, 0, 0, time.FixedZone("CST", 8*3600))
	report := AnalysisReport{
		Totals: []MetricValue{{MetricID: MetricSales, Status: MetricStatusAvailable, Value: &exact}},
		Table:  ReportTable{Rows: []ReportRow{{EntityType: EntityTypeShop, EntityID: "S1"}}},
		Charts: []ChartSpec{{ChartID: "chart-1", Kind: ChartKindBar}},
		Explanations: []Explanation{{
			RefID: "exp-1", MetricID: MetricSales, Value: "120", Text: "销售额为120。",
		}},
		Evidence: []EvidenceRef{{RefID: "snap:fixture-v1", Kind: EvidenceKindSnapshot, SnapshotReadAt: readAt}},
	}
	alias := report // shares Charts, Table.Rows, Explanations, Evidence backing arrays

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var wire struct {
		Totals []struct {
			Value *string `json:"value"`
		} `json:"totals"`
		Table struct {
			Rows []struct {
				Metrics json.RawMessage `json:"metrics"`
			} `json:"rows"`
		} `json:"table"`
		Charts []struct {
			MetricIDs json.RawMessage `json:"metric_ids"`
			Points    json.RawMessage `json:"points"`
		} `json:"charts"`
		Explanations []struct {
			EvidenceRefs json.RawMessage `json:"evidence_refs"`
		} `json:"explanations"`
		Evidence []struct {
			SnapshotReadAt string `json:"snapshot_read_at"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode report JSON: %v", err)
	}
	if len(wire.Totals) != 1 || wire.Totals[0].Value == nil || *wire.Totals[0].Value != "120.000000000000000000000000000000000000001" {
		t.Fatalf("exact decimal changed on the wire: %s", raw)
	}
	if len(wire.Table.Rows) != 1 || string(wire.Table.Rows[0].Metrics) != "[]" {
		t.Fatalf("row metrics must marshal as []: %s", raw)
	}
	if len(wire.Charts) != 1 || string(wire.Charts[0].MetricIDs) != "[]" || string(wire.Charts[0].Points) != "[]" {
		t.Fatalf("chart lists must marshal as []: %s", raw)
	}
	if len(wire.Explanations) != 1 || string(wire.Explanations[0].EvidenceRefs) != "[]" {
		t.Fatalf("explanation evidence refs must marshal as []: %s", raw)
	}
	if len(wire.Evidence) != 1 || wire.Evidence[0].SnapshotReadAt != "2026-09-07T16:30:00Z" {
		t.Fatalf("evidence snapshot time must marshal in UTC: %s", raw)
	}

	for name, view := range map[string]AnalysisReport{"source": report, "alias": alias} {
		if view.Charts[0].MetricIDs != nil || view.Charts[0].Points != nil {
			t.Fatalf("%s charts mutated by marshaling: %#v", name, view.Charts[0])
		}
		if view.Table.Rows[0].Metrics != nil {
			t.Fatalf("%s row metrics mutated by marshaling", name)
		}
		if view.Explanations[0].EvidenceRefs != nil {
			t.Fatalf("%s explanation evidence refs mutated by marshaling", name)
		}
		if view.Evidence[0].SnapshotReadAt.Location() != readAt.Location() {
			t.Fatalf("%s evidence time location mutated by marshaling: %v", name, view.Evidence[0].SnapshotReadAt.Location())
		}
		if view.Totals[0].Value == nil || !view.Totals[0].Value.Equal(exact) {
			t.Fatalf("%s total decimal mutated by marshaling: %v", name, view.Totals[0].Value)
		}
	}
}

func TestAssistantReportJSONRequiredArrays(t *testing.T) {
	t.Run("empty report lists are arrays", func(t *testing.T) {
		report := AnalysisReport{
			Charts:       []ChartSpec{{ChartID: "empty-chart", Kind: ChartKindBar}},
			Explanations: []Explanation{{RefID: "empty-explanation"}},
		}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		var wire struct {
			Coverage map[string]json.RawMessage `json:"coverage"`
			Totals   json.RawMessage            `json:"totals"`
			Table    struct {
				Rows json.RawMessage `json:"rows"`
			} `json:"table"`
			Charts []struct {
				MetricIDs json.RawMessage `json:"metric_ids"`
				Points    json.RawMessage `json:"points"`
			} `json:"charts"`
			Explanations []struct {
				EvidenceRefs json.RawMessage `json:"evidence_refs"`
			} `json:"explanations"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("decode report JSON: %v", err)
		}
		for _, name := range []string{"requested", "expected", "covered", "missing", "stale", "unknown", "included", "excluded"} {
			if got := string(wire.Coverage[name]); got != "[]" {
				t.Errorf("coverage.%s = %s, want []", name, got)
			}
		}
		if len(wire.Charts) != 1 || len(wire.Explanations) != 1 {
			t.Fatalf("report lists lost entries: %s", raw)
		}
		for name, value := range map[string]json.RawMessage{
			"totals": wire.Totals, "table.rows": wire.Table.Rows,
			"chart.metric_ids": wire.Charts[0].MetricIDs, "chart.points": wire.Charts[0].Points,
			"explanation.evidence_refs": wire.Explanations[0].EvidenceRefs,
		} {
			if string(value) != "[]" {
				t.Errorf("%s = %s, want []", name, value)
			}
		}
	})

	t.Run("populated lists retain exact values and unavailable nulls", func(t *testing.T) {
		exact := decimal.RequireFromString("120.000000000000000000000000000000000000001")
		zero := decimal.Zero
		report := AnalysisReport{
			Coverage: Coverage{Included: []CoverageEntity{{EntityType: EntityTypeShop, EntityID: "S1"}}},
			Totals: []MetricValue{
				{MetricID: MetricSales, Status: MetricStatusAvailable, Value: &exact},
				{MetricID: MetricMarginRate, Status: MetricStatusUnavailable, Value: nil},
			},
			Table: ReportTable{Rows: []ReportRow{{EntityType: EntityTypeShop, EntityID: "S1"}}},
			Charts: []ChartSpec{{Points: []ChartPoint{
				{MetricID: MetricSales, Status: MetricStatusUnavailable, Value: nil},
				{MetricID: MetricSales, Status: MetricStatusAvailable, Value: &zero},
			}}},
		}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("marshal report: %v", err)
		}
		var wire struct {
			Coverage Coverage `json:"coverage"`
			Totals   []struct {
				Value *string `json:"value"`
			} `json:"totals"`
			Table struct {
				Rows []struct {
					Metrics json.RawMessage `json:"metrics"`
				} `json:"rows"`
			} `json:"table"`
			Charts []struct {
				Points []struct {
					Value *string `json:"value"`
				} `json:"points"`
			} `json:"charts"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("decode report JSON: %v", err)
		}
		if len(wire.Coverage.Included) != 1 || wire.Coverage.Included[0].EntityID != "S1" {
			t.Fatalf("coverage entries changed: %s", raw)
		}
		if len(wire.Totals) != 2 || wire.Totals[0].Value == nil || *wire.Totals[0].Value != "120.000000000000000000000000000000000000001" || wire.Totals[1].Value != nil {
			t.Fatalf("exact decimal string or unavailable null changed: %s", raw)
		}
		if len(wire.Table.Rows) != 1 || string(wire.Table.Rows[0].Metrics) != "[]" {
			t.Fatalf("row metrics must be an array: %s", raw)
		}
		if len(wire.Charts) != 1 || len(wire.Charts[0].Points) != 2 || wire.Charts[0].Points[0].Value != nil || wire.Charts[0].Points[1].Value == nil || *wire.Charts[0].Points[1].Value != "0" {
			t.Fatalf("unavailable and zero chart values became indistinguishable: %s", raw)
		}
	})
}
