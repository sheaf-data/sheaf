// Package indexer takes a corpus of raw ContractElements, TestCases,
// and DocClaims and produces a CoverageProfile per ContractElement.
//
// The indexer's responsibilities, per sheaf-design.md §5.3:
//
//  1. Materialize COMPOSED_FROM inheritance so a Directory protocol
//     surface-exposes the methods inherited from Openable/Node etc.
//     (Phase 0 noted this is ~65% of fuchsia.io's effective surface.)
//  2. Follow IMPLEMENTS edges so a test that exercises a C++ class
//     gets attributed to the FIDL method that class implements.
//  3. For each TestCase: identify which ContractElements it exercises
//     (by name-token overlap, by qualified mention, by the implements
//     map). Emit TestRefs into the appropriate tests.* bucket of each
//     touched element's CoverageProfile.
//  4. For each DocClaim: identify which ContractElement(s) it
//     references, emit DocRefs into the appropriate docs.* bucket.
//  5. For each element: compute GapsSummary by inspecting which
//     categories are empty / thin.

package indexer

import (
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/categorize"
	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/coverage/refs"
	"github.com/sheaf-data/sheaf/internal/slug"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// Indexer is the per-scan cross-referencer.
type Indexer struct {
	c   *corpus.Corpus
	cat *categorize.Categorizer

	// Lookup tables built during Build().
	elemByID           map[string]*contractpb.ContractElement
	composesByProtocol map[string][]string // protocol ID -> list of composed parent IDs (transitive)
	implementorsByFIDL map[string][]string // FIDL element ID -> list of implementing class IDs
	methodsByProtocol  map[string][]*contractpb.ContractElement
	elementsByTokens   map[string][]*contractpb.ContractElement // token -> elements whose ID contains that token
	elementsByLib      map[string][]*contractpb.ContractElement // library -> all elements

	// SAME_AS sibling adjacency, materialized from codegen_bridge
	// config entries. Bidirectional: an edge A↔B inserts B into
	// sameAsSiblings[A] and A into sameAsSiblings[B]. Consulted by
	// buildProfile for cross-ecosystem evidence aggregation.
	sameAsSiblings map[string][]string

	// bridges keeps a reference to the parsed bridges so a follow-up
	// can introspect them (e.g. report rendering). Currently unused
	// outside of resolveSameAs() but kept for future diagnostics.
	bridges []Bridge

	// commonNoisyWords drives the "single-word type needs a library
	// token to match" guard in testCaseRefsElement. Populated from
	// Options.NoisyWords or the package default; lowercased.
	commonNoisyWords map[string]bool

	// ambiguousLastTokens captures pairs of element-leaf-token forms
	// where BOTH the singular and the plural exist as distinct
	// elements (e.g. channelz has both Channelz.GetChannel and
	// Channelz.GetTopChannels — last tokens "channel" and "channels"
	// both appear). addWithStemBlocked refuses to bridge between members of
	// such pairs so a test about plural "channels" stops false-
	// attributing to the singular-name "GetChannel". Computed once
	// after buildLookups; lowercased.
	ambiguousLastTokens map[string]bool

	// prefixCollisionSiblings indexes single-token element leaves
	// against the longer-token sibling leaves they prefix-collide
	// with (e.g. `Channel` [channel] vs `ChannelTrace` [channel,
	// trace]). When Strategy 3 would attribute a test to the short
	// leaf, the matcher first checks whether any longer sibling also
	// matches the test via name-token overlap; if so, the short-leaf
	// attribution is refused in favor of the more-specific match.
	// Per library: map[library]map[shortLeafToken][][]siblingTokens.
	// Computed once after buildLookups.
	prefixCollisionSiblings map[string]map[string][][]string

	// facadeEdges captures the IMPLEMENTS edges emitted by the
	// facade post-pass (when Options.BuildHints is non-nil). Kept
	// for diagnostics only — the edges themselves live on the
	// elements' Relationships slices and feed implementorsByFIDL
	// via buildLookups.
	facadeEdges []ImplementsEdge
}

// Options tunes the indexer's heuristic matching. Zero value is
// the safe default (Fuchsia-tuned).
type Options struct {
	// NoisyWords adds project-specific single-word type names that
	// over-match generic English (e.g. gRPC users might add
	// "service", "request"). Merged into the indexer's built-in
	// English-word list; both are required to pass the
	// single-word-type guard.
	//
	// Pass nil/empty to use only the built-in list.
	NoisyWords []string

	// CodegenBridges, when non-empty, drives SAME_AS edge
	// materialization between elements across ecosystems. Each entry
	// is a parsed Bridge (use indexer.ParseBridges to convert from
	// configpb.CodegenBridge). The indexer walks each bridge's source
	// ecosystem and emits SAME_AS edges to matching targets in the
	// target ecosystem, then unions evidence (tests, docs, examples,
	// implementations) across each connected element when building
	// coverage profiles.
	//
	// Aggregation is direct-neighbor only in v1 — A↔B↔C does not
	// pull C's evidence onto A. Transitive aggregation can be added
	// when a real use case appears.
	CodegenBridges []Bridge

	// BuildHints, when non-nil and not NopHints, drives the facade
	// post-pass: RunFacadePass walks every element's source path
	// through hints.FacadeOf, and when an element belongs to a
	// backend module, emits IMPLEMENTS relationships from that
	// element to every matching element in the facade module. The
	// resulting edges feed the existing implementorsByFIDL map and
	// thus surface on the facade element's `implementations`
	// coverage surface — same exit point as the implements-map
	// adapter.
	BuildHints adapters.BuildHints
}

// New builds an Indexer over the given corpus + categorizer (which
// may be nil; coverage profiles will then have all references in
// unspecified buckets).
func New(c *corpus.Corpus, cat *categorize.Categorizer) *Indexer {
	return NewWithOptions(c, cat, Options{})
}

// NewWithOptions is like New, with knobs for tuning the heuristic
// matcher. Use when scanning a non-Fuchsia / non-FIDL project that
// has its own bag of generic type names.
func NewWithOptions(c *corpus.Corpus, cat *categorize.Categorizer, opts Options) *Indexer {
	noisy := make(map[string]bool, len(defaultNoisyWords)+len(opts.NoisyWords))
	for w := range defaultNoisyWords {
		noisy[w] = true
	}
	for _, w := range opts.NoisyWords {
		noisy[strings.ToLower(w)] = true
	}
	idx := &Indexer{
		c:                  c,
		cat:                cat,
		elemByID:           make(map[string]*contractpb.ContractElement),
		composesByProtocol: make(map[string][]string),
		implementorsByFIDL: make(map[string][]string),
		methodsByProtocol:  make(map[string][]*contractpb.ContractElement),
		elementsByTokens:   make(map[string][]*contractpb.ContractElement),
		elementsByLib:      make(map[string][]*contractpb.ContractElement),
		commonNoisyWords:   noisy,
		sameAsSiblings:     make(map[string][]string),
		bridges:            opts.CodegenBridges,
	}
	// Facade post-pass runs BEFORE buildLookups so the IMPLEMENTS
	// edges it adds participate in implementorsByFIDL exactly like
	// edges emitted by the implements-map adapter.
	if opts.BuildHints != nil {
		idx.facadeEdges = RunFacadePass(opts.BuildHints, c.Elements())
	}
	idx.buildLookups()
	idx.computeAmbiguousLastTokens()
	idx.computePrefixCollisions()
	idx.resolveSameAs()
	return idx
}

// Build runs the full index. After Build() returns, the corpus has
// a CoverageProfile populated for every ContractElement.
func (i *Indexer) Build() {
	for _, e := range i.c.Elements() {
		prof := i.buildProfile(e)
		i.c.SetProfile(prof)
	}
	i.aggregateSameAs()
	i.recomputeGapsForSameAsSiblings()
}

// Stats summarizes index-time observations for diagnostics.
type Stats struct {
	Elements          int
	ProfilesBuilt     int
	TotalCrossRefs    int // every TestRef + DocRef across all profiles
	TestRefsByElement int
	DocRefsByElement  int
	ImplementsLinks   int // count of IMPLEMENTS edges we materialized
	InheritedMethods  int // count of methods surfaced via composition
	FacadeEdges       int // count of IMPLEMENTS edges emitted by the facade post-pass
}

// Build returns Stats summarizing the index pass.
func (i *Indexer) BuildWithStats() Stats {
	s := Stats{}
	for elemID, impls := range i.implementorsByFIDL {
		_ = elemID
		s.ImplementsLinks += len(impls)
	}
	for _, e := range i.c.Elements() {
		prof := i.buildProfile(e)
		s.Elements++
		s.ProfilesBuilt++
		s.TestRefsByElement += countTestRefs(prof)
		s.DocRefsByElement += countDocRefs(prof)
		i.c.SetProfile(prof)
	}
	s.TotalCrossRefs = s.TestRefsByElement + s.DocRefsByElement
	for _, methods := range i.methodsByProtocol {
		for _, m := range methods {
			// Inherited methods are those whose owning protocol differs
			// from the protocol where they were declared. We count via
			// INHERITED_FROM relationships we synthesize below.
			for _, r := range m.GetRelationships() {
				if r.GetKind() == contractpb.RelationshipKind_INHERITED_FROM {
					s.InheritedMethods++
				}
			}
		}
	}
	s.FacadeEdges = len(i.facadeEdges)
	i.aggregateSameAs()
	i.recomputeGapsForSameAsSiblings()
	return s
}

// recomputeGapsForSameAsSiblings re-runs gaps computation for every
// element that received aggregated evidence. Without this, an element
// whose original buildProfile produced no docs/tests but whose sibling
// supplied them would still claim "missing: docs.reference" / "missing:
// tests" — making gapsSummary lie about cross-language coverage.
func (i *Indexer) recomputeGapsForSameAsSiblings() {
	if len(i.sameAsSiblings) == 0 {
		return
	}
	for elemID := range i.sameAsSiblings {
		prof := i.c.Profile(elemID)
		if prof == nil {
			continue
		}
		e := i.elemByID[elemID]
		if e == nil {
			continue
		}
		prof.GapsSummary = i.computeGaps(e, prof)
	}
}

// --- lookup-building ---

func (i *Indexer) buildLookups() {
	// First pass: index by ID.
	for _, e := range i.c.Elements() {
		i.elemByID[e.GetId()] = e
		i.elementsByLib[e.GetLibrary()] = append(i.elementsByLib[e.GetLibrary()], e)
		// Token index for fuzzy mention matching.
		for _, tok := range tokensFromElementID(e.GetId()) {
			i.elementsByTokens[strings.ToLower(tok)] = append(i.elementsByTokens[strings.ToLower(tok)], e)
		}
		// Track protocol -> methods. Methods are stored as separate
		// elements with IDs "lib/Protocol.Method".
		if e.GetKind() == contractpb.ContractElementKind_METHOD {
			if proto := protocolOfMethodID(e.GetId()); proto != "" {
				i.methodsByProtocol[proto] = append(i.methodsByProtocol[proto], e)
			}
		}
		// IMPLEMENTS edges.
		for _, r := range e.GetRelationships() {
			if r.GetKind() == contractpb.RelationshipKind_IMPLEMENTS {
				i.implementorsByFIDL[r.GetTargetElementId()] = append(
					i.implementorsByFIDL[r.GetTargetElementId()], e.GetId())
			}
		}
	}

	// Second pass: compute transitive composes for each protocol.
	for _, e := range i.c.Elements() {
		if e.GetKind() != contractpb.ContractElementKind_PROTOCOL {
			continue
		}
		i.composesByProtocol[e.GetId()] = i.transitiveComposes(e.GetId(), make(map[string]bool))
	}

	// Third pass: synthesize INHERITED_FROM on methods that come via
	// composition. We don't mutate the on-disk element list; we
	// materialize new METHOD elements representing the surface-exposed
	// view of inherited methods.
	//
	// Iterate protocols in sorted ID order, NOT raw map order: this pass
	// mutates methodsByProtocol/elemByID/elementsByTokens as it synthesizes,
	// so a non-deterministic range order made the synthesized inherited
	// surface vary run-to-run — which flipped inherited-doc attribution for
	// same-named methods (e.g. fuchsia.io/{Directory,File,Symlink}.Close all
	// inherit Close from a composed base). Sorted iteration is reproducible.
	protoIDs := make([]string, 0, len(i.composesByProtocol))
	for protoID := range i.composesByProtocol {
		protoIDs = append(protoIDs, protoID)
	}
	sort.Strings(protoIDs)
	for _, protoID := range protoIDs {
		parents := i.composesByProtocol[protoID]
		for _, parent := range parents {
			for _, m := range i.methodsByProtocol[parent] {
				surfaceID := protoID + "." + methodNameOnly(m.GetId())
				if _, exists := i.elemByID[surfaceID]; exists {
					continue // already declared directly on the child
				}
				// Synthesize a surface-exposed method element.
				inherited := cloneMethodWithNewOwner(m, protoID, surfaceID)
				i.elemByID[surfaceID] = inherited
				i.c.AddElement(inherited)
				i.methodsByProtocol[protoID] = append(i.methodsByProtocol[protoID], inherited)
				for _, tok := range tokensFromElementID(inherited.GetId()) {
					i.elementsByTokens[strings.ToLower(tok)] = append(
						i.elementsByTokens[strings.ToLower(tok)], inherited)
				}
				i.elementsByLib[inherited.GetLibrary()] = append(
					i.elementsByLib[inherited.GetLibrary()], inherited)
			}
		}
	}
}

func (i *Indexer) transitiveComposes(protoID string, seen map[string]bool) []string {
	if seen[protoID] {
		return nil
	}
	seen[protoID] = true
	e := i.elemByID[protoID]
	if e == nil {
		return nil
	}
	var out []string
	for _, r := range e.GetRelationships() {
		if r.GetKind() != contractpb.RelationshipKind_COMPOSED_FROM {
			continue
		}
		parent := r.GetTargetElementId()
		if seen[parent] {
			continue
		}
		out = append(out, parent)
		out = append(out, i.transitiveComposes(parent, seen)...)
	}
	return out
}

// --- per-element profile construction ---

func (i *Indexer) buildProfile(e *contractpb.ContractElement) *coveragepb.CoverageProfile {
	prof := &coveragepb.CoverageProfile{
		ElementId: e.GetId(),
		Docs:      &coveragepb.DocCoverage{Reference: &coveragepb.DocCoverage_Reference{}},
		Tests:     &coveragepb.TestCoverage{},
		Examples:  &coveragepb.ExampleCoverage{},
		Usage:     &coveragepb.UsageCoverage{},
	}

	// LIBRARY-kind elements are metadata-only carriers (they hold the
	// library-level @available annotation). They are not real surface;
	// attributing tests/docs to them via the heuristic matcher would
	// always over-count (every test in the library's directory matches
	// the library name). Skip attribution entirely; the LIBRARY element
	// remains in the corpus but its profile is empty.
	if e.GetKind() == contractpb.ContractElementKind_LIBRARY {
		prof.GapsSummary = i.computeGaps(e, prof)
		return prof
	}

	// --- Implementations surface (interface kinds only) ---
	//
	// Populate the implementations surface from i.implementorsByFIDL
	// for interface kinds. This replaces the implements-map attribution
	// path that used to live in testCaseRefsElement: tests of impl
	// classes attribute to the impl element directly (CPP_CLASS /
	// RUST_TYPE elements get their own tests), and the FIDL/proto
	// interface element renders its IMPLEMENTS edges as a first-class
	// surface here.
	if isInterfaceKind(e.GetKind()) {
		implIDs := i.implementorsByFIDL[e.GetId()]
		if len(implIDs) > 0 {
			sorted := append([]string(nil), implIDs...)
			sort.Strings(sorted)
			prof.Implementations = &coveragepb.ImplementationCoverage{}
			for _, implID := range sorted {
				ref := &coveragepb.ImplementationRef{ImplElementId: implID}
				if impl := i.elemByID[implID]; impl != nil {
					ref.ImplKind = impl.GetKind().String()
					ref.Path = impl.GetLocation().GetPath()
					ref.Line = impl.GetLocation().GetLine()
				}
				prof.Implementations.Impls = append(prof.Implementations.Impls, ref)
			}
		}
	}

	// --- Docs ---
	for _, dc := range i.c.DocClaims() {
		if !i.docClaimRefsElement(dc, e) {
			continue
		}
		ref := docClaimToRef(dc)
		// Bucket by Kind and category.
		category := i.docClaimCategory(dc)
		i.placeDocRef(prof, dc, ref, category)
	}

	// --- Tests ---
	for _, tc := range i.c.Tests() {
		if !i.testCaseRefsElement(tc, e) {
			continue
		}
		tref := &commonpb.TestRef{
			Path:      tc.GetLocation().GetPath(),
			Line:      tc.GetLocation().GetLine(),
			TestName:  tc.GetId(),
			Framework: tc.GetFramework(),
			Exercises: e.GetId(),
		}
		category := i.testCategory(tc)
		i.placeTestRef(prof, tc, tref, category)
	}

	// --- Gaps summary ---
	prof.GapsSummary = i.computeGaps(e, prof)
	return prof
}

// docClaimRefsElement returns true if the doc claim references the
// element directly via ContractRefs.
func (i *Indexer) docClaimRefsElement(dc *docclaimpb.DocClaim, e *contractpb.ContractElement) bool {
	// Collect candidate IDs that should be considered "the same
	// element" for doc-attribution purposes:
	//   - the element's canonical ID
	//   - the IDs of any INHERITED_FROM source(s) (so a method
	//     surfaced via `compose` inherits the original's doc claim;
	//     without this, Flatland.ReleaseImageImmediately ends up
	//     undocumented even though TrustedFlatland.ReleaseImageImmediately
	//     IS documented in the FIDL source).
	candidates := []string{e.GetId()}
	for _, rel := range e.GetRelationships() {
		if rel.GetKind() == contractpb.RelationshipKind_INHERITED_FROM {
			candidates = append(candidates, rel.GetTargetElementId())
		}
	}
	for _, ref := range dc.GetContractRefs() {
		for _, cand := range candidates {
			if ref == cand {
				return true
			}
			// Fuzzy: a claim mentioning "Directory.Open" without library
			// prefix should still match "fuchsia.io/Directory.Open".
			if strings.HasSuffix(cand, "/"+ref) || strings.HasSuffix(cand, "."+ref) {
				return true
			}
		}
		// Aliases: ecosystems that conventionally use a different
		// notation than the canonical slash-form (e.g. proto/gRPC
		// docs say `grpc.health.v1.Health.Check`, but the element ID
		// is `grpc.health.v1/Health.Check`) emit those alt forms as
		// aliases so the doc-mention matcher can join them.
		for _, alias := range e.GetAliases() {
			if ref == alias {
				return true
			}
		}
	}
	return false
}

// testCaseRefsElement returns true if the test references the element.
// Two matching strategies in order of confidence:
//  1. Direct contract_refs entry — the test's ref string equals the
//     element's canonical ID, an alias, or matches the wildcard
//     fan-out form lib/*.Method.
//  3. Name-token overlap — every CamelCase-tokenized piece of the
//     element's local name must be present in the test's name or path
//     tokens. Admitted per kind via policy.go::kindStrategies; only
//     SUBCOMMAND / LIBRARY / CONFIG_FACET admit it under default
//     policy. Fine-grained CLI kinds and all interface kinds reject
//     this strategy outright.
//
// Strategy 2 (implements-map) was removed. The implements-
// map heuristic produced 57–100% FP on three Fuchsia FIDL example
// reports and was demoted from a coverage strategy to a relationship-
// only data source. Interface elements (METHOD / TYPE / PROTOCOL /
// SYSCALL) now render their IMPLEMENTS edges via the `implementations`
// surface populated in buildProfile, not via test attribution. The
// `i.implementorsByFIDL` map is retained and continues to feed the
// implementations surface; only the matcher's consumption of it is
// gone.
//
// On strategy 3: both protocol and method names are split with
// splitCamelCase (same way test names are tokenized) and ALL
// resulting tokens must appear in the test's token set. So for the
// method "Scenic.TakeScreenshot" we need {"scenic","take","screenshot"}
// all present. A test named "ScreenshotTest.TakeScreenshotReturnsImage"
// in src/ui/scenic/tests/foo_test.cc has tokens {"screenshot","test",
// "take","screenshot","returns","image","scenic","ui","foo","tests"} →
// match. A random test mentioning "Snapshot" without "scenic" no
// longer counts toward fuchsia.ui.scenic/Snapshot's coverage.
func (i *Indexer) testCaseRefsElement(tc *testcasepb.TestCase, e *contractpb.ContractElement) bool {
	// Strategy 1: direct ref against the canonical ID OR any alias.
	// Also honors fidlmatch's wildcard fan-out form "lib/*.Method"
	// (fix #3): when a test references a method through a semantic
	// variable name (e.g. parent_session_->SetInfiniteHitRegion()),
	// the matcher can't infer the protocol — so it emits a wildcard
	// candidate, and we fan out to any element here whose library
	// matches and whose ID ends in ".<Method>".
	for _, ref := range tc.GetContractRefs() {
		if ref == e.GetId() {
			return true
		}
		for _, alias := range e.GetAliases() {
			if ref == alias {
				return true
			}
		}
		if wildcardRefMatches(ref, e) {
			return true
		}
	}

	// Per-kind strategy admission policy. The default table in
	// policy.go declares, for each ContractElementKind, the ordered
	// list of strategies the matcher is allowed to use. Fine-grained
	// kinds (FLAG / SWITCH / CONFIG_KNOB / METHOD / TYPE / PROTOCOL /
	// POSITIONAL / CPP_CLASS / RUST_TYPE) admit Strategy 1 (direct
	// refs) only. Coarse kinds (SUBCOMMAND / LIBRARY / CONFIG_FACET)
	// also admit Strategy 3 (name-token fallback).
	//
	// The Strategy 2 (implements-map) branch that previously lived
	// here was removed. The implements-map heuristic produced 57–100%
	// FP across three Fuchsia FIDL example reports. It was
	// demoted from a coverage strategy to a relationship-only data
	// source. Interface elements (METHOD / TYPE / PROTOCOL / SYSCALL)
	// now render their IMPLEMENTS edges via the `implementations`
	// surface, populated in buildProfile from i.implementorsByFIDL.
	// Tests of impl classes attribute to the impl element directly
	// via Strategy 1, with cross-links from the interface element's
	// coverage page to each impl's coverage page.
	//
	// The future `conformance_test_globs` opt-in (see design doc §8)
	// will let projects declare specific test paths whose Strategy 1
	// matches populate the tests surface on interface elements. v1
	// ships the field empty in every example config; the hook is a
	// no-op in v1.
	if !admitsStrategy(e.GetKind(), StrategyNameTokens) {
		return false
	}

	// Strategy 3: name-token overlap on the element ID. Build the
	// test's full token set (test-name tokens + source-path tokens),
	// then require every CamelCase-split token of the element's
	// identifier to appear. Path tokens are tracked separately so
	// the CLI-shape rule can require subcommand-name presence in the
	// path specifically (kills false positives from Go's runFoo
	// naming convention).
	//
	// Tokens are expanded with simple trailing-s stemming (Fix C):
	// inserting "level" also inserts "levels", and inserting "levels"
	// also inserts "level". That lets element IDs like
	// "docker --log-level" match test names like "TestLogLevels".
	// Short tokens (<=3 chars) and sibilant-ending words ("class")
	// are excluded to avoid bad stems like "ps" → "p".
	nameTokens := make(map[string]bool, len(tc.GetNameTokens())*2)
	for _, t := range tc.GetNameTokens() {
		addWithStemBlocked(nameTokens, strings.ToLower(t), i.ambiguousLastTokens)
	}
	pathTokens := make(map[string]bool, 16)
	for _, t := range tokensFromPath(tc.GetLocation().GetPath()) {
		addWithStemBlocked(pathTokens, strings.ToLower(t), i.ambiguousLastTokens)
	}
	testTokens := make(map[string]bool, len(nameTokens)+len(pathTokens))
	for k := range nameTokens {
		testTokens[k] = true
	}
	for k := range pathTokens {
		testTokens[k] = true
	}
	// testTokensRaw is the un-stemmed token set, used by the
	// proto-METHOD leaf-exact rule below. Cheap to build separately:
	// re-process the same inputs without addWithStemBlocked's expansion.
	testTokensRaw := make(map[string]bool, len(nameTokens))
	for _, t := range tc.GetNameTokens() {
		testTokensRaw[strings.ToLower(t)] = true
	}
	for _, t := range tokensFromPath(tc.GetLocation().GetPath()) {
		testTokensRaw[strings.ToLower(t)] = true
	}

	allPresent := func(tokens []string) bool {
		if len(tokens) == 0 {
			return false
		}
		for _, t := range tokens {
			if !testTokens[t] {
				return false
			}
		}
		return true
	}

	if e.GetKind() == contractpb.ContractElementKind_METHOD {
		proto, method := splitMethodID(e.GetId())
		if proto == "" || method == "" {
			return false
		}
		methodTokens := splitCamelCase(method)
		if !allPresent(splitCamelCase(proto)) || !allPresent(methodTokens) {
			return false
		}
		// Proto-METHOD leaf-exact rule: protobuf/gRPC services don't
		// conventionally mix singular and plural sibling rpcs (no
		// Foo / Foos pair) the way Fuchsia FIDL might. So for proto
		// methods, demand the LAST CamelCase token of the rpc name
		// match the test's tokens exactly — no trailing-s stemming.
		// Belt-and-suspenders alongside ambiguousLastTokens: catches
		// the cases where a plural sibling doesn't exist (so the
		// ambiguous-stem suppression doesn't fire) but the test name
		// still uses a plural that would otherwise stem-match.
		if e.GetEcosystem() == "proto" && len(methodTokens) > 0 {
			leaf := strings.ToLower(methodTokens[len(methodTokens)-1])
			if !testTokensRaw[leaf] {
				return false
			}
		}
		return true
	}
	// Root SUBCOMMAND guard: a SUBCOMMAND element with no space in
	// its ID is the root binary itself (e.g. "kubectl", "sheaf", "fd").
	// Without this guard, the TYPE-fallback path below attributes
	// EVERY test whose path contains the binary-name token to the
	// root — so every test under `staging/src/k8s.io/kubectl/...`
	// would attribute to `kubectl`. Root binaries should only be
	// attributed via Strategy 1 (direct ref to the binary), which
	// fires from `exec.Command("kubectl", ...)` style invocations.
	if e.GetKind() == contractpb.ContractElementKind_SUBCOMMAND &&
		!strings.Contains(e.GetId(), " ") {
		return false
	}
	// Multi-word SUBCOMMAND (e.g. "kubectl describe") still uses
	// cliShapeMatch below. A direct-evidence-only guard for these
	// elements is the next change — held back because gotest doesn't
	// yet detect every cobra-invocation idiom (sheaf-self's tests
	// call `runScan(...)` directly, no `NewCmdX` / `exec.Command` /
	// SetArgs). Pending the broader gotest pattern set, see the
	// validation log.

	// CLI-shaped element IDs use space separators (cobra emits
	// "docker container run", argh emits "ffx component show"). Try
	// the canonical ID, then each alias — but only aliases that are
	// at least as specific as the canonical (≥ as many tokens).
	// Skipping shorter-form aliases (e.g. "docker run" as alias of
	// "docker container run") prevents single-token aliases from
	// pulling in unrelated tests just because a path token happens
	// to match.
	if strings.Contains(e.GetId(), " ") {
		canonTokens := strings.Count(e.GetId(), " ")
		candidates := append([]string{e.GetId()}, e.GetAliases()...)
		for _, candidate := range candidates {
			if strings.Count(candidate, " ") < canonTokens {
				continue
			}
			if cliShapeMatch(candidate, e.GetLibrary(), testTokens, pathTokens, tc.GetLocation().GetPath()) {
				return true
			}
		}
		return false
	}
	// Protocols / types: require every CamelCase token of the local
	// name to appear. For single-word names this matches the old
	// behavior; for multi-word names (e.g. "SessionListener") it now
	// correctly requires both halves rather than the impossible
	// concatenation.
	localTokens := splitCamelCase(lastSegment(e.GetId()))
	if !allPresent(localTokens) {
		return false
	}
	distinctive := libraryDistinctiveToken(e.GetLibrary())

	// Fix 2 (PROTOCOL-leaf-equals-library-distinctive guard): when a
	// PROTOCOL element's local name is a single token AND that token
	// equals the library's distinctive segment, name-token attribution
	// alone is hopeless — every test mentioning "channelz" anywhere
	// will match `grpc.channelz.v1/Channelz`, including v2 tests,
	// infrastructure tests, and unrelated subsystems. Require body-ref
	// (Strategy 1) or implements-map (Strategy 2) evidence; refuse the
	// name-token path here.
	if e.GetKind() == contractpb.ContractElementKind_PROTOCOL &&
		len(localTokens) == 1 && distinctive != "" && localTokens[0] == distinctive {
		return false
	}

	// Noisy-words guard. Single-word common English names (Snapshot,
	// Command, Event, File, etc.) over-match against any test that
	// happens to mention the word in any context. Tighten by also
	// requiring a token from the library name itself. Generalized
	// (Fix 3): also fires for multi-token leaves where ANY constituent
	// is in the noisy set — e.g. `ServerData` ([server,data]) has
	// "server" in the noisy list, so attribution requires the library
	// distinctive token too. Catches the multi-token-bypass class of
	// false positives (ChannelTrace, ServerData, SocketData, …) that
	// the original len==1 gate left open.
	hasNoisyToken := false
	for _, t := range localTokens {
		if i.commonNoisyWords[t] {
			hasNoisyToken = true
			break
		}
	}
	if hasNoisyToken {
		if distinctive == "" {
			return true // can't tighten without a library name
		}
		if !testTokens[distinctive] {
			return false
		}
	}

	// Fix 4 (prefix-collision guard): when a single-token leaf is a
	// strict CamelCase-prefix of a multi-token sibling leaf in the
	// same library (Channel ⊂ ChannelTrace, Subchannel ⊂
	// SubchannelRef, …), and the SAME test would also match the
	// longer sibling via name-token overlap, refuse the short-leaf
	// attribution. The more-specific sibling wins. Tests that have
	// the short token but NOT any longer-sibling token still
	// attribute to the short leaf (e.g. `GetServer` body refs land
	// on the Server type when the test has no "data"/"ref" tokens).
	if len(localTokens) == 1 {
		if libCollisions := i.prefixCollisionSiblings[e.GetLibrary()]; libCollisions != nil {
			if siblings := libCollisions[localTokens[0]]; siblings != nil {
				for _, siblingTokens := range siblings {
					if allPresent(siblingTokens) {
						return false
					}
				}
			}
		}
	}
	return true
}

// implMentionMatch returns true if the test's name tokens or path
// tokens contain at least one CamelCase-split token of the impl
// element's local name.
//
// Currently unused by the default matcher (Strategy 2 / implements-map
// was removed). Retained for the v2 `conformance_test_globs` opt-in:
// the conformance path may reuse this helper as the
// final disambiguator when a conformance test path matches multiple
// impl-class candidates for the same FIDL element.
//
//nolint:unused // retained for v2 conformance-test path
func implMentionMatch(impl *contractpb.ContractElement, tc *testcasepb.TestCase) bool {
	implLocal := implLocalName(impl.GetId())
	implTokens := splitCamelCase(implLocal)
	if len(implTokens) == 0 {
		return false
	}
	// The impl class's lowercase tokens must appear in some
	// combination in the test's name/path. We accept any of:
	//   - joined form ("FileConnection" → "fileconnection") substring
	//     in the combined name + path haystack
	//   - any single impl-token appearing in the test's NameTokens
	//     (the legacy "any one token" rule, kept because fuchsia.io's
	//     tests routinely name themselves "FileTest.X" while the impl
	//     class is "FileConnection" — only the "file" token overlaps).
	//
	// The single-token-loose rule does admit FPs of the
	// "NodeRemovalTrackerTest attributing to NodeController via impl
	// Node" shape; those still need a finer-grained guard (matching
	// the impl class to a specific CamelCase prefix of the test
	// suite name) that integration tests do not yet cover. Document
	// it as a known wart rather than break the load-bearing case.
	nameLower := strings.ToLower(tc.GetName())
	pathLower := strings.ToLower(tc.GetLocation().GetPath())
	joined := strings.Join(implTokens, "")
	if strings.Contains(nameLower+" "+pathLower, joined) {
		return true
	}
	for _, t := range implTokens {
		if t == "" {
			continue
		}
		if strings.Contains(nameLower, t) || strings.Contains(pathLower, t) {
			return true
		}
		for _, nt := range tc.GetNameTokens() {
			if strings.ToLower(nt) == t {
				return true
			}
		}
	}
	return false
}

// libraryDistinctiveToken returns the most distinctive lowercase
// segment of a library name (last non-version segment), or empty if
// the library is empty or consists entirely of version-style markers.
// "grpc.channelz.v1"  → "channelz"
// "fuchsia.ui.scenic" → "scenic"
// "v1"                → "" (entirely version)
// ""                  → ""
func libraryDistinctiveToken(lib string) string {
	if lib == "" {
		return ""
	}
	segs := strings.FieldsFunc(lib, func(r rune) bool { return r == '.' || r == '/' || r == '-' })
	for i := len(segs) - 1; i >= 0; i-- {
		s := strings.ToLower(segs[i])
		if isVersionSegment(s) {
			continue
		}
		return s
	}
	return ""
}

// defaultNoisyWords is the built-in baseline set of element-name
// words that double as common English nouns/verbs. When an element's
// local name is exactly one of these, the matcher additionally
// requires the library's distinctive token (last dotted segment) in
// the test's token set. Keeps "Snapshot" from matching every test
// mentioning "snapshot" in any context.
//
// Callers can extend this per-project via Options.NoisyWords —
// useful for ecosystems with their own bag of generic identifiers
// (gRPC might add "service"/"request"; OpenAPI "path"/"response").
// computePrefixCollisions scans every element's leaf CamelCase
// token sequence and indexes single-token leaves whose token is the
// first token of a multi-token sibling leaf in the same library
// (e.g. Channel [channel] vs ChannelTrace [channel, trace]). The
// resulting map is consulted by testCaseRefsElement to prefer the
// more-specific sibling when both would attribute to the same test.
func (i *Indexer) computePrefixCollisions() {
	// Group leaf-token-sequences per library.
	leavesByLib := make(map[string][][]string, len(i.elementsByLib))
	for _, e := range i.elemByID {
		tokens := splitCamelCase(lastSegment(e.GetId()))
		if len(tokens) == 0 {
			continue
		}
		lib := e.GetLibrary()
		// Lowercase for comparison consistency with testTokens.
		lc := make([]string, len(tokens))
		for j, t := range tokens {
			lc[j] = strings.ToLower(t)
		}
		leavesByLib[lib] = append(leavesByLib[lib], lc)
	}
	out := make(map[string]map[string][][]string)
	for lib, leaves := range leavesByLib {
		// Partition into single-token leaves (potential "victims" of
		// prefix-collision) and multi-token leaves keyed by their
		// first token (potential "more-specific" matches).
		singles := make(map[string]bool)
		multisByFirst := make(map[string][][]string)
		for _, ts := range leaves {
			if len(ts) == 1 {
				singles[ts[0]] = true
			} else {
				multisByFirst[ts[0]] = append(multisByFirst[ts[0]], ts)
			}
		}
		libCollisions := make(map[string][][]string)
		for s := range singles {
			if multis := multisByFirst[s]; len(multis) > 0 {
				libCollisions[s] = multis
			}
		}
		if len(libCollisions) > 0 {
			out[lib] = libCollisions
		}
	}
	i.prefixCollisionSiblings = out
}

// resolveSameAs runs the codegen_bridge resolver against the corpus's
// post-lookup-build element set and stitches the resulting SAME_AS
// edges in two places:
//
//  1. Each element's Relationships gets a SAME_AS Relationship entry
//     pointing at its sibling. Both directions are written so the
//     existing relationship-rendering path (MCP, report HTML) surfaces
//     the link from either side without directionality logic.
//
//  2. The sameAsSiblings adjacency map gets populated for use by
//     buildProfile's evidence-union step.
//
// No-op when no bridges are configured.
func (i *Indexer) resolveSameAs() {
	if len(i.bridges) == 0 {
		return
	}
	edges := Resolve(i.bridges, i.c.Elements())
	if len(edges) == 0 {
		return
	}
	// De-dup against any SAME_AS edges that already exist on the
	// elements (e.g. emitted directly by an adapter). The same
	// (source, target) tuple shouldn't appear twice.
	hasEdge := func(e *contractpb.ContractElement, targetID string) bool {
		for _, r := range e.GetRelationships() {
			if r.GetKind() == contractpb.RelationshipKind_SAME_AS &&
				r.GetTargetElementId() == targetID {
				return true
			}
		}
		return false
	}
	for _, ed := range edges {
		src := i.elemByID[ed.SourceID]
		tgt := i.elemByID[ed.TargetID]
		if src == nil || tgt == nil {
			continue
		}
		if !hasEdge(src, ed.TargetID) {
			src.Relationships = append(src.Relationships, &contractpb.Relationship{
				Kind:            contractpb.RelationshipKind_SAME_AS,
				TargetElementId: ed.TargetID,
				Note:            "codegen_bridge",
			})
		}
		if !hasEdge(tgt, ed.SourceID) {
			tgt.Relationships = append(tgt.Relationships, &contractpb.Relationship{
				Kind:            contractpb.RelationshipKind_SAME_AS,
				TargetElementId: ed.SourceID,
				Note:            "codegen_bridge",
			})
		}
		i.sameAsSiblings[ed.SourceID] = appendUnique(i.sameAsSiblings[ed.SourceID], ed.TargetID)
		i.sameAsSiblings[ed.TargetID] = appendUnique(i.sameAsSiblings[ed.TargetID], ed.SourceID)
	}
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// aggregateSameAs walks SAME_AS sibling adjacency and unions evidence
// (tests, docs, examples, implementations) from each sibling's
// CoverageProfile onto this element's profile. Direct neighbors only;
// transitive aggregation is not performed (A↔B↔C does not pull C's
// evidence onto A).
//
// Run AFTER all per-element profiles have been built so each sibling's
// profile is populated with its own direct attributions. Idempotent
// is not guaranteed — call exactly once per Build pass.
func (i *Indexer) aggregateSameAs() {
	if len(i.sameAsSiblings) == 0 {
		return
	}
	// Snapshot the pre-aggregation profiles for every sibling we'll
	// read from. Without snapshotting, bidirectional SAME_AS edges
	// double-count: appending A's evidence into B and then reading
	// B (now containing A's evidence) when aggregating onto A causes
	// each ref to land on A twice. Snapshots use the original (pre-
	// union) slice headers so we always source from the directly-
	// attributed evidence only.
	snapshot := make(map[string]*coveragepb.CoverageProfile, len(i.sameAsSiblings))
	for elemID, siblings := range i.sameAsSiblings {
		for _, sibID := range siblings {
			if _, ok := snapshot[sibID]; ok {
				continue
			}
			if p := i.c.Profile(sibID); p != nil {
				snapshot[sibID] = cloneProfileShallow(p)
			}
		}
		_ = elemID
	}
	for elemID, siblings := range i.sameAsSiblings {
		prof := i.c.Profile(elemID)
		if prof == nil {
			continue
		}
		for _, sibID := range siblings {
			sibProf := snapshot[sibID]
			if sibProf == nil {
				continue
			}
			unionTestCoverage(prof.GetTests(), sibProf.GetTests())
			unionDocCoverage(prof.GetDocs(), sibProf.GetDocs())
			unionExampleCoverage(prof.GetExamples(), sibProf.GetExamples())
			unionImplementationCoverage(prof, sibProf)
		}
	}
}

// cloneProfileShallow returns a profile carrying the same evidence
// slice contents as the original but with fresh outer struct +
// surface-message wrappers. The underlying *commonpb.TestRef /
// *commonpb.DocRef pointers are shared — we never mutate per-ref
// contents, only the slice they sit in.
func cloneProfileShallow(p *coveragepb.CoverageProfile) *coveragepb.CoverageProfile {
	out := &coveragepb.CoverageProfile{ElementId: p.GetElementId()}
	if t := p.GetTests(); t != nil {
		out.Tests = &coveragepb.TestCoverage{
			Unit:        append([]*commonpb.TestRef(nil), t.Unit...),
			Integration: append([]*commonpb.TestRef(nil), t.Integration...),
			E2E:         append([]*commonpb.TestRef(nil), t.E2E...),
			Ctf:         append([]*commonpb.TestRef(nil), t.Ctf...),
			Performance: append([]*commonpb.TestRef(nil), t.Performance...),
			Fuzz:        append([]*commonpb.TestRef(nil), t.Fuzz...),
			Golden:      append([]*commonpb.TestRef(nil), t.Golden...),
		}
	}
	if d := p.GetDocs(); d != nil {
		out.Docs = &coveragepb.DocCoverage{
			Concept:      append([]*commonpb.DocRef(nil), d.Concept...),
			Tutorial:     append([]*commonpb.DocRef(nil), d.Tutorial...),
			ReleaseNotes: append([]*commonpb.DocRef(nil), d.ReleaseNotes...),
			Faq:          append([]*commonpb.DocRef(nil), d.Faq...),
		}
		if d.Reference != nil {
			out.Docs.Reference = &coveragepb.DocCoverage_Reference{
				Fidldoc: append([]*commonpb.DocRef(nil), d.Reference.Fidldoc...),
				Clidoc:  append([]*commonpb.DocRef(nil), d.Reference.Clidoc...),
			}
			if len(d.Reference.ByAdapter) > 0 {
				out.Docs.Reference.ByAdapter = make(map[string]*coveragepb.DocCoverage_DocRefList, len(d.Reference.ByAdapter))
				for k, v := range d.Reference.ByAdapter {
					out.Docs.Reference.ByAdapter[k] = &coveragepb.DocCoverage_DocRefList{
						Refs: append([]*commonpb.DocRef(nil), v.GetRefs()...),
					}
				}
			}
		}
		if d.Guide != nil {
			out.Docs.Guide = &coveragepb.DocCoverage_Guide{
				Migration:       append([]*commonpb.DocRef(nil), d.Guide.Migration...),
				Troubleshooting: append([]*commonpb.DocRef(nil), d.Guide.Troubleshooting...),
				Cookbook:        append([]*commonpb.DocRef(nil), d.Guide.Cookbook...),
			}
		}
		if d.Proposal != nil {
			out.Docs.Proposal = &coveragepb.DocCoverage_Proposal{
				Rfc:    append([]*commonpb.DocRef(nil), d.Proposal.Rfc...),
				Design: append([]*commonpb.DocRef(nil), d.Proposal.Design...),
			}
		}
	}
	if e := p.GetExamples(); e != nil {
		out.Examples = &coveragepb.ExampleCoverage{
			InTree:   append([]*commonpb.CodeRef(nil), e.InTree...),
			InDocs:   append([]*commonpb.CodeRef(nil), e.InDocs...),
			External: append([]*commonpb.CodeRef(nil), e.External...),
		}
	}
	if im := p.GetImplementations(); im != nil {
		out.Implementations = &coveragepb.ImplementationCoverage{
			Impls: append([]*coveragepb.ImplementationRef(nil), im.Impls...),
		}
	}
	return out
}

func unionTestCoverage(dst, src *coveragepb.TestCoverage) {
	if dst == nil || src == nil {
		return
	}
	dst.Unit = append(dst.Unit, src.Unit...)
	dst.Integration = append(dst.Integration, src.Integration...)
	dst.E2E = append(dst.E2E, src.E2E...)
	dst.Ctf = append(dst.Ctf, src.Ctf...)
	dst.Performance = append(dst.Performance, src.Performance...)
	dst.Fuzz = append(dst.Fuzz, src.Fuzz...)
	dst.Golden = append(dst.Golden, src.Golden...)
}

func unionDocCoverage(dst, src *coveragepb.DocCoverage) {
	if dst == nil || src == nil {
		return
	}
	if src.Reference != nil {
		if dst.Reference == nil {
			dst.Reference = &coveragepb.DocCoverage_Reference{}
		}
		refs.UnionReference(dst.Reference, src.Reference)
	}
	dst.Concept = append(dst.Concept, src.Concept...)
	dst.Tutorial = append(dst.Tutorial, src.Tutorial...)
	dst.ReleaseNotes = append(dst.ReleaseNotes, src.ReleaseNotes...)
	dst.Faq = append(dst.Faq, src.Faq...)
	if src.Guide != nil {
		if dst.Guide == nil {
			dst.Guide = &coveragepb.DocCoverage_Guide{}
		}
		dst.Guide.Migration = append(dst.Guide.Migration, src.Guide.Migration...)
		dst.Guide.Troubleshooting = append(dst.Guide.Troubleshooting, src.Guide.Troubleshooting...)
		dst.Guide.Cookbook = append(dst.Guide.Cookbook, src.Guide.Cookbook...)
	}
	if src.Proposal != nil {
		if dst.Proposal == nil {
			dst.Proposal = &coveragepb.DocCoverage_Proposal{}
		}
		dst.Proposal.Rfc = append(dst.Proposal.Rfc, src.Proposal.Rfc...)
		dst.Proposal.Design = append(dst.Proposal.Design, src.Proposal.Design...)
	}
}

func unionExampleCoverage(dst, src *coveragepb.ExampleCoverage) {
	if dst == nil || src == nil {
		return
	}
	dst.InTree = append(dst.InTree, src.InTree...)
	dst.InDocs = append(dst.InDocs, src.InDocs...)
	dst.External = append(dst.External, src.External...)
}

func unionImplementationCoverage(dst, src *coveragepb.CoverageProfile) {
	if src.GetImplementations() == nil || len(src.GetImplementations().GetImpls()) == 0 {
		return
	}
	if dst.Implementations == nil {
		dst.Implementations = &coveragepb.ImplementationCoverage{}
	}
	dst.Implementations.Impls = append(dst.Implementations.Impls, src.GetImplementations().GetImpls()...)
}

// isVersionSegment recognizes proto-style version markers as the
// trailing segment of a library/package name: "v1", "v2", "v1alpha",
// "v2beta1", "v3p7" — anything starting with `v` followed by a digit
// and optional [a-z0-9]+ tail. These segments never appear in test
// names, so they're useless as the "distinctive" library token for
// the noisy_words tightening rule above.
func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	// Second char must be a digit.
	if s[1] < '0' || s[1] > '9' {
		return false
	}
	for i := 2; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

var defaultNoisyWords = map[string]bool{
	"command": true, "event": true, "snapshot": true, "file": true,
	"node": true, "session": true, "channel": true, "socket": true,
	"port": true, "process": true, "thread": true, "stream": true,
	"buffer": true, "memory": true, "handle": true, "task": true,
	"job": true, "queue": true, "list": true, "map": true, "set": true,
	"timer": true, "clock": true, "error": true, "result": true,
	"request": true, "response": true, "message": true, "frame": true,
	"image": true, "view": true, "scene": true,
	"client": true, "server": true, "connection": true, "endpoint": true,
}

// placeDocRef puts a doc ref in the right CoverageProfile bucket.
func (i *Indexer) placeDocRef(prof *coveragepb.CoverageProfile, dc *docclaimpb.DocClaim, ref *commonpb.DocRef, category string) {
	switch dc.GetKind() {
	case docclaimpb.DocClaimKind_REFERENCE:
		// "fidl" and "fidldoc" both target the typed Fidldoc bucket
		// (inline /// comments and the rendered bundle respectively).
		// Everything else — clidoc, markdowncli, and any future
		// rendered-reference adapter — routes through the centralized
		// helper, which keeps consumers from having to know the
		// per-adapter bucket layout.
		adapter := dc.GetAdapter()
		if adapter == "fidl" {
			adapter = "fidldoc"
		}
		refs.AppendRef(prof.Docs.Reference, adapter, ref)
	case docclaimpb.DocClaimKind_EXAMPLE:
		// The moat: LLM-tier EXAMPLE claims (from the usage classifier in
		// internal/workflowextract) are additive/flagged. The pass itself
		// routes them into ExampleCoverage.llm_inferred; this guard keeps a
		// re-index from leaking them into the deterministic in_docs surface
		// and inflating the trusted example count. Mirrors the WORKFLOW
		// guard below.
		if dc.GetProvenance().GetTier() == commonpb.RowProvenance_LLM {
			return
		}
		// Code-block claims from the markdown adapter (or any other doc
		// adapter that emits EXAMPLE-kind claims) populate the Examples
		// surface, not the Docs surface. Without this case they'd fall
		// through to Concept, which conflated doc presence with example
		// presence — and left the Example surface at 0% for everyone.
		prof.Examples.InDocs = append(prof.Examples.InDocs, &commonpb.CodeRef{
			Path:      ref.GetPath(),
			StartLine: ref.GetLine(),
			Intent:    "doc code block",
		})
	case docclaimpb.DocClaimKind_WORKFLOW:
		// The moat: LLM-tier WORKFLOW claims (from
		// internal/workflowextract) are additive/flagged and must NOT
		// inflate the deterministic workflow surface. They never enter a
		// coverage bucket — they live only in corpus.DocClaims(), where
		// internal/hardening reads them. Dropping them here (rather than
		// keying them into a separate Reference bucket) keeps them out of
		// the scanner's surface rollup, which walks every Reference key.
		// The deterministic `workflows`/`yaml-workflows` adapters carry
		// tier=DETERMINISTIC (or none) and are unaffected.
		if dc.GetProvenance().GetTier() == commonpb.RowProvenance_LLM {
			return
		}
		// WORKFLOW claims from the workflows adapter route into the
		// per-adapter Reference bucket under the "workflows" key — same
		// pipeline as markdowncli/clidoc, just a separately-keyed
		// bucket so the scanner can identify workflow claims and roll
		// them up into the masthead's workflow-coverage block. The
		// claim's ContractRefs are an ORDERED list (refs[0] first
		// command, refs[N-1] last); the reverse index has already
		// fanned out the claim to each individual element by the time
		// placeDocRef runs.
		refs.AppendRef(prof.Docs.Reference, "workflows", ref)
	default:
		// Use category to route prose mentions.
		// Section-excluded sentinel: the project declared a docs
		// category for this path but the claim sits inside a
		// section their config explicitly excluded (e.g. a flag
		// mention in a README's "Command-line options" table when
		// the docs.concepts block declares section_excludes:
		// "command-line options"). Drop from bucketing — falling
		// through to the default arm would put the claim into
		// Concept and silently bypass the project's filter.
		if category == sectionExcludedCategory {
			return
		}
		switch {
		case strings.HasPrefix(category, "docs.tutorial"):
			prof.Docs.Tutorial = append(prof.Docs.Tutorial, ref)
		case strings.HasPrefix(category, "docs.concept"):
			prof.Docs.Concept = append(prof.Docs.Concept, ref)
		case strings.HasPrefix(category, "docs.guide.migration"):
			if prof.Docs.Guide == nil {
				prof.Docs.Guide = &coveragepb.DocCoverage_Guide{}
			}
			prof.Docs.Guide.Migration = append(prof.Docs.Guide.Migration, ref)
		case strings.HasPrefix(category, "docs.guide.troubleshooting"):
			if prof.Docs.Guide == nil {
				prof.Docs.Guide = &coveragepb.DocCoverage_Guide{}
			}
			prof.Docs.Guide.Troubleshooting = append(prof.Docs.Guide.Troubleshooting, ref)
		case strings.HasPrefix(category, "docs.guide.cookbook"):
			if prof.Docs.Guide == nil {
				prof.Docs.Guide = &coveragepb.DocCoverage_Guide{}
			}
			prof.Docs.Guide.Cookbook = append(prof.Docs.Guide.Cookbook, ref)
		case strings.HasPrefix(category, "docs.proposal.rfc"):
			if prof.Docs.Proposal == nil {
				prof.Docs.Proposal = &coveragepb.DocCoverage_Proposal{}
			}
			prof.Docs.Proposal.Rfc = append(prof.Docs.Proposal.Rfc, ref)
		case strings.HasPrefix(category, "docs.proposal.design"):
			if prof.Docs.Proposal == nil {
				prof.Docs.Proposal = &coveragepb.DocCoverage_Proposal{}
			}
			prof.Docs.Proposal.Design = append(prof.Docs.Proposal.Design, ref)
		case strings.HasPrefix(category, "docs.release_notes"):
			prof.Docs.ReleaseNotes = append(prof.Docs.ReleaseNotes, ref)
		case strings.HasPrefix(category, "docs.faq"):
			prof.Docs.Faq = append(prof.Docs.Faq, ref)
		default:
			// Unbucketed prose mentions go into Concept as a sensible default.
			prof.Docs.Concept = append(prof.Docs.Concept, ref)
		}
	}
}

func (i *Indexer) placeTestRef(prof *coveragepb.CoverageProfile, tc *testcasepb.TestCase, ref *commonpb.TestRef, category string) {
	switch {
	case strings.HasPrefix(category, "tests.unit"):
		prof.Tests.Unit = append(prof.Tests.Unit, ref)
	case strings.HasPrefix(category, "tests.integration"):
		prof.Tests.Integration = append(prof.Tests.Integration, ref)
	case strings.HasPrefix(category, "tests.e2e"):
		prof.Tests.E2E = append(prof.Tests.E2E, ref)
	case strings.HasPrefix(category, "tests.ctf"):
		prof.Tests.Ctf = append(prof.Tests.Ctf, ref)
	case strings.HasPrefix(category, "tests.performance"):
		prof.Tests.Performance = append(prof.Tests.Performance, ref)
	case strings.HasPrefix(category, "tests.fuzz"):
		prof.Tests.Fuzz = append(prof.Tests.Fuzz, ref)
	case strings.HasPrefix(category, "tests.golden"):
		prof.Tests.Golden = append(prof.Tests.Golden, ref)
	default:
		// Unbucketed tests default to unit — safest assumption.
		prof.Tests.Unit = append(prof.Tests.Unit, ref)
	}
}

// sectionExcludedCategory is a sentinel value returned by
// docClaimCategory when the categorizer reports that at least one
// path-matching category was rejected by its section filter. The
// indexer treats it as "explicitly do not bucket this claim into
// the default Concept bin" — without it, the fallback path in
// placeDocRef would re-route filtered claims into Concept and the
// project's section_excludes intent would be silently bypassed.
const sectionExcludedCategory = "_section_excluded"

func (i *Indexer) docClaimCategory(dc *docclaimpb.DocClaim) string {
	if i.cat == nil {
		return ""
	}
	cats, sectionExcluded, err := i.cat.CategorizeWithDecision(dc.GetSourcePath(), dc.GetSectionPath())
	if err != nil {
		return ""
	}
	if len(cats) == 0 {
		if sectionExcluded {
			return sectionExcludedCategory
		}
		return ""
	}
	return cats[0]
}

func (i *Indexer) testCategory(tc *testcasepb.TestCase) string {
	if i.cat == nil {
		return ""
	}
	cats, err := i.cat.Categorize(tc.GetLocation().GetPath(), nil)
	if err != nil || len(cats) == 0 {
		return ""
	}
	return cats[0]
}

// computeGaps inspects which categories are empty or thin.
func (i *Indexer) computeGaps(e *contractpb.ContractElement, prof *coveragepb.CoverageProfile) *coveragepb.GapsSummary {
	g := &coveragepb.GapsSummary{}
	hasDoc := refs.HasReferenceDocs(prof.Docs.Reference) ||
		len(prof.Docs.Concept) > 0 ||
		len(prof.Docs.Tutorial) > 0 ||
		(prof.Docs.Guide != nil && (len(prof.Docs.Guide.Migration)+len(prof.Docs.Guide.Troubleshooting)+len(prof.Docs.Guide.Cookbook) > 0))
	if !hasDoc {
		g.Missing = append(g.Missing, "docs.reference")
	}
	if len(prof.Tests.Unit)+len(prof.Tests.Integration)+len(prof.Tests.E2E)+
		len(prof.Tests.Ctf)+len(prof.Tests.Performance)+len(prof.Tests.Fuzz)+
		len(prof.Tests.Golden) == 0 {
		g.Missing = append(g.Missing, "tests")
	}
	if len(prof.Examples.InTree)+len(prof.Examples.InDocs)+len(prof.Examples.External) == 0 {
		g.Missing = append(g.Missing, "examples")
	}
	// Thin: any rendered-reference entry that's SIGNATURE_ONLY. One
	// GapNote per source bucket — the analyzer downstream may de-dupe.
	for _, r := range refs.AllReferenceRefs(prof.Docs.Reference) {
		if r.GetSubstance() == commonpb.Substance_SIGNATURE_ONLY {
			g.Thin = append(g.Thin, &coveragepb.GapNote{
				Category: "docs.reference",
				Reason:   "rendered reference is signature-only",
			})
			break
		}
	}
	sort.Strings(g.Missing)
	return g
}

// --- helpers ---

func protocolOfMethodID(methodID string) string {
	dot := strings.LastIndex(methodID, ".")
	if dot < 0 {
		return ""
	}
	return methodID[:dot]
}

func methodNameOnly(methodID string) string {
	dot := strings.LastIndex(methodID, ".")
	if dot < 0 {
		return methodID
	}
	return methodID[dot+1:]
}

// wildcardRefMatches honors the fidlmatch wildcard candidate form
// "lib/*.Method" (see internal/fidlmatch). Returns true if e is a
// METHOD-kind element in the named library whose local method name
// is Method.
//
// The fan-out path catches the test-uses-semantic-variable pattern
// (e.g. `parent_session_->SetInfiniteHitRegion()` where the var
// doesn't equal a protocol name). Guarded fidlmatch-side to
// distinctive multi-token method names so single-word "Get"/"Set"
// don't over-attribute across every protocol.
func wildcardRefMatches(ref string, e *contractpb.ContractElement) bool {
	// Format: "<lib>/*.<Method>"
	slash := strings.Index(ref, "/*.")
	if slash < 0 {
		return false
	}
	if e.GetKind() != contractpb.ContractElementKind_METHOD {
		return false
	}
	lib := ref[:slash]
	method := ref[slash+len("/*."):]
	if lib == "" || method == "" {
		return false
	}
	if e.GetLibrary() != lib {
		return false
	}
	// Element ID format: "lib/Protocol.Method". Match the suffix.
	return strings.HasSuffix(e.GetId(), "."+method)
}

func splitMethodID(methodID string) (protocol, method string) {
	dot := strings.LastIndex(methodID, ".")
	if dot < 0 {
		return "", ""
	}
	slash := strings.LastIndex(methodID[:dot], "/")
	if slash < 0 {
		return "", ""
	}
	return methodID[slash+1 : dot], methodID[dot+1:]
}

// addWithStemBlocked inserts token into set, plus a simple
// plural/singular stem so token-equality lookups work across that
// boundary (Fix C). Rules:
//   - Always insert the original token.
//   - If token ends in "s" (but not "ss") and the stem would be at
//     least 3 chars, insert the stripped form too.
//   - If token doesn't end in "s" and is longer than 3 chars, insert
//     the "+s" plural form too.
//
// Excludes short tokens entirely to avoid pathological stems like
// "ps" → "p" or "is" → "i".
//
// The optional blocked set names forms the stemming must not bridge
// to: when the would-be-added stem (or plural) appears in blocked,
// the bridging is suppressed and only the original token is stored.
// Used to disable the trailing-s heuristic for token pairs that would
// conflate distinct contract elements — see Indexer.ambiguousLastTokens.
func addWithStemBlocked(set map[string]bool, token string, blocked map[string]bool) {
	set[token] = true
	if len(token) <= 3 {
		return
	}
	if strings.HasSuffix(token, "s") {
		if !strings.HasSuffix(token, "ss") {
			stem := strings.TrimSuffix(token, "s")
			if len(stem) >= 3 && !blocked[stem] {
				set[stem] = true
			}
		}
		return
	}
	if !blocked[token+"s"] {
		set[token+"s"] = true
	}
}

// computeAmbiguousLastTokens scans every indexed element's leaf
// CamelCase token (the rightmost piece of its id's last segment).
// For each lowercase leaf token, if both forms — the bare token X
// and its trailing-s plural Xs — are leaf tokens of distinct
// elements, both forms are marked ambiguous so addWithStemBlocked
// refuses to bridge them. Computed once at index-build time.
func (i *Indexer) computeAmbiguousLastTokens() {
	last := make(map[string]bool, len(i.elemByID))
	for _, e := range i.elemByID {
		tokens := splitCamelCase(lastSegment(e.GetId()))
		if len(tokens) == 0 {
			continue
		}
		last[strings.ToLower(tokens[len(tokens)-1])] = true
	}
	ambig := make(map[string]bool)
	for t := range last {
		if len(t) <= 3 {
			continue
		}
		// Plural form: t ends in 's', singular t[:-1] also exists.
		if strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
			stem := strings.TrimSuffix(t, "s")
			if len(stem) >= 3 && last[stem] {
				ambig[t] = true
				ambig[stem] = true
			}
		}
		// Singular form: t+"s" also exists. (Symmetric coverage so a
		// single iteration order doesn't miss either direction.)
		if last[t+"s"] {
			ambig[t] = true
			ambig[t+"s"] = true
		}
	}
	i.ambiguousLastTokens = ambig
}

// cliShapeMatch tightens Strategy 3 for space-separated element IDs
// (cobra "docker container run", argh "ffx component show"). It
// applies two rules:
//
//  1. Every token of the element ID (excluding the binary/library
//     name) must appear in either the test's name tokens or its path
//     tokens. Same as the original Strategy 3.
//  2. AT LEAST ONE token from the "subcommand name" portion of the
//     element ID must appear specifically in the path tokens. The
//     subcommand-name portion is the last space-separated segment
//     for SUBCOMMAND elements, or the parent's last segment for
//     flag elements (the bit before the `--flag`).
//
// Rule 2 kills the docker false-positive class: TestRunCommit
// in `commit_test.go` lacks "run" in its PATH (only its name),
// so it stops being attributed to `docker run`. Real run-tests in
// `run_test.go` have "run" in the path; they keep matching.
func cliShapeMatch(elemID, library string, testTokens, pathTokens map[string]bool, testPathForElem string) bool {
	lib := strings.ToLower(library)
	var required []string
	for _, t := range tokensFromElementID(elemID) {
		if t == lib {
			continue
		}
		required = append(required, t)
	}
	if len(required) == 0 {
		return false
	}
	allRequiredInTokens := true
	for _, t := range required {
		if !testTokens[t] {
			allRequiredInTokens = false
			break
		}
	}
	if !allRequiredInTokens {
		// Kebab-vs-concatenated tolerance for the primary check. The
		// kubectl source convention is `pkg/cmd/<concat>/` so the
		// path emits one merged token ("clusterinfo") rather than the
		// element's ["cluster","info"] split. Accept the joined form
		// as a single-token substitute for the full required list,
		// but only when there are ≥2 required tokens (no point trying
		// to "join" a single token — would just re-check the same
		// thing and risk under-constraining single-token matches).
		if len(required) < 2 {
			return false
		}
		joined := strings.Join(required, "")
		if !testTokens[joined] {
			return false
		}
	}
	// Path-token requirement: at least one token from the subcommand
	// portion must be in path tokens. For "docker container run",
	// the subcommand portion is "run". For "docker container run
	// --detach", it's still "run" (we strip the flag suffix).
	//
	// Top-level flags like "docker --log-level" have no subcommand
	// portion — subcommandSegment returns the binary itself. In that
	// case the flag NAME's tokens are the discriminator:
	//   - Multi-token flag (>=2 distinct tokens, e.g. --log-level →
	//     {log, level}): the all-tokens check is strong enough; skip
	//     the path requirement.
	//   - Single-token flag (e.g. --config → {config}): require the
	//     token to appear in path tokens. Without this, common
	//     English-word flag names match every test that mentions
	//     the word.
	subcmdSeg := subcommandSegment(elemID)
	subcmdTokens := tokensFromElementID(subcmdSeg)
	allLib := true
	for _, t := range subcmdTokens {
		if t != lib {
			allLib = false
			break
		}
	}
	if allLib {
		// Top-level flag (e.g. "docker --log-level"). The binary
		// name must appear in the test's path tokens — otherwise the
		// test is almost certainly about a sibling subcommand whose
		// path happens to contain a token like "config" or "host".
		// Tests of the real top-level binary live near the binary
		// root (cmd/<binary>/*); tests of subcommands don't repeat
		// the binary name in their paths.
		//
		// After that gate, multi-token flag names provide enough
		// discrimination on their own; single-token names like
		// --config additionally require the token to be in path.
		if !pathTokens[lib] {
			return false
		}
		nonLibCount := 0
		for _, t := range required {
			if t != lib {
				nonLibCount++
			}
		}
		if nonLibCount >= 2 {
			return true
		}
		for _, t := range required {
			if t == lib {
				continue
			}
			if pathTokens[t] {
				return true
			}
		}
		return false
	}
	// Tightened path-token requirement: the LAST non-library token of
	// the subcommand path must appear in the test's FILE BASENAME
	// tokens — not just any path token. Without this tightening,
	// `pkg/cmd/rollout/history_test.go::TestRolloutHistory` was
	// attributing to `kubectl rollout` (parent) because the `rollout`
	// directory contributes "rollout" to the path-token set, even
	// though the test's file is specifically the `history` child. The
	// basename anchor pins attribution to the test's own file, not
	// any ancestor directory.
	//
	// Tightened further (kubectl v5 validation): the test file
	// basename's tokens (after stripping `_test`) must EXACTLY equal
	// the joined non-lib subcommand tokens — not merely contain the
	// last token. Otherwise `create_role_test.go` (basename tokens
	// {"create","role"}) was attributing to `kubectl create` (last
	// token "create") AND to `kubectl create role`. The leaf has its
	// own element; the parent must not double-claim. We achieve
	// exactness by checking that the basename's joined form equals
	// the subcommand's joined form, with kebab→concatenated tolerance.
	basenameTokens := basenameTokenSet(testPathForElem)
	basenameJoined := basenameJoinedForm(testPathForElem)
	subcmdJoined := joinNonLib(subcmdTokens, lib)
	if subcmdJoined != "" && basenameJoined == subcmdJoined {
		return true
	}
	// Looser fallback: the basename equals the subcommand path's LAST
	// non-lib token, allowing single-segment basenames in projects that
	// don't follow the "<full_subcommand_path>_test.go" naming
	// convention. Example: docker's `run_test.go` covers `docker run`
	// (basename "run" == lastNonLib "run").
	var lastNonLib string
	for j := len(subcmdTokens) - 1; j >= 0; j-- {
		if subcmdTokens[j] != lib {
			lastNonLib = subcmdTokens[j]
			break
		}
	}
	if lastNonLib != "" && len(basenameTokens) == 1 && basenameTokens[lastNonLib] {
		return true
	}
	// Directory anchor (crate-per-command layouts: ffx plugins + subtools,
	// cargo subcommands, git-libexec). When the test file's basename is a
	// generic module name, the command name cannot be in the basename — it
	// is in the directory chain. Mirror the basename exact rule on the dirs:
	// the command path (`required`) must equal a boundary-aligned trailing
	// run of the directory chain. Gated on generic basename so kubectl's
	// basename-carried names (history_test.go) are untouched and the
	// parent-double-claim guard the basename rule provides is preserved.
	if isGenericModuleBasename(testPathForElem) {
		chain := commandDirSegments(testPathForElem)
		reqJoined := alnumLower(strings.Join(required, ""))
		if reqJoined != "" {
			acc := ""
			for i := len(chain) - 1; i >= 0; i-- {
				acc = alnumLower(chain[i]) + acc
				if acc == reqJoined {
					return true
				}
				if len(acc) >= len(reqJoined) {
					break
				}
			}
		}
	}
	return false
}

// alnumLower keeps only [a-z0-9], lowercasing A-Z and dropping every
// other rune. It is the normalization used to collapse kebab/snake/
// dotted forms to a single comparable token ("set-order" → "setorder").
func alnumLower(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// basenameJoinedForm returns the file basename (without `_test` suffix
// or extension) lowercased with non-alpha-num runs collapsed to "".
// `create_role_test.go` → "createrole"; `apiresources_test.go` →
// "apiresources"; `set_cluster_test.go` → "setcluster".
func basenameJoinedForm(path string) string {
	if path == "" {
		return ""
	}
	base := path
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSuffix(base, "_test")
	return alnumLower(base)
}

// isGenericModuleBasename reports whether a path's final element is a
// generic module filename — one that carries no command name because
// the command lives in the directory chain (Rust crate-per-command
// layouts: ffx plugins/subtools, cargo subcommands). Strips extension
// and a trailing `_test`, lowercases, and matches lib/mod/main/args.
func isGenericModuleBasename(path string) bool {
	if path == "" {
		return false
	}
	base := path
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSuffix(strings.ToLower(base), "_test")
	switch base {
	case "lib", "mod", "main", "args":
		return true
	}
	return false
}

// commandDirSegments returns the directory chain of a path (everything
// but the file), lowercased, with trailing scaffolding segments
// (src/tests/test) popped so the command-bearing directories are last.
// `plugins/target/list/src/lib.rs` → ["plugins","target","list"].
// Segments are NOT alnum-collapsed here; the directory-anchor loop
// collapses per-segment so it can align on directory boundaries.
func commandDirSegments(path string) []string {
	if path == "" {
		return nil
	}
	parts := strings.Split(path, "/")
	if len(parts) <= 1 {
		return nil
	}
	segs := parts[:len(parts)-1] // drop the file
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = strings.ToLower(s)
	}
	for len(out) > 0 {
		last := out[len(out)-1]
		if last == "src" || last == "tests" || last == "test" {
			out = out[:len(out)-1]
			continue
		}
		break
	}
	return out
}

// basenameTokenSet returns the tokens of a file path's basename
// (without extension or `_test` suffix), split on `_`/`-`/`.`/etc.
// For `pkg/cmd/rollout/history_test.go` returns {"history"}; for
// `pkg/cmd/create/secret_dockerregistry_test.go` returns
// {"secret", "dockerregistry"}.
func basenameTokenSet(path string) map[string]bool {
	if path == "" {
		return nil
	}
	base := path
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSuffix(base, "_test")
	out := make(map[string]bool, 4)
	for _, t := range strings.FieldsFunc(base, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	}) {
		out[strings.ToLower(t)] = true
	}
	return out
}

