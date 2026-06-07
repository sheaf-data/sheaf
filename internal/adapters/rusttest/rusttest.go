// Package rusttest implements the rust-test test-parser adapter.
//
// Recognizes #[test], #[fuchsia::test], #[tokio::test], and any
// extra project-defined attributes. Function name is taken from the
// following `fn name(...)` declaration.
package rusttest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/fidlmatch"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

const Name = "rust-test"
const Version = "0.1.0"

type Parser struct {
	include             []string
	exclude             []string
	extraTestAttributes []string
	attrRegex           *regexp.Regexp
	matcher             *fidlmatch.Matcher
}

type Config struct {
	Include             []string
	Exclude             []string
	ExtraTestAttributes []string
	// IDLPrefix selects the fidlmatch Matcher for body-level FIDL
	// invocation extraction. Empty = "fuchsia" (default).
	IDLPrefix string
}

func New(cfg Config) *Parser {
	attrs := []string{"test", "tokio::test", "async_std::test"}
	attrs = append(attrs, cfg.ExtraTestAttributes...)
	alt := ""
	for i, a := range attrs {
		if i > 0 {
			alt += "|"
		}
		alt += regexp.QuoteMeta(a)
	}
	// Match `#[<attr>(...)]` or `#[<attr>]`, with optional whitespace.
	// Allow any args after the attribute, including nested parens (single level).
	rx := regexp.MustCompile(`(?m)^\s*#\[(` + alt + `)(?:\([^\)]*\))?\][\s\S]*?fn\s+(\w+)\s*\(`)
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.rs"}
	}
	exclude := cfg.Exclude
	if len(exclude) == 0 {
		exclude = []string{"**/target/**"}
	}
	return &Parser{
		include:             include,
		exclude:             exclude,
		extraTestAttributes: cfg.ExtraTestAttributes,
		attrRegex:           rx,
		matcher:             fidlmatch.NewMatcher(fidlmatch.Config{IDLPrefix: cfg.IDLPrefix}),
	}
}

func (p *Parser) Name() string                  { return Name }
func (p *Parser) Version() string               { return Version }
func (p *Parser) SupportedFrameworks() []string { return []string{"rust-test"} }

