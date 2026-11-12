package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/shopspring/decimal"

	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
)

// AssistantExplanationMetric is the only metric data exposed to an explanation
// model: a metric identifier and its deterministic decimal value.
type AssistantExplanationMetric struct {
	MetricID models.MetricID
	Value    string
}

// AssistantExplanationInput deliberately contains no request text, facts,
// entity identifiers, SQL, formulas, or report fields outside this allow-list.
// LanguageDirective is the shared generation instruction, passed once.
type AssistantExplanationInput struct {
	Metrics           []AssistantExplanationMetric
	EvidenceRefs      []string
	LanguageDirective string
	// ChatID/OwnerID let the injected model resolve the chat-selected provider.
	ChatID  string
	OwnerID string
}

// AssistantExplanationClaim is one model proposal. The workflow checks its
// metric value and evidence refs before adding it to a report.
type AssistantExplanationClaim struct {
	MetricID     models.MetricID
	Value        string
	Text         string
	EvidenceRefs []string
}

// AssistantExplanationModel is injected so the workflow does not select or
// configure a provider itself.
type AssistantExplanationModel func(context.Context, AssistantExplanationInput) ([]AssistantExplanationClaim, error)

// AssistantWorkflow is one compiled, acyclic Eino workflow for a validated
// request. Its Eino input and output are both AnalysisReport.
// AssistantWorkflowOption configures server-side context for one workflow.
type AssistantWorkflowOption func(*AssistantWorkflow)

// WithWorkflowChat supplies the owner/chat so explanation calls can resolve the
// chat-selected model.
func WithWorkflowChat(chatID, ownerID string) AssistantWorkflowOption {
	return func(w *AssistantWorkflow) {
		w.chatID, w.ownerID = chatID, ownerID
	}
}

type AssistantWorkflow struct {
	request models.AnalysisRequest
	source  repositories.AssistantSourceRepository
	model   AssistantExplanationModel
	chatID  string
	ownerID string
	invoke  func(context.Context, models.AnalysisReport) (models.AnalysisReport, error)
}

type assistantWorkflowStateKey struct{}

type assistantWorkflowState struct {
	request           models.AnalysisRequest
	source            repositories.AssistantSourceRepository
	model             AssistantExplanationModel
	chatID            string
	ownerID           string
	reportID          string
	capabilities      models.SourceCapabilities
	snapshot          models.FactSnapshot
	coverage          AssistantMetricCoverage
	refundUnavailable string
}

type assistantExplanationContext struct {
	context.Context
}

func (assistantExplanationContext) Value(any) any { return nil }

var assistantExplanationDecimal = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
var assistantExplanationNumber = regexp.MustCompile(`-?[0-9]+(\.[0-9]+)?`)

const assistantExplanationBudget = 10 * time.Second

// NewAssistantWorkflow compiles the six business stages using Eino Workflow
// lambdas. Sales and coverage are each read once; declared refunds are loaded
// independently after source and fact validation.
func NewAssistantWorkflow(req models.AnalysisRequest, source repositories.AssistantSourceRepository, model AssistantExplanationModel, opts ...AssistantWorkflowOption) (*AssistantWorkflow, error) {
	if source == nil {
		return nil, fmt.Errorf("assistant workflow requires a source")
	}

	workflow := compose.NewWorkflow[models.AnalysisReport, models.AnalysisReport]()
	workflow.AddLambdaNode("validate_snapshot", compose.InvokableLambda(validateAssistantSnapshot)).AddInput(compose.START)
	workflow.AddLambdaNode("validate_coverage", compose.InvokableLambda(validateAssistantCoverage)).AddInput("validate_snapshot")
	workflow.AddLambdaNode("validate_facts", compose.InvokableLambda(validateAssistantFacts)).AddInput("validate_coverage")
	workflow.AddLambdaNode("compute_metrics", compose.InvokableLambda(computeAssistantMetrics)).AddInput("validate_facts")
	workflow.AddLambdaNode("validate_chart_specs", compose.InvokableLambda(validateAssistantChartSpecs)).AddInput("compute_metrics")
	workflow.AddLambdaNode("explain", compose.InvokableLambda(explainAssistantReport)).AddInput("validate_chart_specs")
	workflow.End().AddInput("explain")

	compiled, err := workflow.Compile(context.Background())
	if err != nil {
		return nil, fmt.Errorf("compile assistant workflow: %w", err)
	}
	built := &AssistantWorkflow{
		request: cloneAssistantWorkflowRequest(req),
		source:  source,
		model:   model,
		invoke: func(ctx context.Context, input models.AnalysisReport) (models.AnalysisReport, error) {
			return compiled.Invoke(ctx, input)
		},
	}
	for _, opt := range opts {
		opt(built)
	}
	return built, nil
}

