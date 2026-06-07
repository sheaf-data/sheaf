package scanner

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// EvidencePanel is the per-element split-pane view: five fixed surfaces
// (Contract, Docs, Tests, Examples, Workflows) each rendered as 0..N
// evidence cards.
type EvidencePanel struct {
	ElementID       string
	ShortName       string
	FixGroupKey     string
	FixGroupLabel   string
	SurfacesPresent int // count of surfaces with at least one card
	SurfacesAbsent  []string
	Surfaces        []EvidenceSurface
}

// EvidenceSurface is one row in the panel.
type EvidenceSurface struct {
	Key       string // "contract" | "docs" | "tests" | "examples" | "workflows"
	Label     string // "CONTRACT", "DOCS", …
	Cards     []EvidenceCard
	EmptyCopy string // italic empty-state copy; non-empty when Cards is empty
	// ExtraRefs is the count of additional refs beyond the rendered
	// cards (cap is maxPerSurface = 3). When non-zero the template
	// renders the "+ N more {noun}" surface footer.
	ExtraRefs int
	ExtraNoun string       // "tests" | "refs" | "examples" | "recipes"
	ExtraURL  template.URL // "open all in source" target
	// Inline marks a docs surface whose only reference re-quotes the
	// contract's own source (colocated inline docs, e.g. FIDL /// comments):
	// the duplicate card is suppressed and EmptyCopy carries a short note.
	Inline bool
}

// EvidenceCard is one quoted-source excerpt in a surface.
type EvidenceCard struct {
	Path      string
	Line      int
	URL       template.URL
	MetaLabel string
	Kind      string // "code" | "prose"
	Text      string // verbatim fragment body, already trimmed; will be {{HTML escaped}}
	// TruncMore is the count of additional lines (code) or paragraphs
	// (prose) in the full source span that didn't fit in Text. Zero
	// means no per-card truncation footer renders.
	TruncMore int
	TruncNoun string       // "lines" | "paragraphs"
	TruncURL  template.URL // "read full {section|file}" target
}

// surfaceKeys + surfaceLabels track the fixed display order from §2 of
// the evidence-panel spec, with "implementations" inserted between
// tests and examples and rendered ONLY for interface kinds (see
// isInterfaceKindString). The renderer relies on this order.
var (
	surfaceKeys   = []string{"contract", "docs", "tests", "implementations", "examples", "workflows"}
	surfaceLabels = map[string]string{
		"contract":        "CONTRACT",
		"docs":            "DOCS",
		"tests":           "TESTS",
		"implementations": "IMPLS",
		"examples":        "EXAMPLES",
		"workflows":       "WORKFLOWS",
	}
	surfaceAbsentName = map[string]string{
		"contract":        "contract",
		"docs":            "docs",
		"tests":           "tests",
		"implementations": "implementations",
		"examples":        "examples",
		"workflows":       "workflows",
	}
	surfaceEmptyCopy = map[string]string{
		// Contract is never empty — every element has a declaration.
		// Empty surfaces render the literal "N/A" so the row reads as
		// a flat absence rather than a sentence the eye has to parse.
		"docs":            "N/A",
		"tests":           "N/A",
		"implementations": "N/A · no in-tree implementor",
		"examples":        "N/A",
		"workflows":       "N/A",
	}
)

// isInterfaceKindEvidence returns whether the element kind admits the
// Implementations evidence surface. Mirrors compute.go's
// isInterfaceKindString; duplicated here to avoid the evidence builder
// reaching into render-time helpers.
func isInterfaceKindEvidence(kind string) bool {
	switch kind {
	case "METHOD", "TYPE", "PROTOCOL", "SYSCALL":
		return true
	}
	return false
}

// workflowClassifier holds compiled regexes for tagging refs as
// workflow per R-INGEST-11. Default rule set when nil.
type workflowClassifier struct {
	pathRe []*regexp.Regexp
}

