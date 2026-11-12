package services

import (
	"context"
	"log"
	"strings"

	"yanfachatbi/internal/models"
	"yanfachatbi/pkg/llm"
)

// SetModelProviderService wires the admin-configured model catalogue.
func (s *chatService) SetModelProviderService(providers *ModelProviderService) {
	s.modelProviders = providers
}

// resolveLLMSelection maps a stored preferred model (either "slug/model" or a
// legacy bare name) to a provider client and the raw model ID sent upstream.
// Unknown or ambiguous values fall back to the first enabled provider/model.
func (s *chatService) resolveLLMSelection(ctx context.Context, modelID string) (client llm.Client, rawModel string, ok bool) {
	if s.llmManager == nil || s.modelProviders == nil {
		return nil, "", false
	}
	providers, err := s.modelProviders.List(ctx)
	if err != nil {
		log.Printf("ChatService -> resolveLLMSelection -> list providers failed: %v", err)
		return nil, "", false
	}
	slug, raw := pickModelEntry(providers, modelID)
	if slug == "" {
		return nil, "", false
	}
	providerClient, err := s.llmManager.GetClient(slug)
	if err != nil {
		log.Printf("ChatService -> resolveLLMSelection -> client for %s unavailable: %v", slug, err)
		return nil, "", false
	}
	return providerClient, raw, true
}

// reasoningSpecFor returns the reasoning spec for a model in the catalogue.
func (s *chatService) reasoningSpecFor(ctx context.Context, modelID string) *models.ReasoningSpec {
	if s.modelProviders == nil {
		return nil
	}
	providers, err := s.modelProviders.List(ctx)
	if err != nil {
		return nil
	}
	slug, raw := pickModelEntry(providers, modelID)
	if slug == "" {
		return nil
	}
	for _, provider := range providers {
		if provider.Slug != slug {
			continue
		}
		for i := range provider.Models {
			if provider.Models[i].ID == raw && provider.Models[i].Reasoning != nil && provider.Models[i].Reasoning.Supported() {
				return provider.Models[i].Reasoning
			}
		}
	}
	return nil
}

// pickModelEntry resolves a stored model ID against the provider catalogue.
// Returns ("", "") when nothing is usable.
func pickModelEntry(providers []ProviderView, modelID string) (slug, raw string) {
	trimmed := strings.TrimSpace(modelID)
	if slugHint, rawHint, ok := models.SplitModelID(trimmed); ok {
		for _, provider := range providers {
			if !provider.Enabled || provider.Slug != slugHint {
				continue
			}
			for i := range provider.Models {
				if provider.Models[i].ID == rawHint {
					return provider.Slug, rawHint
				}
			}
		}
	}
	if trimmed != "" {
		for _, provider := range providers {
			if !provider.Enabled {
				continue
			}
			for i := range provider.Models {
				if provider.Models[i].ID == trimmed {
					return provider.Slug, trimmed
				}
			}
		}
	}
	for _, provider := range providers {
		if provider.Enabled && len(provider.Models) > 0 {
			return provider.Slug, provider.Models[0].ID
		}
	}
	return "", ""
}

// defaultModelID returns the composite ID of the first enabled provider model.
func (s *chatService) defaultModelID(ctx context.Context) (string, bool) {
	if s.modelProviders == nil {
		return "", false
	}
	providers, err := s.modelProviders.List(ctx)
	if err != nil {
		return "", false
	}
	for _, provider := range providers {
		if provider.Enabled && len(provider.Models) > 0 {
			return provider.Slug + "/" + provider.Models[0].ID, true
		}
	}
	return "", false
}
