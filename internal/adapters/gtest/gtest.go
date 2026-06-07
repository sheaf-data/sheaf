// Package gtest implements the gtest/zxtest test-parser adapter.
//
// Uses regex to identify TEST(), TEST_F(), TEST_P() invocations and
// any extra project-defined test macros. Per Phase 0 we know smoketest
// precision is sufficient — we are not trying to be a C++ parser.
package gtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/fidlmatch"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

const Name = "gtest"
const Version = "0.1.0"

// Parser implements adapters.TestParser for gtest/zxtest.
type Parser struct {
	include         []string
	exclude         []string
	extraTestMacros []string // additional macros like "FUCHSIA_TEST", "ZX_TEST"
	macroRegex      *regexp.Regexp
	matcher         *fidlmatch.Matcher
}

// Config bundles the per-adapter config from sheaf.textproto.
type Config struct {
	Include         []string
	Exclude         []string
	ExtraTestMacros []string
	// IDLPrefix selects the fidlmatch Matcher used for body-level
	// FIDL invocation extraction. Empty = "fuchsia" (default).
	IDLPrefix string
}

// New constructs a Parser with the given config.
func New(cfg Config) *Parser {
	macros := []string{"TEST", "TEST_F", "TEST_P"}
	macros = append(macros, cfg.ExtraTestMacros...)
	// Build a regex like (TEST|TEST_F|TEST_P|FUCHSIA_TEST)\s*\(\s*(\w+)\s*,\s*(\w+)\s*\)
	// We match across whitespace and accept any extra args after the test name.
	macroAlt := ""
	for i, m := range macros {
		if i > 0 {
			macroAlt += "|"
		}
		macroAlt += regexp.QuoteMeta(m)
	}
	rx := regexp.MustCompile(`(?m)\b(` + macroAlt + `)\s*\(\s*(\w+)\s*,\s*(\w+)\s*[\),]`)
	include := cfg.Include
	if len(include) == 0 {
		// Both _test.cc (singular) and _tests.cc (plural) appear in
		// real codebases; Fuchsia uses both in different subsystems.
		include = []string{
			"**/*_test.cc", "**/*_test.cpp",
			"**/*_tests.cc", "**/*_tests.cpp",
			"**/*_unittest.cc",
		}
	}
	return &Parser{
		include:         include,
		exclude:         cfg.Exclude,
		extraTestMacros: cfg.ExtraTestMacros,
		macroRegex:      rx,
		matcher:         fidlmatch.NewMatcher(fidlmatch.Config{IDLPrefix: cfg.IDLPrefix}),
	}
}

func (p *Parser) Name() string                  { return Name }
func (p *Parser) Version() string               { return Version }
func (p *Parser) SupportedFrameworks() []string { return []string{"gtest", "zxtest"} }

