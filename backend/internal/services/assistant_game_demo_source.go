package services

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lib/pq"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

const assistantGameDemoContract = "game-shop-demo-v1"

// AssistantGameDemoSource reads synthetic data from PostgreSQL. It never falls
// back to AssistantFixtureSource. The caller owns the dedicated reader.
type AssistantGameDemoSource struct{ reader AssistantERPReadOnlyTx }

func NewAssistantGameDemoSource(reader AssistantERPReadOnlyTx) *AssistantGameDemoSource {
	return &AssistantGameDemoSource{reader: reader}
}

func gameDemoError(field, text string) error {
	return models.NewValidationError(models.ErrorCodeCapabilityUnavailable, field, text)
}

// Each operation verifies the database/role/seed marker, including after reset.
func (s *AssistantGameDemoSource) read(ctx context.Context, fn func(context.Context, *sql.Tx, time.Time) error) error {
	if s.reader == nil || assistantERPNilReader(s.reader) {
		return gameDemoError("source", "game demo reader is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return s.reader.WithReadOnlyTx(ctx, func(tx *sql.Tx) error {
		var database, role, contract string
		var asOf time.Time
		if err := tx.QueryRowContext(ctx, `SELECT current_database(), current_user, contract_version, data_as_of FROM game_demo.metadata WHERE singleton`).Scan(&database, &role, &contract, &asOf); err != nil {
			return gameDemoError("source", "game demo database marker is unavailable")
		}
		if database != "gameshelf_demo" || role != "game_demo_reader" || contract != assistantGameDemoContract {
			return gameDemoError("source", "game demo database identity does not match its contract")
		}
		return fn(ctx, tx, asOf)
	})
}

func (s *AssistantGameDemoSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	err := s.read(ctx, func(context.Context, *sql.Tx, time.Time) error { return nil })
	if err != nil {
		return models.SourceCapabilities{}, err
	}
	return models.SourceCapabilities{ContractVersion: assistantGameDemoContract, SupportedBases: []models.Basis{models.BasisPayment},
		SupportedMetrics: []models.MetricID{models.MetricQuantity, models.MetricSales, models.MetricCost, models.MetricMargin, models.MetricMarginRate, models.MetricAveragePrice},
		IdentityDomain:   "synthetic-physical-game-sku", Currencies: []string{"HKD"}, CoverageEvidence: true}, nil
}

func gameDemoShops(scope models.Scope) ([]string, error) {
	shops := []string{"CAMPUS", "PIXEL"}
	if scope.Mode == models.ScopeModeAll {
		if len(scope.ShopIDs)+len(scope.PlatformIDs) > 0 {
			return nil, gameDemoError("scope", "all scope cannot include explicit IDs")
		}
		return shops, nil
	}
	if scope.Mode != models.ScopeModeSelected || len(scope.ShopIDs)+len(scope.PlatformIDs) == 0 {
		return nil, gameDemoError("scope", "select a demo shop or DEMO-WEB platform")
	}
	for _, id := range scope.PlatformIDs {
		if id != "DEMO-WEB" {
			return nil, gameDemoError("scope", "unknown demo platform")
		}
	}
	if len(scope.ShopIDs) > 0 {
		for _, id := range scope.ShopIDs {
			if !slices.Contains(shops, id) {
				return nil, gameDemoError("scope", "unknown demo shop")
			}
		}
		shops = slices.Clone(scope.ShopIDs)
		slices.Sort(shops)
		shops = slices.Compact(shops)
	}
	return shops, nil
}

func gameDemoValidate(basis models.Basis, window models.DateRange, scope models.Scope) ([]string, error) {
	if basis != models.BasisPayment {
		return nil, gameDemoError("basis", "game demo supports payment basis only")
	}
	if err := assistantERPValidateWindow(window, "period"); err != nil {
		return nil, err
	}
	start, _ := time.Parse(time.DateOnly, window.StartDate)
	end, _ := time.Parse(time.DateOnly, window.EndDateExclusive)
	if end.Sub(start) > 366*24*time.Hour {
		return nil, gameDemoError("period", "game demo windows are limited to 366 days")
	}
	return gameDemoShops(scope)
}

