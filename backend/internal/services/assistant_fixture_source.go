package services

import (
	"context"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

// Assistant fixture source (T05).
//
// AssistantFixtureSource implements repositories.AssistantSourceRepository over
// the frozen numeric fixture — no
// database, no SQL, no driver. It exists so the workflow, the metric engine,
// and the injection acceptance can be developed and verified before Q01–Q05
// close. It must never be presented as customer-data verification; the real
// source adapter is T19.
//
// The fixture does not filter rows by product or scope: it is a fixed two-shop
// table, and filtering an in-memory fixture by an identifier would test the
// filter, not the source. Scope and product values are captured in the plan
// exactly as received; the returned rows never exceed the fixture.
// (AssistantFixturePlan records the values a real adapter would bind.)

const (
	// AssistantFixtureContractVersion is the fixture's declared contract
	// version. Non-empty by definition: an empty version means "no approved
	// contract", and repositories.AssistantReadSnapshot must refuse such a
	// source with capability_unavailable.
	AssistantFixtureContractVersion = "fixture-2026-09"

	// AssistantFixtureCurrency is the fixture's single currency.
	AssistantFixtureCurrency = "CNY"

	// AssistantFixturePlatformID is the one platform both fixture shops sit on.
	AssistantFixturePlatformID = "PL-1"

	// AssistantFixtureShopS1 and AssistantFixtureShopS2 are the fixture shops,
	// matching the frozen numeric-fixture table.
	AssistantFixtureShopS1 = "S1"
	AssistantFixtureShopS2 = "S2"

	// AssistantFixtureProductID and AssistantFixtureSkuID are the fixture's
	// single product identity: one product, one verified period.
	AssistantFixtureProductID = "P-GLASS-500"
	AssistantFixtureSkuID     = "P-GLASS-500-SKU1"
	AssistantFixtureProduct   = "双层玻璃杯 500ml"

	// Frozen fixture business dates, inside the frozen default window
	// 2026-09-01..2026-09-08 exclusive (clock 2026-09-08T00:30:00+08:00,
	// Asia/Shanghai). Payment facts fall on their payment days, outstock facts
	// on their later shipping days (AC04 attributes by basis, never by one
	// shared date). The refund occurs on 2026-09-05 while its original payment
	// lies outside the window (2026-08-30); FactRow carries only the refund
	// occurrence date, so the out-of-period payment is a documented fixture
	// property, not a field.
	assistantFixtureDateS1Payment   = "2026-09-01"
	assistantFixtureDateS2Payment   = "2026-09-02"
	assistantFixtureDateS1Outstock  = "2026-09-03"
	assistantFixtureDateS2Outstock  = "2026-09-04"
	assistantFixtureRefundDate      = "2026-09-05"
	AssistantFixtureRefundSales     = "10"
	AssistantFixtureRefundPaymentAt = "2026-08-30"
)

// AssistantFixturePlan is the captured typed query plan of one fixture source
// call. It records exactly what the source received. There is no SQL anywhere
// in the fixture path; BoundParameters lists the values a real fixed statement
// would bind, in a deterministic order, so tests can assert a hostile value
// stayed one bound parameter and never became SQL text.
type AssistantFixturePlan struct {
	Operation       string
	Basis           models.Basis
	Window          models.DateRange
	ScopeMode       models.ScopeMode
	PlatformIDs     []string
	ShopIDs         []string
	ProductID       string
	SkuIDs          []string
	Metrics         []models.MetricID
	BoundParameters []string
}

// assistantFixtureBoundParameters orders the typed values a fixed statement
// would bind: product ID, SKU IDs, shop IDs, platform IDs. Empty values are
// skipped.
func assistantFixtureBoundParameters(product models.ProductSelection, scope models.Scope) []string {
	var params []string
	if product.ProductID != "" {
		params = append(params, product.ProductID)
	}
	params = append(params, product.SkuIDs...)
	params = append(params, scope.ShopIDs...)
	params = append(params, scope.PlatformIDs...)
	return params
}

// AssistantFixtureSource is a repositories.AssistantSourceRepository over the
// frozen numeric fixture. Plans records the captured query plans of all calls.
type AssistantFixtureSource struct {
	clock func() time.Time

	mu    sync.Mutex
	plans []AssistantFixturePlan
}

// NewAssistantFixtureSource builds the fixture source on the frozen fixture
// clock, so every snapshot is reproducible.
func NewAssistantFixtureSource() *AssistantFixtureSource {
	return &AssistantFixtureSource{clock: AssistantFixtureClock}
}

// Plans returns a copy of the captured query plans.
func (s *AssistantFixtureSource) Plans() []AssistantFixturePlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AssistantFixturePlan, len(s.plans))
	copy(out, s.plans)
	return out
}

