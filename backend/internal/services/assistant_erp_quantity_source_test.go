package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/dbmanager"
)

type assistantERPFixtureReader func(context.Context, func(*sql.Tx) error) error

func (r assistantERPFixtureReader) WithReadOnlyTx(ctx context.Context, fn func(*sql.Tx) error) error {
	return r(ctx, fn)
}

func assistantERPFixtureScope() map[string]string {
	return map[string]string{
		"s1": "orders-a", "s2": "orders-a", "s3": "orders-a",
		"s4": "orders-a", "s5": "orders-a", "s6": "orders-a", "s7": "orders-b",
	}
}

func TestAssistantERPQuantitySourceContract(t *testing.T) {
	reader := assistantERPFixtureReader(func(context.Context, func(*sql.Tx) error) error {
		t.Fatal("contract validation must not open a transaction")
		return nil
	})
	scope := assistantERPFixtureScope()
	for name, invalid := range map[string]map[string]string{
		"absent":       nil,
		"six":          {"s1": "x", "s2": "x", "s3": "x", "s4": "x", "s5": "x", "s6": "x"},
		"eight":        {"s1": "x", "s2": "x", "s3": "x", "s4": "x", "s5": "x", "s6": "x", "s7": "x", "s8": "x"},
		"bad shop":     {"s/1": "x", "s2": "x", "s3": "x", "s4": "x", "s5": "x", "s6": "x", "s7": "x"},
		"blank source": {"s1": " ", "s2": "x", "s3": "x", "s4": "x", "s5": "x", "s6": "x", "s7": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewAssistantERPQuantitySource(reader, invalid); err == nil {
				t.Fatal("accepted invalid pilot scope")
			}
		})
	}
	if _, err := NewAssistantERPQuantitySource(nil, scope); err == nil {
		t.Fatal("accepted nil reader")
	}
	var nilReader *assistantERPFixtureReader
	if _, err := NewAssistantERPQuantitySource(nilReader, scope); err == nil {
		t.Fatal("accepted typed nil reader")
	}
	s, err := NewAssistantERPQuantitySource(reader, scope)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	caps, err := s.Capabilities(ctx)
	if err != nil || caps.ContractVersion == "" || !caps.CoverageEvidence ||
		len(caps.SupportedBases) != 1 || !caps.SupportsBasis(models.BasisPayment) ||
		len(caps.SupportedMetrics) != 1 || !caps.SupportsMetric(models.MetricQuantity) {
		t.Fatalf("unexpected capabilities: %+v, %v", caps, err)
	}
	_, err = s.ReadRefundFacts(ctx, models.FactQuery{})
	assertAssistantERPErrorCode(t, err, models.ErrorCodeCapabilityUnavailable)
	_, err = s.ReadSalesFacts(ctx, models.FactQuery{Basis: models.BasisOutstock})
	assertAssistantERPErrorCode(t, err, models.ErrorCodeCapabilityUnavailable)
	_, err = s.ReadSalesFacts(ctx, models.FactQuery{Basis: models.BasisPayment, Metrics: []models.MetricID{models.MetricRefund}})
	assertAssistantERPErrorCode(t, err, models.ErrorCodeCapabilityUnavailable)
	_, err = s.ReadSalesFacts(ctx, models.FactQuery{Basis: models.BasisPayment, Window: models.DateRange{StartDate: "2026-02-30", EndDateExclusive: "2026-03-01"}})
	assertAssistantERPErrorCode(t, err, models.ErrorCodeInvalidDateRange)
}

func assertAssistantERPErrorCode(t *testing.T, err error, code models.ValidationErrorCode) {
	t.Helper()
	v, ok := models.AsValidationError(err)
	if !ok || v.Code != code {
		t.Fatalf("got error %v; want %s", err, code)
	}
}