func gameDemoProduct(ctx context.Context, tx *sql.Tx, product models.ProductSelection) error {
	if product.ProductID == "" {
		if len(product.SkuIDs) > 0 {
			return gameDemoError("product", "select a product before its SKU")
		}
		return nil
	}
	var sku string
	if err := tx.QueryRowContext(ctx, `SELECT sku_id FROM game_demo.products WHERE product_id=$1`, product.ProductID).Scan(&sku); err != nil {
		return gameDemoError("product", "unknown demo product")
	}
	for _, id := range product.SkuIDs {
		if id != sku {
			return gameDemoError("product.sku_ids", "SKU does not belong to the selected demo product")
		}
	}
	return nil
}

func (s *AssistantGameDemoSource) FindProductCandidates(ctx context.Context, access models.AccessContext, query string) ([]models.Candidate, error) {
	scope := models.Scope{Mode: models.ScopeModeAll}
	if !access.ApprovedScope.Empty() && !access.ApprovedScope.All {
		scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: access.ApprovedScope.ShopIDs, PlatformIDs: access.ApprovedScope.PlatformIDs}
	}
	if _, err := gameDemoShops(scope); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if len(query) > 512 {
		return nil, gameDemoError("product_query", "game demo search is too long")
	}
	var out []models.Candidate
	err := s.read(ctx, func(ctx context.Context, tx *sql.Tx, _ time.Time) error {
		rows, err := tx.QueryContext(ctx, `SELECT product_id, title || ' | ' || gaming_platform || ' | ' || edition || ' | ' || sku_id FROM game_demo.products WHERE product_id=$1 OR strpos(lower(title),lower($1))>0 OR strpos(lower(sku_id),lower($1))>0 ORDER BY product_id LIMIT 21`, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c := models.Candidate{EntityType: models.EntityTypeProduct, PlatformID: "DEMO-WEB"}
			if err := rows.Scan(&c.EntityID, &c.Label); err != nil {
				return err
			}
			out = append(out, c)
		}
		if len(out) > 20 {
			return gameDemoError("product_query", "narrow the game search")
		}
		return rows.Err()
	})
	return out, err
}

func (s *AssistantGameDemoSource) ResolveEntities(ctx context.Context, q repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	if _, err := gameDemoShops(q.Scope); err != nil {
		return nil, err
	}
	var out []models.Candidate
	err := s.read(ctx, func(ctx context.Context, tx *sql.Tx, _ time.Time) error {
		if err := gameDemoProduct(ctx, tx, q.Product); err != nil {
			return err
		}
		if q.Product.ProductID != "" {
			out = []models.Candidate{{EntityType: models.EntityTypeProduct, EntityID: q.Product.ProductID, Label: q.Product.ProductID, PlatformID: "DEMO-WEB"}}
		}
		return nil
	})
	return out, err
}

