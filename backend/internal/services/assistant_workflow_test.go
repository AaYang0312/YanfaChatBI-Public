package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

func TestAssistantWorkflow(t *testing.T) {
	t.Run("rejects invented values and unknown evidence without changing metrics", func(t *testing.T) {
		fixture := NewAssistantFixtureSource()
		source := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, func(_ context.Context, input AssistantExplanationInput) ([]AssistantExplanationClaim, error) {
			if len(input.Metrics) == 0 || len(input.EvidenceRefs) == 0 {
				t.Fatalf("explanation input is missing metric values or evidence: %#v", input)
			}
			return []AssistantExplanationClaim{
				{
					MetricID:     models.MetricSales,
					Value:        "999",
					Text:         "销售额为999。",
					EvidenceRefs: []string{"snap:" + AssistantFixtureContractVersion},
				},
				{
					MetricID:     models.MetricCost,
					Value:        "72",
					Text:         "成本为72。",
					EvidenceRefs: []string{"unknown:evidence"},
				},
			}, nil
		})
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-rejected"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
		if report.ExplanationStatus != models.ExplanationStatusRejectedEvidence {
			t.Fatalf("explanation status = %q, want rejected_evidence", report.ExplanationStatus)
		}
		if len(report.Explanations) != 0 {
			t.Fatalf("rejected explanations = %#v", report.Explanations)
		}
		assertAssistantWorkflowReadCounts(t, source, fixture, 1, 1)
		bar := assistantWorkflowChart(t, report.Charts, chartBarID, models.ChartKindBar)
		if len(bar.ExplanationRefs) != 0 {
			t.Fatalf("rejected explanation linked to chart: %#v", bar.ExplanationRefs)
		}
	})

	t.Run("reports missing coverage as partial without dropping reliable sales", func(t *testing.T) {
		fixture := NewAssistantFixtureSource()
		counted := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
		source := &assistantWorkflowCoverageMismatchSource{assistantWorkflowCountingSource: counted}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, nil)
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-coverage-mismatch"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if report.Outcome != models.OutcomePartial {
			t.Fatalf("outcome = %s, want partial", report.Outcome)
		}
		var s1Row, s2Row models.ReportRow
		for _, row := range report.Table.Rows {
			switch row.EntityID {
			case AssistantFixtureShopS1:
				s1Row = row
			case AssistantFixtureShopS2:
				s2Row = row
			}
		}
		s1Sales := assistantWorkflowRequireMetric(t, s1Row.Metrics, models.MetricSales)
		if s1Sales.Value != nil || s1Sales.Status != models.MetricStatusUnavailable || !containsReason(s1Sales.ReasonCodes, models.ReasonCoverageMissing) {
			t.Fatalf("S1 sales = %+v, want null unavailable coverage_missing", s1Sales)
		}
		s2Sales := assistantWorkflowRequireMetric(t, s2Row.Metrics, models.MetricSales)
		if s2Sales.Value == nil || s2Sales.Value.String() != "60" || s2Sales.Status != models.MetricStatusAvailable {
			t.Fatalf("S2 sales = %+v, want available 60", s2Sales)
		}
		assertAssistantWorkflowReadCounts(t, counted, fixture, 1, 1)
	})

	t.Run("model failure marks explanation unavailable and preserves charts", func(t *testing.T) {
		fixture := NewAssistantFixtureSource()
		source := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, func(context.Context, AssistantExplanationInput) ([]AssistantExplanationClaim, error) {
			return nil, errors.New("model unavailable")
		})
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-model-error"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
		if report.ExplanationStatus != models.ExplanationStatusUnavailable {
			t.Fatalf("explanation status = %q, want unavailable", report.ExplanationStatus)
		}
		bar := assistantWorkflowChart(t, report.Charts, chartBarID, models.ChartKindBar)
		if len(bar.Points) != 2 {
			t.Fatalf("model error changed bar chart points: %#v", bar.Points)
		}
		for i, point := range bar.Points {
			if point.Value == nil || !point.Value.Equal(decimal.NewFromInt(60)) {
				t.Fatalf("chart point %d sales = %v, want 60", i, point.Value)
			}
		}
		assertAssistantWorkflowReadCounts(t, source, fixture, 1, 1)
	})

	t.Run("independent explanation timeout leaves report intact", func(t *testing.T) {
		fixture := NewAssistantFixtureSource()
		source := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
		modelReceivedDeadline := false
		modelBudgetExpired := false
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, func(ctx context.Context, _ AssistantExplanationInput) ([]AssistantExplanationClaim, error) {
			deadline, ok := ctx.Deadline()
			modelReceivedDeadline = ok && time.Until(deadline) > 0
			<-ctx.Done()
			modelBudgetExpired = errors.Is(ctx.Err(), context.DeadlineExceeded)
			return nil, ctx.Err()
		})
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-explanation-timeout"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if !modelReceivedDeadline {
			t.Fatal("explanation model did not receive an independent deadline")
		}
		if !modelBudgetExpired {
			t.Fatal("explanation did not stop when its deadline expired")
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
		if report.ExplanationStatus != models.ExplanationStatusUnavailable {
			t.Fatalf("explanation status = %q, want unavailable", report.ExplanationStatus)
		}
		bar := assistantWorkflowChart(t, report.Charts, chartBarID, models.ChartKindBar)
		if len(bar.Points) != 2 {
			t.Fatalf("explanation timeout changed bar chart points: %#v", bar.Points)
		}
		for i, point := range bar.Points {
			if point.Value == nil || !point.Value.Equal(decimal.NewFromInt(60)) {
				t.Fatalf("chart point %d sales = %v, want 60", i, point.Value)
			}
		}
		assertAssistantWorkflowReadCounts(t, source, fixture, 1, 1)
	})

	t.Run("accepts grounded explanations and links their charts", func(t *testing.T) {
		fixture := NewAssistantFixtureSource()
		source := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, func(ctx context.Context, input AssistantExplanationInput) ([]AssistantExplanationClaim, error) {
			if !assistantWorkflowInputHasMetric(input, models.MetricSales, "120") {
				t.Fatalf("model input does not carry sales=120: %#v", input)
			}
			if ctx.Value(assistantWorkflowStateKey{}) != nil {
				t.Fatal("model context exposed workflow state")
			}
			return []AssistantExplanationClaim{{
				MetricID:     models.MetricSales,
				Value:        "120",
				Text:         "销售额为120。",
				EvidenceRefs: []string{"snap:" + AssistantFixtureContractVersion},
			}}, nil
		})
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-grounded"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if report.ExplanationStatus != models.ExplanationStatusAvailable || len(report.Explanations) != 1 {
			t.Fatalf("explanation result = %q %#v", report.ExplanationStatus, report.Explanations)
		}
		if report.Explanations[0].Value != "120" || report.Explanations[0].RefID != "explanation:sales" {
			t.Fatalf("accepted explanation = %#v", report.Explanations[0])
		}
		bar := assistantWorkflowChart(t, report.Charts, chartBarID, models.ChartKindBar)
		if len(bar.ExplanationRefs) != 1 || bar.ExplanationRefs[0] != "explanation:sales" {
			t.Fatalf("chart explanation references = %#v", bar.ExplanationRefs)
		}
		assertAssistantWorkflowReadCounts(t, source, fixture, 1, 1)
	})

	t.Run("rejects an English numeric hallucination and retains fixture decimals", func(t *testing.T) {
		fixture := NewAssistantFixtureSource()
		source := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, func(_ context.Context, input AssistantExplanationInput) ([]AssistantExplanationClaim, error) {
			if input.LanguageDirective != constants.LanguageDirective {
				t.Fatalf("explanation node did not receive LanguageDirective: %q", input.LanguageDirective)
			}
			if !strings.Contains(input.LanguageDirective, "Simplified Chinese") || !strings.Contains(input.LanguageDirective, "Preserve SQL") {
				t.Fatal("explanation directive does not require Simplified Chinese and preserved SQL")
			}
			if !assistantWorkflowInputHasMetric(input, models.MetricMarginRate, "0.4") ||
				!assistantWorkflowInputHasMetric(input, models.MetricAveragePrice, "40") ||
				!assistantWorkflowInputHasMetric(input, models.MetricMargin, "48") {
				t.Fatalf("explanation input dropped fixture decimals: %#v", input.Metrics)
			}
			return []AssistantExplanationClaim{{
				MetricID:     models.MetricMarginRate,
				Value:        "0.45",
				Text:         "The margin rate is 0.45.",
				EvidenceRefs: []string{"snap:" + AssistantFixtureContractVersion},
			}}, nil
		})
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-english-hallucination"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if report.ExplanationStatus != models.ExplanationStatusRejectedEvidence {
			t.Fatalf("explanation status = %q, want rejected_evidence", report.ExplanationStatus)
		}
		if len(report.Explanations) != 0 {
			t.Fatalf("English numeric hallucination was accepted: %#v", report.Explanations)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricCost, "72")
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricMargin, "48")
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricMarginRate, "0.4")
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricAveragePrice, "40")
		bar := assistantWorkflowChart(t, report.Charts, chartBarID, models.ChartKindBar)
		if len(bar.Points) != 2 {
			t.Fatalf("hallucination changed bar chart points: %#v", bar.Points)
		}
		for i, point := range bar.Points {
			if point.Value == nil || point.Value.String() != "60" {
				t.Fatalf("chart point %d sales = %v, want 60", i, point.Value)
			}
		}
		if len(bar.ExplanationRefs) != 0 {
			t.Fatal("rejected English hallucination was linked to the chart")
		}
		assertAssistantWorkflowReadCounts(t, source, fixture, 1, 1)
	})
}

