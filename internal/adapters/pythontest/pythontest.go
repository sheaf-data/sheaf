// Package pythontest implements the python-test test-parser adapter: the
// Python analog of the rust-test ffx subprocess-invocation extractor.
//
// Honeydew (Fuchsia's Python end-to-end framework) drives the device by
// running real `ffx <subcommand> --<flags>` invocations through its FFX
// transport (`ffx.run(["target", "list", "--no-probe"])`). Those e2e files
// are the substantive *behavior* testing of ffx's flags — but they are
// invisible to a Rust-only test corpus, so ffx's flag-test coverage scores
// near zero despite the behavior being exercised. This adapter walks the
// configured Python files, extracts each ffx invocation, and emits a
// TestCase whose ContractRefs are the canonical command + flag element IDs
// (`ffx target list`, `ffx target list --format`) so the indexer's
// direct-ref matcher credits those elements.
//
// Extraction strategy (two tiers):
//
//   - Preferred: a bundled Python `ast` helper (extract_ffx_invocations.py)
//     run as a subprocess. The AST is robust to multi-line calls, locally
//     built command lists (`cmd = [...]; cmd.extend([...])`), module-level
//     constant lists, and dict-constant subscripts (`_FFX_CMDS["X"]`). It
//     elides dynamic elements and reports what it could not resolve, so
//     nothing is silently truncated. `gen-ffx-coverage-inputs.py` already
//     makes Python-at-scan-time a prerequisite for ffx, so this adds no new
//     dependency for the intended use.
//
//   - Fallback: a pure-Go regex pass for the inline-literal-list idiom,
//     used only when no python3 interpreter is available (or the helper
//     errors). It logs the invocations it cannot extract rather than
//     dropping them quietly.
//
// Scope tags: the adapter framework (walk files, run a helper, canonicalize
// via internal/ffxinvoke, emit TestCases) is Generic — any CLI whose tests
// are Python e2e. The ffx-receiver recognition (`.ffx(`/`_ffx_transport`/
// `class FFX` self.run) is Honeydew-/ffx-provider-specific and lives in the
// bundled helper + the fallback regex, kept separable from the framework.
package pythontest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/ffxinvoke"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

const Name = "python-test"
const Version = "0.1.0"

type Parser struct {
	include []string
	exclude []string
}

type Config struct {
	Include []string
	Exclude []string
}

func New(cfg Config) *Parser {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.py"}
	}
	exclude := cfg.Exclude
	if len(exclude) == 0 {
		exclude = []string{"**/third_party/**"}
	}
	return &Parser{include: include, exclude: exclude}
}

func (p *Parser) Name() string                  { return Name }
func (p *Parser) Version() string               { return Version }
func (p *Parser) SupportedFrameworks() []string { return []string{"python-test"} }

