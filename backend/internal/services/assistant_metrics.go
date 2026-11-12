package services

import (
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

const (
	refundPrefix      = "refund:"
	coverageRefPrefix = "cov:"
	chartBarID        = "chart-bar"
	chartLineID       = "chart-line"
	chartBarTitle     = "商品销售额"
	chartLineTitle    = "趋势"
	dimensionEntity   = "entity"
	dimensionDate     = "business_date"
	dataRefTable      = "table.rows"
	dataRefTrend      = "trend"
)

// AssistantMetricCoverage carries the independent per-window coverage evidence
// the metric engine consumes. Main answers the main analysis window; Trend
// answers the recorded trend window. When the two windows are structurally
// equal the same coverage rows may be passed under both views.
type AssistantMetricCoverage struct {
	Main  []repositories.AssistantCoverage
	Trend []repositories.AssistantCoverage
}

// ComputeAnalysisReport derives a deterministic AnalysisReport from an already
// validated AnalysisRequest, FactSnapshot, and the independently read coverage
// evidence. It never reads SQL, never uses float arithmetic, and never invents
// display rounding or a customer threshold.
func ComputeAnalysisReport(req models.AnalysisRequest, snap models.FactSnapshot, coverage AssistantMetricCoverage, reportID string) models.AnalysisReport {
	e := newMetricEngine(req, snap, coverage, reportID)
	return e.report()
}

type metricEngine struct {
	req         models.AnalysisRequest
	snap        models.FactSnapshot
	coverage    AssistantMetricCoverage
	reportID    string
	loc         *time.Location
	start       time.Time
	end         time.Time
	trend0      time.Time
	trend1      time.Time
	hasTrend    bool
	mainStatus  map[string]models.CoverageStatus
	trendStatus map[string]models.CoverageStatus
	// mainLeaves/trendLeaves hold the shop-leaf coverage evidence that
	// constrains platform-level completeness when grouping by platform;
	// leafExcluded lists the leaves that broke it for the main window.
	mainLeaves   map[string]models.CoverageStatus
	trendLeaves  map[string]models.CoverageStatus
	leafExcluded []string
}

type groupKey struct {
	groupID    string
	platformID string
	shopID     string
	currency   string
	entityType models.EntityType
}

type groupAcc struct {
	key              groupKey
	qty              decimal.Decimal
	sales            decimal.Decimal
	cost             decimal.Decimal
	sawQtySales      bool
	sawVerifiedCost  bool
	costUnverified   bool
	bundleUnverified bool
	hasNonZero       bool
}

type refundKey struct {
	level    models.EntityType
	entityID string
	currency string
}

type refundAcc struct {
	amount           decimal.Decimal
	has              bool
	hasCovered       bool
	level            models.EntityType
	entityID         string
	currency         string
	currencyConflict bool
	groups           map[refundKey]decimal.Decimal
	unavailable      string
}

func newMetricEngine(req models.AnalysisRequest, snap models.FactSnapshot, coverage AssistantMetricCoverage, reportID string) *metricEngine {
	loc := time.FixedZone("CST", 8*3600)
	if loaded, err := AssistantLoadLocation(req.Period.Timezone); err == nil {
		loc = loaded
	} else if loaded, err := AssistantLoadLocation(models.AssistantDefaultTimezone); err == nil {
		loc = loaded
	}
	e := &metricEngine{
		req:         req,
		snap:        snap,
		coverage:    coverage,
		reportID:    reportID,
		loc:         loc,
		mainStatus:  coverageStatusByGroup(coverage.Main, req.GroupBy),
		trendStatus: coverageStatusByGroup(coverage.Trend, req.GroupBy),
		mainLeaves:  coverageShopLeaves(coverage.Main, req.GroupBy),
		trendLeaves: coverageShopLeaves(coverage.Trend, req.GroupBy),
	}
	e.start = parseBoundary(req.Period.StartDate, loc)
	e.end = parseBoundary(req.Period.EndDateExclusive, loc)
	if req.TrendPeriod != nil && strings.TrimSpace(req.TrendPeriod.StartDate) != "" {
		e.hasTrend = true
		e.trend0 = parseBoundary(req.TrendPeriod.StartDate, loc)
		e.trend1 = parseBoundary(req.TrendPeriod.EndDateExclusive, loc)
	}
	return e
}

// coverageStatusByGroup maps coverage evidence onto the report's group
// dimension: shop leafs for shop grouping, platform parents for platform
// grouping. The platform parent is never counted again as a shop leaf.
func coverageStatusByGroup(coverage []repositories.AssistantCoverage, groupBy models.GroupBy) map[string]models.CoverageStatus {
	out := make(map[string]models.CoverageStatus, len(coverage))
	for _, entity := range coverage {
		if groupBy == models.GroupByPlatform {
			if entity.EntityType != models.EntityTypePlatform {
				continue
			}
		} else if entity.EntityType != models.EntityTypeShop {
			continue
		}
		out[entity.EntityID] = entity.Status
	}
	return out
}

// coverageShopLeaves collects the shop-leaf coverage evidence that constrains
// platform-level completeness. When grouping by platform, an explicitly
// requested shop the source marks missing/unknown/stale must keep constraining
// its platform group even when a sibling member hits; shop grouping reports
// the leaves themselves and collects nothing.
func coverageShopLeaves(coverage []repositories.AssistantCoverage, groupBy models.GroupBy) map[string]models.CoverageStatus {
	if groupBy != models.GroupByPlatform {
		return nil
	}
	out := make(map[string]models.CoverageStatus, len(coverage))
	for _, entity := range coverage {
		if entity.EntityType == models.EntityTypeShop {
			out[entity.EntityID] = entity.Status
		}
	}
	return out
}

// mainCoverageStatus resolves one group's coverage state and reason codes from
// the main-window evidence. A requested entity absent from the evidence is
// missing; a blocked basis forces every entity unknown.
func (e *metricEngine) mainCoverageStatus(id string, basisBlocked bool) (models.CoverageStatus, []string) {
	if basisBlocked {
		return models.CoverageStatusUnknown, []string{models.ReasonBasisUnavailable}
	}
	if status, ok := e.mainStatus[id]; ok {
		switch status {
		case models.CoverageStatusCovered:
			return models.CoverageStatusCovered, nil
		case models.CoverageStatusStale:
			return models.CoverageStatusStale, []string{models.ReasonCoverageStale}
		case models.CoverageStatusMissing:
			return models.CoverageStatusMissing, []string{models.ReasonCoverageMissing}
		default:
			return models.CoverageStatusUnknown, []string{models.ReasonCoverageUnknown}
		}
	}
	return models.CoverageStatusMissing, []string{models.ReasonCoverageMissing}
}

// coveredIDs returns the set of group ids whose coverage evidence is covered
// for the given window map.
func coveredIDs(statuses map[string]models.CoverageStatus) map[string]struct{} {
	out := make(map[string]struct{}, len(statuses))
	for id, status := range statuses {
		if status == models.CoverageStatusCovered {
			out[id] = struct{}{}
		}
	}
	return out
}

func parseBoundary(date string, loc *time.Location) time.Time {
	date = strings.TrimSpace(date)
	if date == "" {
		return time.Time{}
	}
	if t, err := time.ParseInLocation(models.AssistantDateLayout, date, loc); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	if t, err := time.Parse(time.RFC3339, date); err == nil {
		t = t.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
	}
	return time.Time{}
}

func factInstant(businessDate string, loc *time.Location) (time.Time, bool) {
	businessDate = strings.TrimSpace(businessDate)
	if t, err := time.ParseInLocation(models.AssistantDateLayout, businessDate, loc); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc), true
	}
	if t, err := time.Parse(time.RFC3339, businessDate); err == nil {
		return t.In(loc), true
	}
	return time.Time{}, false
}

