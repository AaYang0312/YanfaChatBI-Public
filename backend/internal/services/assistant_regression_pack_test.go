package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yanfachatbi/internal/apis/dtos"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/dbmanager"
	"yanfachatbi/pkg/llm"
	"yanfachatbi/pkg/mongodb"

	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestAssistantRegressionPack(t *testing.T) {
	if got := AssistantFixtureClock().Format(time.RFC3339); got != models.AssistantFixtureClockRFC3339 {
		t.Fatalf("fixture clock = %s", got)
	}
	cases := []struct {
		ac   string
		name string
		fn   func(*testing.T)
	}{
		{"AC01", "resolved product identity is the only report identity", regressionAC01},
		{"AC02", "ambiguous product lists candidates before any fact read", regressionAC02},
		{"AC03", "missing invalid and sql fields create no run", regressionAC03},
		{"AC04", "payment and outstock windows keep their own facts", regressionAC04},
		{"AC05", "fixed clock window excludes the exclusive boundary", regressionAC05},
		{"AC06", "basis switch stores a new run and leaves the old report", regressionAC06},
		{"AC07", "fixture shops persist without double counting", regressionAC07},
		{"AC08", "persisted fixture ids status and query count", regressionAC08},
		{"AC09", "zero denominator stays null", regressionAC09},
		{"AC10", "threshold hit is explicit", regressionAC10},
		{"AC11", "platform and shop runs share one fixture snapshot", regressionAC11},
		{"AC13", "uncollected shop is unavailable", regressionAC13},
		{"AC14", "missing entity is partial", regressionAC14},
		{"AC15", "empty and unavailable stay distinct", regressionAC15},
		{"AC16", "payment coverage does not satisfy outstock", regressionAC16},
		{"AC17", "trend gap is null", regressionAC17},
		{"AC18", "unverified cost nulls margin only", regressionAC18},
		{"AC19", "refund stays beside margin", regressionAC19},
		{"AC20", "shop refund is not a product zero", regressionAC20},
		{"AC21", "chart load does not reread the source", regressionAC21},
		{"AC22", "model numbers do not replace fixture metrics", regressionAC22},
		{"AC23", "model failure keeps the report", regressionAC23},
		{"AC24", "refresh reads the stored snapshot", regressionAC24},
		{"AC25", "failed persistence emits no completed event", regressionAC25},
		{"AC26", "old and new messages keep their run references", regressionAC26},
		{"AC27", "cancel is scoped to one run", regressionAC27},
		{"AC28", "one terminal state survives a late callback", regressionAC28},
		{"AC29", "restart interrupts only the unfinished run", regressionAC29},
		{"AC30", "foreign owner is rejected before a source read", regressionAC30},
		{"AC31", "injection stays a bound parameter", regressionAC31},
		{"AC32", "postgres read-only probe", regressionAC32},
		{"AC33", "encryption failure stores no plaintext", regressionAC33},
		{"AC34", "assistant report does not auto execute", regressionAC34},
	}
	seen := map[string]int{}
	for _, tc := range cases {
		seen[tc.ac]++
		t.Run(tc.ac+" "+tc.name, tc.fn)
	}
	for _, id := range []string{
		"AC01", "AC02", "AC03", "AC04", "AC05", "AC06", "AC07", "AC08", "AC09", "AC10", "AC11",
		"AC13", "AC14", "AC15", "AC16", "AC17", "AC18", "AC19", "AC20", "AC21", "AC22", "AC23",
		"AC24", "AC25", "AC26", "AC27", "AC28", "AC29", "AC30", "AC31", "AC32", "AC33", "AC34",
	} {
		if seen[id] != 1 {
			t.Fatalf("%s cases = %d, want 1", id, seen[id])
		}
	}
}

type regressionWorld struct {
	t        *testing.T
	userID   primitive.ObjectID
	chatID   primitive.ObjectID
	access   models.AccessContext
	source   *regressionSource
	messages *regressionMessages
	runs     *regressionRunStore
	notifier *regressionNotifier
	service  *assistantService
	nextRun  int
	runIDs   []string
}

func newRegressionWorld(t *testing.T, runIDs ...string) *regressionWorld {
	t.Helper()
	if len(runIDs) == 0 {
		runIDs = []string{"run-regression"}
	}
	userID := primitive.NewObjectID()
	chatID := primitive.NewObjectID()
	w := &regressionWorld{
		t:        t,
		userID:   userID,
		chatID:   chatID,
		access:   models.AccessContext{OwnerID: userID.Hex(), ChatID: chatID.Hex()},
		source:   &regressionSource{inner: NewAssistantFixtureSource()},
		messages: &regressionMessages{},
		runs:     newRegressionRunStore(),
		notifier: &regressionNotifier{},
		runIDs:   append([]string(nil), runIDs...),
	}
	w.service = w.build(&regressionRoutingCipher{store: w.runs, fallback: assistantServiceCipher(t)})
	return w
}

func (w *regressionWorld) build(cipher repositories.ReportCipher) *assistantService {
	w.t.Helper()
	chats := &assistantServiceChatLookup{chat: &models.Chat{UserID: w.userID, Base: models.Base{ID: w.chatID}}}
	return NewAssistantService(
		chats,
		w.messages,
		repositories.NewAssistantRunRepository(w.runs, cipher),
		cipher,
		NewAssistantFixtureCatalog(),
		w.source,
		nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantClock(AssistantFixtureClock),
		WithAssistantInstanceID("proc-regression"),
		WithAssistantRunIDGenerator(func() string {
			id := w.runIDs[w.nextRun]
			w.nextRun++
			return id
		}),
		WithAssistantRunNotifier(w.notifier),
	).(*assistantService)
}

func (w *regressionWorld) request(requestID string) models.AnalysisRequest {
	req := assistantFixtureWorkflowRequest()
	req.RequestID = requestID
	return req
}

func (w *regressionWorld) create(req models.AnalysisRequest) models.RunView {
	w.t.Helper()
	view, err := w.service.CreateRun(context.Background(), w.access, req)
	if err != nil {
		w.t.Fatalf("create %s: %v", req.RequestID, err)
	}
	return view
}

