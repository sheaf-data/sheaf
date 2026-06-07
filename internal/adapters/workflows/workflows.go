// Package workflows implements a rendered-reference adapter that
// turns multi-step recipe / tutorial / task markdown files into
// WORKFLOW DocClaims — the inter-element layer the per-element
// coverage adapters (markdowncli, fidldoc, clidoc) can't see.
//
// Per-element coverage answers "is this flag documented in
// isolation?" Workflow coverage answers "is this flag's *use*
// documented — does it appear in a sequence alongside the commands
// it composes with?" Agents that have seen a flag in isolation
// can still combine commands incorrectly; workflow claims surface
// that gap directly.
//
// The adapter:
//
//  1. Walks docs_dir for markdown files (include/exclude globbed).
//  2. For each file, extracts every `<binary_name> <subcmd>…` line
//     from inside ``` fenced code blocks, in document order.
//  3. Dedups consecutive identical invocations (a fence with three
//     `kubectl get pods` lines is still one mention of "kubectl get
//     pods" for workflow purposes).
//  4. Emits one WORKFLOW DocClaim per file whose distinct-command
//     count meets min_elements (default 2). Single-command files
//     are EXAMPLE territory and skipped here.
//
// Resolution of the matched command tokens against the actual
// contract surface (so "kubectl apply" maps to the element
// `kubectl apply` not the element `kubectl apply edit-last-applied`)
// happens by greedy longest-prefix match in the indexer's join
// pass; this adapter emits the raw command path it saw in the fence.
package workflows

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/climatch"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "workflows"
const Version = "0.1.0"

type Adapter struct {
	docsDir     string
	include     []string
	exclude     []string
	binaryName  string
	minElements int
	urlBase     string
}

type Config struct {
	DocsDir     string
	Include     []string
	Exclude     []string
	BinaryName  string
	MinElements int
	// URLBase prepends a stable canonical URL to each emitted workflow's
	// SourceLocation. For kubectl's tutorial corpus (sourced from the
	// kubernetes/website checkout) this is "https://kubernetes.io/docs/".
	// Empty disables URL emission.
	URLBase string
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.md"}
	}
	min := cfg.MinElements
	if min < 2 {
		min = 2
	}
	return &Adapter{
		docsDir:     cfg.DocsDir,
		include:     include,
		exclude:     cfg.Exclude,
		binaryName:  cfg.BinaryName,
		minElements: min,
		urlBase:     strings.TrimRight(cfg.URLBase, "/"),
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Parse implements adapters.RenderedReferenceParser. Walks docs_dir
// and emits one WORKFLOW DocClaim per qualifying file.
func (a *Adapter) Parse(ctx context.Context) ([]*docclaimpb.DocClaim, error) {
	if a.docsDir == "" {
		return nil, fmt.Errorf("workflows: docs_dir is empty")
	}
	if a.binaryName == "" {
		return nil, fmt.Errorf("workflows: binary_name is empty")
	}
	var out []*docclaimpb.DocClaim
	err := adapters.WalkMatching(a.docsDir, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := filepath.Join(a.docsDir, rel)
		body, err := os.ReadFile(abs)
		if err != nil {
			return fmt.Errorf("workflows: read %s: %w", abs, err)
		}
		refs := extractWorkflow(body, a.binaryName)
		distinct := uniqueOrder(refs)
		if len(distinct) < a.minElements {
			return nil
		}
		claim := &docclaimpb.DocClaim{
			SourcePath:   rel,
			Location:     &commonpb.SourceLocation{Path: rel, Line: 1},
			RawText:      truncate(workflowSummary(refs), 300),
			ContractRefs: refs, // preserve ordering, including duplicates
			Kind:         docclaimpb.DocClaimKind_WORKFLOW,
			Adapter:      Name,
		}
		if a.urlBase != "" {
			claim.Url = a.urlBase + "/" + slugForCanonicalURL(rel)
		}
		out = append(out, claim)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Stable order so snapshots diff cleanly.
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetSourcePath() < out[j].GetSourcePath() })
	return out, nil
}

