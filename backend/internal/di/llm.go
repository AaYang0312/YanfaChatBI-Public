package di

import (
	"context"
	"fmt"
	"log"
	"yanfachatbi/config"
	"yanfachatbi/internal/constants"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"
	"yanfachatbi/pkg/llm"
)

// llmDBConfigs returns one response-schema entry per supported database type.
func llmDBConfigs(provider string) []llm.LLMDBConfig {
	types := []string{
		constants.DatabaseTypePostgreSQL,
		constants.DatabaseTypeYugabyteDB,
		constants.DatabaseTypeTimescaleDB,
		constants.DatabaseTypeMySQL,
		constants.DatabaseTypeStarRocks,
		constants.DatabaseTypeClickhouse,
		constants.DatabaseTypeMongoDB,
		constants.DatabaseTypeSpreadsheet,
	}
	out := make([]llm.LLMDBConfig, 0, len(types))
	for _, dbType := range types {
		out = append(out, llm.LLMDBConfig{
			DBType:       dbType,
			Schema:       constants.GetLLMResponseSchema(provider, dbType),
			SystemPrompt: constants.GetSystemPrompt(provider, dbType, false),
		})
	}
	return out
}

// buildLLMClients constructs one OpenAI-compatible client per enabled provider.
// The returned order is the configured creation order, used to pick a default.
func buildLLMClients(providers []*models.ModelProvider, keys map[string]string) (map[string]llm.Client, []string, error) {
	clients := make(map[string]llm.Client, len(providers))
	order := make([]string, 0, len(providers))
	for _, provider := range providers {
		if provider == nil || !provider.Enabled || len(provider.Models) == 0 {
			continue
		}
		defaultModel := provider.Models[0]
		reasoningByModel := make(map[string]*models.ReasoningSpec)
		for i := range provider.Models {
			if spec := provider.Models[i].Reasoning; spec != nil && spec.Supported() {
				reasoningByModel[provider.Models[i].ID] = spec
			}
		}
		client, err := llm.NewOpenAIClient(llm.Config{
			Provider:            provider.Slug,
			Model:               defaultModel.ID,
			APIKey:              keys[provider.Slug],
			BaseURL:             provider.BaseURL,
			MaxCompletionTokens: defaultModel.MaxCompletionTokens,
			Temperature:         defaultModel.Temperature,
			DBConfigs:           llmDBConfigs(provider.Slug),
			ReasoningByModel:    reasoningByModel,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("register provider %s: %w", provider.Slug, err)
		}
		clients[provider.Slug] = client
		order = append(order, provider.Slug)
	}
	return clients, order, nil
}

// providerKeys decrypts stored API keys for in-memory client construction.
func providerKeys(providers []*models.ModelProvider, crypto *utils.AESGCMCrypto) map[string]string {
	keys := make(map[string]string, len(providers))
	if crypto == nil {
		return keys
	}
	for _, provider := range providers {
		if provider == nil || provider.APIKeyEncrypted == "" {
			continue
		}
		if key, err := crypto.Decrypt(provider.APIKeyEncrypted); err == nil && key != "" {
			keys[provider.Slug] = key
		}
	}
	return keys
}

// bootstrapModelProvider creates an env-derived default provider on first boot
// so an upgrade keeps the previous OPENAI_* configuration working.
func bootstrapModelProvider(ctx context.Context, repo repositories.ModelProviderRepository, crypto *utils.AESGCMCrypto) error {
	existing, err := repo.List(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	if config.Env.OpenAIAPIKey == "" {
		log.Printf("Model providers -> no stored providers and OPENAI_API_KEY is empty; models stay unconfigured")
		return nil
	}
	if crypto == nil {
		return fmt.Errorf("bootstrap model provider requires an encryption key")
	}
	modelID := config.Env.DefaultLLMModel
	if modelID == "" {
		modelID = "gpt-4o"
	}
	encrypted, err := crypto.Encrypt(config.Env.OpenAIAPIKey)
	if err != nil {
		return fmt.Errorf("encrypt bootstrap api key: %w", err)
	}
	provider := &models.ModelProvider{
		Base:            models.NewBase(),
		Name:            "Default",
		Slug:            "default",
		BaseURL:         config.Env.OpenAIBaseURL,
		APIKeyEncrypted: encrypted,
		Enabled:         true,
		Models: []models.ProviderModel{{
			ID:                  modelID,
			DisplayName:         modelID,
			MaxCompletionTokens: 30000,
			ContextLimit:        131072,
			Temperature:         1,
		}},
	}
	if err := repo.Create(ctx, provider); err != nil {
		return fmt.Errorf("bootstrap default model provider: %w", err)
	}
	log.Printf("Model providers -> bootstrapped default provider (%s) from OPENAI_* environment", modelID)
	return nil
}