// TestAssistantWorkflowDefaultTrendPeriod exercises the real workflow entry
// (Invoke rebuilds and validates the request, so the default is consumed, not
// just the normalize helper): nil and blank trend_period must fill the default
// [main end exclusive - 7d, main end exclusive) and produce a consumed trend
// chart, while the equal-window coverage read is reused.
func TestAssistantWorkflowDefaultTrendPeriod(t *testing.T) {
	cases := []struct {
		name  string
		trend *models.DateRange
	}{
		{name: "nil trend period consumes the default window"},
		{name: "blank trend period consumes the default window", trend: &models.DateRange{}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fixture := NewAssistantFixtureSource()
			source := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
			request := assistantFixtureWorkflowRequest()
			request.TrendPeriod = tt.trend
			workflow, err := NewAssistantWorkflow(request, source, nil)
			if err != nil {
				t.Fatalf("NewAssistantWorkflow: %v", err)
			}

			report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-default-trend"})
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
			// Standard fixture payment trend over the default window
			// 2026-09-01..2026-09-08: S1 pays 60 on 09-01, S2 pays 60 on
			// 09-02, fully covered later dates are true zeros.
			wantDates := []string{
				"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04",
				"2026-09-05", "2026-09-06", "2026-09-07",
			}
			wantValues := []string{"60", "60", "0", "0", "0", "0", "0"}
			line := assistantWorkflowChart(t, report.Charts, chartLineID, models.ChartKindLine)
			if len(line.Points) != len(wantDates) {
				t.Fatalf("default trend points = %d, want %d: %#v", len(line.Points), len(wantDates), line.Points)
			}
			for i, point := range line.Points {
				if point.BusinessDate != wantDates[i] {
					t.Fatalf("trend point %d date = %q, want %s", i, point.BusinessDate, wantDates[i])
				}
				if point.Value == nil || point.Value.String() != wantValues[i] || point.Status != models.MetricStatusAvailable {
					t.Fatalf("trend point %s = %+v, want available %s", wantDates[i], point, wantValues[i])
				}
			}
			assertAssistantWorkflowReadCounts(t, source, fixture, 1, 1)
		})
	}
}

