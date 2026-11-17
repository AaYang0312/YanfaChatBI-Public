package services

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/dbmanager"
)

func TestGameDemoConnectionChangeFailsBeforeFixtureFallback(t *testing.T) {
	owner, id := primitive.NewObjectID(), primitive.NewObjectID()
	port, role, password := "5432", "game_demo_reader", "synthetic-demo-reader-only"
	chat := &models.Chat{Base: models.Base{ID: id}, UserID: owner, Connection: models.Connection{Type: "postgresql", Host: "game-demo-postgres", Database: "gameshelf_demo", Port: &port, Username: &role, Password: &password}}
	ctx, err := sourceOriginContext(context.Background(), chat, chat.Connection)
	if err != nil {
		t.Fatal(err)
	}
	chat.Connection.Host = "other-postgres"
	svc := &assistantService{demoEnabled: true, chats: assistantServiceChatLookup{chat: chat}}
	_, _, err = svc.openGameDemoSource(ctx, models.AccessContext{OwnerID: owner.Hex(), ChatID: id.Hex()}, &SourceRead{Classification: models.SourceIndependent, Context: ctx})
	if !errors.Is(err, ErrSourceBindingConflict) {
		t.Fatalf("changed connection must fail before fixture fallback, got %v", err)
	}
}

type gameDemoNoRead struct{ calls int }

func (r *gameDemoNoRead) WithReadOnlyTx(context.Context, func(*sql.Tx) error) error {
	r.calls++
	return nil
}

func gameDemoRequest(product string) models.AnalysisRequest {
	return models.AnalysisRequest{SchemaVersion: 1, RequestID: "game-demo-eval", Workflow: models.WorkflowProductAnalysis,
		Basis: models.BasisPayment, Period: models.Period{DateRange: models.DateRange{StartDate: "2026-11-02", EndDateExclusive: "2026-11-09"}, Timezone: "Asia/Shanghai"},
		Scope: models.Scope{Mode: models.ScopeModeAll}, Product: models.ProductSelection{ProductID: product}, GroupBy: models.GroupByShop,
		Metrics: []models.MetricID{models.MetricQuantity, models.MetricSales, models.MetricCost, models.MetricMargin, models.MetricMarginRate, models.MetricAveragePrice}}
}

