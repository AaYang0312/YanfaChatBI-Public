package services

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/dbmanager"
)

// assistantFixtureQuery builds the typed sales query for the frozen default
// window (2026-09-01..2026-09-08 exclusive) with all-scope.
func assistantFixtureQuery(t *testing.T, basis models.Basis) models.FactQuery {
	t.Helper()
	period, err := AssistantDefaultPeriod(AssistantFixtureClock(), models.AssistantDefaultTimezone)
	if err != nil {
		t.Fatalf("default period: %v", err)
	}
	return models.FactQuery{
		Basis:  basis,
		Window: period.DateRange,
		Scope:  models.Scope{Mode: models.ScopeModeAll},
		Metrics: []models.MetricID{
			models.MetricQuantity, models.MetricSales, models.MetricCost,
		},
	}
}

// assistantAssertSalesRows asserts the frozen numeric-fixture table for one
// basis and returns the rows for aggregate assertions.
func assistantAssertSalesRows(t *testing.T, rows []models.FactRow, basis models.Basis, s1Date, s2Date string) {
	t.Helper()
	if len(rows) != 2 {
		t.Fatalf("sales rows = %d want 2", len(rows))
	}
	want := []struct {
		shopID   string
		date     string
		quantity string
		sales    string
		cost     string
	}{
		{AssistantFixtureShopS1, s1Date, "2", "60", "30"},
		{AssistantFixtureShopS2, s2Date, "1", "60", "42"},
	}
	for i, w := range want {
		row := rows[i]
		if row.EntityID != assistantFixtureEntityID(w.shopID) || row.BusinessDate != w.date || row.Basis != basis {
			t.Fatalf("row %d = shop %s date %s basis %s, want %s %s %s",
				i, row.EntityID, row.BusinessDate, row.Basis, assistantFixtureEntityID(w.shopID), w.date, basis)
		}
		if row.Quantity.String() != w.quantity || row.Sales.String() != w.sales || row.Cost.String() != w.cost {
			t.Fatalf("row %s amounts = %s/%s/%s want %s/%s/%s",
				row.EntityID, row.Quantity, row.Sales, row.Cost, w.quantity, w.sales, w.cost)
		}
		if !row.CostVerified {
			t.Fatalf("row %s cost must be verified: the fixture period is cost-verified", row.EntityID)
		}
		if row.Currency != AssistantFixtureCurrency ||
			row.CoverageStatus != models.CoverageStatusCovered ||
			row.BundleRole != models.BundleRoleNone ||
			!row.GiftCostVerified {
			t.Fatalf("row %s flags = %s/%s/%s/%v", row.EntityID, row.Currency,
				row.CoverageStatus, row.BundleRole, row.GiftCostVerified)
		}
	}
}

