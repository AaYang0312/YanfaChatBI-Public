package constants

import (
	"strings"
	"testing"
)

func TestGameDemoPromptsUseNeutralIdentity(t *testing.T) {
	t.Setenv("YANFA_ASSISTANT_DEMO", "true")
	t.Setenv("ENVIRONMENT", "DEVELOPMENT")
	for _, prompt := range []string{GetSystemPrompt("demo", DatabaseTypePostgreSQL, false), GetRecommendationsPrompt("demo"), GetVisualizationPrompt(DatabaseTypePostgreSQL)} {
		if strings.Contains(prompt, "YanfaChatBI") || !strings.Contains(prompt, "GameShelf") {
			t.Fatal("demo prompt retains original product identity")
		}
		if strings.Count(prompt, LanguageDirective) != 1 {
			t.Fatal("language or SQL instructions changed")
		}
	}
}

func TestGameDemoBrandRequiresDevelopmentOptIn(t *testing.T) {
	t.Setenv("YANFA_ASSISTANT_DEMO", "true")
	t.Setenv("ENVIRONMENT", "PRODUCTION")
	if !strings.Contains(GetSystemPrompt("demo", DatabaseTypePostgreSQL, false), "YanfaChatBI") {
		t.Fatal("production default changed")
	}
}
