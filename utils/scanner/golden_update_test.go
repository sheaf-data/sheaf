package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestUpdateSelfScanGolden regenerates example-reports/sheaf-self.html
// from the in-process Render path. Gated: set SHEAF_UPDATE_GOLDEN=1 to
// run it. Mirrors TestRender_SelfScanByteIdentical's Render args + rules
// staging exactly, so the regenerated golden satisfies that test.
func TestUpdateSelfScanGolden(t *testing.T) {
	if os.Getenv("SHEAF_UPDATE_GOLDEN") == "" {
		t.Skip("set SHEAF_UPDATE_GOLDEN=1 to regenerate example-reports/sheaf-self.html")
	}
	repoRoot := repoRootForTest(t)
	configPath := filepath.Join(repoRoot, "docs/examples/self-scan/sheaf.textproto")
	committed := filepath.Join(repoRoot, "example-reports/sheaf-self.html")
	rulesSrc := filepath.Join(repoRoot, "docs/examples/self-scan/categorization-rules.textproto")

	staged := filepath.Join(repoRoot, "categorization-rules.textproto")
	if _, err := os.Stat(staged); err == nil {
		t.Fatalf("categorization-rules.textproto already present at repo root; remove it before regenerating")
	}
	src, err := os.ReadFile(rulesSrc)
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	if err := os.WriteFile(staged, src, 0o644); err != nil {
		t.Fatalf("stage rules: %v", err)
	}
	defer os.Remove(staged)

	n, bridged, err := Render(context.Background(), configPath, repoRoot,
		"sheaf", "", "cli",
		"https://github.com/sheaf-data/sheaf/blob/main/{path}#L{line}", committed)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	t.Logf("regenerated %s: %d elements, %d bridged", committed, n, bridged)
}