func defaultWorkflowClassifier() *workflowClassifier {
	return &workflowClassifier{
		pathRe: []*regexp.Regexp{
			regexp.MustCompile(`(?i)(workflows?|recipes?|best[_-]practices?|tutorial|how[_-]to)/`),
		},
	}
}

func (wc *workflowClassifier) matchesPath(path string) bool {
	if wc == nil {
		wc = defaultWorkflowClassifier()
	}
	for _, re := range wc.pathRe {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// buildEvidencePanels constructs one EvidencePanel per MethodRow.
// Returns nil when absRepoRoot is empty — without a repo root the
// enricher cannot read source, so all surfaces would be empty noise.
func buildEvidencePanels(
	methods []MethodRow,
	elements []map[string]any,
	profiles []map[string]any,
	groups []FixGroup,
	urlTemplate, absRepoRoot string,
	view EcosystemView,
) []EvidencePanel {
	if absRepoRoot == "" {
		return nil
	}
	elemByID := make(map[string]map[string]any, len(elements))
	for _, e := range elements {
		id, _ := e["id"].(string)
		if id != "" {
			elemByID[id] = e
		}
	}
	profByID := make(map[string]map[string]any, len(profiles))
	for _, p := range profiles {
		id, _ := p["elementId"].(string)
		if id != "" {
			profByID[id] = p
		}
	}
	groupKeyByMember := make(map[string]string, len(methods))
	groupLabelByMember := make(map[string]string, len(methods))
	for _, g := range groups {
		for _, item := range g.Items {
			groupKeyByMember[item.Name] = g.Key
			groupLabelByMember[item.Name] = g.Label
		}
	}
	wc := defaultWorkflowClassifier()
	out := make([]EvidencePanel, 0, len(methods))
	for _, m := range methods {
		e := elemByID[m.Name]
		p := profByID[m.Name]
		panel := buildEvidencePanel(m, e, p, urlTemplate, absRepoRoot, wc, view)
		panel.FixGroupKey = groupKeyByMember[m.Name]
		panel.FixGroupLabel = groupLabelByMember[m.Name]
		out = append(out, panel)
	}
	return out
}

func buildEvidencePanel(
	m MethodRow,
	e, prof map[string]any,
	urlTemplate, absRepoRoot string,
	wc *workflowClassifier,
	view EcosystemView,
) EvidencePanel {
	panel := EvidencePanel{ElementID: m.Name, ShortName: m.ShortName}
	// Build per-element surface list: drop the implementations row on
	// non-interface kinds so unrelated rows (FLAGs, SUBCOMMANDs, ...)
	// don't carry a permanent N/A line. Also drop any surface the view
	// scopes out (K8s views render contract+docs only). Order from
	// surfaceKeys is preserved for the surfaces that do render.
	keep := isInterfaceKindEvidence(m.Kind)
	var keys []string
	for _, k := range surfaceKeys {
		if k == "implementations" && !keep {
			continue
		}
		if !viewAllowsSurface(view, k) {
			continue
		}
		keys = append(keys, k)
	}
	panel.Surfaces = make([]EvidenceSurface, len(keys))
	for i, key := range keys {
		panel.Surfaces[i] = EvidenceSurface{Key: key, Label: surfaceLabels[key]}
	}
	set := func(key string, s EvidenceSurface) {
		i := indexOf(keys, key)
		if i >= 0 {
			panel.Surfaces[i] = s
		}
	}
	set("contract", buildContractSurface(e, urlTemplate, absRepoRoot))
	docCards, workflowCards := buildDocsSurfaces(m.Name, prof, urlTemplate, absRepoRoot, wc)
	set("docs", finalizeSurface("docs", docCards))
	set("workflows", finalizeSurface("workflows", workflowCards))
	set("tests", finalizeSurface("tests", buildTestCards(prof, urlTemplate, absRepoRoot)))
	set("examples", finalizeSurface("examples", buildExampleCards(prof, urlTemplate, absRepoRoot)))
	if keep {
		set("implementations", finalizeSurface("implementations",
			buildImplementationsCards(prof, urlTemplate, absRepoRoot)))
	}
	// Suppress a DOCS card that merely re-quotes the contract's own source.
	// For inline/colocated docs (FIDL /// comments live in the contract
	// file) the doc card points at the same path:line as the contract, so
	// the two cards are near-identical; replace it with a one-line
	// "documented inline" note pointing back at Contract.
	if ci, di := indexOf(keys, "contract"), indexOf(keys, "docs"); ci >= 0 && di >= 0 && len(panel.Surfaces[ci].Cards) > 0 && len(panel.Surfaces[di].Cards) > 0 {
		contractSrc := make(map[string]bool, len(panel.Surfaces[ci].Cards))
		for _, c := range panel.Surfaces[ci].Cards {
			contractSrc[fmt.Sprintf("%s:%d", c.Path, c.Line)] = true
		}
		allInline := true
		for _, c := range panel.Surfaces[di].Cards {
			if !contractSrc[fmt.Sprintf("%s:%d", c.Path, c.Line)] {
				allInline = false
				break
			}
		}
		if allInline {
			panel.Surfaces[di] = EvidenceSurface{Key: "docs", Label: surfaceLabels["docs"], EmptyCopy: "documented inline — see Contract above", Inline: true}
		}
	}
	for _, s := range panel.Surfaces {
		if len(s.Cards) > 0 || s.Inline {
			panel.SurfacesPresent++
		} else {
			panel.SurfacesAbsent = append(panel.SurfacesAbsent, surfaceAbsentName[s.Key])
		}
	}
	return panel
}

func finalizeSurface(key string, cards []EvidenceCard) EvidenceSurface {
	s := EvidenceSurface{Key: key, Label: surfaceLabels[key]}
	const maxPerSurface = 3
	if len(cards) == 0 {
		s.EmptyCopy = surfaceEmptyCopy[key]
		return s
	}
	if len(cards) > maxPerSurface {
		extra := len(cards) - maxPerSurface
		s.ExtraRefs = extra
		s.ExtraNoun = pluralExtraNoun(key)
		s.ExtraURL = cards[maxPerSurface].URL
		cards = cards[:maxPerSurface]
	}
	s.Cards = cards
	return s
}

func pluralExtraNoun(key string) string {
	switch key {
	case "tests":
		return "tests"
	case "examples":
		return "examples"
	case "workflows":
		return "recipes"
	default:
		return "refs"
	}
}

// ----------------------------------------------------------------------
// Contract surface
// ----------------------------------------------------------------------

func buildContractSurface(e map[string]any, urlTemplate, absRepoRoot string) EvidenceSurface {
	s := EvidenceSurface{Key: "contract", Label: surfaceLabels["contract"]}
	if e == nil {
		s.EmptyCopy = "Contract element not in scan."
		return s
	}
	path, line := locPathLine(e["location"])
	if path == "" {
		s.EmptyCopy = "Contract element has no source location."
		return s
	}
	url := pickURL(locURL(e["location"]), urlTemplate, absRepoRoot, path, line)
	text, totalLines, truncated := readContractLines(absRepoRoot, path, line, contractCapForExt(path))
	if text == "" {
		excerpt, _ := e["docCommentExcerpt"].(string)
		excerpt = strings.TrimSpace(excerpt)
		if excerpt == "" {
			s.EmptyCopy = "Contract source not readable from this report."
			return s
		}
		s.Cards = []EvidenceCard{{
			Path: path, Line: line, URL: url, Kind: "prose", Text: excerpt,
			MetaLabel: contractMetaLabel(path, 1, false),
		}}
		return s
	}
	card := EvidenceCard{
		Path: path, Line: line, URL: url, Kind: "code", Text: text,
		MetaLabel: contractMetaLabel(path, totalLines, truncated),
	}
	if truncated {
		rendered := strings.Count(text, "\n") + 1
		if more := totalLines - rendered; more > 0 {
			card.TruncMore = more
			card.TruncNoun = "lines"
			card.TruncURL = url
		}
	}
	s.Cards = []EvidenceCard{card}
	return s
}

func contractCapForExt(path string) int {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".proto", ".fidl":
		return 12
	case ".rs", ".go", ".py", ".ts", ".js":
		return 16
	default:
		return 12
	}
}

func contractMetaLabel(path string, totalLines int, truncated bool) string {
	lang := languageLabel(path)
	noun := "lines"
	if totalLines == 1 {
		noun = "line"
	}
	if truncated {
		return fmt.Sprintf("%s · %d %s · truncated", lang, totalLines, noun)
	}
	return fmt.Sprintf("%s · %d %s", lang, totalLines, noun)
}

// ----------------------------------------------------------------------
// Docs / Workflows surface
// ----------------------------------------------------------------------

type docRefLocFull struct {
	path     string
	line     int
	url      string
	words    int
	workflow bool
	adapter  string
	external bool
}

func buildDocsSurfaces(
	elementID string,
	prof map[string]any,
	urlTemplate, absRepoRoot string,
	wc *workflowClassifier,
) (docs, workflows []EvidenceCard) {
	if prof == nil {
		return nil, nil
	}
	refs := collectDocRefs(prof)
	type key struct {
		path string
		line int
	}
	seen := map[key]int{}
	var deduped []docRefLocFull
	for _, r := range refs {
		k := key{r.path, r.line}
		if idx, ok := seen[k]; ok {
			if r.workflow && !deduped[idx].workflow {
				deduped[idx].workflow = true
			}
			continue
		}
		seen[k] = len(deduped)
		deduped = append(deduped, r)
	}
	for i := range deduped {
		if !deduped[i].workflow {
			deduped[i].workflow = wc.matchesPath(deduped[i].path)
		}
	}
	sortRefs := func(rs []docRefLocFull) {
		sort.SliceStable(rs, func(i, j int) bool {
			ri, rj := rs[i], rs[j]
			if ri.external != rj.external {
				return !ri.external
			}
			if ri.words != rj.words {
				return ri.words > rj.words
			}
			return ri.path < rj.path
		})
	}
	var docRefs, wfRefs []docRefLocFull
	for _, r := range deduped {
		if r.workflow {
			wfRefs = append(wfRefs, r)
		} else {
			docRefs = append(docRefs, r)
		}
	}
	sortRefs(docRefs)
	sortRefs(wfRefs)
	docs = refsToDocCards(elementID, docRefs, urlTemplate, absRepoRoot)
	workflows = refsToDocCards(elementID, wfRefs, urlTemplate, absRepoRoot)
	return
}

func refsToDocCards(elementID string, refs []docRefLocFull, urlTemplate, absRepoRoot string) []EvidenceCard {
	var out []EvidenceCard
	for _, r := range refs {
		if r.external {
			continue // R-UI-14: external refs render as link pills, not cards
		}
		url := pickURL(r.url, urlTemplate, absRepoRoot, r.path, r.line)
		text, totalParagraphs, truncated := readDocParagraph(absRepoRoot, r.path, r.line, elementID)
		card := EvidenceCard{
			Path: r.path, Line: r.line, URL: url, Kind: "prose",
			Text: text, MetaLabel: docMetaLabel(r.path, r.words),
		}
		if truncated && totalParagraphs > 1 {
			card.TruncMore = totalParagraphs - 1
			card.TruncNoun = "paragraphs"
			card.TruncURL = url
		}
		out = append(out, card)
	}
	return out
}

func docMetaLabel(path string, words int) string {
	lang := languageLabel(path)
	if words > 0 {
		return fmt.Sprintf("%s · %d words", lang, words)
	}
	return lang
}

func collectDocRefs(p map[string]any) []docRefLocFull {
	if p == nil {
		return nil
	}
	d, _ := p["docs"].(map[string]any)
	if d == nil {
		return nil
	}
	var out []docRefLocFull
	emit := func(arr []any, adapter string) {
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			path, _ := m["path"].(string)
			line := numAsInt(m["line"])
			url, _ := m["url"].(string)
			words := numAsInt(m["words"])
			isWorkflow, _ := m["isWorkflow"].(bool)
			external := path == "" || strings.Contains(path, "://") || strings.HasPrefix(path, "http")
			out = append(out, docRefLocFull{
				path: path, line: line, url: url, words: words,
				workflow: isWorkflow, adapter: adapter, external: external,
			})
		}
	}
	for _, key := range []string{"concept", "tutorial", "releaseNotes", "faq"} {
		if arr, ok := d[key].([]any); ok {
			emit(arr, key)
		}
	}
	if g, ok := d["guide"].(map[string]any); ok {
		for _, key := range []string{"migration", "troubleshooting", "cookbook"} {
			if arr, ok := g[key].([]any); ok {
				emit(arr, "guide."+key)
			}
		}
	}
	if ref, ok := d["reference"].(map[string]any); ok {
		walkReferenceRefs(ref, func(m map[string]any) { emit([]any{m}, "reference") })
	}
	return out
}

