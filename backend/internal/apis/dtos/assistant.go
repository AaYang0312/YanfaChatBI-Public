package dtos

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"yanfachatbi/internal/models"
)

// Assistant v1 wire types. The field sets below are exactly the shared contract
// in the shared assistant contract; every other key is
// rejected with the unknown_field code. Client identity is never part of a
// request body: AccessContext is built by the server.

// AssistantDateRange is the wire shape of a left-closed, right-open business-day
// range.
type AssistantDateRange struct {
	StartDate        string `json:"start_date"`
	EndDateExclusive string `json:"end_date_exclusive"`
}

// AssistantPeriod is the wire shape of the main analysis window.
type AssistantPeriod struct {
	StartDate        string `json:"start_date"`
	EndDateExclusive string `json:"end_date_exclusive"`
	Timezone         string `json:"timezone"`
}

// AssistantScope is the wire shape of the requested entity range.
type AssistantScope struct {
	Mode        string   `json:"mode"`
	PlatformIDs []string `json:"platform_ids,omitempty"`
	ShopIDs     []string `json:"shop_ids,omitempty"`
}

// AssistantProduct is the wire shape of a confirmed product identity.
type AssistantProduct struct {
	ProductID string   `json:"product_id"`
	SkuIDs    []string `json:"sku_ids,omitempty"`
}

// AnalysisRequest is the wire shape of an executable Assistant request.
// low_margin_threshold is a decimal string, never a JSON number.
type AnalysisRequest struct {
	SchemaVersion      int                 `json:"schema_version"`
	RequestID          string              `json:"request_id"`
	Workflow           string              `json:"workflow"`
	Basis              string              `json:"basis,omitempty"`
	Period             AssistantPeriod     `json:"period"`
	TrendPeriod        *AssistantDateRange `json:"trend_period,omitempty"`
	Scope              AssistantScope      `json:"scope"`
	Product            AssistantProduct    `json:"product"`
	GroupBy            string              `json:"group_by"`
	Metrics            []string            `json:"metrics"`
	LowMarginThreshold *string             `json:"low_margin_threshold,omitempty"`
	StreamID           string              `json:"stream_id,omitempty"`
	ParentRunID        string              `json:"parent_run_id,omitempty"`
}

// DraftRequest is the wire shape of an unresolved natural-language draft. It may
// carry product query text; it never carries a client user ID, SQL, credentials,
// column names, or schema names.
type DraftRequest struct {
	SchemaVersion      int                 `json:"schema_version"`
	RequestID          string              `json:"request_id"`
	Workflow           string              `json:"workflow"`
	Basis              string              `json:"basis,omitempty"`
	Period             *AssistantPeriod    `json:"period,omitempty"`
	TrendPeriod        *AssistantDateRange `json:"trend_period,omitempty"`
	Scope              *AssistantScope     `json:"scope,omitempty"`
	ProductQuery       string              `json:"product_query"`
	Product            *AssistantProduct   `json:"product,omitempty"`
	GroupBy            string              `json:"group_by,omitempty"`
	Metrics            []string            `json:"metrics,omitempty"`
	LowMarginThreshold *string             `json:"low_margin_threshold,omitempty"`
	StreamID           string              `json:"stream_id,omitempty"`
	ParentRunID        string              `json:"parent_run_id,omitempty"`
}

// forbiddenAssistantRequestFields are never accepted in an Assistant request
// body at any depth: client-supplied identity, SQL text, credentials, and
// arbitrary column or schema names. Identity comes from the JWT and the
// authorized Chat path; source objects come only from the server-side approved
// contract.
var forbiddenAssistantRequestFields = []string{
	"user_id",
	"userid",
	"owner_id",
	"owner",
	"tenant_id",
	"sql",
	"raw_sql",
	"sql_text",
	"statement",
	"query",
	"queries",
	"columns",
	"column",
	"column_names",
	"schema",
	"schema_name",
	"table",
	"table_name",
	"order_by",
	"limit",
	"password",
	"credentials",
	"credential",
	"token",
	"api_key",
	"dsn",
	"access_key",
	"secret",
}

// ForbiddenAssistantRequestFields returns a copy of the never-accepted key list.
func ForbiddenAssistantRequestFields() []string {
	out := make([]string, len(forbiddenAssistantRequestFields))
	copy(out, forbiddenAssistantRequestFields)
	return out
}

// DecodeAssistantAnalysisRequest strictly decodes an Assistant request body. It
// returns a *models.ValidationError with code unknown_field for any extra key
// and code invalid_schema for malformed JSON, a wrong JSON type, or a decimal
// that is not a string.
func DecodeAssistantAnalysisRequest(raw []byte) (AnalysisRequest, error) {
	var req AnalysisRequest
	if err := decodeAssistantRequest(raw, &req, "analysis request"); err != nil {
		return AnalysisRequest{}, err
	}
	return req, nil
}

