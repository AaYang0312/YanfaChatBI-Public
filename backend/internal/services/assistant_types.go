package services

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // IANA database embedded so business time zones resolve identically on every host.

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
)

// Assistant v1 domain validation. These functions are pure: no Mongo, no SQL,
// no HTTP, and no model call. They never read the wall clock themselves; the
// caller injects the clock so the frozen fixture window is reproducible.

// assistantProductIdentityField is the missing-field path for a product that is
// not confirmed yet.
const assistantProductIdentityField = "product.product_id"

// AssistantFixtureClock returns the frozen fixture clock
// 2026-09-08T00:30:00+08:00. The source is a constant, so a parse failure is a
// programmer error in that constant.
func AssistantFixtureClock() time.Time {
	clock, err := time.Parse(time.RFC3339, models.AssistantFixtureClockRFC3339)
	if err != nil {
		panic("models.AssistantFixtureClockRFC3339 is not RFC3339: " + err.Error())
	}
	return clock
}

// AssistantLoadLocation resolves a business time zone, defaulting to
// Asia/Shanghai. An unknown zone fails with the invalid_enum code.
func AssistantLoadLocation(timezone string) (*time.Location, error) {
	name := strings.TrimSpace(timezone)
	if name == "" {
		name = models.AssistantDefaultTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "period.timezone",
			"period.timezone "+name+" is not a resolvable IANA time zone")
	}
	return loc, nil
}

// AssistantDefaultPeriod returns the frozen default window: the last
// AssistantDefaultWindowDays full business days before the clock's own business
// day, left-closed and right-open. With the fixture clock
// 2026-09-08T00:30:00+08:00 the window is 2026-09-01 through 2026-09-08
// exclusive, so a transaction at 2026-09-08T00:00:00+08:00 is outside it.
// Weekends are never skipped: a business day is a data-complete calendar day.
func AssistantDefaultPeriod(clock time.Time, timezone string) (models.Period, error) {
	loc, err := AssistantLoadLocation(timezone)
	if err != nil {
		return models.Period{}, err
	}
	end := assistantStartOfDay(clock.In(loc))
	start := end.AddDate(0, 0, -models.AssistantDefaultWindowDays)
	return models.Period{
		DateRange: models.DateRange{
			StartDate:        start.Format(models.AssistantDateLayout),
			EndDateExclusive: end.Format(models.AssistantDateLayout),
		},
		Timezone: loc.String(),
	}, nil
}

// AssistantDefaultTrendPeriod returns the seven full business days immediately
// before the main window's exclusive end, so the trend ends exactly where the
// main window ends and may therefore precede a short main window's start. The
// default main window 2026-09-01..2026-09-08 yields the same 2026-09-01..
// 2026-09-08 trend; an explicit overlap with the main window is allowed because
// the two windows are aggregated independently.
func AssistantDefaultTrendPeriod(main models.Period) (models.DateRange, error) {
	loc, err := AssistantLoadLocation(main.Timezone)
	if err != nil {
		return models.DateRange{}, err
	}
	endOfMain, err := assistantParseBusinessDate(main.EndDateExclusive, loc, "period.end_date_exclusive")
	if err != nil {
		return models.DateRange{}, err
	}
	end := assistantStartOfDay(endOfMain)
	start := end.AddDate(0, 0, -models.AssistantDefaultWindowDays)
	return models.DateRange{
		StartDate:        start.Format(models.AssistantDateLayout),
		EndDateExclusive: end.Format(models.AssistantDateLayout),
	}, nil
}

// ValidateAnalysisRequest normalizes and strictly validates an executable
// request. Every frozen default is applied (basis payment, timezone
// Asia/Shanghai, the seven-business-day window, product_analysis grouping);
// nothing required may be absent. On any error the returned request is the zero
// value, so no run object can exist.
func ValidateAnalysisRequest(access models.AccessContext, req models.AnalysisRequest, clock time.Time) (models.AnalysisRequest, error) {
	normalized, missing, err := assistantValidate(access, req, clock, true)
	if err != nil {
		return models.AnalysisRequest{}, err
	}
	if len(missing) > 0 {
		return models.AnalysisRequest{}, assistantMissingFieldError(missing)
	}
	return normalized, nil
}

