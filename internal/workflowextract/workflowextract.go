// Package workflowextract is the citation-gated LLM WORKFLOW
// (composition) extraction pass: a post-indexer stage that asks an LLM
// which ORDERED sequences of contract elements each prose doc walks
// through ("call A, then B, then C"), then keeps only the sequences
// whose every surviving element mechanically checks out at its cited
// line.
//
// It is the same propose→cite→verify discipline as internal/attribution,
// applied to the COMPOSITION surface instead of the per-element join:
//
//   - The model PROPOSES ordered sequences of (element, cited-line) steps
//     per doc file.
//   - A deterministic gate DISPOSES of any step whose cited line does not
//     actually name the element (whole-token match against the element's
//     name + aliases). A sequence must retain >=2 surviving, gated
//     elements to be a workflow — single survivors are left to
//     element-level attribution.
//
// The result is a DocClaim{kind=WORKFLOW} with an ORDERED contract_refs
// list, tagged LLM-tier. These claims are ADDITIVE/FLAGGED: they live in
// the corpus's DocClaims but never enter a coverage profile, so the
// deterministic workflow-surface count (the moat, enforced in
// internal/indexer.placeDocRef) is untouched. They feed
// internal/hardening, which classifies each workflow's SOURCE grammar
// (CLI-command fences / yaml FQDN blocks / free prose) and emits the
// ranked deterministic-replacement backlog.
//
// Determinism/cost: each doc-file extraction is cached on disk keyed by
// sha256(model + promptVersion + body + candidate-set), so a re-run with
// a pinned backend is reproducible and free.
package workflowextract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/llm"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// promptVersion is part of the cache key; bump when a prompt changes.
const promptVersion = "wfx-v1"

// Transform tags stamped on emitted WORKFLOW claims' provenance. They
// classify the SOURCE grammar of the extracted sequence and drive the
// GROUP-BY in internal/hardening. Documented in common.proto's
// RowProvenance.transform taxonomy.
const (
	transformCLIGrammar  = "workflow-cli-grammar"  // `<binary> <subcmd>` command fences
	transformYAMLGrammar = "workflow-yaml-grammar" // fenced yaml naming >=2 dotted FQDNs
	transformProse       = "workflow-prose"        // free prose, no parseable grammar
)

// maxFileLines caps how much of a single doc file is sent in one call.
const maxFileLines = 3000

// maxExcerpt caps the raw_text excerpt stored on an emitted claim.
const maxExcerpt = 200

// Config tunes the pass.
type Config struct {
	// CacheDir holds per-doc LLM responses, content-hash keyed. Empty
	// disables caching.
	CacheDir string
	// MaxDocs caps how many doc files are adjudicated (cost control). 0
	// means no cap.
	MaxDocs int
	// MaxCandidates caps the contract-element candidate list shown per
	// prompt. 0 means no cap.
	MaxCandidates int
}

// Stats reports per-run accounting.
type Stats struct {
	DocsScanned      int
	CacheHits        int
	Proposed         int // ordered sequences the model emitted
	Gated            int // sequences with >=2 citation-verified elements
	Dropped          int // sequences discarded: fewer than 2 survived the gate
	Emitted          int // WORKFLOW DocClaims added to the corpus (== Gated)
	ElementsProposed int // individual (element, line) steps the model emitted
	ElementsDropped  int // steps dropped: cited line did not name the element
	CLIGrammar       int // emitted workflows classified workflow-cli-grammar
	YAMLGrammar      int // emitted workflows classified workflow-yaml-grammar
	Prose            int // emitted workflows classified workflow-prose
}

// Pass runs the workflow extraction over a corpus.
type Pass struct {
	client llm.Client
	cfg    Config
}

func New(cfg Config, client llm.Client) *Pass {
	return &Pass{client: client, cfg: cfg}
}

// rawWorkflow / rawStep are the model's per-file JSON shape: a list of
// ordered sequences, each a list of (element, cited-line) steps.
type rawWorkflow struct {
	Steps []rawStep `json:"steps"`
}

type rawStep struct {
	Element string `json:"element"` // candidate element ID (or local name)
	Line    int    `json:"line"`    // 1-based file line naming it
}

// candidate is one element offered to the model, with its citation-gate
// match strings precomputed.
type candidate struct {
	id      string
	local   string   // canonical local name (after final ::)
	kind    string   // human kind label
	aliases []string // alias forms (bare/dotted/macro)
}

