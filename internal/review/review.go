// Package review defines the Adapter interface that the PR-bot uses
// to post rendered comments to a review system, plus the v1 set of
// concrete adapters (noop, file, gerrit, github).
//
// The "post" side is intentionally pluggable so Sheaf stays out of
// each review system's webhook plumbing. Implementors run their own
// receiver, translate their native event shape to Sheaf's bot
// request shape, and let Sheaf handle the scan + delta + render +
// post.

package review

import (
	"context"
	"errors"
	"fmt"

	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// Adapter posts a rendered PR comment body to a review system.
//
// Adapters receive only the markdown body — not the structured
// prbot.Comment — to avoid an import cycle. The bot caller takes
// care of rendering the body before calling Post.
type Adapter interface {
	Name() string
	// Post returns a human-readable description of where the comment
	// landed (URL, file path, "logged-only", etc.) and an error.
	// Implementations must be context-cancellable.
	Post(ctx context.Context, prRef, body string) (string, error)
}

// ErrNoAdapter is returned when no review adapter is configured.
var ErrNoAdapter = errors.New("review: no adapter configured")

// Build constructs an Adapter from a ReviewAdapterConfig. Returns
// NoopAdapter when cfg is nil or adapter is "" or "noop".
func Build(cfg *configpb.ReviewAdapterConfig) (Adapter, error) {
	if cfg == nil {
		return Noop{}, nil
	}
	switch cfg.GetAdapter() {
	case "", "noop":
		return Noop{}, nil
	case "file":
		// file adapter is configured via per-adapter oneof, but the
		// v1 schema doesn't yet have a typed `file { ... }` block.
		// We accept a free-form output path via the env var
		// SHEAF_REVIEW_FILE_OUT; callers can also construct the
		// adapter directly via NewFile(...) for explicit paths.
		return NewFileFromEnv(), nil
	case "gerrit":
		gc := cfg.GetGerrit()
		if gc == nil {
			return nil, fmt.Errorf("review: gerrit adapter requires gerrit { ... } block")
		}
		return NewGerrit(GerritConfig{
			Host:         gc.GetHost(),
			Project:      gc.GetProject(),
			AuthTokenEnv: gc.GetAuthTokenEnv(),
		}), nil
	case "github":
		gh := cfg.GetGithub()
		if gh == nil {
			return nil, fmt.Errorf("review: github adapter requires github { ... } block")
		}
		return NewGitHub(GitHubConfig{
			Repo:     gh.GetRepo(),
			TokenEnv: gh.GetTokenEnv(),
		}), nil
	default:
		return nil, fmt.Errorf("review: unknown adapter %q", cfg.GetAdapter())
	}
}
