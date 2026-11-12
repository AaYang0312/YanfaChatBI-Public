package services

import (
	"context"
	"errors"
	"testing"

	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"
)

// assistantExplanationFakeClient is a minimal llm.Client stub for
// workflow-boundary tests. Only GenerateRawJSON is exercised; the remaining
// methods satisfy the interface and fail loudly if called.
type assistantExplanationFakeClient struct {
	rawJSON string
	err     error
}

func (f *assistantExplanationFakeClient) GenerateRawJSON(context.Context, string, string, ...string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.rawJSON, nil
}

func (f *assistantExplanationFakeClient) GenerateResponse(context.Context, []*models.LLMMessage, string, bool, ...string) (string, error) {
	return "", errors.New("GenerateResponse is not used by the explanation adapter")
}

func (f *assistantExplanationFakeClient) GenerateRecommendations(context.Context, []*models.LLMMessage, string) (string, error) {
	return "", errors.New("GenerateRecommendations is not used by the explanation adapter")
}

func (f *assistantExplanationFakeClient) GenerateVisualization(context.Context, string, string, string, ...string) (string, error) {
	return "", errors.New("GenerateVisualization is not used by the explanation adapter")
}

func (f *assistantExplanationFakeClient) GenerateWithTools(context.Context, []*models.LLMMessage, []llm.ToolDefinition, llm.ToolExecutorFunc, llm.ToolCallConfig) (*llm.ToolCallResult, error) {
	return nil, errors.New("GenerateWithTools is not used by the explanation adapter")
}

func (f *assistantExplanationFakeClient) GetModelInfo() llm.ModelInfo { return llm.ModelInfo{} }

func (f *assistantExplanationFakeClient) SetModel(string) error { return nil }

// assistantExplanationBarChart returns the entity-dimension sales bar chart by
// its stable id. It never assumes the bar chart is the only or first chart:
// T25 adds a trend chart, so the total chart count is not a consumer contract.
func assistantExplanationBarChart(report models.AnalysisReport) (models.ChartSpec, bool) {
	for _, chart := range report.Charts {
		if chart.ChartID == chartBarID {
			return chart, true
		}
	}
	return models.ChartSpec{}, false
}

// assertAssistantExplanationBarSalesPoints asserts the bar chart carries the
// fixture's two shop sales points (S1 and S2, 60 each). It targets the bar
// chart's own financial points, not the number of charts in the report.
func assertAssistantExplanationBarSalesPoints(t *testing.T, chart models.ChartSpec) {
	t.Helper()
	if len(chart.Points) != 2 {
		t.Fatalf("bar chart points = %#v, want two shop sales points", chart.Points)
	}
	for _, point := range chart.Points {
		if point.Value == nil || point.Value.String() != "60" {
			t.Fatalf("bar chart point sales = %v, want 60", point.Value)
		}
	}
}

// TestAssistantExplanationAdapterWorkflowBoundary exercises the adapter through
// the real workflow so the assertions target consumer-visible behavior: which
// claims are accepted, which values stay deterministic, and whether the report
// and charts remain readable on failure.
func TestAssistantExplanationAdapterWorkflowBoundary(t *testing.T) {
	t.Run("hallucinated value is rejected and fixture metrics preserved", func(t *testing.T) {
		client := &assistantExplanationFakeClient{
			rawJSON: `{"explanations":[{"metric_id":"sales","value":"999","text":"销售额为999。","evidence_refs":["snap:` + AssistantFixtureContractVersion + `"]}]}`,
		}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), NewAssistantFixtureSource(), NewAssistantExplanationModel(client))
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-adapter-hallucination"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if report.ExplanationStatus != models.ExplanationStatusRejectedEvidence {
			t.Fatalf("explanation status = %q, want rejected_evidence", report.ExplanationStatus)
		}
		if len(report.Explanations) != 0 {
			t.Fatalf("hallucinated claim was accepted: %#v", report.Explanations)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricMargin, "48")
	})

	t.Run("grounded Chinese claim is accepted and linked to its chart", func(t *testing.T) {
		client := &assistantExplanationFakeClient{
			rawJSON: `{"explanations":[{"metric_id":"sales","value":"120","text":"销售额为120元。","evidence_refs":["snap:` + AssistantFixtureContractVersion + `"]}]}`,
		}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), NewAssistantFixtureSource(), NewAssistantExplanationModel(client))
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-adapter-grounded"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		if report.ExplanationStatus != models.ExplanationStatusAvailable || len(report.Explanations) != 1 {
			t.Fatalf("explanation result = %q %#v", report.ExplanationStatus, report.Explanations)
		}
		if report.Explanations[0].Value != "120" || report.Explanations[0].RefID != "explanation:sales" {
			t.Fatalf("accepted explanation = %#v", report.Explanations[0])
		}
		bar, ok := assistantExplanationBarChart(report)
		if !ok {
			t.Fatalf("bar chart missing: %#v", report.Charts)
		}
		if len(bar.ExplanationRefs) != 1 || bar.ExplanationRefs[0] != "explanation:sales" {
			t.Fatalf("bar chart explanation references = %#v", bar.ExplanationRefs)
		}
		assertAssistantExplanationBarSalesPoints(t, bar)
	})

	t.Run("client failure keeps report and charts readable", func(t *testing.T) {
		client := &assistantExplanationFakeClient{err: errors.New("provider down")}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), NewAssistantFixtureSource(), NewAssistantExplanationModel(client))
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-adapter-error"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
		if report.ExplanationStatus != models.ExplanationStatusUnavailable {
			t.Fatalf("explanation status = %q, want unavailable", report.ExplanationStatus)
		}
		bar, ok := assistantExplanationBarChart(report)
		if !ok {
			t.Fatalf("bar chart missing after client failure: %#v", report.Charts)
		}
		if len(bar.ExplanationRefs) != 0 {
			t.Fatalf("bar chart explanation refs = %#v, want none on failure", bar.ExplanationRefs)
		}
		assertAssistantExplanationBarSalesPoints(t, bar)
	})

	t.Run("malformed provider JSON keeps report and charts readable", func(t *testing.T) {
		client := &assistantExplanationFakeClient{rawJSON: "not a json object"}
		workflow, err := NewAssistantWorkflow(assistantFixtureWorkflowRequest(), NewAssistantFixtureSource(), NewAssistantExplanationModel(client))
		if err != nil {
			t.Fatalf("NewAssistantWorkflow: %v", err)
		}

		report, err := workflow.Invoke(context.Background(), models.AnalysisReport{ReportID: "run-adapter-bad-json"})
		if err != nil {
			t.Fatalf("Invoke: %v", err)
		}
		assertAssistantWorkflowMetric(t, report.Totals, models.MetricSales, "120")
		if report.ExplanationStatus != models.ExplanationStatusUnavailable {
			t.Fatalf("explanation status = %q, want unavailable", report.ExplanationStatus)
		}
		bar, ok := assistantExplanationBarChart(report)
		if !ok {
			t.Fatalf("bar chart missing after malformed JSON: %#v", report.Charts)
		}
		if len(bar.ExplanationRefs) != 0 {
			t.Fatalf("bar chart explanation refs = %#v, want none on failure", bar.ExplanationRefs)
		}
		assertAssistantExplanationBarSalesPoints(t, bar)
	})
}
