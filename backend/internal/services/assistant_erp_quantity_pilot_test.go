package services

import (
	"context"
	"slices"
	"testing"
	"time"

	"yanfachatbi/internal/models"
)

func TestAssistantERPQuantityPilotValidation(t *testing.T) {
	base := models.AnalysisRequest{Basis: models.BasisPayment, Metrics: []models.MetricID{models.MetricQuantity}}
	if err := validateAssistantERPQuantityPilot(base); err != nil {
		t.Fatalf("approved request refused: %v", err)
	}
	cases := map[string]func(*models.AnalysisRequest){
		"outstock": func(r *models.AnalysisRequest) { r.Basis = models.BasisOutstock },
		"refund": func(r *models.AnalysisRequest) {
			r.Metrics = []models.MetricID{models.MetricQuantity, models.MetricRefund}
		},
		"sku":      func(r *models.AnalysisRequest) { r.Product.SkuIDs = []string{"not-a-verified-sku"} },
		"timezone": func(r *models.AnalysisRequest) { r.Period.Timezone = "UTC" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := base
			mutate(&req)
			err := validateAssistantERPQuantityPilot(req)
			if e, ok := models.AsValidationError(err); !ok || e.Code != models.ErrorCodeCapabilityUnavailable {
				t.Fatalf("unapproved contract returned %v", err)
			}
		})
	}
}

// The allow-list is server configured and cannot be inferred from a database name.
func TestAssistantERPQuantityPilotPinsApprovedSource(t *testing.T) {
	const approved = "017f4f30-4d33-4cf0-97bf-08f88fca0001"
	const other = "017f4f30-4d33-4cf0-97bf-08f88fca0002"
	scope := []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7"}
	order := make(map[string]string, len(scope))
	for _, shop := range scope {
		order[shop] = "erp.trade.list.query"
	}
	record := &models.ERPSource{SourceID: approved, ShopScope: scope, OrderSourceByShop: order}
	read := &SourceRead{Classification: models.SourceRegisteredERP, Source: record}
	svc := &assistantService{}
	if svc.useERPQuantityPilot(read) {
		t.Fatal("pilot should be disabled by default")
	}
	digest := assistantERPScopeDigest(order)
	WithAssistantERPQuantityPilot(other, digest)(svc)
	if svc.useERPQuantityPilot(read) {
		t.Fatal("wrong source passed pilot gate")
	}
	WithAssistantERPQuantityPilot(approved, digest)(svc)
	if !svc.useERPQuantityPilot(read) {
		t.Fatal("approved frozen scope rejected")
	}
	read.Classification = models.SourceIndependent
	if svc.useERPQuantityPilot(read) {
		t.Fatal("independent source passed pilot gate")
	}
	read.Classification = models.SourceRegisteredERP
	order["s1"] = "erp.trade.outstock.simple.query"
	if svc.useERPQuantityPilot(read) {
		t.Fatal("substituted order source passed frozen-scope check")
	}
	delete(order, "s1")
	order["outside"] = "erp.trade.list.query"
	record.ShopScope[0] = "outside"
	if svc.useERPQuantityPilot(read) {
		t.Fatal("substituted shop passed frozen-scope check")
	}
	delete(order, "outside")
	order["s1"] = "erp.trade.list.query"
	record.ShopScope[0] = record.ShopScope[1]
	if svc.useERPQuantityPilot(read) {
		t.Fatal("duplicate shop passed frozen-scope check")
	}
}

func TestAssistantERPQuantityIncompleteDraftDoesNotOpenReader(t *testing.T) {
	const id = "017f4f30-4d33-4cf0-97bf-08f88fca0001"
	shops := []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7"}
	order := make(map[string]string, len(shops))
	for _, shop := range shops {
		order[shop] = "erp.trade.list.query"
	}
	source := &models.ERPSource{SourceID: id, ShopScope: shops, OrderSourceByShop: order}
	read := &SourceRead{Classification: models.SourceRegisteredERP, Source: source}
	svc := &assistantService{now: time.Now, chats: assistantERPNoReaderLookup{t: t}}
	WithAssistantERPQuantityPilot(id, assistantERPScopeDigest(order))(svc)
	draft := models.DraftRequest{SchemaVersion: models.AssistantSchemaVersion, RequestID: "pilot-draft",
		Workflow: models.WorkflowProductAnalysis, ProductQuery: "fixture steel gun"}
	for _, missing := range []string{"metrics", "scope"} {
		if missing == "scope" {
			draft.Metrics = []models.MetricID{models.MetricQuantity}
			draft.Scope = &models.Scope{Mode: models.ScopeModeSelected}
		}
		result, err := svc.resolveAdmitted(context.Background(), models.AccessContext{}, draft, read)
		if err != nil || !slices.Contains(result.MissingFields, missing) || result.Request != nil || len(result.Candidates) != 0 {
			t.Fatalf("missing %s: got %+v, %v", missing, result, err)
		}
	}
}

type assistantERPNoReaderLookup struct{ t *testing.T }

func (f assistantERPNoReaderLookup) FindOwnedChat(context.Context, string, string) (*models.Chat, error) {
	f.t.Fatal("incomplete draft attempted to open its catalog reader")
	return nil, nil
}
