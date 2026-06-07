package affordance

import (
	"testing"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	"google.golang.org/protobuf/types/known/structpb"
)

// Helper: build a CONFIG_KNOB element with id, type, and optional
// max_size — covers the typical input shape from the CML adapter.
func knob(id, knobType, maxSize string) *contractpb.ContractElement {
	meta := map[string]interface{}{}
	if knobType != "" {
		meta["type"] = knobType
	}
	if maxSize != "" {
		meta["max_size"] = maxSize
	}
	s, _ := structpb.NewStruct(meta)
	return &contractpb.ContractElement{
		Id:            id,
		Kind:          contractpb.ContractElementKind_CONFIG_KNOB,
		Ecosystem:     "cml",
		EcosystemMeta: s,
	}
}

// Two CML knobs in sibling C++/Rust packages with identical name +
// type + max_size are a clean match: bidirectional SAME_AFFORDANCE
// with confidence at the max (0.95) because type and constraint both
// match.
func TestAnnotate_CrossLanguageSamePackage(t *testing.T) {
	a := knob("cml:cpp/config_example/greeting", "string", "512")
	b := knob("cml:rust/config_example/greeting", "string", "512")
	elems := []*contractpb.ContractElement{a, b}

	m := New(Config{})
	stats := m.Annotate(elems)

	if stats.ClustersMatched != 1 {
		t.Fatalf("clusters: want 1, got %d", stats.ClustersMatched)
	}
	if stats.RelationshipsEmitted != 2 {
		t.Fatalf("relationships: want 2 (bidirectional), got %d", stats.RelationshipsEmitted)
	}
	if !relationshipExists(a, contractpb.RelationshipKind_SAME_AFFORDANCE, b.GetId()) {
		t.Errorf("missing a→b SAME_AFFORDANCE")
	}
	if !relationshipExists(b, contractpb.RelationshipKind_SAME_AFFORDANCE, a.GetId()) {
		t.Errorf("missing b→a SAME_AFFORDANCE")
	}
	if rel := findRel(a, b.GetId()); rel != nil {
		// baseline 0.70 + type-match 0.15 + max_size-match 0.05 = 0.90
		want := 0.90
		if rel.GetConfidence() != want {
			t.Errorf("confidence: want %.2f, got %.2f", want, rel.GetConfidence())
		}
	}
}

// Same name + same kind but different type — still a match, but the
// confidence is the baseline 0.70 because the type signal didn't fire.
func TestAnnotate_TypeMismatchLowerConfidence(t *testing.T) {
	a := knob("cml:p1/conn/max_connections", "uint64", "")
	b := knob("cml:p2/conn/max_connections", "string", "")
	elems := []*contractpb.ContractElement{a, b}

	m := New(Config{})
	stats := m.Annotate(elems)

	if stats.RelationshipsEmitted != 2 {
		t.Fatalf("want 2 relationships, got %d", stats.RelationshipsEmitted)
	}
	if rel := findRel(a, b.GetId()); rel != nil {
		if rel.GetConfidence() != 0.70 {
			t.Errorf("baseline-only confidence: want 0.70, got %.2f", rel.GetConfidence())
		}
	}
}

// CONFIG_KNOB only by default — a CONFIG_KNOB and a FLAG sharing a
// name should NOT match because FLAG isn't in the default match-kind
// set. Tests the kind-filter precisely.
func TestAnnotate_KindFilterRejectsCrossKind(t *testing.T) {
	a := knob("cml:p1/svc/greeting", "string", "")
	b := &contractpb.ContractElement{
		Id:        "argh:srv --greeting",
		Kind:      contractpb.ContractElementKind_FLAG,
		Ecosystem: "argh",
	}
	elems := []*contractpb.ContractElement{a, b}

	m := New(Config{})
	stats := m.Annotate(elems)

	if stats.RelationshipsEmitted != 0 {
		t.Fatalf("want 0 relationships across kinds, got %d", stats.RelationshipsEmitted)
	}
}