// record appends one captured plan.
func (s *AssistantFixtureSource) record(plan AssistantFixturePlan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans = append(s.plans, plan)
}

// Capabilities declares the fixture contract: both bases, the full metric
// allow-list, one identity domain, one currency, and coverage evidence.
func (s *AssistantFixtureSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	return models.SourceCapabilities{
		ContractVersion:  AssistantFixtureContractVersion,
		SupportedBases:   []models.Basis{models.BasisPayment, models.BasisOutstock},
		SupportedMetrics: models.SupportedMetrics(),
		IdentityDomain:   "fixture",
		Currencies:       []string{AssistantFixtureCurrency},
		CoverageEvidence: true,
	}, nil
}

// ResolveEntities resolves exact typed identifiers against the fixture
// identity: one product, one SKU. Unknown values — including injection text —
// resolve to zero candidates and can never widen the scope.
func (s *AssistantFixtureSource) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	s.record(assistantEntityPlan("resolve_entities", query))
	var candidates []models.Candidate
	if query.Product.ProductID == AssistantFixtureProductID {
		candidates = append(candidates, models.Candidate{
			EntityType: models.EntityTypeProduct,
			EntityID:   AssistantFixtureProductID,
			Label:      AssistantFixtureProduct,
		})
	}
	if query.Product.ProductID == AssistantFixtureSkuID {
		candidates = append(candidates, models.Candidate{
			EntityType: models.EntityTypeSku,
			EntityID:   AssistantFixtureSkuID,
			Label:      AssistantFixtureProduct,
		})
	}
	return candidates, nil
}

// ReadCoverage reads per-entity coverage. Both fixture shops (and their
// platform) are covered inside the frozen domain window for a known product;
// a selected entity the fixture does not carry — or that the selected
// platforms do not carry — is reported missing, a known entity with an
// unknown product or a window outside the static domain is reported unknown,
// never silently dropped and never synthesized. A selected scope that names
// only platforms expands to those platforms' member shops from the fixed
// catalog.
func (s *AssistantFixtureSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	s.record(AssistantFixturePlan{
		Operation:       "read_coverage",
		Basis:           query.Basis,
		Window:          query.Window,
		ScopeMode:       query.Scope.Mode,
		PlatformIDs:     append([]string(nil), query.Scope.PlatformIDs...),
		ShopIDs:         append([]string(nil), query.Scope.ShopIDs...),
		ProductID:       query.Product.ProductID,
		SkuIDs:          append([]string(nil), query.Product.SkuIDs...),
		BoundParameters: assistantFixtureBoundParameters(query.Product, query.Scope),
	})

	productKnown := assistantFixtureProductKnown(query.Product)
	windowCovered := assistantFixtureWindowCovered(query.Window)

	if query.Scope.Mode == models.ScopeModeAll {
		return []repositories.AssistantCoverage{
			{EntityType: models.EntityTypeShop, EntityID: AssistantFixtureShopS1, Status: assistantFixtureCoverageStatus(true, productKnown, windowCovered)},
			{EntityType: models.EntityTypeShop, EntityID: AssistantFixtureShopS2, Status: assistantFixtureCoverageStatus(true, productKnown, windowCovered)},
			{EntityType: models.EntityTypePlatform, EntityID: AssistantFixturePlatformID, Status: assistantFixtureCoverageStatus(true, productKnown, windowCovered)},
		}, nil
	}

	coverage := make([]repositories.AssistantCoverage, 0, len(query.Scope.ShopIDs)+len(query.Scope.PlatformIDs)+2)
	// The selected scope's platform/shop intersection is executed against the
	// fixed catalog, never against ID prefixes: explicit shops keep their own
	// missing/unknown semantics, and a shop the selected platforms do not
	// carry is never claimed covered.
	for _, shopID := range query.Scope.ShopIDs {
		coverage = append(coverage, repositories.AssistantCoverage{
			EntityType: models.EntityTypeShop,
			EntityID:   shopID,
			Status:     assistantFixtureCoverageStatus(assistantFixtureShopInPlatforms(shopID, query.Scope.PlatformIDs), productKnown, windowCovered),
		})
	}
	for _, platformID := range query.Scope.PlatformIDs {
		// A platform is covered for this request only when its member shops
		// intersect the request's shop selection; an empty intersection is a
		// scope the catalog cannot serve, not a covered true zero.
		inCatalog := platformID == AssistantFixturePlatformID &&
			assistantFixturePlatformCarriesShops(platformID, query.Scope.ShopIDs)
		coverage = append(coverage, repositories.AssistantCoverage{
			EntityType: models.EntityTypePlatform,
			EntityID:   platformID,
			Status:     assistantFixtureCoverageStatus(inCatalog, productKnown, windowCovered),
		})
	}
	if len(query.Scope.ShopIDs) == 0 {
		// Platform-only selection: report the platform's member shops from
		// the fixed catalog, so a platform-scoped shop grouping has honest
		// per-shop coverage evidence instead of an empty expected set.
		for _, platformID := range query.Scope.PlatformIDs {
			for _, shopID := range assistantFixturePlatformShops(platformID) {
				coverage = append(coverage, repositories.AssistantCoverage{
					EntityType: models.EntityTypeShop,
					EntityID:   shopID,
					Status:     assistantFixtureCoverageStatus(true, productKnown, windowCovered),
				})
			}
		}
	}
	return coverage, nil
}

