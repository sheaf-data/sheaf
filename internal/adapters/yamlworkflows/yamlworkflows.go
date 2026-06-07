// Package yamlworkflows implements a rendered-reference adapter that
// turns rST `.. code-block:: yaml` directives into WORKFLOW DocClaims
// — the inter-element layer that the per-element coverage adapters
// can't see, in the YAML/declarative shape (sibling to the CLI-shaped
// workflows adapter under internal/adapters/workflows).
//
// Per-element coverage answers "is this proto message documented in
// isolation?" Workflow coverage answers "is this message's *use*
// documented — does it appear in a composition alongside the other
// messages it routes traffic to / through?" envoy is the load-bearing
// case: a Listener with no filter chain is useless; a Cluster with
// no Listener never gets traffic. Examples that wire 2+ TYPEs
// together are the surface the agent reader needs.
//
// The adapter:
//
//  1. Walks docs_dir for rST files (include/exclude globbed).
//  2. For each file, extracts each `.. code-block:: yaml` (and
//     aliases `.. sourcecode:: yaml`, `.. code:: yaml`) directive's
//     body, indent-aware.
//  3. Runs protomatch over each block body to find dotted FQDNs of
//     the form `<pkg>.<Type>` — the same matcher the rst doc-parser
//     uses for in-line literal references.
//  4. Optionally filters refs to a project's idl_prefix (so envoy
//     reports don't catch `google.protobuf.*` or `xds.*` references
//     that happen to live in the same YAML).
//  5. Emits one WORKFLOW DocClaim per block with >= min_elements
//     distinct contract refs, ordered by appearance. The
//     SourcePath includes a line anchor so multiple blocks in one
//     file render as distinct workflows.
//
// The WORKFLOW kind routes through the indexer into
// `prof.Docs.Reference.byAdapter.yaml-workflows.refs[]`; the
// scanner's masthead workflow-coverage block aggregates from there
// (one workflow per distinct source path, length = distinct
// elements named).
package yamlworkflows

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/protomatch"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "yaml-workflows"
const Version = "0.1.0"

type Adapter struct {
	docsDir     string
	include     []string
	exclude     []string
	idlPrefix   string
	minElements int
	urlBase     string
}

type Config struct {
	// DocsDir is the root the adapter walks (absolute, or
	// orchestrator-resolved relative to --repo).
	DocsDir string
	// Include/Exclude — globs evaluated against repo-relative paths
	// under DocsDir. Default Include: **/*.rst.
	Include []string
	Exclude []string
	// IDLPrefix filters extracted FQDN refs to those whose package
	// starts with this string. For envoy, "envoy" keeps
	// envoy.config.cluster.v3.Cluster and drops
	// google.protobuf.Any. Empty disables the filter.
	IDLPrefix string
	// MinElements — a block needs at least this many distinct
	// elements to count as a workflow. Default 2 (single-element
	// blocks are EXAMPLE territory).
	MinElements int
	// URLBase prepended to the rel path to produce a canonical URL.
	// Empty disables URL emission.
	URLBase string
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.rst"}
	}
	min := cfg.MinElements
	if min < 2 {
		min = 2
	}
	return &Adapter{
		docsDir:     cfg.DocsDir,
		include:     include,
		exclude:     cfg.Exclude,
		idlPrefix:   cfg.IDLPrefix,
		minElements: min,
		urlBase:     strings.TrimRight(cfg.URLBase, "/"),
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Parse implements adapters.RenderedReferenceParser. Walks docs_dir
// and emits one WORKFLOW DocClaim per qualifying yaml code-block.
func (a *Adapter) Parse(ctx context.Context) ([]*docclaimpb.DocClaim, error) {
	if a.docsDir == "" {
		return nil, fmt.Errorf("yaml-workflows: docs_dir is empty")
	}
	var out []*docclaimpb.DocClaim
	err := adapters.WalkMatching(a.docsDir, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(a.docsDir, rel)
		if err != nil {
			return fmt.Errorf("yaml-workflows: read %s: %w", rel, err)
		}
		for _, blk := range findYamlCodeBlocks(body) {
			refs := protomatch.Extract(blk.content)
			refs = keepSlashForm(refs)
			refs = filterByPrefix(refs, a.idlPrefix)
			// protomatch returns alphabetically-sorted refs; re-order
			// by first appearance in the block so the WORKFLOW claim's
			// ContractRefs list is the actual ingredient sequence.
			refs = orderByFirstAppearance(refs, blk.content)
			refs = dedupePreserveOrder(refs)
			if len(refs) < a.minElements {
				continue
			}
			// Per-block SourcePath so multiple blocks in one file render
			// as distinct workflows in the scanner's aggregation.
			sourcePath := fmt.Sprintf("%s#L%d", rel, blk.line)
			claim := &docclaimpb.DocClaim{
				SourcePath:   sourcePath,
				Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(blk.line)},
				RawText:      truncate(blk.content, 300),
				WordCount:    uint32(countWords(blk.content)),
				Kind:         docclaimpb.DocClaimKind_WORKFLOW,
				Adapter:      Name,
				ContractRefs: refs,
			}
			if a.urlBase != "" {
				claim.Url = a.urlBase + "/" + rel
			}
			out = append(out, claim)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetSourcePath() < out[j].GetSourcePath() })
	return out, nil
}

