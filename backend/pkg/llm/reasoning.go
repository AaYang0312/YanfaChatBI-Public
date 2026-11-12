package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"yanfachatbi/internal/models"
)

type reasoningKey struct{}

type reasoningInjection struct {
	spec  *models.ReasoningSpec
	level string
}

// WithReasoningLevel attaches the chat-selected thinking level to a request
// context. An empty level means "use the endpoint default and inject nothing".
func WithReasoningLevel(ctx context.Context, level string) context.Context {
	injection, _ := ctx.Value(reasoningKey{}).(reasoningInjection)
	injection.level = level
	return context.WithValue(ctx, reasoningKey{}, injection)
}

// withReasoningSpec pairs the model's spec with the level already on ctx.
func withReasoningSpec(ctx context.Context, spec *models.ReasoningSpec) context.Context {
	injection, _ := ctx.Value(reasoningKey{}).(reasoningInjection)
	injection.spec = spec
	return context.WithValue(ctx, reasoningKey{}, injection)
}

// ReasoningLevelFromContext returns the selected level ("" when unset).
func ReasoningLevelFromContext(ctx context.Context) string {
	injection, _ := ctx.Value(reasoningKey{}).(reasoningInjection)
	return injection.level
}

type reasoningTransport struct{ base http.RoundTripper }

// NewReasoningTransport wraps a transport so chat completion bodies get the
// configured thinking parameters injected before they leave the process.
func NewReasoningTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &reasoningTransport{base: base}
}

func (t *reasoningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	injection, _ := req.Context().Value(reasoningKey{}).(reasoningInjection)
	if injection.spec == nil || !injection.spec.Supported() || injection.level == "" ||
		req.Body == nil || !strings.Contains(req.URL.Path, "chat/completions") {
		return t.base.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body.Close()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		// Not a JSON object we understand; forward the original bytes.
		req.Body = io.NopCloser(bytes.NewReader(body))
		return t.base.RoundTrip(req)
	}
	InjectReasoning(payload, injection.spec, injection.level)
	rewritten, err := json.Marshal(payload)
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		return t.base.RoundTrip(req)
	}
	req.Body = io.NopCloser(bytes.NewReader(rewritten))
	req.ContentLength = int64(len(rewritten))
	req.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	req.Header.Del("Content-Encoding")
	return t.base.RoundTrip(req)
}

// InjectReasoning writes the resolved level into the request payload using the
// model's declared mode. Unknown levels fall back to the declared default.
func InjectReasoning(payload map[string]any, spec *models.ReasoningSpec, requested string) {
	if payload == nil || spec == nil || !spec.Supported() || requested == "" {
		return
	}
	level := spec.EffectiveLevel(requested)
	switch spec.Mode {
	case models.ReasoningModeEffort, models.ReasoningModeCustom:
		setJSONPath(payload, spec.Param, level)
	case models.ReasoningModeToggle:
		value := spec.OnValue
		if value == nil {
			value = true
		}
		if len(spec.Levels) > 1 && level == spec.Levels[1] {
			value = spec.OffValue
			if value == nil {
				value = false
			}
		}
		setJSONPath(payload, spec.Param, value)
	}
}

func setJSONPath(payload map[string]any, path string, value any) {
	segments := strings.Split(path, ".")
	current := payload
	for i, segment := range segments {
		if i == len(segments)-1 {
			current[segment] = value
			return
		}
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[segment] = next
		}
		current = next
	}
}
