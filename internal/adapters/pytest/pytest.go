// Package pytest implements a Python pytest test-parser adapter.
//
// Walks Python test files (default `**/test_*.py` and `**/*_test.py`)
// using an AST-free scanner — no Python runtime dependency. Discovers:
//
//   - Module-scope `def test_<name>(...)` functions.
//   - `def test_<name>(self, ...)` methods on test classes. A class is
//     a test class if its name starts with `Test`, ends with `Test`/
//     `Tests`, or it derives from a `*TestCase` base (e.g.
//     `unittest.TestCase`). This mirrors pytest's actual collection
//     rule (python_classes default `Test*` plus unittest.TestCase
//     subclasses), so real-world suites like Pigweed's pw_rpc Python
//     tests — which use `class ClientTest(unittest.TestCase)` — are
//     discovered. Only the outermost test class is considered; nested
//     classes are out of scope for v1.
//
// For each discovered test, emits a TestCase whose ContractRefs are
// the union of three extraction patterns applied to the test body:
//
//  1. Dotted FQDNs: `pw.log.LogEntry` — at least one dot before the
//     CamelCase target. Module aliases are applied to the leading
//     segment (`pw_log` → `pw.log`). Both the dotted ("pkg.Name") and
//     slash ("pkg/Name") forms are emitted for the alias matcher.
//
//  2. String-literal refs (when idl_prefix is set): same shape as
//     protocpp's. Matches both the all-dots form ("pw.log.Logs.Listen")
//     and the gRPC method-name form ("pw.log.Logs/Listen"); parent
//     refs (service from method) are also emitted.
//
//  3. Import-tracked bare names: a `from X import Y` (or `import X as
//     L` plus `L.Y`) where Y is referenced bare in a test body
//     resolves to the qualified form. Multi-line parenthesized imports
//     are supported; module aliases apply to the leading segment.
package pytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

const Name = "pytest"
const Version = "0.1.0"

type Parser struct {
	include       []string
	exclude       []string
	idlPrefix     string
	moduleAliases map[string]string
}

type Config struct {
	Include []string
	Exclude []string
	// IDLPrefix anchors the string-literal extractor. Empty disables
	// the string-literal pass; the other two patterns still run.
	IDLPrefix string
	// ModuleAliases maps a Python module name to its canonical IDL
	// name. Entries are "python_module=idl_name" strings, e.g.
	// "pw_log=pw.log". Applied to the leading segment of any extracted
	// qualified ref.
	ModuleAliases []string
}