// ValidateDraftRequest is the execution path for a draft: unresolved product
// identity fails with missing_field, and product text matching several
// candidates fails with ambiguous_product instead of picking the first one.
func ValidateDraftRequest(access models.AccessContext, draft models.DraftRequest, clock time.Time,
	productCandidates []models.Candidate) (models.AnalysisRequest, error) {

	request, missing, candidates, err := assistantDraftToRequest(access, draft, clock, productCandidates)
	if err != nil {
		return models.AnalysisRequest{}, err
	}
	if len(missing) > 0 {
		if assistantAmbiguousProduct(missing, candidates) {
			return models.AnalysisRequest{}, models.NewValidationError(models.ErrorCodeAmbiguousProduct,
				assistantProductIdentityField,
				fmt.Sprintf("product text matches %d candidates; confirm one product_id before creating a run",
					len(candidates)))
		}
		return models.AnalysisRequest{}, assistantMissingFieldError(missing)
	}
	normalized, err := ValidateAnalysisRequest(access, request, clock)
	if err != nil {
		return models.AnalysisRequest{}, err
	}
	return normalized, nil
}

// ResolveDraft is the resolve path. A draft that is structurally sound but not
// yet executable returns needs_input with missing_fields and candidates; it
// never carries metric values or a report ID, and it never reports a run state.
// Malformed input returns a *models.ValidationError instead.
func ResolveDraft(access models.AccessContext, draft models.DraftRequest, clock time.Time,
	productCandidates []models.Candidate) (models.Resolution, error) {

	request, missing, candidates, err := assistantDraftToRequest(access, draft, clock, productCandidates)
	if err != nil {
		return models.Resolution{}, err
	}
	if len(missing) > 0 || len(candidates) > 0 {
		resolution := models.Resolution{
			Status:        models.ResolveStatusNeedsInput,
			MissingFields: missing,
			Candidates:    candidates,
		}
		if err := ValidateResolution(resolution); err != nil {
			return models.Resolution{}, err
		}
		return resolution, nil
	}
	normalized, err := ValidateAnalysisRequest(access, request, clock)
	if err != nil {
		return models.Resolution{}, err
	}
	resolution := models.Resolution{Status: models.ResolveStatusResolved, Request: &normalized}
	if err := ValidateResolution(resolution); err != nil {
		return models.Resolution{}, err
	}
	return resolution, nil
}

// ValidateResolution enforces the shared-contract shape of a resolve response.
func ValidateResolution(resolution models.Resolution) error {
	if !resolution.Status.Valid() {
		return models.NewValidationError(models.ErrorCodeInvalidEnum, "status",
			"resolution status "+string(resolution.Status)+" is not resolved or needs_input")
	}
	switch resolution.Status {
	case models.ResolveStatusResolved:
		if resolution.Request == nil {
			return models.NewValidationError(models.ErrorCodeInvalidSchema, "request",
				"resolved resolution must carry one analysis request")
		}
		if len(resolution.MissingFields) > 0 || len(resolution.Candidates) > 0 {
			return models.NewValidationError(models.ErrorCodeInvalidSchema, "missing_fields",
				"resolved resolution must not carry missing fields or candidates")
		}
	case models.ResolveStatusNeedsInput:
		if resolution.Request != nil {
			return models.NewValidationError(models.ErrorCodeInvalidSchema, "request",
				"needs_input resolution must not carry an analysis request")
		}
		if len(resolution.MissingFields) == 0 && len(resolution.Candidates) == 0 {
			return models.NewValidationError(models.ErrorCodeInvalidSchema, "missing_fields",
				"needs_input resolution must list missing fields or candidates")
		}
		for _, candidate := range resolution.Candidates {
			if !candidate.EntityType.Valid() {
				return models.NewValidationError(models.ErrorCodeInvalidEnum, "candidates.entity_type",
					"candidate entity_type "+string(candidate.EntityType)+" is not supported")
			}
			if strings.TrimSpace(candidate.EntityID) == "" {
				return models.NewValidationError(models.ErrorCodeInvalidSchema, "candidates.entity_id",
					"candidate entity_id is required; only entity_id may be resubmitted")
			}
		}
	}
	return nil
}

