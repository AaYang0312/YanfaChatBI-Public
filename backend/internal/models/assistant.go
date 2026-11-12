// Package models holds the normative Assistant v1 domain types.
//
// Field names and JSON keys here are the shared contract from
// the shared assistant contract; later changes must not create
// parallel types with the same meaning.
package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// AssistantSchemaVersion is the only request schema version Assistant v1 accepts.
const AssistantSchemaVersion = 1

// AssistantDateLayout is the wire layout for business dates. Assistant v1
// requests carry calendar business dates only; instant boundaries are derived
// from the business date plus the period timezone.
const AssistantDateLayout = "2006-01-02"

// Frozen fixture clock and default window (shared contract). The fixture clock
// is 2026-09-08T00:30:00+08:00, so the default seven-full-business-day window is
// 2026-09-01 (inclusive) through 2026-09-08 (exclusive). "Business day" means a
// data-complete calendar day: the frozen window spans Sat 2026-09-05 and Sun
// 2026-09-06, so weekends are never skipped.
const (
	AssistantFixtureClockRFC3339   = "2026-09-08T00:30:00+08:00"
	AssistantDefaultTimezone       = "Asia/Shanghai"
	AssistantDefaultWindowDays     = 7
	AssistantDefaultStartDate      = "2026-09-01"
	AssistantDefaultEndDateExclu   = "2026-09-08"
	AssistantDefaultBasis          = Basis("payment")
	AssistantDefaultTrendStartDate = "2026-08-25"
)

// Workflow selects the frozen Assistant v1 business flow.
type Workflow string

const (
	WorkflowProductAnalysis Workflow = "product_analysis"
	WorkflowSalesComparison Workflow = "sales_comparison"
)

// Valid reports whether the value is one of the two frozen workflows.
func (w Workflow) Valid() bool {
	return w == WorkflowProductAnalysis || w == WorkflowSalesComparison
}

// Basis is the accounting basis for quantity, sales, and cost.
type Basis string

const (
	BasisPayment  Basis = "payment"
	BasisOutstock Basis = "outstock"
)

// Valid reports whether the basis is a frozen value.
func (b Basis) Valid() bool {
	return b == BasisPayment || b == BasisOutstock
}

// ScopeMode is the requested entity range.
type ScopeMode string

const (
	ScopeModeAll      ScopeMode = "all"
	ScopeModeSelected ScopeMode = "selected"
)

// Valid reports whether the mode is a frozen value.
func (m ScopeMode) Valid() bool {
	return m == ScopeModeAll || m == ScopeModeSelected
}

// GroupBy is the frozen grouping dimension.
type GroupBy string

const (
	GroupByPlatform GroupBy = "platform"
	GroupByShop     GroupBy = "shop"
)

// Valid reports whether the grouping is a frozen value.
func (g GroupBy) Valid() bool {
	return g == GroupByPlatform || g == GroupByShop
}

// MetricID is a supported metric identifier. Metric identifiers are enum values
// only; they are never interpolated into SQL.
//
// The set is the frozen numeric vocabulary of the shared contract: quantity,
// sales, and cost are fact-row fields; margin, margin_rate, and avg_price are
// the defined derived metrics; refund is reported separately and never netted
// out of margin. A shop payment total is deliberately absent: it must not be
// conflated with product sales, and its source semantics are open in Q02.
type MetricID string

const (
	MetricQuantity     MetricID = "quantity"
	MetricSales        MetricID = "sales"
	MetricCost         MetricID = "cost"
	MetricMargin       MetricID = "margin"
	MetricMarginRate   MetricID = "margin_rate"
	MetricAveragePrice MetricID = "avg_price"
	MetricRefund       MetricID = "refund"
)

// SupportedMetrics is the closed metric allow-list, in report order.
func SupportedMetrics() []MetricID {
	return []MetricID{
		MetricQuantity,
		MetricSales,
		MetricCost,
		MetricMargin,
		MetricMarginRate,
		MetricAveragePrice,
		MetricRefund,
	}
}

// SupportedBases is the closed basis allow-list.
func SupportedBases() []Basis {
	return []Basis{BasisPayment, BasisOutstock}
}

// Valid reports whether the metric identifier is supported.
func (m MetricID) Valid() bool {
	for _, want := range SupportedMetrics() {
		if m == want {
			return true
		}
	}
	return false
}

// RunStatus is the run lifecycle state. needs_input is never a RunStatus: it is
// a resolve response only, so an ambiguous request cannot leave a running run.
type RunStatus string

const (
	RunStatusCreated     RunStatus = "created"
	RunStatusRunning     RunStatus = "running"
	RunStatusCompleted   RunStatus = "completed"
	RunStatusFailed      RunStatus = "failed"
	RunStatusCancelled   RunStatus = "cancelled"
	RunStatusInterrupted RunStatus = "interrupted"
)

// SupportedRunStatuses is the frozen status set.
func SupportedRunStatuses() []RunStatus {
	return []RunStatus{
		RunStatusCreated,
		RunStatusRunning,
		RunStatusCompleted,
		RunStatusFailed,
		RunStatusCancelled,
		RunStatusInterrupted,
	}
}

// Valid reports whether the status is a frozen run state.
func (s RunStatus) Valid() bool {
	for _, want := range SupportedRunStatuses() {
		if s == want {
			return true
		}
	}
	return false
}

