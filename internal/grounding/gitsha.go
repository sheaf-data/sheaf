package grounding

import (
	"os/exec"
	"strings"
)

// gitShortCommit best-effort resolves the short HEAD of the repo at repoRoot;
// returns "" on any failure (not a git repo, git missing, detached/empty repo,
// …). This is a package-local copy of internal/cli/index.go's helper —
// internal/grounding cannot import internal/cli without an import cycle, and
// the helper is a few trivial lines.
//
// Returning "" for a non-git directory is load-bearing: fixture/unit scans run
// against temp dirs (not git repos), and they MUST stay deterministic, so the
// emitted Report carries an empty commit there rather than leaking a sha.
func gitShortCommit(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