// AuthorizeScope checks a requested range against the approved scope in the
// access context. An empty approved scope makes no decision: the service layer
// must resolve the approved range first. A requested entity outside it fails
// unauthorized_scope and is never silently dropped.
func AuthorizeScope(access models.AccessContext, scope models.Scope) error {
	if access.ApprovedScope.Empty() {
		return nil
	}
	if scope.Mode == models.ScopeModeAll {
		if !access.ApprovedScope.All {
			return models.NewValidationError(models.ErrorCodeUnauthorizedScope, "scope.mode",
				"scope.mode all is not the approved range for this chat")
		}
		return nil
	}
	for _, platformID := range scope.PlatformIDs {
		if !access.ApprovedScope.AllowsPlatform(platformID) {
			return models.NewValidationError(models.ErrorCodeUnauthorizedScope, "scope.platform_ids",
				"platform "+platformID+" is outside the approved scope")
		}
	}
	for _, shopID := range scope.ShopIDs {
		if !access.ApprovedScope.AllowsShop(shopID) {
			return models.NewValidationError(models.ErrorCodeUnauthorizedScope, "scope.shop_ids",
				"shop "+shopID+" is outside the approved scope")
		}
	}
	return nil
}

// FactQueryFromRequest projects one validated request onto the parameterized,
// enum-only source query. The result carries no SQL, schema, table, or column
// name.
func FactQueryFromRequest(req models.AnalysisRequest) models.FactQuery {
	query := models.FactQuery{
		Basis:   req.Basis,
		Window:  req.Period.DateRange,
		Scope:   req.Scope,
		Product: req.Product,
		Metrics: append([]models.MetricID(nil), req.Metrics...),
	}
	if req.TrendPeriod != nil {
		trend := *req.TrendPeriod
		query.TrendWindow = &trend
	}
	return query
}

// assistantDraftToRequest maps a draft onto the request shape, validates it in
// draft mode, and resolves product identity from the supplied candidates.
func assistantDraftToRequest(access models.AccessContext, draft models.DraftRequest, clock time.Time,
	productCandidates []models.Candidate) (models.AnalysisRequest, []string, []models.Candidate, error) {

	base := models.AnalysisRequest{
		SchemaVersion:      draft.SchemaVersion,
		RequestID:          draft.RequestID,
		Workflow:           draft.Workflow,
		Basis:              draft.Basis,
		TrendPeriod:        draft.TrendPeriod,
		GroupBy:            draft.GroupBy,
		Metrics:            draft.Metrics,
		LowMarginThreshold: draft.LowMarginThreshold,
		StreamID:           draft.StreamID,
		ParentRunID:        draft.ParentRunID,
	}
	if draft.Period != nil {
		base.Period = *draft.Period
	}
	if draft.Scope != nil {
		base.Scope = *draft.Scope
	}
	if draft.Product != nil {
		base.Product = *draft.Product
	}

	normalized, missing, err := assistantValidate(access, base, clock, false)
	if err != nil {
		return models.AnalysisRequest{}, nil, nil, err
	}

	if normalized.Workflow == models.WorkflowProductAnalysis && normalized.Product.ProductID == "" {
		candidates, err := assistantProductCandidates(productCandidates)
		if err != nil {
			return models.AnalysisRequest{}, nil, nil, err
		}
		switch {
		case len(candidates) == 1 && draft.ProductQuery != "":
			normalized.Product.ProductID = candidates[0].EntityID
			missing = assistantRemove(missing, assistantProductIdentityField)
		default:
			if !assistantContains(missing, assistantProductIdentityField) {
				missing = append(missing, assistantProductIdentityField)
			}
			if len(candidates) > 1 {
				return normalized, missing, candidates, nil
			}
			return normalized, missing, nil, nil
		}
	}
	return normalized, missing, nil, nil
}

