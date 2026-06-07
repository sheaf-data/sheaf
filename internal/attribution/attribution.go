// Package attribution is the citation-gated LLM attribution pass: a
// post-indexer stage that asks an LLM which contract elements each test
// exercises (and each doc documents), then keeps only the judgments
// whose cited source line mechanically checks out.
//
// It is the propose→verify discipline from llmextract applied to the
// ATTRIBUTION join instead of extraction:
//
//   - The model PROPOSES (element, cited-line) edges per test/doc.
//   - A deterministic gate DISPOSES of any edge whose cited line does
//     not actually reference the element (whole-token match against the
//     element's name + aliases). A hallucinated edge cannot survive.
//
// The result is an edge that is mechanically grounded (a real reference
// exists at the cited line) but tagged LLM-tier — the gate confirms a
// reference, not that the test semantically exercises the element, so
// these edges stay in the flagged tier and never enter the deterministic
// coverage number (the moat). The deterministic cross-reference join is
// untouched; this pass only ADDS LLM-tier edges the join missed
// (recall) and, by only emitting judgments it can cite, is precise by
// construction.
//
// Determinism/cost: each (test|doc) extraction is cached on disk keyed by
// sha256(model + promptVersion + body + candidate-set), so a re-run with
// a pinned backend is reproducible and free.
package attribution

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
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// promptVersion is part of the cache key; bump when a prompt changes.
const promptVersion = "attr-v1"

// Config tunes the pass.
type Config struct {
	// CacheDir holds per-test/doc LLM responses, content-hash keyed.
	// Empty disables caching.
	CacheDir string
	// MaxTests / MaxDocs cap how many tests/docs are adjudicated (cost
	// control). 0 means no cap.
	MaxTests int
	MaxDocs  int
	// MaxCandidates caps the contract-element candidate list shown per
	// prompt. 0 means no cap (show the whole in-scope surface).
	MaxCandidates int
}

// Stats reports per-run accounting.
type Stats struct {
	TestsScanned int
	DocsScanned  int
	CacheHits    int
	Proposed     int // edges the model emitted
	CiteVerified int // survived the citation gate
	CiteDropped  int // dropped: cited line did not reference the element
	Redundant    int // dropped: a deterministic edge already covers (element,source)
	TestEdges    int // LLM-tier test edges added
	DocEdges     int // LLM-tier doc edges added
	AliasEdges   int // edges whose citation matched via an alias (macro-alias hardening signal)
}

// Pass runs the attribution over a corpus.
type Pass struct {
	client llm.Client
	cfg    Config
}

func New(cfg Config, client llm.Client) *Pass {
	return &Pass{client: client, cfg: cfg}
}

// rawEdge is the model's per-edge JSON shape.
type rawEdge struct {
	Element string `json:"element"` // candidate element ID (or local name)
	Line    int    `json:"line"`    // 1-based absolute file line referencing it
}

// candidate is one element offered to the model, with its citation-gate
// match strings precomputed.
type candidate struct {
	id        string
	local     string   // canonical local name (after final ::)
	kind      string   // human kind label
	aliases   []string // alias forms (bare/dotted/macro)
	libraryID string
}

