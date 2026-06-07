// Package adapters defines the interfaces every ecosystem-specific
// parser implements. The orchestrator works only against these
// interfaces; concrete adapters live in subpackages and self-register
// via init().
package adapters

import (
	"context"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// ScopeConfig is the subset of project scope an adapter needs to know
// to limit its discovery. Adapters receive this from the orchestrator
// alongside their own typed config.
type ScopeConfig struct {
	Libraries   []string // primary libraries; empty means no filter
	AlsoInclude []string
	Exclude     []string
}

// ContractAnchorParser discovers ContractElements from contract source
// (FIDL libs, argh structs, OpenAPI specs, etc.).
type ContractAnchorParser interface {
	Name() string
	Version() string
	Discover(ctx context.Context, repoPath string, scope ScopeConfig) ([]*contractpb.ContractElement, error)
}

// TestParser discovers TestCases from a test framework's files.
type TestParser interface {
	Name() string
	Version() string
	SupportedFrameworks() []string
	Discover(ctx context.Context, repoPath string, scope ScopeConfig) ([]*testcasepb.TestCase, error)
}

// DocParser parses human-authored prose (markdown, manpages, etc.).
type DocParser interface {
	Name() string
	Version() string
	SupportedFormats() []string
	Parse(ctx context.Context, repoPath string, scope ScopeConfig) ([]*docclaimpb.DocClaim, error)
}

// RenderedReferenceParser parses pre-rendered reference bundles
// (fidldoc.zip, clidoc.tar.gz). Distinct from DocParser because the
// bundle structure and anchor schemes are known in advance.
type RenderedReferenceParser interface {
	Name() string
	Version() string
	Parse(ctx context.Context) ([]*docclaimpb.DocClaim, error)
}

// ImplementsMapper bridges implementation classes back to the
// contract elements they serve. Emits one ContractElement per
// discovered implementing class.
type ImplementsMapper interface {
	Name() string
	Version() string
	Discover(ctx context.Context, repoPath string, scope ScopeConfig) ([]*contractpb.ContractElement, error)
}

// BuildHints lets build-graph adapters (e.g. pwfacade) pass project-
// structural metadata to contract/test/doc adapters that run later in
// the pipeline. Future build-graph recognizers populate it; future
// C++/Rust/etc. adapters consume it. PR 0 ships interface + zero-value
// implementation only.
type BuildHints interface {
	// IsPublic reports whether relPath is part of the project's
	// public API surface. ok=false, known=false means the build
	// graph has no opinion (treat the file as public by default).
	IsPublic(relPath string) (ok bool, known bool)

	// FacadeOf returns the facade module name and backend name if
	// relPath belongs to a backend of a facade declaration.
	// Returns (_, _, false) when relPath is not a facade backend.
	FacadeOf(relPath string) (facade string, backend string, ok bool)

	// FacadeModule reports the facade a backend MODULE backs, keyed by
	// module name rather than file path. The facade post-pass uses it
	// when an element's own header is not directly listed as a backend
	// header (Pigweed backends route their facade glue through
	// public_overrides/ headers, so the symbol-defining header is not
	// the one the build graph names). Returns ("", false) when the
	// module is not a recognized backend.
	FacadeModule(module string) (facade string, ok bool)

	// FacadeSymbol maps a backend element's local name to the facade
	// element local name it implements, encoding an ecosystem's
	// implements-by-convention rule (e.g. Pigweed's PW_HANDLE_<X> backend
	// macro implements the facade's PW_<X> macro). The facade post-pass
	// consults this before falling back to same-local-name matching, so
	// each recognizer carries its own convention without the generic
	// indexer special-casing any ecosystem. Returns ("", false) when the
	// recognizer has no opinion (the default — same-name matching applies).
	FacadeSymbol(backendModule, backendLocalName string) (facadeLocalName string, ok bool)
}

// NopHints is the zero-value BuildHints. Adapters that have no
// build-graph source can use it directly; the orchestrator passes
// NopHints{} until a build-graph adapter is wired.
type NopHints struct{}

func (NopHints) IsPublic(string) (bool, bool)               { return false, false }
func (NopHints) FacadeOf(string) (string, string, bool)     { return "", "", false }
func (NopHints) FacadeModule(string) (string, bool)         { return "", false }
func (NopHints) FacadeSymbol(string, string) (string, bool) { return "", false }