// IsTerminal reports whether a late callback must be rejected for this status.
func (s RunStatus) IsTerminal() bool {
	return s == RunStatusCompleted || s == RunStatusFailed ||
		s == RunStatusCancelled || s == RunStatusInterrupted
}

// RunOutcome is the business outcome of a completed run. It is separate from
// RunStatus so "empty" and "unavailable" are never both "failed".
type RunOutcome string

const (
	OutcomeComplete    RunOutcome = "complete"
	OutcomePartial     RunOutcome = "partial"
	OutcomeEmpty       RunOutcome = "empty"
	OutcomeUnavailable RunOutcome = "unavailable"
)

// SupportedRunOutcomes is the frozen completed-outcome set.
func SupportedRunOutcomes() []RunOutcome {
	return []RunOutcome{
		OutcomeComplete,
		OutcomePartial,
		OutcomeEmpty,
		OutcomeUnavailable,
	}
}

// Valid reports whether the outcome is a frozen value.
func (o RunOutcome) Valid() bool {
	for _, want := range SupportedRunOutcomes() {
		if o == want {
			return true
		}
	}
	return false
}

// MetricStatus is the per-metric availability state.
type MetricStatus string

const (
	MetricStatusAvailable   MetricStatus = "available"
	MetricStatusPartial     MetricStatus = "partial"
	MetricStatusUnavailable MetricStatus = "unavailable"
)

// SupportedMetricStatuses is the frozen metric status set.
func SupportedMetricStatuses() []MetricStatus {
	return []MetricStatus{
		MetricStatusAvailable,
		MetricStatusPartial,
		MetricStatusUnavailable,
	}
}

// Valid reports whether the metric status is a frozen value.
func (s MetricStatus) Valid() bool {
	for _, want := range SupportedMetricStatuses() {
		if s == want {
			return true
		}
	}
	return false
}

// ResolveStatus is the resolve-response state. needs_input exists only here.
type ResolveStatus string

const (
	ResolveStatusResolved   ResolveStatus = "resolved"
	ResolveStatusNeedsInput ResolveStatus = "needs_input"
)

// Valid reports whether the resolve status is a frozen value.
func (s ResolveStatus) Valid() bool {
	return s == ResolveStatusResolved || s == ResolveStatusNeedsInput
}

// CoverageStatus is the per-row coverage evidence state for an entity, basis,
// window, and metric. The names mirror the covered / missing / stale / unknown
// entity states of the shared contract.
type CoverageStatus string

const (
	CoverageStatusCovered CoverageStatus = "covered"
	CoverageStatusMissing CoverageStatus = "missing"
	CoverageStatusStale   CoverageStatus = "stale"
	CoverageStatusUnknown CoverageStatus = "unknown"
)

// SupportedCoverageStatuses is the frozen coverage-state set.
func SupportedCoverageStatuses() []CoverageStatus {
	return []CoverageStatus{
		CoverageStatusCovered,
		CoverageStatusMissing,
		CoverageStatusStale,
		CoverageStatusUnknown,
	}
}

// Valid reports whether the coverage state is a frozen value.
func (c CoverageStatus) Valid() bool {
	for _, want := range SupportedCoverageStatuses() {
		if c == want {
			return true
		}
	}
	return false
}

// BundleRole is the frozen bundle/gift classification of a fact row.
type BundleRole string

const (
	BundleRoleNone   BundleRole = "none"
	BundleRoleParent BundleRole = "bundle_parent"
	BundleRoleChild  BundleRole = "bundle_child"
	BundleRoleGift   BundleRole = "gift"
)

// SupportedBundleRoles is the frozen bundle-role set.
func SupportedBundleRoles() []BundleRole {
	return []BundleRole{
		BundleRoleNone,
		BundleRoleParent,
		BundleRoleChild,
		BundleRoleGift,
	}
}

// Valid reports whether the bundle role is a frozen value.
func (b BundleRole) Valid() bool {
	for _, want := range SupportedBundleRoles() {
		if b == want {
			return true
		}
	}
	return false
}

// ValidationErrorCode is the machine-readable error code returned to callers.
type ValidationErrorCode string

const (
	ErrorCodeInvalidSchema         ValidationErrorCode = "invalid_schema"
	ErrorCodeInvalidDateRange      ValidationErrorCode = "invalid_date_range"
	ErrorCodeInvalidEnum           ValidationErrorCode = "invalid_enum"
	ErrorCodeUnknownField          ValidationErrorCode = "unknown_field"
	ErrorCodeAmbiguousProduct      ValidationErrorCode = "ambiguous_product"
	ErrorCodeMissingField          ValidationErrorCode = "missing_field"
	ErrorCodeConflictingScope      ValidationErrorCode = "conflicting_scope"
	ErrorCodeUnauthorizedScope     ValidationErrorCode = "unauthorized_scope"
	ErrorCodeRequestConflict       ValidationErrorCode = "request_conflict"
	ErrorCodeCapabilityUnavailable ValidationErrorCode = "capability_unavailable"
	ErrorCodeSourceUnavailable     ValidationErrorCode = "source_unavailable"
	ErrorCodeTimeout               ValidationErrorCode = "timeout"
)