// Run adjudicates the corpus's tests and docs — batched BY FILE — and
// appends LLM-tier edges to the relevant coverage profiles in place. The
// deterministic edges (already on the profiles) are left untouched.
//
// Batching by file (not per test/doc item) reads each source once and
// makes one model call per file instead of one per item, and the static
// instructions + candidate-element list are sent as a cached prefix
// (prompt caching) shared across every file — so the large repeated
// context is paid for once.
func (p *Pass) Run(ctx context.Context, c *corpus.Corpus, repoRoot string) (Stats, error) {
	var st Stats
	if p.client == nil {
		return st, fmt.Errorf("attribution: nil llm.Client")
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
	testPrefix := buildSystemPrefix("test", candList)
	docPrefix := buildSystemPrefix("doc", candList)

	resolve := func(ref string) (candidate, bool) {
		if cd, ok := candByID[ref]; ok {
			return cd, true
		}
		return resolveByLocal(candByID, cands, ref)
	}

	// ---- tests, grouped by file ----
	testFiles := groupTestsByFile(c.Tests())
	for i, tf := range testFiles {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if p.cfg.MaxTests > 0 && i >= p.cfg.MaxTests {
			break
		}
		body, absLines, ok := readWholeFile(repoRoot, tf.path)
		if !ok {
			continue
		}
		st.TestsScanned++
		edges, hit := p.adjudicateFile(ctx, "test", testPrefix, body)
		if hit {
			st.CacheHits++
		}
		for _, e := range edges {
			cd, exists := resolve(e.Element)
			if !exists {
				st.CiteDropped++
				continue
			}
			st.Proposed++
			vline, matchedAlias, ok := verifyEdge(absLines, e.Line, cd)
			if !ok {
				st.CiteDropped++
				continue
			}
			st.CiteVerified++
			tc := tf.enclosing(vline)
			testName := tf.path
			if tc != nil {
				testName = tc.GetId()
			}
			if alreadyDeterministicTest(c.Profile(cd.id), testName) {
				st.Redundant++
				continue
			}
			framework := ""
			if tc != nil {
				framework = tc.GetFramework()
			}
			appendTestEdge(c, cd.id, &commonpb.TestRef{
				Path:       tf.path,
				Line:       uint32(vline),
				TestName:   testName,
				Framework:  framework,
				Exercises:  cd.id,
				Provenance: edgeProvenance(p.client.Name(), matchedAlias),
			})
			st.TestEdges++
			if matchedAlias {
				st.AliasEdges++
			}
		}
	}

	// ---- docs, grouped by file ----
	docFiles := groupDocsByFile(c.DocClaims())
	for i, df := range docFiles {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if p.cfg.MaxDocs > 0 && i >= p.cfg.MaxDocs {
			break
		}
		body, absLines, ok := readWholeFile(repoRoot, df.path)
		if !ok {
			continue
		}
		st.DocsScanned++
		edges, hit := p.adjudicateFile(ctx, "doc", docPrefix, body)
		if hit {
			st.CacheHits++
		}
		for _, e := range edges {
			cd, exists := resolve(e.Element)
			if !exists {
				st.CiteDropped++
				continue
			}
			st.Proposed++
			vline, matchedAlias, ok := verifyEdge(absLines, e.Line, cd)
			if !ok {
				st.CiteDropped++
				continue
			}
			st.CiteVerified++
			if alreadyDeterministicDoc(c.Profile(cd.id), df.path) {
				st.Redundant++
				continue
			}
			appendDocEdge(c, cd.id, &commonpb.DocRef{
				Path:       df.path,
				Line:       uint32(vline),
				Url:        df.url,
				Substance:  commonpb.Substance_PARTIAL,
				Adapter:    "llm",
				Provenance: edgeProvenance(p.client.Name(), matchedAlias),
			})
			st.DocEdges++
			if matchedAlias {
				st.AliasEdges++
			}
		}
	}

	return st, nil
}

// adjudicateFile runs (or cache-reads) one LLM call for a whole file's
// source against the cached candidate prefix. Returns the proposed edges
// and a cache-hit flag. Prefers the backend's cached-prefix path (prompt
// caching) when available, else concatenates prefix + body.
func (p *Pass) adjudicateFile(ctx context.Context, kind, systemPrefix, body string) ([]rawEdge, bool) {
	key := p.cacheKey(kind, body, systemPrefix)
	if edges, ok := p.cacheGet(key); ok {
		return edges, true
	}
	var resp string
	var err error
	if cg, ok := p.client.(llm.CachedGenerator); ok {
		resp, err = cg.GenerateCached(ctx, systemPrefix, body)
	} else {
		resp, err = p.client.Generate(ctx, systemPrefix+"\n\n"+body)
	}
	if err != nil {
		// Soft-fail: a single unreachable/erroring call should not abort
		// the whole pass. No edges from this file.
		return nil, false
	}
	edges := parseEdges(resp)
	p.cachePut(key, edges)
	return edges, false
}

// buildCandidates snapshots the in-scope contract elements with their
// citation-gate match strings.
func (p *Pass) buildCandidates(c *corpus.Corpus) []candidate {
	elems := c.Elements()
	out := make([]candidate, 0, len(elems))
	for _, e := range elems {
		out = append(out, candidate{
			id:        e.GetId(),
			local:     localName(idTail(e.GetId())),
			kind:      kindLabel(e.GetKind()),
			aliases:   e.GetAliases(),
			libraryID: e.GetLibrary(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// ----------------- citation gate -----------------

// verifyEdge confirms the model's cited line (±2 slack) actually
// references the candidate, by a whole-token match against its canonical
// local name first, then its aliases. Returns the verified 1-based file
// line, whether the match came from an alias (vs the canonical local
// name), and ok.
func verifyEdge(absLines map[int]string, citedLine int, cd candidate) (int, bool, bool) {
	if cd.local == "" {
		return 0, false, false
	}
	// Tighter slack than extraction (±2): attribution cites a specific
	// usage line from line-numbered source, and loose slack lets an
	// adjacent comment mention satisfy the gate — exactly the FP class we
	// are trying to exclude. ±1 tolerates a single off-by-one only.
	for d := 0; d <= 1; d++ {
		for _, cand := range []int{citedLine - d, citedLine + d} {
			line, ok := absLines[cand]
			if !ok {
				continue
			}
			if containsIdent(line, cd.local) {
				return cand, false, true
			}
			for _, a := range cd.aliases {
				if a != "" && a != cd.local && containsIdent(line, a) {
					return cand, true, true
				}
			}
		}
	}
	return 0, false, false
}

// containsIdent reports whether line contains ident as a whole token.
// Lifted from internal/adapters/llmextract (unexported there); same
// semantics so extraction and attribution gate identically.
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

// idTail returns the portion of an element ID after the first "/".
func idTail(id string) string {
	if i := strings.Index(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// resolveByLocal lets the model cite an element by its bare local name
// when the full ID is awkward; resolves to a unique candidate or fails.
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

func edgeProvenance(source string, matchedAlias bool) *commonpb.RowProvenance {
	p := &commonpb.RowProvenance{Tier: commonpb.RowProvenance_LLM, Source: source}
	if matchedAlias {
		// The citation only held via an alias form — the kind of edge a
		// deterministic alias map (#define-graph / canonical_refs) would
		// pin. Tag it so the hardening doc can group these.
		p.Transform = "macro-alias"
	}
	return p
}

// ----------------- profile mutation -----------------

// appendTestEdge adds an LLM-tier test edge to the SEPARATE llm_inferred
// bucket — never the deterministic unit/integration/etc lists — so the
// trusted coverage number and the analyzers (which read the deterministic
// buckets) are unaffected. The two-tier report reads llm_inferred to show
// the flagged tier.
func appendTestEdge(c *corpus.Corpus, elementID string, ref *commonpb.TestRef) {
	prof := c.Profile(elementID)
	if prof == nil {
		prof = &coveragepb.CoverageProfile{ElementId: elementID}
		c.SetProfile(prof)
	}
	if prof.Tests == nil {
		prof.Tests = &coveragepb.TestCoverage{}
	}
	prof.Tests.LlmInferred = append(prof.Tests.LlmInferred, ref)
}

func appendDocEdge(c *corpus.Corpus, elementID string, ref *commonpb.DocRef) {
	prof := c.Profile(elementID)
	if prof == nil {
		prof = &coveragepb.CoverageProfile{ElementId: elementID}
		c.SetProfile(prof)
	}
	if prof.Docs == nil {
		prof.Docs = &coveragepb.DocCoverage{}
	}
	prof.Docs.LlmInferred = append(prof.Docs.LlmInferred, ref)
}

func alreadyDeterministicTest(prof *coveragepb.CoverageProfile, testName string) bool {
	if prof == nil {
		return false
	}
	for _, b := range allTestRefs(prof) {
		if b.GetProvenance().GetTier() != commonpb.RowProvenance_LLM &&
			b.GetTestName() == testName {
			return true
		}
	}
	return false
}

func alreadyDeterministicDoc(prof *coveragepb.CoverageProfile, path string) bool {
	if prof == nil {
		return false
	}
	for _, d := range allDocRefs(prof) {
		if d.GetProvenance().GetTier() != commonpb.RowProvenance_LLM && d.GetPath() == path {
			return true
		}
	}
	return false
}

func allTestRefs(p *coveragepb.CoverageProfile) []*commonpb.TestRef {
	t := p.GetTests()
	if t == nil {
		return nil
	}
	var out []*commonpb.TestRef
	out = append(out, t.GetUnit()...)
	out = append(out, t.GetIntegration()...)
	out = append(out, t.GetE2E()...)
	out = append(out, t.GetCtf()...)
	out = append(out, t.GetPerformance()...)
	out = append(out, t.GetFuzz()...)
	out = append(out, t.GetGolden()...)
	return out
}

func allDocRefs(p *coveragepb.CoverageProfile) []*commonpb.DocRef {
	d := p.GetDocs()
	if d == nil {
		return nil
	}
	var out []*commonpb.DocRef
	out = append(out, d.GetConcept()...)
	out = append(out, d.GetTutorial()...)
	return out
}

// ----------------- prompt + parse -----------------

// buildSystemPrefix renders the static, cacheable prefix: the
// instructions + the full candidate-element list. It is identical across
// every file of a given kind, so prompt caching pays for it once. The
// per-file source arrives as the user body (a separate message).
func buildSystemPrefix(kind, candList string) string {
	verb := "EXERCISES (calls, instantiates, or asserts on)"
	noun := "test file"
	if kind == "doc" {
		verb = "DOCUMENTS (describes the behavior or API of)"
		noun = "documentation file"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `You decide which contract elements a %s actually %s.

The user message contains the full source of one %s, each line prefixed
"<n>| " with its 1-based line number.

Output ONLY a JSON array (no prose, no markdown fences). Each item:
  {"element": "<element id from the candidate list>", "line": <1-based file line where it is referenced>}

Rules:
- Include an element ONLY where the file genuinely %s it — not a mere
  name collision, not a comment, not an unrelated mention.
- "line" MUST be the exact prefixed line number where the element's name
  appears at the point it is %s.
- One item per (element, reference site). Use element IDs exactly as in
  the candidate list. If none apply, output [].

Candidate contract elements (id — kind — local name):
%s`, noun, verb, noun, verb, verb, candList)
	return b.String()
}

func parseEdges(resp string) []rawEdge {
	s := strings.TrimSpace(resp)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			s = s[i : j+1]
		}
	}
	var raws []rawEdge
	if json.Unmarshal([]byte(s), &raws) != nil {
		return nil
	}
	out := raws[:0]
	for _, r := range raws {
		if strings.TrimSpace(r.Element) != "" && r.Line > 0 {
			out = append(out, r)
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

// maxFileLines caps how much of a single source file is sent in one
// call. Test/doc files are normally well under this; a pathologically
// large file is truncated (the tail is dropped) rather than blowing the
// request up.
const maxFileLines = 3000

// testFile groups a file's tests so a single call covers them all, and
// lets us map a cited line back to its enclosing test.
type testFile struct {
	path  string
	tests []*testcasepb.TestCase // sorted by start line
}

// enclosing returns the test whose declaration is the nearest at or
// before `line` — the test that the cited reference sits inside.
func (tf *testFile) enclosing(line int) *testcasepb.TestCase {
	var hit *testcasepb.TestCase
	for _, t := range tf.tests {
		if int(t.GetLocation().GetLine()) <= line {
			hit = t
		} else {
			break
		}
	}
	if hit == nil && len(tf.tests) > 0 {
		hit = tf.tests[0]
	}
	return hit
}

func groupTestsByFile(tests []*testcasepb.TestCase) []*testFile {
	byPath := map[string]*testFile{}
	var order []string
	for _, t := range tests {
		path := t.GetLocation().GetPath()
		if path == "" {
			continue
		}
		tf := byPath[path]
		if tf == nil {
			tf = &testFile{path: path}
			byPath[path] = tf
			order = append(order, path)
		}
		tf.tests = append(tf.tests, t)
	}
	sort.Strings(order)
	out := make([]*testFile, 0, len(order))
	for _, p := range order {
		tf := byPath[p]
		sort.SliceStable(tf.tests, func(i, j int) bool {
			return tf.tests[i].GetLocation().GetLine() < tf.tests[j].GetLocation().GetLine()
		})
		out = append(out, tf)
	}
	return out
}

// docFile groups a file's doc claims; url is the first non-empty claim URL.
type docFile struct {
	path string
	url  string
}

func groupDocsByFile(claims []*docclaimpb.DocClaim) []*docFile {
	byPath := map[string]*docFile{}
	var order []string
	for _, dc := range claims {
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

// readWholeFile reads a file and returns its line-number-prefixed text
// (capped at maxFileLines) plus a map of absolute line number → raw line
// for the citation gate.
func readWholeFile(repoRoot, rel string) (string, map[int]string, bool) {
	if rel == "" {
		return "", nil, false
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		return "", nil, false
	}
	lines := strings.Split(string(raw), "\n")
	n := len(lines)
	if n > maxFileLines {
		n = maxFileLines
	}
	abs := make(map[int]string, n)
	var b strings.Builder
	for i := 0; i < n; i++ {
		abs[i+1] = lines[i]
		fmt.Fprintf(&b, "%d| %s\n", i+1, lines[i])
	}
	return b.String(), abs, true
}

// ----------------- cache -----------------

func (p *Pass) cacheKey(kind, body, candList string) string {
	h := sha256.Sum256([]byte(p.client.Name() + "\x00" + promptVersion + "\x00" + kind + "\x00" + body + "\x00" + candList))
	return hex.EncodeToString(h[:])
}

func (p *Pass) cacheGet(key string) ([]rawEdge, bool) {
	if p.cfg.CacheDir == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(p.cfg.CacheDir, key+".json"))
	if err != nil {
		return nil, false
	}
	var edges []rawEdge
	if json.Unmarshal(b, &edges) != nil {
		return nil, false
	}
	return edges, true
}

func (p *Pass) cachePut(key string, edges []rawEdge) {
	if p.cfg.CacheDir == "" {
		return
	}
	_ = os.MkdirAll(p.cfg.CacheDir, 0o755)
	b, err := json.Marshal(edges)
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
