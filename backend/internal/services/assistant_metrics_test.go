package services

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

func TestAssistantMetrics(t *testing.T) {
	t.Run("AC04 basis windows do not borrow slices", testAssistantMetricsAC04)
	t.Run("AC05 fixture window and exclusive end", testAssistantMetricsAC05)
	t.Run("AC07 bundle child gift and unverified parent", testAssistantMetricsAC07)
	t.Run("AC08 exact decimal totals", testAssistantMetricsAC08)
	t.Run("AC09 divide by zero is null", testAssistantMetricsAC09)
	t.Run("AC10 threshold hits and ranking", testAssistantMetricsAC10)
	t.Run("AC11 platform shop totals and currencies", testAssistantMetricsAC11)
	t.Run("AC13 missing coverage is not zero", testAssistantMetricsAC13)
	t.Run("AC14 partial excluded entity", testAssistantMetricsAC14)
	t.Run("AC15 empty versus unavailable", testAssistantMetricsAC15)
	t.Run("AC16 payment facts are not outstock", testAssistantMetricsAC16)
	t.Run("AC17 trend covered true zeros", testAssistantMetricsAC17)
	t.Run("AC17 trend unknown stays null", testAssistantMetricsTrendUnknown)
	t.Run("AC17 trend incomparable set", testAssistantMetricsTrendIncomparable)
	t.Run("AC18 unverified cost not zero filled", testAssistantMetricsAC18)
	t.Run("AC19 refund independent of margin", testAssistantMetricsAC19)
	t.Run("AC20 unattributed refund not product zero", testAssistantMetricsAC20)
}