// directiveRx matches a rST yaml-tagged code-block directive opener.
// Captures the leading indent (1) and directive name (2). Accepts
// the stdlib aliases `code-block` / `sourcecode` / `code` plus a few
// project-custom variants (envoy's `validated-code-block`, the
// frequently-used `literalinclude`/`include` pairs) so widely-used
// Sphinx extensions don't silently swallow the most composed
// examples.
var directiveRx = regexp.MustCompile(`^(\s*)\.\. (code-block|sourcecode|code|validated-code-block)::\s+yaml\s*$`)

// optionRx matches a Sphinx directive option line like
// `:caption: ...` indented past the directive itself.
var optionRx = regexp.MustCompile(`^\s+:[A-Za-z][A-Za-z0-9_-]*:`)

type yamlBlock struct {
	line    int    // 1-based line number of the directive
	content string // body text (lines preserved, but trimmed of trailing blanks)
}

// findYamlCodeBlocks scans body for `.. code-block:: yaml` (and
// aliases) directives and returns each block's body + the directive's
// 1-based line number. The body is the contiguous run of lines indented
// strictly past the directive's own leading indent, with intermediate
// blanks tolerated and trailing blanks trimmed. Sphinx option lines
// (`:caption:`, `:lineno-start:`, etc.) that follow the directive are
// skipped before the body proper begins.
func findYamlCodeBlocks(body []byte) []yamlBlock {
	lines := strings.Split(string(body), "\n")
	var blocks []yamlBlock
	for i := 0; i < len(lines); i++ {
		m := directiveRx.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		dirIndent := len(m[1])
		// Skip option lines + the blank line that conventionally
		// separates them from the body.
		j := i + 1
		for j < len(lines) {
			ln := lines[j]
			if strings.TrimSpace(ln) == "" {
				j++
				continue
			}
			if optionRx.MatchString(ln) && indentOf(ln) > dirIndent {
				j++
				continue
			}
			break
		}
		bodyStart := j
		bodyEnd := bodyStart
		for bodyEnd < len(lines) {
			ln := lines[bodyEnd]
			if strings.TrimSpace(ln) == "" {
				bodyEnd++
				continue
			}
			if indentOf(ln) <= dirIndent {
				break
			}
			bodyEnd++
		}
		for bodyEnd > bodyStart && strings.TrimSpace(lines[bodyEnd-1]) == "" {
			bodyEnd--
		}
		if bodyEnd <= bodyStart {
			continue
		}
		content := strings.Join(lines[bodyStart:bodyEnd], "\n")
		blocks = append(blocks, yamlBlock{line: i + 1, content: content})
		i = bodyEnd - 1
	}
	return blocks
}

func indentOf(s string) int {
	n := 0
	for _, c := range s {
		if c == ' ' {
			n++
		} else if c == '\t' {
			// rST tabs expand to the next multiple of 8 per the spec;
			// for our purposes (relative indent comparison) treating
			// a tab as 8 spaces is accurate enough.
			n += 8 - (n % 8)
		} else {
			break
		}
	}
	return n
}

// keepSlashForm filters protomatch's output to just the slash-form
// IDs (the canonical ContractElement.ID shape). Without this filter
// protomatch's dual emission of slash + dotted forms would double-count
// each ref in dedupePreserveOrder.
func keepSlashForm(refs []string) []string {
	out := refs[:0]
	for _, r := range refs {
		if strings.Contains(r, "/") {
			out = append(out, r)
		}
	}
	return out
}

// filterByPrefix keeps only refs whose package starts with prefix.
// Compared against the package portion (before the first slash); empty
// prefix is a no-op.
func filterByPrefix(refs []string, prefix string) []string {
	if prefix == "" {
		return refs
	}
	out := refs[:0]
	for _, r := range refs {
		slash := strings.IndexByte(r, '/')
		if slash < 0 {
			continue
		}
		pkg := r[:slash]
		if pkg == prefix || strings.HasPrefix(pkg, prefix+".") {
			out = append(out, r)
		}
	}
	return out
}

// orderByFirstAppearance re-sorts refs so they appear in the same
// order the dotted FQDNs first appear in `body`. The protomatch
// package returns alphabetically-sorted output (deliberately, for
// stable diffs in single-element-mention reports); for a WORKFLOW
// claim the order is the ingredient sequence, which is information.
// Refs not found in body (because protomatch can emit slash-form
// without a literal slash-form appearance in body) fall through to
// where their corresponding dotted form appears.
func orderByFirstAppearance(refs []string, body string) []string {
	if len(refs) < 2 {
		return refs
	}
	indexOf := func(r string) int {
		// Slash-form IDs (pkg/Type) match a dotted appearance in body.
		// Convert pkg/Type → pkg.Type for the body search.
		dotted := strings.Replace(r, "/", ".", 1)
		if i := strings.Index(body, dotted); i >= 0 {
			return i
		}
		if i := strings.Index(body, r); i >= 0 {
			return i
		}
		return 1 << 30 // tail
	}
	out := make([]string, len(refs))
	copy(out, refs)
	sort.SliceStable(out, func(i, j int) bool {
		return indexOf(out[i]) < indexOf(out[j])
	})
	return out
}

// dedupePreserveOrder removes consecutive AND non-consecutive
// duplicates while preserving the first-seen order. The ContractRefs
// list emitted on a WORKFLOW DocClaim is the canonical "ingredient
// list" — repeated mentions of the same Type within one yaml block
// don't add information.
func dedupePreserveOrder(refs []string) []string {
	seen := make(map[string]bool, len(refs))
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func countWords(s string) int { return len(strings.Fields(s)) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