// assistantValidate applies every shared-contract rule. In strict mode a
// required user-facing value that is absent is an error; in draft mode it is
// reported through the missing list so resolve can answer needs_input.
func assistantValidate(access models.AccessContext, in models.AnalysisRequest, clock time.Time,
	strict bool) (models.AnalysisRequest, []string, error) {

	out := in
	var missing []string

	if out.SchemaVersion != models.AssistantSchemaVersion {
		return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeInvalidSchema, "schema_version",
			"schema_version must be the integer 1")
	}
	if strings.TrimSpace(out.RequestID) == "" {
		return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeMissingField, "request_id",
			"request_id is required and is the caller-generated deduplication key")
	}
	out.RequestID = strings.TrimSpace(out.RequestID)

	out.Workflow = models.Workflow(assistantTrimString(string(out.Workflow)))
	switch {
	case out.Workflow == "":
		if strict {
			return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeMissingField, "workflow",
				"workflow is required")
		}
		missing = append(missing, "workflow")
	case !out.Workflow.Valid():
		return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "workflow",
			"workflow "+string(out.Workflow)+" is not product_analysis or sales_comparison")
	}

	out.Basis = models.Basis(assistantTrimString(string(out.Basis)))
	if out.Basis == "" {
		out.Basis = models.AssistantDefaultBasis
	}
	if !out.Basis.Valid() {
		return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "basis",
			"basis "+string(out.Basis)+" is not payment or outstock")
	}

	loc, err := AssistantLoadLocation(out.Period.Timezone)
	if err != nil {
		return models.AnalysisRequest{}, nil, err
	}
	out.Period.Timezone = loc.String()

	if assistantBlankRange(out.Period.DateRange) {
		def, err := AssistantDefaultPeriod(clock, out.Period.Timezone)
		if err != nil {
			return models.AnalysisRequest{}, nil, err
		}
		out.Period.DateRange = def.DateRange
	} else if err := assistantValidateDateRange(out.Period.DateRange, loc, "period"); err != nil {
		return models.AnalysisRequest{}, nil, err
	}

	if out.TrendPeriod == nil || assistantBlankRange(*out.TrendPeriod) {
		def, err := AssistantDefaultTrendPeriod(out.Period)
		if err != nil {
			return models.AnalysisRequest{}, nil, err
		}
		out.TrendPeriod = &def
	} else if err := assistantValidateDateRange(*out.TrendPeriod, loc, "trend_period"); err != nil {
		return models.AnalysisRequest{}, nil, err
	}

	out.GroupBy = models.GroupBy(assistantTrimString(string(out.GroupBy)))
	switch out.Workflow {
	case models.WorkflowProductAnalysis:
		if out.GroupBy == "" {
			out.GroupBy = models.GroupByShop
		} else if out.GroupBy != models.GroupByShop {
			return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "group_by",
				"product_analysis groups by shop only")
		}
	case models.WorkflowSalesComparison:
		switch {
		case out.GroupBy == "":
			if strict {
				return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeMissingField, "group_by",
					"sales_comparison requires an explicit group_by of platform or shop")
			}
			missing = append(missing, "group_by")
		case !out.GroupBy.Valid():
			return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "group_by",
				"group_by "+string(out.GroupBy)+" is not platform or shop")
		}
	}

	scope, scopeMissing, err := assistantValidateScope(out.Scope)
	if err != nil {
		return models.AnalysisRequest{}, nil, err
	}
	out.Scope = scope
	missing = append(missing, scopeMissing...)

	if out.Workflow == models.WorkflowSalesComparison && out.GroupBy == models.GroupByShop &&
		len(out.Scope.PlatformIDs) != 1 {
		return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeMissingField,
			"scope.platform_ids",
			"sales_comparison grouped by shop requires exactly one platform_id")
	}

	if err := AuthorizeScope(access, out.Scope); err != nil {
		return models.AnalysisRequest{}, nil, err
	}

	product, productMissing, err := assistantValidateProduct(out.Workflow, out.Product, strict)
	if err != nil {
		return models.AnalysisRequest{}, nil, err
	}
	out.Product = product
	missing = append(missing, productMissing...)

	metrics, metricsMissing, err := assistantValidateMetrics(out.Metrics, strict)
	if err != nil {
		return models.AnalysisRequest{}, nil, err
	}
	out.Metrics = metrics
	missing = append(missing, metricsMissing...)

	if out.LowMarginThreshold != nil {
		threshold := *out.LowMarginThreshold
		if threshold.IsNegative() || threshold.GreaterThan(decimal.NewFromInt(1)) {
			return models.AnalysisRequest{}, nil, models.NewValidationError(models.ErrorCodeInvalidSchema,
				"low_margin_threshold",
				"low_margin_threshold is a ratio string between 0 and 1; it is kept exact, never rounded")
		}
	}

	out.StreamID = assistantTrimString(out.StreamID)
	out.ParentRunID = assistantTrimString(out.ParentRunID)

	sort.Strings(missing)
	return out, assistantDedup(missing), nil
}

