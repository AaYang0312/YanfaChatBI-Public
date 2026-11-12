package llm

import (
	"testing"

	"yanfachatbi/internal/models"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcde", 2},
		{"你好", 2},
		{"你好abcde", 4},
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Fatalf("EstimateTokens(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestPromptBudget(t *testing.T) {
	model := &models.ProviderModel{ContextLimit: 100000, MaxCompletionTokens: 8000}
	budget, reserve := PromptBudget(model)
	if reserve != 8000+10000 {
		t.Fatalf("reserve = %d", reserve)
	}
	if budget != 100000-reserve {
		t.Fatalf("budget = %d", budget)
	}

	// Missing limits fall back to a 128k window and a 4k completion reserve.
	budget, reserve = PromptBudget(nil)
	if reserve != 4096+13107 {
		t.Fatalf("fallback reserve = %d", reserve)
	}
	if budget < 1024 {
		t.Fatalf("fallback budget = %d", budget)
	}

	// A tiny window still yields the floor.
	budget, _ = PromptBudget(&models.ProviderModel{ContextLimit: 2048, MaxCompletionTokens: 4096})
	if budget != 1024 {
		t.Fatalf("floor budget = %d", budget)
	}
}

func TestUsageCalibrator(t *testing.T) {
	var cal UsageCalibrator
	if got := cal.Adjust(1000); got != 1000 {
		t.Fatalf("uncalibrated adjust = %d", got)
	}
	cal.Observe(1000, 2000)
	if got := cal.Adjust(1000); got != 2000 {
		t.Fatalf("adjusted = %d", got)
	}
	// Outliers are clamped to 3x.
	cal.Observe(1000, 100000)
	if cal.Ratio() > 3.0001 {
		t.Fatalf("ratio not clamped: %f", cal.Ratio())
	}
	// Ignored samples do not change anything.
	before := cal.Ratio()
	cal.Observe(0, 10)
	cal.Observe(10, 0)
	if cal.Ratio() != before {
		t.Fatalf("invalid sample changed ratio")
	}
}

func TestExceedsBudget(t *testing.T) {
	if ExceedsBudget(700, 1000, 0.75) {
		t.Fatal("700/1000 must not exceed 75%")
	}
	if !ExceedsBudget(760, 1000, 0.75) {
		t.Fatal("760/1000 must exceed 75%")
	}
	if ExceedsBudget(100, 0, 0.75) {
		t.Fatal("zero budget must not trigger")
	}
	// Invalid thresholds fall back to 0.75.
	if !ExceedsBudget(800, 1000, 2) {
		t.Fatal("invalid threshold must fall back to 0.75")
	}
}

func TestTrimToTokenBudget(t *testing.T) {
	text := "abcdefghijklmnopqrstuvwxyz"
	trimmed, cut := TrimToTokenBudget(text, 2)
	if !cut {
		t.Fatal("expected truncation")
	}
	if EstimateTokens(trimmed) > 2 {
		t.Fatalf("trimmed over budget: %q (%d)", trimmed, EstimateTokens(trimmed))
	}

	short := "hi"
	if out, cut := TrimToTokenBudget(short, 10); cut || out != short {
		t.Fatalf("short text changed: %q %v", out, cut)
	}

	chinese := "这是一个很长的中文句子用于测试截断行为"
	trimmed, cut = TrimToTokenBudget(chinese, 3)
	if !cut || EstimateTokens(trimmed) > 3 {
		t.Fatalf("chinese trim = %q (%d)", trimmed, EstimateTokens(trimmed))
	}
}