func (s *AssistantGameDemoSource) ReadCoverage(ctx context.Context, q repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	shops, err := gameDemoValidate(q.Basis, q.Window, q.Scope)
	if err != nil {
		return nil, err
	}
	var out []repositories.AssistantCoverage
	err = s.read(ctx, func(ctx context.Context, tx *sql.Tx, _ time.Time) error {
		if err := gameDemoProduct(ctx, tx, q.Product); err != nil {
			return err
		}
		// Expected product/day cells come from the catalogue and requested calendar,
		// never from fact rows. An absent cell is missing, including out-of-seed days.
		rows, err := tx.QueryContext(ctx, `SELECT s.shop_id,
   CASE WHEN bool_and(COALESCE(c.status='covered',false)) THEN 'covered' ELSE 'missing' END
   FROM game_demo.shops s CROSS JOIN game_demo.products p
   CROSS JOIN generate_series($1::date,$2::date-1,interval '1 day') d(day)
   LEFT JOIN game_demo.coverage c ON c.shop_id=s.shop_id AND c.product_id=p.product_id AND c.business_date=d.day::date
   WHERE s.shop_id=ANY($3::text[]) AND ($4='' OR p.product_id=$4)
   GROUP BY s.shop_id ORDER BY s.shop_id`, q.Window.StartDate, q.Window.EndDateExclusive, pq.Array(shops), q.Product.ProductID)
		if err != nil {
			return err
		}
		defer rows.Close()
		platformStatus := models.CoverageStatusCovered
		for rows.Next() {
			c := repositories.AssistantCoverage{EntityType: models.EntityTypeShop}
			if err := rows.Scan(&c.EntityID, &c.Status); err != nil {
				return err
			}
			if c.Status != models.CoverageStatusCovered {
				platformStatus = models.CoverageStatusMissing
			}
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) != len(shops) {
			return gameDemoError("coverage", "demo shops are missing")
		}
		out = append(out, repositories.AssistantCoverage{EntityType: models.EntityTypePlatform, EntityID: "DEMO-WEB", Status: platformStatus})
		return nil
	})
	return out, err
}

func (s *AssistantGameDemoSource) ReadSalesFacts(ctx context.Context, q models.FactQuery) (models.FactSnapshot, error) {
	shops, err := gameDemoValidate(q.Basis, q.Window, q.Scope)
	if err != nil {
		return models.FactSnapshot{}, err
	}
	if q.TrendWindow != nil {
		if _, err := gameDemoValidate(q.Basis, *q.TrendWindow, q.Scope); err != nil {
			return models.FactSnapshot{}, err
		}
	}
	for _, metric := range q.Metrics {
		if metric == models.MetricRefund || !metric.Valid() {
			return models.FactSnapshot{}, gameDemoError("metrics", "unsupported demo metric")
		}
	}
	snap := models.FactSnapshot{Basis: q.Basis, Window: q.Window, ContractVersion: assistantGameDemoContract, Rows: []models.FactRow{}}
	err = s.read(ctx, func(ctx context.Context, tx *sql.Tx, asOf time.Time) error {
		if err := gameDemoProduct(ctx, tx, q.Product); err != nil {
			return err
		}
		snap.ReadAt = time.Now().UTC()
		snap.DataAsOf = asOf
		trendStart, trendEnd := assistantERPTrendArgs(q.TrendWindow)
		rows, err := tx.QueryContext(ctx, `SELECT 'DEMO-WEB/' || shop_id, business_date::text, quantity, sales, cost
   FROM reporting.v_game_sales_daily WHERE shop_id=ANY($1::text[]) AND ($2='' OR product_id=$2)
   AND ((business_date >= $3::date AND business_date < $4::date) OR (business_date >= $5::date AND business_date < $6::date))
   ORDER BY business_date,shop_id,product_id LIMIT 10001`, pq.Array(shops), q.Product.ProductID, q.Window.StartDate, q.Window.EndDateExclusive, trendStart, trendEnd)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row := models.FactRow{Basis: q.Basis, Currency: "HKD", CostVerified: true, CoverageStatus: models.CoverageStatusCovered, BundleRole: models.BundleRoleNone}
			if err := rows.Scan(&row.EntityID, &row.BusinessDate, &row.Quantity, &row.Sales, &row.Cost); err != nil {
				return err
			}
			snap.Rows = append(snap.Rows, row)
			if len(snap.Rows) > 10000 {
				return fmt.Errorf("game demo fact limit exceeded")
			}
		}
		return rows.Err()
	})
	return snap, err
}

func (*AssistantGameDemoSource) ReadRefundFacts(context.Context, models.FactQuery) (models.FactSnapshot, error) {
	return models.FactSnapshot{}, gameDemoError("metrics.refund", "refunds are outside this synthetic demo contract")
}

var _ repositories.AssistantSourceRepository = (*AssistantGameDemoSource)(nil)
var _ AssistantProductCatalog = (*AssistantGameDemoSource)(nil)