// ----------------------------------------------------------------------
// Implementations surface (interface kinds only)
// ----------------------------------------------------------------------

// buildImplementationsCards reads the implementations.impls[] list
// from the profile and renders one card per implementor. The "fragment"
// body is the impl element's identifier in mono — the surface is a
// pointer at a concrete class, not a quoted source body.
func buildImplementationsCards(prof map[string]any, urlTemplate, absRepoRoot string) []EvidenceCard {
	if prof == nil {
		return nil
	}
	im, _ := prof["implementations"].(map[string]any)
	if im == nil {
		return nil
	}
	impls, _ := im["impls"].([]any)
	if len(impls) == 0 {
		return nil
	}
	type entry struct {
		id, kind, path string
		line           int
	}
	var rs []entry
	for _, item := range impls {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		id, _ := m["implElementId"].(string)
		if id == "" {
			continue
		}
		rs = append(rs, entry{
			id:   id,
			kind: firstString(m["implKind"]),
			path: firstString(m["path"]),
			line: numAsInt(m["line"]),
		})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].id < rs[j].id })
	var out []EvidenceCard
	for _, r := range rs {
		url := pickURL("", urlTemplate, absRepoRoot, r.path, r.line)
		meta := r.kind
		if meta == "" {
			meta = "impl"
		}
		card := EvidenceCard{
			Path: r.path, Line: r.line, URL: url,
			Kind: "code", Text: r.id, MetaLabel: meta,
		}
		out = append(out, card)
	}
	return out
}

