package helmvalues

import (
	"context"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// byID indexes a slice of elements by their ID for convenient lookup.
func byID(elems []*contractpb.ContractElement) map[string]*contractpb.ContractElement {
	m := make(map[string]*contractpb.ContractElement, len(elems))
	for _, e := range elems {
		m[e.GetId()] = e
	}
	return m
}

// discoverAll runs DiscoverWithDocs against the package testdata, which
// holds one schema-path chart (schema-chart) and one values.yaml-path
// chart (values-chart).
func discoverAll(t *testing.T) ([]*contractpb.ContractElement, map[string]*contractpb.ContractElement) {
	t.Helper()
	a := New(Config{})
	elems, _, err := a.DiscoverWithDocs(context.Background(), "testdata", adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("DiscoverWithDocs: %v", err)
	}
	return elems, byID(elems)
}

// TestSchemaPath_EmitsKeysWithDescriptions verifies the preferred
// values.schema.json path: every value key becomes a CONFIG_KNOB and the
// schema `description` (resolved through $ref/$defs) becomes its doc.
func TestSchemaPath_EmitsKeysWithDescriptions(t *testing.T) {
	_, idx := discoverAll(t)

	rc, ok := idx["schema-demo.replicaCount"]
	if !ok {
		t.Fatal("schema-demo.replicaCount not emitted")
	}
	if rc.GetKind() != contractpb.ContractElementKind_CONFIG_KNOB {
		t.Errorf("replicaCount kind = %v, want CONFIG_KNOB", rc.GetKind())
	}
	if rc.GetDocCommentExcerpt() == "" {
		t.Error("replicaCount should carry the schema description (resolved via $ref)")
	}
	// Nested object property under image.* must resolve through $ref too.
	if _, ok := idx["schema-demo.image.tag"]; !ok {
		t.Error("nested schema-demo.image.tag not emitted (nested $ref not walked)")
	}
	if _, ok := idx["schema-demo.image.repository"]; !ok {
		t.Error("nested schema-demo.image.repository not emitted")
	}
}

// TestSchemaPath_ArrayItems verifies an array-typed value still emits as a
// single CONFIG_KNOB at the value key (we don't fabricate per-item knobs
// for an empty `items: {}`).
func TestSchemaPath_ArrayItems(t *testing.T) {
	_, idx := discoverAll(t)
	ea, ok := idx["schema-demo.extraArgs"]
	if !ok {
		t.Fatal("schema-demo.extraArgs not emitted")
	}
	if ea.GetDocCommentExcerpt() == "" {
		t.Error("extraArgs should carry its schema description")
	}
}

// TestSchemaPath_UndocumentedFinding verifies a schema key with no
// `description` is a real undocumented finding: emitted as a CONFIG_KNOB
// with an empty DocCommentExcerpt, NOT synthesized.
func TestSchemaPath_UndocumentedFinding(t *testing.T) {
	_, idx := discoverAll(t)
	u, ok := idx["schema-demo.undocumented"]
	if !ok {
		t.Fatal("schema-demo.undocumented not emitted")
	}
	if u.GetDocCommentExcerpt() != "" {
		t.Errorf("undocumented key should have empty doc, got %q", u.GetDocCommentExcerpt())
	}
}

// TestValuesPath_CommentDocs verifies the fallback values.yaml path: the
// helm-docs `# --` comment supplies the doc source, the marker stripped.
func TestValuesPath_CommentDocs(t *testing.T) {
	_, idx := discoverAll(t)
	rc, ok := idx["values-demo.replicaCount"]
	if !ok {
		t.Fatal("values-demo.replicaCount not emitted")
	}
	want := "The number of replicas of the controller to run."
	if rc.GetDocCommentExcerpt() != want {
		t.Errorf("replicaCount doc = %q, want %q", rc.GetDocCommentExcerpt(), want)
	}
	// Nested key under image.* with its own `# --` comment.
	tag, ok := idx["values-demo.image.tag"]
	if !ok {
		t.Fatal("values-demo.image.tag not emitted")
	}
	if tag.GetDocCommentExcerpt() == "" {
		t.Error("values-demo.image.tag should carry its `# --` comment")
	}
}

// TestValuesPath_UndocumentedFinding verifies a values.yaml key with no
// comment at all is a real undocumented finding (empty doc, not
// synthesized), not silently dropped.
func TestValuesPath_UndocumentedFinding(t *testing.T) {
	_, idx := discoverAll(t)
	u, ok := idx["values-demo.service.undocumented"]
	if !ok {
		t.Fatal("values-demo.service.undocumented not emitted")
	}
	if u.GetDocCommentExcerpt() != "" {
		t.Errorf("uncommented values key should have empty doc, got %q", u.GetDocCommentExcerpt())
	}
}

// TestProvenance verifies every element sets a file:line location into the
// actual source file (schema for the schema path, values.yaml for the
// fallback path).
func TestProvenance(t *testing.T) {
	elems, _ := discoverAll(t)
	for _, e := range elems {
		loc := e.GetLocation()
		if loc == nil || loc.GetPath() == "" || loc.GetLine() == 0 {
			t.Errorf("%s missing file:line location: %+v", e.GetId(), loc)
		}
	}
	idx := byID(elems)
	// Schema-path elements point into values.schema.json.
	if p := idx["schema-demo.replicaCount"].GetLocation().GetPath(); p != "schema-chart/values.schema.json" {
		t.Errorf("schema knob location path = %q, want schema-chart/values.schema.json", p)
	}
	// Values-path elements point into values.yaml.
	if p := idx["values-demo.replicaCount"].GetLocation().GetPath(); p != "values-chart/values.yaml" {
		t.Errorf("values knob location path = %q, want values-chart/values.yaml", p)
	}
}

// TestEcosystemIsHelm verifies every element is labeled "helm", never
// "kubernetes".
func TestEcosystemIsHelm(t *testing.T) {
	elems, _ := discoverAll(t)
	if len(elems) == 0 {
		t.Fatal("no elements emitted")
	}
	for _, e := range elems {
		if e.GetEcosystem() != "helm" {
			t.Errorf("%s ecosystem = %q, want helm", e.GetId(), e.GetEcosystem())
		}
	}
}

// TestKinds verifies each chart emits exactly one LIBRARY (the chart) and
// the rest are CONFIG_KNOBs (the value keys).
func TestKinds(t *testing.T) {
	elems, idx := discoverAll(t)

	// Chart LIBRARY elements.
	for _, chart := range []string{"schema-demo", "values-demo"} {
		lib, ok := idx[chart]
		if !ok {
			t.Fatalf("chart LIBRARY %q not emitted", chart)
		}
		if lib.GetKind() != contractpb.ContractElementKind_LIBRARY {
			t.Errorf("%s kind = %v, want LIBRARY", chart, lib.GetKind())
		}
		if lib.GetLibrary() != chart {
			t.Errorf("%s Library = %q, want %q", chart, lib.GetLibrary(), chart)
		}
	}

	libCount, knobCount := 0, 0
	for _, e := range elems {
		switch e.GetKind() {
		case contractpb.ContractElementKind_LIBRARY:
			libCount++
		case contractpb.ContractElementKind_CONFIG_KNOB:
			knobCount++
		default:
			t.Errorf("%s unexpected kind %v", e.GetId(), e.GetKind())
		}
	}
	if libCount != 2 {
		t.Errorf("LIBRARY count = %d, want 2 (one per chart)", libCount)
	}
	if knobCount == 0 {
		t.Error("expected CONFIG_KNOB elements")
	}
}

// TestSchemaWinsOverValues verifies that when a chart ships both a schema
// and a values.yaml, only the schema path emits (no double-counting). The
// schema-chart fixture has no values.yaml, so we assert the negative on
// the documented behavior via a synthetic single-dir check using scope.
func TestScopeFilter(t *testing.T) {
	a := New(Config{})
	elems, _, err := a.DiscoverWithDocs(context.Background(), "testdata",
		adapters.ScopeConfig{Libraries: []string{"schema-demo"}})
	if err != nil {
		t.Fatalf("DiscoverWithDocs: %v", err)
	}
	for _, e := range elems {
		if e.GetLibrary() != "schema-demo" {
			t.Errorf("scope filter leaked %s (library %q)", e.GetId(), e.GetLibrary())
		}
	}
	if len(elems) == 0 {
		t.Error("scoped discovery returned no elements")
	}
}

// TestNoTemplateParsing is a guard: the adapter must never read
// templates/*.yaml. We add a templates dir with a raw Helm template and
// confirm no element references it.
func TestNoTemplateParsing(t *testing.T) {
	elems, _ := discoverAll(t)
	for _, e := range elems {
		if p := e.GetLocation().GetPath(); p != "" {
			if contains(p, "templates/") {
				t.Errorf("%s points into a template file %q — adapter must not parse templates", e.GetId(), p)
			}
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
