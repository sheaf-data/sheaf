// Package llm defines the interfaces Sheaf consumes for LLM-backed
// operations: text generation (v2+ HITL claim extraction) and
// embeddings (v1 find_examples semantic search).
//
// Concrete implementations live in subpackages (e.g. internal/llm/ollama)
// and are selected by name at runtime from the project's sheaf.textproto.
package llm

import (
	"context"
	"math"
)

// Embedder produces vector embeddings of text. Implementations should
// batch inputs when possible; the slice returned must be the same
// length as `texts` and in the same order.
type Embedder interface {
	Name() string
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Client is the general LLM surface for prompt-response and (v2+)
// structured claim extraction.
type Client interface {
	Name() string
	Generate(ctx context.Context, prompt string) (string, error)
}

// CachedGenerator is an optional capability: a backend that supports a
// cached prefix (e.g. Anthropic prompt caching). Callers that re-send a
// large identical prefix across many calls — the attribution pass's
// instructions + candidate list, the same for every file — should type-
// assert to this and prefer GenerateCached, falling back to
// Generate(prefix + "\n\n" + variable) when unavailable. `prefix` is the
// stable, cacheable portion; `variable` is the per-call body.
type CachedGenerator interface {
	GenerateCached(ctx context.Context, prefix, variable string) (string, error)
}

// Cosine returns the cosine similarity of two vectors. Returns 0
// when either vector has zero magnitude or differing dimensions.
func Cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, magA, magB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		magA += float64(a[i]) * float64(a[i])
		magB += float64(b[i]) * float64(b[i])
	}
	if magA == 0 || magB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(magA) * math.Sqrt(magB)))
}

// NoopEmbedder always returns ErrNoEmbedder. Used as a sentinel when
// no embedding backend is configured — callers should fall back to
// deterministic scoring (e.g., token overlap).
type NoopEmbedder struct{}

func (NoopEmbedder) Name() string { return "noop" }
func (NoopEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, ErrNoEmbedder
}

// NoopClient is the equivalent for LLM generation.
type NoopClient struct{}

func (NoopClient) Name() string                                         { return "noop" }
func (NoopClient) Generate(_ context.Context, _ string) (string, error) { return "", ErrNoClient }