// assistantFixtureProductKnown reports whether a product/SKU selection matches
// the fixture identity. The zero selection (comparison workflow) has no product
// restriction and is known.
func assistantFixtureProductKnown(product models.ProductSelection) bool {
	if product.ProductID == "" {
		return true
	}
	if product.ProductID != AssistantFixtureProductID {
		return false
	}
	if len(product.SkuIDs) == 0 {
		return true
	}
	for _, skuID := range product.SkuIDs {
		if skuID == AssistantFixtureSkuID {
			return true
		}
	}
	return false
}

// assistantFixtureWindowCovered reports whether a query window lies fully
// inside the fixture's static coverage domain [2026-09-01, 2026-09-08).
func assistantFixtureWindowCovered(window models.DateRange) bool {
	if window.StartDate == "" || window.EndDateExclusive == "" {
		return false
	}
	return window.StartDate >= models.AssistantDefaultStartDate &&
		window.EndDateExclusive <= models.AssistantDefaultEndDateExclu
}

// assistantFixtureCoverageStatus maps requested-catalog membership and the
// product/window gates onto a coverage state. An entity the requested catalog
// does not carry — unknown, or outside the selected platforms — is missing; a
// carried entity is covered only when the product is known and the window is
// fully in-domain, otherwise unknown.
func assistantFixtureCoverageStatus(inCatalog, productKnown, windowCovered bool) models.CoverageStatus {
	if !inCatalog {
		return models.CoverageStatusMissing
	}
	if !productKnown || !windowCovered {
		return models.CoverageStatusUnknown
	}
	return models.CoverageStatusCovered
}

// assistantFixtureShopExists reports whether a shop exists in the fixture.
func assistantFixtureShopExists(shopID string) bool {
	return shopID == AssistantFixtureShopS1 || shopID == AssistantFixtureShopS2
}

// assistantFixturePlatformShops returns the member shops the fixed catalog
// assigns to a platform; a platform outside the catalog has no member shops.
func assistantFixturePlatformShops(platformID string) []string {
	if platformID != AssistantFixturePlatformID {
		return nil
	}
	return []string{AssistantFixtureShopS1, AssistantFixtureShopS2}
}

// assistantFixtureShopInPlatforms reports whether the fixed catalog carries
// the shop under one of the requested platforms. An empty platform list does
// not constrain the shop.
func assistantFixtureShopInPlatforms(shopID string, platformIDs []string) bool {
	if !assistantFixtureShopExists(shopID) {
		return false
	}
	if len(platformIDs) == 0 {
		return true
	}
	for _, platformID := range platformIDs {
		for _, member := range assistantFixturePlatformShops(platformID) {
			if member == shopID {
				return true
			}
		}
	}
	return false
}

// assistantFixturePlatformCarriesShops reports whether a platform's fixed
// member shops intersect the request's explicit shop selection; an empty
// selection does not constrain the platform.
func assistantFixturePlatformCarriesShops(platformID string, shopIDs []string) bool {
	if len(shopIDs) == 0 {
		return true
	}
	for _, member := range assistantFixturePlatformShops(platformID) {
		for _, shopID := range shopIDs {
			if member == shopID {
				return true
			}
		}
	}
	return false
}