func (w *regressionWorld) wait(runID string) models.RunView {
	w.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		view, err := w.service.GetRun(context.Background(), w.access, runID)
		if err != nil {
			w.t.Fatalf("get %s: %v", runID, err)
		}
		if view.Status.IsTerminal() {
			return view
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("run %s stayed %s cause=%v", runID, view.Status, w.cause(runID))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (w *regressionWorld) cause(runID string) error {
	record, err := w.service.loadLocatedRun(context.Background(), runID)
	if err != nil {
		return err
	}
	_, execErr := w.service.executeWorkflow(context.Background(), record)
	return execErr
}

func (w *regressionWorld) completed(requestID string) models.RunView {
	w.t.Helper()
	created := w.create(w.request(requestID))
	view := w.wait(created.RunID)
	if view.Status != models.RunStatusCompleted || view.Report == nil {
		w.t.Fatalf("%s status=%s report=%v", requestID, view.Status, view.Report != nil)
	}
	if view.Report.ReportID != view.RunID {
		w.t.Fatalf("report id %s != run id %s", view.Report.ReportID, view.RunID)
	}
	return view
}

func regressionAC01(t *testing.T) {
	w := newRegressionWorld(t, "run-ac01")
	resolution, err := w.service.Resolve(context.Background(), w.access, models.DraftRequest{
		SchemaVersion: models.AssistantSchemaVersion,
		RequestID:     "req-ac01",
		Workflow:      models.WorkflowProductAnalysis,
		ProductQuery:  AssistantFixtureProduct,
		Metrics:       []models.MetricID{models.MetricQuantity},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != models.ResolveStatusResolved || resolution.Request == nil {
		t.Fatalf("resolution = %s", resolution.Status)
	}
	if resolution.Request.Product.ProductID != AssistantFixtureProductID {
		t.Fatalf("resolved product = %s", resolution.Request.Product.ProductID)
	}
	if w.source.factReads() != 0 {
		t.Fatalf("resolve fact reads = %d", w.source.factReads())
	}
	view := w.completed("req-ac01-run")
	if view.NormalizedInput.Product.ProductID != AssistantFixtureProductID {
		t.Fatalf("stored product = %s", view.NormalizedInput.Product.ProductID)
	}
	ids := map[string]bool{}
	for _, row := range view.Report.Table.Rows {
		ids[row.EntityID] = true
		if row.EntityID != AssistantFixtureShopS1 && row.EntityID != AssistantFixtureShopS2 {
			t.Fatalf("row %s is outside the fixture identity", row.EntityID)
		}
	}
	if !ids[AssistantFixtureShopS1] || !ids[AssistantFixtureShopS2] || len(view.Report.Table.Rows) != 2 {
		t.Fatalf("rows = %#v", ids)
	}
	if w.source.salesReads() != 1 {
		t.Fatalf("sales reads = %d, want 1", w.source.salesReads())
	}
}

func regressionAC02(t *testing.T) {
	w := newRegressionWorld(t, "run-ac02")
	w.service.catalog = regressionCatalog{{EntityType: models.EntityTypeProduct, EntityID: "P-CUP-A", Label: "A"}, {EntityType: models.EntityTypeProduct, EntityID: "P-CUP-B", Label: "B"}}
	resolution, err := w.service.Resolve(context.Background(), w.access, models.DraftRequest{
		SchemaVersion: models.AssistantSchemaVersion,
		RequestID:     "req-ac02",
		Workflow:      models.WorkflowProductAnalysis,
		ProductQuery:  "玻璃杯",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Status != models.ResolveStatusNeedsInput || resolution.Request != nil || len(resolution.Candidates) != 2 {
		t.Fatalf("resolution = %+v", resolution)
	}
	if resolution.Candidates[0].EntityID == "" || resolution.Candidates[0].EntityID == resolution.Candidates[1].EntityID {
		t.Fatalf("candidates collapsed: %+v", resolution.Candidates)
	}
	if w.source.factReads() != 0 || w.runs.count() != 0 {
		t.Fatalf("facts=%d runs=%d, want neither", w.source.factReads(), w.runs.count())
	}
	selected := w.request("req-ac02-selected")
	selected.Product.ProductID = resolution.Candidates[1].EntityID
	w.source.productID = resolution.Candidates[1].EntityID
	created := w.create(selected)
	view := w.wait(created.RunID)
	if view.Report == nil || view.NormalizedInput.Product.ProductID != resolution.Candidates[1].EntityID {
		t.Fatalf("selected identity was not kept: %+v", view.NormalizedInput.Product)
	}
	for _, row := range view.Report.Table.Rows {
		if row.EntityID != AssistantFixtureShopS1 && row.EntityID != AssistantFixtureShopS2 {
			t.Fatalf("selected result included %s", row.EntityID)
		}
	}
}

func regressionAC03(t *testing.T) {
	w := newRegressionWorld(t, "run-ac03")
	beforeMessages := w.messages.count()
	cases := []struct {
		name string
		req  models.AnalysisRequest
		code models.ValidationErrorCode
	}{
		{"missing product", models.AnalysisRequest{SchemaVersion: models.AssistantSchemaVersion, RequestID: "req-ac03-missing", Workflow: models.WorkflowProductAnalysis}, models.ErrorCodeMissingField},
		{"reversed dates", models.AnalysisRequest{SchemaVersion: models.AssistantSchemaVersion, RequestID: "req-ac03-dates", Workflow: models.WorkflowProductAnalysis, Period: models.Period{DateRange: models.DateRange{StartDate: "2026-09-08", EndDateExclusive: "2026-09-01"}, Timezone: models.AssistantDefaultTimezone}, Product: models.ProductSelection{ProductID: AssistantFixtureProductID}, Metrics: []models.MetricID{models.MetricQuantity}}, models.ErrorCodeInvalidDateRange},
		{"bad enum", models.AnalysisRequest{SchemaVersion: models.AssistantSchemaVersion, RequestID: "req-ac03-enum", Workflow: "inventory", Product: models.ProductSelection{ProductID: AssistantFixtureProductID}}, models.ErrorCodeInvalidEnum},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.service.CreateRun(context.Background(), w.access, tc.req)
			validation, ok := models.AsValidationError(err)
			if !ok || validation.Code != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
	if _, err := dtos.DecodeAssistantAnalysisRequest([]byte(`{"schema_version":1,"request_id":"req-ac03-sql","workflow":"product_analysis","sql":"select 1","product":{"product_id":"P-GLASS-500"}}`)); err == nil {
		t.Fatal("sql field decoded")
	} else if validation, ok := models.AsValidationError(err); !ok || validation.Code != models.ErrorCodeUnknownField {
		t.Fatalf("sql field error = %v", err)
	}
	if w.runs.count() != 0 || w.messages.count() != beforeMessages || w.source.factReads() != 0 {
		t.Fatalf("runs=%d messages=%d facts=%d", w.runs.count(), w.messages.count(), w.source.factReads())
	}
}

func regressionAC04(t *testing.T) {
	w := newRegressionWorld(t, "run-ac04-pay", "run-ac04-out")
	payment := w.request("req-ac04-pay")
	payment.Period.EndDateExclusive = "2026-09-03"
	outstock := w.request("req-ac04-out")
	outstock.Basis = models.BasisOutstock
	outstock.Period.StartDate = "2026-09-03"
	outstock.Period.EndDateExclusive = "2026-09-05"
	payView := w.wait(w.create(payment).RunID)
	outView := w.wait(w.create(outstock).RunID)
	if payView.Status != models.RunStatusCompleted || outView.Status != models.RunStatusCompleted {
		t.Fatalf("statuses %s %s", payView.Status, outView.Status)
	}
	if payView.NormalizedInput.Basis != models.BasisPayment || outView.NormalizedInput.Basis != models.BasisOutstock {
		t.Fatalf("stored bases %s %s", payView.NormalizedInput.Basis, outView.NormalizedInput.Basis)
	}
	if w.source.salesReads() != 2 {
		t.Fatalf("sales reads = %d, want one per basis", w.source.salesReads())
	}
	assertRegressionIdentity(t, payView.Report, "payment")
	assertRegressionIdentity(t, outView.Report, "outstock")
	if payView.Report.ReportID == outView.Report.ReportID {
		t.Fatal("basis windows shared one report")
	}
}

func regressionAC05(t *testing.T) {
	w := newRegressionWorld(t, "run-ac05")
	req := w.request("req-ac05")
	req.Period = models.Period{}
	view := w.wait(w.create(req).RunID)
	if view.NormalizedInput.Period.StartDate != models.AssistantDefaultStartDate || view.NormalizedInput.Period.EndDateExclusive != models.AssistantDefaultEndDateExclu || view.NormalizedInput.Period.Timezone != models.AssistantDefaultTimezone {
		t.Fatalf("stored window = %+v", view.NormalizedInput.Period)
	}
	if view.Report == nil {
		t.Fatal("report missing")
	}
	if got := view.Report.WindowEndExclusive.Format(time.RFC3339); got != "2026-09-07T16:00:00Z" {
		t.Fatalf("exclusive end = %s", got)
	}
	if view.Report.DataAsOf.In(time.FixedZone("CST", 8*3600)).Format(time.RFC3339) != models.AssistantFixtureClockRFC3339 {
		t.Fatalf("data_as_of = %s", view.Report.DataAsOf.Format(time.RFC3339))
	}
	for _, row := range w.source.rows() {
		if row.BusinessDate >= models.AssistantDefaultEndDateExclu {
			t.Fatalf("exclusive boundary row was returned: %+v", row)
		}
	}
}

func regressionAC06(t *testing.T) {
	w := newRegressionWorld(t, "run-ac06-old", "run-ac06-new")
	oldReq := w.request("req-ac06-old")
	oldReq.TrendPeriod = &models.DateRange{StartDate: "2026-08-25", EndDateExclusive: "2026-09-01"}
	old := w.wait(w.create(oldReq).RunID)
	newReq := w.request("req-ac06-new")
	newReq.Basis = models.BasisOutstock
	newReq.TrendPeriod = &models.DateRange{StartDate: "2026-08-18", EndDateExclusive: "2026-08-25"}
	next := w.wait(w.create(newReq).RunID)
	if old.RunID == next.RunID || old.NormalizedInput.Basis == next.NormalizedInput.Basis {
		t.Fatal("basis switch reused the old run")
	}
	reloaded, err := w.service.GetRun(context.Background(), w.access, old.RunID)
	if err != nil {
		t.Fatalf("reload old: %v", err)
	}
	if reloaded.NormalizedInput.Basis != models.BasisPayment || reloaded.Report == nil || reloaded.Report.Basis != models.BasisPayment {
		t.Fatalf("old report changed: %+v", reloaded.NormalizedInput.Basis)
	}
	if next.Report == nil || next.Report.Basis != models.BasisOutstock || len(next.Report.Charts) == 0 || len(next.Report.Table.Rows) == 0 {
		t.Fatalf("new report missing basis-specific table or charts")
	}
	if next.Report.ExplanationStatus == "" {
		t.Fatal("new report has no explanation state")
	}
}

func regressionAC07(t *testing.T) {
	w := newRegressionWorld(t, "run-ac07")
	view := w.completed("req-ac07")
	if len(view.Report.Table.Rows) != 2 {
		t.Fatalf("rows = %d, want the two fixture shops", len(view.Report.Table.Rows))
	}
	seen := map[string]int{}
	for _, row := range view.Report.Table.Rows {
		seen[row.EntityID]++
	}
	if seen[AssistantFixtureShopS1] != 1 || seen[AssistantFixtureShopS2] != 1 {
		t.Fatalf("shop rows = %#v", seen)
	}
	if w.source.salesReads() != 1 {
		t.Fatalf("sales reads = %d", w.source.salesReads())
	}
	stored := w.runs.must(t, view.RunID)
	plain, err := json.Marshal(view.Report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if bytes.Equal(stored.EncryptedResult, plain) || bytes.Contains(stored.EncryptedResult, []byte(`"entity_id":"`+AssistantFixtureShopS1+`"`)) {
		t.Fatal("shop identity was stored as plaintext")
	}
}

func regressionAC08(t *testing.T) {
	w := newRegressionWorld(t, "run-ac08")
	view := w.completed("req-ac08")
	if view.RunID != "run-ac08" || view.Status != models.RunStatusCompleted {
		t.Fatalf("run = %s/%s", view.RunID, view.Status)
	}
	if view.Report.ReportID != "run-ac08" || view.NormalizedInput.Product.ProductID != AssistantFixtureProductID {
		t.Fatalf("persisted identity mismatch")
	}
	if w.source.salesReads() != 1 || w.source.coverageReads() != 1 {
		t.Fatalf("query counts sales=%d coverage=%d", w.source.salesReads(), w.source.coverageReads())
	}
	raw, err := json.Marshal(view.Report.Totals)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(raw, []byte(`:120`)) || bytes.Contains(raw, []byte(`:0.4`)) {
		t.Fatalf("decimal leaked as a JSON number: %s", raw)
	}
}

func regressionAC09(t *testing.T) {
	w := newRegressionWorld(t, "run-ac09")
	req := w.request("req-ac09")
	req.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{AssistantFixtureShopS1}}
	w.source.mode = regressionModeZero
	view := w.wait(w.create(req).RunID)
	if view.Report == nil || view.Report.Outcome != models.OutcomeEmpty {
		t.Fatalf("outcome=%v cause=%v", view.Report, w.cause(view.RunID))
	}
	for _, id := range []models.MetricID{models.MetricAveragePrice, models.MetricMarginRate} {
		metric := regressionMetric(view.Report.Totals, id)
		if metric.Value != nil || metric.Status != models.MetricStatusUnavailable {
			t.Fatalf("%s = %+v, want null unavailable", id, metric)
		}
	}
	raw, _ := json.Marshal(view.Report.Totals)
	lower := strings.ToLower(string(raw))
	if strings.Contains(lower, "nan") || strings.Contains(lower, "infinity") {
		t.Fatalf("non-finite value serialized: %s", raw)
	}
}

func regressionAC10(t *testing.T) {
	w := newRegressionWorld(t, "run-ac10-none", "run-ac10-threshold")
	none := w.completed("req-ac10-none")
	if len(none.Report.Candidates) != 0 {
		t.Fatalf("threshold absent produced %d candidates", len(none.Report.Candidates))
	}
	for _, row := range none.Report.Table.Rows {
		if row.ThresholdHit {
			t.Fatalf("%s hit without a threshold", row.EntityID)
		}
	}
	threshold := decimal.RequireFromString("0.4")
	with := w.request("req-ac10-threshold")
	with.LowMarginThreshold = &threshold
	hit := w.wait(w.create(with).RunID)
	if hit.Status != models.RunStatusCompleted || hit.Report == nil {
		t.Fatalf("threshold status=%s report=%v cause=%v", hit.Status, hit.Report != nil, w.cause(hit.RunID))
	}
	seenS1, seenS2 := false, false
	for _, row := range hit.Report.Table.Rows {
		switch row.EntityID {
		case AssistantFixtureShopS1:
			seenS1 = true
			if row.ThresholdHit {
				t.Fatal("S1 must not hit threshold 0.4")
			}
		case AssistantFixtureShopS2:
			seenS2 = true
			if !row.ThresholdHit {
				t.Fatal("S2 must hit threshold 0.4")
			}
		default:
			if row.ThresholdHit {
				t.Fatalf("%s hit threshold 0.4", row.EntityID)
			}
		}
	}
	if !seenS1 || !seenS2 {
		t.Fatal("threshold report is missing S1 or S2")
	}
	if len(hit.Report.Candidates) != 1 || hit.Report.Candidates[0].EntityID != AssistantFixtureShopS2 {
		t.Fatalf("candidates = %d, want only S2", len(hit.Report.Candidates))
	}
	reloaded, err := w.service.GetRun(context.Background(), w.access, none.RunID)
	if err != nil || reloaded.NormalizedInput.LowMarginThreshold != nil {
		t.Fatal("threshold write changed the earlier run")
	}
}

func regressionAC11(t *testing.T) {
	w := newRegressionWorld(t, "run-ac11-shop", "run-ac11-platform")
	shop := w.request("req-ac11-shop")
	shop.Workflow = models.WorkflowSalesComparison
	shop.GroupBy = models.GroupByShop
	shop.Scope = models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}, ShopIDs: []string{AssistantFixtureShopS1, AssistantFixtureShopS2}}
	shop.Product = models.ProductSelection{}
	platform := shop
	platform.RequestID = "req-ac11-platform"
	platform.GroupBy = models.GroupByPlatform
	platform.Scope = models.Scope{Mode: models.ScopeModeSelected, PlatformIDs: []string{AssistantFixturePlatformID}}
	shopView := w.wait(w.create(shop).RunID)
	platformView := w.wait(w.create(platform).RunID)
	if shopView.Status != models.RunStatusCompleted || platformView.Status != models.RunStatusCompleted {
		t.Fatalf("statuses %s %s causes %v %v", shopView.Status, platformView.Status, w.cause(shopView.RunID), w.cause(platformView.RunID))
	}
	if shopView.Report.ReportID == platformView.Report.ReportID {
		t.Fatal("groupings shared one run")
	}
	if len(shopView.Report.Table.Rows) != 2 || len(platformView.Report.Table.Rows) != 1 {
		t.Fatalf("shop rows %d platform rows %d", len(shopView.Report.Table.Rows), len(platformView.Report.Table.Rows))
	}
	if platformView.Report.Table.Rows[0].EntityID != AssistantFixturePlatformID {
		t.Fatalf("platform row = %s", platformView.Report.Table.Rows[0].EntityID)
	}
	if w.source.salesReads() != 2 {
		t.Fatalf("sales reads = %d, want one per grouping", w.source.salesReads())
	}
}

func regressionAC13(t *testing.T) {
	w := newRegressionWorld(t, "run-ac13")
	w.source.mode = regressionModeUnknownShop
	req := w.request("req-ac13")
	req.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{AssistantFixtureShopS1, "S3"}}
	view := w.wait(w.create(req).RunID)
	if view.Status != models.RunStatusCompleted || view.Report == nil {
		t.Fatalf("status=%s report=%v cause=%v", view.Status, view.Report != nil, w.cause(view.RunID))
	}
	row := regressionRow(t, view.Report, "S3")
	for _, id := range []models.MetricID{models.MetricQuantity, models.MetricSales} {
		metric := regressionMetric(row.Metrics, id)
		if metric.Value != nil || metric.Status != models.MetricStatusUnavailable || !regressionHasReasonCode(metric.ReasonCodes, models.ReasonCoverageUnknown) {
			t.Fatalf("S3 %s = %+v, want null unavailable %s", id, metric, models.ReasonCoverageUnknown)
		}
	}
	if !regressionHasReasonCode(row.ReasonCodes, models.ReasonCoverageUnknown) {
		t.Fatalf("S3 row reasons = %v", row.ReasonCodes)
	}
	point := regressionBarPoint(t, view.Report, "S3")
	if point.Value != nil || point.Status != models.MetricStatusUnavailable || !regressionHasReasonCode(point.ReasonCodes, models.ReasonCoverageUnknown) {
		t.Fatalf("S3 chart = value %v status %s reasons %v, want null unavailable", point.Value, point.Status, point.ReasonCodes)
	}
	s1 := regressionMetric(regressionRow(t, view.Report, AssistantFixtureShopS1).Metrics, models.MetricSales)
	if s1.Value == nil || s1.Status == models.MetricStatusUnavailable {
		t.Fatalf("covered shop S1 was dropped: %+v", s1)
	}
}

func regressionAC14(t *testing.T) {
	w := newRegressionWorld(t, "run-ac14")
	req := w.request("req-ac14")
	req.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{AssistantFixtureShopS1, AssistantFixtureShopS2, "S9"}}
	view := w.wait(w.create(req).RunID)
	if view.Report == nil || view.Report.Outcome != models.OutcomePartial {
		t.Fatalf("outcome = %v", view.Report)
	}
	if !regressionHasEntity(view.Report.Coverage.Included, AssistantFixtureShopS1) || !regressionHasEntity(view.Report.Coverage.Excluded, "S9") {
		t.Fatalf("coverage included=%d excluded=%d", len(view.Report.Coverage.Included), len(view.Report.Coverage.Excluded))
	}
}

func regressionAC15(t *testing.T) {
	emptyWorld := newRegressionWorld(t, "run-ac15-empty")
	emptyReq := emptyWorld.request("req-ac15-empty")
	emptyReq.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{AssistantFixtureShopS1}}
	emptyWorld.source.mode = regressionModeZero
	empty := emptyWorld.wait(emptyWorld.create(emptyReq).RunID)
	missingWorld := newRegressionWorld(t, "run-ac15-missing")
	missingWorld.source.mode = regressionModeNoCoverage
	missingReq := missingWorld.request("req-ac15-missing")
	missingReq.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{AssistantFixtureShopS1, AssistantFixtureShopS2}}
	missing := missingWorld.wait(missingWorld.create(missingReq).RunID)
	if empty.Report == nil || empty.Report.Outcome != models.OutcomeEmpty {
		t.Fatalf("empty outcome=%v cause=%v", empty.Report, emptyWorld.cause(empty.RunID))
	}
	if missing.Report == nil || missing.Report.Outcome != models.OutcomeUnavailable {
		t.Fatalf("missing outcome=%v status=%s cause=%v", missing.Report, missing.Status, missingWorld.cause(missing.RunID))
	}
	if regressionMetric(missing.Report.Totals, models.MetricQuantity).Value != nil || regressionMetric(missing.Report.Totals, models.MetricSales).Value != nil {
		t.Fatal("missing coverage published quantity or sales")
	}
}

func regressionAC16(t *testing.T) {
	w := newRegressionWorld(t, "run-ac16")
	w.source.mode = regressionModePaymentOnly
	req := w.request("req-ac16")
	req.Basis = models.BasisOutstock
	view := w.wait(w.create(req).RunID)
	if view.Status != models.RunStatusFailed {
		t.Fatalf("status = %s, want failed closed", view.Status)
	}
	if view.Report != nil {
		t.Fatal("outstock request returned the payment snapshot")
	}
	if view.SafeError == nil || view.SafeError.Code == "" {
		t.Fatal("missing safe error")
	}
}

func regressionAC17(t *testing.T) {
	w := newRegressionWorld(t, "run-ac17")
	req := w.request("req-ac17")
	req.TrendPeriod = &models.DateRange{StartDate: "2026-08-25", EndDateExclusive: "2026-09-01"}
	view := w.wait(w.create(req).RunID)
	if view.Report == nil {
		t.Fatalf("report missing status=%s cause=%v", view.Status, w.cause(view.RunID))
	}
	if len(view.Report.Trend) == 0 {
		t.Fatal("trend missing")
	}
	var gap bool
	for _, point := range view.Report.Trend {
		if point.Value == nil {
			gap = true
		}
		if point.Value != nil && point.Value.IsZero() && regressionHasReasonCode(point.ReasonCodes, models.ReasonCoverageUnknown) {
			t.Fatalf("unknown trend date became zero: %+v", point)
		}
	}
	if !gap {
		t.Fatal("trend has no null gap")
	}
	for _, metric := range view.Report.Totals {
		if strings.Contains(string(metric.MetricID), "growth") {
			t.Fatalf("emitted %s", metric.MetricID)
		}
	}
}

func regressionAC18(t *testing.T) {
	w := newRegressionWorld(t, "run-ac18")
	w.source.mode = regressionModeUnverifiedCost
	view := w.completed("req-ac18")
	row := regressionRow(t, view.Report, AssistantFixtureShopS2)
	quantity := regressionMetric(row.Metrics, models.MetricQuantity)
	sales := regressionMetric(row.Metrics, models.MetricSales)
	margin := regressionMetric(row.Metrics, models.MetricMargin)
	if quantity.Value == nil || sales.Value == nil || margin.Value != nil {
		t.Fatalf("quantity=%v sales=%v margin=%v", quantity.Value, sales.Value, margin.Value)
	}
	if !regressionHasEntity(view.Report.Coverage.Excluded, AssistantFixtureShopS2) && !regressionHasReason(view.Report.Totals, models.ReasonCostUnverified) {
		t.Fatal("unverified cost was not excluded or marked")
	}
}

func regressionAC19(t *testing.T) {
	w := newRegressionWorld(t, "run-ac19")
	w.source.mode = regressionModeProductRefund
	view := w.completed("req-ac19")
	if view.Report.MarginLabel != models.AssistantMarginRefundLabel {
		t.Fatalf("margin label = %q", view.Report.MarginLabel)
	}
	margin := regressionMetric(view.Report.Totals, models.MetricMargin)
	refund := regressionMetric(view.Report.Totals, models.MetricRefund)
	if margin.Value == nil || margin.Value.String() != "48" {
		t.Fatalf("margin = %s, want 48 beside the refund", regressionDecimal(margin.Value))
	}
	if refund.Value == nil || refund.Value.String() != "10" || refund.Status != models.MetricStatusAvailable {
		t.Fatalf("refund = %+v, want available 10", refund)
	}
	if view.Report.Refunds.Status != models.MetricStatusAvailable || view.Report.Refunds.Amount == nil || view.Report.Refunds.Amount.String() != "10" || view.Report.Refunds.Label != models.AssistantMarginRefundLabel {
		t.Fatalf("refund summary = %+v", view.Report.Refunds)
	}
	if view.Report.Refunds.OccurrenceWindow != view.Report.Window.DateRange {
		t.Fatalf("refund window = %+v", view.Report.Refunds.OccurrenceWindow)
	}
}

func regressionAC20(t *testing.T) {
	w := newRegressionWorld(t, "run-ac20")
	w.source.mode = regressionModeShopRefund
	view := w.completed("req-ac20")
	if view.Report.Refunds.Level != models.EntityTypeShop && view.Report.Refunds.Level != models.EntityTypePlatform {
		t.Fatalf("refund level = %q, want shop or platform", view.Report.Refunds.Level)
	}
	if view.Report.Refunds.Amount == nil || view.Report.Refunds.Amount.String() != "10" {
		t.Fatalf("shop refund amount = %s, want 10", regressionDecimal(view.Report.Refunds.Amount))
	}
	if !regressionHasReasonCode(view.Report.Refunds.ReasonCodes, models.ReasonProductAttributionUnavailable) {
		t.Fatalf("refund reasons = %v", view.Report.Refunds.ReasonCodes)
	}
	if view.Report.Refunds.ProductRefund == nil || view.Report.Refunds.ProductRefund.Value != nil || view.Report.Refunds.ProductRefund.Status != models.MetricStatusUnavailable {
		t.Fatalf("product refund = %+v, want null unavailable", view.Report.Refunds.ProductRefund)
	}
	if !regressionHasReasonCode(view.Report.Refunds.ProductRefund.ReasonCodes, models.ReasonProductAttributionUnavailable) {
		t.Fatalf("product refund reasons = %v", view.Report.Refunds.ProductRefund.ReasonCodes)
	}
	s1 := regressionMetric(regressionRow(t, view.Report, AssistantFixtureShopS1).Metrics, models.MetricRefund)
	if s1.Value == nil || s1.Value.String() != "10" {
		t.Fatalf("S1 shop refund = %s, want 10", regressionDecimal(s1.Value))
	}
	for _, row := range view.Report.Table.Rows {
		if row.EntityID == AssistantFixtureShopS1 {
			continue
		}
		got := regressionMetric(row.Metrics, models.MetricRefund)
		if got.Value != nil && got.Value.IsZero() {
			t.Fatalf("row %s product refund is zero", row.EntityID)
		}
	}
}

func regressionAC21(t *testing.T) {
	w := newRegressionWorld(t, "run-ac21")
	view := w.completed("req-ac21")
	reads := w.source.salesReads()
	if len(view.Report.Charts) == 0 || len(view.Report.Table.Rows) == 0 {
		t.Fatal("report has no chart or table")
	}
	chart := view.Report.Charts[0]
	if chart.DataRef != "table.rows" || len(chart.Points) != len(view.Report.Table.Rows) {
		t.Fatalf("chart/table shape %s %d/%d", chart.DataRef, len(chart.Points), len(view.Report.Table.Rows))
	}
	for i, point := range chart.Points {
		row := view.Report.Table.Rows[i]
		tableValue := regressionMetric(row.Metrics, point.MetricID).Value
		if point.EntityID != row.EntityID || (point.Value == nil) != (tableValue == nil) {
			t.Fatalf("point %d diverges from row %s", i, row.EntityID)
		}
		if point.Value != nil && tableValue != nil && !point.Value.Equal(*tableValue) {
			t.Fatalf("point %d %s != row %s", i, point.Value, tableValue)
		}
	}
	if _, err := w.service.GetRun(context.Background(), w.access, view.RunID); err != nil {
		t.Fatalf("chart reload: %v", err)
	}
	if w.source.salesReads() != reads {
		t.Fatalf("chart reload sales reads %d -> %d", reads, w.source.salesReads())
	}
}

func regressionAC22(t *testing.T) {
	source := &regressionSource{inner: NewAssistantFixtureSource()}
	called := false
	workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), source, func(_ context.Context, input AssistantExplanationInput) ([]AssistantExplanationClaim, error) {
		called = true
		if !regressionExplanationHasMetric(input, models.MetricSales, "120") {
			t.Fatalf("lying model received %#v, want fixture sales 120", input.Metrics)
		}
		return []AssistantExplanationClaim{
			{
				MetricID:     models.MetricSales,
				Value:        "999",
				Text:         "sales 999",
				EvidenceRefs: []string{"snap:" + AssistantFixtureContractVersion},
			},
			{
				MetricID:     models.MetricCost,
				Value:        "72",
				Text:         "cost 72",
				EvidenceRefs: []string{"unknown:evidence"},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("NewAssistantWorkflow: %v", err)
	}
	report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-ac22"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if !called {
		t.Fatal("lying model was not invoked")
	}
	sales := regressionMetric(report.Totals, models.MetricSales)
	if sales.Value == nil || sales.Value.String() != "120" {
		t.Fatalf("sales = %s, want fixture 120", regressionDecimal(sales.Value))
	}
	if report.ExplanationStatus != models.ExplanationStatusRejectedEvidence || len(report.Explanations) != 0 {
		t.Fatalf("explanation = %s %#v", report.ExplanationStatus, report.Explanations)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(raw, []byte("999")) {
		t.Fatal("rejected model number 999 remained in the report")
	}
}

func regressionAC23(t *testing.T) {
	w := newRegressionWorld(t, "run-ac23")
	view := w.completed("req-ac23")
	if view.Report.ExplanationStatus != models.ExplanationStatusUnavailable || len(view.Report.Explanations) != 0 {
		t.Fatalf("nil model explanation = %s %#v", view.Report.ExplanationStatus, view.Report.Explanations)
	}
	if len(view.Report.Charts) == 0 || regressionMetric(view.Report.Totals, models.MetricSales).Value == nil {
		t.Fatal("unavailable model dropped the computed report")
	}
	if view.SafeError != nil {
		t.Fatalf("unavailable model became a run error: %+v", view.SafeError)
	}
}

func regressionAC24(t *testing.T) {
	w := newRegressionWorld(t, "run-ac24-first", "run-ac24-second")
	first := w.completed("req-ac24-first")
	w.source.mode = regressionModeZero
	reloaded, err := w.service.GetRun(context.Background(), w.access, first.RunID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Report == nil || reloaded.Report.Outcome != first.Report.Outcome || reloaded.Revision != first.Revision {
		t.Fatal("reload changed the stored snapshot")
	}
	if w.source.salesReads() != 1 {
		t.Fatalf("reload reread sales: %d", w.source.salesReads())
	}
	secondReq := w.request("req-ac24-second")
	secondReq.Period.EndDateExclusive = "2026-09-07"
	second := w.wait(w.create(secondReq).RunID)
	if second.RunID == first.RunID || second.Report == nil || second.NormalizedInput.Period.EndDateExclusive == first.NormalizedInput.Period.EndDateExclusive {
		t.Fatal("explicit rerun did not create a new snapshot")
	}
}
func regressionAC25(t *testing.T) {
	w := newRegressionWorld(t, "run-ac25-fail")
	w.service = w.build(&regressionFailingCipher{err: errors.New("encrypt failed")})
	created := w.create(w.request("req-ac25-fail"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := w.service.GetRun(context.Background(), w.access, created.RunID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if view.Status == models.RunStatusCompleted || view.Report != nil {
			t.Fatal("encryption failure completed")
		}
		if view.Status != models.RunStatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	failed, err := w.service.GetRun(context.Background(), w.access, created.RunID)
	if err != nil {
		t.Fatalf("get failed run: %v", err)
	}
	if failed.Status == models.RunStatusCompleted || failed.Report != nil {
		t.Fatal("encryption failure completed")
	}
	for _, event := range w.notifier.published() {
		if event.Status == models.RunStatusCompleted {
			t.Fatal("completed notification preceded persistence")
		}
	}
	ok := newRegressionWorld(t, "run-ac25-ok")
	view := ok.completed("req-ac25-ok")
	var matched bool
	for _, event := range ok.notifier.published() {
		if event.RunID == view.RunID && event.Status == models.RunStatusCompleted && event.Revision == view.Revision {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("no notification matched revision %d", view.Revision)
	}
}

func regressionAC26(t *testing.T) {
	old := &models.Message{ChatID: primitive.NewObjectID(), Type: "assistant", Content: "old", Queries: &[]models.Query{{Query: "select 1"}}, Base: models.NewBase()}
	response := BuildAssistantMessageResponse(old)
	if response.AssistantRunID != nil {
		t.Fatal("old message gained a run reference")
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal old: %v", err)
	}
	if bytes.Contains(encoded, []byte("assistant_run_id")) {
		t.Fatalf("old DTO contains assistant_run_id: %s", encoded)
	}
	w := newRegressionWorld(t, "run-ac26")
	view := w.completed("req-ac26")
	stored := w.messages.byRun(t, view.RunID)
	fresh := BuildAssistantMessageResponse(stored)
	if fresh.AssistantRunID == nil || *fresh.AssistantRunID != view.RunID || fresh.Queries != nil {
		t.Fatalf("new DTO = %#v", fresh)
	}
	reloaded, err := w.service.GetRun(context.Background(), w.access, *fresh.AssistantRunID)
	if err != nil || reloaded.RunID != view.RunID || reloaded.Report == nil {
		t.Fatalf("run restore failed: %v", err)
	}
}

func regressionAC27(t *testing.T) {
	userA := primitive.NewObjectID()
	userB := primitive.NewObjectID()
	chatA := primitive.NewObjectID()
	chatB := primitive.NewObjectID()
	chats := &assistantLifecycleChats{rows: map[string]*models.Chat{
		userA.Hex() + "/" + chatA.Hex(): {UserID: userA, Base: models.Base{ID: chatA}},
		userB.Hex() + "/" + chatB.Hex(): {UserID: userB, Base: models.Base{ID: chatB}},
	}}
	source := &assistantLifecycleBlockingSource{inner: NewAssistantFixtureSource(), entered: make(chan struct{}), released: make(chan struct{})}
	defer source.release()
	cipher := assistantServiceCipher(t)
	var next int
	ids := []string{"run-ac27-a", "run-ac27-b"}
	service := NewAssistantService(chats, assistantLifecycleMessages{}, repositories.NewAssistantRunRepository(newAssistantLifecycleRunCollection(), cipher), cipher, nil, source, nil,
		NewUnconfiguredSourceAccessService(nil),
		WithAssistantClock(AssistantFixtureClock),
		WithAssistantRunIDGenerator(func() string {
			id := ids[next]
			next++
			return id
		}),
	)
	reqA := assistantFixtureWorkflowRequest()
	reqA.RequestID = "req-ac27-a"
	reqA.StreamID = "stream-shared"
	reqB := reqA
	reqB.RequestID = "req-ac27-b"
	accessA := models.AccessContext{OwnerID: userA.Hex(), ChatID: chatA.Hex()}
	accessB := models.AccessContext{OwnerID: userB.Hex(), ChatID: chatB.Hex()}
	createdA, err := service.CreateRun(context.Background(), accessA, reqA)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	createdB, err := service.CreateRun(context.Background(), accessB, reqB)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	select {
	case <-source.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("source was not entered")
	}
	cancelled, err := service.CancelRun(context.Background(), accessA, createdA.RunID)
	if err != nil || cancelled.Status != models.RunStatusCancelled {
		t.Fatalf("cancel A: %v %s", err, cancelled.Status)
	}
	source.release()
	other, err := service.GetRun(context.Background(), accessB, createdB.RunID)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if other.Status == models.RunStatusCancelled {
		t.Fatal("cancelling A cancelled B")
	}
}

func regressionAC28(t *testing.T) {
	w := newRegressionWorld(t, "run-ac28")
	view := w.completed("req-ac28")
	_, err := w.service.runs.Transition(context.Background(), models.RunTransition{
		OwnerID:          view.OwnerID,
		ChatID:           view.ChatID,
		RunID:            view.RunID,
		From:             models.RunStatusRunning,
		To:               models.RunStatusFailed,
		ExpectedRevision: view.Revision,
		Stage:            models.RunStageFailed,
		SafeError:        &models.SafeRunError{Code: "late", Message: "late"},
		UpdatedAt:        AssistantFixtureClock(),
	})
	if !errors.Is(err, repositories.ErrTransitionConflict) {
		t.Fatalf("late callback = %v", err)
	}
	after, err := w.service.GetRun(context.Background(), w.access, view.RunID)
	if err != nil || after.Status != view.Status || after.Revision != view.Revision {
		t.Fatal("late callback rewrote the terminal run")
	}
}

func regressionAC29(t *testing.T) {
	userID := primitive.NewObjectID()
	chatID := primitive.NewObjectID()
	repo := repositories.NewAssistantRunRepository(newAssistantLifecycleRunCollection(), assistantServiceCipher(t))
	now := AssistantFixtureClock()
	orphan := models.RunRecord{SchemaVersion: models.AssistantSchemaVersion, RunID: "run-ac29-orphan", OwnerID: userID.Hex(), ChatID: chatID.Hex(), RequestID: "req-ac29-orphan", RequestFingerprint: "fp-ac29-orphan", MessageID: primitive.NewObjectID().Hex(), Status: models.RunStatusRunning, Stage: models.RunStageRunning, CreatedAt: now, UpdatedAt: now, ExecutingInstance: "proc-old", Version: models.RunVersion{SchemaVersion: models.AssistantSchemaVersion, MetricVersion: assistantMetricVersion}}
	if _, err := repo.CreateOrGet(context.Background(), orphan); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	terminal := orphan.Clone()
	terminal.RunID = "run-ac29-done"
	terminal.RequestID = "req-ac29-done"
	terminal.RequestFingerprint = "fp-ac29-done"
	terminal.MessageID = primitive.NewObjectID().Hex()
	seeded, err := repo.CreateOrGet(context.Background(), terminal)
	if err != nil {
		t.Fatalf("seed terminal: %v", err)
	}
	if _, err := repo.Transition(context.Background(), models.RunTransition{OwnerID: seeded.OwnerID, ChatID: seeded.ChatID, RunID: seeded.RunID, From: models.RunStatusRunning, To: models.RunStatusCompleted, ExpectedRevision: seeded.Revision, Stage: models.RunStageCompleted, ReportPlaintext: []byte(`{"report_id":"run-ac29-done"}`), UpdatedAt: now}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	source := &assistantServiceCountingSource{inner: NewAssistantFixtureSource()}
	_ = NewAssistantService(&assistantLifecycleChats{rows: map[string]*models.Chat{userID.Hex() + "/" + chatID.Hex(): {UserID: userID, Base: models.Base{ID: chatID}}}}, assistantLifecycleMessages{}, repo, assistantServiceCipher(t), nil, source, nil, NewUnconfiguredSourceAccessService(nil), WithAssistantInstanceID("proc-new"), WithAssistantClock(func() time.Time { return now }))
	got, err := repo.GetOwned(context.Background(), userID.Hex(), chatID.Hex(), orphan.RunID)
	if err != nil || got.Status != models.RunStatusInterrupted {
		t.Fatalf("orphan = %s %v", got.Status, err)
	}
	kept, err := repo.GetOwned(context.Background(), userID.Hex(), chatID.Hex(), terminal.RunID)
	if err != nil || kept.Status != models.RunStatusCompleted || len(kept.EncryptedResult) == 0 {
		t.Fatal("terminal report changed")
	}
	if source.calls() != 0 {
		t.Fatalf("restart source calls = %d", source.calls())
	}
}

func regressionAC30(t *testing.T) {
	w := newRegressionWorld(t, "run-ac30")
	created := w.completed("req-ac30")
	foreign := models.AccessContext{OwnerID: primitive.NewObjectID().Hex(), ChatID: w.chatID.Hex()}
	reads := w.source.callCount()
	if _, err := w.service.Resolve(context.Background(), foreign, models.DraftRequest{SchemaVersion: models.AssistantSchemaVersion, RequestID: "req-ac30-foreign", Workflow: models.WorkflowProductAnalysis, ProductQuery: AssistantFixtureProduct}); !IsAssistantNotFound(err) {
		t.Fatalf("foreign resolve = %v", err)
	}
	if _, err := w.service.CreateRun(context.Background(), foreign, w.request("req-ac30-foreign-run")); !IsAssistantNotFound(err) {
		t.Fatalf("foreign create = %v", err)
	}
	if _, err := w.service.GetRun(context.Background(), foreign, created.RunID); !IsAssistantNotFound(err) {
		t.Fatalf("foreign get = %v", err)
	}
	if _, err := w.service.CancelRun(context.Background(), foreign, created.RunID); !IsAssistantNotFound(err) {
		t.Fatalf("foreign cancel = %v", err)
	}
	executor := BuildToolExecutor(nil, foreign.OwnerID, foreign.ChatID, "postgresql", w.service)
	result, err := executor(context.Background(), llm.ToolCall{ID: "call-ac30", Name: llm.AnalyzeProductToolName, Arguments: map[string]interface{}{"request_id": "req-ac30-tool", "product_query": AssistantFixtureProduct}})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("foreign tool = %#v %v", result, err)
	}
	if w.source.callCount() != reads {
		t.Fatalf("foreign access source calls %d -> %d", reads, w.source.callCount())
	}
}

func regressionAC31(t *testing.T) {
	w := newRegressionWorld(t, "run-ac31")
	injection := "'; drop table shops; --"
	req := w.request("req-ac31")
	req.Product.ProductID = injection
	req.Scope = models.Scope{Mode: models.ScopeModeSelected, ShopIDs: []string{injection}}
	view := w.wait(w.create(req).RunID)
	if view.Status != models.RunStatusCompleted && view.Status != models.RunStatusFailed {
		t.Fatalf("status = %s", view.Status)
	}
	plans := w.source.inner.Plans()
	if len(plans) == 0 {
		t.Fatal("no captured plan")
	}
	var bound bool
	for _, plan := range plans {
		if strings.Contains(plan.Operation, " ") || strings.Contains(strings.ToLower(plan.Operation), "select") {
			t.Fatalf("plan operation became SQL: %s", plan.Operation)
		}
		for _, param := range plan.BoundParameters {
			if param == injection {
				bound = true
			}
			if strings.Count(param, injection) != 1 && strings.Contains(param, injection) {
				t.Fatalf("injection was concatenated: %q", param)
			}
		}
	}
	if !bound {
		t.Fatal("injection was not kept as one bound parameter")
	}
	if len(w.source.rows()) > 2 {
		t.Fatalf("injection widened fixture rows to %d", len(w.source.rows()))
	}
}

func regressionAC32(t *testing.T) {
	dsn := os.Getenv("ASSISTANT_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("Q05 unverified: ASSISTANT_TEST_PG_DSN is not set; the PostgreSQL read-only boundary (SQLSTATE 25006, cancellation, idle release) is not exercised")
	}
	reader, err := dbmanager.NewAssistantReadonlyReader(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	ctx := context.Background()
	if err := reader.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	probe, err := reader.RunWriteProbe(ctx, "'; drop table shops; --")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.WriteSQLState != "25006" || probe.Echo != "'; drop table shops; --" {
		t.Fatalf("probe = %+v", probe)
	}
	if err := reader.RunCancelProbe(ctx, "30", 3*time.Second); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if stats := reader.Stats(); stats.InUse != 0 {
		t.Fatalf("connection stayed in use: %+v", stats)
	}
}

func regressionAC33(t *testing.T) {
	w := newRegressionWorld(t, "run-ac33")
	w.service = w.build(&regressionFailingCipher{err: errors.New("encrypt failed")})
	created := w.create(w.request("req-ac33"))
	deadline := time.Now().Add(2 * time.Second)
	var view models.RunView
	for time.Now().Before(deadline) {
		var err error
		view, err = w.service.GetRun(context.Background(), w.access, created.RunID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if view.Status == models.RunStatusCompleted || view.Report != nil {
			t.Fatal("encryption failure published a report")
		}
		if view.Status != models.RunStatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if view.Status == models.RunStatusCompleted || view.Report != nil {
		t.Fatal("encryption failure published a report")
	}
	stored := w.runs.must(t, view.RunID)
	if len(stored.EncryptedResult) != 0 || len(stored.ReportPlaintext) != 0 {
		t.Fatal("plaintext or ciphertext was stored")
	}
	for _, event := range w.notifier.published() {
		if event.Status == models.RunStatusCompleted || event.Report != nil {
			t.Fatal("SSE carried a report after encryption failure")
		}
	}
	broken := &regressionOpaqueCipher{}
	ok := newRegressionWorld(t, "run-ac33-bad")
	ok.service = ok.build(broken)
	completed := ok.wait(ok.create(ok.request("req-ac33-bad")).RunID)
	if completed.Status != models.RunStatusCompleted {
		t.Fatalf("status = %s", completed.Status)
	}
	broken.fail = true
	if _, err := ok.service.GetRun(context.Background(), ok.access, completed.RunID); err == nil {
		t.Fatal("bad ciphertext decoded as a report")
	}
}

func regressionAC34(t *testing.T) {
	originalQuery := autoExecuteQuery
	originalResponse := processLLMResponseForAutoRun
	t.Cleanup(func() {
		autoExecuteQuery = originalQuery
		processLLMResponseForAutoRun = originalResponse
	})
	var calls int
	autoExecuteQuery = func(*chatService, context.Context, string, string, *dtos.ExecuteQueryRequest) (*dtos.QueryExecutionResponse, uint32, error) {
		calls++
		return &dtos.QueryExecutionResponse{}, 0, nil
	}
	w := newRegressionWorld(t, "run-ac34")
	view := w.completed("req-ac34")
	message := w.messages.byRun(t, view.RunID)
	if message.Queries != nil || message.AssistantRunID == nil {
		t.Fatal("persisted assistant message is executable")
	}
	response := BuildAssistantMessageResponse(message)
	processLLMResponseForAutoRun = func(*chatService, context.Context, string, string, string, string) (*dtos.MessageResponse, error) {
		return response, nil
	}
	chatID := w.chatID
	userID := w.userID
	service := &chatService{chatRepo: &assistantLifecycleChatRepo{chat: &models.Chat{UserID: userID, Base: models.Base{ID: chatID}, Settings: models.ChatSettings{AutoExecuteQuery: true}}}, activeProcesses: map[string]context.CancelFunc{}, streamHandler: assistantLifecycleStreamWait{}, sourceAccess: NewUnconfiguredSourceAccessService(nil)}
	if err := service.processLLMResponseAndRunQuery(context.Background(), userID.Hex(), chatID.Hex(), message.ID.Hex(), "stream-ac34"); err != nil {
		t.Fatalf("auto path: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for calls == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls != 0 {
		t.Fatalf("auto execution calls = %d", calls)
	}
	if _, err := w.service.GetRun(context.Background(), w.access, view.RunID); err != nil || view.Report == nil {
		t.Fatal("report disappeared")
	}
}

func regressionMongoPack(t *testing.T) {
	uri := os.Getenv("ASSISTANT_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("deployment unverified: ASSISTANT_TEST_MONGO_URI is not set; assistant_runs Mongo conditional updates are not exercised")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetRegistry(mongodb.BSONRegistry()))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	collection := client.Database("assistant_regression_pack").Collection("assistant_runs_" + primitive.NewObjectID().Hex())
	t.Cleanup(func() { _ = collection.Drop(context.Background()) })
	if err := repositories.EnsureAssistantRunIndexes(ctx, collection); err != nil {
		t.Fatalf("indexes: %v", err)
	}
	repo := repositories.NewAssistantRunRepository(collection, assistantServiceCipher(t))
	now := AssistantFixtureClock()
	run := models.RunRecord{SchemaVersion: models.AssistantSchemaVersion, RunID: "run-mongo-pack", OwnerID: "owner-pack", ChatID: "chat-pack", RequestID: "req-mongo-pack", RequestFingerprint: "fp-mongo-pack", MessageID: primitive.NewObjectID().Hex(), Status: models.RunStatusRunning, Stage: models.RunStageRunning, CreatedAt: now, UpdatedAt: now, ExecutingInstance: "proc-pack", Version: models.RunVersion{SchemaVersion: models.AssistantSchemaVersion, MetricVersion: assistantMetricVersion}}
	seeded, err := repo.CreateOrGet(ctx, run)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	errorsOut := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, err := repo.Transition(ctx, models.RunTransition{OwnerID: seeded.OwnerID, ChatID: seeded.ChatID, RunID: seeded.RunID, From: models.RunStatusRunning, To: models.RunStatusCompleted, ExpectedRevision: seeded.Revision, Stage: models.RunStageCompleted, ReportPlaintext: []byte(`{"report_id":"run-mongo-pack"}`), UpdatedAt: now})
		errorsOut <- err
	}()
	go func() {
		defer wg.Done()
		_, err := repo.Transition(ctx, models.RunTransition{OwnerID: seeded.OwnerID, ChatID: seeded.ChatID, RunID: seeded.RunID, From: models.RunStatusRunning, To: models.RunStatusCancelled, ExpectedRevision: seeded.Revision, Stage: models.RunStageCancelled, UpdatedAt: now})
		errorsOut <- err
	}()
	wg.Wait()
	close(errorsOut)
	var conflicts int
	for err := range errorsOut {
		if errors.Is(err, repositories.ErrTransitionConflict) {
			conflicts++
			continue
		}
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1", conflicts)
	}
	stored, err := repo.GetOwned(ctx, seeded.OwnerID, seeded.ChatID, seeded.RunID)
	if err != nil || !stored.Status.IsTerminal() {
		t.Fatalf("stored = %s %v", stored.Status, err)
	}
}

func TestAssistantRegressionMongo(t *testing.T) {
	regressionMongoPack(t)
}

type regressionMode string

const (
	regressionModeZero           regressionMode = "zero"
	regressionModeUnknownShop    regressionMode = "unknown-shop"
	regressionModeNoCoverage     regressionMode = "no-coverage"
	regressionModePaymentOnly    regressionMode = "payment-only"
	regressionModeUnverifiedCost regressionMode = "unverified-cost"
	regressionModeShopRefund     regressionMode = "shop-refund"
	regressionModeProductRefund  regressionMode = "product-refund"
)

type regressionSource struct {
	inner     *AssistantFixtureSource
	mode      regressionMode
	productID string
	mu        sync.Mutex
	n         int
	sales     int
	coverage  int
	refunds   int
	seenRows  []models.FactRow
}

func (s *regressionSource) hit(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	switch kind {
	case "sales":
		s.sales++
	case "coverage":
		s.coverage++
	case "refund":
		s.refunds++
	}
}

func (s *regressionSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *regressionSource) salesReads() int    { s.mu.Lock(); defer s.mu.Unlock(); return s.sales }
func (s *regressionSource) coverageReads() int { s.mu.Lock(); defer s.mu.Unlock(); return s.coverage }
func (s *regressionSource) refundReads() int   { s.mu.Lock(); defer s.mu.Unlock(); return s.refunds }
func (s *regressionSource) factReads() int {
	return s.salesReads() + s.coverageReads() + s.refundReads()
}

func (s *regressionSource) rows() []models.FactRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]models.FactRow(nil), s.seenRows...)
}

func (s *regressionSource) Capabilities(ctx context.Context) (models.SourceCapabilities, error) {
	s.hit("capabilities")
	caps, err := s.inner.Capabilities(ctx)
	if s.mode == regressionModePaymentOnly {
		caps.SupportedBases = []models.Basis{models.BasisPayment}
	}
	return caps, err
}

func (s *regressionSource) ResolveEntities(ctx context.Context, query repositories.AssistantEntityQuery) ([]models.Candidate, error) {
	s.hit("resolve")
	return s.inner.ResolveEntities(ctx, query)
}

func (s *regressionSource) ReadCoverage(ctx context.Context, query repositories.AssistantCoverageQuery) ([]repositories.AssistantCoverage, error) {
	s.hit("coverage")
	coverage, err := s.inner.ReadCoverage(ctx, query)
	if err != nil {
		return nil, err
	}
	switch s.mode {
	case regressionModeUnknownShop:
		for i := range coverage {
			if coverage[i].EntityID == "S3" {
				coverage[i].Status = models.CoverageStatusUnknown
			}
		}
	case regressionModeNoCoverage:
		for i := range coverage {
			coverage[i].Status = models.CoverageStatusMissing
		}
	}
	return coverage, nil
}

func (s *regressionSource) ReadSalesFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.hit("sales")
	snap, err := s.inner.ReadSalesFacts(ctx, query)
	if err != nil {
		return snap, err
	}
	selectedShops := map[string]struct{}{}
	for _, shopID := range query.Scope.ShopIDs {
		selectedShops[shopID] = struct{}{}
	}
	keep := func(entityID string) bool {
		if query.Scope.Mode != models.ScopeModeSelected || len(selectedShops) == 0 {
			return true
		}
		_, shopID := splitEntity(entityID)
		_, ok := selectedShops[shopID]
		return ok
	}
	switch s.mode {
	case regressionModeZero:
		filtered := snap.Rows[:0]
		for _, row := range snap.Rows {
			if !keep(row.EntityID) {
				continue
			}
			row.Quantity = decimal.Zero
			row.Sales = decimal.Zero
			row.Cost = decimal.Zero
			filtered = append(filtered, row)
		}
		snap.Rows = filtered
	case regressionModeNoCoverage:
		filtered := snap.Rows[:0]
		for _, row := range snap.Rows {
			if !keep(row.EntityID) {
				continue
			}
			row.CoverageStatus = models.CoverageStatusMissing
			row.Quantity = decimal.Zero
			row.Sales = decimal.Zero
			row.Cost = decimal.Zero
			filtered = append(filtered, row)
		}
		snap.Rows = filtered
	case regressionModeUnverifiedCost:
		for i := range snap.Rows {
			if _, shopID := splitEntity(snap.Rows[i].EntityID); shopID == AssistantFixtureShopS2 {
				snap.Rows[i].CostVerified = false
			}
		}
	case regressionModeUnknownShop:
		filtered := snap.Rows[:0]
		for _, row := range snap.Rows {
			if !keep(row.EntityID) {
				continue
			}
			filtered = append(filtered, row)
		}
		if _, selected := selectedShops["S3"]; selected {
			filtered = append(filtered, models.FactRow{
				EntityID:       "S3",
				Basis:          query.Basis,
				BusinessDate:   assistantFixtureDateS1Payment,
				Quantity:       decimal.Zero,
				Sales:          decimal.Zero,
				Cost:           decimal.Zero,
				CostVerified:   false,
				Currency:       AssistantFixtureCurrency,
				CoverageStatus: models.CoverageStatusUnknown,
				BundleRole:     models.BundleRoleNone,
			})
		}
		snap.Rows = filtered
	}
	if s.productID != "" && query.Product.ProductID != s.productID {
		snap.Rows = nil
	}
	s.mu.Lock()
	s.seenRows = append(s.seenRows, snap.Rows...)
	s.mu.Unlock()
	return snap, nil
}

func (s *regressionSource) ReadRefundFacts(ctx context.Context, query models.FactQuery) (models.FactSnapshot, error) {
	s.hit("refund")
	snap, err := s.inner.ReadRefundFacts(ctx, query)
	if s.mode == regressionModeZero || s.mode == regressionModeNoCoverage {
		snap.Rows = nil
		return snap, err
	}
	if s.productID != "" && query.Product.ProductID != s.productID {
		snap.Rows = nil
		return snap, err
	}
	if entityID, ok := regressionRefundCoverageID(s.mode); ok {
		snap.Rows = []models.FactRow{regressionRefundFact(entityID, query.Basis)}
		return snap, err
	}
	if len(snap.Rows) == 1 && snap.Rows[0].EntityID == "" {
		snap.Rows[0].EntityID = "refund:shop:" + AssistantFixtureShopS1
	}
	return snap, err
}

type regressionCatalog []models.Candidate

func (c regressionCatalog) FindProductCandidates(context.Context, models.AccessContext, string) ([]models.Candidate, error) {
	return append([]models.Candidate(nil), c...), nil
}

type regressionMessages struct {
	mu   sync.Mutex
	rows []*models.Message
}

func (m *regressionMessages) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *regressionMessages) InsertReportMessage(_ context.Context, message *models.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *message
	if message.AssistantRunID != nil {
		runID := *message.AssistantRunID
		copied.AssistantRunID = &runID
	}
	m.rows = append(m.rows, &copied)
	return nil
}

func (m *regressionMessages) FindReportMessage(_ context.Context, messageID string) (*models.Message, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, message := range m.rows {
		if message.ID.Hex() == messageID {
			copied := *message
			return &copied, true, nil
		}
	}
	return nil, false, nil
}

func (m *regressionMessages) byRun(t *testing.T, runID string) *models.Message {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, message := range m.rows {
		if message.AssistantRunID != nil && *message.AssistantRunID == runID {
			copied := *message
			return &copied
		}
	}
	t.Fatalf("message for %s not stored", runID)
	return nil
}

type regressionNotifier struct {
	mu    sync.Mutex
	items []models.RunView
}

func (n *regressionNotifier) PublishRunUpdate(_ context.Context, view models.RunView) error {
	n.mu.Lock()
	n.items = append(n.items, view)
	n.mu.Unlock()
	return nil
}

func (n *regressionNotifier) published() []models.RunView {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]models.RunView(nil), n.items...)
}

type regressionFailingCipher struct{ err error }

func (c *regressionFailingCipher) EncryptReport([]byte) ([]byte, error) { return nil, c.err }
func (c *regressionFailingCipher) DecryptReport([]byte) ([]byte, error) { return nil, c.err }

type regressionOpaqueCipher struct {
	mu   sync.Mutex
	fail bool
	seq  byte
}

func (c *regressionOpaqueCipher) EncryptReport(plain []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	return []byte{c.seq, byte(len(plain))}, nil
}

func (c *regressionOpaqueCipher) DecryptReport(ciphertext []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return nil, errors.New("ciphertext is not a report")
	}
	if len(ciphertext) == 0 || ciphertext[0] == '{' {
		return nil, errors.New("ciphertext is raw JSON")
	}
	return []byte(`{"report_id":"opaque"}`), nil
}

type regressionRoutingCipher struct {
	store    *regressionRunStore
	fallback repositories.ReportCipher
}

func (c *regressionRoutingCipher) current() repositories.ReportCipher {
	c.store.mu.Lock()
	override := c.store.cipher
	c.store.mu.Unlock()
	if override != nil {
		return override
	}
	return c.fallback
}

func (c *regressionRoutingCipher) EncryptReport(plain []byte) ([]byte, error) {
	return c.current().EncryptReport(plain)
}

func (c *regressionRoutingCipher) DecryptReport(ciphertext []byte) ([]byte, error) {
	return c.current().DecryptReport(ciphertext)
}

type regressionRunStore struct {
	mu       sync.Mutex
	rows     map[string]bson.Raw
	original map[string]models.RunRecord
	cipher   repositories.ReportCipher
}

func newRegressionRunStore() *regressionRunStore {
	return &regressionRunStore{rows: map[string]bson.Raw{}, original: map[string]models.RunRecord{}}
}

func (s *regressionRunStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.original)
}

func (s *regressionRunStore) records() []models.RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]models.RunRecord, 0, len(s.original))
	for _, record := range s.original {
		out = append(out, record.Clone())
	}
	return out
}

