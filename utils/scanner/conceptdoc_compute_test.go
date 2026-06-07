package scanner

import "testing"

// These tests pin the ADDITIVE docs.concepts (narrative concept-doc)
// surface to its own field and prove it is wired strictly parallel to the
// existing `///`-fed Concept field — neither bucket bleeds into the other.
// This is the regression guard for REQUIREMENTS-concept-ingest.md decision
// 3 ("leave the existing Concept field untouched").

// countConceptDoc reads ONLY docs.concepts; the legacy docs.concept /
// docs.reference buckets must not contribute to it.
func TestCountConceptDoc_ReadsOnlyConceptsBucket(t *testing.T) {
	// A profile with the LEGACY concept + reference surfaces populated but
	// NO docs.concepts bucket → countConceptDoc must be 0.
	legacy := map[string]any{
		"docs": map[string]any{
			"concept": []any{map[string]any{"substance": "SUBSTANTIVE"}},
			"reference": map[string]any{
				"fidldoc": []any{map[string]any{"path": "ref.md"}},
			},
		},
	}
	if got := countConceptDoc(legacy); got != 0 {
		t.Fatalf("countConceptDoc must ignore legacy concept/reference buckets, got %d", got)
	}
	// The legacy Concept count, by contrast, sees those.
	if got := countConcept(legacy); got == 0 {
		t.Fatalf("sanity: countConcept should see the legacy buckets, got 0")
	}

	// A profile with ONLY the new docs.concepts bucket → countConceptDoc
	// counts it, countConcept does NOT.
	narrative := map[string]any{
		"docs": map[string]any{
			"concepts": []any{
				map[string]any{"sourcePath": "docs/concepts/x.md"},
				map[string]any{"sourcePath": "docs/development/y.md"},
			},
		},
	}
	if got := countConceptDoc(narrative); got != 2 {
		t.Fatalf("countConceptDoc should count the 2 concepts claims, got %d", got)
	}
	if got := countConcept(narrative); got != 0 {
		t.Fatalf("countConcept must NOT see the new docs.concepts bucket, got %d", got)
	}
}

// End-to-end through BuildReport: the new bucket populates ConceptDoc /
// ConceptDocCount / ConceptDocPct WITHOUT moving Concept / ConceptCount /
// ConceptPct, and vice-versa.
func TestBuildReport_ConceptDocIsAdditiveAndIsolated(t *testing.T) {
	snap := &Snapshot{
		Library: "lib",
		Elements: []map[string]any{
			{"id": "lib/A", "kind": "PROTOCOL"},
			{"id": "lib/B", "kind": "PROTOCOL"},
		},
		Profiles: []map[string]any{
			// A: only the LEGACY `///` reference-doc surface.
			{"elementId": "lib/A", "docs": map[string]any{
				"reference": map[string]any{
					"fidldoc": []any{map[string]any{"path": "a-ref.md"}},
				},
			}},
			// B: only the NEW narrative docs.concepts surface.
			{"elementId": "lib/B", "docs": map[string]any{
				"concepts": []any{map[string]any{"sourcePath": "docs/concepts/b.md"}},
			}},
		},
		SurfacesRequired: []string{"docs.reference", "tests"},
	}
	r := BuildReport(snap, "fidl", "now", "HEAD")

	// Per-element isolation: A has Concept>0 but ConceptDoc==0; B is the
	// mirror image.
	var a, b *MethodRow
	for i := range r.Methods {
		switch r.Methods[i].Name {
		case "lib/A":
			a = &r.Methods[i]
		case "lib/B":
			b = &r.Methods[i]
		}
	}
	if a == nil || b == nil {
		t.Fatalf("expected methods lib/A and lib/B; got %+v", r.Methods)
	}
	if a.Concept == 0 {
		t.Errorf("A: legacy Concept should be > 0 (the `///` ref surface), got 0")
	}
	if a.ConceptDoc != 0 {
		t.Errorf("A: ConceptDoc must be 0 (no narrative concept-doc), got %d", a.ConceptDoc)
	}
	if b.ConceptDoc == 0 {
		t.Errorf("B: ConceptDoc should be > 0 (the narrative surface), got 0")
	}
	if b.Concept != 0 {
		t.Errorf("B: legacy Concept must be 0 (no `///` ref), got %d — the new bucket leaked into Concept", b.Concept)
	}

	// Per-library isolation: exactly one element on each surface.
	if r.ConceptCount != 1 {
		t.Errorf("ConceptCount (legacy) should be 1 (only A), got %d", r.ConceptCount)
	}
	if r.ConceptDocCount != 1 {
		t.Errorf("ConceptDocCount (new) should be 1 (only B), got %d", r.ConceptDocCount)
	}
	// Percents are parallel and independent (2 elements total → 50% each).
	if r.ConceptPct != 50 {
		t.Errorf("ConceptPct should be 50, got %d", r.ConceptPct)
	}
	if r.ConceptDocPct != 50 {
		t.Errorf("ConceptDocPct should be 50, got %d", r.ConceptDocPct)
	}
}

// Adding a docs.concepts bucket must NOT change the legacy Concept rollup
// at all — the shipped-reports-unaffected guarantee.
func TestBuildReport_AddingConceptDocLeavesConceptUnchanged(t *testing.T) {
	base := &Snapshot{
		Library:  "lib",
		Elements: []map[string]any{{"id": "lib/A", "kind": "PROTOCOL"}},
		Profiles: []map[string]any{
			{"elementId": "lib/A", "docs": map[string]any{
				"reference": map[string]any{"fidldoc": []any{map[string]any{"path": "a-ref.md"}}},
			}},
		},
		SurfacesRequired: []string{"docs.reference"},
	}
	before := BuildReport(base, "fidl", "now", "HEAD")

	// Same snapshot but with a narrative concepts claim ADDED to the profile.
	withNarrative := &Snapshot{
		Library:  "lib",
		Elements: []map[string]any{{"id": "lib/A", "kind": "PROTOCOL"}},
		Profiles: []map[string]any{
			{"elementId": "lib/A", "docs": map[string]any{
				"reference": map[string]any{"fidldoc": []any{map[string]any{"path": "a-ref.md"}}},
				"concepts":  []any{map[string]any{"sourcePath": "docs/concepts/a.md"}},
			}},
		},
		SurfacesRequired: []string{"docs.reference"},
	}
	after := BuildReport(withNarrative, "fidl", "now", "HEAD")

	if before.ConceptCount != after.ConceptCount {
		t.Errorf("legacy ConceptCount changed when adding narrative docs: %d -> %d", before.ConceptCount, after.ConceptCount)
	}
	if before.ConceptPct != after.ConceptPct {
		t.Errorf("legacy ConceptPct changed when adding narrative docs: %d -> %d", before.ConceptPct, after.ConceptPct)
	}
	if before.Methods[0].Concept != after.Methods[0].Concept {
		t.Errorf("per-element legacy Concept changed: %d -> %d", before.Methods[0].Concept, after.Methods[0].Concept)
	}
	// And the new field DID light up after the addition.
	if after.ConceptDocCount != 1 {
		t.Errorf("ConceptDocCount should be 1 after adding narrative doc, got %d", after.ConceptDocCount)
	}
}
