package constants

import "testing"

func TestOpenAIEndpointCatalogue(t *testing.T) {
	original := SupportedLLMModels
	t.Cleanup(func() { SupportedLLMModels = original })

	cases := []struct {
		name        string
		baseURL     string
		wantDefault string
	}{
		{name: "official_openai", wantDefault: "gpt-4o"},
		{name: "bigmodel", baseURL: "https://open.bigmodel.cn/api/paas/v4", wantDefault: "glm-5.3-flash"},
		{name: "bigmodel_trailing_slash", baseURL: "https://open.bigmodel.cn/api/paas/v4/", wantDefault: "glm-5.3-flash"},
		{name: "bigmodel_multiple_trailing_slashes", baseURL: "https://open.bigmodel.cn/api/paas/v4//", wantDefault: "glm-5.3-flash"},
		{name: "generic_proxy", baseURL: "https://proxy.example/v1", wantDefault: "remote-magpie/cursor/grok-4.6"},
		{name: "similar_host", baseURL: "https://open.bigmodel.cn.example/api/paas/v4", wantDefault: "remote-magpie/cursor/grok-4.6"},
		{name: "different_path", baseURL: "https://open.bigmodel.cn/api/paas/v40", wantDefault: "remote-magpie/cursor/grok-4.6"},
		{name: "different_scheme", baseURL: "http://open.bigmodel.cn/api/paas/v4", wantDefault: "remote-magpie/cursor/grok-4.6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []struct {
				name  string
				value string
			}{
				{name: "with_key", value: "synthetic-test-only"},
				{name: "without_key"},
			} {
				t.Run(key.name, func(t *testing.T) {
					SupportedLLMModels = append([]LLMModel(nil), original...)
					t.Cleanup(func() { SupportedLLMModels = original })
					DisableUnavailableProviders(key.value)
					ApplyOpenAIProxyOverride(tc.baseURL)

					visible := GetAvailableModelsByAPIKeys(key.value)
					providerDefault := GetDefaultModelForProvider(OpenAI)
					fallback := GetFirstAvailableModel()
					if key.value == "" {
						if len(visible) != 0 || providerDefault != nil || fallback != nil {
							t.Fatalf("missing key exposed models or a default: models=%v, provider=%v, fallback=%v", visible, providerDefault, fallback)
						}
						if enabled := GetEnabledLLMModels(); len(enabled) != 0 {
							t.Fatalf("endpoint restored key-disabled models: %v", enabled)
						}
						return
					}

					if providerDefault == nil || providerDefault.ID != tc.wantDefault || !providerDefault.IsEnabled {
						t.Fatalf("provider default = %v, want enabled %s", providerDefault, tc.wantDefault)
					}
					if fallback == nil || fallback.ID != tc.wantDefault {
						t.Fatalf("fallback = %v, want %s", fallback, tc.wantDefault)
					}
					var selectorDefault string
					for _, model := range visible {
						if model.Provider != OpenAI {
							t.Fatalf("unconfigured provider appeared in selector: %s", model.ID)
						}
						if tc.wantDefault == "gpt-4o" {
							if model.ID == "glm-5.3-flash" || OpenAIProxyModelIDs[model.ID] {
								t.Fatalf("official OpenAI endpoint exposed incompatible model %s", model.ID)
							}
						} else if model.ID != tc.wantDefault {
							t.Fatalf("endpoint exposed incompatible model %s", model.ID)
						}
						if model.Default != nil && *model.Default {
							if selectorDefault != "" {
								t.Fatalf("selector has multiple enabled defaults: %s and %s", selectorDefault, model.ID)
							}
							selectorDefault = model.ID
						}
					}
					if selectorDefault != providerDefault.ID {
						t.Fatalf("selector default = %q, provider default = %q", selectorDefault, providerDefault.ID)
					}
					if tc.wantDefault == "gpt-4o" {
						for _, model := range original {
							if model.Provider == OpenAI && model.IsEnabled && model.ID != "glm-5.3-flash" && !OpenAIProxyModelIDs[model.ID] && !IsValidModel(model.ID) {
								t.Errorf("official OpenAI endpoint lost enabled stock model %s", model.ID)
							}
						}
					}
				})
			}
		})
	}
}

func TestOpenAIEndpointPreservesDisabledModels(t *testing.T) {
	original := SupportedLLMModels
	t.Cleanup(func() { SupportedLLMModels = original })

	cases := []struct {
		name    string
		baseURL string
		modelID string
	}{
		{name: "official_openai", modelID: "gpt-4o"},
		{name: "bigmodel", baseURL: "https://open.bigmodel.cn/api/paas/v4", modelID: "glm-5.3-flash"},
		{name: "generic_proxy", baseURL: "https://proxy.example/v1", modelID: "remote-magpie/cursor/grok-4.6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SupportedLLMModels = append([]LLMModel(nil), original...)
			t.Cleanup(func() { SupportedLLMModels = original })
			model := GetLLMModel(tc.modelID)
			if model == nil {
				t.Fatalf("model %s is missing from the catalogue", tc.modelID)
			}
			model.IsEnabled = false
			DisableUnavailableProviders("synthetic-test-only")
			ApplyOpenAIProxyOverride(tc.baseURL)

			if IsValidModel(tc.modelID) {
				t.Fatalf("endpoint restored disabled model %s", tc.modelID)
			}
			for _, visible := range GetAvailableModelsByAPIKeys("synthetic-test-only") {
				if visible.ID == tc.modelID {
					t.Fatalf("disabled model %s appeared in selector", tc.modelID)
				}
			}
		})
	}
}