// DecodeAssistantDraftRequest strictly decodes an Assistant draft body.
func DecodeAssistantDraftRequest(raw []byte) (DraftRequest, error) {
	var draft DraftRequest
	if err := decodeAssistantRequest(raw, &draft, "draft request"); err != nil {
		return DraftRequest{}, err
	}
	return draft, nil
}

func decodeAssistantRequest(raw []byte, target any, kind string) error {
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return models.NewValidationError(models.ErrorCodeInvalidSchema, "", kind+" body is empty")
	}

	var tree any
	if err := json.Unmarshal([]byte(body), &tree); err != nil {
		return models.NewValidationError(models.ErrorCodeInvalidSchema, "",
			"body is not valid JSON: "+err.Error())
	}
	if field, forbidden := findForbiddenField(tree, ""); forbidden {
		return models.NewValidationError(models.ErrorCodeUnknownField, field,
			"field "+field+" is never accepted in an Assistant "+kind)
	}

	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return decodeAssistantError(err)
	}
	return nil
}

func decodeAssistantError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field != "" {
			return models.NewValidationError(models.ErrorCodeInvalidSchema, typeErr.Field,
				fmt.Sprintf("field %q must be %s, got %s", typeErr.Field, typeErr.Type, typeErr.Value))
		}
		return models.NewValidationError(models.ErrorCodeInvalidSchema, "", err.Error())
	}
	if name, unknown := unknownFieldName(err); unknown {
		return models.NewValidationError(models.ErrorCodeUnknownField, name,
			"field "+name+" is not part of the Assistant request contract")
	}
	return models.NewValidationError(models.ErrorCodeInvalidSchema, "", err.Error())
}

// unknownFieldName extracts the field name encoding/json reports for
// DisallowUnknownFields failures, whose message is `json: unknown field "x"`.
func unknownFieldName(err error) (string, bool) {
	const marker = `unknown field "`
	msg := err.Error()
	idx := strings.Index(msg, marker)
	if idx < 0 {
		return "", false
	}
	rest := msg[idx+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// findForbiddenField walks the decoded JSON tree and reports the first forbidden
// key as a dotted path. Keys are compared lower-cased because Go JSON field
// matching is case-insensitive; iteration is sorted so the reported path is
// deterministic.
func findForbiddenField(node any, path string) (string, bool) {
	switch value := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			lower := strings.ToLower(key)
			childPath := lower
			if path != "" {
				childPath = path + "." + lower
			}
			for _, forbidden := range forbiddenAssistantRequestFields {
				if lower == forbidden {
					return childPath, true
				}
			}
			if found, ok := findForbiddenField(value[key], childPath); ok {
				return found, true
			}
		}
	case []any:
		for i, child := range value {
			if found, ok := findForbiddenField(child, fmt.Sprintf("%s[%d]", path, i)); ok {
				return found, true
			}
		}
	}
	return "", false
}

// ToDomain converts the wire request into the normative domain struct.
func (r AnalysisRequest) ToDomain() (models.AnalysisRequest, error) {
	threshold, err := parseAssistantThreshold(r.LowMarginThreshold)
	if err != nil {
		return models.AnalysisRequest{}, err
	}
	return models.AnalysisRequest{
		SchemaVersion:      r.SchemaVersion,
		RequestID:          r.RequestID,
		Workflow:           models.Workflow(r.Workflow),
		Basis:              models.Basis(r.Basis),
		Period:             periodToDomain(r.Period),
		TrendPeriod:        dateRangePtrToDomain(r.TrendPeriod),
		Scope:              scopeToDomain(r.Scope),
		Product:            productToDomain(r.Product),
		GroupBy:            models.GroupBy(r.GroupBy),
		Metrics:            metricIDsToDomain(r.Metrics),
		LowMarginThreshold: threshold,
		StreamID:           r.StreamID,
		ParentRunID:        r.ParentRunID,
	}, nil
}

// ToDomain converts the wire draft into the normative domain struct.
func (d DraftRequest) ToDomain() (models.DraftRequest, error) {
	threshold, err := parseAssistantThreshold(d.LowMarginThreshold)
	if err != nil {
		return models.DraftRequest{}, err
	}
	return models.DraftRequest{
		SchemaVersion:      d.SchemaVersion,
		RequestID:          d.RequestID,
		Workflow:           models.Workflow(d.Workflow),
		Basis:              models.Basis(d.Basis),
		Period:             periodPtrToDomain(d.Period),
		TrendPeriod:        dateRangePtrToDomain(d.TrendPeriod),
		Scope:              scopePtrToDomain(d.Scope),
		ProductQuery:       d.ProductQuery,
		Product:            productPtrToDomain(d.Product),
		GroupBy:            models.GroupBy(d.GroupBy),
		Metrics:            metricIDsToDomain(d.Metrics),
		LowMarginThreshold: threshold,
		StreamID:           d.StreamID,
		ParentRunID:        d.ParentRunID,
	}, nil
}

