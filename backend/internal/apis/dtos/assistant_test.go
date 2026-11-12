package dtos

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"yanfachatbi/internal/models"
)

const assistantValidRequestJSON = `{
	"schema_version": 1,
	"request_id": "req-1",
	"workflow": "product_analysis",
	"basis": "payment",
	"period": {"start_date": "2026-09-01", "end_date_exclusive": "2026-09-08", "timezone": "Asia/Shanghai"},
	"trend_period": {"start_date": "2026-08-25", "end_date_exclusive": "2026-09-01"},
	"scope": {"mode": "selected", "platform_ids": ["P1"], "shop_ids": ["S1"]},
	"product": {"product_id": "P1-SKU-1", "sku_ids": ["SKU-1"]},
	"group_by": "shop",
	"metrics": ["quantity", "sales", "cost", "margin", "margin_rate", "avg_price"],
	"low_margin_threshold": "0.15",
	"stream_id": "stream-1",
	"parent_run_id": "run-old"
}`

func TestAssistantValidationWireFieldSets(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected []string
	}{
		{"AnalysisRequest", AnalysisRequest{}, []string{
			"schema_version", "request_id", "workflow", "basis", "period", "trend_period",
			"scope", "product", "group_by", "metrics", "low_margin_threshold", "stream_id",
			"parent_run_id",
		}},
		{"DraftRequest", DraftRequest{}, []string{
			"schema_version", "request_id", "workflow", "basis", "period", "trend_period",
			"scope", "product_query", "product", "group_by", "metrics", "low_margin_threshold",
			"stream_id", "parent_run_id",
		}},
		{"AssistantPeriod", AssistantPeriod{}, []string{"start_date", "end_date_exclusive", "timezone"}},
		{"AssistantDateRange", AssistantDateRange{}, []string{"start_date", "end_date_exclusive"}},
		{"AssistantScope", AssistantScope{}, []string{"mode", "platform_ids", "shop_ids"}},
		{"AssistantProduct", AssistantProduct{}, []string{"product_id", "sku_ids"}},
	}

	for _, c := range cases {
		got := wireFields(c.value)
		want := append([]string(nil), c.expected...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s wire fields = %v, want %v", c.name, got, want)
		}
	}
}

