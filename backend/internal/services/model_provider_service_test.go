package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type fakeProviderRepo struct {
	providers []*models.ModelProvider
}

func (f *fakeProviderRepo) List(ctx context.Context) ([]*models.ModelProvider, error) {
	return f.providers, nil
}

func (f *fakeProviderRepo) Get(ctx context.Context, id primitive.ObjectID) (*models.ModelProvider, error) {
	for _, provider := range f.providers {
		if provider.ID == id {
			return provider, nil
		}
	}
	return nil, repositories.ErrModelProviderNotFound
}

func (f *fakeProviderRepo) GetByName(ctx context.Context, name string) (*models.ModelProvider, error) {
	for _, provider := range f.providers {
		if provider.Name == name {
			return provider, nil
		}
	}
	return nil, repositories.ErrModelProviderNotFound
}

func (f *fakeProviderRepo) Create(ctx context.Context, provider *models.ModelProvider) error {
	for _, existing := range f.providers {
		if existing.Name == provider.Name {
			return repositories.ErrModelProviderConflict
		}
	}
	f.providers = append(f.providers, provider)
	return nil
}

func (f *fakeProviderRepo) Update(ctx context.Context, provider *models.ModelProvider) error {
	for i, existing := range f.providers {
		if existing.ID == provider.ID {
			f.providers[i] = provider
			return nil
		}
	}
	return repositories.ErrModelProviderNotFound
}

func (f *fakeProviderRepo) Delete(ctx context.Context, id primitive.ObjectID) error {
	for i, existing := range f.providers {
		if existing.ID == id {
			f.providers = append(f.providers[:i], f.providers[i+1:]...)
			return nil
		}
	}
	return repositories.ErrModelProviderNotFound
}

func (f *fakeProviderRepo) EnsureIndexes(ctx context.Context) error { return nil }

