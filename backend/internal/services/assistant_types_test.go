package services

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
)

// assistantApprovedAll approves the whole connection, so scope.mode all is valid.
func assistantApprovedAll() models.AccessContext {
	return models.AccessContext{
		OwnerID:      "owner-1",
		ChatID:       "chat-1",
		ConnectionID: "conn-1",
		ApprovedScope: models.ApprovedScope{
			All:         true,
			PlatformIDs: []string{"P1", "P2"},
			ShopIDs:     []string{"S1", "S2"},
		},
	}
}

// assistantApprovedLists approves only the listed platforms and shops.
func assistantApprovedLists() models.AccessContext {
	access := assistantApprovedAll()
	access.ApprovedScope.All = false
	return access
}

// assistantRequestPayload assembles a valid request body with JSON overrides. A
// null override removes the contract field.
func assistantRequestPayload(overrides string) string {
	if overrides == "" {
		overrides = `{}`
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{
		"schema_version": 1,
		"request_id": "req-1",
		"workflow": "product_analysis",
		"scope": {"mode": "all"},
		"product": {"product_id": "P1-SKU-1"},
		"group_by": "shop",
		"metrics": ["quantity", "sales", "cost", "margin", "margin_rate", "avg_price"]
	}`), &base); err != nil {
		panic(err)
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(overrides), &patch); err != nil {
		panic(err)
	}
	for key, value := range patch {
		if value == nil || string(value) == "null" {
			delete(base, key)
			continue
		}
		base[key] = value
	}
	raw, err := json.Marshal(base)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func assistantValidatePayload(access models.AccessContext, payload string) (models.AnalysisRequest, error) {
	wire, err := dtos.DecodeAssistantAnalysisRequest([]byte(payload))
	if err != nil {
		return models.AnalysisRequest{}, err
	}
	domain, err := wire.ToDomain()
	if err != nil {
		return models.AnalysisRequest{}, err
	}
	return ValidateAnalysisRequest(access, domain, AssistantFixtureClock())
}

func assistantErrorCode(t *testing.T, err error) models.ValidationErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("expected a validation error, got nil")
	}
	validationErr, ok := models.AsValidationError(err)
	if !ok {
		t.Fatalf("error %v is not a *models.ValidationError", err)
	}
	return validationErr.Code
}

func assistantMustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := AssistantLoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

func TestAssistantValidationFixtureClock(t *testing.T) {
	clock := AssistantFixtureClock()
	if got := clock.Format("2006-01-02T15:04:05-07:00"); got != models.AssistantFixtureClockRFC3339 {
		t.Fatalf("AssistantFixtureClock() = %q want %q", got, models.AssistantFixtureClockRFC3339)
	}

	period, err := AssistantDefaultPeriod(clock, "")
	if err != nil {
		t.Fatalf("default period: %v", err)
	}
	if period.StartDate != "2026-09-01" || period.EndDateExclusive != "2026-09-08" {
		t.Fatalf("default window = %s..%s want 2026-09-01..2026-09-08", period.StartDate, period.EndDateExclusive)
	}
	if period.Timezone != "Asia/Shanghai" {
		t.Fatalf("default timezone = %q want Asia/Shanghai", period.Timezone)
	}
	// The exclusive bound is the first instant of the clock's own business day, so
	// a transaction at 2026-09-08T00:00:00+08:00 is outside the window.
	localClock := clock.In(assistantMustLocation(t, "Asia/Shanghai"))
	if day := localClock.Format(models.AssistantDateLayout); day != period.EndDateExclusive {
		t.Fatalf("window end %q must exclude the clock business day %q", period.EndDateExclusive, day)
	}

	trend, err := AssistantDefaultTrendPeriod(period)
	if err != nil {
		t.Fatalf("default trend period: %v", err)
	}
	if trend.StartDate != "2026-09-01" || trend.EndDateExclusive != "2026-09-08" {
		t.Fatalf("default trend window = %+v want 2026-09-01..2026-09-08", trend)
	}
}

func TestAssistantValidationValidRequest(t *testing.T) {
	t.Run("defaults normalize to payment, Asia/Shanghai, and the seven-day window", func(t *testing.T) {
		req, err := assistantValidatePayload(assistantApprovedAll(), assistantRequestPayload(""))
		if err != nil {
			t.Fatalf("validate valid request: %v", err)
		}
		if req.Basis != models.BasisPayment {
			t.Fatalf("basis = %q, want payment", req.Basis)
		}
		if req.Period.Timezone != "Asia/Shanghai" {
			t.Fatalf("period.timezone = %q, want Asia/Shanghai", req.Period.Timezone)
		}
		if req.Period.StartDate != "2026-09-01" || req.Period.EndDateExclusive != "2026-09-08" {
			t.Fatalf("period = %+v want 2026-09-01..2026-09-08", req.Period)
		}
		if req.SchemaVersion != models.AssistantSchemaVersion || req.RequestID != "req-1" {
			t.Fatalf("request identity lost: %+v", req)
		}
		if req.GroupBy != models.GroupByShop {
			t.Fatalf("product_analysis group_by = %q want shop", req.GroupBy)
		}
		if req.Scope.Mode != models.ScopeModeAll {
			t.Fatalf("scope.mode = %q want all", req.Scope.Mode)
		}
		if req.TrendPeriod == nil {
			t.Fatal("absent trend_period must default to seven days ending at main end")
		}
		if req.TrendPeriod.StartDate != "2026-09-01" || req.TrendPeriod.EndDateExclusive != "2026-09-08" {
			t.Fatalf("trend_period = %+v, want 2026-09-01..2026-09-08 (default)", req.TrendPeriod)
		}
		if req.LowMarginThreshold != nil {
			t.Fatalf("absent threshold must stay nil, distinct from \"0\", got %s", req.LowMarginThreshold)
		}
		if len(req.Metrics) != 6 {
			t.Fatalf("metrics = %v", req.Metrics)
		}

		query := FactQueryFromRequest(req)
		if query.Basis != models.BasisPayment || query.Window.StartDate != "2026-09-01" ||
			query.Window.EndDateExclusive != "2026-09-08" {
			t.Fatalf("fact query window = %+v", query)
		}
		if len(query.Metrics) != 6 || query.Product.ProductID != "P1-SKU-1" {
			t.Fatalf("fact query identity = %+v", query)
		}
	})

	t.Run("explicit basis, window, and threshold are preserved exactly", func(t *testing.T) {
		payload := assistantRequestPayload(`{
			"basis": "outstock",
			"period": {"start_date": "2026-08-01", "end_date_exclusive": "2026-08-31", "timezone": "Asia/Shanghai"},
			"low_margin_threshold": "0.30000000000000004"
		}`)
		req, err := assistantValidatePayload(assistantApprovedAll(), payload)
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if req.Basis != models.BasisOutstock {
			t.Fatalf("basis = %q want outstock", req.Basis)
		}
		if req.Period.StartDate != "2026-08-01" || req.Period.EndDateExclusive != "2026-08-31" {
			t.Fatalf("period = %+v", req.Period)
		}
		if req.LowMarginThreshold == nil || req.LowMarginThreshold.String() != "0.30000000000000004" {
			t.Fatalf("threshold must stay the exact decimal string, got %v", req.LowMarginThreshold)
		}
	})

	t.Run("threshold zero is distinct from an absent threshold", func(t *testing.T) {
		req, err := assistantValidatePayload(assistantApprovedAll(),
			assistantRequestPayload(`{"low_margin_threshold": "0"}`))
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if req.LowMarginThreshold == nil || !req.LowMarginThreshold.IsZero() {
			t.Fatalf("threshold = %v want exact zero, not nil", req.LowMarginThreshold)
		}
	})

	t.Run("blank trend window defaults to the seven days ending at the main window end", func(t *testing.T) {
		req, err := assistantValidatePayload(assistantApprovedAll(),
			assistantRequestPayload(`{"trend_period": {}}`))
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if req.TrendPeriod == nil {
			t.Fatal("trend_period missing")
		}
		if req.TrendPeriod.StartDate != "2026-09-01" || req.TrendPeriod.EndDateExclusive != "2026-09-08" {
			t.Fatalf("trend = %+v", req.TrendPeriod)
		}
	})

	t.Run("trend may precede a short main window and may overlap it", func(t *testing.T) {
		req, err := assistantValidatePayload(assistantApprovedAll(),
			assistantRequestPayload(`{
				"period": {"start_date": "2026-09-04", "end_date_exclusive": "2026-09-08", "timezone": "Asia/Shanghai"},
				"trend_period": {}
			}`))
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if req.TrendPeriod == nil {
			t.Fatal("trend_period missing")
		}
		if req.TrendPeriod.StartDate != "2026-09-01" || req.TrendPeriod.EndDateExclusive != "2026-09-08" {
			t.Fatalf("trend = %+v, want 2026-09-01..2026-09-08 preceding the short main start", req.TrendPeriod)
		}
	})

	t.Run("sales_comparison grouping", func(t *testing.T) {
		platform, err := assistantValidatePayload(assistantApprovedLists(), assistantRequestPayload(`{
			"workflow": "sales_comparison",
			"group_by": "platform",
			"scope": {"mode": "selected", "platform_ids": ["P1", "P2"]},
			"product": null
		}`))
		if err != nil {
			t.Fatalf("platform grouping: %v", err)
		}
		if platform.GroupBy != models.GroupByPlatform {
			t.Fatalf("group_by = %q", platform.GroupBy)
		}

		shop, err := assistantValidatePayload(assistantApprovedLists(), assistantRequestPayload(`{
			"workflow": "sales_comparison",
			"group_by": "shop",
			"scope": {"mode": "selected", "platform_ids": ["P1"]},
			"product": null
		}`))
		if err != nil {
			t.Fatalf("single-platform shop grouping: %v", err)
		}
		if shop.GroupBy != models.GroupByShop {
			t.Fatalf("group_by = %q", shop.GroupBy)
		}
	})
}

func TestAssistantValidationInvalidPayloads(t *testing.T) {
	cases := []struct {
		name     string
		access   models.AccessContext
		payload  string
		wantCode models.ValidationErrorCode
	}{
		{
			name:     "unknown_field: client user id",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"user_id": "u1"}`),
			wantCode: models.ErrorCodeUnknownField,
		},
		{
			name:     "unknown_field: arbitrary sql",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"sql": "SELECT * FROM bi.order_items"}`),
			wantCode: models.ErrorCodeUnknownField,
		},
		{
			name:     "invalid_schema: wrong schema version",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"schema_version": 2}`),
			wantCode: models.ErrorCodeInvalidSchema,
		},
		{
			name:     "invalid_schema: threshold out of ratio range",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"low_margin_threshold": "1.5"}`),
			wantCode: models.ErrorCodeInvalidSchema,
		},
		{
			name:     "invalid_date_range: reversed window",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"period": {"start_date": "2026-09-08", "end_date_exclusive": "2026-09-01", "timezone": "Asia/Shanghai"}}`),
			wantCode: models.ErrorCodeInvalidDateRange,
		},
		{
			name:     "invalid_date_range: empty window",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"period": {"start_date": "2026-09-01", "end_date_exclusive": "2026-09-01", "timezone": "Asia/Shanghai"}}`),
			wantCode: models.ErrorCodeInvalidDateRange,
		},
		{
			name:     "invalid_date_range: business date with a time component",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"period": {"start_date": "2026-09-01T00:00:00+08:00", "end_date_exclusive": "2026-09-08", "timezone": "Asia/Shanghai"}}`),
			wantCode: models.ErrorCodeInvalidDateRange,
		},
		{
			name:     "invalid_enum: workflow",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"workflow": "inventory_review"}`),
			wantCode: models.ErrorCodeInvalidEnum,
		},
		{
			name:     "invalid_enum: basis",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"basis": "paid"}`),
			wantCode: models.ErrorCodeInvalidEnum,
		},
		{
			name:     "invalid_enum: scope mode",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"scope": {"mode": "everything"}}`),
			wantCode: models.ErrorCodeInvalidEnum,
		},
		{
			name:     "invalid_enum: group by",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"group_by": "region"}`),
			wantCode: models.ErrorCodeInvalidEnum,
		},
		{
			name:     "invalid_enum: metric is a column name",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"metrics": ["order_items.total"]}`),
			wantCode: models.ErrorCodeInvalidEnum,
		},
		{
			name:     "invalid_enum: timezone",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"period": {"start_date": "2026-09-01", "end_date_exclusive": "2026-09-08", "timezone": "Mars/Phobos"}}`),
			wantCode: models.ErrorCodeInvalidEnum,
		},
		{
			name:     "missing_field: product workflow without a confirmed product",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"product": null}`),
			wantCode: models.ErrorCodeMissingField,
		},
		{
			name:     "missing_field: blank product id",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"product": {"product_id": "  "}}`),
			wantCode: models.ErrorCodeMissingField,
		},
		{
			name:   "missing_field: shop grouping over two platforms",
			access: assistantApprovedLists(),
			payload: assistantRequestPayload(`{
				"workflow": "sales_comparison",
				"group_by": "shop",
				"scope": {"mode": "selected", "platform_ids": ["P1", "P2"]},
				"product": null
			}`),
			wantCode: models.ErrorCodeMissingField,
		},
		{
			name:   "missing_field: comparison without explicit grouping",
			access: assistantApprovedLists(),
			payload: assistantRequestPayload(`{
				"workflow": "sales_comparison",
				"scope": {"mode": "selected", "platform_ids": ["P1"]},
				"product": null,
				"group_by": null
			}`),
			wantCode: models.ErrorCodeMissingField,
		},
		{
			name:     "missing_field: no metrics",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"metrics": null}`),
			wantCode: models.ErrorCodeMissingField,
		},
		{
			name:     "missing_field: no request id",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"request_id": "  "}`),
			wantCode: models.ErrorCodeMissingField,
		},
		{
			name:     "conflicting_scope: all with explicit shops",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"scope": {"mode": "all", "shop_ids": ["S1"]}}`),
			wantCode: models.ErrorCodeConflictingScope,
		},
		{
			name:     "conflicting_scope: all with explicit platforms",
			access:   assistantApprovedAll(),
			payload:  assistantRequestPayload(`{"scope": {"mode": "all", "platform_ids": ["P1"]}}`),
			wantCode: models.ErrorCodeConflictingScope,
		},
		{
			name:     "unauthorized_scope: shop outside the approved list",
			access:   assistantApprovedLists(),
			payload:  assistantRequestPayload(`{"scope": {"mode": "selected", "shop_ids": ["S9"]}}`),
			wantCode: models.ErrorCodeUnauthorizedScope,
		},
		{
			name:     "unauthorized_scope: all without a whole-connection approval",
			access:   assistantApprovedLists(),
			payload:  assistantRequestPayload(`{"scope": {"mode": "all"}}`),
			wantCode: models.ErrorCodeUnauthorizedScope,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := assistantValidatePayload(c.access, c.payload)
			if got := assistantErrorCode(t, err); got != c.wantCode {
				t.Fatalf("code = %s want %s (%v)", got, c.wantCode, err)
			}
			if !reflect.DeepEqual(req, models.AnalysisRequest{}) {
				t.Fatalf("an invalid payload must not yield a request to build a run from: %+v", req)
			}
			validationErr, _ := models.AsValidationError(err)
			if validationErr.Field == "" {
				t.Fatalf("error must name the offending field: %+v", validationErr)
			}
			if strings.Contains(validationErr.Message, "SELECT") {
				t.Fatalf("error message must not echo sql: %s", validationErr.Message)
			}
			if status, mapped := validationErr.Code.HTTPStatus(); !mapped || status == 0 {
				t.Fatalf("code %s must map to a request status", validationErr.Code)
			}
		})
	}
}

func TestAssistantValidationDraftResolution(t *testing.T) {
	draft := models.DraftRequest{
		SchemaVersion: models.AssistantSchemaVersion,
		RequestID:     "draft-1",
		Workflow:      models.WorkflowProductAnalysis,
		ProductQuery:  "双层玻璃杯",
		Metrics:       []models.MetricID{models.MetricQuantity, models.MetricSales},
	}

	t.Run("a unique candidate resolves the product", func(t *testing.T) {
		candidates := []models.Candidate{{
			EntityType: models.EntityTypeProduct, EntityID: "P1-SKU-1", Label: "双层玻璃杯 500ml",
			PlatformID: "P1", ShopID: "S1",
		}}
		req, err := ValidateDraftRequest(assistantApprovedAll(), draft, AssistantFixtureClock(), candidates)
		if err != nil {
			t.Fatalf("validate draft: %v", err)
		}
		if req.Product.ProductID != "P1-SKU-1" {
			t.Fatalf("product = %+v", req.Product)
		}
		if req.Basis != models.BasisPayment || req.Period.StartDate != "2026-09-01" ||
			req.Period.EndDateExclusive != "2026-09-08" || req.Period.Timezone != "Asia/Shanghai" {
			t.Fatalf("draft defaults = %+v", req.Period)
		}

		resolution, err := ResolveDraft(assistantApprovedAll(), draft, AssistantFixtureClock(), candidates)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if resolution.Status != models.ResolveStatusResolved || resolution.Request == nil {
			t.Fatalf("resolution = %+v", resolution)
		}
		if len(resolution.MissingFields) != 0 || len(resolution.Candidates) != 0 {
			t.Fatalf("resolved resolution carries input requests: %+v", resolution)
		}
	})

	t.Run("several candidates need input, never a run", func(t *testing.T) {
		candidates := []models.Candidate{
			{EntityType: models.EntityTypeProduct, EntityID: "P1", Label: "双层玻璃杯 A", PlatformID: "P1", ShopID: "S1"},
			{EntityType: models.EntityTypeProduct, EntityID: "P2", Label: "双层玻璃杯 B", PlatformID: "P1", ShopID: "S2"},
			{EntityType: models.EntityTypeSku, EntityID: "SKU-9", Label: "规格 1000ml", PlatformID: "P1", ShopID: "S2"},
		}
		resolution, err := ResolveDraft(assistantApprovedAll(), draft, AssistantFixtureClock(), candidates)
		if err != nil {
			t.Fatalf("resolve ambiguous draft: %v", err)
		}
		if resolution.Status != models.ResolveStatusNeedsInput {
			t.Fatalf("status = %q want needs_input", resolution.Status)
		}
		if resolution.Request != nil {
			t.Fatalf("needs_input must not carry an analysis request: %+v", resolution.Request)
		}
		if !assistantContains(resolution.MissingFields, assistantProductIdentityField) {
			t.Fatalf("missing_fields = %v", resolution.MissingFields)
		}
		if len(resolution.Candidates) != 2 {
			t.Fatalf("candidates = %+v want the two product candidates", resolution.Candidates)
		}

		req, err := ValidateDraftRequest(assistantApprovedAll(), draft, AssistantFixtureClock(), candidates)
		if got := assistantErrorCode(t, err); got != models.ErrorCodeAmbiguousProduct {
			t.Fatalf("code = %s want ambiguous_product (%v)", got, err)
		}
		if !reflect.DeepEqual(req, models.AnalysisRequest{}) {
			t.Fatalf("ambiguous product must not yield a request: %+v", req)
		}
	})

	t.Run("no candidate asks the caller to supply the product", func(t *testing.T) {
		resolution, err := ResolveDraft(assistantApprovedAll(), draft, AssistantFixtureClock(), nil)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if resolution.Status != models.ResolveStatusNeedsInput || len(resolution.Candidates) != 0 {
			t.Fatalf("resolution = %+v", resolution)
		}
		if !assistantContains(resolution.MissingFields, assistantProductIdentityField) {
			t.Fatalf("missing_fields = %v", resolution.MissingFields)
		}

		req, err := ValidateDraftRequest(assistantApprovedAll(), draft, AssistantFixtureClock(), nil)
		if got := assistantErrorCode(t, err); got != models.ErrorCodeMissingField {
			t.Fatalf("code = %s want missing_field (%v)", got, err)
		}
		if !reflect.DeepEqual(req, models.AnalysisRequest{}) {
			t.Fatalf("missing product must not yield a request: %+v", req)
		}
	})

	t.Run("a draft without metrics or grouping asks for input", func(t *testing.T) {
		incomplete := models.DraftRequest{
			SchemaVersion: models.AssistantSchemaVersion,
			RequestID:     "draft-2",
			Workflow:      models.WorkflowSalesComparison,
		}
		resolution, err := ResolveDraft(assistantApprovedAll(), incomplete, AssistantFixtureClock(), nil)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		want := []string{"group_by", "metrics"}
		if !reflect.DeepEqual(resolution.MissingFields, want) {
			t.Fatalf("missing_fields = %v want %v", resolution.MissingFields, want)
		}
	})

	t.Run("a malformed draft is an error, not needs_input", func(t *testing.T) {
		badWorkflow := draft
		badWorkflow.Workflow = "inventory_review"
		_, err := ResolveDraft(assistantApprovedAll(), badWorkflow, AssistantFixtureClock(), nil)
		if got := assistantErrorCode(t, err); got != models.ErrorCodeInvalidEnum {
			t.Fatalf("code = %s want invalid_enum (%v)", got, err)
		}

		noIdentity := draft
		noIdentity.RequestID = ""
		_, err = ResolveDraft(assistantApprovedAll(), noIdentity, AssistantFixtureClock(), nil)
		if got := assistantErrorCode(t, err); got != models.ErrorCodeMissingField {
			t.Fatalf("code = %s want missing_field for request_id (%v)", got, err)
		}
	})
}

func TestAssistantValidationResolutionShape(t *testing.T) {
	valid, err := assistantValidatePayload(assistantApprovedAll(), assistantRequestPayload(""))
	if err != nil {
		t.Fatalf("build valid request: %v", err)
	}

	if err := ValidateResolution(models.Resolution{Status: models.ResolveStatusResolved, Request: &valid}); err != nil {
		t.Fatalf("valid resolved resolution rejected: %v", err)
	}
	if err := ValidateResolution(models.Resolution{
		Status:        models.ResolveStatusNeedsInput,
		MissingFields: []string{"metrics"},
	}); err != nil {
		t.Fatalf("valid needs_input resolution rejected: %v", err)
	}

	cases := []struct {
		name       string
		resolution models.Resolution
		wantCode   models.ValidationErrorCode
	}{
		{"unknown status", models.Resolution{Status: "running"}, models.ErrorCodeInvalidEnum},
		{"resolved without a request", models.Resolution{Status: models.ResolveStatusResolved}, models.ErrorCodeInvalidSchema},
		{"resolved with candidates", models.Resolution{
			Status:     models.ResolveStatusResolved,
			Request:    &valid,
			Candidates: []models.Candidate{{EntityType: models.EntityTypeProduct, EntityID: "P1"}},
		}, models.ErrorCodeInvalidSchema},
		{"needs_input with a request", models.Resolution{
			Status:        models.ResolveStatusNeedsInput,
			Request:       &valid,
			MissingFields: []string{"metrics"},
		}, models.ErrorCodeInvalidSchema},
		{"needs_input with nothing to answer", models.Resolution{Status: models.ResolveStatusNeedsInput}, models.ErrorCodeInvalidSchema},
		{"candidate with a bad entity type", models.Resolution{
			Status:     models.ResolveStatusNeedsInput,
			Candidates: []models.Candidate{{EntityType: "warehouse", EntityID: "W1"}},
		}, models.ErrorCodeInvalidEnum},
		{"candidate without an id", models.Resolution{
			Status:     models.ResolveStatusNeedsInput,
			Candidates: []models.Candidate{{EntityType: models.EntityTypeProduct}},
		}, models.ErrorCodeInvalidSchema},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := assistantErrorCode(t, ValidateResolution(c.resolution)); got != c.wantCode {
				t.Fatalf("code = %s want %s", got, c.wantCode)
			}
		})
	}
}