func TestAssistantValidationAnalysisRequestDecoding(t *testing.T) {
	t.Run("valid payload converts to the domain request", func(t *testing.T) {
		wire, err := DecodeAssistantAnalysisRequest([]byte(assistantValidRequestJSON))
		if err != nil {
			t.Fatalf("decode valid request: %v", err)
		}
		req, err := wire.ToDomain()
		if err != nil {
			t.Fatalf("to domain: %v", err)
		}
		if req.Period.StartDate != "2026-09-01" || req.Period.EndDateExclusive != "2026-09-08" {
			t.Fatalf("period = %+v", req.Period)
		}
		if req.Period.Timezone != "Asia/Shanghai" || req.Basis != models.BasisPayment {
			t.Fatalf("period.timezone = %q basis = %q", req.Period.Timezone, req.Basis)
		}
		if req.TrendPeriod == nil || req.TrendPeriod.StartDate != "2026-08-25" {
			t.Fatalf("trend_period = %+v", req.TrendPeriod)
		}
		if req.Scope.Mode != models.ScopeModeSelected || len(req.Scope.ShopIDs) != 1 {
			t.Fatalf("scope = %+v", req.Scope)
		}
		if req.Product.ProductID != "P1-SKU-1" || len(req.Product.SkuIDs) != 1 {
			t.Fatalf("product = %+v", req.Product)
		}
		if len(req.Metrics) != 6 || req.Metrics[5] != models.MetricAveragePrice {
			t.Fatalf("metrics = %v", req.Metrics)
		}
		// The decimal string is kept exactly: no rounding, no float64.
		if req.LowMarginThreshold == nil || req.LowMarginThreshold.String() != "0.15" {
			t.Fatalf("low_margin_threshold = %v", req.LowMarginThreshold)
		}

		// Emitted wire shape round-trips into the same field set.
		raw, err := json.Marshal(NewAnalysisRequest(req))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		again, err := DecodeAssistantAnalysisRequest(raw)
		if err != nil {
			t.Fatalf("re-decode: %v\n%s", err, raw)
		}
		if again.LowMarginThreshold == nil || *again.LowMarginThreshold != "0.15" {
			t.Fatalf("threshold must serialize as a string, got %+v", again.LowMarginThreshold)
		}
	})

	t.Run("unknown and forbidden fields fail unknown_field", func(t *testing.T) {
		cases := map[string]string{
			"client user id":        `{"schema_version":1,"request_id":"r","user_id":"u1"}`,
			"client owner id":       `{"schema_version":1,"request_id":"r","owner_id":"o1"}`,
			"connection id":         `{"schema_version":1,"request_id":"r","connection_id":"c1"}`,
			"approved scope":        `{"schema_version":1,"request_id":"r","approved_scope":{"all":true}}`,
			"arbitrary sql":         `{"schema_version":1,"request_id":"r","sql":"SELECT * FROM orders"}`,
			"arbitrary query":       `{"schema_version":1,"request_id":"r","query":"drop table shops"}`,
			"arbitrary columns":     `{"schema_version":1,"request_id":"r","columns":["order_items.total"]}`,
			"arbitrary schema name": `{"schema_version":1,"request_id":"r","schema":"bi"}`,
			"nested table name":     `{"schema_version":1,"request_id":"r","scope":{"mode":"all","table_name":"orders"}}`,
			"credential":            `{"schema_version":1,"request_id":"r","password":"secret"}`,
			"case-folded identity":  `{"schema_version":1,"request_id":"r","UserID":"u1"}`,
			"unknown metric key":    `{"schema_version":1,"request_id":"r","matrics":["sales"]}`,
			"trend period timezone": `{"schema_version":1,"request_id":"r","trend_period":{"start_date":"2026-08-25","end_date_exclusive":"2026-09-01","timezone":"UTC"}}`,
		}
		for name, payload := range cases {
			got, err := DecodeAssistantAnalysisRequest([]byte(payload))
			if err == nil {
				t.Fatalf("%s: decoded %v, want unknown_field", name, got)
			}
			if !reflect.DeepEqual(got, AnalysisRequest{}) {
				t.Fatalf("%s: returned a partially decoded request %+v", name, got)
			}
			validationErr, ok := models.AsValidationError(err)
			if !ok {
				t.Fatalf("%s: error %v is not a *models.ValidationError", name, err)
			}
			if validationErr.Code != models.ErrorCodeUnknownField {
				t.Fatalf("%s: code = %s want unknown_field (%s)", name, validationErr.Code, validationErr.Message)
			}
			if validationErr.Field == "" {
				t.Fatalf("%s: unknown field name is not reported", name)
			}
		}
	})

	t.Run("malformed types fail invalid_schema", func(t *testing.T) {
		cases := map[string]string{
			"string schema version": `{"schema_version":"1","request_id":"r"}`,
			"numeric decimal":       `{"schema_version":1,"request_id":"r","low_margin_threshold":0.15}`,
			"period as string":      `{"schema_version":1,"request_id":"r","period":"2026-09-01"}`,
			"metrics as string":     `{"schema_version":1,"request_id":"r","metrics":"sales"}`,
			"empty body":            ``,
			"broken json":           `{"schema_version":1,`,
			"decimal not a number":  `{"schema_version":1,"request_id":"r","low_margin_threshold":"cheap"}`,
			"nested type error":     `{"schema_version":1,"request_id":"r","scope":{"mode":{"a":1}}}`,
			"array root":            `[]`,
		}
		for name, payload := range cases {
			wire, err := DecodeAssistantAnalysisRequest([]byte(payload))
			if err == nil {
				// A well-typed body can still carry an unusable decimal string.
				_, err = wire.ToDomain()
			}
			if err == nil {
				t.Fatalf("%s: decoded, want invalid_schema", name)
			}
			validationErr, ok := models.AsValidationError(err)
			if !ok {
				t.Fatalf("%s: error %v is not a *models.ValidationError", name, err)
			}
			if validationErr.Code != models.ErrorCodeInvalidSchema {
				t.Fatalf("%s: code = %s message = %s, want invalid_schema", name, validationErr.Code, validationErr.Message)
			}
		}
	})
}