func (s *regressionRunStore) must(t *testing.T, runID string) models.RunRecord {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.original[runID]
	if !ok {
		t.Fatalf("run %s missing", runID)
	}
	return record.Clone()
}

func (s *regressionRunStore) raw(t *testing.T, runID string) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := s.rows[runID]
	if raw == nil {
		t.Fatalf("run %s missing", runID)
	}
	return append([]byte(nil), raw...)
}

func (s *regressionRunStore) InsertOne(_ context.Context, document interface{}, _ ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	record, ok := document.(models.RunRecord)
	if !ok {
		return nil, errors.New("regression store requires a run record")
	}
	raw, err := bson.MarshalWithRegistry(mongodb.BSONRegistry(), record)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.original {
		if existing.OwnerID == record.OwnerID && existing.ChatID == record.ChatID && (existing.RequestID == record.RequestID || existing.RunID == record.RunID) {
			return nil, mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 11000, Message: "E11000 duplicate key error"}}}
		}
	}
	s.rows[record.RunID] = raw
	s.original[record.RunID] = record.Clone()
	return &mongo.InsertOneResult{InsertedID: record.RunID}, nil
}

func (s *regressionRunStore) FindOne(_ context.Context, filter interface{}, _ ...*options.FindOneOptions) *mongo.SingleResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, record := range s.original {
		if regressionMatch(&record, filter) {
			return regressionResult(record.Clone(), s.rows[id])
		}
	}
	return mongo.NewSingleResultFromDocument(bson.D{}, mongo.ErrNoDocuments, nil)
}

