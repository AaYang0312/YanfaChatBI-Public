package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"yanfachatbi/internal/models"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/utils"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	// ErrModelProviderInvalid marks a rejected provider payload.
	ErrModelProviderInvalid = errors.New("MODEL_PROVIDER_INVALID")
	// ErrModelProviderUnauthorized marks a probe rejected by the endpoint (401/403).
	ErrModelProviderUnauthorized = errors.New("MODEL_PROVIDER_PROBE_UNAUTHORIZED")
	// ErrModelProviderModelsUnsupported marks an endpoint without {base}/models.
	ErrModelProviderModelsUnsupported = errors.New("MODEL_PROVIDER_PROBE_UNSUPPORTED")
	// ErrModelProviderProbeTimeout marks a probe that exceeded the deadline.
	ErrModelProviderProbeTimeout = errors.New("MODEL_PROVIDER_PROBE_TIMEOUT")
	// ErrModelProviderProbeInvalid marks a response that is not an OpenAI model list.
	ErrModelProviderProbeInvalid = errors.New("MODEL_PROVIDER_PROBE_INVALID")
	// ErrModelProviderKeyUnconfigured marks a missing encryption key.
	ErrModelProviderKeyUnconfigured = errors.New("MODEL_PROVIDER_KEY_UNCONFIGURED")
)

const modelProviderProbeTimeout = 10 * time.Second