// ----------------------------------------------------------------------
// Tests surface
// ----------------------------------------------------------------------

func buildTestCards(prof map[string]any, urlTemplate, absRepoRoot string) []EvidenceCard {
	if prof == nil {
		return nil
	}
	t, _ := prof["tests"].(map[string]any)
	if t == nil {
		return nil
	}
	type testEntry struct {
		path, name string
		line       int
		url        string
	}
	var refs []testEntry
	for _, key := range []string{"unit", "integration", "e2e", "ctf", "performance", "fuzz", "golden"} {
		arr, ok := t[key].([]any)
		if !ok {
			continue
		}
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			path, _ := m["path"].(string)
			if path == "" {
				continue
			}
			refs = append(refs, testEntry{
				path: path,
				line: numAsInt(m["line"]),
				name: firstString(m["testName"]),
				url:  firstString(m["url"]),
			})
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].path != refs[j].path {
			return refs[i].path < refs[j].path
		}
		return refs[i].line < refs[j].line
	})
	var out []EvidenceCard
	for _, r := range refs {
		url := pickURL(r.url, urlTemplate, absRepoRoot, r.path, r.line)
		text, totalLines, truncated := readTestFunctionBody(absRepoRoot, r.path, r.line, 30)
		meta := languageLabel(r.path)
		if totalLines > 0 {
			meta += fmt.Sprintf(" · %d lines", totalLines)
		}
		if r.name != "" {
			meta = r.name + " · " + meta
		}
		card := EvidenceCard{
			Path: r.path, Line: r.line, URL: url,
			Kind: "code", Text: text, MetaLabel: meta,
		}
		if truncated {
			rendered := strings.Count(text, "\n") + 1
			if more := totalLines - rendered; more > 0 {
				card.TruncMore = more
				card.TruncNoun = "lines"
				card.TruncURL = url
			}
		}
		out = append(out, card)
	}
	return out
}

