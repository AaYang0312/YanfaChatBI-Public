package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

// These regressions enter through the existing compiled workflow and inspect
// only its public report/error contract. They can be copied onto the baseline.
func TestAssistantRefundCurrencyBoundary(t *testing.T) {
	cases := []struct {
		name       string
		currencies []string
		wantAmount string
		wantCur    string
	}{
		{name: "single currency", currencies: []string{"CNY", "CNY", "CNY"}, wantAmount: "6", wantCur: "CNY"},
		{name: "refund currency independent of sales", currencies: []string{"USD"}, wantAmount: "1", wantCur: "USD"},
		{name: "two currencies", currencies: []string{"CNY", "USD"}},
		{name: "conflict never resets", currencies: []string{"CNY", "USD", "CNY"}},
		{name: "reverse conflict never resets", currencies: []string{"USD", "CNY", "USD"}},
		{name: "unknown currency", currencies: []string{""}},
		{name: "unknown then known", currencies: []string{"", "CNY"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			source := &assistantRefundBoundarySource{
				AssistantSourceRepository: NewAssistantFixtureSource(),
				refund: func(snapshot *models.FactSnapshot) {
					row := snapshot.Rows[0]
					snapshot.Rows = nil
					for i, currency := range tt.currencies {
						row.Currency = currency
						row.Sales = decimalFromBoundaryInt(i + 1)
						snapshot.Rows = append(snapshot.Rows, row)
					}
				},
			}
			report := invokeAssistantRefundBoundary(t, source)
			assertAssistantRefundSalesPreserved(t, report)
			refund := assistantWorkflowRequireMetric(t, report.Totals, models.MetricRefund)
			tableRefund := assistantWorkflowRequireMetric(t, report.Table.Totals, models.MetricRefund)
			if tt.wantAmount == "" {
				if report.Refunds.Status != models.MetricStatusUnavailable || report.Refunds.Amount != nil || report.Refunds.Currency != "" || !containsReason(report.Refunds.ReasonCodes, models.ReasonCurrencyConversionUnsupported) {
					t.Fatalf("mixed/unknown currency summary = %+v", report.Refunds)
				}
				for _, metric := range []models.MetricValue{refund, tableRefund} {
					if metric.Status != models.MetricStatusUnavailable || metric.Value != nil || metric.Currency != "" || !containsReason(metric.ReasonCodes, models.ReasonCurrencyConversionUnsupported) {
						t.Fatalf("mixed/unknown currency total = %+v", metric)
					}
				}
			} else {
				if report.Refunds.Status != models.MetricStatusAvailable || report.Refunds.Amount == nil || report.Refunds.Amount.String() != tt.wantAmount || report.Refunds.Currency != tt.wantCur {
					t.Fatalf("single currency summary = %+v; want %s %s", report.Refunds, tt.wantCur, tt.wantAmount)
				}
				for _, metric := range []models.MetricValue{refund, tableRefund} {
					if metric.Status != models.MetricStatusAvailable || metric.Value == nil || metric.Value.String() != tt.wantAmount || metric.Currency != tt.wantCur {
						t.Fatalf("single currency total = %+v; want %s %s", metric, tt.wantCur, tt.wantAmount)
					}
				}
			}
		})
	}
}

