package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// --- sampling: deterministic + risk-weighted -------------------------------

func sampleElems() (*scanner.ReportData, map[string]map[string]any) {
	rd := &scanner.ReportData{Methods: []scanner.MethodRow{
		{Name: "app run"},            // localName "run" → collision-prone → ranked first
		{Name: "lib/Service.Method"}, // 5 refs → high count
		{Name: "lib/Thing"},          // 1 ref → low
		{Name: "lib/Bare"},           // 0 refs → not an attributed claim, excluded
	}}
	profByID := map[string]map[string]any{
		"app run":            testProfile(2, "a_test.go"),
		"lib/Service.Method": testProfile(5, "s_test.go"),
		"lib/Thing":          testProfile(1, "t_test.go"),
		"lib/Bare":           testProfile(0),
	}
	return rd, profByID
}

// Same input must yield byte-identical assertions (no RNG): reproducibility
// is what lets the agent's verdicts be re-attached to a re-run.
func TestSampleAssertions_Deterministic(t *testing.T) {
	rd, profByID := sampleElems()
	r1, r2 := &Report{}, &Report{}
	sampleAssertions(r1, "mylib", rd, profByID, Options{})
	sampleAssertions(r2, "mylib", rd, profByID, Options{})
	if !reflect.DeepEqual(r1.Assertions, r2.Assertions) {
		t.Fatalf("sampling is not deterministic:\n%+v\n!=\n%+v", r1.Assertions, r2.Assertions)
	}
	if len(r1.Assertions) == 0 {
		t.Fatal("expected some assertions")
	}
}

// The sample is weighted toward the riskiest claims: a collision-prone name
// and a high-count element are kept under a tight cap; a low-count plain one
// is dropped (with a caveat naming the truncation).
func TestSampleAssertions_RiskWeightedAndBounded(t *testing.T) {
	rd, profByID := sampleElems()
	rep := &Report{}
	sampleAssertions(rep, "mylib", rd, profByID, Options{MaxAssertionElements: 2})

	got := map[string]bool{}
	for _, a := range rep.Assertions {
		got[a.Element] = true
	}
	if !got["app run"] {
		t.Errorf("collision-prone 'app run' must be sampled, got %v", keysOf(got))
	}
	if !got["lib/Service.Method"] {
		t.Errorf("high-count 'lib/Service.Method' must be sampled, got %v", keysOf(got))
	}
	if got["lib/Thing"] {
		t.Errorf("low-count 'lib/Thing' should be dropped under cap=2, got %v", keysOf(got))
	}
	if got["lib/Bare"] {
		t.Errorf("unattributed 'lib/Bare' must never be sampled, got %v", keysOf(got))
	}
	if !caveatsHave(rep, "Attribution sample capped") {
		t.Errorf("capping the sample must add an explicit caveat, got %v", rep.Caveats)
	}
}

// Library is taken from the element's "<lib>/..." prefix when present, else
// the snapshot's library.
func TestSampleAssertions_LibraryResolution(t *testing.T) {
	rd, profByID := sampleElems()
	rep := &Report{}
	sampleAssertions(rep, "mylib", rd, profByID, Options{})
	var sawScoped, sawUnscoped bool
	for _, a := range rep.Assertions {
		if a.Element == "lib/Service.Method" && a.Library == "lib" {
			sawScoped = true
		}
		if a.Element == "app run" && a.Library == "mylib" {
			sawUnscoped = true
		}
		if a.Verdict != nil {
			t.Errorf("freshly sampled assertion must have a null verdict, got %v", *a.Verdict)
		}
	}
	if !sawScoped || !sawUnscoped {
		t.Errorf("library resolution wrong: scoped=%v unscoped=%v", sawScoped, sawUnscoped)
	}
}

// --- summarize: precision math ---------------------------------------------

func verdicted(lib, elem, verdict, reason string) Assertion {
	a := Assertion{Kind: "tested_by", Library: lib, Element: elem, TestName: "T", TestPath: "x_test.go", TestLine: 1}
	if verdict != "" {
		v := verdict
		a.Verdict = &v
	}
	if reason != "" {
		r := reason
		a.Reason = &r
	}
	return a
}

func TestRenderPrecisionLedger_Math(t *testing.T) {
	rows := []Assertion{
		verdicted("A", "A/e1", "tp", ""),
		verdicted("A", "A/e2", "tp", ""),
		verdicted("A", "A/e3", "tp", ""),
		verdicted("A", "A/e4", "fp", "matched on shared token 'run'"),
		verdicted("B", "B/e1", "tp", ""),
		verdicted("B", "B/e2", "ambiguous", "unclear"),
		verdicted("B", "B/e3", "", ""), // unverified
		verdicted("C", "C/e1", "ambiguous", "only ambiguous → precision undefined"),
	}
	out := RenderPrecisionLedger(rows)

	// A: 3 tp, 1 fp → 75%. B: 1 tp, 0 fp → 100%. C: no tp/fp → "—".
	for _, want := range []string{"75%", "100%", "| — |", "Confirmed false positives", "shared token", "unverified: **1**"} {
		if !strings.Contains(out, want) {
			t.Errorf("precision ledger missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderPrecisionLedger_Empty(t *testing.T) {
	out := RenderPrecisionLedger(nil)
	if !strings.Contains(out, "No `tested_by`") {
		t.Fatalf("empty input should explain how to emit assertions, got:\n%s", out)
	}
}

// --- loader: JSONL round-trip ----------------------------------------------

func TestLoadAssertions_JSONL(t *testing.T) {
	rows := []Assertion{
		verdicted("A", "A/e1", "tp", ""),
		verdicted("A", "A/e2", "fp", "shared token"),
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "verdicted.jsonl")
	var b strings.Builder
	for _, a := range rows {
		data, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadAssertions(path)
	if err != nil {
		t.Fatalf("LoadAssertions: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("want 2 assertions, got %d", len(loaded))
	}
	if verdictOf(loaded[1]) != "fp" {
		t.Errorf("verdict round-trip failed, got %q", verdictOf(loaded[1]))
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