// Discover scans the repo for gtest-style tests and returns them.
// Context cancellation is honored between files.
func (p *Parser) Discover(ctx context.Context, repoRoot string, _ adapters.ScopeConfig) ([]*testcasepb.TestCase, error) {
	var out []*testcasepb.TestCase
	err := adapters.WalkMatching(repoRoot, p.include, p.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("gtest: read %s: %w", rel, err)
		}
		// Pre-compute line offsets for line-number resolution.
		lineOffsets := computeLineOffsets(body)
		matches := p.macroRegex.FindAllSubmatchIndex(body, -1)
		// Library scope is file-level (driven by #includes), but the
		// FIDL ref *extraction* runs per-TEST against the bytes from
		// each TEST() macro to the start of the next macro (or EOF).
		// That keeps a test's ContractRefs grounded in code the test
		// actually contains, instead of every method any test in the
		// file happens to mention — which previously inflated
		// method-level test counts 10-30× (audit on
		// fuchsia.ui.composition surfaced the issue).
		//
		// Proto/gRPC refs and the FIDL include-derived library set
		// are still file-level (they're metadata, not call sites);
		// only the body-scan ContractRefs get the per-test range.
		bodyStr := string(body)
		scope := p.matcher.CPPIncludeLibraries(bodyStr)
		for i, m := range matches {
			macro := string(body[m[2]:m[3]])
			suite := string(body[m[4]:m[5]])
			testName := string(body[m[6]:m[7]])
			line := lineFromOffset(lineOffsets, m[0])
			id := suite + "." + testName
			// Per-test body range: from this TEST() macro to the
			// start of the next TEST() macro (or end of file).
			rangeStart := m[0]
			rangeEnd := len(body)
			if i+1 < len(matches) {
				rangeEnd = matches[i+1][0]
			}
			testBody := bodyStr[rangeStart:rangeEnd]
			testRefs := p.matcher.Extract(testBody, "cpp", scope)
			// Proto/gRPC refs: services-in-scope still come from
			// file-level (using-decls), but the actual method-call
			// and fixture-construction scans are bounded to this
			// test's body so each test only claims methods it
			// actually invokes — see extractGtestProtoRefsInRange.
			protoRefs := extractGtestProtoRefsInRange(body, body[rangeStart:rangeEnd])
			testRefs = mergeRefs(testRefs, protoRefs)
			testCase := &testcasepb.TestCase{
				Id:        id,
				Framework: "gtest",
				Location: &commonpb.SourceLocation{
					Path: rel,
					Line: uint32(line),
				},
				Name:         id,
				NameTokens:   tokenizeName(suite + " " + testName),
				SourceHash:   hashBytes(body[m[0]:m[1]]),
				Assertions:   nil, // first pass: don't extract individual EXPECT/ASSERT
				ContractRefs: testRefs,
			}
			// Stash the macro form on ecosystem_meta if non-standard.
			_ = macro
			out = append(out, testCase)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func computeLineOffsets(body []byte) []int {
	offsets := []int{0}
	for i, b := range body {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

func lineFromOffset(offsets []int, off int) int {
	// Binary search.
	lo, hi := 0, len(offsets)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if offsets[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1 // 1-based
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ----------------------------------------------------------------
// Proto/gRPC body-ref extractor.
// ----------------------------------------------------------------
//
// Mirrors the gotest cobra-invocation extractor in spirit: scans a
// C++ test file body for patterns that imply use of specific proto
// contract elements, returning candidate refs in the alias forms the
// indexer's Strategy 1 (direct-ref) matcher accepts:
//
//   - Bare service+method  ("Channelz.GetChannel")
//   - Bare message-type    ("GetChannelRequest")
//
// The proto adapter emits these as Aliases on every PROTOCOL /
// METHOD / TYPE element, so an exact match wins immediately and
// candidates that don't match any element are dropped silently.
//
// Patterns recognized:
//
//  1. Service scope establishment:
//     <Service>::Stub
//     <Service>::AsyncService
//     <Service>::NewStub(
//     <Service>::Service
//     ::<Service>::Service [base class]
//     → adds <Service> to the file's "services in scope" set.
//
//  2. Stub-style method invocation:
//     <var>->Method(    (any var, capitalized method name)
//     <var>.Method(     (rarer; usually pointer)
//     → emits "<Service>.Method" for every Service in scope.
//
//  3. Message-type fixture declaration:
//     <CapitalizedType> <varname>;
//     <CapitalizedType> <varname> =
//     <CapitalizedType> <varname>(
//     → emits "<CapitalizedType>" as a bare ref.
//
// Cross-product (1) × (2) over-emits when the file uses multiple
// services; the indexer's exact-alias match filters non-resolvers.
var (
	gtestServiceScopeRx = regexp.MustCompile(
		`\b([A-Z][A-Za-z0-9_]*)::(?:Stub\b|AsyncService\b|Service\b|NewStub\s*\()`)
	gtestMethodCallRx = regexp.MustCompile(
		`(?:->|\.)\s*([A-Z][A-Za-z0-9_]*)\s*\(`)
	gtestFixtureRx = regexp.MustCompile(
		`(?m)^\s*([A-Z][A-Za-z0-9_]+(?:Request|Response|Event|Trace|State|Data|Ref|Info|Config|Options?))\s+[a-z_][A-Za-z0-9_]*\s*[;=({]`)
)

// gtestMethodSkipList drops obviously-not-a-rpc-method capitalized
// identifiers that the broad method-call pattern would otherwise
// match (test macros, std library, common gmock/gtest helpers).
var gtestMethodSkipList = map[string]bool{
	"EXPECT": true, "ASSERT": true, "EXPECT_TRUE": true, "EXPECT_EQ": true,
	"EXPECT_NE": true, "EXPECT_FALSE": true, "EXPECT_THAT": true,
	"ASSERT_TRUE": true, "ASSERT_EQ": true, "ASSERT_NE": true,
	"ON_CALL": true, "EXPECT_CALL": true, "TEST": true, "TEST_F": true,
	"TEST_P": true, "INSTANTIATE_TEST_SUITE_P": true,
	// gtest matcher helpers
	"Eq": true, "Ne": true, "Lt": true, "Le": true, "Gt": true, "Ge": true,
	"Not": true, "AllOf": true, "AnyOf": true,
	// std library noise
	"Get": true, "Set": true, "Add": true, "Push": true, "Pop": true,
	"Reset": true, "Clear": true, "Size": true, "Empty": true,
	"Begin": true, "End": true, "Front": true, "Back": true,
	"Lock": true, "Unlock": true, "Wait": true, "Notify": true,
	// extremely common types likely to false-positive as methods
	"Status": true,
}

func extractGtestProtoRefs(body []byte) []string {
	return extractGtestProtoRefsInRange(body, body)
}

// extractGtestProtoRefsInRange does the same extraction but lets the
// caller scope the Pass-2 method-call walk and the Pass-3 fixture walk
// to a narrower byte range (typically one TEST() macro's body) while
// the Pass-1 services-in-scope walk runs over the full file (since
// `using` declarations are typically at file scope, above all tests).
//
// The grpc.channelz.v1 audit showed why per-test scoping for Pass 2
// matters: a `channelz_service_test.cc` file with a dozen TESTs each
// calling a different `Channelz.GetX` RPC was attributing EVERY
// `GetX` method to EVERY test, because the cross-product was computed
// against the union of method calls across the whole file. Per-test
// scoping confines each test's method refs to the methods that test
// actually calls.
func extractGtestProtoRefsInRange(fullBody, testBody []byte) []string {
	refs := make(map[string]struct{})
	fullStr := string(fullBody)
	testStr := string(testBody)

	// 1. Services in scope — file-level (includes / using-decls live
	//    above the TEST() macros).
	services := make(map[string]struct{})
	for _, m := range gtestServiceScopeRx.FindAllStringSubmatch(fullStr, -1) {
		services[m[1]] = struct{}{}
	}

	// 2. Method calls → cross-product with services. Bounded to the
	//    test body so each test only claims methods it actually calls.
	if len(services) > 0 {
		seenMethods := make(map[string]struct{})
		for _, m := range gtestMethodCallRx.FindAllStringSubmatch(testStr, -1) {
			name := m[1]
			if gtestMethodSkipList[name] {
				continue
			}
			seenMethods[name] = struct{}{}
		}
		for svc := range services {
			for method := range seenMethods {
				refs[svc+"."+method] = struct{}{}
			}
		}
	}

	// 3. Message-type fixtures — bounded to the test body for the same
	//    reason (different tests in one file may construct different
	//    fixture types).
	for _, m := range gtestFixtureRx.FindAllStringSubmatch(testStr, -1) {
		refs[m[1]] = struct{}{}
	}

	out := make([]string, 0, len(refs))
	for r := range refs {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// mergeRefs returns a-then-b with duplicates removed. Used to fold
// proto refs into the FIDL refs from the matcher.
func mergeRefs(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// tokenizeName splits a test name into lowercase tokens on CamelCase
// boundaries and non-alphanumeric runs. "FidlReadAtEndReturnsErr"
// becomes ["fidl", "read", "at", "end", "returns", "err"].
func tokenizeName(s string) []string {
	var tokens []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			tokens = append(tokens, string(cur))
			cur = cur[:0]
		}
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 && len(cur) > 0 {
				last := cur[len(cur)-1]
				if last >= 'a' && last <= 'z' {
					flush()
				}
			}
			cur = append(cur, r+('a'-'A'))
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}