func TestAssistantReadonly(t *testing.T) {
	injection := "'; drop table shops; --"
	ctx := context.Background()

	t.Run("fixture returns the frozen numeric fixture without a database", func(t *testing.T) {
		source := NewAssistantFixtureSource()

		t.Run("payment basis falls on the payment days", func(t *testing.T) {
			snapshot, err := source.ReadSalesFacts(ctx, assistantFixtureQuery(t, models.BasisPayment))
			if err != nil {
				t.Fatalf("ReadSalesFacts: %v", err)
			}
			if snapshot.Basis != models.BasisPayment || snapshot.ContractVersion != AssistantFixtureContractVersion {
				t.Fatalf("snapshot header = %s/%s", snapshot.Basis, snapshot.ContractVersion)
			}
			if snapshot.Window.StartDate != "2026-09-01" || snapshot.Window.EndDateExclusive != "2026-09-08" {
				t.Fatalf("window = %+v want the frozen default window", snapshot.Window)
			}
			assistantAssertSalesRows(t, snapshot.Rows, models.BasisPayment, "2026-09-01", "2026-09-02")
		})

		t.Run("outstock basis carries the same amounts on the shipping days", func(t *testing.T) {
			snapshot, err := source.ReadSalesFacts(ctx, assistantFixtureQuery(t, models.BasisOutstock))
			if err != nil {
				t.Fatalf("ReadSalesFacts: %v", err)
			}
			assistantAssertSalesRows(t, snapshot.Rows, models.BasisOutstock, "2026-09-03", "2026-09-04")
		})

		t.Run("aggregates reproduce the frozen totals including avg price 40", func(t *testing.T) {
			snapshot, err := source.ReadSalesFacts(ctx, assistantFixtureQuery(t, models.BasisPayment))
			if err != nil {
				t.Fatalf("ReadSalesFacts: %v", err)
			}
			quantity := decimal.Zero
			sales := decimal.Zero
			cost := decimal.Zero
			for _, row := range snapshot.Rows {
				quantity = quantity.Add(row.Quantity)
				sales = sales.Add(row.Sales)
				cost = cost.Add(row.Cost)
			}
			margin := sales.Sub(cost)
			if quantity.String() != "3" || sales.String() != "120" || cost.String() != "72" {
				t.Fatalf("totals = %s/%s/%s want 3/120/72", quantity, sales, cost)
			}
			if margin.String() != "48" {
				t.Fatalf("margin = %s want 48 (未扣退款)", margin)
			}
			if rate := margin.Div(sales); rate.String() != "0.4" {
				t.Fatalf("margin rate = %s want 0.4", rate)
			}
			// 合计成交均价必须为 40：不得对店铺均价 30 与 60 做简单平均而得到 45。
			avgPrice := sales.Div(quantity)
			if avgPrice.String() != "40" {
				t.Fatalf("avg price = %s want 40", avgPrice)
			}
			if naive := decimal.RequireFromString("30").Add(decimal.RequireFromString("60")).Div(decimal.NewFromInt(2)); naive.String() != "45" || naive.String() == avgPrice.String() {
				t.Fatalf("naive shop-price average must not equal the fixture average: %s vs %s", naive, avgPrice)
			}
		})

		t.Run("the refund is published only for a scope that provably covers the catalog", func(t *testing.T) {
			// The anonymous refund names no shop, platform, or product: a
			// selected subset it cannot be proven to belong to receives the
			// row with real unavailable semantics — coverage unknown, no
			// fabricated attribution, no dropped request condition. Scopes
			// that provably cover the whole fixed catalog keep the covered 10.
			cases := []struct {
				name        string
				scope       models.Scope
				wantCovered bool
			}{
				{name: "all scope keeps the covered refund", scope: models.Scope{Mode: models.ScopeModeAll}, wantCovered: true},
				{name: "selected platform covering every catalog shop keeps it", scope: models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}}, wantCovered: true},
				{name: "selected shops exhausting the catalog keep it", scope: models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{AssistantFixtureShopS1, AssistantFixtureShopS2}}, wantCovered: true},
				{name: "one catalog shop cannot prove it", scope: models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{AssistantFixtureShopS1}}},
				{name: "a shop outside the catalog cannot prove it", scope: models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{"S9"}}},
				{name: "mixed known and unknown shops cannot prove it", scope: models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{AssistantFixtureShopS1, "S9"}}},
			}
			for _, tt := range cases {
				t.Run(tt.name, func(t *testing.T) {
					query := assistantFixtureQuery(t, models.BasisPayment)
					query.Scope = tt.scope
					snapshot, err := source.ReadRefundFacts(ctx, query)
					if err != nil {
						t.Fatalf("ReadRefundFacts: %v", err)
					}
					if len(snapshot.Rows) != 1 {
						t.Fatalf("refund rows = %d want the one frozen refund, never dropped", len(snapshot.Rows))
					}
					row := snapshot.Rows[0]
					if row.EntityID != "" {
						t.Fatalf("refund attribution = %q want none", row.EntityID)
					}
					if tt.wantCovered {
						if row.CoverageStatus != models.CoverageStatusCovered {
							t.Fatalf("refund status = %s, want covered for a scope that provably covers the catalog", row.CoverageStatus)
						}
						return
					}
					if row.CoverageStatus != models.CoverageStatusUnknown {
						t.Fatalf("refund status = %s, want coverage unknown for an unprovable subset", row.CoverageStatus)
					}
				})
			}
		})

		t.Run("the refund is separate and never deducted from the product margin", func(t *testing.T) {
			snapshot, err := source.ReadRefundFacts(ctx, assistantFixtureQuery(t, models.BasisPayment))
			if err != nil {
				t.Fatalf("ReadRefundFacts: %v", err)
			}
			if len(snapshot.Rows) != 1 {
				t.Fatalf("refund rows = %d want 1", len(snapshot.Rows))
			}
			row := snapshot.Rows[0]
			if row.Sales.String() != AssistantFixtureRefundSales {
				t.Fatalf("refund = %s want %s", row.Sales, AssistantFixtureRefundSales)
			}
			if row.BusinessDate != "2026-09-05" {
				t.Fatalf("refund date = %s want the in-period 2026-09-05", row.BusinessDate)
			}
			if row.EntityID != "" {
				t.Fatalf("refund attribution = %q want none: 无商品归属的退款不摊到商品", row.EntityID)
			}
			if row.CostVerified {
				t.Fatal("refund cost must not claim verification: Q02/Q04 are open")
			}
			// 商品毛利合计仍为 48：退款独立列示。
			salesSnapshot, err := source.ReadSalesFacts(ctx, assistantFixtureQuery(t, models.BasisPayment))
			if err != nil {
				t.Fatalf("ReadSalesFacts: %v", err)
			}
			margin := decimal.Zero
			for _, factRow := range salesSnapshot.Rows {
				margin = margin.Add(factRow.Sales.Sub(factRow.Cost))
			}
			if margin.String() != "48" {
				t.Fatalf("product margin = %s want 48 after reading the refund", margin)
			}
		})
	})

	t.Run("injection text stays one bound parameter and cannot alter the fixture scope", func(t *testing.T) {
		source := NewAssistantFixtureSource()
		cleanQuery := assistantFixtureQuery(t, models.BasisPayment)
		injectedQuery := cleanQuery
		injectedQuery.Product = models.ProductSelection{ProductID: injection}

		cleanSnapshot, err := source.ReadSalesFacts(ctx, cleanQuery)
		if err != nil {
			t.Fatalf("clean read: %v", err)
		}
		injectedSnapshot, err := source.ReadSalesFacts(ctx, injectedQuery)
		if err != nil {
			t.Fatalf("injected read: %v", err)
		}
		// The hostile value is a literal, unknown product identifier: the source
		// returns no rows for it, never the two default rows, so it can never
		// prove a covered true zero.
		if len(cleanSnapshot.Rows) != 2 {
			t.Fatalf("clean rows = %d want 2", len(cleanSnapshot.Rows))
		}
		if len(injectedSnapshot.Rows) != 0 {
			t.Fatalf("hostile product returned %d rows, want 0: %+v", len(injectedSnapshot.Rows), injectedSnapshot.Rows)
		}
		if injectedSnapshot.Window != cleanSnapshot.Window || injectedSnapshot.Basis != cleanSnapshot.Basis {
			t.Fatal("injection changed the snapshot window or basis")
		}

		plans := source.Plans()
		if len(plans) != 2 {
			t.Fatalf("captured plans = %d want 2", len(plans))
		}
		cleanPlan, hostilePlan := plans[0], plans[1]

		// The captured plan shows exactly one bound parameter and it is the
		// hostile value: never SQL text, never a second statement.
		if hostilePlan.ProductID != injection {
			t.Fatalf("plan echoed ProductID = %q, want the injected value unchanged", hostilePlan.ProductID)
		}
		if len(hostilePlan.BoundParameters) != 1 || hostilePlan.BoundParameters[0] != injection {
			t.Fatalf("bound parameters = %#v, want exactly one: the injected value", hostilePlan.BoundParameters)
		}
		injectionCount := 0
		for _, param := range hostilePlan.BoundParameters {
			if param == injection {
				injectionCount++
			}
		}
		if injectionCount != 1 {
			t.Fatalf("injection appears %d times among bound parameters, want exactly 1", injectionCount)
		}

		// The scope echo is byte-identical to the clean plan: the injection
		// cannot widen, narrow, or rewrite the requested range.
		if hostilePlan.ScopeMode != cleanPlan.ScopeMode ||
			!reflect.DeepEqual(hostilePlan.ShopIDs, cleanPlan.ShopIDs) ||
			!reflect.DeepEqual(hostilePlan.PlatformIDs, cleanPlan.PlatformIDs) ||
			!reflect.DeepEqual(hostilePlan.Metrics, cleanPlan.Metrics) ||
			hostilePlan.Window != cleanPlan.Window ||
			hostilePlan.Basis != cleanPlan.Basis {
			t.Fatalf("captured scope changed:\nclean    = %+v\nhostile = %+v", cleanPlan, hostilePlan)
		}

		// No plan field outside the bound parameter carries SQL shape.
		for _, field := range []string{hostilePlan.Operation, string(hostilePlan.Basis),
			hostilePlan.Window.StartDate, hostilePlan.Window.EndDateExclusive, string(hostilePlan.ScopeMode)} {
			if strings.Contains(strings.ToLower(field), "drop") || strings.Contains(strings.ToLower(field), "select") ||
				strings.Contains(field, ";") {
				t.Fatalf("plan field %q carries SQL shape; the fixture path must stay enum-only", field)
			}
		}

		// Resolution of the hostile text yields zero candidates: it cannot
		// resolve to fixture identity and cannot widen the scope.
		resolved, err := source.ResolveEntities(ctx, repositories.AssistantEntityQuery{
			Product: models.ProductSelection{ProductID: injection},
			Scope:   models.Scope{Mode: models.ScopeModeAll},
		})
		if err != nil {
			t.Fatalf("ResolveEntities: %v", err)
		}
		if len(resolved) != 0 {
			t.Fatalf("hostile product text resolved to %d candidates, want 0", len(resolved))
		}
	})

	t.Run("a fixture-like source without a contract version is capability_unavailable", func(t *testing.T) {
		source := &assistantBlankContractSource{inner: NewAssistantFixtureSource()}
		snapshot, err := repositories.AssistantReadSnapshot(ctx, source, assistantFixtureQuery(t, models.BasisPayment))
		if err == nil {
			t.Fatal("AssistantReadSnapshot succeeded without a contract version")
		}
		validation, ok := models.AsValidationError(err)
		if !ok {
			t.Fatalf("error is not a models.ValidationError: %v", err)
		}
		if validation.Code != models.ErrorCodeCapabilityUnavailable {
			t.Fatalf("code = %s want %s", validation.Code, models.ErrorCodeCapabilityUnavailable)
		}
		if snapshot.Rows != nil {
			t.Fatalf("refused snapshot must carry no rows, got %d", len(snapshot.Rows))
		}
	})

	t.Run("postgresql read-only boundary verifies only with ASSISTANT_TEST_PG_DSN", func(t *testing.T) {
		dsn := os.Getenv("ASSISTANT_TEST_PG_DSN")
		if dsn == "" {
			t.Skip("Q05 unverified: ASSISTANT_TEST_PG_DSN is not set; " +
				"the PostgreSQL read-only boundary (SQLSTATE 25006, cancellation, idle release) is not exercised")
		}
		reader, err := dbmanager.NewAssistantReadonlyReader(dsn)
		if err != nil {
			t.Fatalf("open reader: %v", err)
		}
		defer reader.Close()
		if err := reader.Ping(ctx); err != nil {
			t.Fatalf("ping ASSISTANT_TEST_PG_DSN: %v", err)
		}

		probe, err := reader.RunWriteProbe(ctx, injection)
		if err != nil {
			t.Fatalf("write probe: %v", err)
		}
		if probe.Echo != injection {
			t.Fatalf("bound value echo = %q, want the injection text returned unchanged as data", probe.Echo)
		}
		if probe.WriteSQLState != "25006" {
			t.Fatalf("write SQLSTATE = %q want 25006", probe.WriteSQLState)
		}

		var boundSelect *dbmanager.AssistantExecutedStatement
		statements := reader.Statements()
		for i := range statements {
			if statements[i].SQL == "SELECT $1::text" {
				boundSelect = &statements[i]
			}
		}
		if boundSelect == nil || len(boundSelect.Params) != 1 || boundSelect.Params[0] != injection {
			t.Fatalf("captured SELECT = %+v, want SQL SELECT $1::text with exactly one bound parameter", boundSelect)
		}

		if err := reader.RunCancelProbe(ctx, "30", 3*time.Second); err != nil {
			t.Fatalf("cancel probe: %v", err)
		}

		for _, stmt := range reader.Statements() {
			if strings.Contains(strings.ToLower(stmt.SQL), "create extension") {
				t.Fatalf("statement log contains CREATE EXTENSION: %q", stmt.SQL)
			}
		}
		if stats := reader.Stats(); stats.InUse != 0 {
			t.Fatalf("connection still in use after the probes: %+v", stats)
		}
	})
}

// assistantBlankContractSource hides the fixture contract version to exercise
// the capability_unavailable gate through the repositories boundary.
type assistantBlankContractSource struct {
	inner *AssistantFixtureSource
}

func (s *assistantBlankContractSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	capabilities, err := s.inner.Capabilities(ctx)
	capabilities.ContractVersion = ""
	return capabilities, err
}

func (s *assistantBlankContractSource) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	return s.inner.ResolveEntities(ctx, query)
}

func (s *assistantBlankContractSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	return s.inner.ReadCoverage(ctx, query)
}

func (s *assistantBlankContractSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	return s.inner.ReadSalesFacts(ctx, query)
}

func (s *assistantBlankContractSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	return s.inner.ReadRefundFacts(ctx, query)
}