// Run adjudicates the corpus's prose docs — batched BY FILE — and adds
// LLM-tier WORKFLOW DocClaims to the corpus for every ordered sequence
// whose elements survive the citation gate. The deterministic claims and
// coverage profiles are left untouched.
func (p *Pass) Run(ctx context.Context, c *corpus.Corpus, repoRoot string) (Stats, error) {
	var st Stats
	if p.client == nil {
		return st, fmt.Errorf("workflowextract: nil llm.Client")
	}
	cands := p.buildCandidates(c)
	if len(cands) == 0 {
		return st, nil
	}
	candByID := map[string]candidate{}
	for _, cd := range cands {
		candByID[cd.id] = cd
	}
	candList := renderCandidates(cands, p.cfg.MaxCandidates)
	prefix := buildSystemPrefix(candList)

	resolve := func(ref string) (candidate, bool) {
		if cd, ok := candByID[ref]; ok {
			return cd, true
		}
		return resolveByLocal(candByID, cands, ref)
	}

	for i, df := range groupDocsByFile(c.DocClaims()) {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if p.cfg.MaxDocs > 0 && i >= p.cfg.MaxDocs {
			break
		}
		body, lines, ok := readDocFile(repoRoot, df.path)
		if !ok {
			continue
		}
		st.DocsScanned++
		workflows, hit := p.adjudicateFile(ctx, body, prefix)
		if hit {
			st.CacheHits++
		}
		for _, wf := range workflows {
			st.Proposed++
			// Citation-gate every step, preserving sequence order and
			// keeping distinct elements only.
			var refIDs []string
			var citedLines []int
			seen := map[string]bool{}
			for _, step := range wf.Steps {
				cd, exists := resolve(step.Element)
				if !exists {
					st.ElementsProposed++
					st.ElementsDropped++
					continue
				}
				st.ElementsProposed++
				vline, ok := verifyStep(lines, step.Line, cd)
				if !ok {
					st.ElementsDropped++
					continue
				}
				if seen[cd.id] {
					continue
				}
				seen[cd.id] = true
				refIDs = append(refIDs, cd.id)
				citedLines = append(citedLines, vline)
			}
			// A workflow needs >=2 surviving, gated elements. A single
			// survivor is element-level attribution territory, not a
			// composition.
			if len(refIDs) < 2 {
				st.Dropped++
				continue
			}
			st.Gated++
			transform := classifyGrammar(lines, citedLines)
			switch transform {
			case transformCLIGrammar:
				st.CLIGrammar++
			case transformYAMLGrammar:
				st.YAMLGrammar++
			default:
				st.Prose++
			}
			c.AddDocClaim(&docclaimpb.DocClaim{
				SourcePath:   df.path,
				Location:     &commonpb.SourceLocation{Path: df.path, Line: uint32(citedLines[0]), Url: df.url},
				RawText:      excerpt(lines, citedLines),
				ContractRefs: refIDs,
				Url:          df.url,
				Kind:         docclaimpb.DocClaimKind_WORKFLOW,
				Adapter:      "workflowextract",
				Provenance: &commonpb.RowProvenance{
					Tier:      commonpb.RowProvenance_LLM,
					Source:    p.client.Name(),
					Transform: transform,
				},
			})
			st.Emitted++
		}
	}
	return st, nil
}

// adjudicateFile runs (or cache-reads) one LLM call for a whole doc's
// source against the cached candidate prefix. Prefers the backend's
// cached-prefix path (prompt caching) when available, else concatenates
// prefix + body. Soft-fails: an erroring call yields no workflows.
func (p *Pass) adjudicateFile(ctx context.Context, body, prefix string) ([]rawWorkflow, bool) {
	key := p.cacheKey(body, prefix)
	if wfs, ok := p.cacheGet(key); ok {
		return wfs, true
	}
	var resp string
	var err error
	if cg, ok := p.client.(llm.CachedGenerator); ok {
		resp, err = cg.GenerateCached(ctx, prefix, body)
	} else {
		resp, err = p.client.Generate(ctx, prefix+"\n\n"+body)
	}
	if err != nil {
		return nil, false
	}
	wfs := parseWorkflows(resp)
	p.cachePut(key, wfs)
	return wfs, false
}

