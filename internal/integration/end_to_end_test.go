// Integration test: end-to-end pipeline against a synthetic project.
// Wires real config loader + real categorizer + real orchestrator
// against four real adapters (gtest, rust-test, bats, markdown).

package integration

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/sheaf-data/sheaf/internal/categorize"
	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
)

const sheafConfig = `
version: 1
project { name: "demo" display_name: "Demo Project" }
test_parser {
  name: "gtest"
  gtest { include: "src/**/*_test.cc" }
}
test_parser {
  name: "rust-test"
  rust_test { include: "src/**/*.rs" }
}
test_parser {
  name: "bats"
  bats { include: "tests/**/*.bats" }
}
doc_parser {
  name: "markdown"
  markdown {
    include: "docs/**/*.md"
    code_block_languages: "rust"
  }
}
`

// Rust convention: #[test] functions live inline in any .rs file,
// not exclusively in *_test.rs files. The category needs both patterns.
const sheafRules = `
version: 1
category {
  dotted_path: "tests.unit_tests"
  paths: "src/**/*_test.cc"
  paths: "src/**/*.rs"
  exclude_paths: "src/**/main.rs"
}
category { dotted_path: "tests.integration_tests" paths: "tests/**/*.bats" }
category { dotted_path: "docs.concepts" paths: "docs/**/*.md" }
`

func setupFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"sheaf.textproto":                fixtureWith(sheafConfig),
		"categorization-rules.textproto": fixtureWith(sheafRules),
		"src/foo_test.cc": `
TEST(FooTest, BarReturnsTrue) { EXPECT_TRUE(Bar()); }
TEST(FooTest, BarHandlesEmpty) { EXPECT_EQ(Bar(""), 0); }
TEST_F(FixtureSuite, ProcessesInput) { ASSERT_NE(Process(), nullptr); }
`,
		"src/lib.rs": `
#[test]
fn open_succeeds() { assert!(open("/foo").is_ok()); }
#[test]
fn open_fails_on_missing() { assert!(open("/nope").is_err()); }
`,
		"tests/integration/end_to_end.bats": `
@test "full workflow runs" { run demo; [ "$status" -eq 0 ]; }
@test "handles bad input gracefully" { run demo --bad; [ "$status" -ne 0 ]; }
`,
		"docs/overview.md": "# Overview\n\nThe `demo` tool lets you call `fuchsia.io/Directory.Open` and inspect the result.\n\n```rust\nlet dir = open(\"/foo\")?;\n```\n\nUse `demo subcommand --json` for machine output.\n",
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

func fixtureWith(s string) string {
	return s
}

func TestEndToEnd_AllAdaptersAgainstFixture(t *testing.T) {
	repo := setupFixture(t)

	cfgPath := filepath.Join(repo, "sheaf.textproto")
	rulesPath := filepath.Join(repo, "categorization-rules.textproto")

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rules, err := config.LoadRules(rulesPath)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	cat, err := categorize.New(rules)
	if err != nil {
		t.Fatalf("New categorizer: %v", err)
	}

	o, err := orchestrator.New(cfg, nil, repo)
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	res, err := o.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.AdapterErrors) > 0 {
		t.Fatalf("AdapterErrors = %+v, want 0", res.AdapterErrors)
	}

	// --- Tests: 3 gtest + 2 rust + 2 bats = 7 ---
	stats := res.Corpus.Stats()
	if stats.Tests != 7 {
		t.Errorf("Tests = %d, want 7", stats.Tests)
	}
	frameworks := make(map[string]int)
	for _, tc := range res.Corpus.Tests() {
		frameworks[tc.GetFramework()]++
	}
	if frameworks["gtest"] != 3 {
		t.Errorf("gtest count = %d, want 3", frameworks["gtest"])
	}
	if frameworks["rust-test"] != 2 {
		t.Errorf("rust-test count = %d, want 2", frameworks["rust-test"])
	}
	if frameworks["bats"] != 2 {
		t.Errorf("bats count = %d, want 2", frameworks["bats"])
	}

	// --- Doc claims: at least the two prose mentions + one example ---
	if stats.DocClaims < 3 {
		t.Errorf("DocClaims = %d, want >=3", stats.DocClaims)
	}
	mentions := make(map[string]bool)
	for _, dc := range res.Corpus.DocClaims() {
		for _, r := range dc.GetContractRefs() {
			mentions[r] = true
		}
	}
	if !mentions["fuchsia.io/Directory.Open"] {
		t.Errorf("missing mention fuchsia.io/Directory.Open; got %v", mentions)
	}

	// --- Categorizer: assigns each test file to its bucket ---
	for _, tc := range res.Corpus.Tests() {
		path := tc.GetLocation().GetPath()
		buckets, err := cat.Categorize(path, nil)
		if err != nil {
			t.Fatalf("Categorize(%q): %v", path, err)
		}
		switch tc.GetFramework() {
		case "gtest", "rust-test":
			if !containsString(buckets, "tests.unit_tests") {
				t.Errorf("%s file %q expected tests.unit_tests; got %v", tc.GetFramework(), path, buckets)
			}
		case "bats":
			if !containsString(buckets, "tests.integration_tests") {
				t.Errorf("bats file %q expected tests.integration_tests; got %v", path, buckets)
			}
		}
	}

	// --- Categorizer: doc files land in docs.concepts ---
	for _, dc := range res.Corpus.DocClaims() {
		buckets, _ := cat.Categorize(dc.GetSourcePath(), dc.GetSectionPath())
		if !containsString(buckets, "docs.concepts") {
			t.Errorf("doc %q expected docs.concepts; got %v", dc.GetSourcePath(), buckets)
		}
	}
}

func containsString(s []string, want string) bool {
	i := sort.SearchStrings(s, want)
	return i < len(s) && s[i] == want
}
