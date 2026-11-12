package models

import "testing"

func TestDefaultReasoningSpec(t *testing.T) {
	cases := []struct {
		model   string
		mode    string
		param   string
		levels  []string
		hasSpec bool
	}{
		{"gpt-6-astra", ReasoningModeEffort, "reasoning_effort", []string{"low", "medium", "high", "xhigh", "max"}, true},
		{"gpt-5.6-sol", ReasoningModeEffort, "reasoning_effort", []string{"low", "medium", "high", "xhigh", "max"}, true},
		{"o3-mini", ReasoningModeEffort, "reasoning_effort", []string{"low", "medium", "high", "xhigh", "max"}, true},
		{"kimi-k3", ReasoningModeEffort, "reasoning_effort", []string{"low", "high", "max"}, true},
		{"MiniMax-M3.1-Flash", ReasoningModeCustom, "reasoning.effort", []string{"low", "medium", "high", "xhigh", "max"}, true},
		{"glm-5.3", ReasoningModeToggle, "thinking.type", []string{"on", "off"}, true},
		{"qwen3.8-max", ReasoningModeToggle, "enable_thinking", []string{"on", "off"}, true},
		{"deepseek-v4-flash", ReasoningModeToggle, "thinking.type", []string{"on", "off"}, true},
		{"llama-3-70b", "", "", nil, false},
	}
	for _, tc := range cases {
		spec := DefaultReasoningSpec(tc.model)
		if !tc.hasSpec {
			if spec != nil {
				t.Fatalf("%s: expected no spec, got %+v", tc.model, spec)
			}
			continue
		}
		if spec == nil {
			t.Fatalf("%s: expected a spec", tc.model)
		}
		if spec.Mode != tc.mode || spec.Param != tc.param {
			t.Fatalf("%s: mode/param = %s/%s", tc.model, spec.Mode, spec.Param)
		}
		if len(spec.Levels) != len(tc.levels) {
			t.Fatalf("%s: levels = %v", tc.model, spec.Levels)
		}
		for i := range tc.levels {
			if spec.Levels[i] != tc.levels[i] {
				t.Fatalf("%s: levels = %v", tc.model, spec.Levels)
			}
		}
	}
}