func inWindow(businessDate string, start, end time.Time, loc *time.Location) bool {
	if start.IsZero() || end.IsZero() {
		return false
	}
	t, ok := factInstant(businessDate, loc)
	if !ok {
		return false
	}
	return !t.Before(start) && t.Before(end)
}

func (e *metricEngine) report() models.AnalysisReport {
	basisBlocked := e.basisBlocked()
	groups, refunds, trendDates := e.accumulate(basisBlocked)

	requested := e.requestedIDs()
	coverage, included, excluded := e.buildCoverage(requested, groups, basisBlocked)
	rows, totals := e.buildTable(groups, included, excluded, refunds, basisBlocked)
	outcome := e.outcome(requested, included, excluded, groups, basisBlocked)
	candidates := thresholdCandidates(rows, e.req.LowMarginThreshold)

	incomparable := e.hasTrend && !sameEntitySet(coveredIDs(e.mainStatus), coveredIDs(e.trendStatus))
	chartMetric := models.MetricSales
	if len(e.req.Metrics) == 1 && e.req.Metrics[0] == models.MetricQuantity {
		chartMetric = models.MetricQuantity
	}
	trend := e.buildTrend(chartMetric, trendDates, incomparable, basisBlocked)
	charts := e.buildCharts(chartMetric, rows, trend, coverage.Ref)

	actual := e.actualScope(included)
	period := e.req.Period
	if period.Timezone == "" {
		period.Timezone = models.AssistantDefaultTimezone
	}

	var trendWindow *models.DateRange
	if e.hasTrend {
		tw := *e.req.TrendPeriod
		trendWindow = &tw
	}

	return models.AnalysisReport{
		ReportID:           e.reportID,
		SchemaVersion:      models.AssistantSchemaVersion,
		Workflow:           e.req.Workflow,
		Basis:              e.req.Basis,
		RequestedScope:     e.req.Scope,
		ActualScope:        actual,
		Window:             period,
		WindowStart:        e.start,
		WindowEndExclusive: e.end,
		TrendWindow:        trendWindow,
		ContractVersion:    e.snap.ContractVersion,
		Outcome:            outcome,
		Coverage:           coverage,
		Totals:             totals,
		Table: models.ReportTable{
			Dimension: string(e.req.GroupBy),
			Rows:      rows,
			Totals:    totals,
		},
		Trend:       trend,
		Candidates:  candidates,
		Charts:      charts,
		Refunds:     e.buildRefunds(refunds, basisBlocked),
		Evidence:    e.evidence(coverage.Ref),
		DataAsOf:    e.snap.DataAsOf,
		MarginLabel: models.AssistantMarginRefundLabel,
	}
}

func (e *metricEngine) basisBlocked() bool {
	if e.snap.Basis != "" && e.snap.Basis != e.req.Basis {
		return true
	}
	for _, row := range e.snap.Rows {
		if row.Basis == e.req.Basis {
			return false
		}
	}
	return len(e.snap.Rows) > 0
}