// SupportedValidationErrorCodes is the frozen error-code list from the shared
// contract, in contract order.
func SupportedValidationErrorCodes() []ValidationErrorCode {
	return []ValidationErrorCode{
		ErrorCodeInvalidSchema,
		ErrorCodeInvalidDateRange,
		ErrorCodeInvalidEnum,
		ErrorCodeUnknownField,
		ErrorCodeAmbiguousProduct,
		ErrorCodeMissingField,
		ErrorCodeConflictingScope,
		ErrorCodeUnauthorizedScope,
		ErrorCodeRequestConflict,
		ErrorCodeCapabilityUnavailable,
		ErrorCodeSourceUnavailable,
		ErrorCodeTimeout,
	}
}

// HTTPStatus maps a code to the frozen HTTP status. Syntax is 400, semantic
// validation is 422, identity is 401, a foreign or absent Chat/run is 404,
// unauthorized scope is 403, and a reused request ID with a different
// fingerprint is 409. capability_unavailable, source_unavailable, and timeout
// are run-level results, not request-validation responses, so they are
// deliberately unmapped (ok is false); the HTTP layer must not invent a status.
func (c ValidationErrorCode) HTTPStatus() (int, bool) {
	switch c {
	case ErrorCodeUnknownField, ErrorCodeInvalidSchema:
		return 400, true
	case ErrorCodeInvalidDateRange, ErrorCodeInvalidEnum, ErrorCodeMissingField,
		ErrorCodeConflictingScope, ErrorCodeAmbiguousProduct:
		return 422, true
	case ErrorCodeUnauthorizedScope:
		return 403, true
	case ErrorCodeRequestConflict:
		return 409, true
	default:
		return 0, false
	}
}

// ValidationError carries one machine code plus the offending field path. The
// message is safe to return: it never contains SQL text, credentials, or a
// result payload.
type ValidationError struct {
	Code    ValidationErrorCode `json:"code"`
	Field   string              `json:"field,omitempty"`
	Message string              `json:"message"`
}

// NewValidationError builds a *ValidationError.
func NewValidationError(code ValidationErrorCode, field, message string) *ValidationError {
	return &ValidationError{Code: code, Field: field, Message: message}
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s (field %q)", e.Code, e.Message, e.Field)
}

// AsValidationError reports whether err is a *ValidationError.
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}

// ApprovedScope is the server-side approved data range for one Chat connection.
// An empty scope (nothing set, All false) means the caller has not supplied an
// approval yet; request validation then makes no authorization decision and the
// service layer must resolve the approved range before execution.
type ApprovedScope struct {
	// All is true when every platform and shop in this connection is approved.
	All         bool     `json:"all"`
	PlatformIDs []string `json:"platform_ids,omitempty"`
	ShopIDs     []string `json:"shop_ids,omitempty"`
}

// Empty reports whether no approval has been provided.
func (s ApprovedScope) Empty() bool {
	return !s.All && len(s.PlatformIDs) == 0 && len(s.ShopIDs) == 0
}

// AllowsPlatform reports whether the platform is approved. Unknown never
// becomes allowed.
func (s ApprovedScope) AllowsPlatform(platformID string) bool {
	if s.All {
		return true
	}
	for _, id := range s.PlatformIDs {
		if id == platformID {
			return true
		}
	}
	return false
}

// AllowsShop reports whether the shop is approved. Unknown never becomes
// allowed.
func (s ApprovedScope) AllowsShop(shopID string) bool {
	if s.All {
		return true
	}
	for _, id := range s.ShopIDs {
		if id == shopID {
			return true
		}
	}
	return false
}

// AccessContext is built by the server only. Client-supplied JSON with these
// field names is never bound into it.
type AccessContext struct {
	OwnerID       string        `json:"-"`
	ChatID        string        `json:"-"`
	ConnectionID  string        `json:"-"`
	ApprovedScope ApprovedScope `json:"-"`
}

// DateRange is a left-closed, right-open business-day range.
type DateRange struct {
	StartDate        string `json:"start_date"`
	EndDateExclusive string `json:"end_date_exclusive"`
}

// Period is the main analysis window with its timezone.
type Period struct {
	DateRange
	Timezone string `json:"timezone"`
}

// Scope is the requested entity range.
type Scope struct {
	Mode        ScopeMode `json:"mode"`
	PlatformIDs []string  `json:"platform_ids,omitempty"`
	ShopIDs     []string  `json:"shop_ids,omitempty"`
}

// ProductSelection is a confirmed product identity. It never carries query
// text: the execution stage must not retain ambiguity.
type ProductSelection struct {
	ProductID string   `json:"product_id"`
	SkuIDs    []string `json:"sku_ids,omitempty"`
}

// AnalysisRequest is the resolved, executable Assistant v1 request. The JSON
// field set is exactly the shared contract; the wire layer rejects everything
// else.
type AnalysisRequest struct {
	SchemaVersion      int              `json:"schema_version"`
	RequestID          string           `json:"request_id"`
	Workflow           Workflow         `json:"workflow"`
	Basis              Basis            `json:"basis"`
	Period             Period           `json:"period"`
	TrendPeriod        *DateRange       `json:"trend_period,omitempty"`
	Scope              Scope            `json:"scope"`
	Product            ProductSelection `json:"product"`
	GroupBy            GroupBy          `json:"group_by"`
	Metrics            []MetricID       `json:"metrics"`
	LowMarginThreshold *decimal.Decimal `json:"low_margin_threshold,omitempty"`
	StreamID           string           `json:"stream_id,omitempty"`
	ParentRunID        string           `json:"parent_run_id,omitempty"`
}