func testAssistantMetricsAC04(t *testing.T) {
	snap := models.FactSnapshot{
		Basis:           "",
		Window:          models.DateRange{StartDate: "2026-09-01", EndDateExclusive: "2026-09-08"},
		ContractVersion: "fixture-v1",
		Rows: []models.FactRow{
			fact("S1", models.BasisPayment, "2026-09-01", "2", "60", "30", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
			fact("S1", models.BasisOutstock, "2026-09-03", "2", "60", "30", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
		},
	}

	payWin := fixtureRequest()
	payWin.Period.EndDateExclusive = "2026-09-02"
	cov := AssistantMetricCoverage{Main: coveredShops("S1")}
	pay := ComputeAnalysisReport(payWin, snap, cov, "run-ac04-pay")
	assertMetricString(t, pay.Totals, models.MetricQuantity, "2")
	assertMetricString(t, pay.Totals, models.MetricSales, "60")
	assertMetricString(t, pay.Totals, models.MetricCost, "30")

	outWin := fixtureRequest()
	outWin.Basis = models.BasisOutstock
	outWin.Period.StartDate = "2026-09-02"
	outWin.Period.EndDateExclusive = "2026-09-04"
	out := ComputeAnalysisReport(outWin, snap, cov, "run-ac04-out")
	assertMetricString(t, out.Totals, models.MetricQuantity, "2")
	assertMetricString(t, out.Totals, models.MetricSales, "60")
	assertMetricString(t, out.Totals, models.MetricCost, "30")

	payBorrow := fixtureRequest()
	payBorrow.Period.StartDate = "2026-09-02"
	payBorrow.Period.EndDateExclusive = "2026-09-04"
	payMiss := ComputeAnalysisReport(payBorrow, snap, cov, "run-ac04-pay-miss")
	assertMetricNotString(t, payMiss.Totals, models.MetricSales, "60")
	assertMetricNotString(t, payMiss.Totals, models.MetricQuantity, "2")

	outBorrow := fixtureRequest()
	outBorrow.Basis = models.BasisOutstock
	outBorrow.Period.EndDateExclusive = "2026-09-02"
	outMiss := ComputeAnalysisReport(outBorrow, snap, cov, "run-ac04-out-miss")
	assertMetricNotString(t, outMiss.Totals, models.MetricSales, "60")
	assertMetricNotString(t, outMiss.Totals, models.MetricQuantity, "2")
}

func testAssistantMetricsAC05(t *testing.T) {
	req := fixtureRequest()
	snap := numericSnapshot()
	snap.Rows = append(snap.Rows, fact("S1", models.BasisPayment, "2026-09-08T00:00:00+08:00", "9", "900", "90", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false))
	rep := ComputeAnalysisReport(req, snap, fixtureCoverage(), "run-ac05")

	wantStart, err := time.Parse(time.RFC3339, "2026-09-01T00:00:00+08:00")
	if err != nil {
		t.Fatalf("parse start: %v", err)
	}
	wantEnd, err := time.Parse(time.RFC3339, "2026-09-08T00:00:00+08:00")
	if err != nil {
		t.Fatalf("parse end: %v", err)
	}
	if !rep.WindowStart.Equal(wantStart) {
		t.Fatalf("window_start = %s, want %s", rep.WindowStart.Format(time.RFC3339), wantStart.Format(time.RFC3339))
	}
	if !rep.WindowEndExclusive.Equal(wantEnd) {
		t.Fatalf("window_end_exclusive = %s, want %s", rep.WindowEndExclusive.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
	}
	if got := rep.WindowStart.Format(time.RFC3339); got != "2026-09-01T00:00:00+08:00" {
		t.Fatalf("window_start rfc3339 = %s", got)
	}
	if got := rep.WindowEndExclusive.Format(time.RFC3339); got != "2026-09-08T00:00:00+08:00" {
		t.Fatalf("window_end_exclusive rfc3339 = %s", got)
	}
	assertMetricString(t, rep.Totals, models.MetricQuantity, "3")
	assertMetricNotString(t, rep.Totals, models.MetricQuantity, "12")
}

func testAssistantMetricsAC07(t *testing.T) {
	snap := numericSnapshot()
	snap.Rows = append(snap.Rows,
		fact("S1", models.BasisPayment, "2026-09-02", "99", "990", "99", true, "CNY", models.CoverageStatusCovered, models.BundleRoleChild, false),
		fact("S1", models.BasisPayment, "2026-09-02", "0", "0", "5", true, "CNY", models.CoverageStatusCovered, models.BundleRoleGift, true),
	)
	rep := ComputeAnalysisReport(fixtureRequest(), snap, fixtureCoverage(), "run-ac07")
	s1 := mustRow(t, rep, "S1")
	s2 := mustRow(t, rep, "S2")
	assertMetricString(t, s1.Metrics, models.MetricQuantity, "2")
	assertMetricString(t, s1.Metrics, models.MetricSales, "60")
	assertMetricString(t, s1.Metrics, models.MetricCost, "35")
	assertMetricString(t, s2.Metrics, models.MetricQuantity, "1")
	assertMetricString(t, s2.Metrics, models.MetricSales, "60")
	assertMetricString(t, rep.Totals, models.MetricQuantity, "3")
	assertMetricString(t, rep.Totals, models.MetricSales, "120")
	assertMetricString(t, rep.Totals, models.MetricCost, "77")

	unverified := numericSnapshot()
	for i := range unverified.Rows {
		if unverified.Rows[i].EntityID == "S1" {
			unverified.Rows[i].BundleRole = models.BundleRoleParent
			unverified.Rows[i].CostVerified = false
		}
	}
	bad := ComputeAnalysisReport(fixtureRequest(), unverified, fixtureCoverage(), "run-ac07-bundle")
	s1bad := mustRow(t, bad, "S1")
	assertMetricString(t, s1bad.Metrics, models.MetricQuantity, "2")
	assertMetricString(t, s1bad.Metrics, models.MetricSales, "60")
	assertMetricNull(t, s1bad.Metrics, models.MetricMargin, models.ReasonBundleUnverified)
	assertMetricNull(t, s1bad.Metrics, models.MetricCost, models.ReasonBundleUnverified)
}

func testAssistantMetricsAC08(t *testing.T) {
	rep := ComputeAnalysisReport(fixtureRequest(), numericSnapshot(), fixtureCoverage(), "run-ac08")
	assertMetricString(t, rep.Totals, models.MetricSales, "120")
	assertMetricString(t, rep.Totals, models.MetricCost, "72")
	assertMetricString(t, rep.Totals, models.MetricMargin, "48")
	assertMetricString(t, rep.Totals, models.MetricMarginRate, "0.4")
	assertMetricString(t, rep.Totals, models.MetricAveragePrice, "40")
	assertMetricString(t, mustRow(t, rep, "S1").Metrics, models.MetricMarginRate, "0.5")
	assertMetricString(t, mustRow(t, rep, "S2").Metrics, models.MetricMarginRate, "0.3")

	raw, err := json.Marshal(rep.Totals)
	if err != nil {
		t.Fatalf("marshal totals: %v", err)
	}
	body := string(raw)
	for _, want := range []string{`"120"`, `"72"`, `"48"`, `"0.4"`, `"40"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("totals json %s missing %s", body, want)
		}
	}
	if strings.Contains(body, `:120`) || strings.Contains(body, `:0.4`) {
		t.Fatalf("totals json used numeric decimals: %s", body)
	}
}

func testAssistantMetricsAC09(t *testing.T) {
	snap := models.FactSnapshot{
		Basis:           models.BasisPayment,
		ContractVersion: "fixture-v1",
		Rows: []models.FactRow{
			fact("S1", models.BasisPayment, "2026-09-01", "0", "0", "0", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
		},
	}
	rep := ComputeAnalysisReport(fixtureRequest(), snap, AssistantMetricCoverage{Main: coveredShops("S1")}, "run-ac09")
	assertMetricString(t, rep.Totals, models.MetricQuantity, "0")
	assertMetricString(t, rep.Totals, models.MetricSales, "0")
	assertMetricNull(t, rep.Totals, models.MetricAveragePrice, models.ReasonDivideByZero)
	assertMetricNull(t, rep.Totals, models.MetricMarginRate, models.ReasonDivideByZero)
	raw, err := json.Marshal(findMetric(rep.Totals, models.MetricAveragePrice))
	if err != nil {
		t.Fatalf("marshal avg: %v", err)
	}
	if strings.Contains(string(raw), `"value":"0"`) {
		t.Fatalf("zero denominator serialized as 0: %s", raw)
	}
	if strings.Contains(strings.ToLower(string(raw)), "nan") || strings.Contains(strings.ToLower(string(raw)), "inf") {
		t.Fatalf("zero denominator produced nan/inf: %s", raw)
	}
}

func testAssistantMetricsAC10(t *testing.T) {
	threshold := decimal.RequireFromString("0.4")
	with := fixtureRequest()
	with.LowMarginThreshold = &threshold
	hit := ComputeAnalysisReport(with, numericSnapshot(), fixtureCoverage(), "run-ac10-hit")
	s1 := mustRow(t, hit, "S1")
	s2 := mustRow(t, hit, "S2")
	if s1.ThresholdHit {
		t.Fatal("S1 rate 0.5 must not hit threshold 0.4")
	}
	if !s2.ThresholdHit {
		t.Fatal("S2 rate 0.3 must hit threshold 0.4")
	}
	if len(hit.Candidates) != 1 || hit.Candidates[0].EntityID != "S2" {
		t.Fatalf("candidates = %+v, want only S2", idsOf(hit.Candidates))
	}

	ranked := ComputeAnalysisReport(fixtureRequest(), numericSnapshot(), fixtureCoverage(), "run-ac10-rank")
	if len(ranked.Candidates) != 0 {
		t.Fatalf("no threshold must yield zero hits, got %d", len(ranked.Candidates))
	}
	for _, row := range ranked.Table.Rows {
		if row.ThresholdHit {
			t.Fatalf("%s threshold_hit without a threshold", row.EntityID)
		}
		if row.Rank <= 0 {
			t.Fatalf("%s rank = %d, want assigned rank", row.EntityID, row.Rank)
		}
	}
}

func testAssistantMetricsAC11(t *testing.T) {
	snap := models.FactSnapshot{
		Basis:           models.BasisPayment,
		ContractVersion: "fixture-v1",
		Rows: []models.FactRow{
			fact("P1/S1", models.BasisPayment, "2026-09-01", "2", "60", "30", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
			fact("P1/S2", models.BasisPayment, "2026-09-01", "1", "60", "42", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
			fact("P2/U1", models.BasisPayment, "2026-09-01", "1", "10", "4", true, "USD", models.CoverageStatusCovered, models.BundleRoleNone, false),
		},
	}
	shopReq := fixtureRequest()
	shopReq.Workflow = models.WorkflowSalesComparison
	shopReq.GroupBy = models.GroupByShop
	shopReq.Scope = models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{"P1"}, ShopIDs: []string{"S1", "S2"}}
	shops := ComputeAnalysisReport(shopReq, snap, AssistantMetricCoverage{Main: coveredShops("S1", "S2")}, "run-ac11-shop")

	platReq := fixtureRequest()
	platReq.Workflow = models.WorkflowSalesComparison
	platReq.GroupBy = models.GroupByPlatform
	platReq.Scope = models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{"P1", "P2"}}
	platforms := ComputeAnalysisReport(platReq, snap, AssistantMetricCoverage{Main: coveragePlatforms(models.CoverageStatusCovered, "P1", "P2")}, "run-ac11-plat")

	p1 := mustRow(t, platforms, "P1")
	shopSales := mustDec(t, mustRow(t, shops, "S1").Metrics, models.MetricSales).Add(mustDec(t, mustRow(t, shops, "S2").Metrics, models.MetricSales))
	if !mustDec(t, p1.Metrics, models.MetricSales).Equal(shopSales) {
		t.Fatalf("platform P1 sales %s != shop children %s", mustDec(t, p1.Metrics, models.MetricSales), shopSales)
	}
	assertMetricString(t, p1.Metrics, models.MetricSales, "120")
	assertMetricString(t, mustRow(t, platforms, "P2").Metrics, models.MetricSales, "10")
	if mustRow(t, platforms, "P1").Currency == mustRow(t, platforms, "P2").Currency {
		t.Fatal("CNY and USD must remain separate groups")
	}
	assertMetricNull(t, platforms.Totals, models.MetricSales, models.ReasonCurrencyConversionUnsupported)
}

func testAssistantMetricsAC13(t *testing.T) {
	req := fixtureRequest()
	req.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{"S1", "S3"}}
	snap := models.FactSnapshot{
		Basis:           models.BasisPayment,
		ContractVersion: "fixture-v1",
		Rows: []models.FactRow{
			fact("S1", models.BasisPayment, "2026-09-01", "2", "60", "30", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
			fact("S3", models.BasisPayment, "2026-09-01", "0", "0", "0", false, "CNY", models.CoverageStatusUnknown, models.BundleRoleNone, false),
		},
	}
	rep := ComputeAnalysisReport(req, snap, AssistantMetricCoverage{Main: append(coveredShops("S1"), coverageShops(models.CoverageStatusUnknown, "S3")...)}, "run-ac13")
	s3 := mustRow(t, rep, "S3")
	assertMetricNull(t, s3.Metrics, models.MetricQuantity, models.ReasonCoverageUnknown)
	assertMetricNull(t, s3.Metrics, models.MetricSales, models.ReasonCoverageUnknown)
	pt := mustChartPoint(t, rep, "S3")
	if pt.Value != nil {
		t.Fatalf("uncovered chart value = %s, want null not 0", pt.Value)
	}
	if pt.Status != models.MetricStatusUnavailable {
		t.Fatalf("uncovered chart status = %s", pt.Status)
	}
}

func testAssistantMetricsAC14(t *testing.T) {
	req := fixtureRequest()
	req.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{"S1", "S2", "S3"}}
	rep := ComputeAnalysisReport(req, numericSnapshot(), AssistantMetricCoverage{Main: append(coveredShops("S1", "S2"), coverageShops(models.CoverageStatusMissing, "S3")...)}, "run-ac14")
	if rep.Outcome != models.OutcomePartial {
		t.Fatalf("outcome = %s, want partial", rep.Outcome)
	}
	var excluded *models.CoverageEntity
	for i := range rep.Coverage.Excluded {
		if rep.Coverage.Excluded[i].EntityID == "S3" {
			excluded = &rep.Coverage.Excluded[i]
			break
		}
	}
	if excluded == nil {
		t.Fatalf("excluded = %v, want S3", coverageIDs(rep.Coverage.Excluded))
	}
	if excluded.CoverageRef == "" {
		t.Fatal("excluded S3 missing coverage_ref")
	}
	if !containsID(rep.Coverage.Included, "S1") || !containsID(rep.Coverage.Included, "S2") {
		t.Fatalf("included = %v, want S1 and S2", coverageIDs(rep.Coverage.Included))
	}
}

func testAssistantMetricsAC15(t *testing.T) {
	emptyReq := fixtureRequest()
	emptyReq.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{"S1"}}
	emptySnap := models.FactSnapshot{
		Basis:           models.BasisPayment,
		ContractVersion: "fixture-v1",
		Rows: []models.FactRow{
			fact("S1", models.BasisPayment, "2026-09-01", "0", "0", "0", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
		},
	}
	empty := ComputeAnalysisReport(emptyReq, emptySnap, AssistantMetricCoverage{Main: coveredShops("S1")}, "run-ac15-empty")
	if empty.Outcome != models.OutcomeEmpty {
		t.Fatalf("covered zero outcome = %s, want empty", empty.Outcome)
	}
	assertMetricString(t, empty.Totals, models.MetricQuantity, "0")
	assertMetricString(t, empty.Totals, models.MetricSales, "0")

	missReq := fixtureRequest()
	missReq.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{"S1", "S2"}}
	miss := ComputeAnalysisReport(missReq, models.FactSnapshot{Basis: models.BasisPayment, ContractVersion: "fixture-v1"}, AssistantMetricCoverage{Main: coverageShops(models.CoverageStatusMissing, "S1", "S2")}, "run-ac15-miss")
	if miss.Outcome != models.OutcomeUnavailable {
		t.Fatalf("no coverage outcome = %s, want unavailable", miss.Outcome)
	}
	assertMetricNull(t, miss.Totals, models.MetricQuantity, models.ReasonCoverageMissing)
	assertMetricNull(t, miss.Totals, models.MetricSales, models.ReasonCoverageMissing)
}

func testAssistantMetricsAC16(t *testing.T) {
	req := fixtureRequest()
	req.Basis = models.BasisOutstock
	rep := ComputeAnalysisReport(req, numericSnapshot(), fixtureCoverage(), "run-ac16")
	if rep.Outcome != models.OutcomeUnavailable {
		t.Fatalf("outstock on payment snapshot outcome = %s", rep.Outcome)
	}
	assertMetricNull(t, rep.Totals, models.MetricSales, models.ReasonBasisUnavailable)
	assertMetricNull(t, rep.Totals, models.MetricQuantity, models.ReasonBasisUnavailable)
	assertMetricNotString(t, rep.Totals, models.MetricSales, "120")
	assertMetricNotString(t, rep.Totals, models.MetricQuantity, "3")
}

func testAssistantMetricsAC17(t *testing.T) {
	req := fixtureRequest()
	req.TrendPeriod = &models.DateRange{StartDate: "2026-09-01", EndDateExclusive: "2026-09-08"}
	snap := models.FactSnapshot{
		Basis:           models.BasisPayment,
		Window:          models.DateRange{StartDate: models.AssistantDefaultStartDate, EndDateExclusive: models.AssistantDefaultEndDateExclu},
		ContractVersion: "fixture-v1",
		Rows: []models.FactRow{
			fact("S1", models.BasisPayment, "2026-09-01", "2", "60", "30", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
			fact("S2", models.BasisPayment, "2026-09-02", "1", "60", "42", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
		},
	}
	cov := AssistantMetricCoverage{Main: coveredShops("S1", "S2"), Trend: coveredShops("S1", "S2")}
	rep := ComputeAnalysisReport(req, snap, cov, "run-ac17")

	byDate := map[string]models.ChartPoint{}
	for _, p := range rep.Trend {
		byDate[p.BusinessDate] = p
	}
	for _, date := range []string{"2026-09-01", "2026-09-02"} {
		point, ok := byDate[date]
		if !ok || point.Value == nil || point.Value.String() != "60" || point.Status != models.MetricStatusAvailable {
			t.Fatalf("trend %s = %+v, want available 60", date, point)
		}
	}
	for _, date := range []string{"2026-09-03", "2026-09-04", "2026-09-05", "2026-09-06", "2026-09-07"} {
		point, ok := byDate[date]
		if !ok || point.Value == nil || !point.Value.IsZero() || point.Status != models.MetricStatusAvailable {
			t.Fatalf("trend %s = %+v, want covered true zero", date, point)
		}
	}
	if len(rep.Trend) != 7 {
		t.Fatalf("trend dates = %v, want seven", trendDates(rep.Trend))
	}
}

func testAssistantMetricsTrendUnknown(t *testing.T) {
	req := fixtureRequest()
	req.TrendPeriod = &models.DateRange{StartDate: "2026-08-25", EndDateExclusive: "2026-09-01"}
	snap := numericSnapshot()
	snap.Rows = append(snap.Rows,
		fact("S1", models.BasisPayment, "2026-08-25", "1", "20", "10", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
	)
	cov := AssistantMetricCoverage{
		Main:  coveredShops("S1", "S2"),
		Trend: coverageShops(models.CoverageStatusUnknown, "S1", "S2"),
	}
	rep := ComputeAnalysisReport(req, snap, cov, "run-ac17-unknown")
	var gap bool
	for _, point := range rep.Trend {
		if point.Value == nil {
			gap = true
		}
		if point.Value != nil && point.Value.IsZero() && containsReason(point.ReasonCodes, models.ReasonCoverageUnknown) {
			t.Fatalf("unknown trend date became zero: %+v", point)
		}
		if point.Status != models.MetricStatusUnavailable {
			t.Fatalf("unknown trend date status = %s, want unavailable", point.Status)
		}
	}
	if !gap {
		t.Fatal("trend has no null gap")
	}
}

func testAssistantMetricsTrendIncomparable(t *testing.T) {
	req := fixtureRequest()
	req.TrendPeriod = &models.DateRange{StartDate: "2026-09-01", EndDateExclusive: "2026-09-08"}
	snap := numericSnapshot()
	cov := AssistantMetricCoverage{
		Main:  coveredShops("S1", "S2"),
		Trend: coveredShops("S1"),
	}
	rep := ComputeAnalysisReport(req, snap, cov, "run-ac17-incomparable")
	incomparable := false
	for _, p := range rep.Trend {
		if containsReason(p.ReasonCodes, models.ReasonTrendIncomparable) {
			incomparable = true
		}
	}
	if !incomparable {
		t.Fatal("different covered entity sets must mark trend incomparable; no growth rate is emitted")
	}
	for _, m := range rep.Totals {
		if strings.Contains(string(m.MetricID), "growth") {
			t.Fatalf("emitted growth metric %s", m.MetricID)
		}
	}
}

func testAssistantMetricsAC18(t *testing.T) {
	snap := numericSnapshot()
	for i := range snap.Rows {
		if snap.Rows[i].EntityID == "S2" {
			snap.Rows[i].CostVerified = false
		}
	}
	rep := ComputeAnalysisReport(fixtureRequest(), snap, fixtureCoverage(), "run-ac18")
	s2 := mustRow(t, rep, "S2")
	assertMetricString(t, s2.Metrics, models.MetricQuantity, "1")
	assertMetricString(t, s2.Metrics, models.MetricSales, "60")
	assertMetricNull(t, s2.Metrics, models.MetricMargin, models.ReasonCostUnverified)
	assertMetricNull(t, s2.Metrics, models.MetricCost, models.ReasonCostUnverified)
	assertMetricNull(t, s2.Metrics, models.MetricMarginRate, models.ReasonCostUnverified)
}

func testAssistantMetricsAC19(t *testing.T) {
	snap := numericSnapshot()
	snap.Rows = append(snap.Rows, fact("refund:product:P1-SKU-1", models.BasisPayment, "2026-09-05", "0", "10", "0", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false))
	rep := ComputeAnalysisReport(fixtureRequest(), snap, fixtureCoverage(), "run-ac19")
	assertMetricString(t, rep.Totals, models.MetricMargin, "48")
	assertMetricString(t, rep.Totals, models.MetricRefund, "10")
	if rep.MarginLabel != "未扣退款" {
		t.Fatalf("margin_label = %q", rep.MarginLabel)
	}
	if rep.Refunds.Label != "未扣退款" {
		t.Fatalf("refunds.label = %q", rep.Refunds.Label)
	}
	if rep.Refunds.Amount == nil || rep.Refunds.Amount.String() != "10" {
		t.Fatalf("refunds.amount = %v", rep.Refunds.Amount)
	}
	if rep.Refunds.OccurrenceWindow != rep.Window.DateRange {
		t.Fatalf("refund occurrence window = %+v", rep.Refunds.OccurrenceWindow)
	}
}

func testAssistantMetricsAC20(t *testing.T) {
	snap := numericSnapshot()
	snap.Rows = append(snap.Rows, fact("refund:shop:S1", models.BasisPayment, "2026-09-05", "0", "10", "0", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false))
	rep := ComputeAnalysisReport(fixtureRequest(), snap, fixtureCoverage(), "run-ac20")
	if rep.Refunds.Level != models.EntityTypeShop && rep.Refunds.Level != models.EntityTypePlatform {
		t.Fatalf("unattributed refund level = %s, want shop or platform", rep.Refunds.Level)
	}
	if !containsReason(rep.Refunds.ReasonCodes, models.ReasonProductAttributionUnavailable) {
		t.Fatalf("refund reasons = %v, want product_attribution_unavailable", rep.Refunds.ReasonCodes)
	}
	if rep.Refunds.ProductRefund == nil {
		t.Fatal("product_refund missing")
	}
	if rep.Refunds.ProductRefund.Value != nil {
		t.Fatalf("product refund value = %s, must not emit 0", rep.Refunds.ProductRefund.Value)
	}
	if !containsReason(rep.Refunds.ProductRefund.ReasonCodes, models.ReasonProductAttributionUnavailable) {
		t.Fatalf("product refund reasons = %v", rep.Refunds.ProductRefund.ReasonCodes)
	}
	s2refund := findMetric(mustRow(t, rep, "S2").Metrics, models.MetricRefund)
	if s2refund.Value != nil && s2refund.Value.String() == "0" {
		t.Fatal("unattributed product refund emitted as 0 on S2")
	}
	assertMetricString(t, mustRow(t, rep, "S1").Metrics, models.MetricRefund, "10")
}

func fixtureRequest() models.AnalysisRequest {
	return models.AnalysisRequest{
		SchemaVersion: models.AssistantSchemaVersion,
		RequestID:     "req-metrics",
		Workflow:      models.WorkflowProductAnalysis,
		Basis:         models.BasisPayment,
		Period: models.Period{
			DateRange: models.DateRange{StartDate: models.AssistantDefaultStartDate, EndDateExclusive: models.AssistantDefaultEndDateExclu},
			Timezone:  models.AssistantDefaultTimezone,
		},
		Scope:   models.Scope{Mode: models.ScopeModeAll, ShopIDs: []string{"S1", "S2"}},
		Product: models.ProductSelection{ProductID: "P1-SKU-1"},
		GroupBy: models.GroupByShop,
		Metrics: models.SupportedMetrics(),
	}
}

func numericSnapshot() models.FactSnapshot {
	return models.FactSnapshot{
		Basis:           models.BasisPayment,
		Window:          models.DateRange{StartDate: models.AssistantDefaultStartDate, EndDateExclusive: models.AssistantDefaultEndDateExclu},
		ContractVersion: "fixture-v1",
		Rows: []models.FactRow{
			fact("S1", models.BasisPayment, "2026-09-01", "2", "60", "30", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
			fact("S2", models.BasisPayment, "2026-09-01", "1", "60", "42", true, "CNY", models.CoverageStatusCovered, models.BundleRoleNone, false),
		},
	}
}

func coverageShops(status models.CoverageStatus, ids ...string) []repositories.AssistantCoverage {
	out := make([]repositories.AssistantCoverage, 0, len(ids))
	for _, id := range ids {
		out = append(out, repositories.AssistantCoverage{EntityType: models.EntityTypeShop, EntityID: id, Status: status})
	}
	return out
}

func coveragePlatforms(status models.CoverageStatus, ids ...string) []repositories.AssistantCoverage {
	out := make([]repositories.AssistantCoverage, 0, len(ids))
	for _, id := range ids {
		out = append(out, repositories.AssistantCoverage{EntityType: models.EntityTypePlatform, EntityID: id, Status: status})
	}
	return out
}

func coveredShops(ids ...string) []repositories.AssistantCoverage {
	return coverageShops(models.CoverageStatusCovered, ids...)
}

func fixtureCoverage() AssistantMetricCoverage {
	return AssistantMetricCoverage{Main: coveredShops("S1", "S2")}
}

func fact(entityID string, basis models.Basis, date, qty, sales, cost string, verified bool, currency string, coverage models.CoverageStatus, role models.BundleRole, giftVerified bool) models.FactRow {
	return models.FactRow{
		EntityID:         entityID,
		Basis:            basis,
		BusinessDate:     date,
		Quantity:         decimal.RequireFromString(qty),
		Sales:            decimal.RequireFromString(sales),
		Cost:             decimal.RequireFromString(cost),
		CostVerified:     verified,
		Currency:         currency,
		CoverageStatus:   coverage,
		BundleRole:       role,
		GiftCostVerified: giftVerified,
	}
}

func findMetric(values []models.MetricValue, id models.MetricID) models.MetricValue {
	for _, m := range values {
		if m.MetricID == id {
			return m
		}
	}
	return models.MetricValue{MetricID: id}
}

func assertMetricString(t *testing.T, values []models.MetricValue, id models.MetricID, want string) {
	t.Helper()
	m := findMetric(values, id)
	if m.Status == models.MetricStatusUnavailable {
		t.Fatalf("%s status = %s reasons=%v, want value %s", id, m.Status, m.ReasonCodes, want)
	}
	if m.Value == nil {
		t.Fatalf("%s value is null, want %s", id, want)
	}
	if m.Value.String() != want {
		t.Fatalf("%s = %s, want %s", id, m.Value.String(), want)
	}
}

func assertMetricNotString(t *testing.T, values []models.MetricValue, id models.MetricID, banned string) {
	t.Helper()
	m := findMetric(values, id)
	if m.Value != nil && m.Value.String() == banned {
		t.Fatalf("%s unexpectedly %s", id, banned)
	}
}

func assertMetricNull(t *testing.T, values []models.MetricValue, id models.MetricID, reason string) {
	t.Helper()
	m := findMetric(values, id)
	if m.Value != nil {
		t.Fatalf("%s value = %s, want null", id, m.Value.String())
	}
	if m.Status != models.MetricStatusUnavailable {
		t.Fatalf("%s status = %s, want unavailable", id, m.Status)
	}
	if !containsReason(m.ReasonCodes, reason) {
		t.Fatalf("%s reasons = %v, want %s", id, m.ReasonCodes, reason)
	}
}

func mustDec(t *testing.T, values []models.MetricValue, id models.MetricID) decimal.Decimal {
	t.Helper()
	m := findMetric(values, id)
	if m.Value == nil {
		t.Fatalf("%s value is null", id)
	}
	return *m.Value
}

func mustRow(t *testing.T, rep models.AnalysisReport, entityID string) models.ReportRow {
	t.Helper()
	for _, row := range rep.Table.Rows {
		if row.EntityID == entityID {
			return row
		}
	}
	t.Fatalf("row %s not found in %v", entityID, idsOf(rep.Table.Rows))
	return models.ReportRow{}
}

func mustChartPoint(t *testing.T, rep models.AnalysisReport, entityID string) models.ChartPoint {
	t.Helper()
	for _, chart := range rep.Charts {
		if chart.Kind != models.ChartKindBar {
			continue
		}
		for _, p := range chart.Points {
			if p.EntityID == entityID || p.Dimension == entityID {
				return p
			}
		}
	}
	t.Fatalf("chart point %s not found", entityID)
	return models.ChartPoint{}
}

func idsOf(rows []models.ReportRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.EntityID)
	}
	return out
}

func coverageIDs(ents []models.CoverageEntity) []string {
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.EntityID)
	}
	return out
}

func containsID(ents []models.CoverageEntity, id string) bool {
	for _, e := range ents {
		if e.EntityID == id {
			return true
		}
	}
	return false
}

func containsReason(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func trendDates(points []models.ChartPoint) []string {
	out := make([]string, 0, len(points))
	for _, p := range points {
		out = append(out, p.BusinessDate)
	}
	return out
}