// ----------------------------------------------------------------------
// Examples surface
// ----------------------------------------------------------------------

func buildExampleCards(prof map[string]any, urlTemplate, absRepoRoot string) []EvidenceCard {
	if prof == nil {
		return nil
	}
	x, _ := prof["examples"].(map[string]any)
	if x == nil {
		return nil
	}
	type entry struct {
		path       string
		start, end int
		intent     string
		external   bool
	}
	var refs []entry
	emit := func(arr []any, external bool) {
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			path, _ := m["path"].(string)
			if path == "" {
				continue
			}
			start := numAsInt(m["startLine"])
			end := numAsInt(m["endLine"])
			if end < start {
				end = start
			}
			refs = append(refs, entry{
				path: path, start: start, end: end,
				intent:   firstString(m["intent"]),
				external: external,
			})
		}
	}
	if arr, ok := x["inTree"].([]any); ok {
		emit(arr, false)
	}
	if arr, ok := x["inDocs"].([]any); ok {
		emit(arr, false)
	}
	if arr, ok := x["external"].([]any); ok {
		emit(arr, true)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].external != refs[j].external {
			return !refs[i].external
		}
		span := func(e entry) int { return e.end - e.start }
		if span(refs[i]) != span(refs[j]) {
			return span(refs[i]) > span(refs[j])
		}
		if refs[i].path != refs[j].path {
			return refs[i].path < refs[j].path
		}
		return refs[i].start < refs[j].start
	})
	var out []EvidenceCard
	for _, r := range refs {
		if r.external {
			continue
		}
		url := pickURL("", urlTemplate, absRepoRoot, r.path, r.start)
		text, totalLines, truncated := readCodeLines(absRepoRoot, r.path, r.start, r.end, 16)
		meta := languageLabel(r.path)
		if totalLines > 0 {
			meta += fmt.Sprintf(" · %d lines", totalLines)
		}
		card := EvidenceCard{
			Path: r.path, Line: r.start, URL: url,
			Kind: "code", Text: text, MetaLabel: meta,
		}
		if truncated {
			rendered := strings.Count(text, "\n") + 1
			if more := totalLines - rendered; more > 0 {
				card.TruncMore = more
				card.TruncNoun = "lines"
				card.TruncURL = url
			}
		}
		out = append(out, card)
	}
	return out
}

