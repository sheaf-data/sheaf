package rst

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

func setupRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("writefile: %v", err)
		}
	}
	return dir
}

func TestParse_SectionPath_HeadingStack(t *testing.T) {
	// rST page with three heading levels (first-seen order: = → H1,
	// - → H2, ~ → H3) and a flag mention under each.
	body := strings.Join([]string{
		"fd",
		"==",
		"",
		"Top-level mention of ``--hidden``.",
		"",
		"How to use",
		"----------",
		"",
		"Pattern syntax",
		"~~~~~~~~~~~~~~",
		"",
		"Use ``--hidden`` to include hidden files.",
		"",
		"Command-line options",
		"~~~~~~~~~~~~~~~~~~~~",
		"",
		".. code-block:: text",
		"",
		"   -H, --hidden    Search hidden files",
		"",
		"Installation",
		"------------",
		"",
		"Run ``brew install --foo`` to install.",
	}, "\n")
	repo := setupRepo(t, map[string]string{"README.rst": body})
	p := New(Config{Include: []string{"README.rst"}})
	claims, err := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	type wantPath struct {
		flag    string
		section []string
	}
	wants := []wantPath{
		{"--hidden", []string{"fd"}},
		{"--hidden", []string{"fd", "How to use", "Pattern syntax"}},
		{"--hidden", []string{"fd", "How to use", "Command-line options"}},
		{"--foo", []string{"fd", "Installation"}},
	}
	var got []wantPath
	for _, c := range claims {
		if c.GetKind() != docclaimpb.DocClaimKind_PROSE_MENTION {
			continue
		}
		for _, r := range c.GetContractRefs() {
			if strings.HasPrefix(r, "--") {
				got = append(got, wantPath{flag: r, section: append([]string(nil), c.GetSectionPath()...)})
			}
		}
	}
	if len(got) != len(wants) {
		t.Fatalf("expected %d flag claims, got %d: %+v", len(wants), len(got), got)
	}
	for i, w := range wants {
		if got[i].flag != w.flag {
			t.Errorf("claim[%d].flag = %q, want %q", i, got[i].flag, w.flag)
		}
		if !equalStrings(got[i].section, w.section) {
			t.Errorf("claim[%d].section = %v, want %v", i, got[i].section, w.section)
		}
	}
}

func TestParse_OverlinedHeading(t *testing.T) {
	body := strings.Join([]string{
		"=====",
		"Title",
		"=====",
		"",
		"Section",
		"-------",
		"",
		"Mention of ``foo.Bar`` here.",
	}, "\n")
	repo := setupRepo(t, map[string]string{"doc.rst": body})
	p := New(Config{Include: []string{"doc.rst"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	var matched bool
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			if r == "foo.Bar" {
				want := []string{"Title", "Section"}
				if !equalStrings(c.GetSectionPath(), want) {
					t.Errorf("section_path = %v, want %v", c.GetSectionPath(), want)
				}
				matched = true
			}
		}
	}
	if !matched {
		t.Errorf("missing foo.Bar mention")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParse_QualifiedMentions(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"docs/io.rst": "Use ``fuchsia.io/Directory.Open`` to open a directory. " +
			"You can also call ``Node.Sync`` afterwards. " +
			"Avoid ``nope`` — bare identifiers are skipped.",
	})
	p := New(Config{Include: []string{"docs/**/*.rst"}})
	claims, err := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			got[r] = true
		}
	}
	if !got["fuchsia.io/Directory.Open"] {
		t.Errorf("missing fuchsia.io/Directory.Open mention: %v", got)
	}
	if !got["Node.Sync"] {
		t.Errorf("missing Node.Sync mention: %v", got)
	}
	if got["nope"] {
		t.Errorf("bare identifier 'nope' should NOT be surfaced")
	}
}