// parseAssistantThreshold keeps the exact decimal the client sent: no rounding,
// no float64, and absent stays distinct from zero.
func parseAssistantThreshold(raw *string) (*decimal.Decimal, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, models.NewValidationError(models.ErrorCodeInvalidSchema, "low_margin_threshold",
			"low_margin_threshold must be a decimal string")
	}
	value, err := decimal.NewFromString(trimmed)
	if err != nil {
		return nil, models.NewValidationError(models.ErrorCodeInvalidSchema, "low_margin_threshold",
			"low_margin_threshold must be a decimal string: "+err.Error())
	}
	return &value, nil
}

func periodToDomain(p AssistantPeriod) models.Period {
	return models.Period{
		DateRange: models.DateRange{
			StartDate:        p.StartDate,
			EndDateExclusive: p.EndDateExclusive,
		},
		Timezone: p.Timezone,
	}
}

func periodPtrToDomain(p *AssistantPeriod) *models.Period {
	if p == nil {
		return nil
	}
	domain := periodToDomain(*p)
	return &domain
}

func dateRangeToDomain(r AssistantDateRange) models.DateRange {
	return models.DateRange{StartDate: r.StartDate, EndDateExclusive: r.EndDateExclusive}
}

func dateRangePtrToDomain(r *AssistantDateRange) *models.DateRange {
	if r == nil {
		return nil
	}
	domain := dateRangeToDomain(*r)
	return &domain
}

func scopeToDomain(s AssistantScope) models.Scope {
	return models.Scope{
		Mode:        models.ScopeMode(s.Mode),
		PlatformIDs: copyStrings(s.PlatformIDs),
		ShopIDs:     copyStrings(s.ShopIDs),
	}
}

func scopePtrToDomain(s *AssistantScope) *models.Scope {
	if s == nil {
		return nil
	}
	domain := scopeToDomain(*s)
	return &domain
}

func productToDomain(p AssistantProduct) models.ProductSelection {
	return models.ProductSelection{ProductID: p.ProductID, SkuIDs: copyStrings(p.SkuIDs)}
}

func productPtrToDomain(p *AssistantProduct) *models.ProductSelection {
	if p == nil {
		return nil
	}
	domain := productToDomain(*p)
	return &domain
}

func metricIDsToDomain(in []string) []models.MetricID {
	if in == nil {
		return nil
	}
	out := make([]models.MetricID, 0, len(in))
	for _, name := range in {
		out = append(out, models.MetricID(name))
	}
	return out
}

func metricIDsToWire(in []models.MetricID) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, metric := range in {
		out = append(out, string(metric))
	}
	return out
}

func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := append([]string(nil), in...)
	return out
}

// NewAnalysisRequest builds the wire shape of a domain request so the emitted
// JSON field set stays identical to the accepted one.
func NewAnalysisRequest(req models.AnalysisRequest) AnalysisRequest {
	out := AnalysisRequest{
		SchemaVersion: req.SchemaVersion,
		RequestID:     req.RequestID,
		Workflow:      string(req.Workflow),
		Basis:         string(req.Basis),
		Period: AssistantPeriod{
			StartDate:        req.Period.StartDate,
			EndDateExclusive: req.Period.EndDateExclusive,
			Timezone:         req.Period.Timezone,
		},
		GroupBy:            string(req.GroupBy),
		Metrics:            metricIDsToWire(req.Metrics),
		StreamID:           req.StreamID,
		ParentRunID:        req.ParentRunID,
		Scope:              scopeToWire(req.Scope),
		Product:            productToWire(req.Product),
		LowMarginThreshold: decimalToWire(req.LowMarginThreshold),
	}
	if req.TrendPeriod != nil {
		out.TrendPeriod = &AssistantDateRange{
			StartDate:        req.TrendPeriod.StartDate,
			EndDateExclusive: req.TrendPeriod.EndDateExclusive,
		}
	}
	return out
}

