package constants

import (
	"strings"
	"testing"
)

func TestPromptLanguage(t *testing.T) {
	builders := []struct {
		name   string
		prompt string
	}{
		{name: "GetSystemPrompt", prompt: GetSystemPrompt(OpenAI, DatabaseTypePostgreSQL, false)},
		{name: "GetRecommendationsPrompt", prompt: GetRecommendationsPrompt(OpenAI)},
		{name: "GetVisualizationPrompt", prompt: GetVisualizationPrompt(DatabaseTypePostgreSQL)},
	}
	for _, builder := range builders {
		t.Run(builder.name, func(t *testing.T) {
			if !strings.Contains(builder.prompt, "Simplified Chinese") {
				t.Fatalf("%s prompt does not default to Simplified Chinese", builder.name)
			}
			if !strings.Contains(builder.prompt, "Preserve SQL") {
				t.Fatalf("%s prompt does not preserve SQL", builder.name)
			}
			if count := strings.Count(builder.prompt, LanguageDirective); count != 1 {
				t.Fatalf("%s appended LanguageDirective %d times, want 1", builder.name, count)
			}
			if !strings.HasSuffix(builder.prompt, LanguageDirective) {
				t.Fatalf("%s did not append LanguageDirective once at the end", builder.name)
			}
		})
	}

	t.Run("visualization prompts keep SQL rules and JSON keys", func(t *testing.T) {
		for _, dbType := range []string{
			DatabaseTypePostgreSQL,
			DatabaseTypeMySQL,
			DatabaseTypeYugabyteDB,
			DatabaseTypeClickhouse,
			DatabaseTypeMongoDB,
			DatabaseTypeTimescaleDB,
			DatabaseTypeStarRocks,
			DatabaseTypeSpreadsheet,
			"unknown",
		} {
			prompt := GetVisualizationPrompt(dbType)
			if strings.Count(prompt, LanguageDirective) != 1 {
				t.Fatalf("%s visualization prompt appended LanguageDirective more than once", dbType)
			}
			if !strings.Contains(prompt, "SQL") {
				t.Fatalf("%s visualization prompt dropped SQL guidance", dbType)
			}
			for _, key := range []string{`"chart_type"`, `"title"`, `"data_key"`} {
				if !strings.Contains(prompt, key) {
					t.Fatalf("%s visualization prompt changed chart field %s", dbType, key)
				}
			}
		}
	})

	t.Run("recommendation JSON keys stay unchanged", func(t *testing.T) {
		prompt := GetRecommendationsPrompt(Claude)
		for _, key := range []string{`"recommendations"`, `"text"`} {
			if !strings.Contains(prompt, key) {
				t.Fatalf("recommendations prompt changed JSON key %s", key)
			}
		}
	})
}
