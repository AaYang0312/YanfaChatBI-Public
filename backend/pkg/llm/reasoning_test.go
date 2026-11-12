package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yanfachatbi/internal/models"
)

func TestInjectReasoningEffort(t *testing.T) {
	spec := &models.ReasoningSpec{Mode: models.ReasoningModeEffort, Param: "reasoning_effort",
		Levels: []string{"low", "medium", "high"}, DefaultLevel: "medium"}
	body := map[string]any{"model": "gpt-6"}
	InjectReasoning(body, spec, "high")
	if body["reasoning_effort"] != "high" {
		t.Fatalf("effort not injected: %v", body)
	}

	// Unknown level falls back to the declared default.
	body = map[string]any{}
	InjectReasoning(body, spec, "ultra")
	if body["reasoning_effort"] != "medium" {
		t.Fatalf("unknown level did not fall back: %v", body)
	}

	// Empty level injects nothing (endpoint default).
	body = map[string]any{}
	InjectReasoning(body, spec, "")
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("empty level must not inject: %v", body)
	}
}

func TestInjectReasoningToggle(t *testing.T) {
	spec := &models.ReasoningSpec{Mode: models.ReasoningModeToggle, Param: "thinking.type",
		Levels: []string{"on", "off"}, DefaultLevel: "on", OnValue: true, OffValue: false}

	body := map[string]any{}
	InjectReasoning(body, spec, "on")
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != true {
		t.Fatalf("toggle on not injected: %v", body)
	}

	body = map[string]any{}
	InjectReasoning(body, spec, "off")
	thinking, ok = body["thinking"].(map[string]any)
	if !ok || thinking["type"] != false {
		t.Fatalf("toggle off not injected: %v", body)
	}
}

func TestInjectReasoningNestedCustom(t *testing.T) {
	spec := &models.ReasoningSpec{Mode: models.ReasoningModeCustom, Param: "reasoning.effort",
		Levels: []string{"low", "medium", "high", "xhigh", "max"}, DefaultLevel: "max"}
	body := map[string]any{"model": "MiniMax-M3.1-Flash"}
	InjectReasoning(body, spec, "high")
	nested, ok := body["reasoning"].(map[string]any)
	if !ok || nested["effort"] != "high" {
		t.Fatalf("nested reasoning not injected: %v", body)
	}
}

func TestInjectReasoningUnsupportedSpec(t *testing.T) {
	body := map[string]any{}
	InjectReasoning(body, nil, "high")
	InjectReasoning(body, &models.ReasoningSpec{Mode: models.ReasoningModeNone}, "high")
	InjectReasoning(body, &models.ReasoningSpec{Mode: models.ReasoningModeEffort, Param: "", Levels: []string{"high"}}, "high")
	if len(body) != 0 {
		t.Fatalf("unsupported specs must not inject: %v", body)
	}
}

func TestReasoningTransportRewritesChatBody(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client := &http.Client{Transport: NewReasoningTransport(server.Client().Transport)}
	spec := &models.ReasoningSpec{Mode: models.ReasoningModeEffort, Param: "reasoning_effort", Levels: []string{"low", "high"}, DefaultLevel: "low"}
	ctx := WithReasoningLevel(context.Background(), "high")
	ctx = withReasoningSpec(ctx, spec)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[]}`))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if received["reasoning_effort"] != "high" {
		t.Fatalf("transport did not inject reasoning: %v", received)
	}
}

func TestReasoningTransportLeavesOtherPathsAlone(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Transport: NewReasoningTransport(server.Client().Transport)}
	spec := &models.ReasoningSpec{Mode: models.ReasoningModeEffort, Param: "reasoning_effort", Levels: []string{"low", "high"}, DefaultLevel: "low"}
	ctx := WithReasoningLevel(context.Background(), "high")
	ctx = withReasoningSpec(ctx, spec)

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/embeddings",
		strings.NewReader(`{"input":"x"}`))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, ok := received["reasoning_effort"]; ok {
		t.Fatalf("non-chat path must not be modified: %v", received)
	}
}

func TestRetryWithoutResponseFormat(t *testing.T) {
	if !retryWithoutResponseFormat(fmtError("error, status code: 400, message: This response_format type is unavailable now")) {
		t.Fatal("response_format rejection not detected")
	}
	if !retryWithoutResponseFormat(fmtError("unsupported value: json_schema")) {
		t.Fatal("json_schema rejection not detected")
	}
	if retryWithoutResponseFormat(fmtError("rate limited")) {
		t.Fatal("unrelated error must not trigger a retry")
	}
	if retryWithoutResponseFormat(nil) {
		t.Fatal("nil error must not trigger a retry")
	}
}

func fmtError(message string) error { return &simpleError{message} }

type simpleError struct{ message string }

func (e *simpleError) Error() string { return e.message }
