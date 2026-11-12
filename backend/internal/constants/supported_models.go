package constants

import (
	"log"
	"strings"
)

// LLMModel represents a supported LLM model configuration
type LLMModel struct {
	ID                  string  `json:"id"`                  // Unique identifier (e.g., "gpt-4o", "gemini-2.0-flash")
	Provider            string  `json:"provider"`            // Provider name (e.g., "openai", "gemini")
	DisplayName         string  `json:"displayName"`         // Human-readable name (e.g., "GPT-4 Omni")
	IsEnabled           bool    `json:"isEnabled"`           // Whether this model is available for use/allowed by the admin to use this.
	Default             *bool   `json:"default"`             // Default for the provider's enabled endpoint catalogue (only one should be true)
	APIVersion          string  `json:"apiVersion"`          // API version for the model (e.g., "v1", "v1beta", "v1alpha" for Gemini models)
	MaxCompletionTokens int     `json:"maxCompletionTokens"` // Maximum tokens for completion
	Temperature         float64 `json:"temperature"`         // Default temperature for this model
	InputTokenLimit     int     `json:"inputTokenLimit"`     // Maximum input tokens
	Description         string  `json:"description"`         // Brief description of the model
}

// SupportedLLMModels is the built-in OpenAI-compatible catalogue. Gemini,
// Claude and Ollama were removed; their endpoints are configured through the
// model-provider settings like any other OpenAI-compatible service.
var SupportedLLMModels = OpenAILLMModels

// OpenAIProxyModelIDs lists model IDs served through generic custom
// OPENAI_BASE_URL proxies. Endpoint-specific models are filtered separately.
var OpenAIProxyModelIDs = map[string]bool{
	"remote-magpie/cursor/grok-4.6": true,
}

// ApplyOpenAIProxyOverride restricts OpenAI models to the configured endpoint.
// Always call after DisableUnavailableProviders; this only disables models.
func ApplyOpenAIProxyOverride(baseURL string) {
	isBigModel := strings.TrimRight(baseURL, "/") == "https://open.bigmodel.cn/api/paas/v4"
	for i := range SupportedLLMModels {
		model := &SupportedLLMModels[i]
		if model.Provider != OpenAI {
			continue
		}
		switch {
		case isBigModel:
			if model.ID != "glm-5.3-flash" {
				model.IsEnabled = false
			}
		case baseURL == "":
			if model.ID == "glm-5.3-flash" || OpenAIProxyModelIDs[model.ID] {
				model.IsEnabled = false
			}
		default:
			if model.ID == "glm-5.3-flash" || !OpenAIProxyModelIDs[model.ID] {
				model.IsEnabled = false
			}
		}
	}
}

// GetEnabledLLMModels returns only enabled LLM models
func GetEnabledLLMModels() []LLMModel {
	var enabled []LLMModel
	for _, model := range SupportedLLMModels {
		if model.IsEnabled {
			enabled = append(enabled, model)
		}
	}
	return enabled
}

// GetLLMModelsByProvider returns all models for a specific provider
func GetLLMModelsByProvider(provider string) []LLMModel {
	var models []LLMModel
	for _, model := range SupportedLLMModels {
		if model.Provider == provider && model.IsEnabled {
			models = append(models, model)
		}
	}
	return models
}

// GetLLMModel returns a specific model by ID
func GetLLMModel(modelID string) *LLMModel {
	for i := range SupportedLLMModels {
		if SupportedLLMModels[i].ID == modelID {
			return &SupportedLLMModels[i]
		}
	}
	return nil
}

// GetDefaultModelForProvider returns the default model for a provider
// Prioritizes the model with Default=true, then falls back to first enabled model
func GetDefaultModelForProvider(provider string) *LLMModel {
	// First pass: look for the model marked as default
	for i := range SupportedLLMModels {
		model := &SupportedLLMModels[i]
		if model.Provider == provider && model.IsEnabled && model.Default != nil && *model.Default {
			return model
		}
	}
	// Fallback: return first enabled model for this provider
	for i := range SupportedLLMModels {
		if SupportedLLMModels[i].Provider == provider && SupportedLLMModels[i].IsEnabled {
			return &SupportedLLMModels[i]
		}
	}
	return nil
}

// DisableUnavailableProviders disables all models for providers that don't have API keys configured
// Call this during application startup to prevent fallback to unavailable providers
func DisableUnavailableProviders(openAIKey string) {
	for i := range SupportedLLMModels {
		if SupportedLLMModels[i].Provider == OpenAI && openAIKey == "" {
			SupportedLLMModels[i].IsEnabled = false
		}
	}
}

// GetFirstAvailableModel returns the first available (enabled) model from any provider
// Priority order: OpenAI -> Gemini -> Claude -> Ollama (matches initialization order)
func GetFirstAvailableModel() *LLMModel {
	// Try providers in order of preference
	providers := []string{OpenAI, Gemini, Claude, Ollama}
	for _, provider := range providers {
		if model := GetDefaultModelForProvider(provider); model != nil {
			return model
		}
	}
	return nil
}

// IsValidModel checks if a model ID is valid and enabled
func IsValidModel(modelID string) bool {
	model := GetLLMModel(modelID)
	return model != nil && model.IsEnabled
}

// GetAvailableModelsByAPIKeys returns only models for providers that have API keys configured
// Pass empty strings for API keys that are not available
// This filters the model list based on which API providers are actually configured
func GetAvailableModelsByAPIKeys(openAIKey string) []LLMModel {
	var available []LLMModel
	for _, model := range SupportedLLMModels {
		if !model.IsEnabled {
			continue
		}
		if model.Provider == OpenAI && openAIKey != "" {
			available = append(available, model)
		}
	}
	return available
}

// GetAvailableModelsByProviderAndKeys returns models for a specific provider if API key is configured
func GetAvailableModelsByProviderAndKeys(provider, apiKey string) []LLMModel {
	if apiKey == "" {
		return []LLMModel{} // Return empty list if API key not configured
	}

	return GetLLMModelsByProvider(provider)
}

// LogModelInitialization reports the built-in catalogue. Runtime providers are
// configured through model-provider settings, so this is informational only.
func LogModelInitialization(openAIKey string) {
	separator := strings.Repeat("=", 80)
	log.Println("\n" + separator)
	log.Println("🤖 LLM MODEL INITIALIZATION REPORT")
	log.Println(separator)

	available := GetAvailableModelsByAPIKeys(openAIKey)
	log.Println("\n📘 OPENAI-COMPATIBLE MODELS:")
	if openAIKey != "" {
		log.Printf("  ✅ API Key: CONFIGURED\n")
		log.Printf("  📊 Available Models: %d/%d\n", len(available), len(SupportedLLMModels))
		for _, model := range available {
			log.Printf("     • %s (%s)\n", model.DisplayName, model.ID)
		}
	} else {
		log.Printf("  ❌ API Key: NOT CONFIGURED\n")
		log.Printf("  📊 Available Models: 0/%d\n", len(SupportedLLMModels))
		log.Printf("  ⚠️  Configure providers in model settings or set OPENAI_API_KEY for the bootstrap provider\n")
	}
}

// ptrBool is a helper function to create a pointer to a bool value
func ptrBool(b bool) *bool {
	return &b
}