func TestAssistantRefundAttributionBoundary(t *testing.T) {
	cases := []struct {
		name       string
		identities []string
		wantLevel  models.EntityType
		wantID     string
		wantRows   map[string]string
	}{
		{name: "anonymous scope aggregate", identities: []string{""}},
		{name: "empty typed shop", identities: []string{"refund:shop:"}},
		{name: "unknown typed identity", identities: []string{"refund:unknown:S1"}},
		{name: "distinct shops", identities: []string{"refund:shop:S1", "refund:shop:S2"}, wantRows: map[string]string{"S1": "1", "S2": "2"}},
		{name: "distinct products", identities: []string{"refund:product:SKU1", "refund:product:SKU2"}},
		{name: "mixed levels", identities: []string{"refund:shop:S1", "refund:platform:P1"}, wantRows: map[string]string{"S1": "1"}},
		{name: "identity conflict never resets", identities: []string{"refund:shop:S1", "refund:shop:S2", "refund:shop:S1"}, wantRows: map[string]string{"S1": "4", "S2": "2"}},
		{name: "common shop", identities: []string{"refund:shop:S1", "refund:shop:S1"}, wantLevel: models.EntityTypeShop, wantID: "S1", wantRows: map[string]string{"S1": "3"}},
		{name: "common platform", identities: []string{"refund:platform:P1", "refund:platform:P1"}, wantLevel: models.EntityTypePlatform, wantID: "P1"},
		{name: "anonymous cannot borrow known shop", identities: []string{"", "refund:shop:S1"}, wantRows: map[string]string{"S1": "2"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			source := &assistantRefundBoundarySource{
				AssistantSourceRepository: NewAssistantFixtureSource(),
				refund: func(snapshot *models.FactSnapshot) {
					row := snapshot.Rows[0]
					snapshot.Rows = nil
					for i, id := range tt.identities {
						row.EntityID = id
						row.Sales = decimalFromBoundaryInt(i + 1)
						snapshot.Rows = append(snapshot.Rows, row)
					}
				},
			}
			report := invokeAssistantRefundBoundary(t, source)
			assertAssistantRefundSalesPreserved(t, report)
			if report.Refunds.Level != tt.wantLevel || report.Refunds.EntityID != tt.wantID {
				t.Fatalf("summary invented common attribution: %+v; want level=%s id=%s", report.Refunds, tt.wantLevel, tt.wantID)
			}
			wantAmount := fmt.Sprint(len(tt.identities) * (len(tt.identities) + 1) / 2)
			assertAssistantWorkflowMetric(t, report.Totals, models.MetricRefund, wantAmount)
			if report.Refunds.Amount == nil || report.Refunds.Amount.String() != wantAmount {
				t.Fatalf("request-scope aggregate lost: %+v", report.Refunds)
			}
			if report.Refunds.ProductRefund == nil || report.Refunds.ProductRefund.Value != nil || report.Refunds.ProductRefund.Status != models.MetricStatusUnavailable || !containsReason(report.Refunds.ProductRefund.ReasonCodes, models.ReasonProductAttributionUnavailable) {
				t.Fatalf("unattributed product refunds = %+v", report.Refunds.ProductRefund)
			}
			for _, row := range report.Table.Rows {
				metric := assistantWorkflowRequireMetric(t, row.Metrics, models.MetricRefund)
				if want, ok := tt.wantRows[row.EntityID]; ok {
					if metric.Status != models.MetricStatusAvailable || metric.Value == nil || metric.Value.String() != want || metric.Currency != "CNY" {
						t.Fatalf("shop %s refund = %+v; want CNY %s", row.EntityID, metric, want)
					}
				} else if metric.Status != models.MetricStatusUnavailable || metric.Value != nil || !containsReason(metric.ReasonCodes, models.ReasonProductAttributionUnavailable) {
					t.Fatalf("shop %s acquired unattributed refund: %+v", row.EntityID, metric)
				}
			}
		})
	}
}