func New(cfg Config) *Parser {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/test_*.py", "**/*_test.py"}
	}
	aliases := map[string]string{}
	for _, p := range cfg.ModuleAliases {
		if i := strings.Index(p, "="); i > 0 {
			k := strings.TrimSpace(p[:i])
			v := strings.TrimSpace(p[i+1:])
			if k != "" && v != "" {
				aliases[k] = v
			}
		}
	}
	return &Parser{
		include:       include,
		exclude:       cfg.Exclude,
		idlPrefix:     cfg.IDLPrefix,
		moduleAliases: aliases,
	}
}

func (p *Parser) Name() string                  { return Name }
func (p *Parser) Version() string               { return Version }
func (p *Parser) SupportedFrameworks() []string { return []string{"pytest"} }

func (p *Parser) Discover(ctx context.Context, repoRoot string, _ adapters.ScopeConfig) ([]*testcasepb.TestCase, error) {
	var out []*testcasepb.TestCase
	err := adapters.WalkMatching(repoRoot, p.include, p.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("pytest: read %s: %w", rel, err)
		}
		out = append(out, p.scanFile(string(body), rel)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ----------------------------------------------------------------
// Per-file scan.
// ----------------------------------------------------------------

var (
	testFnRx    = regexp.MustCompile(`^def\s+(test_[A-Za-z0-9_]+)\s*\(`)
	classDeclRx = regexp.MustCompile(`^class\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(([^)]*)\))?\s*:`)
	anyClass    = regexp.MustCompile(`^class\s+[A-Za-z_]`)

	importRx          = regexp.MustCompile(`^import\s+([A-Za-z0-9_.]+)(?:\s+as\s+([A-Za-z0-9_]+))?\s*$`)
	fromImportStartRx = regexp.MustCompile(`^from\s+([A-Za-z0-9_.]+)\s+import\s+(.+)$`)

	dottedRefRx = regexp.MustCompile(`\b([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)\.([A-Z][A-Za-z0-9_]*)\b`)
	prefixRefRx = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.([A-Z][A-Za-z0-9_]*)\b`)
	bareNameRx  = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\b`)
)

// isTestClass reports whether a class declaration introduces a pytest
// test class. Mirrors pytest's collection rule: the default
// python_classes glob (`Test*`), the common `*Test`/`*Tests` suffix
// convention, and any subclass of a `*TestCase` base (unittest-style).
func isTestClass(name, bases string) bool {
	if strings.HasPrefix(name, "Test") {
		return true
	}
	if strings.HasSuffix(name, "Test") || strings.HasSuffix(name, "Tests") {
		return true
	}
	if strings.Contains(bases, "TestCase") {
		return true
	}
	return false
}

// imports tracks two flavors of Python import:
//
//   - modulePrefix maps an alias (or module name) to the underlying
//     module. Populated by `import X` and `import X as L`. Resolved
//     against `<alias>.<Name>` body references.
//   - bareNames maps an alias (or imported name) to its fully-
//     qualified form ("module.Name"). Populated by `from X import Y`
//     and `from X import Y as Z`. Resolved against bare `Y` body
//     references.
type imports struct {
	modulePrefix map[string]string
	bareNames    map[string]string
}

func (p *Parser) scanFile(src, rel string) []*testcasepb.TestCase {
	lines := strings.Split(src, "\n")
	imps := parseImports(lines)

	var out []*testcasepb.TestCase
	currClass := ""
	currClassIndent := -1
	// nestedSkipIndent suppresses test discovery while inside a class
	// nested under a test class. The "outer-only" rule keeps v1
	// behavior simple; nested test-bearing classes are rare in real
	// corpora.
	nestedSkipIndent := -1

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		indent := leadingIndent(line)

		// Exit nested-class skip when indent drops back to or below
		// the inner class's declaration indent.
		if nestedSkipIndent >= 0 {
			if indent <= nestedSkipIndent {
				nestedSkipIndent = -1
			} else {
				continue
			}
		}

		// Close the current test class scope when indent drops back
		// to its declaration column. Decorators above a sibling get
		// to keep the scope open.
		if currClass != "" && indent <= currClassIndent && !strings.HasPrefix(trim, "@") {
			currClass = ""
			currClassIndent = -1
		}

		// Inside a test class, any *nested* class (test or not) opens
		// a skip scope until indent returns.
		if currClass != "" && indent > currClassIndent && anyClass.MatchString(trim) {
			nestedSkipIndent = indent
			continue
		}

		// Module-scope class declaration. A non-test class resets the
		// scope so its methods aren't discovered.
		if indent == 0 {
			if m := classDeclRx.FindStringSubmatch(trim); m != nil {
				if isTestClass(m[1], m[2]) {
					currClass = m[1]
					currClassIndent = 0
				} else {
					currClass = ""
					currClassIndent = -1
				}
				continue
			}
		}

		// Test function discovery.
		if m := testFnRx.FindStringSubmatch(trim); m != nil {
			testName := m[1]
			var fullName string
			switch {
			case indent == 0:
				fullName = testName
			case currClass != "" && indent > currClassIndent:
				fullName = currClass + "." + testName
			default:
				// Module-indented def outside any test class, or a
				// nested def inside another def — not a pytest test.
				continue
			}
			bodyText := joinBody(lines, i, indent)
			refs := p.extractRefs(bodyText, imps)
			id := rel + "::" + fullName
			out = append(out, &testcasepb.TestCase{
				Id:           id,
				Framework:    "pytest",
				Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(i + 1)},
				Name:         fullName,
				NameTokens:   tokenizePyTestName(fullName),
				SourceHash:   hashBytes([]byte(line)),
				ContractRefs: refs,
			})
		}
	}
	return out
}

// leadingIndent counts leading whitespace characters. A tab counts as
// one unit — Python style is internally consistent per-file, and we
// only ever compare indents to one another within a file.
func leadingIndent(s string) int {
	n := 0
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	return n
}

// joinBody returns the body text of the function declared at lines[i]
// with declaration indent `defIndent`. Body lines extend until the
// first non-blank, non-comment line with indent <= defIndent (or EOF).
func joinBody(lines []string, defLine, defIndent int) string {
	var sb strings.Builder
	for j := defLine + 1; j < len(lines); j++ {
		l := lines[j]
		trim := strings.TrimSpace(l)
		if trim == "" || strings.HasPrefix(trim, "#") {
			sb.WriteString(l)
			sb.WriteByte('\n')
			continue
		}
		if leadingIndent(l) <= defIndent {
			break
		}
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// ----------------------------------------------------------------
// Import tracking.
// ----------------------------------------------------------------

func parseImports(lines []string) imports {
	out := imports{
		modulePrefix: map[string]string{},
		bareNames:    map[string]string{},
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if leadingIndent(line) > 0 {
			continue
		}
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if m := importRx.FindStringSubmatch(trim); m != nil {
			mod := m[1]
			alias := mod
			if m[2] != "" {
				alias = m[2]
			}
			out.modulePrefix[alias] = mod
			continue
		}
		if m := fromImportStartRx.FindStringSubmatch(trim); m != nil {
			mod := m[1]
			rest := m[2]
			// Multi-line parenthesized form: collect lines until ')'.
			if strings.Contains(rest, "(") && !strings.Contains(rest, ")") {
				acc := rest
				for i+1 < len(lines) {
					i++
					acc += " " + strings.TrimSpace(lines[i])
					if strings.Contains(acc, ")") {
						break
					}
				}
				rest = acc
			}
			rest = strings.ReplaceAll(rest, "(", "")
			rest = strings.ReplaceAll(rest, ")", "")
			addFromNames(out.bareNames, mod, rest)
		}
	}
	return out
}

func addFromNames(dst map[string]string, module, names string) {
	for _, p := range strings.Split(names, ",") {
		n := strings.TrimSpace(p)
		if n == "" {
			continue
		}
		var real, alias string
		if idx := strings.Index(n, " as "); idx >= 0 {
			real = strings.TrimSpace(n[:idx])
			alias = strings.TrimSpace(n[idx+4:])
		} else {
			real = n
			alias = n
		}
		if real == "" || alias == "" || real == "*" {
			continue
		}
		dst[alias] = module + "." + real
	}
}

// ----------------------------------------------------------------
// Ref extraction.
// ----------------------------------------------------------------

func (p *Parser) extractRefs(body string, imps imports) []string {
	if body == "" {
		return nil
	}
	stripped := stripPyComments(body)
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	emitDottedAndSlash := func(pkg, name string) {
		add(pkg + "." + name)
		add(pkg + "/" + name)
	}

	// Pattern 1: dotted FQDNs (two or more lowercase dotted segments
	// followed by a CamelCase target). Apply module_aliases to the
	// leading segment.
	for _, m := range dottedRefRx.FindAllStringSubmatch(stripped, -1) {
		pkg := m[1]
		name := m[2]
		canonical := p.applyAliasToModule(pkg)
		emitDottedAndSlash(canonical, name)
		if canonical != pkg {
			// Preserve the original form too — useful when the proto
			// adapter's alias scheme doesn't reverse the codegen rename.
			emitDottedAndSlash(pkg, name)
		}
	}

	// Pattern 1b: alias.Name where alias is a tracked import prefix.
	// Catches `L.LogEntry` from `import pw_log as L` — the dotted
	// regex needs ≥2 dotted segments and doesn't fire on this shape.
	for _, m := range prefixRefRx.FindAllStringSubmatch(stripped, -1) {
		alias := m[1]
		name := m[2]
		mod, ok := imps.modulePrefix[alias]
		if !ok {
			continue
		}
		canonical := p.applyAliasToModule(mod)
		emitDottedAndSlash(canonical, name)
		if canonical != mod {
			emitDottedAndSlash(mod, name)
		}
	}

	// Pattern 2: string-literal refs anchored on idl_prefix.
	if p.idlPrefix != "" {
		rx := p.stringLiteralRegex()
		for _, m := range rx.FindAllStringSubmatch(stripped, -1) {
			full := m[1]
			add(full)
			// Parent ref: drop the trailing /Method or .Method.
			if i := strings.LastIndexAny(full, "/."); i > 0 {
				add(full[:i])
			}
		}
	}

	// Pattern 3: bare names matching a tracked `from X import Y`.
	// Walk every CamelCase token in the stripped body and look it up.
	for _, m := range bareNameRx.FindAllString(stripped, -1) {
		qual, ok := imps.bareNames[m]
		if !ok {
			continue
		}
		canonical := p.applyAliasToQualified(qual)
		if dot := strings.LastIndex(canonical, "."); dot > 0 {
			emitDottedAndSlash(canonical[:dot], canonical[dot+1:])
		} else {
			add(canonical)
		}
		if canonical != qual {
			if dot := strings.LastIndex(qual, "."); dot > 0 {
				emitDottedAndSlash(qual[:dot], qual[dot+1:])
			}
		}
	}

	sort.Strings(out)
	return out
}

// applyAliasToModule rewrites a leading module name (no dots) through
// the configured module_aliases. Returns the input unchanged when no
// alias matches.
func (p *Parser) applyAliasToModule(module string) string {
	if a, ok := p.moduleAliases[module]; ok {
		return a
	}
	return module
}

// applyAliasToQualified rewrites only the leading segment of a dotted
// qualified ref (e.g. "pw_log.LogEntry" → "pw.log.LogEntry" when the
// alias map has `pw_log=pw.log`). Trailing segments pass through.
func (p *Parser) applyAliasToQualified(qual string) string {
	if i := strings.Index(qual, "."); i > 0 {
		head := qual[:i]
		if a, ok := p.moduleAliases[head]; ok {
			return a + qual[i:]
		}
	}
	return qual
}

// stringLiteralRegex matches double-quoted strings shaped
// `<prefix>(.<seg>)+.<Service>[(./)<Method>]?`. The `[./]` middle lets
// us catch both the all-dots form ("pw.log.Logs.Listen") and the gRPC
// method-name form ("pw.log.Logs/Listen") that Python code commonly
// uses for descriptor lookups and stub.invoke() calls.
func (p *Parser) stringLiteralRegex() *regexp.Regexp {
	q := regexp.QuoteMeta(p.idlPrefix)
	return regexp.MustCompile(
		`"(` + q + `(?:\.[a-z][a-z0-9_]*)+(?:\.v[0-9][a-z0-9]*)?\.[A-Z][A-Za-z0-9_]*(?:[./][A-Z][A-Za-z0-9_]*)?)"`)
}

// stripPyComments removes `# ...` line-comment tails so identifiers
// mentioned in comments don't show up as ContractRefs. Conservative:
// honors string-literal boundaries so a `#` inside a quoted string
// doesn't truncate the line. Triple-quoted strings aren't handled
// (no comment chars inside them anyway).
func stripPyComments(body string) string {
	var sb strings.Builder
	for _, raw := range strings.Split(body, "\n") {
		sb.WriteString(stripLineComment(raw))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func stripLineComment(line string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '\\':
			// Escape only matters inside a string literal — outside,
			// `\` is a stray character and the next byte should be
			// processed normally.
			if inSingle || inDouble {
				i++
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

// ----------------------------------------------------------------
// Tokenization + small helpers.
// ----------------------------------------------------------------

// tokenizePyTestName turns `ClassName.test_method_name` into lowercase
// tokens, dropping the "test" prefix marker the same way gotest drops
// "Test". A leading "test" token from a class prefix is also dropped.
func tokenizePyTestName(name string) []string {
	var out []string
	for _, part := range strings.Split(name, ".") {
		part = strings.TrimPrefix(part, "test_")
		for _, w := range strings.FieldsFunc(part, func(r rune) bool {
			return r == '_' || r == '-' || r == ' '
		}) {
			out = append(out, splitCamel(w)...)
		}
	}
	return out
}

// splitCamel splits an identifier on CamelCase boundaries with
// acronym awareness — same rule as gotest.splitCamel. "HTTPServer" →
// ["http","server"]. A leading "test" token is dropped.
func splitCamel(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
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
				if next >= 'a' && next <= 'z' {
					flush()
				}
			}
		}
		cur = append(cur, r)
	}
	flush()
	if len(out) > 0 && out[0] == "test" {
		out = out[1:]
	}
	return out
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
