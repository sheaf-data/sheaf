package conceptdocs

import (
	"os"
	"path/filepath"
	"testing"
)

const goldenRel = "internal/report/conceptdocs/testdata/drivers.concept-docs.html"

// TestRender_FuchsiaGolden asserts the rendered Concept Docs report for the
// committed fuchsia.driver.framework sample is byte-identical to the committed
// golden. The render is fully deterministic — the sample's generated_at is
// fixed in JSON and the renderer uses no clock/random — so no normalization is
// needed. Regenerate intentional changes with the updater below.
func TestRender_FuchsiaGolden(t *testing.T) {
	committed := filepath.Join(repoRoot(t), goldenRel)
	want, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read golden (regenerate with SHEAF_UPDATE_GOLDEN=1): %v", err)
	}
	got, err := RenderString(BuildView(loadSample(t)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != string(want) {
		t.Errorf("rendered concept-docs report differs from %s (got %d, want %d bytes). "+
			"If intended, regenerate: SHEAF_UPDATE_GOLDEN=1 go test ./internal/report/conceptdocs/ -run UpdateFuchsiaGolden",
			goldenRel, len(got), len(want))
	}
}

// TestUpdateFuchsiaGolden regenerates the committed golden. Gated on
// SHEAF_UPDATE_GOLDEN=1 (mirrors utils/scanner's golden updater).
func TestUpdateFuchsiaGolden(t *testing.T) {
	if os.Getenv("SHEAF_UPDATE_GOLDEN") == "" {
		t.Skip("set SHEAF_UPDATE_GOLDEN=1 to regenerate " + goldenRel)
	}
	committed := filepath.Join(repoRoot(t), goldenRel)
	s, err := RenderString(BuildView(loadSample(t)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if err := os.WriteFile(committed, []byte(s), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("regenerated %s (%d bytes)", committed, len(s))
}
