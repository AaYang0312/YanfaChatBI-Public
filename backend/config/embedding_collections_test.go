package config

import (
	"yanfachatbi/internal/constants"
	"strings"
	"testing"
)

func TestLoadEnvEmbeddingCollectionPair(t *testing.T) {
	originalEnv := Env
	originalModels := append([]constants.LLMModel(nil), constants.SupportedLLMModels...)
	t.Cleanup(func() { Env = originalEnv; constants.SupportedLLMModels = originalModels })
	// This test runs in a network-disabled container with no host environment or .env.
	t.Setenv("IS_DOCKER", "true")
	t.Setenv("YANFA_ADMIN_USERNAME", "test-admin")
	t.Setenv("YANFA_ADMIN_PASSWORD", "test-password")
	t.Setenv("SPREADSHEET_DATA_ENCRYPTION_KEY", strings.Repeat("0", 32))
	t.Setenv("SPREADSHEET_POSTGRES_SSL_MODE", "disable")
	for _, key := range []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "CLAUDE_API_KEY", "OLLAMA_BASE_URL", "DEFAULT_LLM_MODEL", "EMBEDDING_PROVIDER", "EMBEDDING_MODEL", "EMBEDDING_API_KEY"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		name, schema, messages string
		invalid                bool
	}{
		{name: "both absent"},
		{name: "custom", schema: "schema_v2", messages: "messages_v2"},
		{name: "explicit legacy", schema: "neobase_schema", messages: "neobase_messages"},
		{name: "partial", schema: "schema_v2", invalid: true},
		{name: "duplicate", schema: "same", messages: "same", invalid: true},
		{name: "crossed", schema: "neobase_messages", messages: "neobase_schema", invalid: true},
		{name: "unsafe", schema: "schema/path", messages: "messages_v2", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EMBEDDING_SCHEMA_COLLECTION", tc.schema)
			t.Setenv("EMBEDDING_MESSAGE_COLLECTION", tc.messages)
			err := LoadEnv()
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid pair accepted by LoadEnv")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if Env.EmbeddingSchemaCollection != tc.schema || Env.EmbeddingMessageCollection != tc.messages {
				t.Fatalf("pair not loaded: %q / %q", Env.EmbeddingSchemaCollection, Env.EmbeddingMessageCollection)
			}
		})
	}
}