// TestAssistantWorkflowSelectedPlatformShopGrouping exercises the real
// workflow entry for the consumer defect found on the actual platform run: a
// selected platform-only scope with shop grouping must report the platform's
// member shops from the source's coverage evidence (PL-1 → S1/S2,
// 120 = 60+60), and a scope naming both platforms and shops must report their
// intersection — never the unconstrained platform total, and never an
// available zero for an empty expected set.
func TestAssistantWorkflowSelectedPlatformShopGrouping(t *testing.T) {
	newReport := func(t *testing.T, scope models.Scope, groupBy models.GroupBy) models.AnalysisReport {
		t.Helper()
		fixture := NewAssistantFixtureSource()
		source := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
		request := assistantFixtureWorkflowRequest()
		request.Workflow = models.WorkflowSalesComparison
		request.Product = models.ProductSelection{}
		request.Scope = scope
		request.GroupBy = groupBy
		workflow, err := NewAssistantWorkflow(request, source, nil)
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}
		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-selected-platform"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		assertAssistantWorkflowReadCounts(t, source, fixture, 1, 1)
		return report
	}
	rowByID := func(t *testing.T, report models.AnalysisReport, entityID string) models.ReportRow {
		t.Helper()
		for _, row := range report.Table.Rows {
			if row.EntityID == entityID {
				return row
			}
		}
		t.Fatalf("row %s missing from %#v", entityID, report.Table.Rows)
		return models.ReportRow{}
	}
	unavailableSales := func(t *testing.T, metrics []models.MetricValue, label string) {
		t.Helper()
		sales := assistantWorkflowRequireMetric(t, metrics, models.MetricSales)
		if sales.Value != nil || sales.Status != models.MetricStatusUnavailable || !containsReason(sales.ReasonCodes, models.ReasonCoverageMissing) {
			t.Fatalf("%s sales = %+v, want null unavailable coverage_missing", label, sales)
		}
	}

	t.Run("platform-only selection reports the platform's member shops", func(t *testing.T) {
		report := newReport(t, models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}}, models.GroupByShop)
		if report.Outcome != models.OutcomeComplete {
			t.Fatalf("outcome = %s, want complete", report.Outcome)
		}
		if len(report.Table.Rows) != 2 {
			t.Fatalf("rows = %#v, want exactly S1 and S2", report.Table.Rows)
		}
		for _, shopID := range []string{AssistantFixtureShopS1, AssistantFixtureShopS2} {
			sales := assistantWorkflowRequireMetric(t, rowByID(t, report, shopID).Metrics, models.MetricSales)
			if sales.Value == nil || sales.Value.String() != "60" || sales.Status != models.MetricStatusAvailable {
				t.Fatalf("%s sales = %+v, want available 60", shopID, sales)
			}
		}
		// The platform request resolves to its member shops: 120 = 60 + 60.
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
		expectedShops := []string{}
		for _, entity := range report.Coverage.Expected {
			if entity.EntityType != models.EntityTypeShop {
				t.Fatalf("expected set leaked a %s entity: %#v", entity.EntityType, report.Coverage.Expected)
			}
			expectedShops = append(expectedShops, entity.EntityID)
		}
		if len(expectedShops) != 2 || expectedShops[0] != AssistantFixtureShopS1 || expectedShops[1] != AssistantFixtureShopS2 {
			t.Fatalf("coverage expected = %v, want [S1 S2]", expectedShops)
		}
		bar := assistantWorkflowChart(t, report.Charts, chartBarID, models.ChartKindBar)
		if len(bar.Points) != 2 {
			t.Fatalf("bar chart points = %#v, want two shop points", bar.Points)
		}
		for _, point := range bar.Points {
			if point.Value == nil || point.Value.String() != "60" || point.Status != models.MetricStatusAvailable {
				t.Fatalf("bar point %s = %+v, want available 60", point.EntityID, point)
			}
		}
		if len(report.ActualScope.ShopIDs) != 2 || report.ActualScope.ShopIDs[0] != AssistantFixtureShopS1 || report.ActualScope.ShopIDs[1] != AssistantFixtureShopS2 {
			t.Fatalf("actual scope shops = %v, want [S1 S2]", report.ActualScope.ShopIDs)
		}
		if len(report.ActualScope.PlatformIDs) != 1 || report.ActualScope.PlatformIDs[0] != AssistantFixturePlatformID {
			t.Fatalf("actual scope platforms = %v, want [PL-1]", report.ActualScope.PlatformIDs)
		}
	})

	t.Run("explicit shop inside the platform keeps the exact request", func(t *testing.T) {
		report := newReport(t, models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{AssistantFixtureShopS1}}, models.GroupByShop)
		if report.Outcome != models.OutcomeComplete {
			t.Fatalf("outcome = %s, want complete", report.Outcome)
		}
		if len(report.Table.Rows) != 1 {
			t.Fatalf("rows = %#v, want exactly S1", report.Table.Rows)
		}
		sales := assistantWorkflowRequireMetric(t, rowByID(t, report, AssistantFixtureShopS1).Metrics, models.MetricSales)
		if sales.Value == nil || sales.Value.String() != "60" || sales.Status != models.MetricStatusAvailable {
			t.Fatalf("S1 sales = %+v, want available 60", sales)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "60")
		if len(report.Coverage.Expected) != 1 || report.Coverage.Expected[0].ShopID != AssistantFixtureShopS1 {
			t.Fatalf("coverage expected = %#v, want exactly S1", report.Coverage.Expected)
		}
	})

	t.Run("platform total restricted by an explicit shop honors the intersection", func(t *testing.T) {
		report := newReport(t, models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{AssistantFixtureShopS1}}, models.GroupByPlatform)
		if report.Outcome != models.OutcomeComplete {
			t.Fatalf("outcome = %s, want complete", report.Outcome)
		}
		if len(report.Table.Rows) != 1 {
			t.Fatalf("rows = %#v, want exactly PL-1", report.Table.Rows)
		}
		sales := assistantWorkflowRequireMetric(t, rowByID(t, report, AssistantFixturePlatformID).Metrics, models.MetricSales)
		if sales.Value == nil || sales.Value.String() != "60" || sales.Status != models.MetricStatusAvailable {
			t.Fatalf("PL-1 sales = %+v, want the restricted intersection total 60", sales)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "60")
		// The anonymous refund cannot be proven to belong to the S1 subset:
		// every refund output stays unavailable instead of borrowing the
		// catalog-wide 10, while the reliable S1 sales, cost, and margin stay.
		assertAssistantWorkflowUnavailableRefund(t, report, "PL-1+S1")
	})

	t.Run("shop the platform does not carry cannot borrow the platform total", func(t *testing.T) {
		report := newReport(t, models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{"S9"}}, models.GroupByPlatform)
		if report.Outcome != models.OutcomeUnavailable {
			t.Fatalf("outcome = %s, want unavailable", report.Outcome)
		}
		if len(report.Table.Rows) != 1 {
			t.Fatalf("rows = %#v, want exactly the unavailable PL-1 row", report.Table.Rows)
		}
		unavailableSales(t, rowByID(t, report, AssistantFixturePlatformID).Metrics, "PL-1")
		unavailableSales(t, report.Totals, "total")
		assertAssistantWorkflowUnavailableRefund(t, report, "PL-1+S9")
	})

	t.Run("refund output stays consistent across totals table and summary for selected subsets", func(t *testing.T) {
		// The exact consumer defect: same report, sales correctly narrowed to
		// the selected subset while the refund summary still published the
		// catalog-wide anonymous 10.
		report := newReport(t, models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{AssistantFixtureShopS1}}, models.GroupByPlatform)
		assertAssistantWorkflowUnavailableRefund(t, report, "PL-1+S1 platform")
		// With a servable subset the refund reason is uniform everywhere:
		// totals, table totals, summary, and the entity row all say the
		// anonymous refund's subset membership is unprovable.
		for _, place := range []struct {
			name    string
			metrics []models.MetricValue
		}{
			{"totals", report.Totals},
			{"table totals", report.Table.Totals},
			{"PL-1 row", rowByID(t, report, AssistantFixturePlatformID).Metrics},
		} {
			refund := assistantWorkflowRequireMetric(t, place.metrics, models.MetricRefund)
			if !containsReason(refund.ReasonCodes, models.ReasonCoverageUnknown) {
				t.Fatalf("PL-1+S1 %s refund = %+v, want coverage_unknown", place.name, refund)
			}
		}
		if !containsReason(report.Refunds.ReasonCodes, models.ReasonCoverageUnknown) {
			t.Fatalf("PL-1+S1 summary reasons = %v, want coverage_unknown", report.Refunds.ReasonCodes)
		}
		if report.Refunds.ProductRefund == nil || report.Refunds.ProductRefund.Value != nil || report.Refunds.ProductRefund.Status != models.MetricStatusUnavailable {
			t.Fatalf("PL-1+S1 product refund = %+v, want null unavailable", report.Refunds.ProductRefund)
		}
		report = newReport(t, models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{"S9"}}, models.GroupByPlatform)
		assertAssistantWorkflowUnavailableRefund(t, report, "PL-1+S9 platform")
		if report.Refunds.ProductRefund == nil || report.Refunds.ProductRefund.Value != nil || report.Refunds.ProductRefund.Status != models.MetricStatusUnavailable {
			t.Fatalf("PL-1+S9 product refund = %+v, want null unavailable", report.Refunds.ProductRefund)
		}
	})

	t.Run("mixed known and missing shops keep the platform grouping partial", func(t *testing.T) {
		report := newReport(t, models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{AssistantFixtureShopS1, "S9"}}, models.GroupByPlatform)
		// One covered member hit must not upgrade a request that explicitly
		// names a shop the catalog does not carry.
		if report.Outcome != models.OutcomePartial {
			t.Fatalf("outcome = %s, want partial", report.Outcome)
		}
		// The provable member's sales stay published as a partial aggregate;
		// the parent is the only table row, no shop leaf is double counted.
		if len(report.Table.Rows) != 1 {
			t.Fatalf("rows = %#v, want exactly the PL-1 parent row", report.Table.Rows)
		}
		sales := assistantWorkflowRequireMetric(t, rowByID(t, report, AssistantFixturePlatformID).Metrics, models.MetricSales)
		if sales.Value == nil || sales.Value.String() != "60" || sales.Status != models.MetricStatusPartial || !containsReason(sales.ReasonCodes, models.ReasonCoverageMissing) {
			t.Fatalf("PL-1 sales = %+v, want partial 60 with the missing leaf recorded", sales)
		}
		totalsSales := assistantWorkflowRequireMetric(t, report.Totals, models.MetricSales)
		if totalsSales.Value == nil || totalsSales.Value.String() != "60" || totalsSales.Status != models.MetricStatusPartial || !containsReason(totalsSales.ReasonCodes, models.ReasonCoverageMissing) {
			t.Fatalf("totals sales = %+v, want partial 60", totalsSales)
		}
		tableTotalsSales := assistantWorkflowRequireMetric(t, report.Table.Totals, models.MetricSales)
		if tableTotalsSales.Value == nil || tableTotalsSales.Value.String() != "60" || tableTotalsSales.Status != models.MetricStatusPartial {
			t.Fatalf("table totals sales = %+v, want partial 60", tableTotalsSales)
		}
		// The explicitly requested missing shop stays visible as leaf evidence.
		missingS9 := false
		for _, entity := range report.Coverage.Missing {
			if entity.EntityType == models.EntityTypeShop && entity.EntityID == "S9" && containsReason(entity.ReasonCodes, models.ReasonCoverageMissing) {
				missingS9 = true
			}
		}
		if !missingS9 {
			t.Fatalf("coverage missing = %#v, want the explicit S9 shop leaf", report.Coverage.Missing)
		}
		excludedS9 := false
		for _, entity := range report.Coverage.Excluded {
			if entity.EntityType == models.EntityTypeShop && entity.EntityID == "S9" {
				excludedS9 = true
			}
		}
		if !excludedS9 {
			t.Fatalf("coverage excluded = %#v, want the explicit S9 shop leaf", report.Coverage.Excluded)
		}
		includedPlatform := false
		for _, entity := range report.Coverage.Included {
			if entity.EntityType == models.EntityTypePlatform && entity.EntityID == AssistantFixturePlatformID {
				includedPlatform = true
			}
		}
		if !includedPlatform {
			t.Fatalf("coverage included = %#v, want the PL-1 parent", report.Coverage.Included)
		}
		// Refunds stay unavailable in the mixed subset as well.
		assertAssistantWorkflowUnavailableRefund(t, report, "PL-1+S1+S9")
		// Trend: the covered member contributes its exact daily sums, but the
		// missing leaf keeps every point partial instead of available.
		line := assistantWorkflowChart(t, report.Charts, chartLineID, models.ChartKindLine)
		wantValues := []string{"60", "0", "0", "0", "0", "0", "0"}
		if len(line.Points) != len(wantValues) {
			t.Fatalf("trend points = %d, want %d", len(line.Points), len(wantValues))
		}
		for i, point := range line.Points {
			if point.Value == nil || point.Value.String() != wantValues[i] || point.Status != models.MetricStatusPartial || !containsReason(point.ReasonCodes, models.ReasonCoverageMissing) {
				t.Fatalf("trend point %d = %+v, want partial %s with coverage_missing", i, point, wantValues[i])
			}
		}
		// The bar chart mirrors the partial platform row.
		bar := assistantWorkflowChart(t, report.Charts, chartBarID, models.ChartKindBar)
		if len(bar.Points) != 1 {
			t.Fatalf("bar points = %#v, want exactly the PL-1 point", bar.Points)
		}
		if bar.Points[0].Value == nil || bar.Points[0].Value.String() != "60" || bar.Points[0].Status != models.MetricStatusPartial {
			t.Fatalf("bar point = %+v, want partial 60", bar.Points[0])
		}
	})
}