func TestGameDemoRejectsInvalidQueriesBeforeSQL(t *testing.T) {
	reader := &gameDemoNoRead{}
	source := NewAssistantGameDemoSource(reader)
	cases := []models.FactQuery{
		{Basis: models.BasisOutstock},
		{Basis: models.BasisPayment, Window: models.DateRange{StartDate: "bad", EndDateExclusive: "2026-11-09"}},
	}
	for _, q := range cases {
		if _, err := source.ReadSalesFacts(context.Background(), q); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	if reader.calls != 0 {
		t.Fatalf("invalid query read SQL %d times", reader.calls)
	}
}

// This is deliberately a real PostgreSQL test, never a fixture or SQLite substitute.
// Run deploy/game-demo/demo.ps1 -Action verify after starting the isolated stack.
func TestGameDemoPostgresWorkflow(t *testing.T) {
	dsn := os.Getenv("GAME_DEMO_TEST_DSN")
	if dsn == "" {
		t.Skip("GAME_DEMO_TEST_DSN not set: real PostgreSQL integration not run")
	}
	reader, err := dbmanager.NewAssistantReadonlyReader(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	source := NewAssistantGameDemoSource(reader)
	caps, err := source.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if caps.ContractVersion != "game-shop-demo-v1" || !caps.SupportsMetric(models.MetricMarginRate) || caps.SupportsBasis(models.BasisOutstock) {
		t.Fatalf("capabilities: %+v", caps)
	}

	t.Run("ambiguous title requires confirmation", func(t *testing.T) {
		candidates, err := source.FindProductCandidates(ctx, models.AccessContext{}, "NIGHTREIGN")
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 3 {
			t.Fatalf("expected 3 platform/edition matches, got %+v", candidates)
		}
		req := gameDemoRequest("")
		resolution, err := ResolveDraft(models.AccessContext{}, models.DraftRequest{SchemaVersion: 1, RequestID: "ambiguous", Workflow: req.Workflow,
			Basis: req.Basis, Period: &req.Period, Scope: &req.Scope, ProductQuery: "NIGHTREIGN", Metrics: req.Metrics}, time.Now(), candidates)
		if err != nil || resolution.Status != models.ResolveStatusNeedsInput || resolution.Request != nil {
			t.Fatalf("resolution %+v %v", resolution, err)
		}
	})

	t.Run("parameter binding and foreign scope", func(t *testing.T) {
		candidates, err := source.FindProductCandidates(ctx, models.AccessContext{}, "'; DROP TABLE game_demo.products; --")
		if err != nil || len(candidates) != 0 {
			t.Fatalf("hostile search: %+v %v", candidates, err)
		}
		q := FactQueryFromRequest(gameDemoRequest("RE9-PS5-STD"))
		q.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{"production-shop"}}
		if _, err := source.ReadSalesFacts(ctx, q); err == nil {
			t.Fatal("foreign shop accepted")
		}
		q = FactQueryFromRequest(gameDemoRequest("unknown-product"))
		if _, err := source.ReadSalesFacts(ctx, q); err == nil {
			t.Fatal("unknown product accepted")
		}
		q = FactQueryFromRequest(gameDemoRequest("RE9-PS5-STD"))
		q.Product.SkuIDs = []string{"SKU-NR-XBOX-STD"}
		if _, err := source.ReadSalesFacts(ctx, q); err == nil {
			t.Fatal("mismatched SKU accepted")
		}
	})

	t.Run("real facts produce independently known margins", func(t *testing.T) {
		workflow, err := NewAssistantWorkflow(gameDemoRequest("RE9-PS5-STD"), source, nil)
		if err != nil {
			t.Fatal(err)
		}
		report, err := workflow.Invoke(ctx, models.AnalysisReport{ReportID: "real-game-db"})
		if err != nil {
			t.Fatal(err)
		}
		if report.ContractVersion != "game-shop-demo-v1" || report.Outcome != models.OutcomeComplete {
			t.Fatalf("report: %+v", report)
		}
		if len(report.Table.Rows) != 2 {
			t.Fatalf("rows %+v", report.Table.Rows)
		}
		for _, row := range report.Table.Rows {
			wantCost, wantMargin, wantRate := "3000", "3000", "0.5"
			if row.EntityID == "PIXEL" {
				wantCost, wantMargin, wantRate = "4200", "1800", "0.3"
			}
			assertAssistantWorkflowMetric(t, row.Metrics, models.MetricSales, "6000")
			assertAssistantWorkflowMetric(t, row.Metrics, models.MetricCost, wantCost)
			assertAssistantWorkflowMetric(t, row.Metrics, models.MetricMargin, wantMargin)
			assertAssistantWorkflowMetric(t, row.Metrics, models.MetricMarginRate, wantRate)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "12000")
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricMargin, "4800")
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricMarginRate, "0.4")
	})

	t.Run("missing day is unavailable and covered zero is zero", func(t *testing.T) {
		req := gameDemoRequest("NR-PS5-STD")
		req.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{"PIXEL"}}
		req.Period.StartDate = "2026-11-05"
		req.Period.EndDateExclusive = "2026-11-06"
		req.TrendPeriod = &req.Period.DateRange
		workflow, err := NewAssistantWorkflow(req, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		report, err := workflow.Invoke(ctx, models.AnalysisReport{ReportID: "missing-day"})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Trend) != 1 || report.Trend[0].Value != nil {
			t.Fatalf("missing day turned into value: %+v", report.Trend)
		}
		cov, err := source.ReadCoverage(ctx, repositories.AssistantCoverageQuery{Basis: req.Basis, Window: req.Period.DateRange, Scope: req.Scope, Product: req.Product})
		if err != nil || len(cov) == 0 || cov[0].Status == models.CoverageStatusCovered {
			t.Fatalf("coverage %+v %v", cov, err)
		}
		req = gameDemoRequest("NR-XBOX-STD")
		req.Period.StartDate = "2026-11-08"
		req.Period.EndDateExclusive = "2026-11-09"
		req.TrendPeriod = &req.Period.DateRange
		workflow, err = NewAssistantWorkflow(req, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		report, err = workflow.Invoke(ctx, models.AnalysisReport{ReportID: "covered-zero"})
		if err != nil {
			t.Fatal(err)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "0")
		if len(report.Trend) != 1 || report.Trend[0].Value == nil || !report.Trend[0].Value.IsZero() {
			t.Fatalf("covered zero %+v", report.Trend)
		}
	})

	t.Run("database enforces read only", func(t *testing.T) {
		err := reader.WithReadOnlyTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO game_demo.products SELECT * FROM game_demo.products LIMIT 1")
			return err
		})
		state, ok := dbmanager.AssistantSQLState(err)
		if !ok || state != "25006" {
			t.Fatalf("wanted read_only_sql_transaction, got %v %s", err, state)
		}
	})
}
