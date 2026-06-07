// Package cppheader is a regex-based C++ public-header contract
// anchor. Walks .h / .hpp files and emits one ContractElement per
// public class, public method, free function, #define-style macro
// (gated on emit_macros), and enum.
//
// Generic — no libclang dependency, no project-specific conventions.
// Pigweed-flavored behavior is expressed via config knobs
// (emit_macros, ignored_attribute_macros, doc_comment_styles).
package cppheader

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

const Name = "cppheader"
const Version = "0.1.0"

type Config struct {
	Include                []string
	Exclude                []string
	EmitMacros             bool
	DocCommentStyles       []string
	IgnoredAttributeMacros []string
}

type Adapter struct {
	include      []string
	exclude      []string
	emitMacros   bool
	docStyles    map[string]bool
	ignoredAttrs map[string]bool
	hints        adapters.BuildHints
}

// New constructs an Adapter. Pass adapters.NopHints{} when no
// build-graph adapter is wired.
func New(cfg Config, hints adapters.BuildHints) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.h", "**/*.hpp"}
	}
	if hints == nil {
		hints = adapters.NopHints{}
	}
	styles := map[string]bool{}
	if len(cfg.DocCommentStyles) == 0 {
		styles["triple_slash"] = true
		styles["javadoc"] = true
	} else {
		for _, s := range cfg.DocCommentStyles {
			styles[s] = true
		}
	}
	attrs := map[string]bool{}
	for _, m := range cfg.IgnoredAttributeMacros {
		attrs[m] = true
	}
	return &Adapter{
		include:      include,
		exclude:      cfg.Exclude,
		emitMacros:   cfg.EmitMacros,
		docStyles:    styles,
		ignoredAttrs: attrs,
		hints:        hints,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// SetHints replaces the adapter's BuildHints. The orchestrator computes
// hints (via buildgraph.Run) only inside Run, after the anchors are
// constructed, so it injects the real hints here before Discover runs.
// A nil value is ignored to preserve the constructor default.
func (a *Adapter) SetHints(hints adapters.BuildHints) {
	if hints != nil {
		a.hints = hints
	}
}

func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	var out []*contractpb.ContractElement
	seen := map[string]bool{}
	err := adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ok, known := a.hints.IsPublic(rel); known && !ok {
			return nil
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return nil
		}
		library := libraryFromPath(rel)
		if !libraryInScope(library, scope) {
			return nil
		}
		for _, e := range a.parseFile(rel, library, string(body)) {
			if seen[e.Id] {
				continue
			}
			seen[e.Id] = true
			out = append(out, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// libraryFromPath derives a library slug from the first path segment
// under the nearest `include/` or `public/` directory, falling back
// to the top-level directory.
func libraryFromPath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		if (p == "public" || p == "include") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	if len(parts) > 1 {
		return parts[0]
	}
	return "cpp"
}

func libraryInScope(lib string, scope adapters.ScopeConfig) bool {
	for _, ex := range scope.Exclude {
		if matchLibrary(ex, lib) {
			return false
		}
	}
	if len(scope.Libraries) == 0 && len(scope.AlsoInclude) == 0 {
		return true
	}
	for _, l := range scope.Libraries {
		if matchLibrary(l, lib) {
			return true
		}
	}
	for _, l := range scope.AlsoInclude {
		if matchLibrary(l, lib) {
			return true
		}
	}
	return false
}

func matchLibrary(pattern, lib string) bool {
	if pattern == lib {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		return strings.HasPrefix(lib, strings.TrimSuffix(pattern, "*"))
	}
	return false
}
