// Package gotest implements the gotest test-parser adapter.
//
// Recognizes Go test functions matching `func TestXxx(t *testing.T)`
// and `func TestXxx(t *testing.B)` (the standard testing.T and
// testing.B signatures), plus subtests spawned via t.Run("name", ...).
//
// Each TestCase carries:
//   - Name: the Go function name (or function/subtest path)
//   - NameTokens: CamelCase-tokenized + path-tokenized identifiers
//     used by the indexer's name-overlap matcher
//   - Framework: "gotest"
//   - ContractRefs (when BinaryName is set): refs derived from
//     cobra-style invocations in the test body — see
//     extractCobraInvocations below.
//
// Without BinaryName the adapter falls back to name-token matching
// only. With BinaryName configured, the adapter also runs a "cobra
// invocation extractor" that pre-populates ContractRefs on every
// TestCase by scanning the file for:
//
//   - exec.Command("docker", "run", "--rm", ...)
//   - icmd.Command/icmd.RunCommand(...)
//   - cmd.SetArgs([]string{"--platform", "..."})
//   - struct-literal `args:` / `flags:` / `cliArgs:` fields
//
// The indexer's Strategy 1 (direct-ref match against canonical ID
// or any alias) then catches matches that name-token overlap would
// miss — most importantly tests like `TestSigProxyWithTTY` that
// drive flags through subprocess args.

package gotest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

const Name = "gotest"
const Version = "0.1.0"

type Parser struct {
	include    []string
	exclude    []string
	binaryName string

	testFuncRx *regexp.Regexp // func TestXxx(t *testing.T)
	tRunRx     *regexp.Regexp // t.Run("subtest name", ...)
}

type Config struct {
	Include []string
	Exclude []string
	// BinaryName enables the cobra-invocation extractor (Fix A in the
	// docker spot-check writeup). Set this to the CLI's binary name
	// (e.g. "docker") for any cobra-based scan. Empty disables the
	// extractor; the adapter then falls back to name-token matching
	// only, which is correct for non-CLI Go projects.
	BinaryName string
}

func New(cfg Config) *Parser {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*_test.go"}
	}
	exclude := cfg.Exclude
	if len(exclude) == 0 {
		exclude = []string{"**/vendor/**", "**/.git/**"}
	}
	return &Parser{
		include:    include,
		exclude:    exclude,
		binaryName: cfg.BinaryName,
		// Match: func TestFoo(t *testing.T) | func BenchmarkFoo(b *testing.B)
		// | func FuzzFoo(f *testing.F) | func ExampleFoo()
		// (Example funcs have no params.)
		testFuncRx: regexp.MustCompile(`(?m)^func\s+(Test\w+|Benchmark\w+|Fuzz\w+|Example\w*)\s*\([^)]*\)\s*\{`),
		// t.Run("name", ...) — name is a quoted string literal. Both
		// "double" and `backtick` quoted forms are accepted.
		tRunRx: regexp.MustCompile(`(?m)\bt\.Run\(\s*(?:"([^"]+)"|` + "`([^`]+)`" + `)\s*,`),
	}
}

func (p *Parser) Name() string                  { return Name }
func (p *Parser) Version() string               { return Version }
func (p *Parser) SupportedFrameworks() []string { return []string{"gotest"} }

