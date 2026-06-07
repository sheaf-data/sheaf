package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// Shared fixtures used by the per-subcommand test files
// (scan_test.go, doctor_test.go, gaps_test.go, …). The fixture itself
// is not tied to any one subcommand, so it lives here.

const fixtureConfig = `
version: 1
project { name: "demo" }
test_parser {
  name: "gtest"
  gtest { include: "src/**/*_test.cc" }
}
doc_parser {
  name: "markdown"
  markdown { include: "docs/**/*.md" }
}
`

const fixtureRules = `
version: 1
category {
  dotted_path: "tests.unit_tests"
  paths: "src/**/*_test.cc"
}
`

func setupFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"sheaf.textproto":                fixtureConfig,
		"categorization-rules.textproto": fixtureRules,
		"src/foo_test.cc":                `TEST(FooTest, BarReturnsTrue) { EXPECT_TRUE(Bar()); }`,
		"docs/intro.md":                  "Use `fuchsia.io/Directory.Open` to open things.",
	}
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
