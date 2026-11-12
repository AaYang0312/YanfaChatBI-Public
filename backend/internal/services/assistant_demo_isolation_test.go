package services

import (
	"context"
	"testing"

	"yanfachatbi/internal/models"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestAssistantRuntimeDoesNotUseDemoCatalogForBusinessChat(t *testing.T) {
	for _, tc := range []struct {
		name, database, host string
		demo                 bool
		wantAllowed          bool
	}{
		{name: "real chat despite opt-in", database: "bi_agent", demo: true},
		{name: "demo disabled", database: "yanfa_bi_demo"},
		{name: "explicit demo", database: "yanfa_bi_demo", host: "yanfachatbi-spreadsheet-postgres", demo: true, wantAllowed: true},
		{name: "wrong endpoint same name", database: "yanfa_bi_demo", host: "other-postgres", demo: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, id := primitive.NewObjectID(), primitive.NewObjectID()
			port, role := "5432", "yanfa_demo_reader"
			chats := assistantServiceChatLookup{chat: &models.Chat{
				UserID: owner, Base: models.Base{ID: id},
				Connection: models.Connection{Type: "postgresql", Database: tc.database, Host: tc.host, Port: &port, Username: &role},
			}}
			svc := NewAssistantService(chats, nil, nil, nil, NewAssistantFixtureCatalog(),
				NewAssistantFixtureSource(), nil, NewUnconfiguredSourceAccessService(nil),
				WithAssistantDemoIsolation(tc.demo))
			access := models.AccessContext{OwnerID: owner.Hex(), ChatID: id.Hex()}
			draft := models.DraftRequest{SchemaVersion: models.AssistantSchemaVersion, RequestID: "candidate-check", Workflow: models.WorkflowProductAnalysis,
				ProductQuery: AssistantFixtureProduct, Metrics: []models.MetricID{models.MetricQuantity}}
			result, err := svc.Resolve(context.Background(), access, draft)
			if tc.wantAllowed {
				if err != nil || result.Request == nil || result.Request.Product.ProductID != AssistantFixtureProductID {
					t.Fatalf("explicit demo did not resolve fixture: %+v, %v", result, err)
				}
				return
			}
			if validation, ok := models.AsValidationError(err); !ok || validation.Code != models.ErrorCodeCapabilityUnavailable {
				t.Fatalf("expected unavailable real workflow, got %+v, %v", result, err)
			}
			if _, err := svc.CreateRun(context.Background(), access, assistantServiceRequest("denied")); err == nil {
				t.Fatal("unconfigured workflow accepted a run")
			}
		})
	}
}