// Invoke executes the Eino workflow. The input report supplies the report ID;
// deterministic report content is rebuilt from the configured request and its
// validated source snapshots.
func (w *AssistantWorkflow) Invoke(ctx context.Context, input models.AnalysisReport) (models.AnalysisReport, error) {
	if w == nil || w.invoke == nil {
		return models.AnalysisReport{}, fmt.Errorf("assistant workflow is not initialized")
	}
	if ctx == nil {
		return models.AnalysisReport{}, fmt.Errorf("assistant workflow requires a context")
	}
	state := &assistantWorkflowState{
		request:  cloneAssistantWorkflowRequest(w.request),
		source:   w.source,
		model:    w.model,
		chatID:   w.chatID,
		ownerID:  w.ownerID,
		reportID: input.ReportID,
	}
	ctx = context.WithValue(ctx, assistantWorkflowStateKey{}, state)
	return w.invoke(ctx, input)
}

func cloneAssistantWorkflowRequest(req models.AnalysisRequest) models.AnalysisRequest {
	req.Scope.PlatformIDs = append([]string(nil), req.Scope.PlatformIDs...)
	req.Scope.ShopIDs = append([]string(nil), req.Scope.ShopIDs...)
	req.Product.SkuIDs = append([]string(nil), req.Product.SkuIDs...)
	req.Metrics = append([]models.MetricID(nil), req.Metrics...)
	if req.TrendPeriod != nil {
		trend := *req.TrendPeriod
		req.TrendPeriod = &trend
	}
	if req.LowMarginThreshold != nil {
		threshold := *req.LowMarginThreshold
		req.LowMarginThreshold = &threshold
	}
	return req
}

func assistantWorkflowStateFromContext(ctx context.Context) (*assistantWorkflowState, error) {
	state, ok := ctx.Value(assistantWorkflowStateKey{}).(*assistantWorkflowState)
	if !ok || state == nil {
		return nil, fmt.Errorf("assistant workflow execution state is missing")
	}
	return state, nil
}

func validateAssistantSnapshot(ctx context.Context, report models.AnalysisReport) (models.AnalysisReport, error) {
	state, err := assistantWorkflowStateFromContext(ctx)
	if err != nil {
		return report, err
	}
	if strings.TrimSpace(report.ReportID) == "" {
		return report, fmt.Errorf("report_id is required")
	}
	state.reportID = report.ReportID

	normalized, err := ValidateAnalysisRequest(models.AccessContext{}, state.request, time.Now())
	if err != nil {
		return report, fmt.Errorf("validate assistant request: %w", err)
	}
	state.request = normalized

	capabilities, err := state.source.Capabilities(ctx)
	if err != nil {
		return report, fmt.Errorf("read assistant source capabilities: %w", err)
	}
	if capabilities.ContractVersion == "" {
		return report, models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "source.contract_version",
			"the data source has no approved contract version")
	}
	if !capabilities.CoverageEvidence {
		return report, models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "coverage",
			"the data source does not provide coverage evidence")
	}
	if !capabilities.SupportsBasis(normalized.Basis) {
		return report, models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "basis",
			"the data source does not support the requested basis")
	}
	for _, metric := range normalized.Metrics {
		if metric != models.MetricRefund && !capabilities.SupportsMetric(metric) {
			return report, models.NewValidationError(models.ErrorCodeCapabilityUnavailable, "metrics",
				"the data source does not support a requested metric")
		}
	}

	snapshot, err := state.source.ReadSalesFacts(ctx, FactQueryFromRequest(normalized))
	if err != nil {
		return report, fmt.Errorf("read assistant fact snapshot: %w", err)
	}
	state.capabilities = capabilities
	state.snapshot = snapshot
	return report, nil
}