func assistantValidateScope(scope models.Scope) (models.Scope, []string, error) {
	out := scope
	var missing []string

	out.Mode = models.ScopeMode(assistantTrimString(string(out.Mode)))
	if out.Mode == "" {
		out.Mode = models.ScopeModeAll
	}
	if !out.Mode.Valid() {
		return models.Scope{}, nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "scope.mode",
			"scope.mode "+string(out.Mode)+" is not all or selected")
	}

	platformIDs, err := assistantNormalizeIDs(out.PlatformIDs, "scope.platform_ids")
	if err != nil {
		return models.Scope{}, nil, err
	}
	shopIDs, err := assistantNormalizeIDs(out.ShopIDs, "scope.shop_ids")
	if err != nil {
		return models.Scope{}, nil, err
	}
	out.PlatformIDs, out.ShopIDs = platformIDs, shopIDs

	if out.Mode == models.ScopeModeAll && (len(out.PlatformIDs) > 0 || len(out.ShopIDs) > 0) {
		field := "scope.platform_ids"
		if len(out.PlatformIDs) == 0 {
			field = "scope.shop_ids"
		}
		return models.Scope{}, nil, models.NewValidationError(models.ErrorCodeConflictingScope, field,
			"scope.mode all cannot carry an explicit platform or shop selection")
	}
	if out.Mode == models.ScopeModeSelected && len(out.PlatformIDs) == 0 && len(out.ShopIDs) == 0 {
		missing = append(missing, "scope")
	}
	return out, missing, nil
}

func assistantValidateProduct(workflow models.Workflow, product models.ProductSelection,
	strict bool) (models.ProductSelection, []string, error) {

	out := product
	var missing []string

	out.ProductID = assistantTrimString(out.ProductID)
	skuIDs, err := assistantNormalizeIDs(out.SkuIDs, "product.sku_ids")
	if err != nil {
		return models.ProductSelection{}, nil, err
	}
	out.SkuIDs = skuIDs

	if workflow == models.WorkflowProductAnalysis && out.ProductID == "" {
		if strict {
			return models.ProductSelection{}, nil, models.NewValidationError(models.ErrorCodeMissingField,
				assistantProductIdentityField,
				"the product workflow requires a confirmed product_id before a run can be created")
		}
		missing = append(missing, assistantProductIdentityField)
	}
	return out, missing, nil
}

func assistantValidateMetrics(metrics []models.MetricID, strict bool) ([]models.MetricID, []string, error) {
	var missing []string
	out := make([]models.MetricID, 0, len(metrics))
	seen := make(map[models.MetricID]bool, len(metrics))
	for _, metric := range metrics {
		name := models.MetricID(assistantTrimString(string(metric)))
		if name == "" {
			return nil, nil, models.NewValidationError(models.ErrorCodeInvalidSchema, "metrics",
				"metrics must not contain an empty identifier")
		}
		if !name.Valid() {
			return nil, nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "metrics",
				"metric "+string(name)+" is not a supported metric identifier")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		if strict {
			return nil, nil, models.NewValidationError(models.ErrorCodeMissingField, "metrics",
				"metrics must list at least one supported metric identifier")
		}
		missing = append(missing, "metrics")
	}
	return out, missing, nil
}