func (p *Pass) buildCandidates(c *corpus.Corpus) []candidate {
	elems := c.Elements()
	out := make([]candidate, 0, len(elems))
	for _, e := range elems {
		out = append(out, candidate{
			id:      e.GetId(),
			local:   localName(idTail(e.GetId())),
			kind:    kindLabel(e.GetKind()),
			aliases: e.GetAliases(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// ----------------- citation gate -----------------

// verifyStep confirms the model's cited line (±1 slack) actually names
// the candidate, by a whole-token match against its canonical local name
// first, then its aliases. Returns the verified 1-based line and ok.
// ±1 (not ±2) matches attribution's tighter slack: a workflow step cites
// a specific usage line, and loose slack lets an adjacent comment satisfy
// the gate — exactly the FP class we exclude.
func verifyStep(lines []string, citedLine int, cd candidate) (int, bool) {
	if cd.local == "" {
		return 0, false
	}
	for d := 0; d <= 1; d++ {
		for _, ln := range []int{citedLine - d, citedLine + d} {
			if ln < 1 || ln > len(lines) {
				continue
			}
			line := lines[ln-1]
			if containsIdent(line, cd.local) {
				return ln, true
			}
			for _, a := range cd.aliases {
				if a != "" && a != cd.local && containsIdent(line, a) {
					return ln, true
				}
			}
		}
	}
	return 0, false
}

// containsIdent / isIdentByte are copied from internal/attribution (and
// internal/adapters/llmextract, where they are unexported). Same
// semantics so extraction, attribution, and workflow gating verify
// identically. Copied rather than imported to keep this package's change
// self-contained.
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

func localName(qn string) string {
	if i := strings.LastIndex(qn, "::"); i >= 0 {
		return strings.TrimSpace(qn[i+2:])
	}
	return strings.TrimSpace(qn)
}

func idTail(id string) string {
	if i := strings.Index(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func resolveByLocal(byID map[string]candidate, cands []candidate, ref string) (candidate, bool) {
	if cd, ok := byID[ref]; ok {
		return cd, true
	}
	want := localName(ref)
	var hit candidate
	n := 0
	for _, cd := range cands {
		if cd.local == want {
			hit = cd
			n++
		}
	}
	if n == 1 {
		return hit, true
	}
	return candidate{}, false
}

// ----------------- grammar classification -----------------

// classifyGrammar inspects the doc source at the workflow's cited lines
// and tags the SOURCE grammar of the sequence (a cheap lexical check, no
// toolchain). The tag drives the hardening doc's deterministic-replacement
// GROUP-BY:
//
//   - yaml:  a cited line sits inside a yaml code block (markdown ```yaml
//     fence or rST `.. code-block:: yaml`) and that block names >=2
//     dotted FQDNs → the `yaml-workflows` adapter could pin it.
//   - cli:   a cited line is a `<ident> <ident>` command-ish invocation
//     inside a (non-yaml) code fence → the `workflows` adapter could.
//   - prose: neither → free prose, likely the irreducible tail.
func classifyGrammar(lines []string, cited []int) string {
	kinds := blockKinds(lines)
	// yaml takes precedence: a yaml block is a stronger signal than a
	// command-ish line that may happen to sit nearby.
	if inYAMLBlockWithFQDNs(lines, kinds, cited) {
		return transformYAMLGrammar
	}
	for _, ln := range cited {
		if ln < 1 || ln > len(lines) {
			continue
		}
		if kinds[ln-1] == "code" && isCommandish(lines[ln-1]) {
			return transformCLIGrammar
		}
	}
	return transformProse
}

// blockKinds returns a per-line classification: "yaml" for lines inside a
// yaml code block, "code" for lines inside any other fenced/directive
// code block, "" otherwise. Handles markdown ```/~~~ fences and rST
// `.. code-block:: <lang>` / `.. sourcecode:: <lang>` directives.
func blockKinds(lines []string) []string {
	out := make([]string, len(lines))
	inFence := false
	fenceKind := ""
	// rST directive block: lines indented deeper than the directive.
	rstKind := ""
	rstIndent := -1
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		// Markdown fence toggling.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if inFence {
				inFence = false
				fenceKind = ""
				continue // the closing fence line itself is not content
			}
			inFence = true
			lang := strings.ToLower(strings.TrimLeft(trimmed, "`~"))
			lang = strings.TrimSpace(lang)
			if lang == "yaml" || lang == "yml" {
				fenceKind = "yaml"
			} else {
				fenceKind = "code"
			}
			continue // the opening fence line itself is not content
		}
		if inFence {
			out[i] = fenceKind
			continue
		}
		// rST directive handling.
		if rstIndent >= 0 {
			if trimmed == "" {
				out[i] = rstKind // blank lines inside the block stay in it
				continue
			}
			if leadingSpaces(raw) > rstIndent {
				out[i] = rstKind
				continue
			}
			rstIndent = -1
			rstKind = ""
		}
		if d := rstDirectiveLang(trimmed); d != "" {
			if d == "yaml" || d == "yml" {
				rstKind = "yaml"
			} else {
				rstKind = "code"
			}
			rstIndent = leadingSpaces(raw)
			continue
		}
	}
	return out
}

// rstDirectiveLang returns the language of a `.. code-block:: <lang>` or
// `.. sourcecode:: <lang>` directive line (lowercased), or "" if the line
// is not such a directive.
func rstDirectiveLang(trimmed string) string {
	for _, d := range []string{".. code-block::", ".. sourcecode::"} {
		if strings.HasPrefix(trimmed, d) {
			return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, d)))
		}
	}
	return ""
}

func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	return n
}