func validateAssistantCoverage(ctx context.Context, report models.AnalysisReport) (models.AnalysisReport, error) {
	state, err := assistantWorkflowStateFromContext(ctx)
	if err != nil {
		return report, err
	}
	snapshot := state.snapshot
	if snapshot.ContractVersion == "" || snapshot.ContractVersion != state.capabilities.ContractVersion {
		return report, fmt.Errorf("fact snapshot contract version does not match source capabilities")
	}
	if snapshot.Basis != state.request.Basis || snapshot.Window != state.request.Period.DateRange {
		return report, fmt.Errorf("fact snapshot basis or window does not match the request")
	}
	if snapshot.ReadAt.IsZero() || snapshot.DataAsOf.IsZero() {
		return report, fmt.Errorf("fact snapshot is missing read_at or data_as_of")
	}

	mainQuery := repositories.AssistantCoverageQuery{
		Basis:   state.request.Basis,
		Window:  state.request.Period.DateRange,
		Scope:   state.request.Scope,
		Product: state.request.Product,
	}
	mainRows, err := state.source.ReadCoverage(ctx, mainQuery)
	if err != nil {
		return report, fmt.Errorf("read assistant coverage: %w", err)
	}
	validateCoverageRows := func(rows []repositories.AssistantCoverage, window string) error {
		seen := make(map[assistantWorkflowCoverageKey]struct{}, len(rows))
		for i, entity := range rows {
			if !entity.EntityType.Valid() || strings.TrimSpace(entity.EntityID) == "" || !entity.Status.Valid() {
				return fmt.Errorf("coverage row %d has an invalid entity or status", i)
			}
			key := assistantWorkflowCoverageKey{entityType: entity.EntityType, entityID: entity.EntityID}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%s coverage row %d duplicates an entity", window, i)
			}
			seen[key] = struct{}{}
		}
		return nil
	}
	if err := validateCoverageRows(mainRows, "main"); err != nil {
		return report, err
	}

	var trendRows []repositories.AssistantCoverage
	if state.request.TrendPeriod != nil {
		trendQuery := mainQuery
		trendQuery.Window = *state.request.TrendPeriod
		if trendQuery.Window == mainQuery.Window {
			trendRows = mainRows
		} else {
			trendRows, err = state.source.ReadCoverage(ctx, trendQuery)
			if err != nil {
				return report, fmt.Errorf("read assistant trend coverage: %w", err)
			}
			if err := validateCoverageRows(trendRows, "trend"); err != nil {
				return report, err
			}
		}
	}

	state.coverage = AssistantMetricCoverage{Main: mainRows, Trend: trendRows}
	return report, nil
}

type assistantWorkflowCoverageKey struct {
	entityType models.EntityType
	entityID   string
}