// ----------------------------------------------------------------------
// File-reading helpers
// ----------------------------------------------------------------------

// readContractLines reads the declaration starting at startLine plus
// up to 6 lines of preceding consecutive comment. Returns the
// dedented body, the total source-span line count, and whether
// truncation occurred. Best-effort: uses brace balance for {-style
// languages and a trailing `;` heuristic for single-line decls.
func readContractLines(absRoot, path string, startLine, capLines int) (string, int, bool) {
	if absRoot == "" || path == "" || startLine <= 0 {
		return "", 0, false
	}
	full := filepath.Join(absRoot, path)
	data, err := os.ReadFile(full)
	if err != nil {
		return "", 0, false
	}
	lines := strings.Split(string(data), "\n")
	if startLine > len(lines) {
		return "", 0, false
	}
	commentLines := []string{}
	for i := startLine - 2; i >= 0 && len(commentLines) < 6; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if isCommentLine(trimmed) {
			commentLines = append([]string{lines[i]}, commentLines...)
			continue
		}
		break
	}
	bodyStartIdx := startLine - 1
	bodyEndIdx := bodyStartIdx
	balance := 0
	seenOpen := false
	for i := bodyStartIdx; i < len(lines); i++ {
		line := lines[i]
		for _, r := range line {
			switch r {
			case '{', '(':
				balance++
				seenOpen = true
			case '}', ')':
				balance--
			}
		}
		bodyEndIdx = i
		if seenOpen && balance <= 0 {
			break
		}
		if !seenOpen && strings.HasSuffix(strings.TrimSpace(line), ";") {
			break
		}
		if i-bodyStartIdx+1 >= capLines {
			break
		}
	}
	totalBody := bodyEndIdx - bodyStartIdx + 1
	truncated := false
	if totalBody > capLines {
		bodyEndIdx = bodyStartIdx + capLines - 1
		truncated = true
	}
	allLines := append(commentLines, lines[bodyStartIdx:bodyEndIdx+1]...)
	return dedent(allLines), len(commentLines) + totalBody, truncated
}

