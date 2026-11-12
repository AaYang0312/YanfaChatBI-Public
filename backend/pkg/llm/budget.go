package llm

import (
	"math"
	"sync"
	"unicode/utf8"

	"yanfachatbi/internal/models"
)

// EstimateTokens returns a deliberately conservative token estimate:
// ASCII characters count as 1/4 token each, every other rune counts as one
// token. It is used for budget decisions, never for billing.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii := 0
	nonASCII := 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
	}
	return nonASCII + (ascii+3)/4
}

// PromptBudget derives the input budget for a provider model.
//
// reserve = max(MaxCompletionTokens, 4096) + 10% of the context window. The
// remaining context is available for the prompt; the result is floored at
// 1024 tokens so a misconfigured model still yields a usable budget.
func PromptBudget(model *models.ProviderModel) (budget int, reserve int) {
	contextLimit := 0
	maxCompletion := 0
	if model != nil {
		contextLimit = model.ContextLimit
		maxCompletion = model.MaxCompletionTokens
	}
	if contextLimit <= 0 {
		contextLimit = 131072
	}
	if maxCompletion < 4096 {
		maxCompletion = 4096
	}
	reserve = maxCompletion + contextLimit/10
	budget = contextLimit - reserve
	if budget < 1024 {
		budget = 1024
	}
	return budget, reserve
}

// UsageCalibrator corrects estimate drift using provider-reported prompt
// tokens. The ratio is an exponentially weighted average, clamped so a single
// outlier (or a provider that reports no usage) cannot wreck the budget.
type UsageCalibrator struct {
	mu      sync.Mutex
	ratio   float64
	samples int
}

// Observe records one estimated/actual pair.
func (c *UsageCalibrator) Observe(estimated, actual int) {
	if estimated <= 0 || actual <= 0 {
		return
	}
	observed := float64(actual) / float64(estimated)
	observed = math.Max(0.5, math.Min(3, observed))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.samples == 0 {
		c.ratio = observed
	} else {
		c.ratio = c.ratio*0.5 + observed*0.5
	}
	c.samples++
}

// Adjust scales an estimate by the calibration ratio (identity before the
// first observation).
func (c *UsageCalibrator) Adjust(estimated int) int {
	if estimated <= 0 {
		return 0
	}
	c.mu.Lock()
	ratio := c.ratio
	c.mu.Unlock()
	if ratio <= 0 {
		return estimated
	}
	return int(math.Ceil(float64(estimated) * ratio))
}

// Ratio exposes the current calibration ratio for logging (0 = uncalibrated).
func (c *UsageCalibrator) Ratio() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ratio
}

// ExceedsBudget reports whether the estimated prompt tokens are above the
// configured fraction of the budget.
func ExceedsBudget(estimatedTokens, budget int, threshold float64) bool {
	if budget <= 0 {
		return false
	}
	if threshold <= 0 || threshold > 1 {
		threshold = 0.75
	}
	return float64(estimatedTokens) > float64(budget)*threshold
}

// TrimToTokenBudget trims text to at most maxTokens using EstimateTokens,
// keeping complete UTF-8 runes. It returns the text and whether it was cut.
// The trailing ellipsis is included in the budget.
func TrimToTokenBudget(text string, maxTokens int) (string, bool) {
	if maxTokens <= 0 || EstimateTokens(text) <= maxTokens {
		return text, false
	}
	// Work in quarter-token units so ASCII can be appended incrementally.
	limit := maxTokens*4 - 4 // reserve one token for the ellipsis
	if limit < 4 {
		return "…", true
	}
	used := 0
	cut := 0
	for i, r := range text {
		cost := 4
		if r < utf8.RuneSelf {
			cost = 1
		}
		if used+cost > limit {
			cut = i
			break
		}
		used += cost
		cut = i + utf8.RuneLen(r)
	}
	if cut == 0 {
		return "…", true
	}
	return text[:cut] + "…", true
}