// inYAMLBlockWithFQDNs reports whether any cited line sits in a yaml block
// whose contiguous extent names >=2 dotted FQDNs (e.g. envoy.config.*).
func inYAMLBlockWithFQDNs(lines, kinds []string, cited []int) bool {
	for _, ln := range cited {
		i := ln - 1
		if i < 0 || i >= len(lines) || kinds[i] != "yaml" {
			continue
		}
		// Expand to the contiguous yaml block around i.
		lo := i
		for lo > 0 && kinds[lo-1] == "yaml" {
			lo--
		}
		hi := i
		for hi+1 < len(lines) && kinds[hi+1] == "yaml" {
			hi++
		}
		n := 0
		for j := lo; j <= hi; j++ {
			n += countFQDNs(lines[j])
		}
		if n >= 2 {
			return true
		}
	}
	return false
}

// countFQDNs counts dotted-identifier tokens (a.b, a.b.c) on a line.
func countFQDNs(line string) int {
	n := 0
	i := 0
	for i < len(line) {
		if !isIdentStart(line[i]) {
			i++
			continue
		}
		start := i
		dots := 0
		for i < len(line) {
			if isIdentByte(line[i]) {
				i++
			} else if line[i] == '.' && i+1 < len(line) && isIdentStart(line[i+1]) {
				dots++
				i++
			} else {
				break
			}
		}
		if dots >= 1 && i > start {
			n++
		}
	}
	return n
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isCommandish reports whether a line looks like a shell command
// invocation: an optional `$ `/`> ` prompt, then a leading identifier
// (binary name) and at least one more token (subcommand or flag).
func isCommandish(line string) bool {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "$ ")
	t = strings.TrimPrefix(t, "> ")
	t = strings.TrimSpace(t)
	fields := strings.Fields(t)
	if len(fields) < 2 {
		return false
	}
	first := fields[0]
	if !isIdentStart(first[0]) {
		return false
	}
	for i := 0; i < len(first); i++ {
		c := first[i]
		if !isIdentByte(c) && c != '-' && c != '.' && c != '/' {
			return false
		}
	}
	return true
}

// excerpt builds a short raw_text from the workflow's first cited line.
func excerpt(lines []string, cited []int) string {
	if len(cited) == 0 {
		return ""
	}
	ln := cited[0]
	if ln < 1 || ln > len(lines) {
		return ""
	}
	s := strings.TrimSpace(lines[ln-1])
	if len(s) > maxExcerpt {
		s = s[:maxExcerpt]
	}
	return s
}

// ----------------- prompt + parse -----------------

