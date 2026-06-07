package verify

import (
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// --- helpers ---------------------------------------------------------------

func hasCategory(r *Report, c Category) bool {
	for _, f := range r.Findings {
		if f.Category == c {
			return true
		}
	}
	return false
}

func hasCategoryTier(r *Report, c Category, tier string) bool {
	for _, f := range r.Findings {
		if f.Category == c && f.Tier == tier {
			return true
		}
	}
	return false
}

func hasElement(r *Report, el string) bool {
	for _, f := range r.Findings {
		if f.Element == el {
			return true
		}
	}
	return false
}

// testProfile builds a profile map whose unit-test bucket has one ref per
// path passed (round-robin if count > len(paths)).
func testProfile(count int, paths ...string) map[string]any {
	unit := make([]any, 0, count)
	for i := 0; i < count; i++ {
		p := paths[i%len(paths)]
		unit = append(unit, map[string]any{"path": p, "line": float64(i + 1), "testName": "T"})
	}
	return map[string]any{"tests": map[string]any{"unit": unit}}
}

// --- reconcile -------------------------------------------------------------

// The report claims an element is tested, but its profile carries no
// deterministic test refs — a headline that can't be reproduced from the
// join data. This is the one failure verify must call an ERROR pre-disk.
func TestReconcileTests_MismatchIsError(t *testing.T) {
	rd := &scanner.ReportData{
		Total:     2,
		TestCount: 1,
		Methods:   []scanner.MethodRow{{Name: "a", Test: 1}, {Name: "b", Test: 0}},
	}
	profByID := map[string]map[string]any{
		"a": testProfile(0),
		"b": testProfile(0),
	}
	rep := &Report{}
	reconcileTests(rep, rd, profByID)
	if !hasCategory(rep, CatReconcile) {
		t.Fatalf("expected a reconcile finding, got %+v", rep.Findings)
	}
}

func TestReconcileTests_ConsistentIsClean(t *testing.T) {
	rd := &scanner.ReportData{
		Total:     2,
		TestCount: 1,
		Methods:   []scanner.MethodRow{{Name: "a", Test: 3}, {Name: "b", Test: 0}},
	}
	profByID := map[string]map[string]any{
		"a": testProfile(3, "a_test.go", "a2_test.go"),
		"b": testProfile(0),
	}
	rep := &Report{}
	reconcileTests(rep, rd, profByID)
	if hasCategory(rep, CatReconcile) {
		t.Fatalf("expected no reconcile finding, got %+v", rep.Findings)
	}
}

// --- decomposition / thresholds -------------------------------------------

// A modifier tier at exactly 0% must warn (verify before showing) — never
// assert "wrong" pre-disk. This guards the honesty rail.
func TestDecompose_ZeroSurfaceWarnsNotErrors(t *testing.T) {
	rd := &scanner.ReportData{
		Total: 48, ShowTestTile: true,
		TestCount: 10, TestPct: 21,
		PrimaryTotal: 11, PrimaryNoun: "Commands", PrimaryTestN: 10, PrimaryTestPct: 91,
		ModifierTotal: 37, ModifierNoun: "Flags", ModifierTestN: 0, ModifierTestPct: 0,
	}
	rep := &Report{}
	decomposeTiers(rep, rd, 0.15)
	if !hasCategoryTier(rep, CatZeroSurface, "modifier") {
		t.Fatalf("expected zero_surface on the modifier tier, got %+v", rep.Findings)
	}
	for _, f := range rep.Findings {
		if f.Category == CatZeroSurface && f.Severity != SeverityWarn {
			t.Fatalf("a 0%% surface must be a WARNING pre-disk, got severity %q", f.Severity)
		}
	}
}

func TestDecompose_LowCoverageWarns(t *testing.T) {
	rd := &scanner.ReportData{
		Total: 100, ShowTestTile: true, TestCount: 12, TestPct: 12,
		PrimaryTotal: 100, PrimaryNoun: "Methods", PrimaryTestN: 12, PrimaryTestPct: 12,
	}
	rep := &Report{}
	decomposeTiers(rep, rd, 0.15)
	if !hasCategory(rep, CatLowCoverage) {
		t.Fatalf("expected low_coverage warning at 12%%, got %+v", rep.Findings)
	}
}

func TestDecompose_HealthyIsClean(t *testing.T) {
	rd := &scanner.ReportData{
		Total: 10, ShowTestTile: true, TestCount: 9, TestPct: 90,
		PrimaryTotal: 10, PrimaryNoun: "Methods", PrimaryTestN: 9, PrimaryTestPct: 90,
	}
	rep := &Report{}
	decomposeTiers(rep, rd, 0.15)
	if len(rep.Findings) != 0 {
		t.Fatalf("expected no findings on a healthy 90%% surface, got %+v", rep.Findings)
	}
}

// A shown percentage that disagrees with its own numerator/denominator is
// provably broken regardless of disk — must be an error.
func TestDecompose_PctMismatchErrors(t *testing.T) {
	rd := &scanner.ReportData{
		Total: 10, ShowTestTile: true, TestCount: 5, TestPct: 90, // 5/10 = 50%, not 90%
	}
	rep := &Report{}
	decomposeTiers(rep, rd, 0.15)
	if !hasCategory(rep, CatReconcile) {
		t.Fatalf("expected reconcile (pct mismatch) error, got %+v", rep.Findings)
	}
}

// The denominator-effect explainer fires when the pooled figure is flagged
// low but a constituent tier is healthy.
func TestDecompose_DenominatorEffectExplained(t *testing.T) {
	rd := &scanner.ReportData{
		Total: 100, ShowTestTile: true, TestCount: 10, TestPct: 10,
		PrimaryTotal: 10, PrimaryNoun: "Commands", PrimaryTestN: 10, PrimaryTestPct: 100,
		ModifierTotal: 90, ModifierNoun: "Flags", ModifierTestN: 0, ModifierTestPct: 0,
	}
	rep := &Report{}
	decomposeTiers(rep, rd, 0.15)
	if !hasCategory(rep, CatDenominator) {
		t.Fatalf("expected a denominator-effect note, got %+v", rep.Findings)
	}
}

// --- smearing --------------------------------------------------------------

func TestDetectSmearing(t *testing.T) {
	rd := &scanner.ReportData{Methods: []scanner.MethodRow{{Name: "x"}, {Name: "y"}}}
	profByID := map[string]map[string]any{
		"x": testProfile(12, "one_test.go"),                                                   // 12 refs, 1 file → smeared
		"y": testProfile(12, "a_test.go", "b_test.go", "c_test.go", "d_test.go", "e_test.go"), // 12 refs, 5 files → not
	}
	rep := &Report{}
	detectSmearing(rep, rd, profByID)
	if !hasElement(rep, "x") {
		t.Fatalf("expected smearing finding for x, got %+v", rep.Findings)
	}
	if hasElement(rep, "y") {
		t.Fatalf("did not expect a smearing finding for y, got %+v", rep.Findings)
	}
}

// --- small units -----------------------------------------------------------

func TestPctRound(t *testing.T) {
	cases := []struct{ n, total, want int }{
		{1, 1, 100}, {0, 5, 0}, {1, 3, 33}, {2, 3, 67}, {0, 0, 0}, {37, 37, 100},
	}
	for _, c := range cases {
		if got := pctRound(c.n, c.total); got != c.want {
			t.Errorf("pctRound(%d,%d)=%d want %d", c.n, c.total, got, c.want)
		}
	}
}

func TestFinalizeVerdict(t *testing.T) {
	warn := &Report{Findings: []Finding{{Severity: SeverityWarn}}}
	warn.finalize()
	if warn.Verdict != VerdictReview || warn.Warnings != 1 {
		t.Errorf("warn: got %s w=%d", warn.Verdict, warn.Warnings)
	}
	broke := &Report{Findings: []Finding{{Severity: SeverityError}, {Severity: SeverityWarn}}}
	broke.finalize()
	if broke.Verdict != VerdictBroken || broke.Errors != 1 {
		t.Errorf("broken: got %s e=%d", broke.Verdict, broke.Errors)
	}
	clean := &Report{}
	clean.finalize()
	if clean.Verdict != VerdictTrustworthy {
		t.Errorf("clean: got %s", clean.Verdict)
	}
}

func TestRenderLedgerSmoke(t *testing.T) {
	r := &Report{
		Library: "l", Ecosystem: "cli", ElementCount: 1, Threshold: 0.15,
		Metrics:  []MetricCheck{{Name: "tests", Tier: "pooled", Numerator: 0, Denominator: 1, Percent: 0, Reproduced: true, Flagged: true}},
		Findings: []Finding{{Category: CatZeroSurface, Severity: SeverityWarn, Title: "tests reads 0%"}},
	}
	r.finalize()
	out := RenderLedger(r)
	if !strings.Contains(out, "trust ledger") {
		t.Fatalf("ledger missing header:\n%s", out)
	}
	if !strings.Contains(out, "REVIEW") {
		t.Fatalf("ledger missing verdict banner:\n%s", out)
	}
}