func (s *regressionRunStore) FindOneAndUpdate(_ context.Context, filter interface{}, update interface{}, _ ...*options.FindOneAndUpdateOptions) *mongo.SingleResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, record := range s.original {
		if !regressionMatch(&record, filter) {
			continue
		}
		regressionApply(&record, update)
		encoded, err := bson.MarshalWithRegistry(mongodb.BSONRegistry(), record)
		if err != nil {
			return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
		}
		s.original[id] = record.Clone()
		s.rows[id] = encoded
		return regressionResult(record.Clone(), encoded)
	}
	return mongo.NewSingleResultFromDocument(bson.D{}, mongo.ErrNoDocuments, nil)
}

func (s *regressionRunStore) UpdateMany(context.Context, interface{}, interface{}, ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	return &mongo.UpdateResult{}, nil
}

func (s *regressionRunStore) Indexes() mongo.IndexView { return mongo.IndexView{} }

func regressionMatch(record *models.RunRecord, filter interface{}) bool {
	raw, err := bson.Marshal(filter)
	if err != nil {
		return false
	}
	var query bson.M
	if err := bson.Unmarshal(raw, &query); err != nil {
		return false
	}
	for key, value := range query {
		if key == "$or" || key == "$and" {
			continue
		}
		if got := regressionField(record, key); got != regressionString(value) && regressionString(value) != "" {
			return false
		}
	}
	return true
}

