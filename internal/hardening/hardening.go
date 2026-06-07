// Package hardening generates sheaf-hardening.md — the third artifact of
// a `sheaf scan --auto` run: a ranked backlog of how to replace LLM
// reasoning with deterministic adapters/config.
//
// The entries are not hand-written; they FALL OUT of real signals from
// the scan, which is the whole point — the doc is evidence that the
// funnel surfaces actionable, correctly-scoped items:
//
//   - "You have a schema" — a schema-backed ecosystem (proto/fidl/clap/
//     cml) was detected; wire its authoritative adapter.
//   - "Cheap deterministic pass: #define graph" — a lexical scan of the
//     in-scope headers found macro→macro alias edges (e.g. Pigweed's
//     DBG→PW_LOG_DEBUG, PW_LOG→PW_HANDLE_LOG) that a #define-graph pass
//     could resolve with no toolchain. (Per the design doc this is
//     DETECTED and reported, never auto-expanded.)
//   - "Extend an existing adapter" — elements the LLM tier caught that
//     the deterministic cppheader regex missed, classified by syntax
//     form (`using X = MACRO;`, `inline constexpr`).
//
// Each entry records leverage (rows it would pin) so the list is ranked.
package hardening

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/autodetect"
	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// Input is everything Generate needs. Corpus elements must already carry
// RowProvenance (stamped by the orchestrator).
type Input struct {
	RepoRoot  string
	ProjectID string
	Detection *autodetect.Result
	Corpus    *corpus.Corpus
}

// entry is one ranked hardening item.
type entry struct {
	rung    int // 1=schema, 2=cheap deterministic, 3=extend adapter, 4=irreducible
	title   string
	didWhat string // what the LLM (or the gap) is doing today
	fix     string // the deterministic replacement
	effort  string
	rows    int      // leverage: rows it would pin / move to deterministic
	samples []string // up to a few concrete examples
}

// Generate returns the markdown for sheaf-hardening.md.
func Generate(in Input) string {
	var elems []*contractpb.ContractElement
	if in.Corpus != nil {
		elems = in.Corpus.Elements()
	}
	detCount, llmCount := tierCounts(elems)
	llmOnly := llmOnlySymbols(elems)

	var entries []entry
	entries = append(entries, schemaEntries(in.Detection)...)
	if e, ok := defineGraphEntry(in.RepoRoot, elems); ok {
		entries = append(entries, e)
	}
	if e, ok := cppheaderExtensionEntry(in.RepoRoot, llmOnly); ok {
		entries = append(entries, e)
	}
	entries = append(entries, workflowEntries(in.Corpus)...)

	// Rank: highest leverage first, then by rung (cheaper wins ties).
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].rows != entries[j].rows {
			return entries[i].rows > entries[j].rows
		}
		return entries[i].rung < entries[j].rung
	})

	return render(in, detCount, llmCount, len(llmOnly), entries)
}

// ---- tier accounting -------------------------------------------------

func tierCounts(elems []*contractpb.ContractElement) (deterministic, llm int) {
	for _, e := range elems {
		if e.GetProvenance().GetTier() == commonpb.RowProvenance_LLM {
			llm++
		} else {
			deterministic++
		}
	}
	return
}

