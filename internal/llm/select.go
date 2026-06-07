package llm

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/sheaf-data/sheaf/internal/llm/anthropic"
	"github.com/sheaf-data/sheaf/internal/llm/ollama"
)

// Backend names the selectable LLM generation backends. Empty / "auto"
// picks the frontier API when ANTHROPIC_API_KEY is present, else the
// local ollama daemon — so a bringup run on a machine with a key uses
// the higher-quality model automatically, while staying offline-capable.
const (
	BackendAuto      = "auto"
	BackendOllama    = "ollama"
	BackendAnthropic = "anthropic"
	// BackendNone disables the LLM tier entirely: no model is contacted,
	// and any stage that would invoke one (llmextract contract
	// extraction, the attribution pass) is omitted rather than wired. The
	// deterministic contract↔test↔doc join is unaffected — "none" yields
	// the mechanical floor. Aliases "noop"/"off" resolve here too.
	BackendNone = "none"
)

// defaultOllamaModel matches the model the llmextract prototype was
// evaluated on. The package-level ollama default is the smaller 7b; for
// sheaf's extraction/attribution we want the 14b floor.
const defaultOllamaModel = "qwen2.5:14b-instruct"

// NewClient returns a generation Client for the requested backend.
//
//   - backend "" or "auto": anthropic if ANTHROPIC_API_KEY is set, else ollama.
//   - "anthropic": the frontier Messages API (errors if no key).
//   - "ollama":    the local daemon at 127.0.0.1:11434.
//
// model overrides the backend default when non-empty. timeout bounds a
// single request (covers the local model's cold start; for the frontier
// API it's a normal request timeout).
// sink, when non-nil, receives the token usage of every call this client
// makes (for cost accounting); pass nil to disable.
func NewClient(backend, model string, timeout time.Duration, sink UsageSink) (Client, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	var onUsage func(in, out, cc, cr int)
	if sink != nil {
		onUsage = func(in, out, cc, cr int) {
			sink.AddUsage(Usage{
				Calls:                    1,
				InputTokens:              in,
				OutputTokens:             out,
				CacheCreationInputTokens: cc,
				CacheReadInputTokens:     cr,
			})
		}
	}
	switch resolveBackend(backend) {
	case BackendNone:
		// "none" is not a client — it's the absence of one. Callers that
		// reach here have an LLM stage enabled while selecting no backend,
		// which is contradictory; fail loudly rather than silently falling
		// back to a live model. The `--auto` path avoids this by omitting
		// the stage at config-synthesis time (see autoconfig + llm.Disabled).
		return nil, fmt.Errorf("llm: backend %q builds no client — omit the LLM stage (or set enabled:false) instead of selecting %q", BackendNone, BackendNone)
	case BackendAnthropic:
		return anthropic.New(anthropic.Config{
			Model:      model,
			HTTPClient: &http.Client{Timeout: timeout},
			OnUsage:    onUsage,
		})
	default: // ollama
		if model == "" {
			model = defaultOllamaModel
		}
		return ollama.NewClient(ollama.Config{
			Model:      model,
			HTTPClient: &http.Client{Timeout: timeout},
			OnUsage:    onUsage,
		}), nil
	}
}

// resolveBackend maps "auto"/"" to a concrete backend using the
// environment. An explicit backend is honored as-is.
func resolveBackend(backend string) string {
	switch backend {
	case BackendOllama:
		return BackendOllama
	case BackendAnthropic:
		return BackendAnthropic
	case BackendNone, "noop", "off":
		return BackendNone
	default: // "" or "auto"
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			return BackendAnthropic
		}
		return BackendOllama
	}
}

// ResolveBackendName reports which concrete backend an "auto"/explicit
// value resolves to, for logging in the CLI without constructing a client.
func ResolveBackendName(backend string) string { return resolveBackend(backend) }

// Disabled reports whether backend names the no-op tier ("none"/"noop"/
// "off"). Config synthesizers (e.g. `sheaf scan --auto`) use it to skip
// wiring LLM stages entirely, so a deterministic-only run does zero model
// work rather than building a client that would contribute nothing.
func Disabled(backend string) bool { return resolveBackend(backend) == BackendNone }
