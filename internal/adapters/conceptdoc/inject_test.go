package conceptdoc

import (
	"testing"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// claim is a tiny constructor for a docs.concepts DocClaim attributed to one
// element — mirrors what docClaimFor emits, without needing a full mention.
func claim(elementID, path string) *docclaimpb.DocClaim {
	return &docclaimpb.DocClaim{
		SourcePath:   path,
		ContractRefs: []string{elementID},
		Kind:         docclaimpb.DocClaimKind_PROSE_MENTION,
		Adapter:      AdapterName,
		Provenance:   &commonpb.RowProvenance{Tier: commonpb.RowProvenance_DETERMINISTIC, Source: AdapterName},
	}
}

// countConcepts mirrors utils/scanner.countConceptDoc: the consumer-side read
// of the injected bucket. Kept local so this package's test pins the exact
// shape the scanner expects (profile["docs"]["concepts"] as a []any).
func countConcepts(prof map[string]any) int {
	d, _ := prof["docs"].(map[string]any)
	if d == nil {
		return 0
	}
	if arr, ok := d["concepts"].([]any); ok {
		return len(arr)
	}
	return 0
}

func TestClaimsByElement_GroupsByContractRef(t *testing.T) {
	res := &Result{Claims: []*docclaimpb.DocClaim{
		claim("lib/A", "docs/concepts/a1.md"),
		claim("lib/A", "docs/concepts/a2.md"),
		claim("lib/B", "docs/development/b.md"),
	}}
	by := ClaimsByElement(res)
	if len(by) != 2 {
		t.Fatalf("want 2 elements, got %d", len(by))
	}
	if got := len(by["lib/A"]); got != 2 {
		t.Errorf("lib/A: want 2 claims, got %d", got)
	}
	if got := len(by["lib/B"]); got != 1 {
		t.Errorf("lib/B: want 1 claim, got %d", got)
	}
	if _, ok := by["lib/C"]; ok {
		t.Errorf("lib/C should be absent (no claim), got present")
	}
}

func TestInjectIntoProfiles_PopulatesConceptsBucket(t *testing.T) {
	profiles := []map[string]any{
		// A already carries a legacy reference surface — injection must add
		// docs.concepts ALONGSIDE it, not clobber docs.
		{"elementId": "lib/A", "docs": map[string]any{
			"reference": map[string]any{"fidldoc": []any{map[string]any{"path": "a-ref.md"}}},
		}},
		// B has no docs map at all — injection must create it.
		{"elementId": "lib/B"},
		// C has no claims — injection must leave it untouched (not-covered).
		{"elementId": "lib/C", "docs": map[string]any{}},
	}
	by := ClaimsByElement(&Result{Claims: []*docclaimpb.DocClaim{
		claim("lib/A", "docs/concepts/a.md"),
		claim("lib/B", "docs/development/b1.md"),
		claim("lib/B", "docs/development/b2.md"),
	}})

	n := InjectIntoProfiles(profiles, by)
	if n != 2 {
		t.Fatalf("want 2 profiles injected (A,B), got %d", n)
	}

	if got := countConcepts(profiles[0]); got != 1 {
		t.Errorf("A: want 1 concepts claim, got %d", got)
	}
	// Legacy reference surface on A must survive the injection.
	aDocs := profiles[0]["docs"].(map[string]any)
	if _, ok := aDocs["reference"]; !ok {
		t.Errorf("A: legacy docs.reference was clobbered by the injection")
	}
	if got := countConcepts(profiles[1]); got != 2 {
		t.Errorf("B: want 2 concepts claims, got %d", got)
	}
	if got := countConcepts(profiles[2]); got != 0 {
		t.Errorf("C: want 0 concepts claims (not-covered), got %d", got)
	}

	// Each injected claim is a generic map shaped like the rest of the
	// snapshot (protojson keys), so the scanner's []any read works and the
	// source_path round-trips.
	bBucket := profiles[1]["docs"].(map[string]any)["concepts"].([]any)
	first, ok := bBucket[0].(map[string]any)
	if !ok {
		t.Fatalf("injected claim is not a map[string]any, got %T", bBucket[0])
	}
	if first["sourcePath"] == nil && first["source_path"] == nil {
		t.Errorf("injected claim lost its source path: %v", first)
	}
}

// A claim with no contract_ref must not panic and must not create a phantom
// bucket — defensive, since the bucket is keyed by the attributed element.
func TestClaimsByElement_DropsRefless(t *testing.T) {
	res := &Result{Claims: []*docclaimpb.DocClaim{
		{SourcePath: "docs/x.md"}, // no ContractRefs
		claim("lib/A", "docs/a.md"),
	}}
	by := ClaimsByElement(res)
	if len(by) != 1 {
		t.Fatalf("want 1 element, got %d", len(by))
	}
	if _, ok := by["lib/A"]; !ok {
		t.Errorf("lib/A missing")
	}
}