// NewDraftRequest builds the wire shape of a domain draft.
func NewDraftRequest(draft models.DraftRequest) DraftRequest {
	out := DraftRequest{
		SchemaVersion:      draft.SchemaVersion,
		RequestID:          draft.RequestID,
		Workflow:           string(draft.Workflow),
		Basis:              string(draft.Basis),
		TrendPeriod:        dateRangePtrToWire(draft.TrendPeriod),
		ProductQuery:       draft.ProductQuery,
		Product:            productPtrToWire(draft.Product),
		GroupBy:            string(draft.GroupBy),
		Metrics:            metricIDsToWire(draft.Metrics),
		StreamID:           draft.StreamID,
		ParentRunID:        draft.ParentRunID,
		LowMarginThreshold: decimalToWire(draft.LowMarginThreshold),
	}
	if draft.Period != nil {
		period := periodToWire(*draft.Period)
		out.Period = &period
	}
	if draft.Scope != nil {
		scope := scopeToWire(*draft.Scope)
		out.Scope = &scope
	}
	return out
}

func periodToWire(p models.Period) AssistantPeriod {
	return AssistantPeriod{
		StartDate:        p.StartDate,
		EndDateExclusive: p.EndDateExclusive,
		Timezone:         p.Timezone,
	}
}

func dateRangePtrToWire(r *models.DateRange) *AssistantDateRange {
	if r == nil {
		return nil
	}
	return &AssistantDateRange{StartDate: r.StartDate, EndDateExclusive: r.EndDateExclusive}
}

func scopeToWire(s models.Scope) AssistantScope {
	return AssistantScope{
		Mode:        string(s.Mode),
		PlatformIDs: copyStrings(s.PlatformIDs),
		ShopIDs:     copyStrings(s.ShopIDs),
	}
}

func productToWire(p models.ProductSelection) AssistantProduct {
	return AssistantProduct{ProductID: p.ProductID, SkuIDs: copyStrings(p.SkuIDs)}
}

func productPtrToWire(p *models.ProductSelection) *AssistantProduct {
	if p == nil {
		return nil
	}
	wire := productToWire(*p)
	return &wire
}

func decimalToWire(value *decimal.Decimal) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return &text
}

// AssistantCandidate is one resolve candidate. Only entity_id may be resubmitted.
type AssistantCandidate struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Label      string `json:"label"`
	PlatformID string `json:"platform_id,omitempty"`
	ShopID     string `json:"shop_id,omitempty"`
}

// AssistantResolution is the resolve response. needs_input carries candidates
// or missing fields and never a request.
type AssistantResolution struct {
	Status        string               `json:"status"`
	Request       *AnalysisRequest     `json:"request,omitempty"`
	MissingFields []string             `json:"missing_fields,omitempty"`
	Candidates    []AssistantCandidate `json:"candidates,omitempty"`
}

// AssistantRunView is the persisted run returned by get and cancel. It does
// not include ciphertext.
type AssistantRunView struct {
	SchemaVersion      int                  `json:"schema_version"`
	RunID              string               `json:"run_id"`
	RequestID          string               `json:"request_id"`
	MessageID          string               `json:"message_id"`
	Status             string               `json:"status"`
	Stage              string               `json:"stage"`
	Revision           int64                `json:"revision"`
	CancellationMarked bool                 `json:"cancellation_marked"`
	NormalizedInput    AnalysisRequest      `json:"normalized_input"`
	Report             json.RawMessage      `json:"report,omitempty"`
	SafeError          *models.SafeRunError `json:"safe_error,omitempty"`
}

// NewResolution converts a domain resolution to the wire response.
func NewResolution(resolution models.Resolution) AssistantResolution {
	out := AssistantResolution{
		Status:        string(resolution.Status),
		MissingFields: copyStrings(resolution.MissingFields),
	}
	if resolution.Request != nil {
		request := NewAnalysisRequest(*resolution.Request)
		out.Request = &request
	}
	if len(resolution.Candidates) > 0 {
		out.Candidates = make([]AssistantCandidate, 0, len(resolution.Candidates))
		for _, candidate := range resolution.Candidates {
			out.Candidates = append(out.Candidates, AssistantCandidate{
				EntityType: string(candidate.EntityType),
				EntityID:   candidate.EntityID,
				Label:      candidate.Label,
				PlatformID: candidate.PlatformID,
				ShopID:     candidate.ShopID,
			})
		}
	}
	return out
}

// NewRunView converts an authorized run view to the wire response. Report
// JSON uses decimal strings and omits the stored ciphertext.
func NewRunView(view models.RunView) AssistantRunView {
	out := AssistantRunView{
		SchemaVersion:      view.SchemaVersion,
		RunID:              view.RunID,
		RequestID:          view.RequestID,
		MessageID:          view.MessageID,
		Status:             string(view.Status),
		Stage:              string(view.Stage),
		Revision:           view.Revision,
		CancellationMarked: view.CancellationMarked,
		NormalizedInput:    NewAnalysisRequest(view.NormalizedInput),
		SafeError:          view.SafeError,
	}
	if view.Report != nil {
		if encoded, err := json.Marshal(view.Report); err == nil {
			out.Report = encoded
		}
	}
	return out
}