// DraftRequest is the unresolved natural-language request that may carry
// product query text. It has no client user ID, SQL, credential, column-name,
// or schema-name field.
type DraftRequest struct {
	SchemaVersion      int               `json:"schema_version"`
	RequestID          string            `json:"request_id"`
	Workflow           Workflow          `json:"workflow"`
	Basis              Basis             `json:"basis,omitempty"`
	Period             *Period           `json:"period,omitempty"`
	TrendPeriod        *DateRange        `json:"trend_period,omitempty"`
	Scope              *Scope            `json:"scope,omitempty"`
	ProductQuery       string            `json:"product_query"`
	Product            *ProductSelection `json:"product,omitempty"`
	GroupBy            GroupBy           `json:"group_by,omitempty"`
	Metrics            []MetricID        `json:"metrics,omitempty"`
	LowMarginThreshold *decimal.Decimal  `json:"low_margin_threshold,omitempty"`
	StreamID           string            `json:"stream_id,omitempty"`
	ParentRunID        string            `json:"parent_run_id,omitempty"`
}

// EntityType is the kind of entity a resolution candidate refers to.
type EntityType string

const (
	EntityTypeProduct  EntityType = "product"
	EntityTypeSku      EntityType = "sku"
	EntityTypeShop     EntityType = "shop"
	EntityTypePlatform EntityType = "platform"
)

// SupportedEntityTypes is the frozen candidate entity-type set.
func SupportedEntityTypes() []EntityType {
	return []EntityType{
		EntityTypeProduct,
		EntityTypeSku,
		EntityTypeShop,
		EntityTypePlatform,
	}
}

// Valid reports whether the entity type is a frozen value.
func (e EntityType) Valid() bool {
	for _, want := range SupportedEntityTypes() {
		if e == want {
			return true
		}
	}
	return false
}

// Candidate is one resolution candidate. Only EntityID may be resubmitted by a
// client; Label and the parent identifiers are display context.
type Candidate struct {
	EntityType EntityType `json:"entity_type"`
	EntityID   string     `json:"entity_id"`
	Label      string     `json:"label"`
	PlatformID string     `json:"platform_id,omitempty"`
	ShopID     string     `json:"shop_id,omitempty"`
}

// Resolution is either a resolved request or a needs_input response carrying
// missing fields and candidates. needs_input never carries metric values or a
// report ID: this type has no field that can hold either.
type Resolution struct {
	Status        ResolveStatus    `json:"status"`
	Request       *AnalysisRequest `json:"request,omitempty"`
	MissingFields []string         `json:"missing_fields,omitempty"`
	Candidates    []Candidate      `json:"candidates,omitempty"`
}

// MetricValue is one metric's published value. Value is a decimal string in
// JSON; null is never the same as "0".
type MetricValue struct {
	MetricID    MetricID         `json:"metric_id"`
	Status      MetricStatus     `json:"status"`
	Value       *decimal.Decimal `json:"value"`
	Unit        string           `json:"unit,omitempty"`
	Currency    string           `json:"currency,omitempty"`
	ReasonCodes []string         `json:"reason_codes,omitempty"`
	CoverageRef string           `json:"coverage_ref,omitempty"`
}

// FactRow is one approved-contract fact row inside a snapshot.
type FactRow struct {
	EntityID         string          `json:"entity_id"`
	Basis            Basis           `json:"basis"`
	BusinessDate     string          `json:"business_date"`
	Quantity         decimal.Decimal `json:"quantity"`
	Sales            decimal.Decimal `json:"sales"`
	Cost             decimal.Decimal `json:"cost"`
	CostVerified     bool            `json:"cost_verified"`
	Currency         string          `json:"currency"`
	CoverageStatus   CoverageStatus  `json:"coverage_status"`
	BundleRole       BundleRole      `json:"bundle_role"`
	GiftCostVerified bool            `json:"gift_cost_verified"`
}

// FactSnapshot is the read-only, consistent snapshot the metric engine consumes.
type FactSnapshot struct {
	Basis           Basis     `json:"basis"`
	Window          DateRange `json:"window"`
	ReadAt          time.Time `json:"read_at"`
	DataAsOf        time.Time `json:"data_as_of"`
	ContractVersion string    `json:"contract_version"`
	Rows            []FactRow `json:"rows"`
}

// FactQuery is the parameterized, enum-only source query derived from one
// validated AnalysisRequest. It carries no SQL, schema, table, or column name.
type FactQuery struct {
	Basis       Basis            `json:"basis"`
	Window      DateRange        `json:"window"`
	TrendWindow *DateRange       `json:"trend_window,omitempty"`
	Scope       Scope            `json:"scope"`
	Product     ProductSelection `json:"product"`
	Metrics     []MetricID       `json:"metrics"`
}

// SourceCapabilities is the approved source contract description. Unknown
// capability never becomes enabled: the lists are explicit.
type SourceCapabilities struct {
	ContractVersion  string     `json:"contract_version"`
	SupportedBases   []Basis    `json:"supported_bases,omitempty"`
	SupportedMetrics []MetricID `json:"supported_metrics,omitempty"`
	IdentityDomain   string     `json:"identity_domain,omitempty"`
	Currencies       []string   `json:"currencies,omitempty"`
	CoverageEvidence bool       `json:"coverage_evidence"`
}

