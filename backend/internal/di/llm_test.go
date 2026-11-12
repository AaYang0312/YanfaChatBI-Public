package di

import (
	"context"
	"testing"

	"yanfachatbi/config"
	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type memoryProviderRepo struct {
	providers []*models.ModelProvider
}

func (m *memoryProviderRepo) List(ctx context.Context) ([]*models.ModelProvider, error) {
	return m.providers, nil
}

func (m *memoryProviderRepo) Get(ctx context.Context, id primitive.ObjectID) (*models.ModelProvider, error) {
	for _, provider := range m.providers {
		if provider.ID == id {
			return provider, nil
		}
	}
	return nil, repositories.ErrModelProviderNotFound
}

func (m *memoryProviderRepo) GetByName(ctx context.Context, name string) (*models.ModelProvider, error) {
	for _, provider := range m.providers {
		if provider.Name == name {
			return provider, nil
		}
	}
	return nil, repositories.ErrModelProviderNotFound
}

func (m *memoryProviderRepo) Create(ctx context.Context, provider *models.ModelProvider) error {
	m.providers = append(m.providers, provider)
	return nil
}

func (m *memoryProviderRepo) Update(ctx context.Context, provider *models.ModelProvider) error {
	for i, existing := range m.providers {
		if existing.ID == provider.ID {
			m.providers[i] = provider
			return nil
		}
	}
	return repositories.ErrModelProviderNotFound
}

func (m *memoryProviderRepo) Delete(ctx context.Context, id primitive.ObjectID) error {
	return nil
}

func (m *memoryProviderRepo) EnsureIndexes(ctx context.Context) error { return nil }

func TestBootstrapModelProviderCreatesDefaultFromEnv(t *testing.T) {
	original := config.Env
	t.Cleanup(func() { config.Env = original })
	config.Env.OpenAIAPIKey = "sk-synthetic-bootstrap"
	config.Env.OpenAIBaseURL = "https://api.example.com/v1"
	config.Env.DefaultLLMModel = "glm-5.3-flash"

	repo := &memoryProviderRepo{}
	crypto, err := utils.NewAESGCMCrypto("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapModelProvider(context.Background(), repo, crypto); err != nil {
		t.Fatal(err)
	}
	if len(repo.providers) != 1 {
		t.Fatalf("bootstrap created %d providers", len(repo.providers))
	}
	provider := repo.providers[0]
	if provider.Slug != "default" || provider.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("unexpected bootstrap provider: %+v", provider)
	}
	if len(provider.Models) != 1 || provider.Models[0].ID != "glm-5.3-flash" {
		t.Fatalf("bootstrap model = %+v", provider.Models)
	}
	key, err := crypto.Decrypt(provider.APIKeyEncrypted)
	if err != nil || key != "sk-synthetic-bootstrap" {
		t.Fatalf("bootstrap key not encrypted correctly: %v", err)
	}

	// A second call must not duplicate the provider.
	if err := bootstrapModelProvider(context.Background(), repo, crypto); err != nil {
		t.Fatal(err)
	}
	if len(repo.providers) != 1 {
		t.Fatal("bootstrap ran twice")
	}
}

func TestBootstrapModelProviderWithoutEnvCreatesNothing(t *testing.T) {
	original := config.Env
	t.Cleanup(func() { config.Env = original })
	config.Env.OpenAIAPIKey = ""

	repo := &memoryProviderRepo{}
	crypto, _ := utils.NewAESGCMCrypto("0123456789abcdef0123456789abcdef")
	if err := bootstrapModelProvider(context.Background(), repo, crypto); err != nil {
		t.Fatal(err)
	}
	if len(repo.providers) != 0 {
		t.Fatal("bootstrap created a provider without credentials")
	}
}

func TestBuildLLMClientsSkipsDisabledAndMapsReasoning(t *testing.T) {
	providers := []*models.ModelProvider{
		{
			Base:    models.NewBase(),
			Name:    "Enabled", Slug: "enabled", BaseURL: "https://api.example.com/v1", Enabled: true,
			Models: []models.ProviderModel{{
				ID: "m1", MaxCompletionTokens: 100, ContextLimit: 1000, Temperature: 1,
				Reasoning: &models.ReasoningSpec{Mode: models.ReasoningModeEffort, Param: "reasoning_effort", Levels: []string{"low", "high"}, DefaultLevel: "low"},
			}},
		},
		{
			Base:    models.NewBase(),
			Name:    "Disabled", Slug: "disabled", BaseURL: "https://api.example.com/v1", Enabled: false,
			Models:  []models.ProviderModel{{ID: "m2", MaxCompletionTokens: 100, ContextLimit: 1000, Temperature: 1}},
		},
	}
	clients, order, err := buildLLMClients(providers, map[string]string{"enabled": "sk-x", "disabled": "sk-y"})
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || len(order) != 1 || order[0] != "enabled" {
		t.Fatalf("clients=%d order=%v", len(clients), order)
	}
}