func newTestProviderService(t *testing.T, repo repositories.ModelProviderRepository) *ModelProviderService {
	t.Helper()
	crypto, err := utils.NewAESGCMCrypto("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	return NewModelProviderService(repo, crypto, nil)
}

func TestModelProviderCreateValidatesInput(t *testing.T) {
	service := newTestProviderService(t, &fakeProviderRepo{})
	ctx := context.Background()

	if _, err := service.Create(ctx, ProviderInput{Name: "", BaseURL: "https://api.example.com/v1", APIKey: "k"}); !errors.Is(err, ErrModelProviderInvalid) {
		t.Fatalf("empty name accepted: %v", err)
	}
	if _, err := service.Create(ctx, ProviderInput{Name: "x", BaseURL: "ftp://example.com", APIKey: "k"}); !errors.Is(err, ErrModelProviderInvalid) {
		t.Fatalf("non-http base URL accepted: %v", err)
	}
	// No key and no explicit allowance is rejected.
	if _, err := service.Create(ctx, ProviderInput{Name: "Local", BaseURL: "http://127.0.0.1:8000/v1"}); !errors.Is(err, ErrModelProviderInvalid) {
		t.Fatalf("missing api key accepted without allowance: %v", err)
	}
}

func TestModelProviderCreateEncryptsAndMasks(t *testing.T) {
	repo := &fakeProviderRepo{}
	service := newTestProviderService(t, repo)
	view, err := service.Create(context.Background(), ProviderInput{
		Name:    "DeepSeek",
		BaseURL: "https://api.deepseek.com/v1",
		APIKey:  "sk-synthetic-secret-value",
		Models: []models.ProviderModel{{
			ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash",
			MaxCompletionTokens: 8192, ContextLimit: 131072, Temperature: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.APIKeyMasked != "sk-****alue" {
		t.Fatalf("mask = %q", view.APIKeyMasked)
	}
	if view.Slug != "deepseek" {
		t.Fatalf("slug = %q", view.Slug)
	}
	if repo.providers[0].APIKeyEncrypted == "" || repo.providers[0].APIKeyEncrypted == "sk-synthetic-secret-value" {
		t.Fatal("api key stored in plaintext")
	}

	list, err := service.List(context.Background())
	if err != nil || len(list) != 1 || list[0].APIKeyMasked != "sk-****alue" {
		t.Fatalf("list leaked or dropped key: %v %v", err, list)
	}
}

func TestModelProviderProbeAndRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{
			{"id": "deepseek-v4-flash"}, {"id": "deepseek-v4-pro"},
		}})
	}))
	defer server.Close()

	repo := &fakeProviderRepo{}
	service := newTestProviderService(t, repo)
	ids, err := service.Probe(context.Background(), server.URL+"/v1", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "deepseek-v4-flash" {
		t.Fatalf("probe ids = %v", ids)
	}

	if _, err := service.Probe(context.Background(), server.URL+"/v1", "wrong"); !errors.Is(err, ErrModelProviderUnauthorized) {
		t.Fatalf("unauthorized probe accepted: %v", err)
	}

	view, err := service.Create(context.Background(), ProviderInput{
		Name:          "Probe Provider",
		BaseURL:       server.URL + "/v1",
		APIKey:        "sk-test",
		RefreshModels: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Models) != 2 || view.Models[0].ID != "deepseek-v4-flash" {
		t.Fatalf("refresh_models did not populate: %+v", view.Models)
	}
	if view.ModelsRefreshedAt == nil {
		t.Fatal("models_refreshed_at not set")
	}

	refreshed, err := service.RefreshModels(context.Background(), repo.providers[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed.Models) != 2 {
		t.Fatalf("refresh lost models: %+v", refreshed.Models)
	}
}

func TestModelProviderUpdateKeepsStoredKey(t *testing.T) {
	repo := &fakeProviderRepo{}
	service := newTestProviderService(t, repo)
	ctx := context.Background()
	created, err := service.Create(ctx, ProviderInput{
		Name: "Keep", BaseURL: "https://api.example.com/v1", APIKey: "sk-original",
		Models: []models.ProviderModel{{ID: "m1", MaxCompletionTokens: 100, ContextLimit: 1000, Temperature: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := primitive.ObjectIDFromHex(created.ID)
	if _, err := service.Update(ctx, id, ProviderInput{Name: "Keep", BaseURL: "https://api.example.com/v1"}); err != nil {
		t.Fatalf("update without key should keep the stored one: %v", err)
	}
	providers, _ := repo.List(ctx)
	if providers[0].APIKeyEncrypted == "" {
		t.Fatal("stored key was cleared by an update without api_key")
	}
}

func TestModelProviderDeleteAndNameConflict(t *testing.T) {
	repo := &fakeProviderRepo{}
	service := newTestProviderService(t, repo)
	ctx := context.Background()
	first, err := service.Create(ctx, ProviderInput{
		Name: "Alpha", BaseURL: "https://api.example.com/v1", APIKey: "sk-a",
		Models: []models.ProviderModel{{ID: "m1", MaxCompletionTokens: 100, ContextLimit: 1000, Temperature: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, ProviderInput{
		Name: "alpha", BaseURL: "https://api.example.com/v1", APIKey: "sk-b",
		Models: []models.ProviderModel{{ID: "m2", MaxCompletionTokens: 100, ContextLimit: 1000, Temperature: 1}},
	}); !errors.Is(err, repositories.ErrModelProviderConflict) {
		t.Fatalf("case-insensitive duplicate accepted: %v", err)
	}
	id, _ := primitive.ObjectIDFromHex(first.ID)
	if err := service.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	list, _ := service.List(ctx)
	if len(list) != 0 {
		t.Fatalf("delete left records: %v", list)
	}
}

func TestModelProviderValidatesReasoning(t *testing.T) {
	service := newTestProviderService(t, &fakeProviderRepo{})
	ctx := context.Background()
	base := func(spec *models.ReasoningSpec) ProviderInput {
		return ProviderInput{
			Name: "R", BaseURL: "https://api.example.com/v1", APIKey: "k",
			Models: []models.ProviderModel{{ID: "m1", MaxCompletionTokens: 100, ContextLimit: 1000, Temperature: 1, Reasoning: spec}},
		}
	}
	if _, err := service.Create(ctx, base(&models.ReasoningSpec{Mode: "bogus", Param: "x", Levels: []string{"a"}})); !errors.Is(err, ErrModelProviderInvalid) {
		t.Fatalf("bogus mode accepted: %v", err)
	}
	if _, err := service.Create(ctx, base(&models.ReasoningSpec{Mode: models.ReasoningModeEffort, Param: "", Levels: []string{"a"}})); !errors.Is(err, ErrModelProviderInvalid) {
		t.Fatalf("empty param accepted: %v", err)
	}
	if _, err := service.Create(ctx, base(&models.ReasoningSpec{Mode: models.ReasoningModeEffort, Param: "reasoning_effort", Levels: []string{"low"}, DefaultLevel: "high"})); !errors.Is(err, ErrModelProviderInvalid) {
		t.Fatalf("default outside levels accepted: %v", err)
	}
	if _, err := service.Create(ctx, base(&models.ReasoningSpec{Mode: models.ReasoningModeToggle, Param: "thinking.type", Levels: []string{"on", "off"}})); err != nil {
		t.Fatalf("valid toggle rejected: %v", err)
	}
}
