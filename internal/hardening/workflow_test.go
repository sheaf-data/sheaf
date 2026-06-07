package hardening

import (
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

func wfClaim(path, transform, rawText string, refs ...string) *docclaimpb.DocClaim {
	return &docclaimpb.DocClaim{
		SourcePath:   path,
		Location:     &commonpb.SourceLocation{Path: path, Line: 1},
		RawText:      rawText,
		ContractRefs: refs,
		Kind:         docclaimpb.DocClaimKind_WORKFLOW,
		Adapter:      "workflowextract",
		Provenance:   &commonpb.RowProvenance{Tier: commonpb.RowProvenance_LLM, Source: "fake", Transform: transform},
	}
}

// TestWorkflowEntries_AllClassesRanked covers (d): with all three
// grammars present, sheaf-hardening.md gains exactly one entry per class
// with the right rung, leverage count, and derived adapter config.
func TestWorkflowEntries_AllClassesRanked(t *testing.T) {
	c := corpus.New()
	c.AddDocClaim(wfClaim("docs/cli/a.md", "workflow-cli-grammar", "sheaf apply --config c.yaml", "sheaf/apply", "sheaf/verify"))
	c.AddDocClaim(wfClaim("docs/cli/b.md", "workflow-cli-grammar", "sheaf deploy --wait", "sheaf/deploy", "sheaf/status"))
	c.AddDocClaim(wfClaim("docs/api/x.rst", "workflow-yaml-grammar", "name: envoy.config.listener.v3.Listener", "envoy.config.listener.v3/Listener", "envoy.config.cluster.v3/Cluster"))
	c.AddDocClaim(wfClaim("docs/guide/y.md", "workflow-prose", "First call Open then Close", "lib/Open", "lib/Close"))

	md := Generate(Input{ProjectID: "demo", Corpus: c})

	// CLI grammar: rung 2, leverage 2, derived binary + docs_dir.
	mustContain(t, md, "Wire the `workflows` adapter")
	mustContain(t, md, "- **Rung:** 2 (cheap deterministic pass)")
	mustContain(t, md, `binary_name="sheaf"`)
	mustContain(t, md, `docs_dir="docs/cli"`)
	mustContain(t, md, "- **Impact (rows pinned):** 2")

	// YAML grammar: rung 1, leverage 1, derived idl_prefix.
	mustContain(t, md, "Wire the `yaml-workflows` adapter")
	mustContain(t, md, "- **Rung:** 1 (schema available)")
	mustContain(t, md, `idl_prefix="envoy"`)

	// Prose: rung 4, leverage 1, no deterministic replacement.
	mustContain(t, md, "Workflow composition in free prose")
	mustContain(t, md, "- **Rung:** 4 (irreducible tail)")

	// Ranking: the highest-leverage entry (cli, 2) precedes the rung-1
	// yaml entry (1), which precedes the rung-4 prose entry (1).
	cli := strings.Index(md, "Wire the `workflows` adapter")
	yaml := strings.Index(md, "Wire the `yaml-workflows` adapter")
	prose := strings.Index(md, "Workflow composition in free prose")
	if !(cli < yaml && yaml < prose) {
		t.Errorf("entry order cli=%d yaml=%d prose=%d, want cli<yaml<prose", cli, yaml, prose)
	}
}

// TestWorkflowEntries_AbsentClassesEmitNothing: an entry only falls out
// of real signal. With only prose workflows, neither the cli nor the yaml
// entry is emitted.
func TestWorkflowEntries_AbsentClassesEmitNothing(t *testing.T) {
	c := corpus.New()
	c.AddDocClaim(wfClaim("docs/guide/y.md", "workflow-prose", "First call Open then Close", "lib/Open", "lib/Close"))

	md := Generate(Input{ProjectID: "demo", Corpus: c})

	mustContain(t, md, "Workflow composition in free prose")
	if strings.Contains(md, "Wire the `workflows` adapter") {
		t.Error("cli-grammar entry emitted with no cli-grammar workflows")
	}
	if strings.Contains(md, "Wire the `yaml-workflows` adapter") {
		t.Error("yaml-grammar entry emitted with no yaml-grammar workflows")
	}
}

// TestWorkflowEntries_DeterministicWorkflowsIgnored: only LLM-tier
// WORKFLOW claims drive the hardening backlog. A deterministic workflow
// claim must not produce an entry.
func TestWorkflowEntries_DeterministicWorkflowsIgnored(t *testing.T) {
	c := corpus.New()
	det := wfClaim("docs/cli/a.md", "", "sheaf apply", "sheaf/apply", "sheaf/verify")
	det.Provenance = &commonpb.RowProvenance{Tier: commonpb.RowProvenance_DETERMINISTIC, Source: "workflows"}
	c.AddDocClaim(det)

	if got := workflowEntries(c); len(got) != 0 {
		t.Errorf("workflowEntries = %d, want 0 (deterministic claims are not hardening signal)", len(got))
	}
}

func mustContain(t *testing.T, hay, needle string) {
	t.Helper()
	if !strings.Contains(hay, needle) {
		t.Errorf("rendered hardening doc missing %q", needle)
	}
}