func TestAssistantWorkflowRefundSnapshot(t *testing.T) {
	for _, basis := range []models.Basis{models.BasisPayment, models.BasisOutstock} {
		t.Run(string(basis), func(t *testing.T) {
			fixture := NewAssistantFixtureSource()
			source := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
			request := assistantFixtureWorkflowRequest()
			request.Basis = basis
			workflow, err := NewAssistantWorkflow(request, source, nil)
			if err != nil {
				t.Fatalf("NewAssistantWorkflow: %v", err)
			}

			report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-refund"})
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricCost, "72")
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricMargin, "48")
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricMarginRate, "0.4")
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricAveragePrice, "40")
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricRefund, "10")
			if report.MarginLabel != "未扣退款" || report.Refunds.Label != "未扣退款" {
				t.Fatalf("margin label = %q, refund label = %q", report.MarginLabel, report.Refunds.Label)
			}
			if report.Refunds.Status != models.MetricStatusAvailable || report.Refunds.Amount == nil || report.Refunds.Amount.String() != "10" || report.Refunds.Currency != "CNY" {
				t.Fatalf("refund summary = %+v, want available CNY 10", report.Refunds)
			}
			if report.Refunds.EntityID != "" {
				t.Fatalf("unattributed refund acquired entity %q", report.Refunds.EntityID)
			}
			product := report.Refunds.ProductRefund
			if product == nil || product.Status != models.MetricStatusUnavailable || product.Value != nil || !containsReason(product.ReasonCodes, models.ReasonProductAttributionUnavailable) {
				t.Fatalf("product refund = %+v, want unavailable without an invented zero", product)
			}
			if !containsReason(report.Refunds.ReasonCodes, models.ReasonProductAttributionUnavailable) {
				t.Fatalf("refund reasons = %v, want product attribution unavailable", report.Refunds.ReasonCodes)
			}
			for _, row := range report.Table.Rows {
				refund := assistantWorkflowRequireMetric(t, row.Metrics, models.MetricRefund)
				if refund.Status != models.MetricStatusUnavailable || refund.Value != nil || !containsReason(refund.ReasonCodes, models.ReasonProductAttributionUnavailable) {
					t.Fatalf("row %s refund = %+v, want unavailable without borrowing a sales entity", row.EntityID, refund)
				}
			}
			assertAssistantWorkflowReadCounts(t, source, fixture, 1, 1)
		})
	}
}

