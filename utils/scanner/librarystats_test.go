package scanner

import "testing"

// TestComputeLibraryStats locks the two invariants the monorepo index
// depends on: (1) the bridge-completeness distribution is computed over
// the three core surfaces only — a thin/absent workflow signal never
// gates completeness; (2) the distribution sums to the live element
// count (Removed elements excluded), and per-surface counts match.
func TestComputeLibraryStats(t *testing.T) {
	r := &ReportData{
		Bridged: 1,
		Methods: []ElementRow{
			// Fully bridged on the triple, NO workflow — must still
			// land in Completeness[3]. This is the conditional-surface
			// rule: workflows don't gate the bridge.
			{Concept: 1, Test: 2, Example: 1, Workflow: 0},
			// 2 of 3, but has a workflow — the workflow must NOT push it
			// to 3/3; it's counted in Workflows only.
			{Concept: 1, Test: 1, Example: 0, Workflow: 4},
			// 1 of 3.
			{Concept: 1, Test: 0, Example: 0},
			// Orphan: none of the three.
			{Concept: 0, Test: 0, Example: 0},
			// Removed: excluded from every count.
			{Concept: 1, Test: 1, Example: 1, Removed: true},
		},
	}

	st := computeLibraryStats(r)

	if st.Total != 4 {
		t.Fatalf("Total = %d, want 4 (Removed excluded)", st.Total)
	}
	if got, want := st.Completeness, [4]int{1, 1, 1, 1}; got != want {
		t.Fatalf("Completeness = %v, want %v", got, want)
	}
	// The element with all three but no workflow is fully bridged.
	if st.Completeness[3] != 1 {
		t.Fatalf("Completeness[3] = %d, want 1 (triple present, workflow absent)", st.Completeness[3])
	}
	if st.Docs != 3 || st.Tests != 2 || st.Examples != 1 {
		t.Fatalf("per-surface = docs %d / tests %d / examples %d, want 3 / 2 / 1", st.Docs, st.Tests, st.Examples)
	}
	if st.Workflows != 1 {
		t.Fatalf("Workflows = %d, want 1", st.Workflows)
	}

	sum := 0
	for _, c := range st.Completeness {
		sum += c
	}
	if sum != st.Total {
		t.Fatalf("Completeness sum = %d, want Total %d", sum, st.Total)
	}
	if st.Bridged != 1 {
		t.Fatalf("Bridged = %d, want 1 (carried from ReportData)", st.Bridged)
	}
}
