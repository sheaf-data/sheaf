package verify

import (
	"testing"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// A doc source configured + every docs surface 0 → a no_source_map warning
// (the silent missing-source-map trap).
func TestDetectNoSourceMap_ConfiguredButZeroWarns(t *testing.T) {
	rep := &Report{}
	rd := &scanner.ReportData{ConceptCount: 0, ConceptDocCount: 0}
	snap := &scanner.Snapshot{ConceptDocSource: true}
	detectNoSourceMap(rep, rd, snap)
	fs := findingsWith(rep, CatNoSourceMap)
	if len(fs) != 1 {
		t.Fatalf("configured doc source + 0 docs must warn once, got %+v", rep.Findings)
	}
	if fs[0].Severity != SeverityWarn {
		t.Errorf("no_source_map must be a warning (honesty rail), got %q", fs[0].Severity)
	}
}

// The rendered-reference (DocSurfaceDirs) path also triggers it.
func TestDetectNoSourceMap_DocSurfaceDirsTriggers(t *testing.T) {
	rep := &Report{}
	rd := &scanner.ReportData{}
	snap := &scanner.Snapshot{DocSurfaceDirs: map[string]string{"markdowncli": "docs/cli/reference"}}
	detectNoSourceMap(rep, rd, snap)
	if !hasCategory(rep, CatNoSourceMap) {
		t.Fatalf("configured doc dirs + 0 docs must warn, got %+v", rep.Findings)
	}
}

// No doc source configured → 0 docs is honest absence, not a trap. No finding.
func TestDetectNoSourceMap_NotConfiguredClean(t *testing.T) {
	rep := &Report{}
	rd := &scanner.ReportData{ConceptCount: 0, ConceptDocCount: 0}
	snap := &scanner.Snapshot{}
	detectNoSourceMap(rep, rd, snap)
	if hasCategory(rep, CatNoSourceMap) {
		t.Fatalf("0 docs with no configured doc source must be clean, got %+v", rep.Findings)
	}
}

// Docs were attributed → categorization is working, no trap.
func TestDetectNoSourceMap_DocsPresentClean(t *testing.T) {
	rep := &Report{}
	rd := &scanner.ReportData{ConceptCount: 5}
	snap := &scanner.Snapshot{ConceptDocSource: true}
	detectNoSourceMap(rep, rd, snap)
	if hasCategory(rep, CatNoSourceMap) {
		t.Fatalf("docs present must be clean, got %+v", rep.Findings)
	}
}
