package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	configpb "github.com/sheaf-data/sheaf/proto/config"
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

func TestRun_WiresAdaptersInParallel(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/foo_test.cc": `TEST(FooTest, BarReturnsTrue) { EXPECT_TRUE(Bar()); }`,
		"src/lib.rs": `
#[test]
fn does_a_thing() {}
`,
		"tests/x.bats":  `@test "shell works" { :; }`,
		"docs/intro.md": "Use `fuchsia.io/Directory.Open` to open a thing.",
	})
	cfg := &configpb.Config{
		Version: 1,
		Project: &configpb.Project{Name: "test"},
		TestParser: []*configpb.TestParserConfig{
			{
				Name: "gtest",
				PerAdapter: &configpb.TestParserConfig_Gtest{
					Gtest: &configpb.GTestConfig{Include: []string{"src/**/*_test.cc"}},
				},
			},
			{
				Name: "rust-test",
				PerAdapter: &configpb.TestParserConfig_RustTest{
					RustTest: &configpb.RustTestConfig{Include: []string{"src/**/*.rs"}},
				},
			},
			{
				Name: "bats",
				PerAdapter: &configpb.TestParserConfig_Bats{
					Bats: &configpb.BatsConfig{Include: []string{"tests/**/*.bats"}},
				},
			},
		},
		DocParser: []*configpb.DocParserConfig{
			{
				Name: "markdown",
				PerAdapter: &configpb.DocParserConfig_Markdown{
					Markdown: &configpb.MarkdownConfig{Include: []string{"docs/**/*.md"}},
				},
			},
		},
	}

	o, err := New(cfg, nil, repo)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := o.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.AdapterErrors) > 0 {
		t.Errorf("AdapterErrors = %+v, want 0", res.AdapterErrors)
	}
	s := res.Corpus.Stats()
	if s.Tests != 3 { // 1 gtest + 1 rust + 1 bats
		t.Errorf("Tests = %d, want 3", s.Tests)
	}
	if s.DocClaims == 0 {
		t.Errorf("DocClaims = 0, expected >=1 from markdown adapter")
	}
}

func TestNew_UnknownAdapter(t *testing.T) {
	cfg := &configpb.Config{
		Version: 1,
		Project: &configpb.Project{Name: "x"},
		TestParser: []*configpb.TestParserConfig{
			{Name: "junit"},
		},
	}
	if _, err := New(cfg, nil, "/tmp"); err == nil {
		t.Errorf("expected error for unknown test_parser")
	}
}

func TestSummary_ListsResolvedAdapters(t *testing.T) {
	cfg := &configpb.Config{
		Version: 1,
		Project: &configpb.Project{Name: "x"},
		TestParser: []*configpb.TestParserConfig{
			{
				Name: "gtest",
				PerAdapter: &configpb.TestParserConfig_Gtest{
					Gtest: &configpb.GTestConfig{},
				},
			},
		},
		DocParser: []*configpb.DocParserConfig{
			{
				Name: "markdown",
				PerAdapter: &configpb.DocParserConfig_Markdown{
					Markdown: &configpb.MarkdownConfig{},
				},
			},
		},
	}
	o, _ := New(cfg, nil, "/tmp")
	s := o.Summary()
	if len(s.TestParsers) != 1 || s.TestParsers[0] != "gtest" {
		t.Errorf("TestParsers = %v", s.TestParsers)
	}
	if len(s.DocParsers) != 1 || s.DocParsers[0] != "markdown" {
		t.Errorf("DocParsers = %v", s.DocParsers)
	}
}
