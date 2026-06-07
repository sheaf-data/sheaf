package scanner

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestIsShallowRepo builds a 2-commit repo and a --depth 1 clone of it, and
// pins that the full checkout reads as not-shallow and the clone as shallow —
// and that computeLag refuses to compute (Unavailable) on the shallow one
// rather than returning a false all-fresh zero. Reuses gitRun from lag_test.go.
func TestIsShallowRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src := t.TempDir()
	gitRun(t, src, nil, "init", "-q", "-b", "main")
	// Pin an identity so `git commit` works on a runner with no global
	// user.name/user.email (CI): without it git exits 128. The other lag
	// tests get identity from commitEnv's GIT_AUTHOR_*/GIT_COMMITTER_*; this
	// one commits with a nil env, so set it on the repo directly.
	gitRun(t, src, nil, "config", "user.email", "t@t")
	gitRun(t, src, nil, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, nil, "add", "-A")
	gitRun(t, src, nil, "commit", "-qm", "c1")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, nil, "add", "-A")
	gitRun(t, src, nil, "commit", "-qm", "c2")

	if isShallowRepo(src) {
		t.Error("a full checkout must not read as shallow")
	}
	if isShallowRepo(t.TempDir()) {
		t.Error("a non-git directory must not read as shallow (fail-open)")
	}

	parent := t.TempDir()
	dst := filepath.Join(parent, "shallow")
	gitRun(t, parent, nil, "clone", "--quiet", "--depth", "1", "file://"+src, dst)
	if !isShallowRepo(dst) {
		t.Error("a --depth 1 clone must read as shallow")
	}

	got := computeLag(&Snapshot{}, dst)
	if !got.Unavailable {
		t.Errorf("computeLag on a shallow clone must be Unavailable; got %+v", got)
	}
	if got.Pairs != 0 || len(got.Sorted) != 0 {
		t.Errorf("Unavailable lag must carry no distribution; got Pairs=%d Sorted=%d", got.Pairs, len(got.Sorted))
	}
	if got.UnavailableReason == "" {
		t.Error("Unavailable lag must carry a reason to surface as the caveat")
	}
}