func (p *Parser) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*testcasepb.TestCase, error) {
	// Gate on a CLI-shaped scope binary, exactly like the rust-test
	// invocation extractor: a no-op unless ffx is among the scope
	// libraries, so a non-ffx python-test config is preserved untouched.
	if !cliScopeHasFfx(scope) {
		return nil, nil
	}
	runner := newHelperRunner()
	var out []*testcasepb.TestCase
	err := adapters.WalkMatching(repoRoot, p.include, p.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := joinRepo(repoRoot, rel)
		invs, stats := runner.extract(abs, rel)
		if len(invs) == 0 {
			logSkips(rel, stats)
			return nil
		}
		out = append(out, p.testCasesForFile(rel, invs)...)
		logSkips(rel, stats)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// invocation is one extracted ffx call: its literal arg tokens (subcommand
// path + flags, without the leading "ffx"), the source line, the enclosing
// function name, and whether the call carried any un-resolvable element.
type invocation struct {
	Args    []string `json:"args"`
	Line    int      `json:"line"`
	Func    string   `json:"func"`
	Dynamic bool     `json:"dynamic"`
}

// extractStats records what extraction could not turn into a command path,
// so a regen surfaces it rather than truncating silently.
type extractStats struct {
	fullyDynamic int  // calls whose command-arg resolved to no literal token
	lossy        int  // resolved calls that still elided ≥1 dynamic element
	usedFallback bool // the Go regex path ran (no python3 / helper error)
}

func (p *Parser) testCasesForFile(rel string, invs []invocation) []*testcasepb.TestCase {
	// Group invocations by enclosing function so each emitted TestCase maps
	// to a test/affordance method. Invocations outside any function (rare)
	// group under a synthetic file-level name.
	type group struct {
		name  string
		line  int
		refs  []string
		order int
	}
	groups := map[string]*group{}
	var orderCounter int
	for _, inv := range invs {
		cmdRefs, flagRefs, _ := ffxinvoke.Canonicalize(inv.Args)
		if len(cmdRefs) == 0 && len(flagRefs) == 0 {
			continue
		}
		fn := inv.Func
		if fn == "" {
			fn = "<ffx-invocations>"
		}
		g := groups[fn]
		if g == nil {
			g = &group{name: fn, line: inv.Line, order: orderCounter}
			orderCounter++
			groups[fn] = g
		}
		g.refs = appendDedup(g.refs, cmdRefs)
		g.refs = appendDedup(g.refs, flagRefs)
	}
	// Stable output order: by first-seen function.
	ordered := make([]*group, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if ordered[j].order < ordered[i].order {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}
	moduleSuite := deriveModuleSuite(rel)
	var out []*testcasepb.TestCase
	for _, g := range ordered {
		if len(g.refs) == 0 {
			continue
		}
		id := moduleSuite + "::" + g.name
		out = append(out, &testcasepb.TestCase{
			Id:        id,
			Framework: "python-test",
			Location: &commonpb.SourceLocation{
				Path: rel,
				Line: uint32(g.line),
			},
			Name:         g.name,
			NameTokens:   tokenizeFnName(g.name),
			SourceHash:   hashString(id + "|" + strings.Join(g.refs, ",")),
			ContractRefs: g.refs,
		})
	}
	return out
}

// ----------------------------------------------------------------
// Helper runner: prefer the bundled Python AST helper, fall back to
// the pure-Go regex extractor.
// ----------------------------------------------------------------

type helperRunner struct {
	once       sync.Once
	pythonPath string // resolved python3, "" if unavailable
	scriptPath string // temp file holding the bundled helper, "" if unavailable
}

func newHelperRunner() *helperRunner { return &helperRunner{} }

func (h *helperRunner) init() {
	h.once.Do(func() {
		py := findPython()
		if py == "" {
			return
		}
		// Materialize the embedded helper to a temp file so python can run it.
		f, err := os.CreateTemp("", "sheaf-ffx-extract-*.py")
		if err != nil {
			return
		}
		if _, err := f.WriteString(extractHelperSource); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return
		}
		_ = f.Close()
		h.pythonPath = py
		h.scriptPath = f.Name()
	})
}

func (h *helperRunner) extract(absPath, rel string) ([]invocation, extractStats) {
	h.init()
	if h.pythonPath != "" && h.scriptPath != "" {
		invs, stats, ok := h.runAST(absPath)
		if ok {
			return invs, stats
		}
		// Helper errored on this file (syntax error, etc.) — fall through to
		// the regex path so the file isn't silently dropped.
	}
	invs, stats := extractRegex(absPath)
	stats.usedFallback = true
	return invs, stats
}

func (h *helperRunner) runAST(absPath string) ([]invocation, extractStats, bool) {
	cmd := exec.Command(h.pythonPath, h.scriptPath, absPath)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, extractStats{}, false
	}
	var res struct {
		Invocations  []invocation `json:"invocations"`
		FullyDynamic int          `json:"fully_dynamic"`
		Error        string       `json:"error"`
	}
	if err := json.Unmarshal(stdout, &res); err != nil {
		return nil, extractStats{}, false
	}
	if res.Error != "" {
		return nil, extractStats{}, false
	}
	stats := extractStats{fullyDynamic: res.FullyDynamic}
	for _, inv := range res.Invocations {
		if inv.Dynamic {
			stats.lossy++
		}
	}
	return res.Invocations, stats, true
}

// findPython resolves a python3 interpreter, preferring python3.
func findPython() string {
	for _, c := range []string{"python3", "python"} {
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
	}
	return ""
}

func logSkips(rel string, stats extractStats) {
	if stats.fullyDynamic == 0 && !stats.usedFallback {
		return
	}
	switch {
	case stats.usedFallback && stats.fullyDynamic > 0:
		log.Printf("python-test: %s: extracted via Go regex fallback (no python3 / helper error); %d ffx invocation(s) were fully dynamic (no literal subcommand)", rel, stats.fullyDynamic)
	case stats.usedFallback:
		log.Printf("python-test: %s: extracted via Go regex fallback (no python3 / helper error)", rel)
	default:
		log.Printf("python-test: %s: skipped %d ffx invocation(s): fully dynamic (no literal subcommand)", rel, stats.fullyDynamic)
	}
}

// ----------------------------------------------------------------
// Small helpers.
// ----------------------------------------------------------------

func appendDedup(dst, src []string) []string {
	if len(src) == 0 {
		return dst
	}
	seen := make(map[string]bool, len(dst))
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range src {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		dst = append(dst, s)
	}
	return dst
}

func cliScopeHasFfx(scope adapters.ScopeConfig) bool {
	for _, l := range append(append([]string{}, scope.Libraries...), scope.AlsoInclude...) {
		if strings.TrimSpace(l) == ffxinvoke.Binary {
			return true
		}
	}
	return false
}

func joinRepo(repoRoot, rel string) string {
	if strings.HasSuffix(repoRoot, "/") {
		return repoRoot + rel
	}
	return repoRoot + "/" + rel
}

// deriveModuleSuite turns a repo-relative .py path into a ::-joined suite
// name, mirroring rusttest.deriveModuleSuite.
func deriveModuleSuite(rel string) string {
	s := strings.TrimSuffix(rel, ".py")
	return strings.ReplaceAll(s, "/", "::")
}

func tokenizeFnName(fn string) []string {
	var tokens []string
	for _, w := range strings.FieldsFunc(fn, func(r rune) bool { return r == '_' || r == '.' || r == '-' }) {
		w = strings.TrimPrefix(w, "test")
		if w != "" {
			tokens = append(tokens, strings.ToLower(w))
		}
	}
	return tokens
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