// Run only against a fresh, disposable local PostgreSQL container. No external
// DSN is accepted. The fixture schema is created once and is never dropped.
func TestAssistantERPQuantitySourcePostgresFixture(t *testing.T) {
	if os.Getenv("ASSISTANT_ERP_QUANTITY_FIXTURE") != "1" {
		t.Skip("requires the isolated assistant ERP PostgreSQL fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const dsn = "host=127.0.0.1 port=55431 dbname=assistant_erp_quantity_fixture user=assistant_erp_fixture sslmode=disable connect_timeout=3"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, assistantERPQuantityFixtureSQL); err != nil {
		t.Fatalf("initialize local fixture: %v", err)
	}
	// Apply the exact owner-run deployment SQL to this disposable database.
	// The legacy product view is deliberately absent.
	ddl, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "erp-sync", "assistant-ordinary-sales.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatalf("install ordinary-sales view in local fixture: %v", err)
	}
	reader, err := dbmanager.NewAssistantReadonlyReader(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	scope := assistantERPFixtureScope()
	s, err := NewAssistantERPQuantitySource(reader, scope)
	if err != nil {
		t.Fatal(err)
	}
	delete(scope, "s1")
	scope["s7"] = "orders-a"
	scope["outside"] = "orders-a"

	all := models.Scope{Mode: models.ScopeModeAll}
	s1 := models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{"s1"}}
	main := models.DateRange{StartDate: "2026-09-04", EndDateExclusive: "2026-09-06"}
	trend := models.DateRange{StartDate: "2026-09-08", EndDateExclusive: "2026-09-09"}
	product := models.ProductSelection{ProductID: "p1"}
	coverage := repositories.AssistantCoverageQuery{Basis: models.BasisPayment, Window: main, Scope: all, Product: product}
	facts := models.FactQuery{Basis: models.BasisPayment, Window: main, TrendWindow: &trend, Scope: all, Product: product, Metrics: []models.MetricID{models.MetricQuantity}}

	t.Run("catalog keeps multiple matches and restricts scope", func(t *testing.T) {
		for _, query := range []string{"钢钉枪各店销量", "钢钉枪"} {
			got, err := s.FindProductCandidates(ctx, models.AccessContext{}, query)
			if err != nil || len(got) != 2 || got[0].EntityID != "p1" || got[1].EntityID != "p2" {
				t.Fatalf("search %q: %+v, %v", query, got, err)
			}
			if got[0].Label != "钢钉枪甲 (p1)" || got[1].Label != "钢钉枪甲 (p2)" {
				t.Fatalf("same-name products need distinguishable labels: %+v", got)
			}
		}
		access := models.AccessContext{ApprovedScope: models.ApprovedScope{ShopIDs: []string{"s1"}}}
		got, err := s.FindProductCandidates(ctx, access, "钢钉枪")
		if err != nil || len(got) != 1 || got[0].EntityID != "p1" {
			t.Fatalf("chat scope: %+v, %v", got, err)
		}
		got, err = s.FindProductCandidates(ctx, models.AccessContext{}, "%_")
		if err != nil || len(got) != 1 || got[0].EntityID != "literal" {
			t.Fatalf("literal wildcard: %+v, %v", got, err)
		}
		got, err = s.FindProductCandidates(ctx, models.AccessContext{}, "批量测试")
		if err != nil || len(got) != 20 {
			t.Fatalf("bounded ambiguous search: %d results, %v", len(got), err)
		}
		got, err = s.ResolveEntities(ctx, repositories.AssistantEntityQuery{
			Scope: all, Product: models.ProductSelection{ProductID: "fallback-only"},
		})
		if err != nil || len(got) != 0 {
			t.Fatalf("fallback source type resolved as a product: %+v, %v", got, err)
		}
		got, err = s.ResolveEntities(ctx, repositories.AssistantEntityQuery{Scope: all, Product: product})
		if err != nil || len(got) != 1 || got[0].EntityID != "p1" || got[0].EntityType != models.EntityTypeProduct {
			t.Fatalf("exact resolve: %+v, %v", got, err)
		}
	})

	t.Run("coverage uses exact source, order entity, Shanghai range and quality", func(t *testing.T) {
		got, err := s.ReadCoverage(ctx, coverage)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]models.CoverageStatus{
			"shop/s1": models.CoverageStatusCovered, "shop/s2": models.CoverageStatusUnknown,
			"shop/s3": models.CoverageStatusUnknown, "shop/s4": models.CoverageStatusMissing,
			"shop/s5": models.CoverageStatusUnknown, "shop/s6": models.CoverageStatusMissing,
			"shop/s7": models.CoverageStatusCovered, "platform/a": models.CoverageStatusUnknown,
			"platform/b": models.CoverageStatusUnknown, "platform/c": models.CoverageStatusMissing,
		}
		if len(got) != len(want) {
			t.Fatalf("coverage rows: %+v", got)
		}
		for _, row := range got {
			if key := string(row.EntityType) + "/" + row.EntityID; want[key] != row.Status {
				t.Errorf("coverage %s = %s, want %s", key, row.Status, want[key])
			}
		}
		q := coverage
		q.Scope = s1
		got, err = s.ReadCoverage(ctx, q)
		if err != nil || len(got) != 2 || got[0].Status != models.CoverageStatusCovered || got[1].Status != models.CoverageStatusCovered {
			t.Fatalf("selected shop coverage: %+v, %v", got, err)
		}
	})

	t.Run("facts aggregate only ordinary quantity over the disjoint union", func(t *testing.T) {
		got, err := s.ReadSalesFacts(ctx, facts)
		if err != nil {
			t.Fatal(err)
		}
		if got.ReadAt.IsZero() || got.ContractVersion == "" || got.DataAsOf.Format("2006-01-02") != "2026-09-09" {
			t.Fatalf("unexpected snapshot metadata: %+v", got)
		}
		want := map[string]string{"a/s1/2026-09-04": "9007199254740993.25", "a/s1/2026-09-08": "3", "b/s5/2026-09-05": "2"}
		if len(got.Rows) != len(want) {
			t.Fatalf("fact rows: %+v", got.Rows)
		}
		for _, row := range got.Rows {
			if key := row.EntityID + "/" + row.BusinessDate; row.Quantity.String() != want[key] ||
				row.Currency != "CNY" || row.Basis != models.BasisPayment ||
				(row.EntityID == "b/s5" && row.CoverageStatus != models.CoverageStatusUnknown) ||
				(row.EntityID != "b/s5" && row.CoverageStatus != models.CoverageStatusCovered) ||
				row.BundleRole != models.BundleRoleNone || row.CostVerified || row.GiftCostVerified || !row.Sales.IsZero() || !row.Cost.IsZero() {
				t.Errorf("unexpected fact %s: %+v", key, row)
			}
		}
		q := facts
		q.Scope = s1
		q.TrendWindow = &main
		got, err = s.ReadSalesFacts(ctx, q)
		if err != nil || len(got.Rows) != 1 || got.Rows[0].Quantity.String() != "9007199254740993.25" {
			t.Fatalf("overlap double count or widened scope: %+v, %v", got.Rows, err)
		}
	})

	t.Run("rejects unknown, conflicting and injected identities", func(t *testing.T) {
		for _, scope := range []models.Scope{
			{Mode: models.ScopeModeSelected, ShopIDs: []string{"s1", "outside"}},
			{Mode: models.ScopeModeSelected, PlatformIDs: []string{"a", "outside"}},
			{Mode: models.ScopeModeSelected, ShopIDs: []string{"s1') OR TRUE --"}},
		} {
			q := facts
			q.Scope = scope
			_, err := s.ReadSalesFacts(ctx, q)
			assertAssistantERPErrorCode(t, err, models.ErrorCodeUnauthorizedScope)
		}
		q := facts
		q.Scope = models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{"b"}, ShopIDs: []string{"s1"}}
		_, err := s.ReadSalesFacts(ctx, q)
		assertAssistantERPErrorCode(t, err, models.ErrorCodeConflictingScope)
		q.Scope = all
		q.Product.ProductID = "p1' OR '1'='1"
		got, err := s.ReadSalesFacts(ctx, q)
		if err == nil || len(got.Rows) != 0 {
			t.Fatalf("unknown product became a usable snapshot: %+v, %v", got, err)
		}
		cq := coverage
		cq.Product = q.Product
		evidence, err := s.ReadCoverage(ctx, cq)
		if err != nil || len(evidence) == 0 {
			t.Fatalf("unknown product evidence: %+v, %v", evidence, err)
		}
		for _, e := range evidence {
			if e.Status == models.CoverageStatusCovered {
				t.Fatalf("unknown product certified covered: %+v", e)
			}
		}
		q.Product = models.ProductSelection{ProductID: "p1", SkuIDs: []string{"display label"}}
		_, err = s.ReadSalesFacts(ctx, q)
		assertAssistantERPErrorCode(t, err, models.ErrorCodeCapabilityUnavailable)
	})

	t.Run("fact limit fails the whole snapshot", func(t *testing.T) {
		q := facts
		q.Scope = s1
		q.Window = models.DateRange{StartDate: "2030-01-01", EndDateExclusive: "2050-01-01"}
		q.TrendWindow = nil
		q.Product.ProductID = "overflow"
		got, err := s.ReadSalesFacts(ctx, q)
		assertAssistantERPErrorCode(t, err, models.ErrorCodeSourceUnavailable)
		if len(got.Rows) != 0 || !strings.Contains(err.Error(), "row limit") {
			t.Fatalf("oversized snapshot was truncated or failed for the wrong reason: %d rows, %v", len(got.Rows), err)
		}
	})

	t.Run("empty window with a missing watermark is not fully covered", func(t *testing.T) {
		q := facts
		q.Scope = models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{"b"}}
		q.Window = models.DateRange{StartDate: "2026-09-04", EndDateExclusive: "2026-09-05"}
		q.TrendWindow = nil
		got, err := s.ReadSalesFacts(ctx, q)
		if err != nil || len(got.Rows) != 0 || got.DataAsOf.Format(time.DateOnly) != "2026-09-09" {
			t.Fatalf("expected empty facts with valid sibling watermark: %+v, %v", got, err)
		}
		cq := coverage
		cq.Scope, cq.Window = q.Scope, q.Window
		evidence, err := s.ReadCoverage(ctx, cq)
		if err != nil || len(evidence) != 3 ||
			evidence[0].EntityID != "s5" || evidence[0].Status != models.CoverageStatusUnknown ||
			evidence[1].EntityID != "s7" || evidence[1].Status != models.CoverageStatusCovered ||
			evidence[2].EntityID != "b" || evidence[2].Status != models.CoverageStatusUnknown {
			t.Fatalf("missing watermark certified empty window: %+v, %v", evidence, err)
		}
	})

	t.Run("known product absent inside covered window is true zero", func(t *testing.T) {
		q := facts
		q.Scope = s1
		q.Window = models.DateRange{StartDate: "2026-09-05", EndDateExclusive: "2026-09-06"}
		q.TrendWindow = nil
		got, err := s.ReadSalesFacts(ctx, q)
		if err != nil || len(got.Rows) != 0 {
			t.Fatalf("covered empty window: %+v, %v", got, err)
		}
	})

	t.Run("missing currency refuses facts", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, "UPDATE reporting.v_shops SET currency = NULL WHERE shop_id = 's1'"); err != nil {
			t.Fatal(err)
		}
		q := facts
		q.Scope = s1
		got, err := s.ReadSalesFacts(ctx, q)
		assertAssistantERPErrorCode(t, err, models.ErrorCodeSourceUnavailable)
		if len(got.Rows) != 0 || !strings.Contains(err.Error(), "currency") {
			t.Fatalf("missing currency returned facts or failed for the wrong reason: %+v, %v", got.Rows, err)
		}
	})
}

