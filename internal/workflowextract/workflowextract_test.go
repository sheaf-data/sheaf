package workflowextract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// routeClient is a fake llm.Client that returns a canned response chosen
// by a marker substring found in the prompt — enough to drive one
// distinct sequence per fixture file in a single corpus. It also
// implements llm.CachedGenerator so the cached-prefix path is exercised.
type routeClient struct {
	routes      []route
	cachedCalls int
}

type route struct{ marker, resp string }

func (c *routeClient) Name() string { return "fake:wfx" }
func (c *routeClient) Generate(_ context.Context, prompt string) (string, error) {
	return c.match(prompt), nil
}
func (c *routeClient) GenerateCached(_ context.Context, prefix, variable string) (string, error) {
	c.cachedCalls++
	return c.match(prefix + variable), nil
}
func (c *routeClient) match(s string) string {
	for _, r := range c.routes {
		if strings.Contains(s, r.marker) {
			return r.resp
		}
	}
	return "[]"
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func elem(id, local string, aliases ...string) *contractpb.ContractElement {
	return &contractpb.ContractElement{
		Id:      id,
		Library: "lib",
		Aliases: aliases,
		Kind:    contractpb.ContractElementKind_METHOD,
	}
}

// seedDoc adds a deterministic prose claim so groupDocsByFile includes
// the fixture file in the scan set.
func seedDoc(c *corpus.Corpus, path string) {
	c.AddDocClaim(&docclaimpb.DocClaim{
		SourcePath: path,
		Location:   &commonpb.SourceLocation{Path: path, Line: 1},
		Kind:       docclaimpb.DocClaimKind_PROSE_MENTION,
	})
}

const (
	cliBody = "# Deploy tutorial\n\nRun the apply step:\n\n```bash\n" +
		"sheaf apply --config c.yaml\nsheaf verify --strict\n```\n"
	yamlBody = "Envoy config\n============\n\n.. code-block:: yaml\n\n" +
		"   listener:\n     name: envoy.config.listener.v3.Listener\n" +
		"   cluster:\n     name: envoy.config.cluster.v3.Cluster\n"
	proseBody = "# Logging guide\n\n" +
		"First call Open to start a session, then call Close when finished.\n"
)

// TestRun_EmitsGatedWorkflowsByGrammar covers (a) and the grammar
// classification: three fixture files (CLI fences / yaml FQDN block /
// free prose) each yield a >=2-element gated WORKFLOW claim with tier=LLM,
// ordered contract_refs, and the right transform tag.
func TestRun_EmitsGatedWorkflowsByGrammar(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "cli.md", cliBody)
	writeFile(t, root, "yaml.rst", yamlBody)
	writeFile(t, root, "prose.md", proseBody)

	c := corpus.New()
	c.AddElement(elem("sheaf/apply", "apply"))
	c.AddElement(elem("sheaf/verify", "verify"))
	c.AddElement(elem("envoy.config.listener.v3/Listener", "Listener"))
	c.AddElement(elem("envoy.config.cluster.v3/Cluster", "Cluster"))
	c.AddElement(elem("lib/Open", "Open"))
	c.AddElement(elem("lib/Close", "Close"))
	seedDoc(c, "cli.md")
	seedDoc(c, "yaml.rst")
	seedDoc(c, "prose.md")

	client := &routeClient{routes: []route{
		{"Deploy tutorial", `[{"steps":[{"element":"sheaf/apply","line":6},{"element":"sheaf/verify","line":7}]}]`},
		{"Envoy config", `[{"steps":[{"element":"envoy.config.listener.v3/Listener","line":7},{"element":"envoy.config.cluster.v3/Cluster","line":9}]}]`},
		{"Logging guide", `[{"steps":[{"element":"lib/Open","line":3},{"element":"lib/Close","line":3}]}]`},
	}}

	st, err := New(Config{}, client).Run(context.Background(), c, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Emitted != 3 {
		t.Fatalf("Emitted = %d, want 3", st.Emitted)
	}
	if st.CLIGrammar != 1 || st.YAMLGrammar != 1 || st.Prose != 1 {
		t.Errorf("grammar counts cli/yaml/prose = %d/%d/%d, want 1/1/1",
			st.CLIGrammar, st.YAMLGrammar, st.Prose)
	}
	if client.cachedCalls != 3 {
		t.Errorf("cachedCalls = %d, want 3 (one per file via the cached-prefix path)", client.cachedCalls)
	}

	want := map[string]struct {
		transform string
		refs      []string
	}{
		"cli.md":   {"workflow-cli-grammar", []string{"sheaf/apply", "sheaf/verify"}},
		"yaml.rst": {"workflow-yaml-grammar", []string{"envoy.config.listener.v3/Listener", "envoy.config.cluster.v3/Cluster"}},
		"prose.md": {"workflow-prose", []string{"lib/Open", "lib/Close"}},
	}
	got := map[string]*docclaimpb.DocClaim{}
	for _, dc := range c.DocClaims() {
		if dc.GetKind() == docclaimpb.DocClaimKind_WORKFLOW {
			got[dc.GetSourcePath()] = dc
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %d WORKFLOW claims, want 3", len(got))
	}
	for path, w := range want {
		dc := got[path]
		if dc == nil {
			t.Errorf("%s: no WORKFLOW claim", path)
			continue
		}
		if dc.GetProvenance().GetTier() != commonpb.RowProvenance_LLM {
			t.Errorf("%s: tier = %v, want LLM", path, dc.GetProvenance().GetTier())
		}
		if dc.GetProvenance().GetSource() != "fake:wfx" {
			t.Errorf("%s: source = %q, want fake:wfx", path, dc.GetProvenance().GetSource())
		}
		if dc.GetProvenance().GetTransform() != w.transform {
			t.Errorf("%s: transform = %q, want %q", path, dc.GetProvenance().GetTransform(), w.transform)
		}
		if strings.Join(dc.GetContractRefs(), ",") != strings.Join(w.refs, ",") {
			t.Errorf("%s: contract_refs = %v, want %v (ordered)", path, dc.GetContractRefs(), w.refs)
		}
	}
}

// TestRun_GateDropsAndSubTwoNotEmitted covers (b): an element with a bad
// citation is dropped, and a sequence that falls below 2 survivors is not
// emitted. A parallel good sequence in the same file still emits.
func TestRun_GateDropsAndSubTwoNotEmitted(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "prose.md", proseBody)

	c := corpus.New()
	c.AddElement(elem("lib/Open", "Open"))
	c.AddElement(elem("lib/Close", "Close"))
	c.AddElement(elem("lib/Ghost", "Ghost"))
	seedDoc(c, "prose.md")

	// Two sequences: a good 2-step (Open@3, Close@3) and a bad one whose
	// second step (Ghost@3) does NOT appear at line 3 → drops to a single
	// survivor → must not be emitted.
	client := &routeClient{routes: []route{
		{"Logging guide", `[
		  {"steps":[{"element":"lib/Open","line":3},{"element":"lib/Close","line":3}]},
		  {"steps":[{"element":"lib/Open","line":3},{"element":"lib/Ghost","line":3}]}
		]`},
	}}

	st, err := New(Config{}, client).Run(context.Background(), c, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Proposed != 2 {
		t.Errorf("Proposed = %d, want 2", st.Proposed)
	}
	if st.Emitted != 1 {
		t.Errorf("Emitted = %d, want 1 (only the good sequence)", st.Emitted)
	}
	if st.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1 (bad sequence fell below 2 survivors)", st.Dropped)
	}
	if st.ElementsDropped != 1 {
		t.Errorf("ElementsDropped = %d, want 1 (Ghost's bad citation)", st.ElementsDropped)
	}
	// Exactly one WORKFLOW claim, and it has no Ghost.
	var n int
	for _, dc := range c.DocClaims() {
		if dc.GetKind() != docclaimpb.DocClaimKind_WORKFLOW {
			continue
		}
		n++
		for _, r := range dc.GetContractRefs() {
			if r == "lib/Ghost" {
				t.Errorf("emitted claim leaked dropped element Ghost: %v", dc.GetContractRefs())
			}
		}
	}
	if n != 1 {
		t.Errorf("WORKFLOW claims = %d, want 1", n)
	}
}