// joinNonLib concatenates non-library tokens into a single
// lowercase string. Returns "" if every input token is the library
// name (e.g. the root binary element).
func joinNonLib(tokens []string, lib string) string {
	var parts []string
	for _, t := range tokens {
		if t == lib {
			continue
		}
		parts = append(parts, t)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "")
}

// subcommandSegment returns the last subcommand name in a CLI-shaped
// element ID, stripping any trailing flag/positional suffix.
//
//	"docker container run"         → "run"
//	"docker container run --rm"    → "run"
//	"ffx component show <query>"   → "show"
//	"docker"                       → "docker"
func subcommandSegment(elemID string) string {
	// Strip trailing " --flag" / " -x" / " <positional>" markers.
	for _, sep := range []string{" --", " -", " <"} {
		if i := strings.Index(elemID, sep); i >= 0 {
			elemID = elemID[:i]
		}
	}
	if i := strings.LastIndex(elemID, " "); i >= 0 {
		return elemID[i+1:]
	}
	return elemID
}

// implLocalName extracts the local class name from an implementor's
// element ID. Implementors come from a few shapes:
//
//	cpp:src/storage/file.h#FileConnection  → "FileConnection"
//	some.lib/ClassName                     → "ClassName"
//	ClassName                              → "ClassName"
//
// lastSegment was designed for FIDL/proto IDs (split on `/` and `.`)
// and accidentally truncated impl IDs that use `#` to separate the
// containing-file path from the class name. The C++ impl convention
// `<framework>:<path>#<ClassName>` puts the class name AFTER `#`,
// so we must consult the `#` separator first.
//
//nolint:unused // only caller is implMentionMatch, itself retained for the v2 conformance-test path
func implLocalName(id string) string {
	if i := strings.LastIndex(id, "#"); i >= 0 {
		return id[i+1:]
	}
	return lastSegment(id)
}