func regressionField(record *models.RunRecord, key string) string {
	switch key {
	case "owner_id":
		return record.OwnerID
	case "chat_id":
		return record.ChatID
	case "run_id":
		return record.RunID
	case "request_id":
		return record.RequestID
	case "status":
		return string(record.Status)
	default:
		return ""
	}
}

func regressionString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case models.RunStatus:
		return string(typed)
	default:
		raw, err := bson.Marshal(bson.M{"v": value})
		if err != nil {
			return ""
		}
		var decoded bson.M
		if err := bson.Unmarshal(raw, &decoded); err != nil {
			return ""
		}
		if text, ok := decoded["v"].(string); ok {
			return text
		}
		return ""
	}
}

func regressionRawInt(value interface{}) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	default:
		return 0
	}
}
func regressionApply(record *models.RunRecord, update interface{}) {
	raw, err := bson.Marshal(update)
	if err != nil {
		return
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		return
	}
	if set, ok := doc["$set"].(bson.M); ok {
		if status, ok := set["status"].(string); ok {
			record.Status = models.RunStatus(status)
		}
		if stage, ok := set["stage"].(string); ok {
			record.Stage = models.RunStage(stage)
		}
		switch encrypted := set["encrypted_result"].(type) {
		case primitive.Binary:
			record.EncryptedResult = append([]byte(nil), encrypted.Data...)
		case []byte:
			record.EncryptedResult = append([]byte(nil), encrypted...)
		case string:
			record.EncryptedResult = append([]byte(nil), encrypted...)
		}
		if errDoc, ok := set["safe_error"].(bson.M); ok {
			record.SafeError = &models.SafeRunError{Code: regressionString(errDoc["code"]), Message: regressionString(errDoc["message"])}
		}
	}
	if inc, ok := doc["$inc"].(bson.M); ok {
		record.Revision += regressionRawInt(inc["revision"])
	}
}

