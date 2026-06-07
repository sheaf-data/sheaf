package k8smanifest

import (
	"context"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// runDiscover runs the adapter against the testdata directory, limited to
// the given include globs, and returns elements indexed by ID plus the
// raw slice.
func runDiscover(t *testing.T, include []string, scope adapters.ScopeConfig) (map[string]*contractpb.ContractElement, []*contractpb.ContractElement) {
	t.Helper()
	a := New(Config{Include: include})
	elems, err := a.Discover(context.Background(), "testdata", scope)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	byID := make(map[string]*contractpb.ContractElement, len(elems))
	for _, e := range elems {
		byID[e.GetId()] = e
	}
	return byID, elems
}

// rendered restricts discovery to the multi-doc rendered fixture.
func rendered(t *testing.T, scope adapters.ScopeConfig) (map[string]*contractpb.ContractElement, []*contractpb.ContractElement) {
	return runDiscover(t, []string{"rendered.yaml"}, scope)
}

func TestDiscover_MultiDoc(t *testing.T) {
	byID, all := rendered(t, adapters.ScopeConfig{})
	if len(all) == 0 {
		t.Fatal("expected non-empty element set")
	}

	// Both documents in the multi-doc stream must be parsed: the apps/v1
	// Deployment and the core/v1 Service.
	if _, ok := byID["apps/Deployment"]; !ok {
		t.Error("missing TYPE apps/Deployment (first doc)")
	}
	if _, ok := byID["core/Service"]; !ok {
		t.Error("missing TYPE core/Service (second doc)")
	}
}

func TestDiscover_Kinds(t *testing.T) {
	byID, _ := rendered(t, adapters.ScopeConfig{})

	// LIBRARY per group.
	for _, g := range []string{"apps", "core"} {
		e, ok := byID[g]
		if !ok {
			t.Errorf("missing LIBRARY element %q", g)
			continue
		}
		if e.GetKind() != contractpb.ContractElementKind_LIBRARY {
			t.Errorf("%s: kind = %v, want LIBRARY", g, e.GetKind())
		}
	}

	// TYPE per resource kind.
	dep := byID["apps/Deployment"]
	if dep == nil {
		t.Fatal("missing apps/Deployment")
	}
	if dep.GetKind() != contractpb.ContractElementKind_TYPE {
		t.Errorf("Deployment kind = %v, want TYPE", dep.GetKind())
	}

	// CONFIG_KNOB per field.
	repl := byID["apps/Deployment.spec.replicas"]
	if repl == nil {
		t.Fatal("missing apps/Deployment.spec.replicas")
	}
	if repl.GetKind() != contractpb.ContractElementKind_CONFIG_KNOB {
		t.Errorf("spec.replicas kind = %v, want CONFIG_KNOB", repl.GetKind())
	}
}

func TestDiscover_FieldPaths(t *testing.T) {
	byID, _ := rendered(t, adapters.ScopeConfig{})

	// Nested fields and list-member fields must be emitted with honest
	// dotted/[]-marked paths.
	want := []string{
		"apps/Deployment.spec",
		"apps/Deployment.spec.replicas",
		"apps/Deployment.spec.selector.matchLabels.app",
		"apps/Deployment.spec.template.spec.containers[].name",
		"apps/Deployment.spec.template.spec.containers[].image",
		"apps/Deployment.spec.template.spec.containers[].ports[].containerPort",
		"apps/Deployment.metadata.labels.app",
		"core/Service.spec.type",
		"core/Service.spec.ports[].port",
		"core/Service.spec.ports[].targetPort",
	}
	for _, id := range want {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing expected field path %q", id)
		}
	}

	// Envelope identity keys are NOT emitted as root CONFIG_KNOBs.
	for _, id := range []string{"apps/Deployment.apiVersion", "apps/Deployment.kind"} {
		if _, ok := byID[id]; ok {
			t.Errorf("envelope key %q should not be a CONFIG_KNOB", id)
		}
	}
}