// readCodeLines reads a fixed range [startLine, endLine] (1-based
// inclusive) and dedents.
func readCodeLines(absRoot, path string, startLine, endLine, capLines int) (string, int, bool) {
	if absRoot == "" || path == "" || startLine <= 0 {
		return "", 0, false
	}
	full := filepath.Join(absRoot, path)
	data, err := os.ReadFile(full)
	if err != nil {
		return "", 0, false
	}
	lines := strings.Split(string(data), "\n")
	if startLine > len(lines) {
		return "", 0, false
	}
	if endLine <= 0 || endLine < startLine {
		endLine = startLine
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	totalLines := endLine - startLine + 1
	truncated := false
	if totalLines > capLines {
		endLine = startLine + capLines - 1
		truncated = true
	}
	return dedent(lines[startLine-1 : endLine]), totalLines, truncated
}

// readTestFunctionBody reads from startLine until brace balance
// returns to zero, capped at capLines.
func readTestFunctionBody(absRoot, path string, startLine, capLines int) (string, int, bool) {
	if absRoot == "" || path == "" || startLine <= 0 {
		return "", 0, false
	}
	full := filepath.Join(absRoot, path)
	data, err := os.ReadFile(full)
	if err != nil {
		return "", 0, false
	}
	lines := strings.Split(string(data), "\n")
	if startLine > len(lines) {
		return "", 0, false
	}
	balance := 0
	seenOpen := false
	endIdx := startLine - 1
	for i := startLine - 1; i < len(lines); i++ {
		for _, r := range lines[i] {
			switch r {
			case '{':
				balance++
				seenOpen = true
			case '}':
				balance--
			}
		}
		endIdx = i
		if seenOpen && balance <= 0 {
			break
		}
	}
	totalLines := endIdx - (startLine - 1) + 1
	truncated := false
	if totalLines > capLines {
		endIdx = startLine - 1 + capLines - 1
		truncated = true
	}
	return dedent(lines[startLine-1 : endIdx+1]), totalLines, truncated
}

// readDocParagraph reads the paragraph at startLine, preferring one
// that mentions mentionToken. Caps at 600 chars.
func readDocParagraph(absRoot, path string, startLine int, mentionToken string) (string, int, bool) {
	if absRoot == "" || path == "" || startLine <= 0 {
		return "", 0, false
	}
	full := filepath.Join(absRoot, path)
	data, err := os.ReadFile(full)
	if err != nil {
		return "", 0, false
	}
	lines := strings.Split(string(data), "\n")
	if startLine > len(lines) {
		startLine = len(lines)
	}
	idx := startLine - 1
	totalParagraphs := 0
	var primary string
	for idx < len(lines) {
		for idx < len(lines) && isParaSkip(lines[idx]) {
			idx++
		}
		if idx >= len(lines) {
			break
		}
		var buf []string
		for idx < len(lines) && !isParaBreak(lines[idx]) {
			buf = append(buf, lines[idx])
			idx++
		}
		para := collapseWhitespace(strings.Join(buf, " "))
		if para != "" {
			totalParagraphs++
			if primary == "" {
				if mentionToken == "" || paragraphMentions(para, mentionToken) {
					primary = para
				}
			}
		}
	}
	if primary == "" && totalParagraphs > 0 {
		primary, _ = firstParagraph(lines, startLine-1)
	}
	if primary == "" {
		return "", totalParagraphs, false
	}
	truncated := false
	if len(primary) > 600 {
		primary = primary[:600] + "…"
		truncated = true
	}
	if totalParagraphs > 1 {
		truncated = true
	}
	return primary, totalParagraphs, truncated
}

func firstParagraph(lines []string, fromIdx int) (string, int) {
	idx := fromIdx
	for idx < len(lines) && isParaSkip(lines[idx]) {
		idx++
	}
	var buf []string
	for idx < len(lines) && !isParaBreak(lines[idx]) {
		buf = append(buf, lines[idx])
		idx++
	}
	return collapseWhitespace(strings.Join(buf, " ")), idx
}

// ----------------------------------------------------------------------
// Misc helpers
// ----------------------------------------------------------------------

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func isCommentLine(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	switch {
	case strings.HasPrefix(trimmed, "//"),
		strings.HasPrefix(trimmed, "#"),
		strings.HasPrefix(trimmed, "*"),
		strings.HasPrefix(trimmed, "/*"),
		strings.HasPrefix(trimmed, "--"):
		return true
	}
	return false
}

func isParaSkip(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return true
	}
	if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "=") || strings.HasPrefix(t, "---") {
		return true
	}
	if strings.HasPrefix(t, "```") || strings.HasPrefix(t, ".. ") || strings.HasPrefix(t, ":::") {
		return true
	}
	return false
}

