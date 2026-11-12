package config

import (
	"strings"
	"testing"
	"yanfachatbi/internal/constants"
)

func TestValidateConfigDefaultLLMAvailability(t *testing.T) {
	originalEnv := Env
	originalModels := constants.SupportedLLMModels
	t.Cleanup(func() {
		Env = originalEnv
		constants.SupportedLLMModels = originalModels
	})

	cases := []struct {
		name                string
		baseURL             string
		modelID             string
		withoutKey          bool
		wantProviderDefault string
		wantError           string
	}{
		{name: "official_implicit_default", wantProviderDefault: "gpt-4o"},
		{name: "official_explicit_default", modelID: "gpt-4o", wantProviderDefault: "gpt-4o"},
		{name: "bigmodel_default", baseURL: "https://open.bigmodel.cn/api/paas/v4", modelID: "glm-5.3-flash", wantProviderDefault: "glm-5.3-flash"},
		{name: "bigmodel_trailing_slash", baseURL: "https://open.bigmodel.cn/api/paas/v4/", modelID: "glm-5.3-flash", wantProviderDefault: "glm-5.3-flash"},
		{name: "proxy_default", baseURL: "https://proxy.example/v1", modelID: "remote-magpie/cursor/grok-4.6", wantProviderDefault: "remote-magpie/cursor/grok-4.6"},
		{name: "official_rejects_glm", modelID: "glm-5.3-flash", wantError: "is disabled"},
		{name: "official_rejects_proxy_model", modelID: "remote-magpie/cursor/grok-4.6", wantError: "is disabled"},
		{name: "bigmodel_rejects_stock_model", baseURL: "https://open.bigmodel.cn/api/paas/v4", modelID: "gpt-4o", wantError: "is disabled"},
		{name: "bigmodel_rejects_proxy_model", baseURL: "https://open.bigmodel.cn/api/paas/v4", modelID: "remote-magpie/cursor/grok-4.6", wantError: "is disabled"},
		{name: "proxy_rejects_glm", baseURL: "https://proxy.example/v1", modelID: "glm-5.3-flash", wantError: "is disabled"},
		{name: "proxy_rejects_stock_model", baseURL: "https://proxy.example/v1", modelID: "gpt-4o", wantError: "is disabled"},
		{name: "similar_host_rejects_glm", baseURL: "https://open.bigmodel.cn.example/api/paas/v4", modelID: "glm-5.3-flash", wantError: "is disabled"},
		{name: "different_path_rejects_glm", baseURL: "https://open.bigmodel.cn/api/paas/v40", modelID: "glm-5.3-flash", wantError: "is disabled"},
		{name: "unknown_model", modelID: "unknown-test-model", wantError: "model not found"},
		{name: "unknown_model_without_key", modelID: "unknown-test-model", withoutKey: true, wantError: "model not found"},
		{name: "official_without_key_rejects_default", modelID: "gpt-4o", withoutKey: true, wantError: "is disabled"},
		{name: "bigmodel_without_key_rejects_default", baseURL: "https://open.bigmodel.cn/api/paas/v4", modelID: "glm-5.3-flash", withoutKey: true, wantError: "is disabled"},
		{name: "proxy_without_key_rejects_default", baseURL: "https://proxy.example/v1", modelID: "remote-magpie/cursor/grok-4.6", withoutKey: true, wantError: "is disabled"},
		{name: "official_without_key_or_default", withoutKey: true},
		{name: "bigmodel_without_key_or_default", baseURL: "https://open.bigmodel.cn/api/paas/v4", withoutKey: true},
		{name: "proxy_without_key_or_default", baseURL: "https://proxy.example/v1", withoutKey: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			constants.SupportedLLMModels = append([]constants.LLMModel(nil), originalModels...)
			Env = Environment{
				MongoURI:                     "mongodb://example.invalid/config-test",
				JWTExpirationMilliseconds:    1,
				SpreadsheetDataEncryptionKey: strings.Repeat("0", 32),
				SpreadsheetPostgresSSLMode:   "disable",
				DefaultLLMModel:              tc.modelID,
				OpenAIBaseURL:                tc.baseURL,
			}
			if !tc.withoutKey {
				Env.OpenAIAPIKey = "synthetic-test-only"
			}
			t.Cleanup(func() {
				Env = originalEnv
				constants.SupportedLLMModels = originalModels
			})

			err := validateConfig()
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), "DEFAULT_LLM_MODEL") || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("validateConfig() = %v, want DEFAULT_LLM_MODEL error containing %q", err, tc.wantError)
				}
				if model := constants.GetLLMModel(tc.modelID); model != nil && model.IsEnabled {
					t.Fatalf("rejected default %s was not disabled by availability filtering", tc.modelID)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateConfig() = %v", err)
			}

			providerDefault := constants.GetDefaultModelForProvider(constants.OpenAI)
			visible := constants.GetAvailableModelsByAPIKeys(Env.OpenAIAPIKey)
			if tc.wantProviderDefault == "" {
				if providerDefault != nil || len(visible) != 0 {
					t.Fatalf("configuration without a key exposed models or default: models=%v, default=%v", visible, providerDefault)
				}
				return
			}
			if providerDefault == nil || providerDefault.ID != tc.wantProviderDefault || !providerDefault.IsEnabled {
				t.Fatalf("provider default = %v, want enabled %s", providerDefault, tc.wantProviderDefault)
			}
			selectedID := Env.DefaultLLMModel
			if selectedID == "" {
				selectedID = providerDefault.ID
			}
			if tc.modelID != "" && selectedID != tc.modelID {
				t.Fatalf("configured default changed from %s to %s", tc.modelID, selectedID)
			}
			for _, model := range visible {
				if model.ID == selectedID && model.IsEnabled {
					return
				}
			}
			t.Fatalf("selected default %s is missing from the enabled selector catalogue", selectedID)
		})
	}
}

func TestGetRenamedEnvPrefersNewNameAndFallsBack(t *testing.T) {
	t.Setenv("YANFA_TEST_RENAMED", "new")
	t.Setenv("NEOBASE_TEST_RENAMED", "legacy")
	if got := getRenamedEnv("YANFA_TEST_RENAMED", "NEOBASE_TEST_RENAMED", "default"); got != "new" {
		t.Fatalf("new name should win, got %q", got)
	}

	t.Setenv("YANFA_TEST_RENAMED", "")
	if got := getRenamedEnv("YANFA_TEST_RENAMED", "NEOBASE_TEST_RENAMED", "default"); got != "legacy" {
		t.Fatalf("legacy fallback failed, got %q", got)
	}

	t.Setenv("NEOBASE_TEST_RENAMED", "")
	if got := getRenamedEnv("YANFA_TEST_RENAMED", "NEOBASE_TEST_RENAMED", "default"); got != "default" {
		t.Fatalf("default fallback failed, got %q", got)
	}

	if got := getRenamedEnv("YANFA_TEST_RENAMED", "", "default"); got != "default" {
		t.Fatalf("empty legacy key must not fall back, got %q", got)
	}
}
