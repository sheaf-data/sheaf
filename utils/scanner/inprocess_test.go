package scanner

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// repoRootForTest walks up from the package directory to the repo root
// (the directory containing go.mod).
func repoRootForTest(t *testing.T) string {
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

// timestampRE matches the "2026-05-27 19:15 UTC" GeneratedAt stamp that
// the report embeds. Render uses time.Now(), so an exact byte compare
// against the committed report has to neutralize the stamp.
var timestampRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC`)

// TestRender_SelfScanByteIdentical drives the in-process Render path
// against the self-scan config and asserts the rendered HTML is
// byte-identical (modulo the embedded timestamp) to the committed
// utils/scanner/testdata/sheaf-self.html — the proof that the in-process
// pipeline is equivalent to the server-backed scanner CLI that
// produced the committed report.
func TestRender_SelfScanByteIdentical(t *testing.T) {
	repoRoot := repoRootForTest(t)
	// The committed golden was generated against a FULL checkout. On a shallow
	// clone the Lag surface is (correctly) suppressed to its caveat instead of
	// the real distribution, so the bytes can never match — skip rather than
	// report a spurious failure. (That the golden silently differed on a
	// shallow clone before this guard is exactly the bug this addresses.)
	if isShallowRepo(repoRoot) {
		t.Skip("self-scan golden requires full git history; this checkout is shallow (git fetch --unshallow)")
	}
	configPath := filepath.Join(repoRoot, "docs/examples/self-scan/sheaf.textproto")
	committed := filepath.Join(repoRoot, "utils/scanner/testdata/sheaf-self.html")
	rulesSrc := filepath.Join(repoRoot, "docs/examples/self-scan/categorization-rules.textproto")

	// The committed report was generated with the self-scan rules
	// staged at the repo root (see scripts/regen-example-reports.sh).
	// Reproduce that staging here. Skip if a rules file is already
	// present so a concurrently-staged tree isn't clobbered.
	staged := filepath.Join(repoRoot, "categorization-rules.textproto")
	if _, err := os.Stat(staged); err == nil {
		t.Skip("categorization-rules.textproto already present at repo root; skipping to avoid clobber")
	}
	src, err := os.ReadFile(rulesSrc)
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	if err := os.WriteFile(staged, src, 0o644); err != nil {
		t.Fatalf("stage rules: %v", err)
	}
	t.Cleanup(func() { os.Remove(staged) })

	out := filepath.Join(t.TempDir(), "sheaf-self.html")
	n, bridged, err := Render(context.Background(), configPath, repoRoot,
		"sheaf", "", "cli",
		"https://github.com/sheaf-data/sheaf/blob/main/{path}#L{line}", out)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if n == 0 {
		t.Fatalf("Render returned zero elements")
	}
	t.Logf("Render: %d elements, %d bridged", n, bridged)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read rendered: %v", err)
	}
	want, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("read committed report: %v", err)
	}
	gotNorm := timestampRE.ReplaceAll(got, []byte("TS"))
	wantNorm := timestampRE.ReplaceAll(want, []byte("TS"))
	if string(gotNorm) != string(wantNorm) {
		t.Errorf("rendered report differs from committed utils/scanner/testdata/sheaf-self.html "+
			"(got %d bytes, want %d bytes, modulo timestamp). The in-process Render path "+
			"has diverged from the server-backed scanner CLI.", len(gotNorm), len(wantNorm))
	}
}

// TestSelfScan_BridgedFloor guards the dogfood coverage invariant: sheaf's
// own CLI surface must stay near-fully bridged (docs + tests + usage on every
// element). This is the "keep it high" gate. TestRender_SelfScanByteIdentical
// alone can't enforce it — any coverage drop can be made to pass again by
// regenerating the committed golden — so this asserts a hard floor you cannot
// regenerate your way beneath. If a new subcommand or flag lands without the
// matching test, reference doc, and worked example, bridged falls below the
// floor and this fails; the fix is to add the missing piece, not to lower the
// floor. The single permitted gap is the bare-binary root element (`sheaf`
// with no subcommand), which carries no attributed test in any CLI scan —
// kubectl, gh, and fd all share that property. Coverage is independent of the
// categorization source map (rules drive area grouping, not the bridged
// count), so no rules staging is needed here.
func TestSelfScan_BridgedFloor(t *testing.T) {
	repoRoot := repoRootForTest(t)
	configPath := filepath.Join(repoRoot, "docs/examples/self-scan/sheaf.textproto")
	out := filepath.Join(t.TempDir(), "sheaf-self.html")
	n, bridged, err := Render(context.Background(), configPath, repoRoot,
		"sheaf", "", "cli",
		"https://github.com/sheaf-data/sheaf/blob/main/{path}#L{line}", out)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// 48 elements today: 1 bare-binary root (never test-bridged) + 47
	// subcommands/flags that must all stay bridged.
	const floor = 47
	if bridged < floor {
		t.Errorf("self-scan bridged=%d/%d; want >= %d. Coverage regressed: a "+
			"subcommand or flag is missing its test, reference doc, or worked "+
			"example. Add the missing piece (don't lower this floor) — see "+
			"docs/cli/self-monitoring.md.", bridged, n, floor)
	}
}