func regressionResult(record models.RunRecord, raw bson.Raw) *mongo.SingleResult {
	if err := bson.UnmarshalWithRegistry(mongodb.BSONRegistry(), raw, &record); err != nil {
		return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
	}
	return mongo.NewSingleResultFromDocument(record, nil, mongodb.BSONRegistry())
}

func regressionDecimal(value *decimal.Decimal) string {
	if value == nil {
		return "nil"
	}
	return value.String()
}

func regressionMetric(values []models.MetricValue, id models.MetricID) models.MetricValue {
	for _, value := range values {
		if value.MetricID == id {
			return value
		}
	}
	return models.MetricValue{MetricID: id}
}

func regressionRow(t *testing.T, report *models.AnalysisReport, entityID string) models.ReportRow {
	t.Helper()
	if report == nil {
		t.Fatal("report missing")
	}
	for _, row := range report.Table.Rows {
		if row.EntityID == entityID {
			return row
		}
	}
	t.Fatalf("row %s missing", entityID)
	return models.ReportRow{}
}

func regressionHasEntity(entities []models.CoverageEntity, id string) bool {
	for _, entity := range entities {
		if entity.EntityID == id {
			return true
		}
	}
	return false
}

func regressionHasReason(values []models.MetricValue, reason string) bool {
	for _, value := range values {
		if regressionHasReasonCode(value.ReasonCodes, reason) {
			return true
		}
	}
	return false
}