func TestAssistantRefundCurrencyGroupsBoundary(t *testing.T) {
	source := &assistantRefundBoundarySource{
		AssistantSourceRepository: NewAssistantFixtureSource(),
		refund: func(snapshot *models.FactSnapshot) {
			row := snapshot.Rows[0]
			row.EntityID = "refund:shop:S1"
			row.Sales = decimalFromBoundaryInt(1)
			snapshot.Rows = []models.FactRow{row}
			row.Currency = "USD"
			row.Sales = decimalFromBoundaryInt(2)
			snapshot.Rows = append(snapshot.Rows, row)
			row.Currency = "CNY"
			row.Sales = decimalFromBoundaryInt(3)
			snapshot.Rows = append(snapshot.Rows, row)
		},
	}
	report := invokeAssistantRefundBoundary(t, source)
	assertAssistantRefundSalesPreserved(t, report)
	if report.Refunds.Amount != nil || report.Refunds.Status != models.MetricStatusUnavailable || !containsReason(report.Refunds.ReasonCodes, models.ReasonCurrencyConversionUnsupported) {
		t.Fatalf("mixed aggregate = %+v", report.Refunds)
	}
	for _, row := range report.Table.Rows {
		if row.EntityID == "S1" {
			metric := assistantWorkflowRequireMetric(t, row.Metrics, models.MetricRefund)
			if metric.Value == nil || metric.Value.String() != "4" || metric.Currency != "CNY" || metric.Status != models.MetricStatusAvailable {
				t.Fatalf("reliable CNY group = %+v, want CNY 4", metric)
			}
		}
	}
}

func TestAssistantRefundBusinessUnavailableBoundary(t *testing.T) {
	cases := []struct {
		name   string
		status models.CoverageStatus
		reason string
		mixed  bool
	}{
		{name: "missing independent coverage", status: models.CoverageStatusMissing, reason: models.ReasonCoverageMissing},
		{name: "stale independent coverage", status: models.CoverageStatusStale, reason: models.ReasonCoverageStale},
		{name: "unknown independent quality", status: models.CoverageStatusUnknown, reason: models.ReasonCoverageUnknown},
		{name: "covered refund cannot conceal uncovered refund", status: models.CoverageStatusMissing, reason: models.ReasonCoverageMissing, mixed: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			source := &assistantRefundBoundarySource{
				AssistantSourceRepository: NewAssistantFixtureSource(),
				refund: func(snapshot *models.FactSnapshot) {
					row := snapshot.Rows[0]
					row.EntityID = "refund:shop:S1"
					row.CoverageStatus = tt.status
					if tt.mixed {
						snapshot.Rows = append(snapshot.Rows, row)
					} else {
						snapshot.Rows = []models.FactRow{row}
					}
				},
			}
			report := invokeAssistantRefundBoundary(t, source)
			assertAssistantRefundSalesPreserved(t, report)
			assertAssistantRefundUnavailable(t, report, tt.reason)
			if report.Refunds.Level != "" || report.Refunds.EntityID != "" {
				t.Fatalf("uncovered refund published attribution: %+v", report.Refunds)
			}
		})
	}
	for _, declared := range []bool{false, true} {
		t.Run(fmt.Sprintf("capability declared=%t", declared), func(t *testing.T) {
			source := &assistantRefundBoundarySource{AssistantSourceRepository: NewAssistantFixtureSource()}
			if declared {
				source.refundErr = fmt.Errorf("refund business gate: %w", models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "refund", "refund quality is not verified"))
			} else {
				source.capabilities = func(capabilities *models.SourceCapabilities) {
					var supported []models.MetricID
					for _, metric := range capabilities.SupportedMetrics {
						if metric != models.MetricRefund {
							supported = append(supported, metric)
						}
					}
					capabilities.SupportedMetrics = supported
				}
			}
			report := invokeAssistantRefundBoundary(t, source)
			assertAssistantRefundSalesPreserved(t, report)
			assertAssistantRefundUnavailable(t, report, "capability_unavailable")
		})
	}
}

