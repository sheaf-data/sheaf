// Package llmextract is an experimental contract-anchor adapter that
// uses an LLM to extract a repo's public API surface, instead of the
// language-specific regex adapters (cppheader, fidl, …).
//
// The design rule that keeps an LLM out of an anti-hallucination tool's
// trust boundary: the model PROPOSES, a deterministic verifier DISPOSES.
// Every element the model emits must carry a 1-based source line; the
// adapter checks the cited line actually contains the symbol's local
// name and drops anything that fails. Hallucinated elements cannot
// survive because their citation does not check out.
//
// Determinism: each file's extraction is cached on disk keyed by
// sha256(model + promptVersion + fileContent), so a re-scan with a
// pinned model is reproducible and free. (Across model upgrades the
// output may shift — the golden becomes a semantic snapshot, not a
// byte-identical compare.)
//
// It is wired into the config oneof and the orchestrator as the LLM
// contract tier (selected by `sheaf scan --auto`), and is also consumed
// directly by cmd/llmextract-eval, which measures its recall/precision
// against the deterministic cppheader adapter on a structured repo (where
// cppheader supplies the answer key).
package llmextract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/llm"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

const Name = "llmextract"

// promptVersion is part of the cache key: bump it when the prompt
// changes so stale extractions are not reused.
const promptVersion = "v1"

// Config tunes the adapter.
type Config struct {
	Include  []string
	Exclude  []string
	CacheDir string // disk cache for per-file extractions; "" disables caching
}

// Adapter implements adapters.ContractAnchorParser via an llm.Client.
type Adapter struct {
	include  []string
	exclude  []string
	cacheDir string
	client   llm.Client
}

// Stats reports verification accounting for one Discover run.
type Stats struct {
	Proposed     int // elements the model emitted
	CiteVerified int // survived citation verification (== len(elements))
	CiteDropped  int // dropped: cited line did not contain the symbol
	FilesScanned int
	CacheHits    int
	FilesFailed  int // files whose extraction errored (e.g. model timeout); skipped, not fatal
}