func validateAssistantFacts(ctx context.Context, report models.AnalysisReport) (models.AnalysisReport, error) {
	state, err := assistantWorkflowStateFromContext(ctx)
	if err != nil {
		return report, err
	}
	location, err := AssistantLoadLocation(state.request.Period.Timezone)
	if err != nil {
		return report, err
	}
	start := parseBoundary(state.request.Period.StartDate, location)
	end := parseBoundary(state.request.Period.EndDateExclusive, location)
	var trendStart, trendEnd time.Time
	if state.request.TrendPeriod != nil {
		trendStart = parseBoundary(state.request.TrendPeriod.StartDate, location)
		trendEnd = parseBoundary(state.request.TrendPeriod.EndDateExclusive, location)
	}
	for i, row := range state.snapshot.Rows {
		if strings.TrimSpace(row.EntityID) == "" {
			return report, fmt.Errorf("fact snapshot row %d has an empty entity_id", i)
		}
		if row.Basis != state.request.Basis {
			return report, fmt.Errorf("fact snapshot row %d has a mismatched basis", i)
		}
		if !row.BundleRole.Valid() {
			return report, fmt.Errorf("fact snapshot row %d has an invalid bundle role", i)
		}
		if !row.CoverageStatus.Valid() {
			return report, fmt.Errorf("fact snapshot row %d has an invalid coverage status", i)
		}
		if _, ok := factInstant(row.BusinessDate, location); !ok {
			return report, fmt.Errorf("fact snapshot row %d has an invalid business date", i)
		}
		inMain := inWindow(row.BusinessDate, start, end, location)
		inTrend := state.request.TrendPeriod != nil && inWindow(row.BusinessDate, trendStart, trendEnd, location)
		if !inMain && !inTrend {
			return report, fmt.Errorf("fact snapshot row %d is outside the requested windows", i)
		}
	}
	if !state.capabilities.SupportsMetric(models.MetricRefund) {
		state.refundUnavailable = models.ReasonCapabilityUnavailable
		return report, nil
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	refunds, err := state.source.ReadRefundFacts(ctx, FactQueryFromRequest(state.request))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return report, ctxErr
	}
	if err != nil {
		// Only an explicit business capability refusal may preserve sales.
		// Database/transport errors and cancellation remain run failures.
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			if validation, ok := models.AsValidationError(err); ok && validation.Code == models.ErrorCodeCapabilityUnavailable {
				state.refundUnavailable = models.ReasonCapabilityUnavailable
				return report, nil
			}
		}
		return report, fmt.Errorf("read assistant refund snapshot: %w", err)
	}
	if refunds.ContractVersion == "" || refunds.ContractVersion != state.capabilities.ContractVersion {
		return report, fmt.Errorf("refund snapshot contract version does not match source capabilities")
	}
	if refunds.Basis != state.request.Basis || refunds.Window != state.request.Period.DateRange {
		return report, fmt.Errorf("refund snapshot basis or window does not match the request")
	}
	if refunds.ReadAt.IsZero() || refunds.DataAsOf.IsZero() {
		return report, fmt.Errorf("refund snapshot is missing read_at or data_as_of")
	}
	for i, row := range refunds.Rows {
		if row.Basis != state.request.Basis {
			return report, fmt.Errorf("refund snapshot row %d has a mismatched basis", i)
		}
		if !row.BundleRole.Valid() {
			return report, fmt.Errorf("refund snapshot row %d has an invalid bundle role", i)
		}
		if _, ok := factInstant(row.BusinessDate, location); !ok {
			return report, fmt.Errorf("refund snapshot row %d has an invalid business date", i)
		}
		if !inWindow(row.BusinessDate, start, end, location) {
			continue
		}
		// Refund coverage is independent. Missing/stale/unknown evidence makes
		// refunds unavailable in the metric engine without discarding sales.
		if !row.CoverageStatus.Valid() {
			return report, fmt.Errorf("refund snapshot row %d has an invalid coverage status", i)
		}
		// The existing metric engine recognizes refund: identities. Prefixing
		// an empty identity preserves its lack of product or shop attribution.
		if !strings.HasPrefix(row.EntityID, refundPrefix) {
			row.EntityID = refundPrefix + row.EntityID
		}
		state.snapshot.Rows = append(state.snapshot.Rows, row)
	}
	return report, nil
}

