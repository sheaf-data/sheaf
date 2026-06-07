package indexer

import (
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// Strategy names a single attribution strategy. The three values
// correspond 1:1 to the three strategies the matcher implements,
// referred to throughout the indexer and the validation log as
// "Strategy 1 / 2 / 3".
type Strategy uint8

const (
	StrategyUnspecified Strategy = iota
	// StrategyDirectRef: the test/doc's contract_refs (or structured
	// canonical_refs) literally name the element by its canonical ID,
	// an alias, or a per-language canonical form. Strongest evidence.
	StrategyDirectRef
	// StrategyImplementsMap: the test's source file shares a directory
	// with an implementation class that implements the element, and the
	// impl's local name token appears in the test's name/path tokens.
	// Spatial proximity gated by a name-mention check.
	StrategyImplementsMap
	// StrategyNameTokens: every CamelCase-split token of the element's
	// local ID appears in the union of the test's name tokens and path
	// tokens. Weakest evidence; over-attributes for fine-grained kinds.
	StrategyNameTokens
)

func (s Strategy) String() string {
	switch s {
	case StrategyDirectRef:
		return "direct_ref"
	case StrategyImplementsMap:
		return "implements_map"
	case StrategyNameTokens:
		return "name_tokens"
	default:
		return "unspecified"
	}
}

// kindStrategies is the per-kind admission policy.
//
// Validation across the 9 example reports demonstrated that
// Strategy 3's name-token fallback over-attributes catastrophically for
// fine-grained kinds. Coarse kinds (SUBCOMMAND, LIBRARY, CONFIG_FACET)
// continue to admit Strategy 3 because their structural shape (multi-
// token CLI shape, library-distinctive tokens) keeps the fallback
// precise enough to be useful.
//
// Strategy 2 (implements-map) is admitted by NO kind under default
// policy. The implements-map heuristic was demoted from a coverage strategy to a
// relationship-only data source after producing 57–100% FP on three
// Fuchsia FIDL example reports. Interface elements (METHOD / TYPE /
// PROTOCOL) now render their IMPLEMENTS edges via the `implementations`
// surface in CoverageProfile, not via test attribution.
//
// The StrategyImplementsMap constant itself is retained — it is the
// strategy slot the future `conformance_test_globs` feature will reuse.
//
// Order within each slice expresses priority: strategies are tried in
// order; the first to fire wins.
var kindStrategies = map[contractpb.ContractElementKind][]Strategy{
	// Fine-grained: direct refs only.
	contractpb.ContractElementKind_FLAG:        {StrategyDirectRef},
	contractpb.ContractElementKind_SWITCH:      {StrategyDirectRef},
	contractpb.ContractElementKind_CONFIG_KNOB: {StrategyDirectRef},
	contractpb.ContractElementKind_METHOD:      {StrategyDirectRef},
	contractpb.ContractElementKind_TYPE:        {StrategyDirectRef},
	contractpb.ContractElementKind_PROTOCOL:    {StrategyDirectRef},
	contractpb.ContractElementKind_POSITIONAL:  {StrategyDirectRef},

	// Internal-impl kinds: direct refs only.
	contractpb.ContractElementKind_CPP_CLASS: {StrategyDirectRef},
	contractpb.ContractElementKind_RUST_TYPE: {StrategyDirectRef},

	// Coarse: direct refs + name-token fallback.
	contractpb.ContractElementKind_SUBCOMMAND:   {StrategyDirectRef, StrategyNameTokens},
	contractpb.ContractElementKind_LIBRARY:      {StrategyDirectRef, StrategyNameTokens},
	contractpb.ContractElementKind_CONFIG_FACET: {StrategyDirectRef, StrategyNameTokens},

	// Legacy / rare: direct refs only.
	contractpb.ContractElementKind_SYSCALL: {StrategyDirectRef},

	// C++ kinds reserved by the schema-only PR 0 of the C++/Sphinx/monorepo
	// work. No adapter emits these yet; entries exist only to satisfy
	// TestKindStrategiesExhaustive. Real per-kind policy lands with the
	// header adapter in PR 3.
	contractpb.ContractElementKind_CPP_MACRO:         {StrategyDirectRef},
	contractpb.ContractElementKind_CPP_FREE_FUNCTION: {StrategyDirectRef},
	contractpb.ContractElementKind_CPP_METHOD:        {StrategyDirectRef},
	contractpb.ContractElementKind_CPP_ENUM:          {StrategyDirectRef},
}

// kindSurfaces declares, per ContractElementKind, the surfaces against
// which the element's coverage is rendered and against which the
// per-element bridged predicate is evaluated.
//
// Interface kinds (METHOD / TYPE / PROTOCOL / SYSCALL) declare NO
// tests surface. A FIDL/proto declaration is an interface — there is
// no executable body to test. What gets tested is the impl class that
// responds to the protocol (CPP_CLASS / RUST_TYPE), or — in projects
// that opt in via Config.conformance_test_globs — a dedicated
// conformance test suite.
//
// CLI kinds (FLAG / SWITCH / CONFIG_KNOB / SUBCOMMAND / POSITIONAL /
// CONFIG_FACET) keep tests as a surface; CLI tests really do test
// flags.
//
// Implementation kinds (CPP_CLASS / RUST_TYPE) carry tests + docs.reference.
// They are where the real test coverage of an interface lives.
//
// LIBRARY elements are synthetic grouping records (per buildProfile's
// guard at indexer.go); they render docs.reference / docs.concepts /
// examples and are excluded from the masthead's bridged numerator and
// denominator.
var kindSurfaces = map[contractpb.ContractElementKind][]string{
	// Interface kinds: implementations replaces tests.
	contractpb.ContractElementKind_METHOD:   {"docs.reference", "docs.concepts", "examples", "implementations"},
	contractpb.ContractElementKind_TYPE:     {"docs.reference", "docs.concepts", "examples", "implementations"},
	contractpb.ContractElementKind_PROTOCOL: {"docs.reference", "docs.concepts", "examples", "implementations"},
	contractpb.ContractElementKind_SYSCALL:  {"docs.reference", "docs.concepts", "examples", "implementations"},

	// CLI kinds: tests are real; no implementations notion.
	contractpb.ContractElementKind_FLAG:         {"docs.reference", "docs.concepts", "examples", "tests"},
	contractpb.ContractElementKind_SWITCH:       {"docs.reference", "docs.concepts", "examples", "tests"},
	contractpb.ContractElementKind_CONFIG_KNOB:  {"docs.reference", "docs.concepts", "examples", "tests"},
	contractpb.ContractElementKind_SUBCOMMAND:   {"docs.reference", "docs.concepts", "examples", "tests"},
	contractpb.ContractElementKind_POSITIONAL:   {"docs.reference", "docs.concepts", "examples", "tests"},
	contractpb.ContractElementKind_CONFIG_FACET: {"docs.reference", "docs.concepts", "examples", "tests"},

	// Implementation kinds: where real test coverage lives.
	contractpb.ContractElementKind_CPP_CLASS: {"docs.reference", "tests"},
	contractpb.ContractElementKind_RUST_TYPE: {"docs.reference", "tests"},

	// LIBRARY is a synthetic grouping element. No tests, no impls.
	contractpb.ContractElementKind_LIBRARY: {"docs.reference", "docs.concepts", "examples"},

	// C++ kinds reserved by the schema-only PR 0 of the C++/Sphinx/monorepo
	// work. No adapter emits these yet; entries exist only to satisfy
	// TestKindSurfacesExhaustive. Real per-kind surface sets land with
	// the header adapter in PR 3.
	contractpb.ContractElementKind_CPP_MACRO:         {"docs.reference"},
	contractpb.ContractElementKind_CPP_FREE_FUNCTION: {"docs.reference"},
	contractpb.ContractElementKind_CPP_METHOD:        {"docs.reference"},
	contractpb.ContractElementKind_CPP_ENUM:          {"docs.reference"},
}

// Surface identifier constants — referenced by the bridged-math layer
// and the renderer. Keep in sync with kindSurfaces values.
const (
	SurfaceDocsReference   = "docs.reference"
	SurfaceDocsConcepts    = "docs.concepts"
	SurfaceExamples        = "examples"
	SurfaceTests           = "tests"
	SurfaceImplementations = "implementations"
)

// isInterfaceKind reports whether the kind is an interface-kind
// (declaration of a contract, with no executable body of its own).
// Used by the indexer to decide which elements get the implementations
// surface populated, and by the renderer to omit the tests panel.
func isInterfaceKind(k contractpb.ContractElementKind) bool {
	switch k {
	case contractpb.ContractElementKind_METHOD,
		contractpb.ContractElementKind_TYPE,
		contractpb.ContractElementKind_PROTOCOL,
		contractpb.ContractElementKind_SYSCALL:
		return true
	}
	return false
}

// surfacesFor returns the declared surface set for the given kind.
// Mirrors strategiesFor's fail-closed default: an unknown kind returns
// {docs.reference} only. The exhaustiveness unit test
// (TestKindSurfacesExhaustive) catches missing kinds at CI time.
func surfacesFor(k contractpb.ContractElementKind) []string {
	if s, ok := kindSurfaces[k]; ok {
		return s
	}
	return []string{SurfaceDocsReference}
}

// admitsStrategy reports whether the given kind admits the given
// strategy under the default policy.
func admitsStrategy(k contractpb.ContractElementKind, s Strategy) bool {
	for _, allowed := range strategiesFor(k) {
		if allowed == s {
			return true
		}
	}
	return false
}

// strategiesFor returns the admitted strategies for the given kind.
// An unknown kind admits direct_ref only — the most conservative
// fail-closed default. The exhaustiveness unit test (policy_test.go)
// catches any kind missing from the table at CI time.
func strategiesFor(k contractpb.ContractElementKind) []Strategy {
	if s, ok := kindStrategies[k]; ok {
		return s
	}
	return []Strategy{StrategyDirectRef}
}