// SupportsBasis reports whether the basis is explicitly supported.
func (c SourceCapabilities) SupportsBasis(b Basis) bool {
	for _, want := range c.SupportedBases {
		if want == b {
			return true
		}
	}
	return false
}

// SupportsMetric reports whether the metric is explicitly supported.
func (c SourceCapabilities) SupportsMetric(m MetricID) bool {
	for _, want := range c.SupportedMetrics {
		if want == m {
			return true
		}
	}
	return false
}

// AssistantMarginRefundLabel is the frozen margin annotation. Product-cost
// margin never nets refunds; the published label is exactly this string.
const AssistantMarginRefundLabel = "未扣退款"

// Frozen reason codes published on MetricValue / CoverageEntity. Agents must
// not invent a customer threshold or a display-rounding code here.
const (
	ReasonDivideByZero                  = "divide_by_zero"
	ReasonCostUnverified                = "cost_unverified"
	ReasonBundleUnverified              = "bundle_unverified"
	ReasonCoverageMissing               = "coverage_missing"
	ReasonCoverageStale                 = "coverage_stale"
	ReasonCoverageUnknown               = "coverage_unknown"
	ReasonCapabilityUnavailable         = "capability_unavailable"
	ReasonCurrencyConversionUnsupported = "currency_conversion_unsupported"
	ReasonProductAttributionUnavailable = "product_attribution_unavailable"
	ReasonBasisUnavailable              = "basis_unavailable"
	ReasonTrendIncomparable             = "trend_incomparable"
)

// EvidenceRef.Kind values. They name a traceable handle, never a raw order.
const (
	EvidenceKindMetric         = "metric"
	EvidenceKindCoverage       = "coverage"
	EvidenceKindSourceContract = "source_contract"
	EvidenceKindSnapshot       = "snapshot"
)

// ChartKind is the frozen chart template. Only bar and line are allowed.
type ChartKind string

const (
	ChartKindBar  ChartKind = "bar"
	ChartKindLine ChartKind = "line"
)

// SupportedChartKinds is the frozen chart-kind set.
func SupportedChartKinds() []ChartKind {
	return []ChartKind{ChartKindBar, ChartKindLine}
}

// Valid reports whether the kind is bar or line.
func (k ChartKind) Valid() bool {
	return k == ChartKindBar || k == ChartKindLine
}

// CoverageEntity is one entity in a coverage bucket.
type CoverageEntity struct {
	EntityType  EntityType     `json:"entity_type"`
	EntityID    string         `json:"entity_id"`
	PlatformID  string         `json:"platform_id,omitempty"`
	ShopID      string         `json:"shop_id,omitempty"`
	Status      CoverageStatus `json:"status"`
	CoverageRef string         `json:"coverage_ref,omitempty"`
	ReasonCodes []string       `json:"reason_codes,omitempty"`
	Currency    string         `json:"currency,omitempty"`
}

// Coverage is the report-level coverage picture: requested, expected, covered,
// missing, stale, and unknown entities, plus included/excluded subsets,
// interval gaps, metric capability, evidence, and data_as_of.
type Coverage struct {
	Requested []CoverageEntity `json:"requested"`
	Expected  []CoverageEntity `json:"expected"`
	Covered   []CoverageEntity `json:"covered"`
	Missing   []CoverageEntity `json:"missing"`
	Stale     []CoverageEntity `json:"stale"`
	Unknown   []CoverageEntity `json:"unknown"`
	Included  []CoverageEntity `json:"included"`
	Excluded  []CoverageEntity `json:"excluded"`
	Gaps      []DateRange      `json:"gaps,omitempty"`
	Metrics   []MetricID       `json:"metrics,omitempty"`
	Evidence  []EvidenceRef    `json:"evidence,omitempty"`
	DataAsOf  time.Time        `json:"data_as_of"`
	Ref       string           `json:"coverage_ref,omitempty"`
}

// EvidenceRef is a traceable handle to a metric, coverage slice, source
// contract, or snapshot. It does not expose raw orders or credentials.
type EvidenceRef struct {
	RefID           string    `json:"ref_id"`
	Kind            string    `json:"kind"`
	MetricID        MetricID  `json:"metric_id,omitempty"`
	CoverageRef     string    `json:"coverage_ref,omitempty"`
	ContractVersion string    `json:"contract_version,omitempty"`
	SnapshotReadAt  time.Time `json:"snapshot_read_at,omitempty"`
}

// ChartPoint is one deterministic chart or trend point. Value is a decimal
// string or null; null is never the same as "0".
type ChartPoint struct {
	Dimension    string           `json:"dimension"`
	EntityID     string           `json:"entity_id,omitempty"`
	BusinessDate string           `json:"business_date,omitempty"`
	MetricID     MetricID         `json:"metric_id"`
	Value        *decimal.Decimal `json:"value"`
	Status       MetricStatus     `json:"status"`
	Currency     string           `json:"currency,omitempty"`
	CoverageRef  string           `json:"coverage_ref,omitempty"`
	ReasonCodes  []string         `json:"reason_codes,omitempty"`
}

