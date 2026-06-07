package bats

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

func setupRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return dir
}

func TestDiscover_Basic(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"tests/integration/foo.bats": `#!/usr/bin/env bats

@test "addition works" {
  result="$(echo 1 + 1 | bc)"
  [ "$result" -eq 2 ]
}

@test "subtraction works" {
  result="$(echo 5 - 3 | bc)"
  [ "$result" -eq 2 ]
}
`,
	})
	p := New(Config{Include: []string{"tests/**/*.bats"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("got %d, want 2", len(tests))
	}
	names := map[string]bool{tests[0].GetName(): true, tests[1].GetName(): true}
	if !names["addition works"] || !names["subtraction works"] {
		t.Errorf("names = %v", names)
	}
}

func TestDiscover_SingleQuotes(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"x.bats": `@test 'using single quotes' { :; }`,
	})
	p := New(Config{})
	tests, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if len(tests) != 1 || tests[0].GetName() != "using single quotes" {
		t.Errorf("got %+v", tests)
	}
}
