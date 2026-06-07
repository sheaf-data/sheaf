package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheaf-data/sheaf/internal/grounding"
)

// selfScanNarrativeDocGlobs is the concept-doc surface the self-scan's
// concept-doc report grounds against (the doc-centric clear/ambiguous/silent
// lens). It MUST stay in sync with the emit-grounding invocation in
// scripts/regen-example-reports.sh (render_self_inprocess) that produces
// example-reports/sheaf-self-concept-docs.html — the per-subcommand reference
// pages are deliberately excluded (they're the reference surface, not concept
// docs).
var selfScanNarrativeDocGlobs = []string{
	"README.md",
	"docs/cli/sheaf.md",
	"docs/cli/workflows.md",
	"docs/config.md",
	"docs/mcp/*.md",
	"docs/playbooks/**/*.md",
}

// TestSelfScan_ConceptDocReachFloor guards the concept-doc lens invariant:
// sheaf's own narrative docs must clearly ground every element of its CLI
// surface. This is the "keep reach high" gate behind the committed
// example-reports/sheaf-self-concept-docs.html. A new subcommand or flag that
// no concept doc names in the qualified `sheaf <sub> --flag` form lands as
// "silent" and fails this test — the fix is a sentence in docs/cli/sheaf.md
// (or a workflow), not lowering the floor. See docs/cli/self-monitoring.md.
func TestSelfScan_ConceptDocReachFloor(t *testing.T) {
	repoRoot := repoRootForReachTest(t)
	configPath := filepath.Join(repoRoot, "docs/examples/self-scan/sheaf.textproto")

	rep, err := grounding.BuildConfig(context.Background(), configPath, repoRoot,
		"sheaf", "Sheaf (self-scan)", selfScanNarrativeDocGlobs, "")
	if err != nil {
		t.Fatalf("grounding.BuildConfig: %v", err)
	}
	s := rep.Summary
	if s.ElementsNotMentioned > 0 {
		t.Errorf("concept-doc reach regressed: %d/%d CLI elements are silent (named "+
			"in no narrative doc); want 0. Add a qualified `sheaf <sub> --flag` mention "+
			"in docs/cli/sheaf.md — see docs/cli/self-monitoring.md.",
			s.ElementsNotMentioned, s.ElementsTotal)
	}
	if s.ElementsUngrounded > 0 {
		t.Errorf("%d/%d CLI elements are ungrounded (mentioned but unresolvable); want 0",
			s.ElementsUngrounded, s.ElementsTotal)
	}
}

func repoRootForReachTest(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod: %v", dir, err)
	}
	return dir
}
