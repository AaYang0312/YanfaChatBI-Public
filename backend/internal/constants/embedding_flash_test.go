package constants

import "testing"

func TestQwenFlashEmbeddingConfig(t *testing.T) {
	const modelID = "qwen3.7-text-embedding-flash"
	for _, tc := range []struct {
		name, provider, model, openAIKey string
		wantProvider, wantModel                     string
		wantError                                   bool
	}{
		{"explicit compatible provider", "openai", modelID, "synthetic-test-only", "openai", modelID, false},
		{"auto detected compatible provider", "", modelID, "synthetic-test-only", "openai", modelID, false},
		{"missing key", "openai", modelID, "", "", "", true},
		{"wrong provider", "gemini", modelID, "", "", "", true},
		{"unknown similar name", "openai", modelID + "-unknown", "synthetic-test-only", "", "", true},
		{"existing default unchanged", "openai", "", "synthetic-test-only", "openai", "text-embedding-3-small", false},
		{"existing non flash model unchanged", "openai", "qwen3.7-text-embedding", "synthetic-test-only", "openai", "qwen3.7-text-embedding", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, model, err := ValidateEmbeddingConfig(tc.provider, tc.model, tc.openAIKey)
			if tc.wantError {
				if err == nil {
					t.Fatal("invalid embedding configuration was accepted")
				}
				return
			}
			if err != nil || provider != tc.wantProvider || model != tc.wantModel {
				t.Fatalf("configuration resolved to %q/%q, error %v; want %q/%q", provider, model, err, tc.wantProvider, tc.wantModel)
			}
		})
	}
}