func isParaBreak(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return true
	}
	if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") {
		return true
	}
	return false
}

func collapseWhitespace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

func paragraphMentions(paragraph, elementID string) bool {
	tail := elementID
	if i := strings.LastIndexAny(elementID, "/."); i >= 0 {
		tail = elementID[i+1:]
	}
	if tail == "" {
		return false
	}
	return strings.Contains(strings.ToLower(paragraph), strings.ToLower(tail))
}

// dedent strips the longest common leading-whitespace prefix.
func dedent(lines []string) string {
	prefix := ""
	first := true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lead := leadingWhitespace(l)
		if first {
			prefix = lead
			first = false
			continue
		}
		prefix = commonPrefix(prefix, lead)
		if prefix == "" {
			break
		}
	}
	if prefix == "" {
		return strings.TrimRight(strings.Join(lines, "\n"), "\n")
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimPrefix(l, prefix)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func leadingWhitespace(s string) string {
	for i, r := range s {
		if !unicode.IsSpace(r) {
			return s[:i]
		}
	}
	return s
}

func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

func languageLabel(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".proto":
		return "protobuf"
	case ".fidl":
		return "FIDL"
	case ".go":
		return "Go"
	case ".rs":
		return "Rust"
	case ".py":
		return "Python"
	case ".ts":
		return "TypeScript"
	case ".js":
		return "JavaScript"
	case ".rst":
		return "reStructuredText"
	case ".md", ".markdown":
		return "Markdown"
	case ".yaml", ".yml":
		return "YAML"
	case ".json":
		return "JSON"
	case ".toml":
		return "TOML"
	case ".cc", ".cpp", ".cxx", ".h", ".hpp":
		return "C++"
	default:
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
		if ext == "" {
			return "text"
		}
		return ext
	}
}