func regressionHasReasonCode(codes []string, reason string) bool {
	for _, code := range codes {
		if code == reason {
			return true
		}
	}
	return false
}

func regressionCandidates(report *models.AnalysisReport) []models.ReportRow {
	if report == nil {
		return nil
	}
	return report.Candidates
}

func assertRegressionIdentity(t *testing.T, report *models.AnalysisReport, label string) {
	t.Helper()
	if report == nil || report.ReportID == "" || len(report.Table.Rows) == 0 {
		t.Fatalf("%s report missing identity or rows", label)
	}
}

func regressionRefundCoverageID(mode regressionMode) (string, bool) {
	switch mode {
	case regressionModeProductRefund:
		return "refund:product:P1-SKU-1", true
	case regressionModeShopRefund:
		return "refund:shop:" + AssistantFixtureShopS1, true
	default:
		return "", false
	}
}

func regressionRefundFact(entityID string, basis models.Basis) models.FactRow {
	return models.FactRow{
		EntityID:       entityID,
		Basis:          basis,
		BusinessDate:   assistantFixtureRefundDate,
		Quantity:       decimal.Zero,
		Sales:          decimal.RequireFromString(AssistantFixtureRefundSales),
		Cost:           decimal.Zero,
		CostVerified:   true,
		Currency:       AssistantFixtureCurrency,
		CoverageStatus: models.CoverageStatusCovered,
		BundleRole:     models.BundleRoleNone,
	}
}

func regressionExplanationHasMetric(input AssistantExplanationInput, id models.MetricID, value string) bool {
	for _, metric := range input.Metrics {
		if metric.MetricID == id && metric.Value == value {
			return true
		}
	}
	return false
}

func regressionBarPoint(t *testing.T, report *models.AnalysisReport, entityID string) models.ChartPoint {
	t.Helper()
	if report == nil {
		t.Fatal("report missing")
	}
	for _, chart := range report.Charts {
		for _, point := range chart.Points {
			if point.EntityID == entityID || point.Dimension == entityID {
				return point
			}
		}
	}
	t.Fatalf("chart point %s missing", entityID)
	return models.ChartPoint{}
}