// extractWorkflow walks the markdown body, finds every `<binary>…`
// invocation inside a ``` fence (in document order), and returns
// the ordered list of "<binary> <subcommand-path>" strings (one per
// invocation; consecutive duplicates are NOT collapsed here so the
// caller can reason about ordering with multiplicity).
func extractWorkflow(body []byte, binary string) []string {
	var refs []string
	matches := codeFenceRx.FindAllSubmatchIndex(body, -1)
	for _, m := range matches {
		inner := string(body[m[2]:m[3]])
		for _, line := range strings.Split(inner, "\n") {
			deepest := climatch.InvocationRef(line, binary)
			if deepest == "" {
				continue
			}
			// Emit every 2+-deep prefix so the indexer's direct-ref
			// match picks up the longest existing element. For
			// "kubectl get pod" (which is not a real element), we
			// also emit "kubectl get" — which IS an element — so
			// the workflow attribution lands somewhere rather than
			// silently dropping. Inflates the claim count modestly;
			// the workflow length aggregator dedups by element ID
			// after the join, so this doesn't double-count.
			parts := strings.Split(deepest, " ")
			for d := 2; d <= len(parts); d++ {
				refs = append(refs, strings.Join(parts[:d], " "))
			}
			// Also emit per-flag refs for every `--long-flag` mention
			// on this same line, attributed to the LONGEST captured
			// command path. This makes the "flags in ≥1 workflow"
			// metric meaningful — without it the workflow surface
			// only attributes to commands and the masthead's
			// flag-tier workflow row is permanently 0%.
			for _, fm := range longFlagRx.FindAllSubmatch([]byte(line), -1) {
				flag := string(fm[1])
				refs = append(refs, deepest+" --"+flag)
			}
		}
	}
	return refs
}

// longFlagRx matches `--long-flag` mentions on a single line.
// Short flags (`-f`) aren't mapped because the workflows adapter
// doesn't carry the cobra adapter's shorthand → long-name table;
// kubectl recipes lean heavily on long-form anyway.
var longFlagRx = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)

// uniqueOrder returns the input in original order with consecutive
// duplicates removed and exact repeats kept only once. Used to count
// "distinct commands in this workflow" without losing the ordering
// the WORKFLOW claim's ContractRefs preserves.
func uniqueOrder(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func workflowSummary(refs []string) string {
	distinct := uniqueOrder(refs)
	switch {
	case len(distinct) == 0:
		return ""
	case len(distinct) <= 4:
		return strings.Join(distinct, " → ")
	}
	return strings.Join(distinct[:3], " → ") + " → … (" + fmt.Sprintf("%d steps", len(distinct)) + ")"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// slugForCanonicalURL turns a workflow's repo-relative markdown path
// into the slug that follows url_base. Mirrors Hugo's section
// convention used by kubernetes/website:
//   - strip the `.md` extension
//   - drop a trailing `/_index` (Hugo's section landing page)
//   - leave directories intact (tasks/access-application-cluster/access-cluster)
//
// Example: "tutorials/configuration/configure-redis-using-configmap.md"
// → "tutorials/configuration/configure-redis-using-configmap"
// Example: "tasks/administer-cluster/kubeadm/_index.md"
// → "tasks/administer-cluster/kubeadm"
func slugForCanonicalURL(rel string) string {
	s := strings.TrimSuffix(rel, ".md")
	s = strings.TrimSuffix(s, "/_index")
	return s
}

var (
	// Single-capture regex: m[2]:m[3] is the fence body. Earlier
	// versions captured the language tag too and inverted the index
	// arithmetic; this form keeps the consumer unambiguous. `[^\n]*`
	// after the language token consumes the CommonMark info string
	// (e.g. Fuchsia devsite's ```none {:.devsite-disable-click-to-copy})
	// so info-string fences still match.
	codeFenceRx = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*[^\n]*\n(.*?)```")
)