// llmOnlySymbols returns LLM-tier elements whose local symbol name is NOT
// also produced by a deterministic adapter — the LLM's marginal recall
// (or noise). This is the eval's "llmextract-only" set, derived here from
// per-row provenance instead of a side-by-side adapter run.
func llmOnlySymbols(elems []*contractpb.ContractElement) []*contractpb.ContractElement {
	detLocal := map[string]bool{}
	for _, e := range elems {
		if e.GetProvenance().GetTier() != commonpb.RowProvenance_LLM {
			detLocal[localName(e.GetId())] = true
		}
	}
	var out []*contractpb.ContractElement
	for _, e := range elems {
		if e.GetProvenance().GetTier() == commonpb.RowProvenance_LLM && !detLocal[localName(e.GetId())] {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out
}

func localName(id string) string {
	if i := strings.Index(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.LastIndex(id, "::"); i >= 0 {
		return id[i+2:]
	}
	return id
}

// ---- rung 1: schema available ---------------------------------------

func schemaEntries(det *autodetect.Result) []entry {
	if det == nil {
		return nil
	}
	// Only schema-backed ecosystems qualify. cppheader/llmextract are not
	// schema generators; gtest/rust-test/markdown/rst are not contract.
	schema := map[string]string{
		"proto": "protoc --descriptor_set_out",
		"fidl":  "fidlc JSON IR",
		"clap":  "clap derive AST",
		"cml":   "CML config-block parse",
	}
	var out []entry
	for _, d := range det.Contract() {
		gen, ok := schema[d.Adapter]
		if !ok {
			continue
		}
		out = append(out, entry{
			rung:    1,
			title:   fmt.Sprintf("You have a schema (%s) — stop guessing", d.Adapter),
			didWhat: fmt.Sprintf("%d %s file(s) detected; their contract surface is machine-readable.", d.FileCount, d.Adapter),
			fix:     fmt.Sprintf("Wire the %s adapter (%s) — exact elements, no LLM cost or hallucination risk.", d.Adapter, gen),
			effort:  "low (adapter exists; config already assigns it)",
			rows:    d.FileCount,
			samples: d.Samples,
		})
	}
	return out
}

// ---- rung 2: #define alias graph -------------------------------------

// defineGraphEntry walks the in-scope header files (derived from element
// locations) and finds macro→macro alias edges: a `#define A ... B ...`
// whose body's leading identifier B is itself a #define in the same set.
// These are exactly the Pigweed recall gaps (DBG→PW_LOG_DEBUG,
// PW_LOG→PW_HANDLE_LOG) the design doc calls out as a rung-1 lexical
// pass. We DETECT and report; we never expand.
func defineGraphEntry(repoRoot string, elems []*contractpb.ContractElement) (entry, bool) {
	headers := headerPaths(elems)
	if len(headers) == 0 {
		return entry{}, false
	}
	defs, bodies := scanDefines(repoRoot, headers)
	if len(defs) == 0 {
		return entry{}, false
	}
	var edges []string
	for name, body := range bodies {
		lead := leadingIdent(body)
		if lead != "" && lead != name && defs[lead] {
			edges = append(edges, fmt.Sprintf("%s → %s", name, lead))
		}
	}
	if len(edges) == 0 {
		return entry{}, false
	}
	sort.Strings(edges)
	return entry{
		rung:    2,
		title:   "Cheap deterministic pass available: #define alias graph",
		didWhat: fmt.Sprintf("%d macro→macro alias edge(s) found among %d in-scope #defines. Symbols reachable only through an alias (the alias's name vs the underlying macro) are a known regex/LLM recall gap.", len(edges), len(defs)),
		fix:     "Add a lexical #define-graph pass (no LLVM / no preprocessor): build the alias map and emit each alias as a SAME_AS edge to its target macro. Resolves the gap deterministically.",
		effort:  "low (single lexical pass over headers; no toolchain)",
		rows:    len(edges),
		samples: capStrings(edges, 8),
	}, true
}

func headerPaths(elems []*contractpb.ContractElement) []string {
	seen := map[string]bool{}
	for _, e := range elems {
		p := e.GetLocation().GetPath()
		lp := strings.ToLower(p)
		if strings.HasSuffix(lp, ".h") || strings.HasSuffix(lp, ".hpp") || strings.HasSuffix(lp, ".hh") {
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// scanDefines returns the set of defined macro names and a map of
// name→body for object/function-like #defines across the given files.
func scanDefines(repoRoot string, files []string) (map[string]bool, map[string]string) {
	defs := map[string]bool{}
	bodies := map[string]string{}
	for _, rel := range files {
		b, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			name, body, ok := parseDefine(line)
			if !ok {
				continue
			}
			defs[name] = true
			if _, exists := bodies[name]; !exists {
				bodies[name] = body
			}
		}
	}
	return defs, bodies
}

// parseDefine extracts (name, body) from a `#define` line. For a
// function-like macro the parameter list is stripped from the body.
func parseDefine(line string) (name, body string, ok bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "#") {
		return "", "", false
	}
	s = strings.TrimSpace(s[1:])
	if !strings.HasPrefix(s, "define") {
		return "", "", false
	}
	s = strings.TrimSpace(s[len("define"):])
	// name is the leading identifier.
	i := 0
	for i < len(s) && isIdentByte(s[i]) {
		i++
	}
	if i == 0 {
		return "", "", false
	}
	name = s[:i]
	rest := s[i:]
	// Skip a function-like parameter list.
	if strings.HasPrefix(rest, "(") {
		if j := strings.Index(rest, ")"); j >= 0 {
			rest = rest[j+1:]
		}
	}
	return name, strings.TrimSpace(rest), true
}

func leadingIdent(body string) string {
	body = strings.TrimSpace(body)
	i := 0
	for i < len(body) && isIdentByte(body[i]) {
		i++
	}
	return body[:i]
}

// ---- rung 3: extend cppheader ----------------------------------------

// cppheaderExtensionEntry classifies the LLM-only elements by the C++
// syntax form their citation sits on, surfacing the forms the cppheader
// regex misses (`using X = MACRO;` aliases, `inline constexpr` constants).
func cppheaderExtensionEntry(repoRoot string, llmOnly []*contractpb.ContractElement) (entry, bool) {
	if len(llmOnly) == 0 {
		return entry{}, false
	}
	var forms []string
	for _, e := range llmOnly {
		line := sourceLine(repoRoot, e.GetLocation().GetPath(), int(e.GetLocation().GetLine()))
		form := classifyForm(line)
		if form == "" {
			continue
		}
		forms = append(forms, fmt.Sprintf("%s  [%s]", e.GetId(), form))
	}
	if len(forms) == 0 {
		return entry{}, false
	}
	sort.Strings(forms)
	return entry{
		rung:    3,
		title:   "Extend an existing adapter: cppheader misses alias/constexpr forms",
		didWhat: fmt.Sprintf("The LLM tier caught %d public element(s) the cppheader regex did not, on syntax forms it doesn't model (e.g. `using X = MACRO;`, `inline constexpr`).", len(forms)),
		fix:     "Extend cppheader's regex set to recognize `using <Name> = <expr>;` type aliases and `[inline] constexpr <type> <Name>` constants. Moves these rows from LLM to deterministic.",
		effort:  "medium (extend cppheader; add golden cases)",
		rows:    len(forms),
		samples: capStrings(forms, 10),
	}, true
}

func classifyForm(line string) string {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "using ") && strings.Contains(t, "="):
		return "using-alias"
	case strings.Contains(t, "constexpr"):
		return "inline-constexpr"
	case strings.HasPrefix(t, "typedef "):
		return "typedef"
	default:
		return ""
	}
}

func sourceLine(repoRoot, rel string, line int) string {
	if rel == "" || line <= 0 {
		return ""
	}
	b, err := adapters.ReadFile(repoRoot, rel)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(b), "\n")
	if line > len(lines) {
		return ""
	}
	return lines[line-1]
}

// ---- rendering -------------------------------------------------------

func render(in Input, detCount, llmCount, llmOnly int, entries []entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# sheaf-hardening.md\n\n")
	fmt.Fprintf(&b, "Generated by `sheaf scan --auto` for `%s`. A ranked backlog for replacing\n", in.ProjectID)
	b.WriteString("LLM reasoning with deterministic adapters/config. Work the top item first;\n")
	b.WriteString("each entry records the rows it would move from the LLM tier to deterministic.\n\n")

	b.WriteString("## Tier accounting\n\n")
	total := detCount + llmCount
	fmt.Fprintf(&b, "- Deterministic elements: **%d**\n", detCount)
	fmt.Fprintf(&b, "- LLM-tier elements: **%d**\n", llmCount)
	fmt.Fprintf(&b, "- LLM-only (not also found deterministically): **%d**\n", llmOnly)
	fmt.Fprintf(&b, "- Total: **%d**\n\n", total)
	b.WriteString("Healthy end-state: the LLM tier shrinks to the genuine schemaless tail as\n")
	b.WriteString("the items below are worked.\n\n")

	if len(entries) == 0 {
		b.WriteString("## Backlog\n\n_No hardening items detected — the deterministic tier already covers the surface._\n")
		return b.String()
	}

	b.WriteString("## Backlog (ranked by leverage)\n\n")
	for i, e := range entries {
		fmt.Fprintf(&b, "### %d. %s\n\n", i+1, e.title)
		fmt.Fprintf(&b, "- **Rung:** %d (%s)\n", e.rung, rungName(e.rung))
		fmt.Fprintf(&b, "- **What the LLM/gap is doing:** %s\n", e.didWhat)
		fmt.Fprintf(&b, "- **Deterministic replacement:** %s\n", e.fix)
		fmt.Fprintf(&b, "- **Effort:** %s\n", e.effort)
		fmt.Fprintf(&b, "- **Impact (rows pinned):** %d\n", e.rows)
		if len(e.samples) > 0 {
			b.WriteString("- **Examples:**\n")
			for _, s := range e.samples {
				fmt.Fprintf(&b, "  - `%s`\n", s)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func rungName(r int) string {
	switch r {
	case 1:
		return "schema available"
	case 2:
		return "cheap deterministic pass"
	case 3:
		return "extend an existing adapter"
	default:
		return "irreducible tail"
	}
}

func capStrings(ss []string, n int) []string {
	if len(ss) <= n {
		return ss
	}
	out := append([]string(nil), ss[:n]...)
	out = append(out, fmt.Sprintf("… and %d more", len(ss)-n))
	return out
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