func TestAssistantValidationDraftDecoding(t *testing.T) {
	t.Run("draft keeps unresolved product text", func(t *testing.T) {
		payload := `{"schema_version":1,"request_id":"draft-1","workflow":"product_analysis","product_query":"双层玻璃杯 销量"}`
		wire, err := DecodeAssistantDraftRequest([]byte(payload))
		if err != nil {
			t.Fatalf("decode draft: %v", err)
		}
		draft, err := wire.ToDomain()
		if err != nil {
			t.Fatalf("to domain: %v", err)
		}
		if draft.ProductQuery != "双层玻璃杯 销量" {
			t.Fatalf("product_query = %q", draft.ProductQuery)
		}
		if draft.Period != nil || draft.Scope != nil || draft.Product != nil {
			t.Fatalf("absent optional objects must stay nil: %+v", draft)
		}
		if len(wire.Metrics) != 0 || wire.LowMarginThreshold != nil {
			t.Fatalf("optional fields decoded: %+v", wire)
		}

		raw, err := json.Marshal(NewDraftRequest(draft))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(raw), `"product_query"`) {
			t.Fatalf("draft wire must carry product_query, got %s", raw)
		}
		again, err := DecodeAssistantDraftRequest(raw)
		if err != nil {
			t.Fatalf("re-decode draft: %v\n%s", err, raw)
		}
		if !reflect.DeepEqual(again, wire) {
			t.Fatalf("round trip changed the draft: %+v vs %+v", again, wire)
		}
	})

	t.Run("draft rejects identity and sql", func(t *testing.T) {
		cases := map[string]string{
			"user id":   `{"schema_version":1,"request_id":"d","product_query":"杯子","user_id":"u1"}`,
			"chat id":   `{"schema_version":1,"request_id":"d","product_query":"杯子","chat_id":"c1"}`,
			"sql":       `{"schema_version":1,"request_id":"d","product_query":"杯子","statement":"SELECT 1"}`,
			"dsn":       `{"schema_version":1,"request_id":"d","product_query":"杯子","dsn":"postgres://u:p@h/db"}`,
			"order by":  `{"schema_version":1,"request_id":"d","product_query":"杯子","order_by":"sales desc"}`,
			"row limit": `{"schema_version":1,"request_id":"d","product_query":"杯子","limit":100}`,
			"schema in": `{"schema_version":1,"request_id":"d","product_query":"杯子","scope":{"mode":"all","schema_name":"reporting"}}`,
		}
		for name, payload := range cases {
			got, err := DecodeAssistantDraftRequest([]byte(payload))
			if err == nil {
				t.Fatalf("%s: decoded %+v, want unknown_field", name, got)
			}
			validationErr, ok := models.AsValidationError(err)
			if !ok {
				t.Fatalf("%s: error %v is not a *models.ValidationError", name, err)
			}
			if validationErr.Code != models.ErrorCodeUnknownField {
				t.Fatalf("%s: code = %s want unknown_field", name, validationErr.Code)
			}
			if !reflect.DeepEqual(got, DraftRequest{}) {
				t.Fatalf("%s: returned a partially decoded draft %+v", name, got)
			}
		}
	})

	t.Run("confirmed product and query coexist", func(t *testing.T) {
		payload := `{"schema_version":1,"request_id":"d","workflow":"product_analysis","product_query":"双层玻璃杯","product":{"product_id":"P1"}}`
		wire, err := DecodeAssistantDraftRequest([]byte(payload))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		draft, err := wire.ToDomain()
		if err != nil {
			t.Fatalf("to domain: %v", err)
		}
		if draft.Product == nil || draft.Product.ProductID != "P1" {
			t.Fatalf("confirmed product lost: %+v", draft.Product)
		}
	})
}

func TestAssistantValidationForbiddenFieldList(t *testing.T) {
	list := ForbiddenAssistantRequestFields()
	if len(list) == 0 {
		t.Fatal("forbidden field list is empty")
	}
	for _, allowed := range []string{"product_query", "product_id", "sku_ids", "group_by", "schema_version", "basis", "stream_id", "parent_run_id"} {
		for _, forbidden := range list {
			if forbidden == allowed {
				t.Fatalf("%q is a contract field and must not be on the forbidden list", allowed)
			}
		}
	}
}

// wireFields returns the sorted JSON key names of a struct type.
func wireFields(value any) []string {
	var out []string
	t := reflect.TypeOf(value)
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