func TestAssistantWorkflowRefundCapability(t *testing.T) {
	cases := []struct {
		name             string
		refundCapability bool
		refundRequested  bool
	}{
		{name: "undeclared refunds stay unavailable"},
		{name: "declared refunds are listed even when omitted from table metrics", refundCapability: true},
		{name: "explicitly requested refunds remain unavailable without declared capability", refundRequested: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fixture := NewAssistantFixtureSource()
			counted := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
			source := &assistantWorkflowSnapshotSource{
				assistantWorkflowCountingSource: counted,
				capabilities: func(capabilities *models.SourceCapabilities) {
					if !tt.refundCapability {
						capabilities.SupportedMetrics = []models.MetricID{models.MetricSales, models.MetricMargin}
					}
				},
			}
			request := assistantFixtureWorkflowRequest()
			request.Metrics = []models.MetricID{models.MetricSales, models.MetricMargin}
			if tt.refundRequested {
				request.Metrics = append(request.Metrics, models.MetricRefund)
			}
			workflow, err := NewAssistantWorkflow(request, source, nil)
			if err != nil {
				t.Fatalf("NewAssistantWorkflow: %v", err)
			}

			report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-refund-capability"})
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricMargin, "48")
			if tt.refundCapability {
				if report.Refunds.Status != models.MetricStatusAvailable || report.Refunds.Amount == nil || report.Refunds.Amount.String() != "10" {
					t.Fatalf("declared refund = %+v, want available 10", report.Refunds)
				}
				assertAssistantWorkflowReadCounts(t, counted, fixture, 1, 1)
			} else {
				if report.Refunds.Status != models.MetricStatusUnavailable || report.Refunds.Amount != nil {
					t.Fatalf("undeclared refund = %+v, want unavailable without an amount", report.Refunds)
				}
				assertAssistantWorkflowReadCounts(t, counted, fixture, 0, 1)
			}
		})
	}
}