// assistantValidateDateRange enforces left-closed, right-open business dates in
// the strict YYYY-MM-DD layout. Reversed and empty ranges fail; no maximum
// period length is invented because Q07 is open.
func assistantValidateDateRange(dr models.DateRange, loc *time.Location, prefix string) error {
	startField := prefix + ".start_date"
	endField := prefix + ".end_date_exclusive"

	startText := assistantTrimString(dr.StartDate)
	endText := assistantTrimString(dr.EndDateExclusive)
	if startText == "" || endText == "" {
		field := startField
		if startText != "" {
			field = endField
		}
		return models.NewValidationError(models.ErrorCodeInvalidDateRange, field,
			"both "+startField+" and "+endField+" are required together")
	}
	start, err := assistantParseBusinessDate(startText, loc, startField)
	if err != nil {
		return err
	}
	end, err := assistantParseBusinessDate(endText, loc, endField)
	if err != nil {
		return err
	}
	if !start.Before(end) {
		return models.NewValidationError(models.ErrorCodeInvalidDateRange, endField,
			"date range must be non-empty and in order: "+startText+" is not before "+endText)
	}
	return nil
}

func assistantParseBusinessDate(value string, loc *time.Location, field string) (time.Time, error) {
	parsed, err := time.ParseInLocation(models.AssistantDateLayout, value, loc)
	if err == nil && parsed.Format(models.AssistantDateLayout) != value {
		err = errors.New("date carries a time component or is not zero-padded")
	}
	if err != nil {
		return time.Time{}, models.NewValidationError(models.ErrorCodeInvalidDateRange, field,
			value+" is not a business date in YYYY-MM-DD form")
	}
	return parsed, nil
}

func assistantStartOfDay(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
}

func assistantBlankRange(dr models.DateRange) bool {
	return strings.TrimSpace(dr.StartDate) == "" && strings.TrimSpace(dr.EndDateExclusive) == ""
}

func assistantProductCandidates(candidates []models.Candidate) ([]models.Candidate, error) {
	out := make([]models.Candidate, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if !candidate.EntityType.Valid() {
			return nil, models.NewValidationError(models.ErrorCodeInvalidEnum, "candidates.entity_type",
				"candidate entity_type "+string(candidate.EntityType)+" is not supported")
		}
		entityID := strings.TrimSpace(candidate.EntityID)
		if entityID == "" {
			return nil, models.NewValidationError(models.ErrorCodeInvalidSchema, "candidates.entity_id",
				"candidate entity_id is required; only entity_id may be resubmitted")
		}
		if candidate.EntityType != models.EntityTypeProduct {
			continue
		}
		if seen[entityID] {
			continue
		}
		seen[entityID] = true
		candidate.EntityID = entityID
		out = append(out, candidate)
	}
	return out, nil
}

func assistantAmbiguousProduct(missing []string, candidates []models.Candidate) bool {
	return len(candidates) > 1 && assistantContains(missing, assistantProductIdentityField)
}

func assistantMissingFieldError(missing []string) error {
	field := strings.Join(missing, ",")
	return models.NewValidationError(models.ErrorCodeMissingField, field,
		"required request content is missing: "+field)
}

func assistantNormalizeIDs(values []string, field string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		trimmed := assistantTrimString(value)
		if trimmed == "" {
			return nil, models.NewValidationError(models.ErrorCodeInvalidSchema, field,
				field+" must not contain an empty identifier")
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out, nil
}

func assistantTrimString(value string) string { return strings.TrimSpace(value) }

func assistantContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assistantRemove(values []string, want string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != want {
			out = append(out, value)
		}
	}
	return out
}

func assistantDedup(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
