package services

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

const (
	assistantERPQuantityContract = "erp-payment-sale-quantity-pilot-v1"
	assistantERPPilotShopCount   = 7
	assistantERPCandidateLimit   = 20 // Keep multiple choices even when the result is truncated.
	assistantERPFactLimit        = 4096
	assistantERPReadTimeout      = 8 * time.Second
	assistantERPValueLimit       = 512
)

// AssistantERPReadOnlyTx is the transaction boundary provided by
// dbmanager.AssistantReadonlyReader.
type AssistantERPReadOnlyTx interface {
	WithReadOnlyTx(context.Context, func(*sql.Tx) error) error
}

// AssistantERPQuantitySource reads only ordinary payment-basis sale quantity
// for the explicitly approved shops. The caller owns the reader's lifecycle.
type AssistantERPQuantitySource struct {
	reader            AssistantERPReadOnlyTx
	orderSourceByShop map[string]string
	shopIDs           []string
}

var (
	_ AssistantProductCatalog                = (*AssistantERPQuantitySource)(nil)
	_ repositories.AssistantSourceRepository = (*AssistantERPQuantitySource)(nil)
)

// NewAssistantERPQuantitySource validates and copies the seven-shop order
// source mapping without opening a transaction.
func NewAssistantERPQuantitySource(reader AssistantERPReadOnlyTx, orderSourceByShop map[string]string) (*AssistantERPQuantitySource, error) {
	if reader == nil || assistantERPNilReader(reader) {
		return nil, models.NewValidationError(models.ErrorCodeSourceUnavailable, "source", "ERP read-only reader is required")
	}
	if len(orderSourceByShop) != assistantERPPilotShopCount {
		return nil, models.NewValidationError(models.ErrorCodeUnauthorizedScope, "scope", "ERP pilot requires exactly seven approved shops")
	}
	s := &AssistantERPQuantitySource{
		reader:            reader,
		orderSourceByShop: make(map[string]string, assistantERPPilotShopCount),
		shopIDs:           make([]string, 0, assistantERPPilotShopCount),
	}
	for shop, source := range orderSourceByShop {
		if !assistantERPValidEntityID(shop) || !assistantERPValidValue(source) {
			return nil, models.NewValidationError(models.ErrorCodeUnauthorizedScope, "scope", "ERP pilot shop and order source identifiers must be valid")
		}
		s.orderSourceByShop[shop] = source
		s.shopIDs = append(s.shopIDs, shop)
	}
	slices.Sort(s.shopIDs)
	return s, nil
}

func assistantERPNilReader(reader AssistantERPReadOnlyTx) bool {
	v := reflect.ValueOf(reader)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (*AssistantERPQuantitySource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return models.SourceCapabilities{}, err
	}
	return models.SourceCapabilities{
		ContractVersion:  assistantERPQuantityContract,
		SupportedBases:   []models.Basis{models.BasisPayment},
		SupportedMetrics: []models.MetricID{models.MetricQuantity},
		IdentityDomain:   "erp-product",
		CoverageEvidence: true,
	}, nil
}

func (*AssistantERPQuantitySource) ReadRefundFacts(context.Context, models.FactQuery) (models.FactSnapshot, error) {
	return models.FactSnapshot{}, models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "metrics", "refunds are outside the ERP quantity pilot")
}

func (s *AssistantERPQuantitySource) FindProductCandidates(ctx context.Context, access models.AccessContext, productQuery string) ([]models.Candidate, error) {
	query := strings.TrimSpace(productQuery)
	if query == "" {
		return nil, nil
	}
	if !assistantERPValidValue(query) {
		return nil, models.NewValidationError(models.ErrorCodeInvalidSchema, "product_query", "invalid ERP product query")
	}
	term := strings.TrimSpace(strings.TrimSuffix(query, "各店销量"))
	scope := models.Scope{Mode: models.ScopeModeAll}
	if approved := access.ApprovedScope; !approved.Empty() && !approved.All {
		scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: approved.ShopIDs, PlatformIDs: approved.PlatformIDs}
	}
	return s.findProducts(ctx, scope, query, term, assistantERPCandidateLimit)
}

func (s *AssistantERPQuantitySource) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	if err := assistantERPValidateProduct(query.Product); err != nil {
		return nil, err
	}
	if query.Product.ProductID == "" {
		return nil, nil
	}
	return s.findProducts(ctx, query.Scope, query.Product.ProductID, "", 1)
}