func lastSegment(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.LastIndex(id, "."); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// tokensFromElementID splits an element ID like "fuchsia.io/Directory.Open"
// into ["fuchsia", "io", "directory", "open"]. CamelCase is split into
// constituent words.
func tokensFromElementID(id string) []string {
	var out []string
	for _, piece := range strings.FieldsFunc(id, func(r rune) bool {
		return r == '.' || r == '/' || r == '-' || r == '_' || r == ' '
	}) {
		out = append(out, splitCamelCase(piece)...)
	}
	return out
}

// tokensFromPath returns split tokens for both the file basename and
// every directory component on the way down. Directory tokens are how
// a test in src/ui/scenic/tests/screenshot_test.cc gets "scenic" in
// its token set even when neither the suite nor the test name
// mentions the library — common in Fuchsia where tests are named
// after the feature ("ScreenshotTest") rather than the FIDL library.
func tokensFromPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		// Strip extension from the final segment.
		if i := strings.LastIndex(seg, "."); i >= 0 {
			seg = seg[:i]
		}
		out = append(out, strings.FieldsFunc(seg, func(r rune) bool {
			return r == '_' || r == '-' || r == '.'
		})...)
	}
	return out
}

// splitCamelCase splits a CamelCase identifier into lowercase tokens.
// Recognizes two boundaries:
//
//	aB  → split before B   ("FooBar"     → [foo bar])
//	ABc → split before B   ("APIResource"→ [api resource])
//
// The second rule (acronym-aware) is what makes
// "TestAPIResourcesRun" tokenize as [test api resources run] rather
// than [test apiresources run], so a cobra element ID like
// "kubectl api-resources" — whose own tokens are [api resources] —
// can attribute via the name-token matcher. Mirrors the
// adapters/gotest splitCamel.
func splitCamelCase(s string) []string {
	if s == "" {
		return nil
	}
	var tokens []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			tokens = append(tokens, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		isUpper := r >= 'A' && r <= 'Z'
		if isUpper && i > 0 {
			prev := runes[i-1]
			prevLower := prev >= 'a' && prev <= 'z'
			if prevLower {
				flush()
			} else if i+1 < len(runes) {
				next := runes[i+1]
				nextLower := next >= 'a' && next <= 'z'
				if nextLower {
					flush()
				}
			}
		}
		cur = append(cur, r)
	}
	flush()
	return tokens
}