func (p *Parser) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*testcasepb.TestCase, error) {
	var out []*testcasepb.TestCase
	// CLI-shaped scope libraries (bare binary names like "ffx") enable
	// body-invocation extraction: a behavior test that runs `ffx <cmd>
	// --flag` as a subprocess credits the command + flag elements directly.
	// No-op when the scope declares no CLI-shaped binary (preserves every
	// non-CLI rust-test config — kubectl/docker/etc.).
	cliBinaries := cliBinariesFromScope(scope)
	err := adapters.WalkMatching(repoRoot, p.include, p.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("rust-test: read %s: %w", rel, err)
		}
		var fileInvStats invocationStats
		// Module path: derive a suite name from the file's directory.
		moduleSuite := deriveModuleSuite(rel)
		offsets := computeLineOffsets(body)
		matches := p.attrRegex.FindAllSubmatchIndex(body, -1)
		// Library scope from `use` statements is file-level; the
		// per-test ContractRefs come from extracting fidlmatch only
		// over the body range between this #[test] and the next.
		// Matches the gtest adapter's per-test scoping — keeps
		// method-level attribution from inflating just because some
		// other test in the same file mentions a method.
		bodyStr := string(body)
		scope := p.matcher.RustUseLibraries(bodyStr)
		for i, m := range matches {
			attr := string(body[m[2]:m[3]])
			fn := string(body[m[4]:m[5]])
			line := lineFromOffset(offsets, m[0])
			id := moduleSuite + "::" + fn
			rangeStart := m[0]
			rangeEnd := len(body)
			if i+1 < len(matches) {
				rangeEnd = matches[i+1][0]
			}
			testRefs := p.matcher.Extract(bodyStr[rangeStart:rangeEnd], "rust", scope)
			// CLI-flag literals — a Rust integration test like fd's
			// commonly invokes the binary via a helper with args like
			// `&["--hidden", "foo"]`. The fidlmatch matcher doesn't
			// know about CLI flags, so we extract them separately and
			// add them as refs. The clap adapter emits each flag's
			// bare long ("--hidden") and short ("-H") form as an
			// alias, so Strategy 1 attribution lands directly.
			//
			// Bound the flag scan to the actual function body (between
			// the `fn xxx(...) {` opening brace and its matching `}`),
			// not the looser [m[0], matches[i+1][0]] window. The looser
			// window includes the NEXT test's leading attributes
			// (#[test_case], #[cfg], doc comments…), which on
			// parameterized-test patterns embed flag literals like
			// `#[test_case("--hidden", &["--no-hidden"] ; "hidden")]`.
			// Without the tighter bound those literals get attributed
			// to the PREVIOUS test by mistake.
			bodyStart, bodyEnd := functionBodyRange(bodyStr, m[1], rangeEnd)
			testRefs = appendFlagLiteralRefs(testRefs, bodyStr[bodyStart:bodyEnd])
			// ffx-anchored subprocess invocations in the body — `.ffx([…])`
			// (M1) and `Command::new(<ffx-path>).args([…])` (M2). These
			// credit the command + flag elements the invocation exercises,
			// canonicalized through internal/climatch so the refs equal the
			// element-ID form exactly. Bounded to the tight function-body
			// range for the same reason appendFlagLiteralRefs is — so a
			// later test's decorations don't bleed back. No-op unless the
			// scope declares a CLI-shaped binary (cliBinaries).
			invRefs, invStats := extractInvocationRefs(bodyStr[bodyStart:bodyEnd], cliBinaries)
			testRefs = mergeInvocationRefs(testRefs, invRefs)
			fileInvStats.add(invStats)
			out = append(out, &testcasepb.TestCase{
				Id:        id,
				Framework: "rust-test",
				Location: &commonpb.SourceLocation{
					Path: rel,
					Line: uint32(line),
				},
				Name:         fn,
				NameTokens:   tokenizeFnName(fn),
				SourceHash:   hashBytes(body[m[0]:m[1]]),
				ContractRefs: testRefs,
			})
			_ = attr
		}
		// Whole-file invocation pass. ffx runs in test-HELPER methods — the
		// e2e_emu harness's start()/new(), the shared setup the file's
		// #[test]s call — live OUTSIDE any #[test] body, so the per-test
		// range above misses them (and every flag they exercise; emu start's
		// six flags alone). The `.ffx(` / `Command::new(FFX_TOOL_PATH)`
		// idioms are test-harness-only — production ffx code never uses them
		// — so scanning the whole file cannot pick up a non-test invocation.
		// Emit the file's invocation refs on a synthetic file-level TestCase;
		// the per-test pass above stays for granular in-test attribution
		// (the overlap dedupes by element downstream).
		if fileRefs, fileStats := extractInvocationRefs(bodyStr, cliBinaries); len(fileRefs) > 0 {
			fileInvStats = fileStats // whole-file is the superset; its skip tally is authoritative
			out = append(out, &testcasepb.TestCase{
				Id:           moduleSuite + "::<ffx-invocations>",
				Framework:    "rust-test",
				Location:     &commonpb.SourceLocation{Path: rel, Line: 1},
				Name:         "<ffx-invocations>",
				SourceHash:   hashBytes(body),
				ContractRefs: fileRefs,
			})
		}
		logInvocationSkips(rel, fileInvStats)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// mergeInvocationRefs appends invocation-derived refs to the existing
// test refs, de-duplicating. The invocation extractor already dedups
// within itself; this guards against a command/flag ref also produced by
// the fidlmatch/flag-literal passes.
func mergeInvocationRefs(refs, inv []string) []string {
	if len(inv) == 0 {
		return refs
	}
	seen := make(map[string]bool, len(refs)+len(inv))
	for _, r := range refs {
		seen[r] = true
	}
	for _, r := range inv {
		if seen[r] {
			continue
		}
		seen[r] = true
		refs = append(refs, r)
	}
	return refs
}

// cliBinariesFromScope returns scope libraries that look like CLI binary
// names — a single bareword with no "." or "/" (so "ffx"/"kubectl" qualify
// but "fuchsia.driver.framework" / "google/protobuf" don't). Mirrors the
// markdown adapter's helper of the same name; it is the gate for
// body-invocation extraction.
func cliBinariesFromScope(scope adapters.ScopeConfig) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range append(append([]string{}, scope.Libraries...), scope.AlsoInclude...) {
		l = strings.TrimSpace(l)
		if l == "" || strings.ContainsAny(l, "./") || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

func deriveModuleSuite(rel string) string {
	// Strip .rs, replace / with ::.
	s := strings.TrimSuffix(rel, ".rs")
	return strings.ReplaceAll(s, "/", "::")
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
	lo, hi := 0, len(offsets)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if offsets[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// functionBodyRange returns the byte offsets of the function body
// — the `{` immediately following `fn xxx(...)` through its matching
// `}`. start is the offset just after the matched `fn xxx(` prefix
// (so right after the opening paren); maxEnd is the upper bound to
// search. If we can't find a clean brace pair, falls back to
// (start, maxEnd). Tracks string/char/comment context to avoid
// counting braces inside literals.
func functionBodyRange(body string, start, maxEnd int) (int, int) {
	// Find the first `{` after the `fn xxx(...)` signature. We start
	// at `start` which is just past the matched `fn xxx(` — first
	// locate the matching `)` of the param list, then the first `{`.
	depth := 1
	i := start
	for i < maxEnd && depth > 0 {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		i++
	}
	// Skip whitespace + optional `->` return type up to `{`.
	for i < maxEnd && body[i] != '{' {
		i++
	}
	if i >= maxEnd {
		return start, maxEnd
	}
	open := i
	i++
	braceDepth := 1
	for i < maxEnd && braceDepth > 0 {
		switch body[i] {
		case '"':
			// Skip string literal.
			i++
			for i < maxEnd {
				if body[i] == '\\' && i+1 < maxEnd {
					i += 2
					continue
				}
				if body[i] == '"' {
					i++
					break
				}
				i++
			}
		case '/':
			if i+1 < maxEnd && body[i+1] == '/' {
				// Line comment.
				for i < maxEnd && body[i] != '\n' {
					i++
				}
			} else if i+1 < maxEnd && body[i+1] == '*' {
				// Block comment.
				i += 2
				for i+1 < maxEnd && !(body[i] == '*' && body[i+1] == '/') {
					i++
				}
				i += 2
			} else {
				i++
			}
		case '{':
			braceDepth++
			i++
		case '}':
			braceDepth--
			i++
		default:
			i++
		}
	}
	if braceDepth != 0 {
		return open, maxEnd
	}
	return open, i
}

// reFlagLiteral matches a quoted long-form CLI-flag literal inside a
// Rust source snippet ("--name", possibly with "=value"). The leading
// double-quote constrains it to actual string literals so we don't
// pick up flags written in `///` doc comments above the test or in
// `//` comments inside it.
//
// Short-form ("-x") literals are intentionally NOT extracted: Rust
// integration tests routinely pass short flags to NESTED binaries
// (`&["--exec-batch", "bash", "-c", "exit 1"]` — the "-c" is bash's,
// not the binary-under-test's). The collision rate is high enough
// that emitting them as refs creates more FPs than the recall gains
// justify. Tests that exercise a short flag intentionally almost
// always also use the long form somewhere in the same test body.
//
// Captures group 1 = the flag without the value tail. For "--foo=bar"
// the captured form is "--foo" so it can match an element alias.
var reFlagLiteral = regexp.MustCompile(`"(--[a-z][a-z0-9-]*)(?:=[^"]*)?"`)

// appendFlagLiteralRefs scans body for "--flag" / "-x" literals and
// appends them as refs. De-duplicates against existing refs.
func appendFlagLiteralRefs(refs []string, body string) []string {
	seen := make(map[string]bool, len(refs)+8)
	for _, r := range refs {
		seen[r] = true
	}
	for _, m := range reFlagLiteral.FindAllStringSubmatch(body, -1) {
		flag := m[1]
		if seen[flag] {
			continue
		}
		seen[flag] = true
		refs = append(refs, flag)
	}
	return refs
}

func tokenizeFnName(fn string) []string {
	var tokens []string
	var cur []rune
	for _, r := range fn {
		if r == '_' {
			if len(cur) > 0 {
				tokens = append(tokens, string(cur))
				cur = cur[:0]
			}
			continue
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		tokens = append(tokens, string(cur))
	}
	return tokens
}