const assistantERPProductSQL = `
SELECT product_id,
       COALESCE(min(NULLIF(btrim(product_name), '')),
                min(NULLIF(btrim(product_name_snapshot), '')),
                min(NULLIF(btrim(sku_label), '')), product_id)
FROM reporting.v_assistant_ordinary_sales
WHERE shop_id = ANY($1::text[])
  AND product_id <> '' AND product_id = btrim(product_id)
  AND octet_length(product_id) <= $4
  AND (product_id = $2 OR ($3 <> '' AND (
       strpos(lower(product_name), lower($3)) > 0 OR
       strpos(lower(product_name_snapshot), lower($3)) > 0 OR
       strpos(lower(sku_label), lower($3)) > 0)))
GROUP BY product_id
ORDER BY (product_id = $2) DESC, product_id
LIMIT $5`

func (s *AssistantERPQuantitySource) findProducts(ctx context.Context, scope models.Scope, exact, term string, limit int) ([]models.Candidate, error) {
	var out []models.Candidate
	err := s.read(ctx, func(ctx context.Context, tx *sql.Tx) error {
		shops, err := s.selectShops(ctx, tx, scope)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, assistantERPProductSQL, pq.Array(assistantERPEnabledIDs(shops)), exact, term, assistantERPValueLimit, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			candidate := models.Candidate{EntityType: models.EntityTypeProduct}
			if err := rows.Scan(&candidate.EntityID, &candidate.Label); err != nil {
				return err
			}
			// Fail the search on malformed identities so a hidden match cannot
			// leave a misleading singleton for the resolver to auto-select.
			if !assistantERPValidValue(candidate.EntityID) {
				return assistantERPUnavailable("ERP catalog returned an invalid product identity")
			}
			if candidate.Label != candidate.EntityID {
				candidate.Label += " (" + candidate.EntityID + ")"
			}
			out = append(out, candidate)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type assistantERPShop struct {
	id, platform, currency string
	enabled                bool
}

const assistantERPShopsSQL = `
SELECT shop_id, platform, COALESCE(currency, ''), COALESCE(enabled, false)
FROM reporting.v_shops
WHERE shop_id = ANY($1::text[])
ORDER BY shop_id
LIMIT 8`

// selectShops resolves each explicit identifier and rejects a contradictory
// intersection. Disabled shops stay in the expected coverage set.
func (s *AssistantERPQuantitySource) selectShops(ctx context.Context, tx *sql.Tx, scope models.Scope) ([]assistantERPShop, error) {
	shops, platforms := scope.ShopIDs, scope.PlatformIDs
	switch scope.Mode {
	case models.ScopeModeAll:
		if len(shops)+len(platforms) != 0 {
			return nil, assistantERPConflictingScope()
		}
	case models.ScopeModeSelected:
		if len(shops)+len(platforms) == 0 {
			return nil, assistantERPConflictingScope()
		}
	default:
		return nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "scope.mode", "invalid ERP pilot scope mode")
	}
	if len(shops) > assistantERPPilotShopCount || len(platforms) > assistantERPPilotShopCount {
		return nil, assistantERPOutsideScope()
	}
	for _, id := range shops {
		if _, ok := s.orderSourceByShop[id]; !ok {
			return nil, assistantERPOutsideScope()
		}
	}
	for _, id := range platforms {
		if !assistantERPValidEntityID(id) {
			return nil, assistantERPOutsideScope()
		}
	}

	rows, err := tx.QueryContext(ctx, assistantERPShopsSQL, pq.Array(s.shopIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]bool, assistantERPPilotShopCount)
	knownPlatforms := make(map[string]bool, assistantERPPilotShopCount)
	selectedPlatforms := make(map[string]bool, assistantERPPilotShopCount)
	var selected []assistantERPShop
	for rows.Next() {
		var shop assistantERPShop
		if err := rows.Scan(&shop.id, &shop.platform, &shop.currency, &shop.enabled); err != nil {
			return nil, err
		}
		if seen[shop.id] || !assistantERPValidEntityID(shop.platform) {
			return nil, assistantERPUnavailable("ERP pilot shop catalog is invalid")
		}
		seen[shop.id], knownPlatforms[shop.platform] = true, true
		if len(shops) > 0 && !slices.Contains(shops, shop.id) {
			continue
		}
		if len(platforms) > 0 && !slices.Contains(platforms, shop.platform) {
			continue
		}
		selected = append(selected, shop)
		selectedPlatforms[shop.platform] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(seen) != assistantERPPilotShopCount {
		return nil, assistantERPUnavailable("ERP pilot shop catalog is incomplete")
	}
	for _, id := range platforms {
		if !knownPlatforms[id] {
			return nil, assistantERPOutsideScope()
		}
	}
	if len(selected) == 0 || len(shops) > 0 && len(shops) != len(selected) ||
		len(platforms) > 0 && len(platforms) != len(selectedPlatforms) {
		return nil, assistantERPConflictingScope()
	}
	return selected, nil
}

func assistantERPEnabledIDs(shops []assistantERPShop) []string {
	ids := make([]string, 0, len(shops))
	for _, shop := range shops {
		if shop.enabled {
			ids = append(ids, shop.id)
		}
	}
	return ids
}

const assistantERPProductExistsSQL = `
SELECT EXISTS (
    SELECT 1 FROM reporting.v_assistant_ordinary_sales
    WHERE shop_id = ANY($1::text[]) AND product_id = $2
)`

func assistantERPProductKnown(ctx context.Context, tx *sql.Tx, shops []string, product models.ProductSelection) (bool, error) {
	if product.ProductID == "" {
		return true, nil
	}
	var known bool
	err := tx.QueryRowContext(ctx, assistantERPProductExistsSQL, pq.Array(shops), product.ProductID).Scan(&known)
	return known, err
}

type assistantERPShopCoverage struct {
	main, trend models.CoverageStatus
	asOf        sql.NullTime
}

const assistantERPCoverageSQL = `
WITH requested AS (
    SELECT * FROM unnest($1::text[], $2::text[]) AS r(shop_id, source)
), bounds AS (
    SELECT tstzrange($3::date::timestamp AT TIME ZONE 'Asia/Shanghai',
                     $4::date::timestamp AT TIME ZONE 'Asia/Shanghai', '[)') AS main,
           CASE WHEN $5::date IS NOT NULL THEN
               tstzrange($5::date::timestamp AT TIME ZONE 'Asia/Shanghai',
                         $6::date::timestamp AT TIME ZONE 'Asia/Shanghai', '[)')
           END AS trend
)
SELECT r.shop_id, count(c.shop_id) > 0,
       count(c.shop_id) = 1 AND COALESCE(bool_and(c.quality_status = 'passed' AND c.data_as_of IS NOT NULL AND c.covered @> b.main), false),
       count(c.shop_id) = 1 AND COALESCE(bool_and(c.quality_status = 'passed' AND c.data_as_of IS NOT NULL AND c.covered @> b.trend), false),
       min(c.data_as_of)
FROM requested r CROSS JOIN bounds b
LEFT JOIN reporting.v_coverage c ON c.shop_id = r.shop_id AND c.source = r.source AND c.entity = 'orders'
GROUP BY r.shop_id`

func (s *AssistantERPQuantitySource) shopCoverage(ctx context.Context, tx *sql.Tx, ids []string, window models.DateRange, trend *models.DateRange) (map[string]assistantERPShopCoverage, error) {
	sources := make([]string, 0, len(ids))
	for _, id := range ids {
		sources = append(sources, s.orderSourceByShop[id])
	}
	trendStart, trendEnd := assistantERPTrendArgs(trend)
	rows, err := tx.QueryContext(ctx, assistantERPCoverageSQL, pq.Array(ids), pq.Array(sources),
		window.StartDate, window.EndDateExclusive, trendStart, trendEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]assistantERPShopCoverage, len(ids))
	for rows.Next() {
		var id string
		var exists, mainCovered, trendCovered bool
		var asOf sql.NullTime
		if err := rows.Scan(&id, &exists, &mainCovered, &trendCovered, &asOf); err != nil {
			return nil, err
		}
		out[id] = assistantERPShopCoverage{
			main:  assistantERPCoverageStatus(exists, mainCovered),
			trend: assistantERPCoverageStatus(exists, trendCovered),
			asOf:  asOf,
		}
	}
	return out, rows.Err()
}

func assistantERPCoverageStatus(exists, covered bool) models.CoverageStatus {
	if !exists {
		return models.CoverageStatusMissing
	}
	if covered {
		return models.CoverageStatusCovered
	}
	return models.CoverageStatusUnknown
}

func (s *AssistantERPQuantitySource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	if err := assistantERPValidateBasis(query.Basis); err != nil {
		return nil, err
	}
	if err := assistantERPValidateWindow(query.Window, "period"); err != nil {
		return nil, err
	}
	if err := assistantERPValidateProduct(query.Product); err != nil {
		return nil, err
	}

	var out []repositories.AssistantCoverage
	err := s.read(ctx, func(ctx context.Context, tx *sql.Tx) error {
		shops, err := s.selectShops(ctx, tx, query.Scope)
		if err != nil {
			return err
		}
		ids := assistantERPEnabledIDs(shops)
		known, err := assistantERPProductKnown(ctx, tx, ids, query.Product)
		if err != nil {
			return err
		}
		evidence, err := s.shopCoverage(ctx, tx, ids, query.Window, nil)
		if err != nil {
			return err
		}
		platforms := make(map[string]models.CoverageStatus, len(shops))
		for _, shop := range shops {
			status := evidence[shop.id].main
			if status == "" {
				status = models.CoverageStatusMissing
			}
			if status == models.CoverageStatusCovered && !known {
				status = models.CoverageStatusUnknown
			}
			out = append(out, repositories.AssistantCoverage{EntityType: models.EntityTypeShop, EntityID: shop.id, Status: status})
			if prior, exists := platforms[shop.platform]; exists && prior != status {
				status = models.CoverageStatusUnknown
			}
			platforms[shop.platform] = status
		}
		platformIDs := make([]string, 0, len(platforms))
		for platform := range platforms {
			platformIDs = append(platformIDs, platform)
		}
		slices.Sort(platformIDs)
		for _, platform := range platformIDs {
			out = append(out, repositories.AssistantCoverage{EntityType: models.EntityTypePlatform, EntityID: platform, Status: platforms[platform]})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const assistantERPFactsSQL = `
SELECT shop_id, to_char(day, 'YYYY-MM-DD'),
       CASE WHEN count(quantity) = count(*) THEN sum(quantity::numeric)::text END
FROM reporting.v_assistant_ordinary_sales
WHERE shop_id = ANY($1::text[])
  AND ($2 = '' OR product_id = $2)
  AND ((day >= $3::date AND day < $4::date) OR (day >= $5::date AND day < $6::date))
GROUP BY shop_id, day
ORDER BY shop_id, day
LIMIT $7`

func (s *AssistantERPQuantitySource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	if err := assistantERPValidateBasis(query.Basis); err != nil {
		return models.FactSnapshot{}, err
	}
	for _, metric := range query.Metrics {
		if metric != models.MetricQuantity {
			return models.FactSnapshot{}, models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "metrics", "ERP pilot supports only quantity")
		}
	}
	if err := assistantERPValidateWindow(query.Window, "period"); err != nil {
		return models.FactSnapshot{}, err
	}
	if query.TrendWindow != nil {
		if err := assistantERPValidateWindow(*query.TrendWindow, "trend_period"); err != nil {
			return models.FactSnapshot{}, err
		}
	}
	if err := assistantERPValidateProduct(query.Product); err != nil {
		return models.FactSnapshot{}, err
	}

	snapshot := models.FactSnapshot{Basis: query.Basis, Window: query.Window, ContractVersion: assistantERPQuantityContract}
	err := s.read(ctx, func(ctx context.Context, tx *sql.Tx) error {
		snapshot.ReadAt = time.Now().UTC()
		shops, err := s.selectShops(ctx, tx, query.Scope)
		if err != nil {
			return err
		}
		byID := make(map[string]assistantERPShop, len(shops))
		for _, shop := range shops {
			if shop.enabled && !assistantERPValidCurrency(shop.currency) {
				return assistantERPUnavailable("ERP pilot shop currency is missing or invalid")
			}
			byID[shop.id] = shop
		}
		ids := assistantERPEnabledIDs(shops)
		known, err := assistantERPProductKnown(ctx, tx, ids, query.Product)
		if err != nil {
			return err
		}
		if !known {
			return models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "product.product_id", "product identity could not be verified in the requested ERP pilot scope")
		}
		evidence, err := s.shopCoverage(ctx, tx, ids, query.Window, query.TrendWindow)
		if err != nil {
			return err
		}
		// The oldest reported order watermark is conservative across the
		// selected shops. A missing watermark is never replaced with read time.
		for _, row := range evidence {
			if row.asOf.Valid && (snapshot.DataAsOf.IsZero() || row.asOf.Time.Before(snapshot.DataAsOf)) {
				snapshot.DataAsOf = row.asOf.Time.UTC()
			}
		}
		if snapshot.DataAsOf.IsZero() {
			return assistantERPUnavailable("ERP order coverage has no data watermark for the requested scope")
		}
		trendStart, trendEnd := assistantERPTrendArgs(query.TrendWindow)
		rows, err := tx.QueryContext(ctx, assistantERPFactsSQL, pq.Array(ids), query.Product.ProductID,
			query.Window.StartDate, query.Window.EndDateExclusive, trendStart, trendEnd, assistantERPFactLimit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if len(snapshot.Rows) == assistantERPFactLimit {
				return assistantERPUnavailable("ERP quantity result exceeds the safe row limit")
			}
			var id, day, quantityText string
			if err := rows.Scan(&id, &day, &quantityText); err != nil {
				return err
			}
			quantity, err := decimal.NewFromString(quantityText)
			if err != nil {
				return err
			}
			shop, coverage := byID[id], evidence[id]
			status := coverage.main
			if query.TrendWindow != nil {
				inMain := assistantERPDayInWindow(day, query.Window)
				inTrend := assistantERPDayInWindow(day, *query.TrendWindow)
				if !inMain {
					status = coverage.trend
				} else if inTrend && status != coverage.trend {
					// The shared row cannot carry two coverage statuses. The
					// workflow reads independent evidence for each window.
					status = models.CoverageStatusUnknown
				}
			}
			snapshot.Rows = append(snapshot.Rows, models.FactRow{
				EntityID:       shop.platform + "/" + shop.id,
				Basis:          query.Basis,
				BusinessDate:   day,
				Quantity:       quantity,
				Currency:       shop.currency,
				CoverageStatus: status,
				BundleRole:     models.BundleRoleNone,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return models.FactSnapshot{}, err
	}
	return snapshot, nil
}

func assistantERPTrendArgs(window *models.DateRange) (any, any) {
	if window == nil {
		return nil, nil
	}
	return window.StartDate, window.EndDateExclusive
}

func assistantERPDayInWindow(day string, window models.DateRange) bool {
	return day >= window.StartDate && day < window.EndDateExclusive
}

func assistantERPValidateWindow(window models.DateRange, field string) error {
	start, startErr := time.Parse(time.DateOnly, window.StartDate)
	end, endErr := time.Parse(time.DateOnly, window.EndDateExclusive)
	if startErr != nil || endErr != nil || start.Year() < 1 || start.Format(time.DateOnly) != window.StartDate ||
		end.Format(time.DateOnly) != window.EndDateExclusive || !start.Before(end) {
		return models.NewValidationError(models.ErrorCodeInvalidDateRange, field, "ERP pilot requires a non-empty ISO business date range")
	}
	return nil
}

func assistantERPValidateBasis(basis models.Basis) error {
	if basis != models.BasisPayment {
		return models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "basis", "ERP quantity pilot requires payment basis")
	}
	return nil
}

func assistantERPValidateProduct(product models.ProductSelection) error {
	if len(product.SkuIDs) != 0 {
		return models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "product.sku_ids", "ERP quantity pilot has no verified SKU identity")
	}
	if product.ProductID != "" && !assistantERPValidValue(product.ProductID) {
		return models.NewValidationError(models.ErrorCodeInvalidSchema, "product.product_id", "invalid ERP product identifier")
	}
	return nil
}

func assistantERPValidValue(value string) bool {
	return value != "" && len(value) <= assistantERPValueLimit && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}

func assistantERPValidEntityID(value string) bool {
	return assistantERPValidValue(value) && !strings.ContainsRune(value, '/')
}

func assistantERPValidCurrency(currency string) bool {
	return len(currency) == 3 &&
		currency[0] >= 'A' && currency[0] <= 'Z' &&
		currency[1] >= 'A' && currency[1] <= 'Z' &&
		currency[2] >= 'A' && currency[2] <= 'Z'
}

func assistantERPConflictingScope() error {
	return models.NewValidationError(models.ErrorCodeConflictingScope, "scope", "ERP pilot scope selections do not agree")
}

func assistantERPOutsideScope() error {
	return models.NewValidationError(models.ErrorCodeUnauthorizedScope, "scope", "requested entity is outside the approved ERP pilot scope")
}

func assistantERPUnavailable(message string) error {
	return models.NewValidationError(models.ErrorCodeSourceUnavailable, "source", message)
}

func (s *AssistantERPQuantitySource) read(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, assistantERPReadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.reader.WithReadOnlyTx(ctx, func(tx *sql.Tx) error { return fn(ctx, tx) })
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	if cause := sourceAccessCause(err); cause != nil {
		return cause
	}
	if v, ok := models.AsValidationError(err); ok {
		return v
	}
	// Driver errors can contain SQL, identifiers, or connection details.
	return assistantERPUnavailable("ERP quantity source read failed")
}