// ProviderView is the sanitized provider representation returned by the API.
type ProviderView struct {
	ID                string                 `json:"id"`
	Name              string                 `json:"name"`
	Slug              string                 `json:"slug"`
	BaseURL           string                 `json:"base_url"`
	APIKeyMasked      string                 `json:"api_key_masked"`
	HasAPIKey         bool                   `json:"has_api_key"`
	Models            []models.ProviderModel `json:"models"`
	Enabled           bool                   `json:"enabled"`
	PresetID          string                 `json:"preset_id,omitempty"`
	ModelsRefreshedAt *time.Time             `json:"models_refreshed_at,omitempty"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

// ProviderInput is the create/update payload. Empty APIKey on update keeps
// the stored key; RefreshModels asks the service to fetch the catalogue.
type ProviderInput struct {
	Name              string                 `json:"name"`
	BaseURL           string                 `json:"base_url"`
	APIKey            string                 `json:"api_key"`
	Models            []models.ProviderModel `json:"models"`
	Enabled           *bool                  `json:"enabled"`
	PresetID          string                 `json:"preset_id"`
	RefreshModels     bool                   `json:"refresh_models"`
	AllowEmptyAPIKey  bool                   `json:"allow_empty_api_key"`
	ModelsRefreshedAt *time.Time             `json:"models_refreshed_at"`
}

// ModelProviderRebuilder receives the full provider set plus plaintext keys
// (in memory only) so the LLM manager can rebuild its clients.
type ModelProviderRebuilder func(providers []*models.ModelProvider, apiKeys map[string]string) error

// ModelProviderService manages OpenAI-compatible provider configurations.
type ModelProviderService struct {
	repo    repositories.ModelProviderRepository
	crypto  *utils.AESGCMCrypto
	rebuild ModelProviderRebuilder
}

// NewModelProviderService wires the repository, crypto and optional rebuild hook.
func NewModelProviderService(repo repositories.ModelProviderRepository, crypto *utils.AESGCMCrypto, rebuild ModelProviderRebuilder) *ModelProviderService {
	return &ModelProviderService{repo: repo, crypto: crypto, rebuild: rebuild}
}

// List returns every configured provider with masked keys.
func (s *ModelProviderService) List(ctx context.Context) ([]ProviderView, error) {
	providers, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderView, 0, len(providers))
	for _, provider := range providers {
		out = append(out, s.view(provider))
	}
	return out, nil
}

// Create validates and stores a new provider.
func (s *ModelProviderService) Create(ctx context.Context, input ProviderInput) (*ProviderView, error) {
	if err := s.ensureCrypto(); err != nil {
		return nil, err
	}
	if err := s.validateInput(ctx, "", input); err != nil {
		return nil, err
	}
	slug, err := s.uniqueSlug(ctx, input.Name, "")
	if err != nil {
		return nil, err
	}
	provider := &models.ModelProvider{
		Base:     models.NewBase(),
		Name:     strings.TrimSpace(input.Name),
		Slug:     slug,
		BaseURL:  strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"),
		Enabled:  input.Enabled == nil || *input.Enabled,
		PresetID: input.PresetID,
	}
	if input.APIKey != "" {
		encrypted, err := s.crypto.Encrypt(input.APIKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt api key: %w", err)
		}
		provider.APIKeyEncrypted = encrypted
	}
	modelsList, refreshedAt, err := s.resolveModels(ctx, provider.BaseURL, input.APIKey, input, false)
	if err != nil {
		return nil, err
	}
	provider.Models = modelsList
	provider.ModelsRefreshedAt = refreshedAt
	if err := s.repo.Create(ctx, provider); err != nil {
		return nil, err
	}
	if err := s.rebuildNow(ctx); err != nil {
		return nil, err
	}
	view := s.view(provider)
	return &view, nil
}

// Update modifies an existing provider. An empty APIKey keeps the stored one.
func (s *ModelProviderService) Update(ctx context.Context, id primitive.ObjectID, input ProviderInput) (*ProviderView, error) {
	if err := s.ensureCrypto(); err != nil {
		return nil, err
	}
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.validateInput(ctx, existing.ID.Hex(), input); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(input.Name), existing.Name) {
		slug, err := s.uniqueSlug(ctx, input.Name, existing.ID.Hex())
		if err != nil {
			return nil, err
		}
		existing.Slug = slug
	}
	existing.Name = strings.TrimSpace(input.Name)
	existing.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if input.Enabled != nil {
		existing.Enabled = *input.Enabled
	}
	if input.APIKey != "" {
		encrypted, err := s.crypto.Encrypt(input.APIKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt api key: %w", err)
		}
		existing.APIKeyEncrypted = encrypted
	}

	plainKey := input.APIKey
	if plainKey == "" {
		plainKey, _ = s.decryptKey(existing)
	}
	modelsList, refreshedAt, err := s.resolveModels(ctx, existing.BaseURL, plainKey, input, true)
	if err != nil {
		return nil, err
	}
	if modelsList != nil {
		existing.Models = modelsList
		existing.ModelsRefreshedAt = refreshedAt
	}
	existing.UpdatedAt = time.Now()
	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, err
	}
	if err := s.rebuildNow(ctx); err != nil {
		return nil, err
	}
	view := s.view(existing)
	return &view, nil
}

// Delete removes a provider and rebuilds the client set.
func (s *ModelProviderService) Delete(ctx context.Context, id primitive.ObjectID) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	return s.rebuildNow(ctx)
}

// Probe fetches the model catalogue from an OpenAI-compatible endpoint
// without persisting anything.
func (s *ModelProviderService) Probe(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	if !validBaseURL(baseURL) {
		return nil, fmt.Errorf("%w: base_url must be an http(s) URL", ErrModelProviderInvalid)
	}
	return s.fetchModels(ctx, baseURL, apiKey)
}

// RefreshModels re-reads the catalogue for a stored provider.
func (s *ModelProviderService) RefreshModels(ctx context.Context, id primitive.ObjectID) (*ProviderView, error) {
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	key, err := s.decryptKey(existing)
	if err != nil {
		return nil, err
	}
	ids, err := s.fetchModels(ctx, existing.BaseURL, key)
	if err != nil {
		return nil, err
	}
	existing.Models = mergeModels(existing.Models, ids)
	now := time.Now().UTC().Truncate(time.Millisecond)
	existing.ModelsRefreshedAt = &now
	existing.UpdatedAt = now
	if err := s.repo.Update(ctx, existing); err != nil {
		return nil, err
	}
	if err := s.rebuildNow(ctx); err != nil {
		return nil, err
	}
	view := s.view(existing)
	return &view, nil
}

// ProvidersWithKeys loads providers and decrypts keys for the LLM manager.
// The returned map holds plaintext keys in memory only.
func (s *ModelProviderService) ProvidersWithKeys(ctx context.Context) ([]*models.ModelProvider, map[string]string, error) {
	providers, err := s.repo.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys := make(map[string]string, len(providers))
	for _, provider := range providers {
		if key, err := s.decryptKey(provider); err == nil && key != "" {
			keys[provider.Slug] = key
		}
	}
	return providers, keys, nil
}

// Rebuild re-reads the store and notifies the manager. Startup uses it after
// bootstrapping the default provider.
func (s *ModelProviderService) Rebuild(ctx context.Context) error {
	return s.rebuildNow(ctx)
}

func (s *ModelProviderService) rebuildNow(ctx context.Context) error {
	if s.rebuild == nil {
		return nil
	}
	providers, keys, err := s.ProvidersWithKeys(ctx)
	if err != nil {
		return err
	}
	if err := s.rebuild(providers, keys); err != nil {
		return fmt.Errorf("rebuild llm clients: %w", err)
	}
	return nil
}

func (s *ModelProviderService) ensureCrypto() error {
	if s.crypto == nil {
		return fmt.Errorf("%w: set SCHEMA_ENCRYPTION_KEY", ErrModelProviderKeyUnconfigured)
	}
	return nil
}

func (s *ModelProviderService) decryptKey(provider *models.ModelProvider) (string, error) {
	if provider == nil || provider.APIKeyEncrypted == "" {
		return "", nil
	}
	if s.crypto == nil {
		return "", ErrModelProviderKeyUnconfigured
	}
	key, err := s.crypto.Decrypt(provider.APIKeyEncrypted)
	if err != nil {
		log.Printf("ModelProvider -> decrypt key for %s failed: %v", provider.Slug, err)
		return "", nil
	}
	return key, nil
}

func (s *ModelProviderService) view(provider *models.ModelProvider) ProviderView {
	key, _ := s.decryptKey(provider)
	return ProviderView{
		ID:                provider.ID.Hex(),
		Name:              provider.Name,
		Slug:              provider.Slug,
		BaseURL:           provider.BaseURL,
		APIKeyMasked:      maskAPIKey(key),
		HasAPIKey:         key != "",
		Models:            provider.Models,
		Enabled:           provider.Enabled,
		PresetID:          provider.PresetID,
		ModelsRefreshedAt: provider.ModelsRefreshedAt,
		CreatedAt:         provider.CreatedAt,
		UpdatedAt:         provider.UpdatedAt,
	}
}

// resolveModels decides between caller-supplied models and an automatic
// catalogue fetch. Returns nil models when the caller keeps what is stored.
func (s *ModelProviderService) resolveModels(ctx context.Context, baseURL, apiKey string, input ProviderInput, keepExisting bool) ([]models.ProviderModel, *time.Time, error) {
	if len(input.Models) > 0 && !input.RefreshModels {
		if err := validateModels(input.Models); err != nil {
			return nil, nil, err
		}
		return input.Models, input.ModelsRefreshedAt, nil
	}
	if !input.RefreshModels && keepExisting {
		// An update without models keeps what is stored; it must not call the
		// endpoint just because the client omitted the field.
		return nil, nil, nil
	}
	ids, err := s.fetchModels(ctx, baseURL, apiKey)
	if err != nil {
		if len(input.Models) > 0 {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("%w: %v", ErrModelProviderProbeInvalid, err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	return mergeModels(input.Models, ids), &now, nil
}

func (s *ModelProviderService) fetchModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	probeCtx, cancel := context.WithTimeout(ctx, modelProviderProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelProviderInvalid, err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: modelProviderProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %v", ErrModelProviderProbeTimeout, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrModelProviderInvalid, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%w: status %d", ErrModelProviderUnauthorized, resp.StatusCode)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return nil, fmt.Errorf("%w: status %d", ErrModelProviderModelsUnsupported, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d", ErrModelProviderInvalid, resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelProviderProbeInvalid, err)
	}
	ids := make([]string, 0, len(payload.Data)+len(payload.Models))
	seen := make(map[string]bool)
	for _, item := range payload.Data {
		if item.ID != "" && !seen[item.ID] {
			seen[item.ID] = true
			ids = append(ids, item.ID)
		}
	}
	for _, item := range payload.Models {
		if item.ID != "" && !seen[item.ID] {
			seen[item.ID] = true
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: empty model list", ErrModelProviderProbeInvalid)
	}
	return ids, nil
}

func (s *ModelProviderService) validateInput(ctx context.Context, currentID string, input ProviderInput) error {
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > 64 {
		return fmt.Errorf("%w: name must be 1-64 characters", ErrModelProviderInvalid)
	}
	if !validBaseURL(input.BaseURL) {
		return fmt.Errorf("%w: base_url must be an http(s) URL", ErrModelProviderInvalid)
	}
	stored, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	hasStoredKey := false
	for _, provider := range stored {
		if provider.ID.Hex() == currentID {
			hasStoredKey = provider.APIKeyEncrypted != ""
			continue
		}
		if strings.EqualFold(provider.Name, name) {
			return fmt.Errorf("%w: a provider named %q already exists", repositories.ErrModelProviderConflict, name)
		}
	}
	if input.APIKey == "" && !hasStoredKey && !input.AllowEmptyAPIKey {
		return fmt.Errorf("%w: api_key is required for this endpoint", ErrModelProviderInvalid)
	}
	return validateModels(input.Models)
}

func validateModels(list []models.ProviderModel) error {
	for i := range list {
		model := &list[i]
		if strings.TrimSpace(model.ID) == "" {
			return fmt.Errorf("%w: model id is required", ErrModelProviderInvalid)
		}
		if model.DisplayName == "" {
			model.DisplayName = model.ID
		}
		if model.MaxCompletionTokens <= 0 {
			model.MaxCompletionTokens = 8192
		}
		if model.ContextLimit <= 0 {
			model.ContextLimit = 131072
		}
		if model.Temperature <= 0 {
			model.Temperature = 1
		}
		if err := validateReasoning(model.Reasoning); err != nil {
			return err
		}
	}
	return nil
}

func validateReasoning(spec *models.ReasoningSpec) error {
	if spec == nil || spec.Mode == "" || spec.Mode == models.ReasoningModeNone {
		return nil
	}
	switch spec.Mode {
	case models.ReasoningModeEffort, models.ReasoningModeCustom, models.ReasoningModeToggle:
	default:
		return fmt.Errorf("%w: unknown reasoning mode %q", ErrModelProviderInvalid, spec.Mode)
	}
	if !validReasoningParam(spec.Param) {
		return fmt.Errorf("%w: reasoning param is invalid", ErrModelProviderInvalid)
	}
	if len(spec.Levels) == 0 {
		return fmt.Errorf("%w: reasoning levels are required", ErrModelProviderInvalid)
	}
	if spec.DefaultLevel != "" && !containsLevel(spec.Levels, spec.DefaultLevel) {
		return fmt.Errorf("%w: reasoning default_level is not in levels", ErrModelProviderInvalid)
	}
	return nil
}

func validReasoningParam(param string) bool {
	if param == "" || len(param) > 64 {
		return false
	}
	for _, segment := range strings.Split(param, ".") {
		if segment == "" {
			return false
		}
		for _, ch := range segment {
			if !(ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
				return false
			}
		}
	}
	return true
}

func containsLevel(levels []string, level string) bool {
	for _, item := range levels {
		if item == level {
			return true
		}
	}
	return false
}

// mergeModels keeps existing per-model configuration and appends new IDs.
func mergeModels(existing []models.ProviderModel, ids []string) []models.ProviderModel {
	byID := make(map[string]models.ProviderModel, len(existing))
	for _, model := range existing {
		byID[model.ID] = model
	}
	out := make([]models.ProviderModel, 0, len(ids))
	for _, id := range ids {
		if model, ok := byID[id]; ok {
			out = append(out, model)
			continue
		}
		out = append(out, models.ProviderModel{
			ID: id, DisplayName: id, MaxCompletionTokens: 8192, ContextLimit: 131072, Temperature: 1,
			Reasoning: models.DefaultReasoningSpec(id),
		})
	}
	if err := validateModels(out); err != nil {
		return out
	}
	return out
}

func validBaseURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:3] + "****" + key[len(key)-4:]
}

func (s *ModelProviderService) uniqueSlug(ctx context.Context, name, excludeID string) (string, error) {
	base := slugify(name)
	if base == "" {
		base = "provider"
	}
	existing, err := s.repo.List(ctx)
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(existing))
	for _, provider := range existing {
		if provider.ID.Hex() == excludeID {
			continue
		}
		used[provider.Slug] = true
	}
	candidate := base
	for suffix := 2; used[candidate]; suffix++ {
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
	return candidate, nil
}

func slugify(name string) string {
	var b strings.Builder
	lastDash := false
	for _, ch := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9':
			b.WriteRune(ch)
			lastDash = false
		case ch == '-' || ch == '_' || ch == ' ' || ch == '.' || ch == '/':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
