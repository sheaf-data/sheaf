package llm

import (
	"context"
	"fmt"

	"github.com/sheaf-data/sheaf/internal/llm/embedcache"
	"github.com/sheaf-data/sheaf/internal/llm/ollama"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// BuildEmbedder constructs an Embedder from an LLMConfig. Returns
// NoopEmbedder if no embedding provider is configured. The returned
// embedder is never nil.
//
// For backends that ship with a network daemon (ollama), we don't
// liveness-probe here — that's a runtime concern; callers should
// surface failures via the analyzer / find_examples path.
func BuildEmbedder(cfg *configpb.LLMConfig) (Embedder, error) {
	if cfg == nil {
		return NoopEmbedder{}, nil
	}
	switch cfg.GetEmbeddings() {
	case "", "noop":
		return NoopEmbedder{}, nil
	case "ollama-embed":
		oc := cfg.GetOllamaEmbeddings()
		if oc == nil {
			return nil, fmt.Errorf("llm: embeddings=%q requires ollama_embeddings block", cfg.GetEmbeddings())
		}
		return ollama.NewEmbedder(ollama.Config{
			Host:  oc.GetHost(),
			Port:  int(oc.GetPort()),
			Model: oc.GetModel(),
		}), nil
	default:
		return nil, fmt.Errorf("llm: unknown embeddings provider %q (supported: ollama-embed, noop)", cfg.GetEmbeddings())
	}
}

// BuildClient constructs a generation Client from an LLMConfig.
func BuildClient(cfg *configpb.LLMConfig) (Client, error) {
	if cfg == nil {
		return NoopClient{}, nil
	}
	switch cfg.GetClient() {
	case "", "noop":
		return NoopClient{}, nil
	case "local-llama":
		lc := cfg.GetLocalLlama()
		if lc == nil {
			return nil, fmt.Errorf("llm: client=%q requires local_llama block", cfg.GetClient())
		}
		return ollama.NewClient(ollama.Config{
			Host:  lc.GetHost(),
			Port:  int(lc.GetPort()),
			Model: lc.GetModel(),
		}), nil
	default:
		return nil, fmt.Errorf("llm: unknown client %q (supported: local-llama, noop)", cfg.GetClient())
	}
}

// BuildCache constructs the embed cache rooted at the project's
// configured cache location. Returns nil if cache config is absent or
// disabled, in which case callers should skip caching.
func BuildCache(cfg *configpb.CacheStoreConfig) (embedcache.Cache, error) {
	if cfg == nil {
		return nil, nil
	}
	if cfg.GetStore() != "filesystem" && cfg.GetStore() != "" {
		return nil, fmt.Errorf("llm: cache.store=%q not supported in v1 (use filesystem)", cfg.GetStore())
	}
	fs := cfg.GetFilesystem()
	if fs == nil || fs.GetPath() == "" {
		return nil, nil
	}
	return embedcache.New(fs.GetPath() + "/llm")
}

// ProbeEmbedder is a thin reachability check used by `sheaf doctor`
// to differentiate "configured but daemon down" from "happy path".
// Only meaningful for backends that ship as network daemons.
func ProbeEmbedder(ctx context.Context, cfg *configpb.LLMConfig) error {
	if cfg == nil || cfg.GetEmbeddings() == "" || cfg.GetEmbeddings() == "noop" {
		return nil
	}
	if cfg.GetEmbeddings() == "ollama-embed" {
		oc := cfg.GetOllamaEmbeddings()
		if oc == nil {
			return nil
		}
		return ollama.Ping(ctx, oc.GetHost(), int(oc.GetPort()))
	}
	return nil
}
