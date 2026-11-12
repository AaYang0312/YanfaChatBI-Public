package services

import (
	"testing"

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
)

func TestAssistantMetricsQuantityOnlyReport(t *testing.T) {
	req := fixtureRequest()
	req.Metrics = []models.MetricID{models.MetricQuantity}
	req.Period.StartDate = "2026-09-01"
	req.Period.EndDateExclusive = "2026-09-03"
	req.TrendPeriod = &req.Period.DateRange
	snap := models.FactSnapshot{
		Basis: req.Basis,
		Rows: []models.FactRow{{
			EntityID: "S1", Basis: req.Basis, BusinessDate: "2026-09-01",
			Quantity: decimal.NewFromInt(7), CoverageStatus: models.CoverageStatusCovered,
		}},
	}
	covered := coveredShops("S1")
	report := ComputeAnalysisReport(req, snap, AssistantMetricCoverage{Main: covered, Trend: covered}, "quantity-only")

	if report.Outcome != models.OutcomeComplete || len(report.Coverage.Metrics) != 1 ||
		report.Coverage.Metrics[0] != models.MetricQuantity {
		t.Fatalf("outcome = %s, coverage metrics = %v; want complete quantity coverage", report.Outcome, report.Coverage.Metrics)
	}
	if len(report.Totals) != 1 {
		t.Fatalf("totals = %+v; want quantity only", report.Totals)
	}
	assertMetricString(t, report.Totals, models.MetricQuantity, "7")

	checkPoints := func(label string, points []models.ChartPoint, values ...int64) {
		t.Helper()
		if len(points) != len(values) {
			t.Fatalf("%s: got %d points, want %d", label, len(points), len(values))
		}
		for i, point := range points {
			if point.MetricID != models.MetricQuantity || point.Status != models.MetricStatusAvailable ||
				point.CoverageRef == "" || point.Value == nil || !point.Value.Equal(decimal.NewFromInt(values[i])) {
				t.Errorf("%s[%d] = %+v; want covered quantity %d", label, i, point, values[i])
			}
		}
	}

	checkPoints("trend", report.Trend, 7, 0)
	if len(report.Charts) != 2 {
		t.Fatalf("got %d charts, want a bar and a line", len(report.Charts))
	}
	for _, chart := range report.Charts {
		if len(chart.MetricIDs) != 1 || chart.MetricIDs[0] != models.MetricQuantity {
			t.Errorf("%s metrics = %v; want quantity only", chart.Kind, chart.MetricIDs)
		}
	}
	bar, line := report.Charts[0], report.Charts[1]
	if bar.Kind != models.ChartKindBar || bar.Title != "商品销量" || line.Kind != models.ChartKindLine {
		t.Errorf("unexpected chart kinds or bar title: %s %q, %s", bar.Kind, bar.Title, line.Kind)
	}
	checkPoints("bar", bar.Points, 7)
	checkPoints("line", line.Points, 7, 0)
}