func TestDiscover_Ecosystem(t *testing.T) {
	_, all := rendered(t, adapters.ScopeConfig{})
	if len(all) == 0 {
		t.Fatal("expected elements")
	}
	for _, e := range all {
		if e.GetEcosystem() != "manifest" {
			t.Errorf("%s: ecosystem = %q, want \"manifest\" (never \"kubernetes\")", e.GetId(), e.GetEcosystem())
		}
	}
}

func TestDiscover_Location(t *testing.T) {
	byID, all := rendered(t, adapters.ScopeConfig{})

	// Every element sets a real file:line.
	for _, e := range all {
		loc := e.GetLocation()
		if loc == nil {
			t.Errorf("%s: nil location", e.GetId())
			continue
		}
		if loc.GetPath() != "rendered.yaml" {
			t.Errorf("%s: path = %q, want rendered.yaml", e.GetId(), loc.GetPath())
		}
		if loc.GetLine() == 0 {
			t.Errorf("%s: location line is 0", e.GetId())
		}
	}

	// Spot-check exact lines against the fixture (1-based key lines).
	cases := map[string]uint32{
		"apps":                          6,  // apiVersion: apps/v1
		"apps/Deployment":               7,  // kind: Deployment
		"apps/Deployment.spec":          12, // spec:
		"apps/Deployment.spec.replicas": 13, // replicas: 3
	}
	for id, wantLine := range cases {
		e := byID[id]
		if e == nil {
			t.Errorf("missing %q", id)
			continue
		}
		if got := e.GetLocation().GetLine(); got != wantLine {
			t.Errorf("%s: line = %d, want %d", id, got, wantLine)
		}
	}
}

func TestDiscover_FieldMeta(t *testing.T) {
	byID, _ := rendered(t, adapters.ScopeConfig{})

	repl := byID["apps/Deployment.spec.replicas"]
	if repl == nil {
		t.Fatal("missing spec.replicas")
	}
	if got := repl.GetEcosystemMeta().AsMap()["yamlType"]; got != "int" {
		t.Errorf("spec.replicas yamlType = %v, want int", got)
	}

	img := byID["apps/Deployment.spec.template.spec.containers[].image"]
	if img == nil {
		t.Fatal("missing containers[].image")
	}
	if got := img.GetEcosystemMeta().AsMap()["yamlType"]; got != "string" {
		t.Errorf("containers[].image yamlType = %v, want string", got)
	}
}

// TestDiscover_SkipsRawTemplate guards the single most important rule: a
// document that is a raw, un-rendered Helm template is not valid YAML and
// must be skipped gracefully — no crash, no `{{ .Values.x }}` pseudo-field.
func TestDiscover_SkipsRawTemplate(t *testing.T) {
	_, all := runDiscover(t, []string{"has-template.yaml"}, adapters.ScopeConfig{})

	// The doc fails to parse, so no elements come out of it. Critically,
	// nothing it emits may contain template syntax.
	for _, e := range all {
		if containsTemplate(e.GetId()) {
			t.Errorf("emitted element with template syntax in id: %q", e.GetId())
		}
		if e.GetLocation() != nil && containsTemplate(e.GetLocation().GetPath()) {
			t.Errorf("emitted element with template syntax in path: %q", e.GetLocation().GetPath())
		}
	}
}

func containsTemplate(s string) bool {
	return len(s) >= 2 && (indexOf(s, "{{") >= 0 || indexOf(s, "}}") >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestDiscover_ScopeFilter(t *testing.T) {
	// Scope to apps only: the core/v1 Service must drop out.
	byID, _ := rendered(t, adapters.ScopeConfig{Libraries: []string{"apps"}})
	if _, ok := byID["apps/Deployment"]; !ok {
		t.Error("expected apps/Deployment in scope")
	}
	if _, ok := byID["core/Service"]; ok {
		t.Error("core/Service should be filtered out when scope=apps")
	}

	// An unrelated scope yields nothing.
	_, all := rendered(t, adapters.ScopeConfig{Libraries: []string{"networking.k8s.io"}})
	if len(all) != 0 {
		t.Errorf("expected 0 elements for out-of-scope group, got %d", len(all))
	}
}
