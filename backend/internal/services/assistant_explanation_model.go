package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/pkg/llm"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// assistantExplanationSystemPrompt is the bounded generation instruction for a
// metric explanation. It requires Simplified Chinese (reinforced by the shared
// LanguageDirective appended at call time), verbatim metric values, evidence
// refs drawn only from the allow-list, and a single JSON object as the only
// output. It selects no provider, model, or fixed answer.
const assistantExplanationSystemPrompt = `You write concise Simplified Chinese explanations for a business report.

Respond with a single JSON object of exactly this shape:
{"explanations":[{"metric_id":"...","value":"...","text":"...","evidence_refs":["..."]}]}

For each metric in the user message, produce one object in "explanations" where:
- "metric_id" is the metric identifier exactly as given
- "value" is the metric's decimal value copied verbatim: never round, rescale, or reformat it
- "text" is one short Simplified Chinese sentence explaining that metric
- "evidence_refs" lists one or more evidence ref ids chosen only from the evidence refs listed in the user message

Do not invent metric ids, change numeric values, or cite evidence that is not listed. Output only the JSON object, with no markdown fences and no prose.`

// assistantExplanationClaimJSON mirrors AssistantExplanationClaim with explicit
// wire keys. The provider's JSON shape is owned by this adapter, not by the
// workflow struct, so decoding never depends on Go field-name matching.
type assistantExplanationClaimJSON struct {
	MetricID     models.MetricID `json:"metric_id"`
	Value        string          `json:"value"`
	Text         string          `json:"text"`
	EvidenceRefs []string        `json:"evidence_refs"`
}

type assistantExplanationResponseJSON struct {
	Explanations []assistantExplanationClaimJSON `json:"explanations"`
}

type assistantExplanationMetricJSON struct {
	MetricID models.MetricID `json:"metric_id"`
	Value    string          `json:"value"`
}

// assistantExplanationAdapter adapts an existing llm.Client to the
// AssistantExplanationModel contract. It forwards the allow-listed input to
// whatever client is injected and decodes the returned JSON; the workflow still
// validates every claim's metric value and evidence refs.
type assistantExplanationAdapter struct {
	client    llm.Client
	manager   *llm.Manager
	providers *ModelProviderService
	chats     repositories.ChatRepository
}

// AssistantExplanationOption configures catalogue resolution for the adapter.
type AssistantExplanationOption func(*assistantExplanationAdapter)

// WithExplanationCatalogue lets the adapter resolve the chat-selected model
// instead of always using the injected default client.
func WithExplanationCatalogue(manager *llm.Manager, providers *ModelProviderService, chats repositories.ChatRepository) AssistantExplanationOption {
	return func(a *assistantExplanationAdapter) {
		a.manager, a.providers, a.chats = manager, providers, chats
	}
}

// NewAssistantExplanationModel wraps an existing llm.Client so the assistant
// workflow can request metric explanations without selecting or configuring a
// provider itself. A nil client returns a nil model, which keeps explanation
// unavailable exactly as an unconfigured deployment behaves today.
func NewAssistantExplanationModel(client llm.Client, opts ...AssistantExplanationOption) AssistantExplanationModel {
	if client == nil {
		return nil
	}
	adapter := &assistantExplanationAdapter{client: client}
	for _, opt := range opts {
		opt(adapter)
	}
	return adapter.explain
}

// resolveChatModel maps the run's chat to the configured provider/model.
func (a *assistantExplanationAdapter) resolveChatModel(ctx context.Context, input AssistantExplanationInput) (llm.Client, string, bool) {
	if a.manager == nil || a.providers == nil || a.chats == nil || input.ChatID == "" || input.OwnerID == "" {
		return nil, "", false
	}
	owner, err := primitive.ObjectIDFromHex(input.OwnerID)
	if err != nil {
		return nil, "", false
	}
	chatID, err := primitive.ObjectIDFromHex(input.ChatID)
	if err != nil {
		return nil, "", false
	}
	chat, err := a.chats.ReadCurrent(ctx, owner, chatID)
	if err != nil || chat == nil {
		return nil, "", false
	}
	providers, err := a.providers.List(ctx)
	if err != nil {
		return nil, "", false
	}
	modelID := ""
	if chat.PreferredLLMModel != nil {
		modelID = *chat.PreferredLLMModel
	}
	slug, raw := pickModelEntry(providers, modelID)
	if slug == "" {
		return nil, "", false
	}
	client, err := a.manager.GetClient(slug)
	if err != nil {
		return nil, "", false
	}
	return client, raw, true
}

func (a *assistantExplanationAdapter) explain(ctx context.Context, input AssistantExplanationInput) ([]AssistantExplanationClaim, error) {
	if a == nil || a.client == nil {
		return nil, errors.New("assistant explanation model is not configured")
	}
	userMessage, err := assistantExplanationUserMessage(input)
	if err != nil {
		return nil, err
	}
	systemPrompt := strings.TrimRight(assistantExplanationSystemPrompt, "\n") + "\n\n" + input.LanguageDirective
	client, modelID := a.client, ""
	if resolved, rawModel, ok := a.resolveChatModel(ctx, input); ok {
		client, modelID = resolved, rawModel
	}
	raw, err := client.GenerateRawJSON(ctx, systemPrompt, userMessage, modelID)
	if err != nil {
		return nil, err
	}
	response, err := parseAssistantExplanationResponse(raw)
	if err != nil {
		return nil, err
	}
	claims := make([]AssistantExplanationClaim, 0, len(response.Explanations))
	for _, claim := range response.Explanations {
		claims = append(claims, AssistantExplanationClaim{
			MetricID:     claim.MetricID,
			Value:        claim.Value,
			Text:         claim.Text,
			EvidenceRefs: claim.EvidenceRefs,
		})
	}
	return claims, nil
}

// assistantExplanationUserMessage serializes the allow-list as the user
// message: only the metric identifiers with their deterministic decimal values
// and the available evidence ref ids. No request text, entities, SQL, formulas,
// or report fields outside the allow-list are included.
func assistantExplanationUserMessage(input AssistantExplanationInput) (string, error) {
	metrics := make([]assistantExplanationMetricJSON, 0, len(input.Metrics))
	for _, metric := range input.Metrics {
		metrics = append(metrics, assistantExplanationMetricJSON{MetricID: metric.MetricID, Value: metric.Value})
	}
	payload := struct {
		Metrics      []assistantExplanationMetricJSON `json:"metrics"`
		EvidenceRefs []string                         `json:"evidence_refs"`
	}{Metrics: metrics, EvidenceRefs: input.EvidenceRefs}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseAssistantExplanationResponse decodes the provider's JSON object. Some
// providers return a markdown fence around the JSON, so the surrounding fence
// markers are stripped before decoding.
func parseAssistantExplanationResponse(raw string) (assistantExplanationResponseJSON, error) {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var response assistantExplanationResponseJSON
	if err := json.Unmarshal([]byte(cleaned), &response); err != nil {
		return assistantExplanationResponseJSON{}, fmt.Errorf("decode explanation response: %w", err)
	}
	return response, nil
}