func TestAssistantWorkflowRefundReadGate(t *testing.T) {
	cases := []struct {
		name          string
		capabilities  func(*models.SourceCapabilities)
		sales         func(*models.FactSnapshot)
		wantSalesRead int
	}{
		{
			name: "no approved contract",
			capabilities: func(capabilities *models.SourceCapabilities) {
				capabilities.ContractVersion = ""
			},
		},
		{
			name: "no coverage evidence",
			capabilities: func(capabilities *models.SourceCapabilities) {
				capabilities.CoverageEvidence = false
			},
		},
		{
			name: "sales contract differs from the approved version",
			sales: func(snapshot *models.FactSnapshot) {
				snapshot.ContractVersion = "unapproved-sales-version"
			},
			wantSalesRead: 1,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			counted := &assistantWorkflowCountingSource{AssistantSourceRepository: NewAssistantFixtureSource()}
			source := &assistantWorkflowSnapshotSource{
				assistantWorkflowCountingSource: counted,
				capabilities:                    tt.capabilities,
				sales:                           tt.sales,
			}
			workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, nil)
			if err != nil {
				t.Fatalf("NewAssistantWorkflow: %v", err)
			}
			if _, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-refund-gate"}); err == nil {
				t.Fatal("workflow accepted a source before its contract and coverage were validated")
			}
			if counted.salesReads != tt.wantSalesRead || counted.coverageReads != 0 || counted.refundReads != 0 {
				t.Fatalf("sales=%d coverage=%d refund=%d; want %d, 0, 0", counted.salesReads, counted.coverageReads, counted.refundReads, tt.wantSalesRead)
			}
		})
	}
}

func TestAssistantWorkflowRefundValidation(t *testing.T) {
	cases := []struct {
		name   string
		refund func(*models.FactSnapshot)
	}{
		{name: "no refund contract version", refund: func(snapshot *models.FactSnapshot) { snapshot.ContractVersion = "" }},
		{name: "different refund contract version", refund: func(snapshot *models.FactSnapshot) { snapshot.ContractVersion = "unapproved-refund-version" }},
		{name: "different refund basis", refund: func(snapshot *models.FactSnapshot) { snapshot.Basis = models.BasisOutstock }},
		{name: "different refund window", refund: func(snapshot *models.FactSnapshot) { snapshot.Window.EndDateExclusive = "2026-09-09" }},
		{name: "missing refund read time", refund: func(snapshot *models.FactSnapshot) { snapshot.ReadAt = time.Time{} }},
		{name: "missing refund freshness", refund: func(snapshot *models.FactSnapshot) { snapshot.DataAsOf = time.Time{} }},
		{name: "different refund row basis", refund: func(snapshot *models.FactSnapshot) { snapshot.Rows[0].Basis = models.BasisOutstock }},
		{name: "invalid refund occurrence date", refund: func(snapshot *models.FactSnapshot) { snapshot.Rows[0].BusinessDate = "2026-09-31" }},
		{name: "invalid refund coverage", refund: func(snapshot *models.FactSnapshot) { snapshot.Rows[0].CoverageStatus = "" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fixture := NewAssistantFixtureSource()
			counted := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
			source := &assistantWorkflowSnapshotSource{assistantWorkflowCountingSource: counted, refund: tt.refund}
			modelCalled := false
			workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, func(context.Context, AssistantExplanationInput) ([]AssistantExplanationClaim, error) {
				modelCalled = true
				return nil, nil
			})
			if err != nil {
				t.Fatalf("NewAssistantWorkflow: %v", err)
			}
			if _, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-invalid-refund"}); err == nil {
				t.Fatal("workflow published a refund without independent contract, coverage, and period validation")
			}
			if modelCalled {
				t.Fatal("explanation ran after refund validation failed")
			}
			assertAssistantWorkflowReadCounts(t, counted, fixture, 1, 1)
		})
	}
}