// assistantFixtureRefundScopeProven reports whether the anonymous refund can
// be proven to belong to the requested scope. The fixed catalog is exactly
// PL-1 → S1/S2 and the refund names no entity, so only a scope that provably
// covers that whole catalog carries it: the all mode, or a selected scope
// whose explicit shops — or, when no shops are named, the member shops of its
// selected platforms — exhaust {S1, S2}. Membership is never inferred from
// fact rows, request ID prefixes, or the current time.
func assistantFixtureRefundScopeProven(scope models.Scope) bool {
	if scope.Mode != models.ScopeModeSelected {
		return scope.Mode == models.ScopeModeAll
	}
	shops := append([]string(nil), scope.ShopIDs...)
	if len(shops) == 0 {
		for _, platformID := range scope.PlatformIDs {
			shops = append(shops, assistantFixturePlatformShops(platformID)...)
		}
	}
	for _, catalogShop := range assistantFixturePlatformShops(AssistantFixturePlatformID) {
		found := false
		for _, shopID := range shops {
			if shopID == catalogShop {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// assistantFixtureScopeContains reports whether a fact row entity belongs to
// the requested scope. An absent dimension does not constrain the row; when
// shop and platform constraints are both present the row must satisfy their
// intersection, so a platform match alone can never leak a shop the request
// did not name.
func assistantFixtureScopeContains(scope models.Scope, entityID string) bool {
	if scope.Mode == models.ScopeModeAll {
		return true
	}
	platformID, shopID := splitEntity(entityID)
	if len(scope.ShopIDs) > 0 {
		found := false
		for _, id := range scope.ShopIDs {
			if id == shopID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(scope.PlatformIDs) > 0 {
		found := false
		for _, id := range scope.PlatformIDs {
			if id == platformID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// assistantFixtureWindowContains reports whether a business date lies inside
// the union of the main and trend windows.
func assistantFixtureWindowContains(query models.FactQuery, date string) bool {
	if assistantFixtureDateInRange(date, query.Window) {
		return true
	}
	return query.TrendWindow != nil && assistantFixtureDateInRange(date, *query.TrendWindow)
}

func assistantFixtureDateInRange(date string, window models.DateRange) bool {
	if date == "" || window.StartDate == "" || window.EndDateExclusive == "" {
		return false
	}
	return date >= window.StartDate && date < window.EndDateExclusive
}

// ReadSalesFacts returns the frozen numeric fixture as one consistent
// snapshot. The frozen table:
//
//	S1: quantity 2, sales 60, cost 30 → margin 30, rate 0.5
//	S2: quantity 1, sales 60, cost 42 → margin 18, rate 0.3
//	total: quantity 3, sales 120, cost 72, margin 48, rate 0.4, avg price 40
//
// Costs are verified, so margins are computable; no bundle or gift component
// exists in the fixture.
//
// The source applies the union-window, product, and scope filters itself: a row
// outside main∪trend, an unknown product/SKU, or an unselected shop/platform is
// not returned, so an unknown product never becomes the two default rows and
// never proves a covered true zero.
func (s *AssistantFixtureSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.record(assistantFactPlan("sales_facts", query))
	rows := assistantFixtureSalesRows(query.Basis)
	if !assistantFixtureProductKnown(query.Product) {
		rows = nil
	}
	filtered := rows[:0]
	for _, row := range rows {
		if !assistantFixtureWindowContains(query, row.BusinessDate) {
			continue
		}
		if !assistantFixtureScopeContains(query.Scope, row.EntityID) {
			continue
		}
		filtered = append(filtered, row)
	}
	rows = filtered
	return models.FactSnapshot{
		Basis:           query.Basis,
		Window:          query.Window,
		ReadAt:          s.clock(),
		DataAsOf:        s.clock(),
		ContractVersion: AssistantFixtureContractVersion,
		Rows:            rows,
	}, nil
}

// assistantFixtureSalesRows builds the two frozen sales rows for the requested
// basis. The frozen amounts are identical under both bases; only the business
// dates differ (AC04: attribution follows the basis). Unknown bases return
// zero rows: the fixture carries exactly payment and outstock.
//
// Each row's entity id carries the fixed platform prefix PL-1/ so the platform
// attribution comes from the source's declared mapping, never from the
// request's scope selection.
func assistantFixtureSalesRows(basis models.Basis) []models.FactRow {
	rows := []models.FactRow{
		assistantFixtureSalesRow(AssistantFixtureShopS1, basis, 2, "60", "30"),
		assistantFixtureSalesRow(AssistantFixtureShopS2, basis, 1, "60", "42"),
	}
	for i := range rows {
		switch {
		case basis == models.BasisOutstock && rows[i].EntityID == assistantFixtureEntityID(AssistantFixtureShopS1):
			rows[i].BusinessDate = assistantFixtureDateS1Outstock
		case basis == models.BasisOutstock:
			rows[i].BusinessDate = assistantFixtureDateS2Outstock
		case rows[i].EntityID == assistantFixtureEntityID(AssistantFixtureShopS1):
			rows[i].BusinessDate = assistantFixtureDateS1Payment
		default:
			rows[i].BusinessDate = assistantFixtureDateS2Payment
		}
	}
	return rows
}

// assistantFixtureEntityID renders a shop's fixture entity id with the fixed
// platform prefix.
func assistantFixtureEntityID(shopID string) string {
	return AssistantFixturePlatformID + "/" + shopID
}

// assistantFixtureSalesRow builds one frozen sales row.
func assistantFixtureSalesRow(shopID string, basis models.Basis, quantity int64, sales, cost string) models.FactRow {
	return models.FactRow{
		EntityID:         assistantFixtureEntityID(shopID),
		Basis:            basis,
		Quantity:         decimal.NewFromInt(quantity),
		Sales:            decimal.RequireFromString(sales),
		Cost:             decimal.RequireFromString(cost),
		CostVerified:     true,
		Currency:         AssistantFixtureCurrency,
		CoverageStatus:   models.CoverageStatusCovered,
		BundleRole:       models.BundleRoleNone,
		GiftCostVerified: true,
	}
}

// ReadRefundFacts returns the frozen refund fixture: one in-period refund of
// 10, original payment outside the window, no product or shop attribution.
// Sales carries the positive refund amount; quantity and cost are zero, and
// cost is NOT marked verified — refund cost semantics are open in Q02/Q04 and
// the fixture does not decide them. The refund is listed separately: 商品毛利合计
// stays 48 and is never deducted here.
//
// The refund is filtered by the provable request scope, not only by the
// window. It carries no shop, platform, or product identity, so it is
// published covered only when the request provably covers the whole fixed
// catalog. A selected subset — one shop of two, or a shop the catalog does
// not carry — cannot prove the refund belongs to it: the row is still
// returned (no fabricated attribution, no dropped request condition) but
// marked coverage unknown, which the metric engine publishes as unavailable
// refunds in the totals, the table totals, and the refund summary alike. The
// full catalog scope keeps the anonymous 10.
func (s *AssistantFixtureSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.record(assistantFactPlan("refund_facts", query))
	status := models.CoverageStatusCovered
	if !assistantFixtureRefundScopeProven(query.Scope) {
		status = models.CoverageStatusUnknown
	}
	return models.FactSnapshot{
		Basis:           query.Basis,
		Window:          query.Window,
		ReadAt:          s.clock(),
		DataAsOf:        s.clock(),
		ContractVersion: AssistantFixtureContractVersion,
		Rows: []models.FactRow{{
			EntityID:         "",
			Basis:            query.Basis,
			BusinessDate:     assistantFixtureRefundDate,
			Quantity:         decimal.Zero,
			Sales:            decimal.RequireFromString(AssistantFixtureRefundSales),
			Cost:             decimal.Zero,
			CostVerified:     false,
			Currency:         AssistantFixtureCurrency,
			CoverageStatus:   status,
			BundleRole:       models.BundleRoleNone,
			GiftCostVerified: false,
		}},
	}, nil
}

// assistantEntityPlan captures the typed plan of a ResolveEntities call.
func assistantEntityPlan(operation string, query repositories.AssistantEntityQuery) AssistantFixturePlan {
	return AssistantFixturePlan{
		Operation:       operation,
		ScopeMode:       query.Scope.Mode,
		PlatformIDs:     append([]string(nil), query.Scope.PlatformIDs...),
		ShopIDs:         append([]string(nil), query.Scope.ShopIDs...),
		ProductID:       query.Product.ProductID,
		SkuIDs:          append([]string(nil), query.Product.SkuIDs...),
		BoundParameters: assistantFixtureBoundParameters(query.Product, query.Scope),
	}
}

// assistantFactPlan captures the typed plan of a fact read.
func assistantFactPlan(operation string, query models.FactQuery) AssistantFixturePlan {
	return AssistantFixturePlan{
		Operation:       operation,
		Basis:           query.Basis,
		Window:          query.Window,
		ScopeMode:       query.Scope.Mode,
		PlatformIDs:     append([]string(nil), query.Scope.PlatformIDs...),
		ShopIDs:         append([]string(nil), query.Scope.ShopIDs...),
		ProductID:       query.Product.ProductID,
		SkuIDs:          append([]string(nil), query.Product.SkuIDs...),
		Metrics:         append([]models.MetricID(nil), query.Metrics...),
		BoundParameters: assistantFixtureBoundParameters(query.Product, query.Scope),
	}
}