// sameDirectory returns whether two source paths share a directory.
// Currently unused by the default matcher (Strategy 2 / implements-map
// was removed). Retained alongside implMentionMatch for the v2 conformance-test path.
//
//nolint:unused // retained for v2 conformance-test path
func sameDirectory(a, b string) bool {
	aDir := a
	bDir := b
	if i := strings.LastIndex(a, "/"); i >= 0 {
		aDir = a[:i]
	}
	if i := strings.LastIndex(b, "/"); i >= 0 {
		bDir = b[:i]
	}
	// Sibling-directory match too: tests/foo and connection/foo often
	// share a great-grandparent. Match if any directory component
	// overlaps via the implementing file's parent path.
	if aDir == bDir {
		return true
	}
	// Match when the test directory is a sibling under the same module.
	// Heuristic: drop a trailing /tests or /test from a's path and
	// compare prefix.
	noTests := strings.TrimSuffix(strings.TrimSuffix(aDir, "/tests"), "/test")
	if strings.HasPrefix(bDir, noTests+"/") || bDir == noTests {
		return true
	}
	return false
}

func docClaimToRef(dc *docclaimpb.DocClaim) *commonpb.DocRef {
	return &commonpb.DocRef{
		Path:      dc.GetSourcePath(),
		Line:      dc.GetLocation().GetLine(),
		Url:       dc.GetUrl(),
		Substance: dc.GetSubstance(),
		Words:     dc.GetWordCount(),
		Adapter:   dc.GetAdapter(),
	}
}