func (p *Parser) Discover(ctx context.Context, repoRoot string, _ adapters.ScopeConfig) ([]*testcasepb.TestCase, error) {
	var out []*testcasepb.TestCase
	err := adapters.WalkMatching(repoRoot, p.include, p.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("gotest: read %s: %w", rel, err)
		}
		offsets := computeLineOffsets(body)
		pkgSuite := packageSuiteFromPath(rel)

		// Per-FUNCTION ContractRefs from the cobra invocation extractor.
		// Previously emitted file-wide; that caused massive false-positive
		// attribution because a single `--validate` literal in any test
		// in a file would attribute every test in the file to
		// `kubectl apply --validate`. Now we bound each extraction to
		// the function body (the brace-balanced region between the
		// function's opening `{` and its matching `}`), so refs only
		// attribute to the test that actually used them.
		//
		// testFuncs[i] = function-body byte range for the test at match index i.
		testFuncMatches := p.testFuncRx.FindAllSubmatchIndex(body, -1)
		funcBodyRanges := make([][2]int, len(testFuncMatches))
		for i, m := range testFuncMatches {
			s, e := goFunctionBodyRange(body, m[1])
			funcBodyRanges[i] = [2]int{s, e}
		}

		// Outer Test/Benchmark/Fuzz/Example functions.
		for i, m := range testFuncMatches {
			fn := string(body[m[2]:m[3]])
			line := lineFromOffset(offsets, m[0])
			id := pkgSuite + "::" + fn
			var refs []string
			if p.binaryName != "" {
				start, end := funcBodyRanges[i][0], funcBodyRanges[i][1]
				refs = extractCobraInvocations(body[start:end], rel, p.binaryName)
			}
			out = append(out, &testcasepb.TestCase{
				Id:           id,
				Framework:    "gotest",
				Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
				Name:         fn,
				NameTokens:   tokenizeGoTestName(fn),
				SourceHash:   hashBytes(body[m[0]:m[1]]),
				ContractRefs: refs,
			})
		}

		// Subtests via t.Run("...", func(t *testing.T) { ... }).
		// We emit one TestCase per subtest call site. Subtests are
		// strongly informative: docker tests commonly use t.Run names
		// like "with --rm" or "container start".
		//
		// Subtest body bounding: we find the function literal that
		// follows the t.Run( call and brace-scan from its opening `{`.
		for _, m := range p.tRunRx.FindAllSubmatchIndex(body, -1) {
			var name string
			if m[2] >= 0 {
				name = string(body[m[2]:m[3]])
			} else if m[4] >= 0 {
				name = string(body[m[4]:m[5]])
			}
			if name == "" {
				continue
			}
			line := lineFromOffset(offsets, m[0])
			id := pkgSuite + "::" + name
			var refs []string
			if p.binaryName != "" {
				// Find the function-body `{` after the t.Run match.
				start, end := goFunctionBodyRange(body, m[1])
				if end > start {
					refs = extractCobraInvocations(body[start:end], rel, p.binaryName)
				}
			}
			out = append(out, &testcasepb.TestCase{
				Id:           id,
				Framework:    "gotest",
				Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
				Name:         name,
				NameTokens:   tokenizeSubtestName(name),
				SourceHash:   hashBytes(body[m[0]:m[1]]),
				ContractRefs: refs,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// packageSuiteFromPath turns "cli/command/container/run_test.go" into
// "cli/command/container/run_test" — a stable suite identifier.
func packageSuiteFromPath(rel string) string {
	if i := strings.LastIndex(rel, "."); i >= 0 {
		rel = rel[:i]
	}
	return rel
}

// tokenizeGoTestName splits a Go test name into CamelCase tokens,
// dropping the "Test"/"Benchmark"/"Fuzz"/"Example" prefix because
// it's structural, not semantic.
//
//	"TestContainerRun"        → ["container", "run"]
//	"BenchmarkRunWithVolume"  → ["run", "with", "volume"]
//	"ExampleClient_Do"        → ["client", "do"]
//	"Test_run_with_volume"    → ["run", "with", "volume"]
func tokenizeGoTestName(fn string) []string {
	for _, prefix := range []string{"Benchmark", "Example", "Test", "Fuzz"} {
		if strings.HasPrefix(fn, prefix) {
			fn = strings.TrimPrefix(fn, prefix)
			break
		}
	}
	// Strip a leading underscore if any (e.g. "Test_foo" or "Test_Foo").
	fn = strings.TrimLeft(fn, "_")
	return splitMixed(fn)
}

// tokenizeSubtestName lowercases and splits a t.Run name into tokens.
// Subtests are free-form strings: spaces, underscores, dashes,
// dots, and slashes all delimit tokens.
func tokenizeSubtestName(name string) []string {
	return splitMixed(name)
}

// splitMixed splits an identifier on CamelCase boundaries AND on
// any of [space _ - . / +].
func splitMixed(s string) []string {
	var out []string
	for _, piece := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-' || r == '.' || r == '/' || r == '+' || r == ','
	}) {
		out = append(out, splitCamel(piece)...)
	}
	return out
}

// splitCamel splits a Go identifier on CamelCase boundaries, with
// acronym awareness so "APIResources" splits as ["api","resources"]
// rather than the surprising ["apiresources"] you get from a strict
// lower→upper-only rule. The acronym rule: at any position i where
// s[i] is uppercase and s[i+1] is lowercase, flush before s[i].
// This matches the convention go/format and most camel-splitters
// follow ("HTTPServer" → ["http","server"], "XMLParser" →
// ["xml","parser"], "TestAPIResourcesRun" →
// ["test","api","resources","run"]). Mirrors the matching logic in
// internal/indexer/indexer.go so element IDs and test names produce
// comparable token sets.
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
			// Standard boundary: aB → split before B.
			if prevLower {
				flush()
			} else if i+1 < len(runes) {
				// Acronym boundary: ABc → split before B (e.g.
				// "APIResources" → "API" then start "Resources").
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
	return out
}

// goFunctionBodyRange returns the [open, close) byte offsets of the
// function body bounded by `{` and the matching `}`. start is the
// position right after the matched `func TestXxx(...) {` prefix —
// which is at OR just past the opening `{`. The scan walks forward
// from start; if start is already inside the body (past `{`), we
// honor depth=1 from the get-go. Tracks string, rune, and comment
// context to avoid counting braces inside literals.
//
// Returns (start, end) where end is the offset of the matching `}`
// (exclusive). On unbalanced braces returns (start, len(body)).
func goFunctionBodyRange(body []byte, start int) (int, int) {
	// testFuncRx CONSUMES the opening `{` of the function declaration,
	// so `start` (its m[1]) already lies one byte past `{`. We MUST
	// NOT walk forward looking for another `{` — that was the original
	// bug here: scanning forward silently extended the "function body"
	// through the first nested `{` and let cobra-invocation refs leak
	// across adjacent test functions. Begin the brace-balanced scan
	// immediately at depth 1.
	if start >= len(body) {
		return start, len(body)
	}
	// Defensive: if for some reason `start` happens to point AT `{`
	// rather than just past it, step over it.
	if body[start] == '{' {
		start++
	}
	open := start
	i := open
	depth := 1
	for i < len(body) && depth > 0 {
		switch body[i] {
		case '"':
			i++
			for i < len(body) {
				if body[i] == '\\' && i+1 < len(body) {
					i += 2
					continue
				}
				if body[i] == '"' {
					i++
					break
				}
				i++
			}
		case '`':
			// Raw string literal.
			i++
			for i < len(body) && body[i] != '`' {
				i++
			}
			if i < len(body) {
				i++
			}
		case '\'':
			// Rune literal.
			i++
			for i < len(body) {
				if body[i] == '\\' && i+1 < len(body) {
					i += 2
					continue
				}
				if body[i] == '\'' {
					i++
					break
				}
				i++
			}
		case '/':
			if i+1 < len(body) && body[i+1] == '/' {
				for i < len(body) && body[i] != '\n' {
					i++
				}
			} else if i+1 < len(body) && body[i+1] == '*' {
				i += 2
				for i+1 < len(body) && !(body[i] == '*' && body[i+1] == '/') {
					i++
				}
				if i+1 < len(body) {
					i += 2
				}
			} else {
				i++
			}
		case '{':
			depth++
			i++
		case '}':
			depth--
			i++
		default:
			i++
		}
	}
	if depth != 0 {
		return open, len(body)
	}
	return open, i - 1
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

// ============================================================
// Cobra invocation extractor (Fix A from the docker spot-check).
//
// The patterns below cover the dominant test idioms in docker/cli
// and most other cobra-based CLIs:
//
//  1. Subprocess invocations:
//       exec.Command("docker", "run", "--rm", ...)
//       icmd.Command("docker", "container", "ls", "-a")
//       icmd.RunCommand("docker", "run", ...)
//     The leading positional string literals are the command path;
//     anything starting with `-` is a flag.
//
//  2. Direct cobra arg injection:
//       cmd.SetArgs([]string{"--platform", "linux/amd64"})
//     The flags apply to whatever subcommand the test is in;
//     resolution comes from the test file's location.
//
//  3. Struct-literal flag tables:
//       args:  []string{"--platform", "..."}
//       flags: []string{"--mac-address", "..."}
//     Same file-based resolution as (2).
//
// File-based resolution emits TWO candidate refs per flag — the
// family-prefixed form ("docker container run --rm") and the bare
// form ("docker run --rm"). The cobra adapter's alias dedup makes
// one of these the canonical element ID and the other an alias, so
// Strategy 1 in the indexer matches whichever is real.
// ============================================================

var (
	subprocessCallRx = regexp.MustCompile(
		`(?s)(?:exec|icmd)\.(?:Command|RunCommand)\(([^)]*)\)`,
	)
	structArgsRx = regexp.MustCompile(
		`(?s)(?:args|flags|cliArgs|argv|cmdline|cmdLine|opts|cmdArgs)\s*:\s*\[\]string\{([^}]*)\}`,
	)
	setArgsRx = regexp.MustCompile(
		`(?s)\.SetArgs\(\s*\[\]string\{([^}]*)\}`,
	)
	stringLitRx = regexp.MustCompile(`"((?:\\.|[^"\\])*)"`)
	// newCmdRx matches NewCmd<Foo>( calls — the canonical cobra
	// command-construction pattern in kubectl / sheaf / etc. Used as
	// evidence the test wires up cobra plumbing even when it doesn't
	// produce any string-literal args.
	newCmdRx = regexp.MustCompile(`\bNewCmd[A-Z][A-Za-z0-9_]*\(`)
	// longFlagInStringRx finds --flag tokens inside a string literal —
	// used by the liberal per-flag pass for CLIs that assemble their arg
	// lists at runtime (gh: cli:"--x" + shlex.Split + SetArgs(var)), so
	// the []string{}-literal passes never see a flag. Lowercase long form
	// only, matching the cobra flag convention.
	longFlagInStringRx = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
)

// extractCobraInvocations scans a Go test file body for cobra-
// invocation patterns and returns "<commandID> <flag>" refs.
// Deduplicated; safe to share across every TestCase in the file.
func extractCobraInvocations(body []byte, fileRel, binaryName string) []string {
	if binaryName == "" {
		return nil
	}
	seen := make(map[string]bool)
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
		}
	}

	// Pass 1: subprocess invocations. Command path embedded in args.
	for _, m := range subprocessCallRx.FindAllSubmatchIndex(body, -1) {
		args := extractStringLiterals(body[m[2]:m[3]])
		for _, ref := range refsFromSubprocessArgs(args, binaryName) {
			add(ref)
		}
	}

	// Pass 2: struct-literal args/flags fields + SetArgs. Command
	// path comes from the file location. ANY struct-args / SetArgs
	// match — even one with no flag literals — is evidence the test
	// drives the cobra command, so we emit each candidate command's
	// bare ID as a ref (lets the SUBCOMMAND direct-evidence guard
	// attribute) plus one ref per flag found.
	candidates := candidateCommandsFromPath(fileRel, binaryName)
	if len(candidates) > 0 {
		extract := func(captured [][]int) {
			for _, m := range captured {
				args := extractStringLiterals(body[m[2]:m[3]])
				// Bare-command refs: any args block at all means
				// the candidate commands are being driven.
				if len(args) > 0 {
					for _, cmd := range candidates {
						add(cmd)
					}
				}
				for _, a := range args {
					if !isFlagLiteral(a) {
						continue
					}
					flag := flagNameOnly(a)
					for _, cmd := range candidates {
						add(cmd + " " + flag)
					}
				}
			}
		}
		extract(structArgsRx.FindAllSubmatchIndex(body, -1))
		extract(setArgsRx.FindAllSubmatchIndex(body, -1))
	}

	// Pass 3: NewCmdX() construction. Tests that wire up a cobra
	// command (`cmd := NewCmdGet(tf, ioStreams)`) and call
	// cmd.Run(...) or cmd.Execute() are exercising the cobra surface
	// even if no string-literal args appear. Detect by matching
	// `NewCmd[A-Z]\w+\(` followed by a `cmd.Run` / `cmd.Execute` /
	// `cmd.SetArgs` later in the same body. The presence of NewCmdX
	// alone is enough evidence; we emit each candidate command from
	// the file path as a bare ref.
	if len(candidates) > 0 && newCmdRx.Match(body) {
		for _, cmd := range candidates {
			add(cmd)
		}
	}

	// Pass 4: liberal per-flag attribution. CLIs like gh assemble their
	// arg lists at runtime (cli:"--x" string fields, shlex.Split,
	// SetArgs(var)), so the []string{}-literal passes above see no flags.
	// Scan every string literal for --flag tokens and attribute each to
	// the command this file maps to; the indexer's join drops any that
	// aren't a real flag of that command — liberal by design (recall over
	// precision), mirroring markdowncli's code-fence per-flag scan.
	if cmd := commandPathFromTestFile(fileRel, binaryName); cmd != "" {
		for _, lit := range extractStringLiterals(body) {
			for _, fm := range longFlagInStringRx.FindAllStringSubmatch(lit, -1) {
				add(cmd + " --" + fm[1])
			}
		}
	}

	// Stable order for determinism.
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sortStrings(out)
	return out
}

// refsFromSubprocessArgs parses a flat list of string literals from
// an exec.Command/icmd.Command/icmd.RunCommand invocation and returns:
//   - the inferred command path itself as a bare SUBCOMMAND ref
//     ("kubectl get") so the indexer's direct-evidence guard for
//     SUBCOMMAND elements can fire on tests that invoke cobra without
//     setting any flags
//   - one ref per --flag found, all prefixed with that command path
//     ("kubectl get --output")
func refsFromSubprocessArgs(args []string, binaryName string) []string {
	if len(args) == 0 {
		return nil
	}
	// First literal must be the binary name (or strip a leading equal
	// match anyway). Variables in this position aren't string literals
	// and won't be in args; we just skip those invocations.
	start := 0
	if args[0] == binaryName {
		start = 1
	} else {
		// Not a docker invocation we recognize. Skip.
		return nil
	}
	// Collect positional command path up to the first --flag / -x.
	cmd := []string{binaryName}
	i := start
	for i < len(args) && !isFlagLiteral(args[i]) {
		cmd = append(cmd, args[i])
		i++
	}
	cmdID := strings.Join(cmd, " ")
	refs := []string{cmdID}
	// Emit one ref per flag literal.
	for ; i < len(args); i++ {
		if isFlagLiteral(args[i]) {
			refs = append(refs, cmdID+" "+flagNameOnly(args[i]))
		}
	}
	return refs
}

// candidateCommandsFromPath maps a test file's repo-relative path
// to one or more candidate command IDs. Returns nil for utility
// files where attribution is ambiguous (opts_test.go, client_test.go,
// etc.) — those are better left to name-token matching.
//
//	cli/command/container/run_test.go     → ["docker container run", "docker run"]
//	cli/command/registry/login_test.go    → ["docker registry login", "docker login"]
//	cli/command/container/opts_test.go    → nil (utility file)
//	e2e/container/run_test.go             → ["docker container run", "docker run"]
//
// The family-prefixed and bare forms both get emitted because the
// cobra adapter's alias dedup may have chosen either as canonical;
// the indexer's Strategy 1 picks up whichever is the real element.
func candidateCommandsFromPath(fileRel, binaryName string) []string {
	if binaryName == "" {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(fileRel), "/")
	if len(parts) == 0 {
		return nil
	}
	name := strings.TrimSuffix(parts[len(parts)-1], "_test.go")
	if name == "" || utilityFileNames[name] {
		return nil
	}
	family := ""
	if len(parts) >= 2 {
		family = parts[len(parts)-2]
	}
	var refs []string
	if family != "" && family != name && !utilityFileNames[family] && !genericDirs[family] {
		refs = append(refs, binaryName+" "+family+" "+name)
	}
	refs = append(refs, binaryName+" "+name)
	return refs
}

// commandPathFromTestFile resolves a test file's repo-relative path to
// the single full command path it exercises — generalizing
// candidateCommandsFromPath beyond docker's one-level layout to gh's and
// kubectl's deeper nesting. It takes the path segments after the last
// "cmd"/"command" marker, collapses a filename that duplicates its
// parent directory (gh's pr/list/list_test.go → "pr list", not
// "pr list list"), and drops a leading directory equal to the binary
// name (cmd/sheaf/… → "sheaf …"). Returns "" for utility files or when
// no command path resolves.
func commandPathFromTestFile(fileRel, binaryName string) string {
	if binaryName == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(fileRel), "/")
	if len(parts) == 0 {
		return ""
	}
	leaf := strings.TrimSuffix(parts[len(parts)-1], "_test.go")
	if leaf == "" || utilityFileNames[leaf] {
		return ""
	}
	root := -1
	for i, p := range parts {
		if p == "cmd" || p == "command" {
			root = i
		}
	}
	var segs []string
	if root >= 0 {
		segs = append(segs, parts[root+1:len(parts)-1]...)
	} else if len(parts) >= 2 {
		segs = append(segs, parts[len(parts)-2])
	}
	if len(segs) > 0 && segs[0] == binaryName {
		segs = segs[1:]
	}
	if len(segs) == 0 || segs[len(segs)-1] != leaf {
		segs = append(segs, leaf)
	}
	out := []string{binaryName}
	for _, s := range segs {
		if s == "" || genericDirs[s] || utilityFileNames[s] {
			continue
		}
		out = append(out, s)
	}
	if len(out) < 2 {
		return ""
	}
	return strings.Join(out, " ")
}