// New builds the adapter. client must be a working llm.Client (e.g.
// ollama.NewClient). A nil client makes Discover return an error.
func New(cfg Config, client llm.Client) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.h", "**/*.hpp"}
	}
	return &Adapter{
		include:  include,
		exclude:  cfg.Exclude,
		cacheDir: cfg.CacheDir,
		client:   client,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return promptVersion }

// rawElement is the model's per-element JSON shape.
type rawElement struct {
	Name string `json:"name"` // fully-qualified C++ name, or the bare macro name
	Kind string `json:"kind"` // class|method|free_function|macro|enum|type
	Line int    `json:"line"` // 1-based source line
}

// Discover walks the include globs, extracts elements per file via the
// LLM, verifies citations, and returns the surviving ContractElements.
// The accompanying Stats is returned via DiscoverWithStats; Discover
// satisfies the adapters.ContractAnchorParser interface.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	elems, _, err := a.DiscoverWithStats(ctx, repoRoot, scope)
	return elems, err
}

// DiscoverWithStats is Discover plus verification accounting.
func (a *Adapter) DiscoverWithStats(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, Stats, error) {
	var st Stats
	if a.client == nil {
		return nil, st, fmt.Errorf("llmextract: nil llm.Client")
	}
	var out []*contractpb.ContractElement
	var fileErrs []error
	err := adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err // genuine cancellation/deadline aborts the whole walk
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("llmextract: read %s: %w", rel, err)
		}
		library := libraryFromPath(rel)
		if !libraryInScope(library, scope) {
			return nil
		}
		st.FilesScanned++
		raws, hit, err := a.extractFile(ctx, string(body))
		if err != nil {
			// A per-file model failure (e.g. a local-model timeout) is
			// non-fatal: the LLM tier is additive, so one bad file degrades
			// the result by exactly that file, not the whole tier. Record
			// it and keep walking; the next cached run picks it up free.
			st.FilesFailed++
			fileErrs = append(fileErrs, fmt.Errorf("llmextract: %s: %w", rel, err))
			return nil
		}
		if hit {
			st.CacheHits++
		}
		lines := strings.Split(string(body), "\n")
		for _, r := range raws {
			st.Proposed++
			vline, ok := verifyCitation(lines, r)
			if !ok {
				st.CiteDropped++
				continue
			}
			st.CiteVerified++
			out = append(out, buildElement(library, rel, r, vline))
		}
		return nil
	})
	if err == nil && len(fileErrs) > 0 {
		err = errors.Join(fileErrs...)
	}
	return out, st, err
}

// extractFile returns the raw elements for one file's source, using the
// disk cache when available. The bool reports a cache hit.
func (a *Adapter) extractFile(ctx context.Context, src string) ([]rawElement, bool, error) {
	key := a.cacheKey(src)
	if raws, ok := a.cacheGet(key); ok {
		return raws, true, nil
	}
	prompt := buildPrompt(src)
	resp, err := a.client.Generate(ctx, prompt)
	if err != nil {
		return nil, false, err
	}
	raws := parseElements(resp)
	a.cachePut(key, raws)
	return raws, false, nil
}

func (a *Adapter) cacheKey(src string) string {
	h := sha256.Sum256([]byte(a.client.Name() + "\x00" + promptVersion + "\x00" + src))
	return hex.EncodeToString(h[:])
}

func (a *Adapter) cacheGet(key string) ([]rawElement, bool) {
	if a.cacheDir == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(a.cacheDir, key+".json"))
	if err != nil {
		return nil, false
	}
	var raws []rawElement
	if json.Unmarshal(b, &raws) != nil {
		return nil, false
	}
	return raws, true
}

func (a *Adapter) cachePut(key string, raws []rawElement) {
	if a.cacheDir == "" {
		return
	}
	_ = os.MkdirAll(a.cacheDir, 0o755)
	b, err := json.Marshal(raws)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(a.cacheDir, key+".json"), b, 0o644)
}

// buildPrompt renders the per-file extraction prompt. The source is
// emitted with 1-based line-number prefixes so the model can cite
// accurately.
func buildPrompt(src string) string {
	var b strings.Builder
	b.WriteString(`You extract the PUBLIC C++ API surface from a single header file.

Output ONLY a JSON array (no prose, no markdown fences). Each item:
  {"name": "<fully-qualified C++ name, or bare macro name>", "kind": "<class|method|free_function|macro|enum|type>", "line": <1-based line number where it is declared>}

Rules:
- Include: public classes/structs, their public methods, free functions, public #define macros (function-like or constant), enums, and public type aliases.
- For methods, name is "Namespace::Class::Method". For macros, name is the bare macro identifier (e.g. PW_LOG_DEBUG).
- EXCLUDE: anything in an internal/detail namespace, private members, include guards, and implementation-only helpers.
- "line" MUST be the exact line number (from the prefixes below) where the symbol's name appears in its declaration.
- If the file declares nothing public, output [].

Header source (each line is prefixed with "<n>| "):
`)
	for i, line := range strings.Split(src, "\n") {
		fmt.Fprintf(&b, "%d| %s\n", i+1, line)
	}
	return b.String()
}

// parseElements extracts the JSON array from a model response, tolerant
// of leading/trailing prose or markdown fences.
func parseElements(resp string) []rawElement {
	s := strings.TrimSpace(resp)
	// Strip ```json ... ``` fences if present.
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			s = s[i : j+1]
		}
	}
	var raws []rawElement
	if json.Unmarshal([]byte(s), &raws) != nil {
		return nil
	}
	// Drop obviously empty entries.
	out := raws[:0]
	for _, r := range raws {
		if strings.TrimSpace(r.Name) != "" && r.Line > 0 {
			out = append(out, r)
		}
	}
	return out
}

// verifyCitation checks the cited line (±2 lines of slack) actually
// contains the element's local name. Returns the verified 1-based line.
func verifyCitation(lines []string, r rawElement) (int, bool) {
	local := localName(r.Name)
	if local == "" {
		return 0, false
	}
	for d := 0; d <= 2; d++ {
		for _, cand := range []int{r.Line - d, r.Line + d} {
			if cand >= 1 && cand <= len(lines) && containsIdent(lines[cand-1], local) {
				return cand, true
			}
		}
	}
	return 0, false
}

// containsIdent reports whether line contains ident as a whole token
// (not a substring of a larger identifier).
func containsIdent(line, ident string) bool {
	idx := 0
	for {
		k := strings.Index(line[idx:], ident)
		if k < 0 {
			return false
		}
		k += idx
		before := k == 0 || !isIdentByte(line[k-1])
		afterPos := k + len(ident)
		after := afterPos >= len(line) || !isIdentByte(line[afterPos])
		if before && after {
			return true
		}
		idx = k + 1
		if idx >= len(line) {
			return false
		}
	}
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// localName returns the part after the final "::".
func localName(qn string) string {
	if i := strings.LastIndex(qn, "::"); i >= 0 {
		return strings.TrimSpace(qn[i+2:])
	}
	return strings.TrimSpace(qn)
}

func buildElement(library, rel string, r rawElement, line int) *contractpb.ContractElement {
	qn := strings.TrimSpace(r.Name)
	return &contractpb.ContractElement{
		Id:        library + "/" + qn,
		Kind:      kindOf(r.Kind),
		Ecosystem: Name,
		Library:   library,
		Location:  &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
		Aliases:   aliases(qn),
	}
}

func aliases(qn string) []string {
	local := localName(qn)
	out := []string{}
	if local != "" && local != qn {
		out = append(out, local)
	}
	if dotted := strings.ReplaceAll(qn, "::", "."); dotted != qn && dotted != local {
		out = append(out, dotted)
	}
	return out
}

func kindOf(k string) contractpb.ContractElementKind {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "class", "struct":
		return contractpb.ContractElementKind_CPP_CLASS
	case "method":
		return contractpb.ContractElementKind_METHOD
	case "free_function", "function":
		return contractpb.ContractElementKind_CPP_FREE_FUNCTION
	case "macro":
		return contractpb.ContractElementKind_CPP_MACRO
	case "enum", "type", "typedef", "alias":
		return contractpb.ContractElementKind_TYPE
	default:
		return contractpb.ContractElementKind_TYPE
	}
}

// libraryFromPath mirrors cppheader's slug derivation: the segment
// after the nearest public/ or include/ dir, else the top-level dir.
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
		if ex == lib {
			return false
		}
	}
	if len(scope.Libraries) == 0 && len(scope.AlsoInclude) == 0 {
		return true
	}
	for _, l := range scope.Libraries {
		if l == lib {
			return true
		}
	}
	for _, l := range scope.AlsoInclude {
		if l == lib {
			return true
		}
	}
	return false
}