// Cluster size cap: a very-common name like "verbose" emitted by 12
// different packages should be skipped, with the name surfaced in
// Stats.SkippedClusterNames for observability.
func TestAnnotate_LargeClusterSkipped(t *testing.T) {
	var elems []*contractpb.ContractElement
	for i := 0; i < 12; i++ {
		elems = append(elems, knob("cml:pkg"+string(rune('a'+i))+"/app/verbose", "bool", ""))
	}
	m := New(Config{}) // default MaxClusterSize = 8
	stats := m.Annotate(elems)

	if stats.RelationshipsEmitted != 0 {
		t.Errorf("want 0 relationships for skipped cluster, got %d", stats.RelationshipsEmitted)
	}
	if stats.SkippedTooLarge != 1 {
		t.Errorf("want 1 skipped cluster, got %d", stats.SkippedTooLarge)
	}
	if len(stats.SkippedClusterNames) != 1 || stats.SkippedClusterNames[0] != "verbose" {
		t.Errorf("want SkippedClusterNames=[verbose], got %v", stats.SkippedClusterNames)
	}
}

// Idempotency: Annotate twice produces the same edge count as Annotate
// once, because hasRelationship dedups.
func TestAnnotate_Idempotent(t *testing.T) {
	a := knob("cml:p1/svc/x", "string", "")
	b := knob("cml:p2/svc/x", "string", "")
	elems := []*contractpb.ContractElement{a, b}

	m := New(Config{})
	m.Annotate(elems)
	firstCount := len(a.Relationships) + len(b.Relationships)
	m.Annotate(elems)
	secondCount := len(a.Relationships) + len(b.Relationships)

	if firstCount != secondCount {
		t.Errorf("not idempotent: first=%d second=%d", firstCount, secondCount)
	}
}

// Three-element cluster: pairwise bidirectional means each element
// gets 2 relationships, total 6.
func TestAnnotate_ThreeElementCluster(t *testing.T) {
	a := knob("cml:p1/svc/timeout_ms", "uint64", "")
	b := knob("cml:p2/svc/timeout_ms", "uint64", "")
	c := knob("cml:p3/svc/timeout_ms", "uint64", "")
	elems := []*contractpb.ContractElement{a, b, c}

	m := New(Config{})
	stats := m.Annotate(elems)

	if stats.ClustersMatched != 1 {
		t.Errorf("clusters: want 1, got %d", stats.ClustersMatched)
	}
	if stats.RelationshipsEmitted != 6 {
		t.Errorf("relationships: want 6 (3*2 pairwise), got %d", stats.RelationshipsEmitted)
	}
	for _, e := range elems {
		if len(e.Relationships) != 2 {
			t.Errorf("%s: want 2 relationships, got %d", e.GetId(), len(e.Relationships))
		}
	}
}

// Name normalization unit tests covering the most common transforms.
func TestNormalizeName_Cases(t *testing.T) {
	cases := map[string]string{
		"cml:rust/config_example/greeting": "greeting",
		"cml:cpp/config_example/greeting":  "greeting",
		"argh:create --arch":               "arch",
		"argh:list --include-components":   "include_components",
		"argh:list --include_components":   "include_components",
		"argh:srv MAX_CONNECTIONS":         "max_connections",
		"argh:srv maxConnections":          "max_connections",
		"fuchsia.io/Directory.Open":        "open",
		"argh:show <query>":                "query",
		"":                                 "",
	}
	for in, want := range cases {
		if got := normalizeName(in); got != want {
			t.Errorf("normalizeName(%q): want %q, got %q", in, want, got)
		}
	}
}

// ----- helpers -----

func findRel(e *contractpb.ContractElement, targetID string) *contractpb.Relationship {
	for _, r := range e.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_SAME_AFFORDANCE && r.GetTargetElementId() == targetID {
			return r
		}
	}
	return nil
}

func relationshipExists(e *contractpb.ContractElement, k contractpb.RelationshipKind, target string) bool {
	for _, r := range e.GetRelationships() {
		if r.GetKind() == k && r.GetTargetElementId() == target {
			return true
		}
	}
	return false
}
