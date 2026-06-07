package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// siblingRules finds the source map next to the config (so a config like
// docs/examples/self-scan/sheaf.textproto picks up its sibling rules without
// the repo-root copy hack).
func TestSiblingRules_FoundNextToConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "sheaf.textproto")
	if err := os.WriteFile(cfg, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(dir, "categorization-rules.textproto")
	if err := os.WriteFile(rules, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := siblingRules(cfg); got != rules {
		t.Errorf("siblingRules = %q, want %q", got, rules)
	}
}

func TestSiblingRules_AbsentReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "sheaf.textproto")
	if err := os.WriteFile(cfg, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := siblingRules(cfg); got != "" {
		t.Errorf("siblingRules with no rules file = %q, want empty (fall back to repo root)", got)
	}
}

// Source selection: neither --from-snapshot nor --config → a clear error.
func TestRun_NoSourceErrors(t *testing.T) {
	_, err := Run(Options{})
	if err == nil || !strings.Contains(err.Error(), "no snapshot source") {
		t.Fatalf("expected a 'no snapshot source' error, got %v", err)
	}
}

// The --config one-shot needs a library to scan.
func TestRun_ConfigWithoutLibraryErrors(t *testing.T) {
	_, err := Run(Options{ConfigPath: "/nonexistent/sheaf.textproto"})
	if err == nil || !strings.Contains(err.Error(), "requires --library") {
		t.Fatalf("expected a '--config requires --library' error, got %v", err)
	}
}