func TestAssistantRefundFailureBoundary(t *testing.T) {
	databaseErr := errors.New("database read failed")
	transportErr := errors.New("transport connection reset")
	for _, failure := range []error{databaseErr, transportErr, context.Canceled, context.DeadlineExceeded, models.NewValidationError(models.ErrorCodeSourceUnavailable, "source", "database unavailable"), models.NewValidationError(models.ErrorCodeUnauthorizedScope, "scope", "scope denied")} {
		t.Run(failure.Error(), func(t *testing.T) {
			source := &assistantRefundBoundarySource{AssistantSourceRepository: NewAssistantFixtureSource(), refundErr: failure}
			workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "refund-failure"})
			if !errors.Is(err, failure) {
				t.Fatalf("refund failure = %v, want wrapped %v", err, failure)
			}
		})
	}
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent cancelled deadline=%t", deadline), func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(time.Hour))
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			source := &assistantRefundBoundarySource{
				AssistantSourceRepository: NewAssistantFixtureSource(),
				refund: func(*models.FactSnapshot) { cancel() },
			}
			workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = workflow.Invoke(ctx, models.AnalysisReport{ReportID: "refund-cancelled"})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("parent cancellation = %v, want context.Canceled", err)
			}
		})
	}
	t.Run("parent deadline exceeded", func(t *testing.T) {
		source := &assistantRefundBoundarySource{AssistantSourceRepository: NewAssistantFixtureSource()}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err = workflow.Invoke(ctx, models.AnalysisReport{ReportID: "refund-deadline"})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("parent deadline = %v, want context.DeadlineExceeded", err)
		}
	})
}

func assertAssistantRefundSalesPreserved(t *testing.T, report models.AnalysisReport) {
	t.Helper()
	for metric, value := range map[models.MetricID]string{models.MetricQuantity: "3", models.MetricSales: "120", models.MetricCost: "72", models.MetricMargin: "48", models.MetricMarginRate: "0.4", models.MetricAveragePrice: "40"} {
		assertAssistantWorkflowMetric(t, report.Totals, metric, value)
	}
	if report.Outcome != models.OutcomeComplete || report.MarginLabel != "未扣退款" || len(report.Charts) == 0 {
		t.Fatalf("verified sales report was lost: outcome=%s label=%s charts=%d", report.Outcome, report.MarginLabel, len(report.Charts))
	}
}

func assertAssistantRefundUnavailable(t *testing.T, report models.AnalysisReport, reason string) {
	t.Helper()
	if report.Refunds.Status != models.MetricStatusUnavailable || report.Refunds.Amount != nil || !containsReason(report.Refunds.ReasonCodes, reason) {
		t.Fatalf("refund summary = %+v; want unavailable reason %s", report.Refunds, reason)
	}
	metrics := append([]models.MetricValue(nil), report.Totals...)
	metrics = append(metrics, report.Table.Totals...)
	for _, row := range report.Table.Rows {
		metrics = append(metrics, row.Metrics...)
	}
	for _, metric := range metrics {
		if metric.MetricID == models.MetricRefund && (metric.Status != models.MetricStatusUnavailable || metric.Value != nil || !containsReason(metric.ReasonCodes, reason)) {
			t.Fatalf("refund metric = %+v; want unavailable reason %s", metric, reason)
		}
	}
}

func invokeAssistantRefundBoundary(t *testing.T, source repositories.AssistantSourceRepository) models.AnalysisReport {
	t.Helper()
	workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "refund-boundary"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	return report
}

type assistantRefundBoundarySource struct {
	repositories.AssistantSourceRepository
	capabilities func(*models.SourceCapabilities)
	refund       func(*models.FactSnapshot)
	refundErr    error
}

func (s *assistantRefundBoundarySource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	capabilities, err := s.AssistantSourceRepository.Capabilities(ctx)
	if err == nil && s.capabilities != nil {
		s.capabilities(&capabilities)
	}
	return capabilities, err
}

func (s *assistantRefundBoundarySource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	snapshot, err := s.AssistantSourceRepository.ReadRefundFacts(ctx, query)
	if err != nil {
		return snapshot, err
	}
	if s.refund != nil {
		s.refund(&snapshot)
	}
	return snapshot, s.refundErr
}

func decimalFromBoundaryInt(value int) decimal.Decimal {
	return decimal.NewFromInt(int64(value))
}