func (e *metricEngine) accumulate(basisBlocked bool) (map[groupKey]*groupAcc, refundAcc, map[string]map[groupKey]*groupAcc) {
	groups := map[groupKey]*groupAcc{}
	trendDates := map[string]map[groupKey]*groupAcc{}
	var refunds refundAcc
	if basisBlocked {
		return groups, refunds, trendDates
	}

	for _, row := range e.snap.Rows {
		if row.Basis != "" && row.Basis != e.req.Basis {
			continue
		}
		inMain := inWindow(row.BusinessDate, e.start, e.end, e.loc)
		inTrend := e.hasTrend && inWindow(row.BusinessDate, e.trend0, e.trend1, e.loc)
		if !inMain && !inTrend {
			continue
		}
		if strings.HasPrefix(row.EntityID, refundPrefix) {
			if inMain {
				applyRefund(&refunds, row)
			}
			continue
		}
		if row.BundleRole == models.BundleRoleChild {
			continue
		}
		key := e.groupKeyFor(row)
		if inMain {
			applyFact(ensureGroup(groups, key), row)
		}
		if inTrend {
			date := trendDateKey(row.BusinessDate, e.loc)
			byDate, ok := trendDates[date]
			if !ok {
				byDate = map[groupKey]*groupAcc{}
				trendDates[date] = byDate
			}
			applyFact(ensureGroup(byDate, key), row)
		}
	}
	return groups, refunds, trendDates
}

func applyRefund(dst *refundAcc, row models.FactRow) {
	dst.has = true
	if row.CoverageStatus != models.CoverageStatusCovered {
		switch row.CoverageStatus {
		case models.CoverageStatusMissing:
			dst.unavailable = models.ReasonCoverageMissing
		case models.CoverageStatusStale:
			dst.unavailable = models.ReasonCoverageStale
		default:
			dst.unavailable = models.ReasonCoverageUnknown
		}
		return
	}

	level, id := parseRefundEntity(row.EntityID)
	if !dst.hasCovered {
		dst.hasCovered = true
		dst.currency = row.Currency
		dst.level, dst.entityID = level, id
	} else {
		if dst.currency != row.Currency {
			dst.currencyConflict = true
		}
		if dst.level != level || dst.entityID != id {
			dst.level, dst.entityID = "", ""
		}
	}
	if row.Currency == "" {
		dst.currencyConflict = true
	}
	// Never add unlike currencies, even internally. A conflict stays sticky.
	if !dst.currencyConflict {
		dst.amount = dst.amount.Add(row.Sales)
	}
	if level != "" && id != "" && row.Currency != "" {
		if dst.groups == nil {
			dst.groups = make(map[refundKey]decimal.Decimal)
		}
		key := refundKey{level: level, entityID: id, currency: row.Currency}
		dst.groups[key] = dst.groups[key].Add(row.Sales)
	}
}

func parseRefundEntity(entityID string) (models.EntityType, string) {
	rest := strings.TrimPrefix(entityID, refundPrefix)
	if strings.TrimSpace(rest) == "" {
		return "", ""
	}
	level, id, ok := strings.Cut(rest, ":")
	if !ok {
		return models.EntityTypeShop, rest
	}
	if strings.TrimSpace(id) == "" {
		return "", ""
	}
	switch level {
	case "product":
		return models.EntityTypeProduct, id
	case "shop":
		return models.EntityTypeShop, id
	case "platform":
		return models.EntityTypePlatform, id
	default:
		return "", ""
	}
}

func (e *metricEngine) groupKeyFor(row models.FactRow) groupKey {
	platformID, shopID := splitEntity(row.EntityID)
	key := groupKey{currency: row.Currency, platformID: platformID, shopID: shopID}
	switch e.req.GroupBy {
	case models.GroupByPlatform:
		key.entityType = models.EntityTypePlatform
		key.shopID = ""
		if platformID != "" {
			key.groupID = platformID
		} else {
			key.groupID = row.EntityID
			key.platformID = row.EntityID
		}
	default:
		key.entityType = models.EntityTypeShop
		if shopID != "" {
			key.groupID = shopID
		} else {
			key.groupID = row.EntityID
		}
	}
	return key
}

func splitEntity(entityID string) (platformID, shopID string) {
	if platformID, shopID, ok := strings.Cut(entityID, "/"); ok {
		return platformID, shopID
	}
	return "", entityID
}

func ensureGroup(groups map[groupKey]*groupAcc, key groupKey) *groupAcc {
	if acc, ok := groups[key]; ok {
		return acc
	}
	acc := &groupAcc{key: key}
	groups[key] = acc
	return acc
}

// applyFact accumulates one fact row's verified amounts. Coverage is decided
// by the independent per-window coverage evidence, never by the row's own
// coverage flag; the row still contributes its numbers under whatever coverage
// the source declared for its entity.
func applyFact(acc *groupAcc, row models.FactRow) {
	if row.BundleRole == models.BundleRoleChild {
		return
	}
	switch row.BundleRole {
	case models.BundleRoleGift:
		if row.GiftCostVerified {
			acc.cost = acc.cost.Add(row.Cost)
			acc.sawVerifiedCost = true
			if !row.Cost.IsZero() {
				acc.hasNonZero = true
			}
		}
		return
	case models.BundleRoleParent:
		if !row.CostVerified {
			acc.bundleUnverified = true
		}
	}
	acc.qty = acc.qty.Add(row.Quantity)
	acc.sales = acc.sales.Add(row.Sales)
	acc.sawQtySales = true
	if !row.Quantity.IsZero() || !row.Sales.IsZero() || !row.Cost.IsZero() {
		acc.hasNonZero = true
	}
	if row.BundleRole == models.BundleRoleParent && !row.CostVerified {
		return
	}
	if row.CostVerified {
		acc.cost = acc.cost.Add(row.Cost)
		acc.sawVerifiedCost = true
	} else {
		acc.costUnverified = true
	}
}

func trendDateKey(businessDate string, loc *time.Location) string {
	if t, ok := factInstant(businessDate, loc); ok {
		return t.Format(models.AssistantDateLayout)
	}
	return businessDate
}