func TestAssistantWorkflowRefundPeriod(t *testing.T) {
	cases := []struct {
		name              string
		basis             models.Basis
		window            models.DateRange
		trend             *models.DateRange
		date              string
		wantRefund        bool
		wantCoverageReads int
	}{
		// The workflow fills nil/blank trend periods with the default
		// [main end exclusive - 7d, main end exclusive): the standard window
		// normalizes to the same window (coverage reuse), the narrower
		// windows normalize to a different window (independent coverage
		// read). Sales and refunds stay one snapshot each.
		{name: "local start is inclusive", date: "2026-08-31T16:00:00Z", wantRefund: true, wantCoverageReads: 1},
		{name: "before local start is excluded", date: "2026-08-31T15:59:59Z", wantCoverageReads: 1},
		{name: "last instant before local end is included", date: "2026-09-07T15:59:59Z", wantRefund: true, wantCoverageReads: 1},
		{name: "local end is exclusive", date: "2026-09-07T16:00:00Z", wantCoverageReads: 1},
		{
			name:              "fixture refund outside a narrower payment period",
			window:            models.DateRange{StartDate: "2026-09-01", EndDateExclusive: "2026-09-05"},
			wantCoverageReads: 2,
		},
		{
			name:              "fixture refund outside a narrower outstock period",
			basis:             models.BasisOutstock,
			window:            models.DateRange{StartDate: "2026-09-03", EndDateExclusive: "2026-09-05"},
			wantCoverageReads: 2,
		},
		{
			name:              "trend refunds do not enter the main occurrence window",
			trend:             &models.DateRange{StartDate: "2026-08-25", EndDateExclusive: "2026-09-01"},
			date:              "2026-08-31",
			wantCoverageReads: 2,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fixture := NewAssistantFixtureSource()
			counted := &assistantWorkflowCountingSource{AssistantSourceRepository: fixture}
			source := &assistantWorkflowSnapshotSource{
				assistantWorkflowCountingSource: counted,
				refund: func(snapshot *models.FactSnapshot) {
					if tt.date != "" {
						snapshot.Rows[0].BusinessDate = tt.date
					}
				},
			}
			request := assistantFixtureWorkflowRequest()
			if tt.basis != "" {
				request.Basis = tt.basis
			}
			if tt.window.StartDate != "" {
				request.Period.DateRange = tt.window
			}
			request.TrendPeriod = tt.trend
			workflow, err := NewAssistantWorkflow(request, source, nil)
			if err != nil {
				t.Fatalf("NewAssistantWorkflow: %v", err)
			}
			report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-refund-period"})
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricMargin, "48")
			refund := assistantWorkflowRequireMetric(t, report.Totals, models.MetricRefund)
			if tt.wantRefund {
				assertAssistantWorkflowMetric(t, report.Totals, models.MetricRefund, "10")
				if report.Refunds.Status != models.MetricStatusAvailable || report.Refunds.Amount == nil || report.Refunds.Amount.String() != "10" {
					t.Fatalf("in-period refund = %+v, want available 10", report.Refunds)
				}
			} else if refund.Value != nil || refund.Status != models.MetricStatusUnavailable || report.Refunds.Amount != nil || report.Refunds.Status != models.MetricStatusUnavailable {
				t.Fatalf("out-of-period refund was published: total=%+v summary=%+v", refund, report.Refunds)
			}
			assertAssistantWorkflowReadCounts(t, counted, fixture, 1, tt.wantCoverageReads)
		})
	}
}

type assistantWorkflowCountingSource struct {
	repositories.AssistantSourceRepository
	salesReads    int
	coverageReads int
	refundReads   int
}

func (s *assistantWorkflowCountingSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.salesReads++
	return s.AssistantSourceRepository.ReadSalesFacts(ctx, query)
}

func (s *assistantWorkflowCountingSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	s.coverageReads++
	return s.AssistantSourceRepository.ReadCoverage(ctx, query)
}

func (s *assistantWorkflowCountingSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.refundReads++
	return s.AssistantSourceRepository.ReadRefundFacts(ctx, query)
}

// assertAssistantWorkflowUnavailableRefund checks the consumer-visible refund
// boundary for a selected subset: totals, table totals, and the refund summary
// must all stay unavailable without an amount or currency — the anonymous
// catalog-wide refund cannot be proven to belong to the subset. The summary
// carries coverage_unknown from the source's unprovable-scope refund row.
func assertAssistantWorkflowUnavailableRefund(t *testing.T, report models.AnalysisReport, label string) {
	t.Helper()
	for _, place := range []struct {
		name    string
		metrics []models.MetricValue
	}{
		{"totals", report.Totals},
		{"table totals", report.Table.Totals},
	} {
		refund := assistantWorkflowRequireMetric(t, place.metrics, models.MetricRefund)
		if refund.Value != nil || refund.Status != models.MetricStatusUnavailable || refund.Currency != "" {
			t.Fatalf("%s %s refund = %+v, want null unavailable without a currency", label, place.name, refund)
		}
	}
	if report.Refunds.Status != models.MetricStatusUnavailable || report.Refunds.Amount != nil || report.Refunds.Currency != "" || !containsReason(report.Refunds.ReasonCodes, models.ReasonCoverageUnknown) {
		t.Fatalf("%s refund summary = %+v, want unavailable without an amount", label, report.Refunds)
	}
}

