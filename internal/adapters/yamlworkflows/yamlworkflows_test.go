package yamlworkflows

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestParse_EmitsWorkflowPerYamlBlock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "securing.rst", strings.Join([]string{
		"Securing your config",
		"=====================",
		"",
		".. code-block:: yaml",
		"",
		"   resources:",
		"   - \"@type\": type.googleapis.com/envoy.config.listener.v3.Listener",
		"     name: my_listener",
		"   - \"@type\": type.googleapis.com/envoy.config.cluster.v3.Cluster",
		"     name: my_cluster",
		"",
		"Another block:",
		"",
		".. code-block:: yaml",
		"",
		"   resources:",
		"   - \"@type\": type.googleapis.com/envoy.config.route.v3.RouteConfiguration",
		"     name: my_route",
		"",
		"Single-element block (skipped because <minElements):",
		"",
		".. code-block:: yaml",
		"",
		"   \"@type\": type.googleapis.com/envoy.config.cluster.v3.Cluster",
	}, "\n"))

	a := New(Config{DocsDir: dir, IDLPrefix: "envoy"})
	claims, err := a.Parse(context.Background())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// First block has 2 distinct refs (Listener + Cluster) → emit.
	// Second block has 1 ref (RouteConfiguration) → skip.
	// Third block has 1 ref → skip.
	if len(claims) != 1 {
		t.Fatalf("expected 1 WORKFLOW claim, got %d: %+v", len(claims), claims)
	}
	c := claims[0]
	if c.GetKind() != docclaimpb.DocClaimKind_WORKFLOW {
		t.Errorf("kind = %v, want WORKFLOW", c.GetKind())
	}
	gotRefs := c.GetContractRefs()
	wantRefs := []string{
		"envoy.config.listener.v3/Listener",
		"envoy.config.cluster.v3/Cluster",
	}
	if len(gotRefs) != len(wantRefs) {
		t.Fatalf("refs = %v, want %v", gotRefs, wantRefs)
	}
	for i := range wantRefs {
		if gotRefs[i] != wantRefs[i] {
			t.Errorf("refs[%d] = %q, want %q", i, gotRefs[i], wantRefs[i])
		}
	}
	if !strings.HasPrefix(c.GetSourcePath(), "securing.rst#L") {
		t.Errorf("SourcePath = %q, want securing.rst#L<line>", c.GetSourcePath())
	}
}

func TestParse_FiltersByIDLPrefix(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "x.rst", strings.Join([]string{
		".. code-block:: yaml",
		"",
		"   foo: type.googleapis.com/envoy.config.cluster.v3.Cluster",
		"   bar: type.googleapis.com/google.protobuf.Any",
		"   baz: type.googleapis.com/xds.core.v3.Authority",
		"   qux: type.googleapis.com/envoy.config.listener.v3.Listener",
	}, "\n"))
	a := New(Config{DocsDir: dir, IDLPrefix: "envoy"})
	claims, _ := a.Parse(context.Background())
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	for _, r := range claims[0].GetContractRefs() {
		if !strings.HasPrefix(r, "envoy.") {
			t.Errorf("idl_prefix filter let through %q", r)
		}
	}
	if len(claims[0].GetContractRefs()) != 2 {
		t.Errorf("expected 2 envoy refs, got %d (%v)", len(claims[0].GetContractRefs()), claims[0].GetContractRefs())
	}
}

func TestParse_RespectsDirectiveAliases(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "x.rst", strings.Join([]string{
		".. sourcecode:: yaml",
		"",
		"   a: envoy.config.listener.v3.Listener",
		"   b: envoy.config.cluster.v3.Cluster",
	}, "\n"))
	a := New(Config{DocsDir: dir, IDLPrefix: "envoy"})
	claims, _ := a.Parse(context.Background())
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim from sourcecode alias, got %d", len(claims))
	}
}

func TestParse_DedupesRefs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "x.rst", strings.Join([]string{
		".. code-block:: yaml",
		"",
		"   a: envoy.config.cluster.v3.Cluster",
		"   b: envoy.config.cluster.v3.Cluster",
		"   c: envoy.config.cluster.v3.Cluster",
		"   d: envoy.config.listener.v3.Listener",
	}, "\n"))
	a := New(Config{DocsDir: dir, IDLPrefix: "envoy"})
	claims, _ := a.Parse(context.Background())
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	refs := claims[0].GetContractRefs()
	if len(refs) != 2 {
		t.Errorf("expected 2 distinct refs, got %d (%v)", len(refs), refs)
	}
}

func TestParse_PerBlockSourcePath(t *testing.T) {
	// Two qualifying blocks in one file → two distinct WORKFLOW claims.
	dir := t.TempDir()
	writeFile(t, dir, "tutorial.rst", strings.Join([]string{
		".. code-block:: yaml",
		"",
		"   a: envoy.config.listener.v3.Listener",
		"   b: envoy.config.cluster.v3.Cluster",
		"",
		"Second:",
		"",
		".. code-block:: yaml",
		"",
		"   a: envoy.config.cluster.v3.Cluster",
		"   b: envoy.config.route.v3.RouteConfiguration",
	}, "\n"))
	a := New(Config{DocsDir: dir, IDLPrefix: "envoy"})
	claims, _ := a.Parse(context.Background())
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(claims))
	}
	seen := map[string]bool{}
	for _, c := range claims {
		seen[c.GetSourcePath()] = true
	}
	if len(seen) != 2 {
		t.Errorf("expected 2 distinct SourcePaths, got %v", seen)
	}
}