func TestParse_CodeBlocksOptIn(t *testing.T) {
	body := strings.Join([]string{
		"Here's some Rust code:",
		"",
		".. code-block:: rust",
		"",
		"   let x = open(\"/foo\")?;",
		"   x.read()?;",
		"",
		"And opaque:",
		"",
		".. code-block:: text",
		"",
		"   random text",
	}, "\n")
	repo := setupRepo(t, map[string]string{"docs/x.rst": body})
	// Without opt-in.
	p := New(Config{Include: []string{"docs/**/*.rst"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	for _, c := range claims {
		if c.GetKind() == docclaimpb.DocClaimKind_EXAMPLE {
			t.Errorf("expected no EXAMPLE claims without opt-in; got %v", c)
		}
	}
	// With rust enabled.
	p = New(Config{
		Include:            []string{"docs/**/*.rst"},
		CodeBlockLanguages: []string{"rust"},
	})
	claims, _ = p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	var examples int
	for _, c := range claims {
		if c.GetKind() == docclaimpb.DocClaimKind_EXAMPLE {
			examples++
			if !strings.Contains(c.GetRawText(), "open") {
				t.Errorf("EXAMPLE content missing: %q", c.GetRawText())
			}
		}
	}
	if examples != 1 {
		t.Errorf("expected 1 EXAMPLE, got %d", examples)
	}
}

func TestParse_CodeBlocksDontProduceMentions(t *testing.T) {
	// A bare identifier inside a code block must NOT generate a prose mention.
	body := strings.Join([]string{
		".. code-block:: cpp",
		"",
		"   ``fuchsia.io/Directory.Open(x);``",
	}, "\n")
	repo := setupRepo(t, map[string]string{"docs/x.rst": body})
	p := New(Config{Include: []string{"docs/**/*.rst"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	for _, c := range claims {
		if c.GetKind() == docclaimpb.DocClaimKind_PROSE_MENTION &&
			len(c.GetContractRefs()) > 0 && !strings.HasPrefix(c.GetContractRefs()[0], "--") {
			t.Errorf("identifier inside code block should not produce a prose mention; got %+v", c)
		}
	}
}

func TestParse_SubstanceClassification(t *testing.T) {
	cases := []struct {
		text string
		want commonpb.Substance
	}{
		{"``Proto.M``", commonpb.Substance_SIGNATURE_ONLY},
		{"In short, ``Proto.M`` opens a thing for you somehow.", commonpb.Substance_PARTIAL},
		{strings.Repeat("word ", 30) + " ``Proto.M`` " + strings.Repeat("word ", 30), commonpb.Substance_SUBSTANTIVE},
	}
	for _, c := range cases {
		got := classifySubstance(c.text)
		if got != c.want {
			t.Errorf("classifySubstance(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestParse_DerivesLineNumbers(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"docs/x.rst": "Intro line.\n\nSecond paragraph mentioning ``foo.Bar``.\n\nThird line.",
	})
	p := New(Config{Include: []string{"docs/**/*.rst"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	if len(claims) == 0 {
		t.Fatalf("no claims")
	}
	if claims[0].GetLocation().GetLine() != 3 {
		t.Errorf("line = %d, want 3", claims[0].GetLocation().GetLine())
	}
}

func TestParse_SphinxInlineRole(t *testing.T) {
	// Bare role with qualified content.
	repo := setupRepo(t, map[string]string{
		"docs/io.rst": "See :py:class:`foo.Bar` and :ref:`baz`.",
	})
	p := New(Config{Include: []string{"docs/**/*.rst"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	got := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			got[r] = true
		}
	}
	if !got["foo.Bar"] {
		t.Errorf("missing :py:class: target foo.Bar: %v", got)
	}
	if got["baz"] {
		t.Errorf("bare ref label 'baz' should not be surfaced (looksQualified fails)")
	}
}

func TestParse_SphinxRoleEnvoyLabelDecode(t *testing.T) {
	// envoy-style `:ref:`label <envoy_v3_api_..._slug>`` where the
	// visible label is a short word and the angle-bracket target is
	// a Sphinx anchor slug. The decoder reconstructs the canonical
	// FQDN and protomatch extracts the proto ref from it.
	repo := setupRepo(t, map[string]string{
		"docs/x.rst": "See :ref:`Cluster.metadata <envoy_v3_api_field_config.cluster.v3.Cluster.metadata>` for details.",
	})
	p := New(Config{Include: []string{"docs/**/*.rst"}, IDLPrefix: "envoy"})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	got := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			got[r] = true
		}
	}
	if !got["envoy.config.cluster.v3/Cluster"] {
		t.Errorf("expected decoded ref envoy.config.cluster.v3/Cluster from Sphinx slug: %v", got)
	}
}

func TestParse_SphinxRoleAngleTarget(t *testing.T) {
	// `:ref:`label <target>`` — the target overrides the visible label.
	repo := setupRepo(t, map[string]string{
		"docs/x.rst": "See :repo:`cds.proto <api/envoy/service/cluster/v3/cds.proto>` for details.",
	})
	p := New(Config{Include: []string{"docs/**/*.rst"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	got := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			got[r] = true
		}
	}
	if !got["api/envoy/service/cluster/v3/cds.proto"] {
		t.Errorf("expected target path as ref: %v", got)
	}
	if got["cds.proto <api/envoy/service/cluster/v3/cds.proto>"] {
		t.Errorf("raw role content should not appear as ref; target extraction failed")
	}
}

func TestParse_SphinxRoleProtomatch(t *testing.T) {
	// FQDN inside a role gets picked up by protomatch.
	repo := setupRepo(t, map[string]string{
		"docs/x.rst": "Method :ref:`envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources` is multiplexed.",
	})
	p := New(Config{Include: []string{"docs/**/*.rst"}, IDLPrefix: "envoy"})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	got := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			got[r] = true
		}
	}
	want := "envoy.service.discovery.v3/AggregatedDiscoveryService.StreamAggregatedResources"
	if !got[want] {
		t.Errorf("expected protomatch ref %q; got %v", want, got)
	}
}

func TestParse_SphinxHTTPDirective(t *testing.T) {
	// `.. http:post:: /pkg.Service/Method` — protomatch over the
	// directive argument pulls out the gRPC method ref.
	body := strings.Join([]string{
		"xDS endpoints",
		"=============",
		"",
		".. http:post:: /envoy.service.discovery.v3.AggregatedDiscoveryService/StreamAggregatedResources",
		"",
		"Streaming variant of the ADS endpoint.",
	}, "\n")
	repo := setupRepo(t, map[string]string{"docs/x.rst": body})
	p := New(Config{Include: []string{"docs/**/*.rst"}, IDLPrefix: "envoy"})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	got := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			got[r] = true
		}
	}
	want := "envoy.service.discovery.v3/AggregatedDiscoveryService.StreamAggregatedResources"
	if !got[want] {
		t.Errorf("expected directive arg to surface %q; got %v", want, got)
	}
}

func TestParse_SphinxRoleInsideCodeBlock(t *testing.T) {
	// A role-shaped sequence inside a code block must NOT emit a
	// PROSE_MENTION claim — the body is stripped before the role
	// pass. (protomatch may still emit a REFERENCE-kind claim for
	// the code block as a whole; that's the existing code-block
	// fallback behavior, not a role-pass leak.)
	body := strings.Join([]string{
		".. code-block:: text",
		"",
		"   :ref:`foo.Bar`",
	}, "\n")
	repo := setupRepo(t, map[string]string{"docs/x.rst": body})
	p := New(Config{Include: []string{"docs/**/*.rst"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	for _, c := range claims {
		if c.GetKind() != docclaimpb.DocClaimKind_PROSE_MENTION {
			continue
		}
		for _, r := range c.GetContractRefs() {
			if r == "foo.Bar" {
				t.Errorf("role inside code block should not emit a PROSE_MENTION; got ref %q", r)
			}
		}
	}
}

func TestParse_SphinxDirectiveLineNumber(t *testing.T) {
	body := strings.Join([]string{
		"intro",
		"=====",
		"",
		"",
		".. http:post:: /pkg.S.v1.Svc/M",
	}, "\n")
	repo := setupRepo(t, map[string]string{"docs/x.rst": body})
	p := New(Config{Include: []string{"docs/**/*.rst"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	var seen bool
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			if strings.Contains(r, "Svc") && c.GetLocation().GetLine() == 5 {
				seen = true
			}
		}
	}
	if !seen {
		t.Errorf("expected directive ref on line 5; claims=%+v", claims)
	}
}

func TestParse_SourcecodeDirectiveAlias(t *testing.T) {
	body := strings.Join([]string{
		".. sourcecode:: rust",
		"",
		"   fn main() { open(\"/foo\"); }",
	}, "\n")
	repo := setupRepo(t, map[string]string{"docs/x.rst": body})
	p := New(Config{
		Include:            []string{"docs/**/*.rst"},
		CodeBlockLanguages: []string{"rust"},
	})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	var found bool
	for _, c := range claims {
		if c.GetKind() == docclaimpb.DocClaimKind_EXAMPLE && strings.Contains(c.GetRawText(), "fn main") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected EXAMPLE claim from .. sourcecode:: rust block")
	}
}
