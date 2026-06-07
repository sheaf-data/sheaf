package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for `sheaf render`, specifically the commit-derivation behavior
// the regen render_one path depends on: when --commit is omitted but
// --repo-root points at a git tree, the report header must carry the
// repo's short HEAD sha. Without this, every coverage report rendered by
// scripts/regen-example-reports.sh (ffx, envoy, fd, grpc, kubectl, …)
// drops the sha because the template only emits `sha:` when .Commit is
// non-empty.

// renderToFile drives runRender (which writes to *os.File) by aiming its
// stdout/stderr at throwaway temp files, and returns the rendered HTML.
// out is the rendered report path; args are the render flags.
func renderToFile(t *testing.T, out string, args []string) string {
	t.Helper()
	devout, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("temp stdout: %v", err)
	}
	defer devout.Close()
	deverr, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatalf("temp stderr: %v", err)
	}
	defer deverr.Close()

	full := append([]string{"--from-snapshot"}, args...)
	if rc := runRender(devout, deverr, full); rc != 0 {
		errBytes, _ := os.ReadFile(deverr.Name())
		t.Fatalf("runRender returned %d; stderr=%s", rc, string(errBytes))
	}
	html, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read rendered report %s: %v", out, err)
	}
	return string(html)
}

// snapshotForRender builds a Snapshot JSON from the shared fixture and
// returns its path — the input every render case below renders from.
func snapshotForRender(t *testing.T) string {
	t.Helper()
	dir := setupFixture(t)
	snap := filepath.Join(t.TempDir(), "snap.json")
	var out, errOut bytes.Buffer
	if rc := runSnapshot(&out, &errOut, "", dir, "fuchsia.io", "", snap); rc != 0 {
		t.Fatalf("runSnapshot returned %d; stderr=%s", rc, errOut.String())
	}
	return snap
}

func TestRunRender_DerivesCommitFromRepoRoot(t *testing.T) {
	snap := snapshotForRender(t)
	// The sheaf repo itself is a git working tree; use it as the
	// --repo-root so gitShortCommit resolves a real short HEAD.
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	want := strings.TrimSpace(gitShortCommit(repoRoot))
	if want == "" {
		t.Skip("repo root is not a git tree (no HEAD); commit-derivation cannot be exercised")
	}
	out := filepath.Join(t.TempDir(), "report.html")
	html := renderToFile(t, out, []string{
		snap, "--library", "fuchsia.io", "--ecosystem", "fidl",
		"--repo-root", repoRoot, "-o", out,
	})
	wantSpan := "sha:" + want
	if !strings.Contains(html, wantSpan) {
		t.Errorf("rendered report does not carry the derived sha %q; "+
			"the regen render_one coverage reports would show no sha", wantSpan)
	}
}

func TestRunRender_ExplicitCommitOverridesDerivation(t *testing.T) {
	snap := snapshotForRender(t)
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	out := filepath.Join(t.TempDir(), "report.html")
	const pinned = "zzz9999"
	html := renderToFile(t, out, []string{
		snap, "--library", "fuchsia.io", "--ecosystem", "fidl",
		"--repo-root", repoRoot, "--commit", pinned, "-o", out,
	})
	if !strings.Contains(html, "sha:"+pinned) {
		t.Errorf("explicit --commit %q was not honored in the rendered report", pinned)
	}
	// The real HEAD must NOT leak in when an explicit commit is given.
	if real := strings.TrimSpace(gitShortCommit(repoRoot)); real != "" && real != pinned &&
		strings.Contains(html, "sha:"+real) {
		t.Errorf("derived sha %q leaked despite explicit --commit %q", real, pinned)
	}
}

func TestRunRender_NonGitRepoRootLeavesCommitEmpty(t *testing.T) {
	snap := snapshotForRender(t)
	// A plain temp dir is not a git tree, so gitShortCommit returns ""
	// and the report must render with no sha (deterministic).
	nonGit := t.TempDir()
	out := filepath.Join(t.TempDir(), "report.html")
	html := renderToFile(t, out, []string{
		snap, "--library", "fuchsia.io", "--ecosystem", "fidl",
		"--repo-root", nonGit, "-o", out,
	})
	if strings.Contains(html, "sha:") {
		t.Errorf("non-git --repo-root produced a sha; render must stay empty/deterministic")
	}
}