func TestAssistantERPQuantitySourceSanitizesReaderErrors(t *testing.T) {
	reader := assistantERPFixtureReader(func(context.Context, func(*sql.Tx) error) error {
		return errors.New("fixture driver detail")
	})
	s, err := NewAssistantERPQuantitySource(reader, assistantERPFixtureScope())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.FindProductCandidates(context.Background(), models.AccessContext{}, "item")
	assertAssistantERPErrorCode(t, err, models.ErrorCodeSourceUnavailable)
	if strings.Contains(err.Error(), "fixture driver detail") {
		t.Fatal("driver details escaped the adapter")
	}
}

func TestAssistantERPQuantitySourceKeepsDeadlineAndGateCause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	calls := 0
	reader := assistantERPFixtureReader(func(ctx context.Context, _ func(*sql.Tx) error) error {
		calls++
		got, ok := ctx.Deadline()
		if !ok || !got.Equal(want) {
			t.Fatalf("reader lost caller deadline: %v, want %v", got, want)
		}
		return fmt.Errorf("fixture driver detail: %w", ErrSourceSyncing)
	})
	s, err := NewAssistantERPQuantitySource(reader, assistantERPFixtureScope())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.FindProductCandidates(ctx, models.AccessContext{}, "item")
	if err != ErrSourceSyncing {
		t.Fatalf("expected sanitized gate cause, got %v", err)
	}
	cancel()
	_, err = s.FindProductCandidates(ctx, models.AccessContext{}, "item")
	if err != context.Canceled || calls != 1 {
		t.Fatalf("canceled read reached reader or lost cancellation: calls=%d, err=%v", calls, err)
	}
}