type assistantWorkflowCoverageMismatchSource struct {
	*assistantWorkflowCountingSource
}

func (s *assistantWorkflowCoverageMismatchSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	coverage, err := s.assistantWorkflowCountingSource.ReadCoverage(ctx, query)
	if err != nil {
		return nil, err
	}
	for i := range coverage {
		if coverage[i].EntityType == models.EntityTypeShop && coverage[i].EntityID == AssistantFixtureShopS1 {
			coverage[i].Status = models.CoverageStatusMissing
		}
	}
	return coverage, nil
}

type assistantWorkflowSnapshotSource struct {
	*assistantWorkflowCountingSource
	capabilities func(*models.SourceCapabilities)
	sales        func(*models.FactSnapshot)
	refund       func(*models.FactSnapshot)
}

func (s *assistantWorkflowSnapshotSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	capabilities, err := s.AssistantSourceRepository.Capabilities(ctx)
	if err == nil && s.capabilities != nil {
		s.capabilities(&capabilities)
	}
	return capabilities, err
}

func (s *assistantWorkflowSnapshotSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	snapshot, err := s.assistantWorkflowCountingSource.ReadSalesFacts(ctx, query)
	if err == nil && s.sales != nil {
		s.sales(&snapshot)
	}
	return snapshot, err
}

func (s *assistantWorkflowSnapshotSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	snapshot, err := s.assistantWorkflowCountingSource.ReadRefundFacts(ctx, query)
	if err == nil && s.refund != nil {
		s.refund(&snapshot)
	}
	return snapshot, err
}

// assistantWorkflowChart finds a consumer chart by its stable ID and verifies
// its kind. It deliberately does not assume anything about the total number of
// charts or their positions: the report independently carries the entity bar
// and the date trend line.
func assistantWorkflowChart(t *testing.T, charts []models.ChartSpec, chartID string, kind models.ChartKind) models.ChartSpec {
	t.Helper()
	for _, chart := range charts {
		if chart.ChartID != chartID {
			continue
		}
		if chart.Kind != kind {
			t.Fatalf("chart %s kind = %q, want %q", chartID, chart.Kind, kind)
		}
		return chart
	}
	t.Fatalf("chart %s missing from %#v", chartID, charts)
	return models.ChartSpec{}
}

func assertAssistantWorkflowReadCounts(t *testing.T, source *assistantWorkflowCountingSource, fixture *AssistantFixtureSource, refundReads, coverageReads int) {
	t.Helper()
	plans := fixture.Plans()
	wantPlans := 1 + coverageReads + refundReads
	if source.salesReads != 1 || source.coverageReads != coverageReads || source.refundReads != refundReads || len(plans) != wantPlans {
		t.Fatalf("sales reads = %d, coverage reads = %d, refund reads = %d, fixture plans = %d; want 1, %d, %d, %d", source.salesReads, source.coverageReads, source.refundReads, len(plans), coverageReads, refundReads, wantPlans)
	}
	if plans[0].Operation != "sales_facts" {
		t.Fatalf("first source operation = %q; want sales_facts", plans[0].Operation)
	}
	for i := range coverageReads {
		if plans[1+i].Operation != "read_coverage" {
			t.Fatalf("source operation %d = %q; want read_coverage", 1+i, plans[1+i].Operation)
		}
	}
	if refundReads > 0 && plans[1+coverageReads].Operation != "refund_facts" {
		t.Fatalf("source operation %d = %q; want a separate refund_facts snapshot", 1+coverageReads, plans[1+coverageReads].Operation)
	}
}

func assistantFixtureWorkflowRequest() models.AnalysisRequest {
	return models.AnalysisRequest{
		SchemaVersion: models.AssistantSchemaVersion,
		RequestID:     "req-workflow",
		Workflow:      models.WorkflowProductAnalysis,
		Basis:         models.BasisPayment,
		Period: models.Period{
			DateRange: models.DateRange{StartDate: models.AssistantDefaultStartDate, EndDateExclusive: models.AssistantDefaultEndDateExclu},
			Timezone:  models.AssistantDefaultTimezone,
		},
		Scope:   models.Scope{Mode: models.ScopeModeAll},
		Product: models.ProductSelection{ProductID: AssistantFixtureProductID},
		GroupBy: models.GroupByShop,
		Metrics: models.SupportedMetrics(),
	}
}

func assertAssistantWorkflowMetric(t *testing.T, metrics []models.MetricValue, metricID models.MetricID, want string) {
	t.Helper()
	for _, metric := range metrics {
		if metric.MetricID != metricID {
			continue
		}
		if metric.Value == nil || metric.Value.String() != want {
			t.Fatalf("metric %s = %v, want %s", metricID, metric.Value, want)
		}
		return
	}
	t.Fatalf("metric %s missing from %#v", metricID, metrics)
}

func assistantWorkflowInputHasMetric(input AssistantExplanationInput, metricID models.MetricID, value string) bool {
	for _, metric := range input.Metrics {
		if metric.MetricID == metricID && metric.Value == value {
			return true
		}
	}
	return false
}

func assistantWorkflowRequireMetric(t *testing.T, metrics []models.MetricValue, metricID models.MetricID) models.MetricValue {
	t.Helper()
	for _, metric := range metrics {
		if metric.MetricID == metricID {
			return metric
		}
	}
	t.Fatalf("metric %s missing from %#v", metricID, metrics)
	return models.MetricValue{}
}
