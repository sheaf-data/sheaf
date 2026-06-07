// Package buildgraph defines a generic framework for build-file
// recognizers. A Recognizer parses one form of build metadata (a
// pw_facade() GN declaration, a Bazel visibility() block, a Cargo
// workspace member list, ...) and returns a partial BuildHints. The
// orchestrator runs every configured recognizer, combines the
// results with Composite, and threads the resulting BuildHints into
// every contract / test / doc adapter that accepts one.
//
// The framework is generic. The first concrete recognizer
// (internal/adapters/pwfacade) plugs into the same Recognizer
// interface alongside future Bazel / Cargo / Buck recognizers.
package buildgraph

import (
	"context"
	"fmt"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/adapters/pwfacade"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// Recognizer parses one form of build metadata and returns a partial
// BuildHints. Recognizers MUST be deterministic.
type Recognizer interface {
	Name() string
	// Walk reads the repo (rooted at repoRoot) and returns the
	// hints it can determine, plus an error for I/O or parse
	// failures.
	Walk(ctx context.Context, repoRoot string) (adapters.BuildHints, error)
}

// Composite combines multiple BuildHints into one by
// first-non-empty-answer semantics: for each query, ask each child
// in order and return the first one that returns known=true (for
// IsPublic) or ok=true (for FacadeOf).
//
// An empty argument list returns adapters.NopHints{}.
func Composite(hs ...adapters.BuildHints) adapters.BuildHints {
	// Drop nils.
	clean := make([]adapters.BuildHints, 0, len(hs))
	for _, h := range hs {
		if h == nil {
			continue
		}
		clean = append(clean, h)
	}
	if len(clean) == 0 {
		return adapters.NopHints{}
	}
	if len(clean) == 1 {
		return clean[0]
	}
	return composite(clean)
}

type composite []adapters.BuildHints

func (c composite) IsPublic(relPath string) (bool, bool) {
	for _, h := range c {
		if ok, known := h.IsPublic(relPath); known {
			return ok, known
		}
	}
	return false, false
}

func (c composite) FacadeOf(relPath string) (string, string, bool) {
	for _, h := range c {
		if facade, backend, ok := h.FacadeOf(relPath); ok {
			return facade, backend, true
		}
	}
	return "", "", false
}

func (c composite) FacadeModule(module string) (string, bool) {
	for _, h := range c {
		if facade, ok := h.FacadeModule(module); ok {
			return facade, true
		}
	}
	return "", false
}

func (c composite) FacadeSymbol(backendModule, backendLocalName string) (string, bool) {
	for _, h := range c {
		if name, ok := h.FacadeSymbol(backendModule, backendLocalName); ok {
			return name, true
		}
	}
	return "", false
}

// Run is the orchestrator entry point. It interprets the BuildGraph
// config block, runs every enabled recognizer against repoRoot, and
// returns the composite BuildHints. Pass cfg=nil (or a zero-value
// BuildGraph) to get NopHints back.
func Run(ctx context.Context, cfg *configpb.BuildGraph, repoRoot string) (adapters.BuildHints, error) {
	if cfg == nil {
		return adapters.NopHints{}, nil
	}
	var recs []Recognizer
	if pf := cfg.GetPwFacade(); pf != nil {
		recs = append(recs, pwfacade.New(pwfacade.Config{
			Include: pf.GetInclude(),
			Exclude: pf.GetExclude(),
		}))
	}
	if len(recs) == 0 {
		return adapters.NopHints{}, nil
	}
	hints := make([]adapters.BuildHints, 0, len(recs))
	for _, r := range recs {
		h, err := r.Walk(ctx, repoRoot)
		if err != nil {
			return nil, fmt.Errorf("buildgraph: recognizer %s: %w", r.Name(), err)
		}
		if h == nil {
			continue
		}
		hints = append(hints, h)
	}
	return Composite(hints...), nil
}
