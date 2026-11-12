package models

import (
	"strings"
	"time"
)

// Reasoning modes describe how a model's thinking intensity is sent on the
// wire. An empty or "none" mode means the endpoint receives no reasoning
// parameter at all.
const (
	ReasoningModeNone   = "none"
	ReasoningModeEffort = "effort" // {"<param>": "<level>"}
	ReasoningModeToggle = "toggle" // {"<param.path>": on/off value}
	ReasoningModeCustom = "custom" // {"<param.path>": "<level>"}
)

// ReasoningSpec declares whether and how a model accepts thinking intensity.
// It is stored per model and can be omitted entirely.
type ReasoningSpec struct {
	Mode         string   `bson:"mode" json:"mode"`
	Param        string   `bson:"param,omitempty" json:"param,omitempty"`
	Levels       []string `bson:"levels,omitempty" json:"levels,omitempty"`
	DefaultLevel string   `bson:"default_level,omitempty" json:"default_level,omitempty"`
	OnValue      any      `bson:"on_value,omitempty" json:"on_value,omitempty"`
	OffValue     any      `bson:"off_value,omitempty" json:"off_value,omitempty"`
}

// Supported reports whether the spec can inject a parameter.
func (r *ReasoningSpec) Supported() bool {
	if r == nil {
		return false
	}
	return r.Mode != "" && r.Mode != ReasoningModeNone && r.Param != "" && len(r.Levels) > 0
}

// EffectiveLevel resolves a requested level against the declared levels.
// An empty request falls back to DefaultLevel; an unknown level also falls
// back, so a client cannot inject arbitrary values.
func (r *ReasoningSpec) EffectiveLevel(requested string) string {
	if r == nil || len(r.Levels) == 0 {
		return ""
	}
	for _, level := range r.Levels {
		if level == requested {
			return requested
		}
	}
	for _, level := range r.Levels {
		if level == r.DefaultLevel {
			return level
		}
	}
	return r.Levels[0]
}

// ProviderModel is one model exposed by a configured provider.
type ProviderModel struct {
	ID                  string         `bson:"id" json:"id"`
	DisplayName         string         `bson:"display_name" json:"display_name"`
	MaxCompletionTokens int            `bson:"max_completion_tokens" json:"max_completion_tokens"`
	ContextLimit        int            `bson:"context_limit" json:"context_limit"`
	Temperature         float64        `bson:"temperature" json:"temperature"`
	Reasoning           *ReasoningSpec `bson:"reasoning,omitempty" json:"reasoning,omitempty"`
}

// CompositeID is the stable model identifier used by chats and the models API.
func (m ProviderModel) CompositeID(slug string) string {
	return slug + "/" + m.ID
}

// SplitModelID splits "slug/model" identifiers. A bare model name returns
// slug "" and ok=false so callers can fall back to a legacy lookup.
func SplitModelID(id string) (slug, modelID string, ok bool) {
	slash := strings.Index(id, "/")
	if slash <= 0 || slash == len(id)-1 {
		return "", id, false
	}
	return id[:slash], id[slash+1:], true
}

// ModelProvider is an OpenAI-compatible endpoint the deployment can use.
type ModelProvider struct {
	Base              `bson:",inline"`
	Name              string          `bson:"name" json:"name"`
	Slug              string          `bson:"slug" json:"slug"`
	BaseURL           string          `bson:"base_url" json:"base_url"`
	APIKeyEncrypted   string          `bson:"api_key_encrypted,omitempty" json:"-"`
	Models            []ProviderModel `bson:"models" json:"models"`
	Enabled           bool            `bson:"enabled" json:"enabled"`
	PresetID          string          `bson:"preset_id,omitempty" json:"preset_id,omitempty"`
	ModelsRefreshedAt *time.Time      `bson:"models_refreshed_at,omitempty" json:"models_refreshed_at,omitempty"`
}

// ModelByID returns the provider's model with the given raw ID.
func (p *ModelProvider) ModelByID(id string) *ProviderModel {
	if p == nil {
		return nil
	}
	for i := range p.Models {
		if p.Models[i].ID == id {
			return &p.Models[i]
		}
	}
	return nil
}

// DefaultModel returns the model marked as first entry (the catalogue's
// default for a provider).
func (p *ModelProvider) DefaultModel() *ProviderModel {
	if p == nil || len(p.Models) == 0 {
		return nil
	}
	return &p.Models[0]
}

// DefaultReasoningSpec infers a sane thinking-intensity spec from a model ID.
// It covers the common OpenAI-compatible families; callers may override every
// field from the settings UI. Unknown models return nil (no parameter sent).
func DefaultReasoningSpec(modelID string) *ReasoningSpec {
	id := strings.ToLower(strings.TrimSpace(modelID))
	effortLevels := []string{"low", "medium", "high", "xhigh", "max"}
	switch {
	case strings.Contains(id, "gpt-6"), strings.Contains(id, "gpt-5"),
		strings.HasPrefix(id, "o1"), strings.HasPrefix(id, "o3"), strings.HasPrefix(id, "o4"):
		return &ReasoningSpec{Mode: ReasoningModeEffort, Param: "reasoning_effort",
			Levels: effortLevels, DefaultLevel: "medium"}
	case strings.Contains(id, "kimi-k3"):
		return &ReasoningSpec{Mode: ReasoningModeEffort, Param: "reasoning_effort",
			Levels: []string{"low", "high", "max"}, DefaultLevel: "max"}
	case strings.Contains(id, "minimax-m3"):
		return &ReasoningSpec{Mode: ReasoningModeCustom, Param: "reasoning.effort",
			Levels: effortLevels, DefaultLevel: "max"}
	case strings.Contains(id, "glm-4.7"), strings.Contains(id, "glm-5"):
		return &ReasoningSpec{Mode: ReasoningModeToggle, Param: "thinking.type",
			Levels: []string{"on", "off"}, DefaultLevel: "on", OnValue: "enabled", OffValue: "disabled"}
	case strings.Contains(id, "qwen3"):
		return &ReasoningSpec{Mode: ReasoningModeToggle, Param: "enable_thinking",
			Levels: []string{"on", "off"}, DefaultLevel: "on", OnValue: true, OffValue: false}
	case strings.Contains(id, "deepseek-v4"), strings.Contains(id, "deepseek-reasoner"):
		return &ReasoningSpec{Mode: ReasoningModeToggle, Param: "thinking.type",
			Levels: []string{"on", "off"}, DefaultLevel: "on", OnValue: true, OffValue: false}
	}
	return nil
}