// ChartSpec is a bar or line template bound to this report's own data.
// It never carries SQL or a model-invented series.
type ChartSpec struct {
	ChartID         string       `json:"chart_id"`
	Kind            ChartKind    `json:"kind"`
	Title           string       `json:"title"`
	MetricIDs       []MetricID   `json:"metric_ids"`
	Dimension       string       `json:"dimension"`
	DataRef         string       `json:"data_ref"`
	Points          []ChartPoint `json:"points"`
	CoverageRef     string       `json:"coverage_ref,omitempty"`
	ExplanationRefs []string     `json:"explanation_refs,omitempty"`
}

// ReportRow is one grouped entity in the metric table.
type ReportRow struct {
	EntityType   EntityType    `json:"entity_type"`
	EntityID     string        `json:"entity_id"`
	PlatformID   string        `json:"platform_id,omitempty"`
	ShopID       string        `json:"shop_id,omitempty"`
	Currency     string        `json:"currency,omitempty"`
	Rank         int           `json:"rank,omitempty"`
	ThresholdHit bool          `json:"threshold_hit,omitempty"`
	Metrics      []MetricValue `json:"metrics"`
	CoverageRef  string        `json:"coverage_ref,omitempty"`
	ReasonCodes  []string      `json:"reason_codes,omitempty"`
}

// ReportTable is the deterministic metric table driven by the same snapshot
// as the charts.
type ReportTable struct {
	Dimension string        `json:"dimension"`
	Rows      []ReportRow   `json:"rows"`
	Totals    []MetricValue `json:"totals,omitempty"`
}

// RefundSummary is the independent refund picture. Product-cost margin is
// never net of these amounts.
type RefundSummary struct {
	Label            string           `json:"label"`
	Amount           *decimal.Decimal `json:"amount"`
	Status           MetricStatus     `json:"status"`
	Currency         string           `json:"currency,omitempty"`
	OccurrenceWindow DateRange        `json:"occurrence_window"`
	Level            EntityType       `json:"level,omitempty"`
	EntityID         string           `json:"entity_id,omitempty"`
	ReasonCodes      []string         `json:"reason_codes,omitempty"`
	ProductRefund    *MetricValue     `json:"product_refund,omitempty"`
}

// AnalysisReport is the deterministic metric result. ReportID equals run_id.
// The type holds no run-persistence fields: no owner, fingerprint, message
// id, stage, revision, executing instance, cancellation marker, or ciphertext.
type AnalysisReport struct {
	ReportID           string            `json:"report_id"`
	SchemaVersion      int               `json:"schema_version"`
	Workflow           Workflow          `json:"workflow"`
	Basis              Basis             `json:"basis"`
	RequestedScope     Scope             `json:"requested_scope"`
	ActualScope        Scope             `json:"actual_scope"`
	Window             Period            `json:"window"`
	WindowStart        time.Time         `json:"window_start"`
	WindowEndExclusive time.Time         `json:"window_end_exclusive"`
	TrendWindow        *DateRange        `json:"trend_window,omitempty"`
	ContractVersion    string            `json:"contract_version"`
	Outcome            RunOutcome        `json:"outcome"`
	Coverage           Coverage          `json:"coverage"`
	Totals             []MetricValue     `json:"totals"`
	Table              ReportTable       `json:"table"`
	Trend              []ChartPoint      `json:"trend,omitempty"`
	Candidates         []ReportRow       `json:"candidates,omitempty"`
	Charts             []ChartSpec       `json:"charts,omitempty"`
	Refunds            RefundSummary     `json:"refunds"`
	Evidence           []EvidenceRef     `json:"evidence,omitempty"`
	DataAsOf           time.Time         `json:"data_as_of"`
	MarginLabel        string            `json:"margin_label"`
	ExplanationStatus  ExplanationStatus `json:"explanation_status,omitempty"`
	Explanations       []Explanation     `json:"explanations,omitempty"`
}

// MarshalJSON is the report JSON boundary. Every contract-required list is an
// array: an empty bucket marshals as [], never null, so consumers reading a
// length never see null. Optional (omitempty) lists stay absent when unset,
// and a null metric value stays null — a missing value never becomes "0".
// Times marshal in UTC so a persisted round trip does not re-shape instants.
//
// Marshaling never writes to the caller's data: slice headers are normalized
// on the receiver copy, and element structs are rewritten only inside a
// shallow element copy taken when at least one element needs it. Decimal
// pointers are shared, never copied or rewritten.
func (r AnalysisReport) MarshalJSON() ([]byte, error) {
	r.Coverage.Requested = contractList(r.Coverage.Requested)
	r.Coverage.Expected = contractList(r.Coverage.Expected)
	r.Coverage.Covered = contractList(r.Coverage.Covered)
	r.Coverage.Missing = contractList(r.Coverage.Missing)
	r.Coverage.Stale = contractList(r.Coverage.Stale)
	r.Coverage.Unknown = contractList(r.Coverage.Unknown)
	r.Coverage.Included = contractList(r.Coverage.Included)
	r.Coverage.Excluded = contractList(r.Coverage.Excluded)
	r.Totals = contractList(r.Totals)
	r.Table.Rows = contractList(r.Table.Rows)
	r.Charts = normalizeElements(r.Charts,
		func(chart *ChartSpec) bool { return chart.MetricIDs == nil || chart.Points == nil },
		func(chart *ChartSpec) {
			chart.MetricIDs = contractList(chart.MetricIDs)
			chart.Points = contractList(chart.Points)
		})
	r.Table.Rows = normalizeElements(r.Table.Rows,
		func(row *ReportRow) bool { return row.Metrics == nil },
		func(row *ReportRow) { row.Metrics = contractList(row.Metrics) })
	r.Explanations = normalizeElements(r.Explanations,
		func(explanation *Explanation) bool { return explanation.EvidenceRefs == nil },
		func(explanation *Explanation) { explanation.EvidenceRefs = contractList(explanation.EvidenceRefs) })
	r.WindowStart = r.WindowStart.UTC()
	r.WindowEndExclusive = r.WindowEndExclusive.UTC()
	r.DataAsOf = r.DataAsOf.UTC()
	r.Coverage.DataAsOf = r.Coverage.DataAsOf.UTC()
	r.Evidence = normalizeElements(r.Evidence,
		func(evidence *EvidenceRef) bool { return evidence.SnapshotReadAt.Location() != time.UTC },
		func(evidence *EvidenceRef) { evidence.SnapshotReadAt = evidence.SnapshotReadAt.UTC() })
	type reportJSON AnalysisReport
	return json.Marshal(reportJSON(r))
}