// requestedIDs returns the group ids to report: the explicit selection for a
// selected scope, or the full catalog enumerated by the main coverage evidence
// for an all scope. A selected scope that names only platforms reports the
// member shops the source declared in its coverage evidence — the source
// already intersected the selection with its catalog, so the engine consumes
// that evidence instead of guessing membership from fact rows or ID prefixes.
// It never derives the request from fact rows, so an all range with a covered
// entity that has no facts is still retained.
func (e *metricEngine) requestedIDs() []string {
	var ids []string
	switch e.req.Scope.Mode {
	case models.ScopeModeSelected:
		switch {
		case e.req.GroupBy == models.GroupByPlatform:
			ids = append(ids, e.req.Scope.PlatformIDs...)
		case len(e.req.Scope.ShopIDs) > 0:
			ids = append(ids, e.req.Scope.ShopIDs...)
		default:
			for id := range e.mainStatus {
				ids = append(ids, id)
			}
		}
	default:
		for id := range e.mainStatus {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return uniqueStrings(ids)
}

func (e *metricEngine) groupsForID(groups map[groupKey]*groupAcc, id string) []*groupAcc {
	var out []*groupAcc
	for key, acc := range groups {
		if key.groupID == id {
			out = append(out, acc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].key.currency != out[j].key.currency {
			return out[i].key.currency < out[j].key.currency
		}
		return out[i].key.groupID < out[j].key.groupID
	})
	return out
}

func (e *metricEngine) buildCoverage(requested []string, groups map[groupKey]*groupAcc, basisBlocked bool) (models.Coverage, []string, []string) {
	ref := coverageRefPrefix + string(e.req.Basis)
	cov := models.Coverage{
		Metrics:  append([]models.MetricID{}, e.req.Metrics...),
		DataAsOf: e.snap.DataAsOf,
		Ref:      ref,
		Evidence: e.evidence(ref),
	}
	var included, excluded []string
	for _, id := range requested {
		accs := e.groupsForID(groups, id)
		ent := models.CoverageEntity{
			EntityType:  e.entityType(),
			EntityID:    id,
			CoverageRef: ref + ":" + id,
		}
		if e.req.GroupBy == models.GroupByPlatform {
			ent.PlatformID = id
		} else {
			ent.ShopID = id
			if len(accs) == 1 {
				ent.PlatformID = accs[0].key.platformID
				ent.Currency = accs[0].key.currency
			}
		}
		cov.Requested = append(cov.Requested, ent)
		cov.Expected = append(cov.Expected, ent)
		status, reasons := e.mainCoverageStatus(id, basisBlocked)
		ent.Status = status
		ent.ReasonCodes = reasons
		switch status {
		case models.CoverageStatusCovered:
			cov.Covered = append(cov.Covered, ent)
			cov.Included = append(cov.Included, ent)
			included = append(included, id)
		case models.CoverageStatusStale:
			cov.Stale = append(cov.Stale, ent)
			cov.Excluded = append(cov.Excluded, ent)
			excluded = append(excluded, id)
		case models.CoverageStatusMissing:
			cov.Missing = append(cov.Missing, ent)
			cov.Excluded = append(cov.Excluded, ent)
			excluded = append(excluded, id)
		default:
			ent.Status = models.CoverageStatusUnknown
			if len(ent.ReasonCodes) == 0 {
				ent.ReasonCodes = []string{models.ReasonCoverageUnknown}
			}
			cov.Unknown = append(cov.Unknown, ent)
			cov.Excluded = append(cov.Excluded, ent)
			excluded = append(excluded, id)
		}
	}
	// Platform grouping must not lose the requested shop leaves: a member
	// shop the source marks missing/unknown/stale keeps constraining the
	// platform's completeness and stays visible as leaf evidence, even when
	// the platform parent itself is covered through a sibling shop. The leaf
	// never becomes a table row — the parent is not counted twice.
	if e.req.GroupBy == models.GroupByPlatform {
		leafIDs := make([]string, 0, len(e.mainLeaves))
		for id := range e.mainLeaves {
			leafIDs = append(leafIDs, id)
		}
		sort.Strings(leafIDs)
		for _, id := range leafIDs {
			status := e.mainLeaves[id]
			if status == models.CoverageStatusCovered {
				continue
			}
			ent := models.CoverageEntity{
				EntityType:  models.EntityTypeShop,
				EntityID:    id,
				ShopID:      id,
				CoverageRef: ref + ":" + id,
			}
			switch status {
			case models.CoverageStatusStale:
				ent.Status = models.CoverageStatusStale
				ent.ReasonCodes = []string{models.ReasonCoverageStale}
				cov.Stale = append(cov.Stale, ent)
			case models.CoverageStatusMissing:
				ent.Status = models.CoverageStatusMissing
				ent.ReasonCodes = []string{models.ReasonCoverageMissing}
				cov.Missing = append(cov.Missing, ent)
			default:
				ent.Status = models.CoverageStatusUnknown
				ent.ReasonCodes = []string{models.ReasonCoverageUnknown}
				cov.Unknown = append(cov.Unknown, ent)
			}
			cov.Excluded = append(cov.Excluded, ent)
			e.leafExcluded = append(e.leafExcluded, id)
		}
	}
	return cov, included, excluded
}

func (e *metricEngine) entityType() models.EntityType {
	if e.req.GroupBy == models.GroupByPlatform {
		return models.EntityTypePlatform
	}
	return models.EntityTypeShop
}

func (e *metricEngine) buildTable(groups map[groupKey]*groupAcc, included, excluded []string, refunds refundAcc, basisBlocked bool) ([]models.ReportRow, []models.MetricValue) {
	var rows []models.ReportRow
	currencies := map[string]struct{}{}
	grandByCur := map[string]*groupAcc{}

	ids := append(append([]string{}, included...), excluded...)
	ids = uniqueStrings(ids)
	sort.Strings(ids)
	includedSet := stringSet(included)

	for _, id := range ids {
		accs := e.groupsForID(groups, id)
		_, isIncluded := includedSet[id]
		_, coverageReasons := e.mainCoverageStatus(id, basisBlocked)
		if len(accs) == 0 {
			if isIncluded {
				row := e.emptyRow(id, basisBlocked, refunds)
				e.partializeLeafConstrained(row.Metrics, basisBlocked)
				rows = append(rows, row)
			} else {
				rows = append(rows, e.unavailableRow(id, nil, coverageReasons, basisBlocked))
			}
			continue
		}
		for _, acc := range accs {
			row := e.rowFromAcc(acc, !isIncluded, coverageReasons, basisBlocked, refunds)
			if isIncluded {
				e.partializeLeafConstrained(row.Metrics, basisBlocked)
			}
			rows = append(rows, row)
			if isIncluded && !basisBlocked {
				cur := acc.key.currency
				currencies[cur] = struct{}{}
				g := grandByCur[cur]
				if g == nil {
					g = &groupAcc{key: groupKey{currency: cur}}
					grandByCur[cur] = g
				}
				mergeAcc(g, acc)
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		si, sj := salesOf(rows[i]), salesOf(rows[j])
		if si != nil && sj != nil && !si.Equal(*sj) {
			return si.GreaterThan(*sj)
		}
		if si != nil && sj == nil {
			return true
		}
		if si == nil && sj != nil {
			return false
		}
		if rows[i].EntityID != rows[j].EntityID {
			return rows[i].EntityID < rows[j].EntityID
		}
		return rows[i].Currency < rows[j].Currency
	})
	rank := 0
	for i := range rows {
		if metricStatus(rows[i], models.MetricSales) == models.MetricStatusUnavailable &&
			metricStatus(rows[i], models.MetricQuantity) == models.MetricStatusUnavailable {
			continue
		}
		rank++
		rows[i].Rank = rank
		if e.req.LowMarginThreshold != nil {
			if rate := metricValue(rows[i], models.MetricMarginRate); rate != nil && rate.LessThan(*e.req.LowMarginThreshold) {
				rows[i].ThresholdHit = true
			}
		}
	}

	totals := e.buildTotals(grandByCur, currencies, included, excluded, refunds, basisBlocked)
	return rows, totals
}

func mergeAcc(dst, src *groupAcc) {
	dst.qty = dst.qty.Add(src.qty)
	dst.sales = dst.sales.Add(src.sales)
	dst.cost = dst.cost.Add(src.cost)
	dst.sawQtySales = dst.sawQtySales || src.sawQtySales
	dst.sawVerifiedCost = dst.sawVerifiedCost || src.sawVerifiedCost
	dst.costUnverified = dst.costUnverified || src.costUnverified
	dst.bundleUnverified = dst.bundleUnverified || src.bundleUnverified
	dst.hasNonZero = dst.hasNonZero || src.hasNonZero
}

// partializeLeafConstrained downgrades an included platform group's available
// metrics to partial when uncovered shop leaves constrain the group: the
// values stay the provable subset sums, the status stops claiming a complete
// total. Unavailable metrics keep their own stronger reasons.
func (e *metricEngine) partializeLeafConstrained(metrics []models.MetricValue, basisBlocked bool) {
	if basisBlocked || len(e.leafExcluded) == 0 {
		return
	}
	for i := range metrics {
		if metrics[i].Status != models.MetricStatusAvailable {
			continue
		}
		metrics[i].Status = models.MetricStatusPartial
		metrics[i].ReasonCodes = uniqueStrings(append(metrics[i].ReasonCodes, models.ReasonCoverageMissing))
	}
}

func (e *metricEngine) rowFromAcc(acc *groupAcc, excluded bool, coverageReasons []string, basisBlocked bool, refunds refundAcc) models.ReportRow {
	reasons := []string{}
	status := models.MetricStatusAvailable
	covRef := coverageRefPrefix + string(e.req.Basis) + ":" + acc.key.groupID
	if basisBlocked {
		status = models.MetricStatusUnavailable
		reasons = []string{models.ReasonBasisUnavailable}
	} else if excluded {
		status = models.MetricStatusUnavailable
		reasons = append([]string{}, coverageReasons...)
	} else if acc.bundleUnverified {
		reasons = []string{models.ReasonBundleUnverified}
	} else if acc.costUnverified {
		reasons = []string{models.ReasonCostUnverified}
	}
	metrics := e.metricsFromAcc(acc, status, reasons, covRef, acc.key.currency, false, refunds)
	return models.ReportRow{
		EntityType:  acc.key.entityType,
		EntityID:    acc.key.groupID,
		PlatformID:  acc.key.platformID,
		ShopID:      acc.key.shopID,
		Currency:    acc.key.currency,
		Metrics:     metrics,
		CoverageRef: covRef,
		ReasonCodes: uniqueStrings(reasons),
	}
}

func (e *metricEngine) unavailableRow(id string, acc *groupAcc, reasons []string, basisBlocked bool) models.ReportRow {
	if basisBlocked {
		reasons = []string{models.ReasonBasisUnavailable}
	}
	covRef := coverageRefPrefix + string(e.req.Basis) + ":" + id
	platformID, shopID := "", id
	if e.req.GroupBy == models.GroupByPlatform {
		platformID, shopID = id, ""
	} else if acc != nil {
		platformID, shopID = acc.key.platformID, acc.key.shopID
	}
	metrics := e.metricsFromAcc(&groupAcc{}, models.MetricStatusUnavailable, reasons, covRef, "", true, refundAcc{})
	return models.ReportRow{
		EntityType:  e.entityType(),
		EntityID:    id,
		PlatformID:  platformID,
		ShopID:      shopID,
		Metrics:     metrics,
		CoverageRef: covRef,
		ReasonCodes: uniqueStrings(reasons),
	}
}

// emptyRow renders a covered entity that has no fact rows in the main window
// as an available zero row: complete coverage with no transactions is a legal
// true zero, never an unavailable row.
func (e *metricEngine) emptyRow(id string, basisBlocked bool, refunds refundAcc) models.ReportRow {
	key := groupKey{groupID: id, entityType: e.entityType()}
	if e.req.GroupBy == models.GroupByPlatform {
		key.platformID = id
	} else {
		key.shopID = id
	}
	return e.rowFromAcc(&groupAcc{key: key}, false, nil, basisBlocked, refunds)
}

func (e *metricEngine) metricsFromAcc(acc *groupAcc, rowStatus models.MetricStatus, reasons []string, covRef, currency string, forceNull bool, refunds refundAcc) []models.MetricValue {
	out := make([]models.MetricValue, 0, len(e.req.Metrics))
	for _, id := range e.req.Metrics {
		out = append(out, e.oneMetric(id, acc, rowStatus, reasons, covRef, currency, forceNull, refunds))
	}
	return out
}

func (e *metricEngine) oneMetric(id models.MetricID, acc *groupAcc, rowStatus models.MetricStatus, reasons []string, covRef, currency string, forceNull bool, refunds refundAcc) models.MetricValue {
	mv := models.MetricValue{MetricID: id, CoverageRef: covRef, Currency: currency}
	if forceNull || rowStatus == models.MetricStatusUnavailable {
		mv.Status = models.MetricStatusUnavailable
		mv.ReasonCodes = append([]string{}, reasons...)
		return mv
	}
	switch id {
	case models.MetricQuantity:
		mv.Status = models.MetricStatusAvailable
		mv.Value = decPtr(acc.qty)
	case models.MetricSales:
		mv.Status = models.MetricStatusAvailable
		mv.Value = decPtr(acc.sales)
	case models.MetricCost:
		if acc.costUnverified || acc.bundleUnverified {
			mv.Status = models.MetricStatusUnavailable
			mv.ReasonCodes = costReasons(acc)
			return mv
		}
		mv.Status = models.MetricStatusAvailable
		mv.Value = decPtr(acc.cost)
	case models.MetricMargin:
		if acc.costUnverified || acc.bundleUnverified {
			mv.Status = models.MetricStatusUnavailable
			mv.ReasonCodes = costReasons(acc)
			return mv
		}
		margin := acc.sales.Sub(acc.cost)
		mv.Status = models.MetricStatusAvailable
		mv.Value = decPtr(margin)
	case models.MetricMarginRate:
		if acc.costUnverified || acc.bundleUnverified {
			mv.Status = models.MetricStatusUnavailable
			mv.ReasonCodes = costReasons(acc)
			return mv
		}
		if acc.sales.IsZero() {
			mv.Status = models.MetricStatusUnavailable
			mv.ReasonCodes = []string{models.ReasonDivideByZero}
			return mv
		}
		margin := acc.sales.Sub(acc.cost)
		mv.Status = models.MetricStatusAvailable
		mv.Value = decPtr(margin.Div(acc.sales))
	case models.MetricAveragePrice:
		if acc.qty.IsZero() {
			mv.Status = models.MetricStatusUnavailable
			mv.ReasonCodes = []string{models.ReasonDivideByZero}
			return mv
		}
		mv.Status = models.MetricStatusAvailable
		mv.Value = decPtr(acc.sales.Div(acc.qty))
	case models.MetricRefund:
		return refundMetricForRow(acc, refunds, covRef, currency)
	default:
		mv.Status = models.MetricStatusUnavailable
	}
	return mv
}

func costReasons(acc *groupAcc) []string {
	if acc.bundleUnverified {
		return []string{models.ReasonBundleUnverified}
	}
	return []string{models.ReasonCostUnverified}
}

func refundMetricForRow(acc *groupAcc, refunds refundAcc, covRef, currency string) models.MetricValue {
	mv := models.MetricValue{MetricID: models.MetricRefund, Status: models.MetricStatusUnavailable, CoverageRef: covRef, Currency: currency}
	if refunds.unavailable != "" {
		mv.ReasonCodes = []string{refunds.unavailable}
		return mv
	}
	key := refundKey{level: acc.key.entityType, entityID: acc.key.groupID, currency: currency}
	if amount, ok := refunds.groups[key]; ok {
		mv.Status = models.MetricStatusAvailable
		mv.Value = decPtr(amount)
		return mv
	}
	if refunds.has {
		mv.ReasonCodes = []string{models.ReasonProductAttributionUnavailable}
	}
	return mv
}

func (e *metricEngine) buildTotals(grandByCur map[string]*groupAcc, currencies map[string]struct{}, included, excluded []string, refunds refundAcc, basisBlocked bool) []models.MetricValue {
	covRef := coverageRefPrefix + string(e.req.Basis) + ":totals"
	if basisBlocked {
		return e.metricsFromAcc(&groupAcc{}, models.MetricStatusUnavailable, []string{models.ReasonBasisUnavailable}, covRef, "", true, refunds)
	}
	if len(included) == 0 {
		// No covered entity survives the scope: an empty expected set is no
		// coverage, never an available true zero.
		return e.metricsFromAcc(&groupAcc{}, models.MetricStatusUnavailable, []string{models.ReasonCoverageMissing}, covRef, "", true, refunds)
	}
	if len(currencies) > 1 {
		out := e.metricsFromAcc(&groupAcc{}, models.MetricStatusUnavailable, []string{models.ReasonCurrencyConversionUnsupported}, covRef, "", true, refunds)
		for i := range out {
			out[i].Status = models.MetricStatusUnavailable
			out[i].Value = nil
			out[i].ReasonCodes = []string{models.ReasonCurrencyConversionUnsupported}
			if out[i].MetricID == models.MetricRefund {
				out[i] = totalRefundMetric(refunds, covRef)
			}
		}
		return out
	}
	var acc *groupAcc
	var currency string
	for cur, g := range grandByCur {
		acc = g
		currency = cur
	}
	if acc == nil {
		acc = &groupAcc{}
	}
	status := models.MetricStatusAvailable
	var reasons []string
	if len(excluded) > 0 || len(e.leafExcluded) > 0 {
		// An excluded group or an uncovered requested shop leaf keeps the
		// total partial: it must never claim a complete aggregation.
		status = models.MetricStatusPartial
		reasons = []string{models.ReasonCoverageMissing}
	}
	metrics := e.metricsFromAcc(acc, models.MetricStatusAvailable, reasons, covRef, currency, false, refunds)
	if status == models.MetricStatusPartial {
		for i := range metrics {
			if metrics[i].Status == models.MetricStatusAvailable {
				metrics[i].Status = models.MetricStatusPartial
			}
			metrics[i].ReasonCodes = uniqueStrings(append(metrics[i].ReasonCodes, reasons...))
		}
	}
	for i := range metrics {
		if metrics[i].MetricID == models.MetricRefund {
			metrics[i] = totalRefundMetric(refunds, covRef)
		}
	}
	return metrics
}

func totalRefundMetric(refunds refundAcc, covRef string) models.MetricValue {
	mv := models.MetricValue{MetricID: models.MetricRefund, Status: models.MetricStatusUnavailable, CoverageRef: covRef}
	if refunds.unavailable != "" {
		mv.ReasonCodes = []string{refunds.unavailable}
		return mv
	}
	if !refunds.has {
		return mv
	}
	if refunds.currencyConflict {
		mv.ReasonCodes = []string{models.ReasonCurrencyConversionUnsupported}
		return mv
	}
	mv.Status = models.MetricStatusAvailable
	mv.Currency = refunds.currency
	mv.Value = decPtr(refunds.amount)
	return mv
}

func (e *metricEngine) buildRefunds(refunds refundAcc, basisBlocked bool) models.RefundSummary {
	sum := models.RefundSummary{
		Label:            models.AssistantMarginRefundLabel,
		OccurrenceWindow: e.req.Period.DateRange,
	}
	if basisBlocked {
		sum.Status = models.MetricStatusUnavailable
		sum.ReasonCodes = []string{models.ReasonBasisUnavailable}
		return sum
	}
	metric := totalRefundMetric(refunds, coverageRefPrefix+string(e.req.Basis)+":refunds")
	sum.Status, sum.Amount, sum.Currency = metric.Status, metric.Value, metric.Currency
	sum.ReasonCodes = metric.ReasonCodes
	// Aggregate request scope does not imply a shop, platform, or product.
	sum.Level, sum.EntityID = refunds.level, refunds.entityID
	if refunds.level != models.EntityTypeProduct || refunds.entityID == "" {
		sum.ReasonCodes = uniqueStrings(append(sum.ReasonCodes, models.ReasonProductAttributionUnavailable))
		sum.ProductRefund = &models.MetricValue{
			MetricID:    models.MetricRefund,
			Status:      models.MetricStatusUnavailable,
			ReasonCodes: []string{models.ReasonProductAttributionUnavailable},
		}
	}
	return sum
}

func (e *metricEngine) outcome(requested, included, excluded []string, groups map[groupKey]*groupAcc, basisBlocked bool) models.RunOutcome {
	if basisBlocked {
		return models.OutcomeUnavailable
	}
	if len(requested) == 0 {
		return models.OutcomeUnavailable
	}
	if len(included) == 0 {
		return models.OutcomeUnavailable
	}
	if len(excluded) > 0 || len(e.leafExcluded) > 0 {
		// An uncovered requested shop leaf constrains platform completeness
		// exactly like an excluded group: one member hit never upgrades the
		// request to complete.
		return models.OutcomePartial
	}
	anyNonZero := false
	for _, id := range included {
		for _, acc := range e.groupsForID(groups, id) {
			if acc.hasNonZero {
				anyNonZero = true
			}
		}
	}
	if !anyNonZero {
		return models.OutcomeEmpty
	}
	return models.OutcomeComplete
}

func (e *metricEngine) actualScope(included []string) models.Scope {
	scope := models.Scope{Mode: e.req.Scope.Mode}
	if e.req.GroupBy == models.GroupByPlatform {
		scope.PlatformIDs = append([]string{}, included...)
	} else {
		scope.ShopIDs = append([]string{}, included...)
		if plats := e.req.Scope.PlatformIDs; len(plats) > 0 {
			scope.PlatformIDs = append([]string{}, plats...)
		}
	}
	return scope
}

func (e *metricEngine) buildTrend(metric models.MetricID, trendDates map[string]map[groupKey]*groupAcc, incomparable, basisBlocked bool) []models.ChartPoint {
	if !e.hasTrend {
		return nil
	}
	trendCovered := coveredIDs(e.trendStatus)
	fullCoverage := len(e.trendStatus) > 0 && len(trendCovered) == len(e.trendStatus)
	if e.req.GroupBy == models.GroupByPlatform {
		// Explicit shop leaves constrain trend completeness exactly as they
		// constrain the main window: an uncovered leaf keeps the daily points
		// partial instead of available.
		for _, status := range e.trendLeaves {
			if status != models.CoverageStatusCovered {
				fullCoverage = false
				break
			}
		}
	}
	var points []models.ChartPoint
	for d := e.trend0; d.Before(e.trend1); d = d.AddDate(0, 0, 1) {
		date := d.Format(models.AssistantDateLayout)
		byKey := trendDates[date]
		point := models.ChartPoint{
			Dimension:    date,
			BusinessDate: date,
			MetricID:     metric,
			CoverageRef:  coverageRefPrefix + string(e.req.Basis) + ":trend:" + date,
		}
		switch {
		case basisBlocked:
			point.Status = models.MetricStatusUnavailable
			point.ReasonCodes = []string{models.ReasonBasisUnavailable}
		case len(trendCovered) == 0:
			// No usable trend coverage: unknown dates stay a null break, never
			// a fabricated zero.
			point.Status = models.MetricStatusUnavailable
			point.ReasonCodes = []string{models.ReasonCoverageUnknown}
		default:
			// Covered entities contribute their verified values; a covered
			// entity with no row on this date contributes zero.
			var total decimal.Decimal
			currencies := map[string]struct{}{}
			for key, acc := range byKey {
				if _, ok := trendCovered[key.groupID]; !ok {
					continue
				}
				if metric == models.MetricQuantity {
					total = total.Add(acc.qty)
				} else {
					total = total.Add(acc.sales)
					currencies[acc.key.currency] = struct{}{}
				}
			}
			if len(currencies) > 1 {
				point.Status = models.MetricStatusUnavailable
				point.ReasonCodes = []string{models.ReasonCurrencyConversionUnsupported}
			} else {
				point.Value = decPtr(total)
				for cur := range currencies {
					point.Currency = cur
				}
				if fullCoverage {
					point.Status = models.MetricStatusAvailable
				} else {
					point.Status = models.MetricStatusPartial
					point.ReasonCodes = []string{models.ReasonCoverageMissing}
				}
			}
		}
		if incomparable {
			point.ReasonCodes = uniqueStrings(append(point.ReasonCodes, models.ReasonTrendIncomparable))
		}
		points = append(points, point)
	}
	return points
}

func (e *metricEngine) buildCharts(metric models.MetricID, rows []models.ReportRow, trend []models.ChartPoint, covRef string) []models.ChartSpec {
	barTitle := chartBarTitle
	if metric == models.MetricQuantity {
		barTitle = "商品销量"
	}
	barPoints := make([]models.ChartPoint, 0, len(rows))
	for _, row := range rows {
		currency := row.Currency
		if metric == models.MetricQuantity {
			currency = ""
		}
		p := models.ChartPoint{
			Dimension:   row.EntityID,
			EntityID:    row.EntityID,
			MetricID:    metric,
			Currency:    currency,
			CoverageRef: row.CoverageRef,
			Status:      metricStatus(row, metric),
			Value:       metricValue(row, metric),
			ReasonCodes: metricReasons(row, metric),
		}
		barPoints = append(barPoints, p)
	}
	charts := []models.ChartSpec{{
		ChartID:     chartBarID,
		Kind:        models.ChartKindBar,
		Title:       barTitle,
		MetricIDs:   []models.MetricID{metric},
		Dimension:   dimensionEntity,
		DataRef:     dataRefTable,
		Points:      barPoints,
		CoverageRef: covRef,
	}}
	if e.hasTrend {
		charts = append(charts, models.ChartSpec{
			ChartID:     chartLineID,
			Kind:        models.ChartKindLine,
			Title:       chartLineTitle,
			MetricIDs:   []models.MetricID{metric},
			Dimension:   dimensionDate,
			DataRef:     dataRefTrend,
			Points:      trend,
			CoverageRef: covRef,
		})
	}
	return charts
}

func (e *metricEngine) evidence(covRef string) []models.EvidenceRef {
	return []models.EvidenceRef{
		{RefID: "snap:" + e.snap.ContractVersion, Kind: models.EvidenceKindSnapshot, ContractVersion: e.snap.ContractVersion, SnapshotReadAt: e.snap.ReadAt},
		{RefID: covRef, Kind: models.EvidenceKindCoverage, CoverageRef: covRef, ContractVersion: e.snap.ContractVersion},
		{RefID: "contract:" + e.snap.ContractVersion, Kind: models.EvidenceKindSourceContract, ContractVersion: e.snap.ContractVersion},
	}
}

func thresholdCandidates(rows []models.ReportRow, threshold *decimal.Decimal) []models.ReportRow {
	if threshold == nil {
		return nil
	}
	var out []models.ReportRow
	for _, row := range rows {
		if row.ThresholdHit {
			out = append(out, row)
		}
	}
	return out
}

func salesOf(row models.ReportRow) *decimal.Decimal {
	return metricValue(row, models.MetricSales)
}

func metricValue(row models.ReportRow, id models.MetricID) *decimal.Decimal {
	for _, m := range row.Metrics {
		if m.MetricID == id {
			return m.Value
		}
	}
	return nil
}

func metricStatus(row models.ReportRow, id models.MetricID) models.MetricStatus {
	for _, m := range row.Metrics {
		if m.MetricID == id {
			return m.Status
		}
	}
	return models.MetricStatusUnavailable
}

func metricReasons(row models.ReportRow, id models.MetricID) []string {
	for _, m := range row.Metrics {
		if m.MetricID == id {
			return m.ReasonCodes
		}
	}
	return nil
}

func sameEntitySet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}

func stringSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

func uniqueStrings(ids []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func decPtr(d decimal.Decimal) *decimal.Decimal {
	return &d
}
