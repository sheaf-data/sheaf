package hardening

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// workflowEntries groups the LLM-tier WORKFLOW claims (from
// internal/workflowextract) by the SOURCE grammar tag stamped on their
// provenance and emits one ranked hardening entry per warranted class.
//
// The entries FALL OUT of real signal: a class only produces an entry
// when the scan actually extracted workflows of that grammar. Leverage is
// the number of workflows the deterministic replacement would pin, so the
// shared ranking surfaces the highest-leverage replacement first.
//
//	workflow-cli-grammar  → rung 2: wire the `workflows` adapter
//	workflow-yaml-grammar → rung 1: wire the `yaml-workflows` adapter
//	workflow-prose        → rung 4: irreducible tail, record spot-check recall
func workflowEntries(c *corpus.Corpus) []entry {
	if c == nil {
		return nil
	}
	byTransform := map[string][]*docclaimpb.DocClaim{}
	for _, dc := range c.DocClaims() {
		if dc.GetKind() != docclaimpb.DocClaimKind_WORKFLOW {
			continue
		}
		if dc.GetProvenance().GetTier() != commonpb.RowProvenance_LLM {
			continue
		}
		t := dc.GetProvenance().GetTransform()
		byTransform[t] = append(byTransform[t], dc)
	}

	var out []entry
	if cli := byTransform["workflow-cli-grammar"]; len(cli) > 0 {
		docsDir := commonDocsDir(cli)
		binary := dominantBinaryName(cli)
		out = append(out, entry{
			rung:  2,
			title: "Wire the `workflows` adapter — CLI-command workflows are deterministic",
			didWhat: fmt.Sprintf("%d LLM workflow(s) were extracted from CLI-command grammar "+
				"(`<binary> <subcommand>` fences). The command sequence is mechanically "+
				"parseable — the LLM is doing work a grammar adapter already models.", len(cli)),
			fix: fmt.Sprintf("Wire the `workflows` rendered-reference adapter "+
				"(binary_name=%q, docs_dir=%q) to replace them deterministically.", binary, docsDir),
			effort:  "low (adapter exists; add a rendered_reference block)",
			rows:    len(cli),
			samples: workflowSamples(cli),
		})
	}
	if yaml := byTransform["workflow-yaml-grammar"]; len(yaml) > 0 {
		docsDir := commonDocsDir(yaml)
		idlPrefix := dominantIDLPrefix(yaml)
		fix := fmt.Sprintf("Wire the `yaml-workflows` adapter (idl_prefix=%q, docs_dir=%q) — "+
			"the yaml blocks name dotted FQDNs, a schema-backed surface.", idlPrefix, docsDir)
		out = append(out, entry{
			rung:  1,
			title: "Wire the `yaml-workflows` adapter — you have a schema",
			didWhat: fmt.Sprintf("%d LLM workflow(s) were extracted from fenced yaml naming "+
				"two-or-more dotted FQDNs. The composition is declared against a schema; "+
				"the LLM is reading what a yaml parser could.", len(yaml)),
			fix:     fix,
			effort:  "low (adapter exists; add a rendered_reference block)",
			rows:    len(yaml),
			samples: workflowSamples(yaml),
		})
	}
	if prose := byTransform["workflow-prose"]; len(prose) > 0 {
		out = append(out, entry{
			rung:  4,
			title: "Workflow composition in free prose — likely the irreducible tail",
			didWhat: fmt.Sprintf("%d LLM workflow(s) were extracted from free prose with no "+
				"parseable command or yaml grammar. There is no deterministic surface to "+
				"wire — this is the schemaless tail the LLM tier exists for.", len(prose)),
			fix: "No deterministic replacement. Record the spot-check recall on these " +
				"workflows so the LLM tier can be trusted (and re-evaluate if a grammar emerges).",
			effort:  "n/a (irreducible)",
			rows:    len(prose),
			samples: workflowSamples(prose),
		})
	}
	return out
}

// commonDocsDir returns the longest common directory prefix of the
// claims' source paths — a reasonable docs_dir to seed the adapter config.
func commonDocsDir(claims []*docclaimpb.DocClaim) string {
	var dirs []string
	for _, dc := range claims {
		if p := dc.GetSourcePath(); p != "" {
			dirs = append(dirs, path.Dir(p))
		}
	}
	if len(dirs) == 0 {
		return "."
	}
	common := strings.Split(dirs[0], "/")
	for _, d := range dirs[1:] {
		parts := strings.Split(d, "/")
		n := 0
		for n < len(common) && n < len(parts) && common[n] == parts[n] {
			n++
		}
		common = common[:n]
	}
	if len(common) == 0 {
		return "."
	}
	return strings.Join(common, "/")
}

// dominantBinaryName returns the most common leading command token across
// the cli-grammar claims' excerpts (raw_text holds the first cited line,
// a command-ish invocation). Falls back to "<binary>" when undetectable.
func dominantBinaryName(claims []*docclaimpb.DocClaim) string {
	counts := map[string]int{}
	for _, dc := range claims {
		t := strings.TrimSpace(dc.GetRawText())
		t = strings.TrimPrefix(t, "$ ")
		t = strings.TrimPrefix(t, "> ")
		fields := strings.Fields(strings.TrimSpace(t))
		if len(fields) == 0 {
			continue
		}
		tok := fields[0]
		if base := tok[strings.LastIndex(tok, "/")+1:]; base != "" {
			counts[base]++
		}
	}
	return dominantKey(counts, "<binary>")
}

// dominantIDLPrefix returns the most common leading package segment of a
// dotted FQDN across the yaml-grammar claims' excerpts. Falls back to
// "<prefix>" when undetectable.
func dominantIDLPrefix(claims []*docclaimpb.DocClaim) string {
	counts := map[string]int{}
	for _, dc := range claims {
		if pfx := firstFQDNPrefix(dc.GetRawText()); pfx != "" {
			counts[pfx]++
		}
	}
	return dominantKey(counts, "<prefix>")
}

// firstFQDNPrefix returns the leading package segment of the first dotted
// identifier on the line (e.g. "envoy.config.cluster.v3.Cluster" → "envoy").
func firstFQDNPrefix(line string) string {
	i := 0
	for i < len(line) {
		if !isIdentAlpha(line[i]) {
			i++
			continue
		}
		start := i
		dots := 0
		for i < len(line) {
			if isIdentByte(line[i]) {
				i++
			} else if line[i] == '.' && i+1 < len(line) && isIdentAlpha(line[i+1]) {
				dots++
				i++
			} else {
				break
			}
		}
		if dots >= 1 {
			tok := line[start:i]
			if j := strings.Index(tok, "."); j > 0 {
				return tok[:j]
			}
		}
	}
	return ""
}

func isIdentAlpha(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func dominantKey(counts map[string]int, fallback string) string {
	best := ""
	bestN := 0
	for k, n := range counts {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

// workflowSamples renders up to a few "path:line  A → B → C" example
// lines, capturing the ordered composition each workflow pins.
func workflowSamples(claims []*docclaimpb.DocClaim) []string {
	var ss []string
	for _, dc := range claims {
		loc := fmt.Sprintf("%s:%d", dc.GetSourcePath(), dc.GetLocation().GetLine())
		ss = append(ss, fmt.Sprintf("%s  %s", loc, strings.Join(localChain(dc.GetContractRefs()), " → ")))
	}
	sort.Strings(ss)
	return capStrings(ss, 6)
}

// localChain shortens element IDs to their local names for a compact
// sample line.
func localChain(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, localName(id))
	}
	return out
}
