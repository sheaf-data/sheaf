package llm

import (
	"strings"
	"sync"

	"github.com/sheaf-data/sheaf/internal/llm/anthropic"
)

// Usage is the token accounting for one or more LLM generation calls.
// All fields are additive across calls. CacheCreation/CacheRead apply to
// backends with prompt caching (Anthropic); local backends leave them 0.
type Usage struct {
	Calls                    int
	InputTokens              int // uncached input tokens
	OutputTokens             int
	CacheCreationInputTokens int // input tokens written to the prompt cache
	CacheReadInputTokens     int // input tokens served from the prompt cache
}

// Add merges another Usage into u.
func (u *Usage) Add(o Usage) {
	u.Calls += o.Calls
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheCreationInputTokens += o.CacheCreationInputTokens
	u.CacheReadInputTokens += o.CacheReadInputTokens
}

// TotalInputTokens is all input tokens regardless of cache disposition.
func (u Usage) TotalInputTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// UsageSink receives per-call token usage. The concrete backends report
// into it through a plain callback (so the leaf packages need not import
// this one); the orchestrator owns a UsageAccumulator and reads the total
// after a scan.
type UsageSink interface {
	AddUsage(Usage)
}

// UsageAccumulator is a thread-safe UsageSink. Backends may report from
// parallel goroutines (e.g. the contract-anchor phase), so Add is locked.
type UsageAccumulator struct {
	mu    sync.Mutex
	total Usage
}

func (a *UsageAccumulator) AddUsage(u Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.total.Add(u)
}

// Total returns the accumulated usage so far.
func (a *UsageAccumulator) Total() Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.total
}

// modelRate is per-million-token USD pricing for a frontier model.
type modelRate struct {
	input      float64 // per 1M uncached input tokens
	output     float64 // per 1M output tokens
	cacheWrite float64 // per 1M cache-creation input tokens
	cacheRead  float64 // per 1M cache-read input tokens
}

// anthropicRates are Anthropic list prices (USD per million tokens) as of
// early 2026. List prices change; treat the resulting figure as an
// estimate, not a bill. Keyed by a lowercase substring of the model id so
// "claude-sonnet-4-6", "claude-3-5-sonnet-…", etc. all match "sonnet".
var anthropicRates = map[string]modelRate{
	"opus":   {input: 15, output: 75, cacheWrite: 18.75, cacheRead: 1.50},
	"sonnet": {input: 3, output: 15, cacheWrite: 3.75, cacheRead: 0.30},
	"haiku":  {input: 1, output: 5, cacheWrite: 1.25, cacheRead: 0.10},
}

// EstimateCostUSD returns an approximate dollar cost for the given usage
// on the resolved backend. The bool reports whether a rate was known:
//
//   - ollama (local): (0, true) — no marginal cost.
//   - anthropic with a recognized model: (estimate, true).
//   - anything else: (0, false) — report tokens only, no dollar figure.
func EstimateCostUSD(backend, model string, u Usage) (float64, bool) {
	switch resolveBackend(backend) {
	case BackendOllama:
		return 0, true // local compute; no per-token cost
	case BackendAnthropic:
		if model == "" {
			model = anthropic.DefaultModel
		}
		lm := strings.ToLower(model)
		for key, r := range anthropicRates {
			if strings.Contains(lm, key) {
				const perM = 1_000_000.0
				usd := float64(u.InputTokens)/perM*r.input +
					float64(u.OutputTokens)/perM*r.output +
					float64(u.CacheCreationInputTokens)/perM*r.cacheWrite +
					float64(u.CacheReadInputTokens)/perM*r.cacheRead
				return usd, true
			}
		}
		return 0, false
	default:
		return 0, false
	}
}
