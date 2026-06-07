package verify

import "testing"

func intp(n int) *int { return &n }

// Exact match → clean, no finding.
func TestCheckGroundTruth_ExactMatchClean(t *testing.T) {
	rep := &Report{ElementCount: 200}
	checkGroundTruth(rep, Options{ExpectedElements: intp(200)})
	if hasCategory(rep, CatGroundTruth) {
		t.Fatalf("exact match must be clean, got %+v", rep.Findings)
	}
}

// Off-by-one within the tiny rounding tolerance (a synthetic/root element) →
// clean.
func TestCheckGroundTruth_WithinTinyToleranceClean(t *testing.T) {
	rep := &Report{ElementCount: 201}
	checkGroundTruth(rep, Options{ExpectedElements: intp(200)})
	if hasCategory(rep, CatGroundTruth) {
		t.Fatalf("off-by-one within tolerance must be clean, got %+v", rep.Findings)
	}
}

// A moderate gap (beyond the tiny tolerance, within the large threshold) →
// one warning.
func TestCheckGroundTruth_ModerateGapWarns(t *testing.T) {
	rep := &Report{ElementCount: 210}
	checkGroundTruth(rep, Options{ExpectedElements: intp(200)})
	fs := findingsWith(rep, CatGroundTruth)
	if len(fs) != 1 || fs[0].Severity != SeverityWarn {
		t.Fatalf("a moderate gap must be exactly one warning, got %+v", rep.Findings)
	}
}

// A large, unambiguous gap (12 on a 200-element API) → error. This is the one
// ground-truth case the honesty rail lets verify call provably wrong.
func TestCheckGroundTruth_LargeGapErrors(t *testing.T) {
	rep := &Report{ElementCount: 12}
	checkGroundTruth(rep, Options{ExpectedElements: intp(200)})
	fs := findingsWith(rep, CatGroundTruth)
	if len(fs) != 1 || fs[0].Severity != SeverityError {
		t.Fatalf("a large unambiguous gap must be an error, got %+v", rep.Findings)
	}
}

// No authoritative count → an honest caveat, never a finding.
func TestCheckGroundTruth_UnsetCaveatNotFinding(t *testing.T) {
	rep := &Report{ElementCount: 200}
	checkGroundTruth(rep, Options{})
	if hasCategory(rep, CatGroundTruth) {
		t.Fatalf("no expected count must not produce a finding, got %+v", rep.Findings)
	}
	if !caveatsHave(rep, "not independently verified") {
		t.Fatalf("unset must add an honest caveat, got %v", rep.Caveats)
	}
}
