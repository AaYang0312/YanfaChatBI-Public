package embedding

import (
	"fmt"
	"log"
)

// NewProvider creates an embedding provider based on configuration.
// If provider is empty, it auto-detects from available API keys.
func NewProvider(cfg Config) (Provider, error) {
	provider := cfg.Provider

	// Auto-detect provider from API key if not explicitly set
	if provider == "" {
		if cfg.APIKey != "" {
			// Caller should set provider; this is a fallback
			return nil, fmt.Errorf("embedding provider must be specified when API key is provided")
		}
		return nil, fmt.Errorf("no embedding provider configured")
	}

	switch provider {
	case "openai":
		model := cfg.Model
		if model == "" {
			model = DefaultOpenAIModel
		}
		return NewOpenAIProvider(cfg.APIKey, model, cfg.BaseURL)

	default:
		return nil, fmt.Errorf("unsupported embedding provider: %s", provider)
	}
}

// AutoDetectProvider creates an OpenAI-compatible embedding provider from the
// configured credentials. baseURL is an optional endpoint override.
func AutoDetectProvider(openAIKey, preferredProvider, preferredModel, baseURL string) (Provider, error) {
	if preferredProvider != "" && preferredProvider != "openai" {
		return nil, fmt.Errorf("unsupported embedding provider %q: only openai-compatible endpoints are supported", preferredProvider)
	}
	if openAIKey == "" {
		log.Printf("Embedding -> No embedding provider available (no OpenAI-compatible API key)")
		return nil, nil // nil, nil = no provider available, not an error
	}
	log.Printf("Embedding -> Auto-detected OpenAI-compatible embedding provider")
	return NewProvider(Config{
		Provider: "openai",
		Model:    preferredModel,
		APIKey:   openAIKey,
		BaseURL:  baseURL,
	})
}