func computeAssistantMetrics(ctx context.Context, report models.AnalysisReport) (models.AnalysisReport, error) {
	state, err := assistantWorkflowStateFromContext(ctx)
	if err != nil {
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	computed := ComputeAnalysisReport(state.request, state.snapshot, state.coverage, state.reportID)
	if state.refundUnavailable != "" {
		reasons := []string{state.refundUnavailable}
		computed.Refunds.Status = models.MetricStatusUnavailable
		computed.Refunds.Amount = nil
		computed.Refunds.ReasonCodes = uniqueStrings(append(computed.Refunds.ReasonCodes, reasons...))
		unavailable := func(metrics []models.MetricValue) {
			for i := range metrics {
				if metrics[i].MetricID == models.MetricRefund {
					metrics[i].Status = models.MetricStatusUnavailable
					metrics[i].Value = nil
					metrics[i].Currency = ""
					metrics[i].ReasonCodes = reasons
				}
			}
		}
		unavailable(computed.Totals)
		unavailable(computed.Table.Totals)
		for i := range computed.Table.Rows {
			unavailable(computed.Table.Rows[i].Metrics)
		}
	}
	return computed, nil
}

func validateAssistantChartSpecs(ctx context.Context, report models.AnalysisReport) (models.AnalysisReport, error) {
	if err := validateAssistantReportCharts(report); err != nil {
		return report, err
	}
	return report, nil
}

func validateAssistantReportCharts(report models.AnalysisReport) error {
	seen := make(map[string]struct{}, len(report.Charts))
	for _, chart := range report.Charts {
		if chart.ChartID == "" || !chart.Kind.Valid() || len(chart.MetricIDs) == 0 {
			return fmt.Errorf("chart spec has an invalid identity, kind, or metric list")
		}
		if _, exists := seen[chart.ChartID]; exists {
			return fmt.Errorf("chart spec id %q is duplicated", chart.ChartID)
		}
		seen[chart.ChartID] = struct{}{}
		for _, metricID := range chart.MetricIDs {
			if !metricID.Valid() {
				return fmt.Errorf("chart %q has an unsupported metric id", chart.ChartID)
			}
		}
		switch chart.DataRef {
		case dataRefTable:
			if chart.Dimension != dimensionEntity || len(chart.Points) != len(report.Table.Rows) {
				return fmt.Errorf("chart %q does not match the report table shape", chart.ChartID)
			}
			for i, point := range chart.Points {
				row := report.Table.Rows[i]
				if point.EntityID != row.EntityID || !metricIDIn(point.MetricID, chart.MetricIDs) ||
					point.Status != metricStatus(row, point.MetricID) ||
					!assistantDecimalPointersEqual(point.Value, metricValue(row, point.MetricID)) {
					return fmt.Errorf("chart %q point %d differs from the report table", chart.ChartID, i)
				}
			}
		case dataRefTrend:
			if chart.Dimension != dimensionDate || len(chart.Points) != len(report.Trend) {
				return fmt.Errorf("chart %q does not match the report trend shape", chart.ChartID)
			}
			for i, point := range chart.Points {
				if !assistantChartPointsEqual(point, report.Trend[i]) {
					return fmt.Errorf("chart %q point %d differs from the report trend", chart.ChartID, i)
				}
			}
		default:
			return fmt.Errorf("chart %q has an unsupported data_ref", chart.ChartID)
		}
	}
	return nil
}

func metricIDIn(metricID models.MetricID, metricIDs []models.MetricID) bool {
	for _, candidate := range metricIDs {
		if candidate == metricID {
			return true
		}
	}
	return false
}

func assistantDecimalPointersEqual(a, b *decimal.Decimal) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func assistantChartPointsEqual(a, b models.ChartPoint) bool {
	if a.Dimension != b.Dimension || a.EntityID != b.EntityID || a.BusinessDate != b.BusinessDate ||
		a.MetricID != b.MetricID || a.Status != b.Status || a.Currency != b.Currency ||
		a.CoverageRef != b.CoverageRef || len(a.ReasonCodes) != len(b.ReasonCodes) ||
		!assistantDecimalPointersEqual(a.Value, b.Value) {
		return false
	}
	for i := range a.ReasonCodes {
		if a.ReasonCodes[i] != b.ReasonCodes[i] {
			return false
		}
	}
	return true
}

func explainAssistantReport(ctx context.Context, report models.AnalysisReport) (models.AnalysisReport, error) {
	state, err := assistantWorkflowStateFromContext(ctx)
	if err != nil {
		return report, err
	}
	input, expectedValues, knownEvidence := assistantExplanationInput(report, state.request)
	input.ChatID, input.OwnerID = state.chatID, state.ownerID
	if state.model == nil || len(input.Metrics) == 0 {
		report.Explanations = nil
		report.ExplanationStatus = models.ExplanationStatusUnavailable
		return report, nil
	}
	explanationCtx, cancel := context.WithTimeout(assistantExplanationContext{Context: ctx}, assistantExplanationBudget)
	defer cancel()
	claims, err := state.model(explanationCtx, input)
	if err != nil || explanationCtx.Err() != nil {
		report.Explanations = nil
		report.ExplanationStatus = models.ExplanationStatusUnavailable
		return report, nil
	}

	accepted := make([]models.Explanation, 0, len(claims))
	acceptedRefs := make(map[models.MetricID]string, len(claims))
	seenMetrics := make(map[models.MetricID]struct{}, len(claims))
	rejected := len(claims) == 0
	for _, claim := range claims {
		if _, duplicate := seenMetrics[claim.MetricID]; duplicate {
			rejected = true
			continue
		}
		seenMetrics[claim.MetricID] = struct{}{}
		expectedValue, allowedMetric := expectedValues[claim.MetricID]
		if !allowedMetric || !assistantExplanationClaimValid(claim, expectedValue, knownEvidence) {
			rejected = true
			continue
		}
		refID := "explanation:" + string(claim.MetricID)
		explanation := models.Explanation{
			RefID:        refID,
			MetricID:     claim.MetricID,
			Value:        expectedValue,
			Text:         strings.TrimSpace(claim.Text),
			EvidenceRefs: uniqueStrings(claim.EvidenceRefs),
		}
		accepted = append(accepted, explanation)
		acceptedRefs[claim.MetricID] = refID
	}
	report.Explanations = accepted
	if rejected {
		report.ExplanationStatus = models.ExplanationStatusRejectedEvidence
	} else {
		report.ExplanationStatus = models.ExplanationStatusAvailable
	}

	charts := append([]models.ChartSpec(nil), report.Charts...)
	for i := range charts {
		charts[i].ExplanationRefs = nil
		for _, metricID := range charts[i].MetricIDs {
			if refID, ok := acceptedRefs[metricID]; ok {
				charts[i].ExplanationRefs = append(charts[i].ExplanationRefs, refID)
			}
		}
	}
	report.Charts = charts
	return report, nil
}

func assistantExplanationInput(report models.AnalysisReport, request models.AnalysisRequest) (AssistantExplanationInput, map[models.MetricID]string, map[string]struct{}) {
	requested := make(map[models.MetricID]struct{}, len(request.Metrics))
	for _, metricID := range request.Metrics {
		requested[metricID] = struct{}{}
	}
	input := AssistantExplanationInput{
		Metrics:           make([]AssistantExplanationMetric, 0, len(requested)),
		EvidenceRefs:      make([]string, 0, len(report.Evidence)),
		LanguageDirective: constants.LanguageDirective,
	}
	expectedValues := make(map[models.MetricID]string, len(requested))
	knownEvidence := make(map[string]struct{}, len(report.Evidence))
	for _, evidence := range report.Evidence {
		if evidence.RefID == "" {
			continue
		}
		if _, exists := knownEvidence[evidence.RefID]; exists {
			continue
		}
		knownEvidence[evidence.RefID] = struct{}{}
		input.EvidenceRefs = append(input.EvidenceRefs, evidence.RefID)
	}
	for _, metric := range report.Totals {
		if metric.Value == nil {
			continue
		}
		if _, ok := requested[metric.MetricID]; !ok {
			continue
		}
		if _, exists := expectedValues[metric.MetricID]; exists {
			continue
		}
		value := metric.Value.String()
		expectedValues[metric.MetricID] = value
		input.Metrics = append(input.Metrics, AssistantExplanationMetric{MetricID: metric.MetricID, Value: value})
	}
	return input, expectedValues, knownEvidence
}

func assistantExplanationClaimValid(claim AssistantExplanationClaim, expectedValue string, knownEvidence map[string]struct{}) bool {
	if strings.TrimSpace(claim.Text) == "" || !assistantExplanationNumberMatches(claim.Value, expectedValue) ||
		len(claim.EvidenceRefs) == 0 || !assistantExplanationTextNumbersMatch(claim.Text, expectedValue) {
		return false
	}
	for _, ref := range claim.EvidenceRefs {
		if _, ok := knownEvidence[ref]; !ok {
			return false
		}
	}
	return true
}

func assistantExplanationNumberMatches(candidate, expected string) bool {
	candidate = strings.TrimSpace(candidate)
	if !assistantExplanationDecimal.MatchString(candidate) || !assistantExplanationDecimal.MatchString(expected) {
		return false
	}
	candidateValue, err := decimal.NewFromString(candidate)
	if err != nil {
		return false
	}
	expectedValue, err := decimal.NewFromString(expected)
	return err == nil && candidateValue.Equal(expectedValue)
}

func assistantExplanationTextNumbersMatch(text, expected string) bool {
	expectedValue, err := decimal.NewFromString(expected)
	if err != nil {
		return false
	}
	for _, token := range assistantExplanationNumber.FindAllString(text, -1) {
		value, err := decimal.NewFromString(token)
		if err != nil || !value.Equal(expectedValue) {
			return false
		}
	}
	return true
}