const assistantERPQuantityFixtureSQL = `
CREATE SCHEMA reporting;
CREATE TABLE reporting.v_shops (shop_id text PRIMARY KEY, platform text, currency text, enabled boolean);
INSERT INTO reporting.v_shops VALUES
 ('s1','a','CNY',true), ('s2','a','CNY',true), ('s3','a','CNY',true), ('s4','a','CNY',true),
 ('s5','b','CNY',true), ('s6','c','CNY',false), ('s7','b','CNY',true), ('outside','a','CNY',true);
CREATE TABLE reporting.v_coverage (source text, entity text, shop_id text, covered tstzmultirange, data_as_of timestamptz, quality_status text);
INSERT INTO reporting.v_coverage VALUES
 ('orders-a','orders','s1','{["2026-09-03 16:00Z","2026-09-05 16:00Z"),["2026-09-07 16:00Z","2026-09-08 16:00Z")}','2026-09-10 00:00Z','passed'),
 ('orders-a','orders','s2','{["2026-09-03 16:00Z","2026-09-04 00:00Z"),["2026-09-04 00:01Z","2026-09-05 16:00Z")}','2026-09-10 00:00Z','passed'),
 ('orders-a','orders','s3','{["2026-09-03 16:00Z","2026-09-06 16:00Z")}','2026-09-10 00:00Z','unknown'),
 ('orders-a','refunds','s4','{["2026-09-03 16:00Z","2026-09-06 16:00Z")}','2026-09-10 00:00Z','passed'),
 ('wrong','orders','s4','{["2026-09-03 16:00Z","2026-09-06 16:00Z")}','2026-09-01 00:00Z','passed'),
 ('orders-a','orders','s5','{["2026-09-03 16:00Z","2026-09-08 16:00Z")}',NULL,'passed'),
 ('orders-a','orders','s6','{["2026-09-03 16:00Z","2026-09-08 16:00Z")}','2026-09-01 00:00Z','passed'),
 ('orders-b','orders','s7','{["2026-09-03 16:00Z","2026-09-08 16:00Z")}','2026-09-09 00:00Z','passed');
CREATE SCHEMA bi;
CREATE TABLE bi.products (product_id text PRIMARY KEY, title text);
INSERT INTO bi.products VALUES ('p1','钢钉枪甲');
CREATE TABLE bi.order_items (
 shop_id text, paid_at timestamptz, product_id text, quantity numeric, gift_quantity numeric,
 source_type text, line_kind text, active boolean, allocated_paid_amount numeric,
 product_name_snapshot text, sku_label_snapshot text
);
INSERT INTO bi.order_items VALUES
 ('s1','2026-09-03 16:00Z','p1',9007199254740993,99,'0','sale',true,1,NULL,NULL),
 ('s1','2026-09-03 16:01Z','p1',0.25,0,'0','sale',true,0,NULL,NULL),
 -- Unknown and absent source types can both fall back to 'sale'.
 ('s1','2026-09-04 00:00Z','p1',100,0,'99','sale',true,1,NULL,NULL),
 ('s1','2026-09-04 00:00Z','p1',100,0,NULL,'sale',true,1,NULL,NULL),
 ('s1','2026-09-04 00:00Z','fallback-only',100,0,'99','sale',true,1,'钢钉枪回退',NULL),
 ('s1','2026-09-04 00:00Z','p1',100,0,'2','suite',true,1,NULL,NULL),
 ('s1','2026-09-04 00:00Z','p1',100,0,'3','combination',true,1,NULL,NULL),
 ('s1','2026-09-04 00:00Z','p1',100,100,'0','gift',true,1,NULL,NULL),
 ('s1','2026-09-04 00:00Z','p1',100,0,'0','sale',false,1,NULL,NULL),
 ('s1','2026-09-04 00:00Z','p1',100,0,'0','sale',true,NULL,NULL,NULL),
 ('s1',NULL,'p1',100,0,'0','sale',true,1,NULL,NULL),
 ('s1','2026-09-04 00:00Z','',100,0,'0','sale',true,1,'钢钉枪空编号',NULL),
 ('s1','2026-09-03 15:59Z','p1',100,0,'0','sale',true,1,NULL,NULL),
 ('s1','2026-09-05 16:00Z','p1',100,0,'0','sale',true,1,NULL,NULL),
 ('s1','2026-09-07 16:00Z','p1',3,0,'0','sale',true,1,NULL,NULL),
 ('s1','2026-09-08 16:00Z','p1',100,0,'0','sale',true,1,NULL,NULL),
 ('s5','2026-09-05 00:00Z','p1',2,0,'0','sale',true,1,NULL,NULL),
 ('s5','2026-09-05 00:00Z','p2',1,0,'0','sale',true,1,'钢钉枪甲',NULL),
 ('s6','2026-09-05 00:00Z','p1',100,0,'0','sale',true,1,NULL,NULL),
 ('s6','2026-09-05 00:00Z','disabled',100,0,'0','sale',true,1,'钢钉枪下线',NULL),
 ('s1','2026-09-05 00:00Z','literal',1,0,'0','sale',true,1,'literal%_',NULL),
 ('s1','2026-09-05 00:00Z','suite-only',1,0,'2','suite',true,1,'钢钉枪套装',NULL),
 ('outside','2026-09-05 00:00Z','p1',100,0,'0','sale',true,1,NULL,NULL),
 ('outside','2026-09-05 00:00Z','leak',100,0,'0','sale',true,1,'钢钉枪店外',NULL);
INSERT INTO bi.order_items
 SELECT 's1',timestamptz '2026-09-05 00:00Z','bulk-' || n,1,0,'0','sale',true,1,NULL,'批量测试'
 FROM generate_series(1,25) AS n;
INSERT INTO bi.order_items
 SELECT 's1',timestamptz '2030-01-01 00:00Z' + n * interval '1 day','overflow',1,0,'0','sale',true,1,'overflow',NULL
 FROM generate_series(1,4097) AS n;
`
