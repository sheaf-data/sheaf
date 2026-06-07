package prbot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/review"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// setupRepo writes a minimal Sheaf-scannable project to dir and
// returns the dir path.
func setupRepo(t *testing.T, dir string, gtestBody string) {
	t.Helper()
	files := map[string]string{
		"src/foo_test.cc": gtestBody,
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
}

func minimalConfig() *configpb.Config {
	return &configpb.Config{
		Version: 1,
		Project: &configpb.Project{Name: "demo"},
		TestParser: []*configpb.TestParserConfig{
			{
				Name: "gtest",
				PerAdapter: &configpb.TestParserConfig_Gtest{
					Gtest: &configpb.GTestConfig{Include: []string{"src/**/*_test.cc"}},
				},
			},
		},
	}
}

func TestRunReview_NoPost_ReturnsComment(t *testing.T) {
	baseDir := t.TempDir()
	headDir := t.TempDir()
	setupRepo(t, baseDir, `TEST(FooTest, A) {}`)
	setupRepo(t, headDir, `TEST(FooTest, A) {}
TEST(FooTest, B) {}`)
	res, err := RunReview(context.Background(), RunOptions{
		Config:   minimalConfig(),
		BaseRoot: baseDir,
		HeadRoot: headDir,
		PRRef:    "PR#1",
	})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if res.Comment == nil || res.Comment.Body == "" {
		t.Fatalf("expected rendered comment; got %+v", res)
	}
	if res.Posted {
		t.Errorf("expected Posted=false; got %+v", res)
	}
}

func TestRunReview_PostUsesAdapter(t *testing.T) {
	baseDir := t.TempDir()
	headDir := t.TempDir()
	setupRepo(t, baseDir, `TEST(FooTest, A) {}`)
	setupRepo(t, headDir, `TEST(FooTest, A) {}
TEST(FooTest, B) {}`)

	outDir := t.TempDir()
	adapter := review.NewFileDir(outDir)
	res, err := RunReview(context.Background(), RunOptions{
		Config:   minimalConfig(),
		BaseRoot: baseDir,
		HeadRoot: headDir,
		PRRef:    "PR#42",
		Post:     true,
		Adapter:  adapter,
	})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !res.Posted {
		t.Errorf("expected Posted=true; got %+v", res)
	}
	if res.Adapter != "file" {
		t.Errorf("adapter = %q", res.Adapter)
	}
	if !strings.HasPrefix(res.PostedTo, "file://") {
		t.Errorf("posted_to = %q", res.PostedTo)
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "*.md"))
	if len(matches) != 1 {
		t.Errorf("expected 1 file in adapter outdir; got %v", matches)
	}
}

func TestRunReview_PostWithoutAdapterErrors(t *testing.T) {
	_, err := RunReview(context.Background(), RunOptions{
		Config:   minimalConfig(),
		BaseRoot: "/tmp",
		HeadRoot: "/tmp",
		Post:     true,
	})
	if err == nil || !strings.Contains(err.Error(), "Adapter") {
		t.Errorf("expected adapter-required error; got %v", err)
	}
}

func TestRunReview_RequiresPaths(t *testing.T) {
	cfg := minimalConfig()
	if _, err := RunReview(context.Background(), RunOptions{Config: cfg, HeadRoot: "/tmp"}); err == nil {
		t.Errorf("expected error for missing BaseRoot")
	}
	if _, err := RunReview(context.Background(), RunOptions{Config: cfg, BaseRoot: "/tmp"}); err == nil {
		t.Errorf("expected error for missing HeadRoot")
	}
	if _, err := RunReview(context.Background(), RunOptions{BaseRoot: "/tmp", HeadRoot: "/tmp"}); err == nil {
		t.Errorf("expected error for missing Config")
	}
}

func TestRunReview_DefaultPRRef(t *testing.T) {
	baseDir := t.TempDir()
	headDir := t.TempDir()
	setupRepo(t, baseDir, "")
	setupRepo(t, headDir, `TEST(A, B) {}`)
	res, err := RunReview(context.Background(), RunOptions{
		Config:   minimalConfig(),
		BaseRoot: baseDir,
		HeadRoot: headDir,
		// PRRef omitted.
	})
	if err != nil {
		t.Fatalf("RunReview: %v", err)
	}
	if !strings.Contains(res.Comment.Body, "PR#unknown") {
		t.Errorf("expected default PR#unknown in comment header; got %q", res.Comment.Body[:80])
	}
}
