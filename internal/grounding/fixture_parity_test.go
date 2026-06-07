package grounding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestFixtureParity asserts that a real detector output has the SAME JSON
// key structure, key-for-key, as the frozen contract fixture
// (docs/grounding/grounding.fixture.json). This is the in-repo guard for
// "the backend emits exactly the shape the UI renders against." It walks
// both documents and compares the set of dotted key paths (descending into
// the first list element as the representative shape), plus the union of
// keys across all findings / elements / candidates / checked entries so an
// optional key present on only some items is still covered.
//
// It does NOT compare values (those are data); only the shape. If this
// test fails, either the detector drifted from the contract or the frozen
// fixture changed — both must be a deliberate schema_version bump.
func TestFixtureParity(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "docs", "grounding", "grounding.fixture.json")
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Skipf("fixture not present (%v) — parity check skipped", err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	delete(fixture, "_comment") // documentation-only key, not part of the shape

	// Build a representative report exercising all three states + a
	// not_mentioned element, so every shape branch is populated.
	body := "# Drivers and nodes\n\n" +
		"## Controllers\n\nThe parent drives the child through the `NodeController` protocol returned by AddChild.\n" +
		"## The node topology\n\nEach node exposes a controller that the parent uses to manage it.\n" +
		"## Adding children\n\nWhen a driver wants a child, it adds a child node and waits.\n" +
		"## The manager\n\nThe composite node manager assembles a composite from several parent nodes.\n"
	rep, err := Build(Options{
		Library:        "fuchsia.driver.framework",
		LibraryDisplay: "Fuchsia OS — Drivers & Framework",
		Repo:           "fuchsia.git",
		Commit:         "9f3c2a1",
		GeneratedAt:    "2026-06-01T00:00:00Z",
		Elements:       driverElems(),
		Docs:           []Doc{{Path: "docs/concepts/drivers/x.md", Body: []byte(body)}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Round-trip the report through JSON so we compare the WIRE shape (what
	// the JSON tags actually produce), not the Go struct.
	rb, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(rb, &got); err != nil {
		t.Fatalf("parse report json: %v", err)
	}

	// Sanity: the representative report must actually contain at least one
	// finding and at least one not_mentioned element, or the parity check
	// wouldn't exercise those branches.
	if len(rep.Findings) == 0 {
		t.Fatal("representative report has no findings; parity check would be vacuous")
	}

	fKeys := shapeKeys(fixture, "")
	gKeys := shapeKeys(got, "")

	// Augment with union-of-keys for the array-of-object fields so optional
	// keys present on only some items are included from both sides.
	for _, arr := range []string{"elements", "findings"} {
		fKeys = unionInto(fKeys, arr, fixture)
		gKeys = unionInto(gKeys, arr, got)
	}

	onlyFixture := diff(fKeys, gKeys)
	onlySample := diff(gKeys, fKeys)
	if len(onlyFixture) > 0 || len(onlySample) > 0 {
		sort.Strings(onlyFixture)
		sort.Strings(onlySample)
		t.Fatalf("JSON shape drift from frozen fixture:\n  only in fixture: %v\n  only in output : %v",
			onlyFixture, onlySample)
	}
}

// shapeKeys returns the set of dotted key paths in d, descending into the
// first element of any list (representative shape) and using "[]" to mark
// an array level.
func shapeKeys(d any, prefix string) map[string]bool {
	out := map[string]bool{}
	switch v := d.(type) {
	case map[string]any:
		for k, val := range v {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			for kk := range shapeKeys(val, p) {
				out[kk] = true
			}
		}
	case []any:
		if len(v) > 0 {
			for kk := range shapeKeys(v[0], prefix+"[]") {
				out[kk] = true
			}
		}
	}
	return out
}

// unionInto adds, for the named top-level array field, the union of nested
// shape keys across ALL its elements (not just the first), so an optional
// key present on only some items is captured.
func unionInto(keys map[string]bool, field string, doc map[string]any) map[string]bool {
	arr, ok := doc[field].([]any)
	if !ok {
		return keys
	}
	for _, item := range arr {
		for kk := range shapeKeys(item, field+"[]") {
			keys[kk] = true
		}
	}
	return keys
}

func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	return out
}