func cloneMethodWithNewOwner(m *contractpb.ContractElement, newProtoID, surfaceID string) *contractpb.ContractElement {
	clone := &contractpb.ContractElement{
		Id:                 surfaceID,
		Kind:               m.GetKind(),
		Ecosystem:          m.GetEcosystem(),
		Library:            libraryFromProtoID(newProtoID),
		Location:           m.GetLocation(), // declaration site stays the same
		Parameters:         m.GetParameters(),
		ReturnTypes:        m.GetReturnTypes(),
		DocCommentExcerpt:  m.GetDocCommentExcerpt(),
		VersionConstraints: m.GetVersionConstraints(),
	}
	clone.Relationships = append(clone.Relationships, &contractpb.Relationship{
		Kind:            contractpb.RelationshipKind_INHERITED_FROM,
		TargetElementId: m.GetId(),
		Note:            "inherited via composition",
		DeclarationSite: m.GetLocation(),
	})
	return clone
}

func libraryFromProtoID(protoID string) string {
	if i := strings.Index(protoID, "/"); i >= 0 {
		return protoID[:i]
	}
	return ""
}

func countTestRefs(p *coveragepb.CoverageProfile) int {
	if p == nil || p.Tests == nil {
		return 0
	}
	return len(p.Tests.Unit) + len(p.Tests.Integration) + len(p.Tests.E2E) +
		len(p.Tests.Ctf) + len(p.Tests.Performance) + len(p.Tests.Fuzz) + len(p.Tests.Golden)
}
func countDocRefs(p *coveragepb.CoverageProfile) int {
	if p == nil || p.Docs == nil {
		return 0
	}
	n := refs.CountReferenceRefs(p.Docs.Reference)
	n += len(p.Docs.Concept) + len(p.Docs.Tutorial) + len(p.Docs.ReleaseNotes) + len(p.Docs.Faq)
	if p.Docs.Guide != nil {
		n += len(p.Docs.Guide.Migration) + len(p.Docs.Guide.Troubleshooting) + len(p.Docs.Guide.Cookbook)
	}
	if p.Docs.Proposal != nil {
		n += len(p.Docs.Proposal.Rfc) + len(p.Docs.Proposal.Design)
	}
	return n
}

// SlugifiedElementID gives a filesystem-safe ID, used by callers
// (e.g., the eventual CoverageStore) that need disk-safe names.
func SlugifiedElementID(id string) string {
	return slug.SlugifyUnique(id)
}