// contractList returns an empty slice in place of nil so the JSON encoding is
// [] instead of null.
func contractList[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// normalizeElements returns list unchanged when no element satisfies needs;
// otherwise a shallow copy of the element structs with normalize applied to
// the copies that need it. needs must not write through its argument.
func normalizeElements[T any](list []T, needs func(*T) bool, normalize func(*T)) []T {
	for i := range list {
		if needs(&list[i]) {
			copied := make([]T, len(list))
			copy(copied, list)
			for j := range copied {
				if needs(&copied[j]) {
					normalize(&copied[j])
				}
			}
			return copied
		}
	}
	return list
}

// ExplanationStatus is the model explanation outcome. Deterministic report
// metrics remain authoritative for every status.
type ExplanationStatus string

const (
	ExplanationStatusAvailable        ExplanationStatus = "available"
	ExplanationStatusRejectedEvidence ExplanationStatus = "rejected_evidence"
	ExplanationStatusUnavailable      ExplanationStatus = "unavailable"
)

// Valid reports whether the explanation state is part of the shared contract.
func (s ExplanationStatus) Valid() bool {
	return s == ExplanationStatusAvailable || s == ExplanationStatusRejectedEvidence || s == ExplanationStatusUnavailable
}

// Explanation is a bounded narrative claim tied to one deterministic metric
// value and existing report evidence.
type Explanation struct {
	RefID        string   `json:"ref_id"`
	MetricID     MetricID `json:"metric_id"`
	Value        string   `json:"value"`
	Text         string   `json:"text"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// RunStage is the persisted progress label published with status. It is not an
// authorization key and never carries financial values.
type RunStage string

const (
	RunStageCreated     RunStage = "created"
	RunStageRunning     RunStage = "running"
	RunStageCompleted   RunStage = "completed"
	RunStageFailed      RunStage = "failed"
	RunStageCancelled   RunStage = "cancelled"
	RunStageInterrupted RunStage = "interrupted"
)

// RunVersion is the version metadata stored with a run: request schema,
// metric engine, and source contract. It does not invent customer thresholds.
type RunVersion struct {
	SchemaVersion   int    `json:"schema_version" bson:"schema_version"`
	MetricVersion   string `json:"metric_version,omitempty" bson:"metric_version,omitempty"`
	ContractVersion string `json:"contract_version,omitempty" bson:"contract_version,omitempty"`
}

// SafeRunError is the only error text a run may persist. It is a machine code
// plus a short message. SQL, credentials, and report plaintext never belong here.
type SafeRunError struct {
	Code    string `json:"code" bson:"code"`
	Message string `json:"message" bson:"message"`
}

// RunRecord is the assistant_runs document. The encrypted report bytes are the
// stored payload; plaintext AnalysisReport is never a field of this type.
//
// Uniqueness is owner_id + chat_id + request_id. Revision is the optimistic
// concurrency token used by conditional transitions.
type RunRecord struct {
	SchemaVersion      int             `json:"schema_version" bson:"schema_version"`
	RunID              string          `json:"run_id" bson:"run_id"`
	OwnerID            string          `json:"owner_id" bson:"owner_id"`
	ChatID             string          `json:"chat_id" bson:"chat_id"`
	RequestID          string          `json:"request_id" bson:"request_id"`
	RequestFingerprint string          `json:"request_fingerprint" bson:"request_fingerprint"`
	NormalizedInput    AnalysisRequest `json:"normalized_input" bson:"normalized_input"`
	MessageID          string          `json:"message_id" bson:"message_id"`
	Status             RunStatus       `json:"status" bson:"status"`
	Stage              RunStage        `json:"stage" bson:"stage"`
	Revision           int64           `json:"revision" bson:"revision"`
	CreatedAt          time.Time       `json:"created_at" bson:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at" bson:"updated_at"`
	ExecutingInstance  string          `json:"executing_instance" bson:"executing_instance"`
	Version            RunVersion      `json:"version" bson:"version"`
	CancellationMarked bool            `json:"cancellation_marked" bson:"cancellation_marked"`
	EncryptedResult    []byte          `json:"encrypted_result,omitempty" bson:"encrypted_result,omitempty"`
	// ReportPlaintext is caller input only. bson and json "-" keep it out of
	// Mongo and out of any encoded record. The repository encrypts it before insert.
	ReportPlaintext []byte        `json:"-" bson:"-"`
	SafeError       *SafeRunError `json:"safe_error,omitempty" bson:"safe_error,omitempty"`
}

// Clone returns a copy whose slices and encrypted bytes are independent of the
// original. NormalizedInput is copied by value; its pointer fields are not
// shared with later mutations of the stored record.
func (r RunRecord) Clone() RunRecord {
	out := r
	if r.EncryptedResult != nil {
		out.EncryptedResult = append([]byte(nil), r.EncryptedResult...)
	}
	if r.ReportPlaintext != nil {
		out.ReportPlaintext = append([]byte(nil), r.ReportPlaintext...)
	}
	if r.SafeError != nil {
		copied := *r.SafeError
		out.SafeError = &copied
	}
	if r.NormalizedInput.TrendPeriod != nil {
		period := *r.NormalizedInput.TrendPeriod
		out.NormalizedInput.TrendPeriod = &period
	}
	if r.NormalizedInput.LowMarginThreshold != nil {
		threshold := *r.NormalizedInput.LowMarginThreshold
		out.NormalizedInput.LowMarginThreshold = &threshold
	}
	out.NormalizedInput.Metrics = append([]MetricID(nil), r.NormalizedInput.Metrics...)
	out.NormalizedInput.Scope.PlatformIDs = append([]string(nil), r.NormalizedInput.Scope.PlatformIDs...)
	out.NormalizedInput.Scope.ShopIDs = append([]string(nil), r.NormalizedInput.Scope.ShopIDs...)
	out.NormalizedInput.Product.SkuIDs = append([]string(nil), r.NormalizedInput.Product.SkuIDs...)
	return out
}

// RunTransition is one conditional state change. The repository applies it
// only when owner, chat, run, expected status, and expected revision all match.
// A terminal status is not a legal From value.
type RunTransition struct {
	OwnerID            string        `json:"owner_id"`
	ChatID             string        `json:"chat_id"`
	RunID              string        `json:"run_id"`
	From               RunStatus     `json:"from"`
	To                 RunStatus     `json:"to"`
	ExpectedRevision   int64         `json:"expected_revision"`
	Stage              RunStage      `json:"stage"`
	ExecutingInstance  string        `json:"executing_instance,omitempty"`
	CancellationMarked *bool         `json:"cancellation_marked,omitempty"`
	MessageID          string        `json:"message_id,omitempty"`
	Version            *RunVersion   `json:"version,omitempty"`
	ReportPlaintext    []byte        `json:"-"`
	SafeError          *SafeRunError `json:"safe_error,omitempty"`
	UpdatedAt          time.Time     `json:"updated_at"`
}

// Legal reports whether From → To is a frozen assistant transition.
// created → running → one terminal state. created may also fail, cancel, or
// be interrupted before execution starts. Terminal states have no outbound edge.
func (t RunTransition) Legal() bool {
	switch t.From {
	case RunStatusCreated:
		return t.To == RunStatusRunning || t.To == RunStatusFailed ||
			t.To == RunStatusCancelled || t.To == RunStatusInterrupted
	case RunStatusRunning:
		return t.To.IsTerminal()
	default:
		return false
	}
}

// RunView is the authorized read model returned by the assistant service.
// Report is present only after the stored ciphertext decrypts. A decrypt
// failure leaves Report nil and surfaces an error instead of the ciphertext.
type RunView struct {
	SchemaVersion      int             `json:"schema_version"`
	RunID              string          `json:"run_id"`
	OwnerID            string          `json:"owner_id"`
	ChatID             string          `json:"chat_id"`
	RequestID          string          `json:"request_id"`
	RequestFingerprint string          `json:"request_fingerprint"`
	NormalizedInput    AnalysisRequest `json:"normalized_input"`
	MessageID          string          `json:"message_id"`
	Status             RunStatus       `json:"status"`
	Stage              RunStage        `json:"stage"`
	Revision           int64           `json:"revision"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	ExecutingInstance  string          `json:"executing_instance"`
	Version            RunVersion      `json:"version"`
	CancellationMarked bool            `json:"cancellation_marked"`
	Report             *AnalysisReport `json:"report,omitempty"`
	SafeError          *SafeRunError   `json:"safe_error,omitempty"`
}

// RunViewFromRecord copies persistence fields into a view and leaves Report
// unset. Callers attach a decrypted report separately.
func RunViewFromRecord(record RunRecord) RunView {
	view := RunView{
		SchemaVersion:      record.SchemaVersion,
		RunID:              record.RunID,
		OwnerID:            record.OwnerID,
		ChatID:             record.ChatID,
		RequestID:          record.RequestID,
		RequestFingerprint: record.RequestFingerprint,
		NormalizedInput:    record.NormalizedInput,
		MessageID:          record.MessageID,
		Status:             record.Status,
		Stage:              record.Stage,
		Revision:           record.Revision,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
		ExecutingInstance:  record.ExecutingInstance,
		Version:            record.Version,
		CancellationMarked: record.CancellationMarked,
	}
	if record.SafeError != nil {
		copied := *record.SafeError
		view.SafeError = &copied
	}
	return view
}
