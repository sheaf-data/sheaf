package scanner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestUpdateSelfScanGolden regenerates BOTH the frozen self-scan snapshot
// (utils/scanner/testdata/self-scan-snapshot.json) and the golden HTML
// rendered from it (utils/scanner/testdata/sheaf-self.html). Gated: set
// SHEAF_UPDATE_GOLDEN=1.
//
// Run this only to deliberately re-bless — after a render-code change, or
// to refresh the snapshot's captured corpus. Routine doc edits do NOT need
// it: TestRender_SelfScanByteIdentical renders from the committed snapshot,
// not the live repo, so a doc change can't stale the golden.
func TestUpdateSelfScanGolden(t *testing.T) {
	if os.Getenv("SHEAF_UPDATE_GOLDEN") == "" {
		t.Skip("set SHEAF_UPDATE_GOLDEN=1 to regenerate the frozen snapshot + golden")
	}
	repoRoot := repoRootForTest(t)
	configPath := filepath.Join(repoRoot, "docs/examples/self-scan/sheaf.textproto")
	rulesSrc := filepath.Join(repoRoot, "docs/examples/self-scan/categorization-rules.textproto")
	snapPath := filepath.Join(repoRoot, "utils/scanner/testdata/self-scan-snapshot.json")
	committed := filepath.Join(repoRoot, "utils/scanner/testdata/sheaf-self.html")

	// One live scan → the snapshot we freeze. A non-empty rulesPath is
	// loaded as given (no repo-root staging needed).
	snap, err := BuildSnapshot(context.Background(), configPath, repoRoot, "sheaf", "", rulesSrc)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	snapJSON, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(snapPath, append(snapJSON, '\n'), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	// Render the golden from the frozen snapshot with no repo root (no
	// git/lag) — byte-identical to how TestRender_SelfScanByteIdentical
	// renders it.
	html, _, err := RenderStatsStringFromSnapshot(context.Background(), snap, "", "cli",
		"https://github.com/sheaf-data/sheaf/blob/main/{path}#L{line}", "", nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := os.WriteFile(committed, []byte(html), 0o600); err != nil {
		t.Fatalf("write golden: %v", err)
	}
	t.Logf("regenerated %s + %s", snapPath, committed)
}