func buildSystemPrefix(candList string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You identify the ORDERED sequences of contract elements a documentation
file walks the reader through — a workflow / recipe / tutorial that says
"do A, then B, then C" using two-or-more elements in sequence.

The user message contains the full source of one documentation file, each
line prefixed "<n>| " with its 1-based line number.

Output ONLY a JSON array (no prose, no markdown fences). Each item is one
workflow — an ordered sequence of the elements it walks through, in order:
  {"steps": [
     {"element": "<element id from the candidate list>", "line": <1-based file line where it is named>},
     {"element": "<element id>", "line": <1-based line>}
  ]}

Rules:
- Emit a sequence ONLY where the file genuinely composes the elements in
  order (a real recipe/tutorial), not an unordered list of mentions and
  not a single element on its own.
- A workflow needs at least 2 DISTINCT elements. Order the steps as the
  doc presents them: steps[0] first, steps[N-1] last.
- "line" MUST be the exact prefixed line number where the element's name
  appears at the point it takes part in the sequence.
- Use element IDs exactly as in the candidate list. If the file has no
  such sequence, output [].

Candidate contract elements (id — kind — local name):
%s`, candList)
	return b.String()
}

func parseWorkflows(resp string) []rawWorkflow {
	s := strings.TrimSpace(resp)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			s = s[i : j+1]
		}
	}
	var raws []rawWorkflow
	if json.Unmarshal([]byte(s), &raws) != nil {
		return nil
	}
	out := raws[:0]
	for _, r := range raws {
		var steps []rawStep
		for _, st := range r.Steps {
			if strings.TrimSpace(st.Element) != "" && st.Line > 0 {
				steps = append(steps, st)
			}
		}
		if len(steps) > 0 {
			out = append(out, rawWorkflow{Steps: steps})
		}
	}
	return out
}

func renderCandidates(cands []candidate, max int) string {
	var b strings.Builder
	for i, cd := range cands {
		if max > 0 && i >= max {
			fmt.Fprintf(&b, "  … (%d more elements omitted)\n", len(cands)-max)
			break
		}
		fmt.Fprintf(&b, "  %s — %s — %s\n", cd.id, cd.kind, cd.local)
	}
	return b.String()
}

// ----------------- file grouping + reading -----------------

// docFile groups a file's doc claims; url is the first non-empty claim URL.
type docFile struct {
	path string
	url  string
}

func groupDocsByFile(claims []*docclaimpb.DocClaim) []*docFile {
	byPath := map[string]*docFile{}
	var order []string
	for _, dc := range claims {
		// Skip the pass's own output on a re-run over the same corpus:
		// only deterministic / prose claims seed the scan set.
		if dc.GetKind() == docclaimpb.DocClaimKind_WORKFLOW &&
			dc.GetProvenance().GetTier() == commonpb.RowProvenance_LLM {
			continue
		}
		path := dc.GetSourcePath()
		if path == "" {
			path = dc.GetLocation().GetPath()
		}
		if path == "" {
			continue
		}
		df := byPath[path]
		if df == nil {
			df = &docFile{path: path}
			byPath[path] = df
			order = append(order, path)
		}
		if df.url == "" {
			df.url = dc.GetUrl()
		}
	}
	sort.Strings(order)
	out := make([]*docFile, 0, len(order))
	for _, p := range order {
		out = append(out, byPath[p])
	}
	return out
}

// readDocFile reads a doc file and returns its line-number-prefixed text
// (capped at maxFileLines) plus the raw lines slice (1-based via
// lines[n-1]) for the citation gate and grammar classification.
func readDocFile(repoRoot, rel string) (string, []string, bool) {
	if rel == "" {
		return "", nil, false
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		return "", nil, false
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) > maxFileLines {
		lines = lines[:maxFileLines]
	}
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d| %s\n", i+1, line)
	}
	return b.String(), lines, true
}

// ----------------- cache -----------------

func (p *Pass) cacheKey(body, prefix string) string {
	h := sha256.Sum256([]byte(p.client.Name() + "\x00" + promptVersion + "\x00" + body + "\x00" + prefix))
	return hex.EncodeToString(h[:])
}

func (p *Pass) cacheGet(key string) ([]rawWorkflow, bool) {
	if p.cfg.CacheDir == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(p.cfg.CacheDir, key+".json"))
	if err != nil {
		return nil, false
	}
	var wfs []rawWorkflow
	if json.Unmarshal(b, &wfs) != nil {
		return nil, false
	}
	return wfs, true
}

func (p *Pass) cachePut(key string, wfs []rawWorkflow) {
	if p.cfg.CacheDir == "" {
		return
	}
	_ = os.MkdirAll(p.cfg.CacheDir, 0o755)
	b, err := json.Marshal(wfs)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(p.cfg.CacheDir, key+".json"), b, 0o644)
}

// ----------------- misc -----------------

func kindLabel(k contractpb.ContractElementKind) string {
	s := k.String()
	return strings.ToLower(strings.TrimPrefix(s, "CONTRACT_ELEMENT_KIND_"))
}