// utilityFileNames are filenames whose `<name>_test.go` form doesn't
// correspond to a subcommand. Tests in these files are typically
// testing helper/utility code shared across many subcommands.
var utilityFileNames = map[string]bool{
	"client":              true,
	"opts":                true,
	"helpers":             true,
	"cli":                 true,
	"completion":          true,
	"formatter":           true,
	"utils":               true,
	"telemetry":           true,
	"defaultcontextstore": true,
	"inspector":           true,
	"main":                true,
	"cobra":               true,
	"required":            true,
	"aliases_utils":       true,
	"docker":              true, // cmd/docker/docker_test.go is bootstrap
	"completions":         true,
	"builder_windows":     true,
}

// genericDirs are directory names that don't correspond to a
// subcommand family — they're internal-layout grouping.
var genericDirs = map[string]bool{
	"internal": true,
	"pkg":      true,
	"cmd":      true,
	"command":  true,
	"test":     true,
	"tests":    true,
}

// extractStringLiterals returns every "..." string literal in the
// given byte slice, with simple escapes resolved. Honors backslash
// escapes inside the quotes.
func extractStringLiterals(b []byte) []string {
	var out []string
	for _, m := range stringLitRx.FindAllSubmatch(b, -1) {
		s := string(m[1])
		// Resolve a few common escapes; full Go-style decoding isn't
		// needed for the strings we care about (flags + simple values).
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\n`, "\n")
		s = strings.ReplaceAll(s, `\t`, "\t")
		s = strings.ReplaceAll(s, `\\`, `\`)
		out = append(out, s)
	}
	return out
}

// isFlagLiteral reports whether s looks like a CLI flag literal.
// Accepts long form ("--platform"), short form ("-a"), and the
// combined-value form ("--platform=linux/amd64").
func isFlagLiteral(s string) bool {
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	if len(s) == 2 {
		// Short form like "-a" — accept iff the char is alnum.
		c := s[1]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
	}
	if s[1] == '-' {
		// Long form must be "--<alnum>...".
		if len(s) < 3 {
			return false
		}
		c := s[2]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return false
}

// flagNameOnly strips any "=value" suffix from a flag literal.
//
//	"--platform=linux"  → "--platform"
//	"--platform"        → "--platform"
//	"-a"                → "-a"
func flagNameOnly(s string) string {
	if i := strings.Index(s, "="); i >= 0 {
		return s[:i]
	}
	return s
}

func sortStrings(s []string) {
	// Avoid pulling in sort just for this; insertion sort is fine for
	// the small slices we produce.
	for i := 1; i < len(s); i++ {
		v := s[i]
		j := i - 1
		for j >= 0 && s[j] > v {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = v
	}
}
