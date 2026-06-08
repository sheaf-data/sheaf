package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// A config that lives in a subdirectory (e.g. docs/examples/<system>/) with its
// categorization-rules.textproto alongside should resolve to that sibling map,
// not the repo root.
func TestResolveConfigPaths_PrefersSiblingSourceMap(t *testing.T) {
	repo := t.TempDir()
	sub := filepath.Join(repo, "docs", "examples", "self-scan")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(sub, "sheaf.textproto")
	if err := os.WriteFile(cfg, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sib := filepath.Join(sub, "categorization-rules.textproto")
	if err := os.WriteFile(sib, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, rules, err := resolveConfigPaths(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	if rules != sib {
		t.Errorf("rules path = %q, want sibling %q", rules, sib)
	}
}

// With no source map next to the config, resolution falls back to the repo root
// (the historical default), preserving existing behavior.
func TestResolveConfigPaths_FallsBackToRepoRoot(t *testing.T) {
	repo := t.TempDir()
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(sub, "sheaf.textproto")
	if err := os.WriteFile(cfg, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// deliberately no categorization-rules.textproto next to cfg

	_, rules, err := resolveConfigPaths(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repo, "categorization-rules.textproto")
	if rules != want {
		t.Errorf("rules path = %q, want repo-root %q", rules, want)
	}
}
