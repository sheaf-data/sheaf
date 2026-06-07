package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// Tests for `sheaf review`.

func TestReview_RequiresBase(t *testing.T) {
	// The public entry refuses to run without --base; we exercise it
	// through Review() so the flag-required branch is covered.
	if rc := Review([]string{"--repo", "."}); rc != 2 {
		t.Errorf("expected rc=2 when --base missing; got %d", rc)
	}
}

func TestRunReview_MissingConfig(t *testing.T) {
	headDir := t.TempDir()
	baseDir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runReview(&out, &errOut, "", headDir, baseDir, "PR#1", false, "", "", "", "", "", "", "")
	if rc == 0 {
		t.Errorf("expected non-zero exit on missing config; out=%s err=%s", out.String(), errOut.String())
	}
}

func TestBuildReviewAdapter_NoopOverride(t *testing.T) {
	cfg := &configpb.Config{}
	a, err := buildReviewAdapter(cfg, "noop", "")
	if err != nil {
		t.Fatalf("buildReviewAdapter: %v", err)
	}
	if a == nil || a.Name() != "noop" {
		t.Errorf("expected noop adapter; got %#v", a)
	}
}

func TestBuildReviewAdapter_FileOverride(t *testing.T) {
	cfg := &configpb.Config{}
	out := filepath.Join(t.TempDir(), "out.md")
	a, err := buildReviewAdapter(cfg, "file", out)
	if err != nil {
		t.Fatalf("buildReviewAdapter(file): %v", err)
	}
	if a == nil || !strings.Contains(strings.ToLower(a.Name()), "file") {
		t.Errorf("expected file adapter; got %#v", a)
	}
}

func TestReview_FileOutRepoBaseFlags(t *testing.T) {
	// --base + --repo + --review file + --file-out: confirm the flag
	// wiring parses, even though the pipeline fails on missing config.
	headDir := t.TempDir()
	baseDir := t.TempDir()
	fileOut := filepath.Join(t.TempDir(), "out.md")
	rc := Review([]string{
		"--base", baseDir,
		"--repo", headDir,
		"--review", "file",
		"--file-out", fileOut,
		"--pr", "PR#unit",
	})
	// rc != 2 (would be parse error); 3 expected because config
	// loading fails on the empty repo. We just need flag parsing
	// to land.
	if rc == 2 {
		t.Errorf("flag parse failed for review --base/--repo/--review/--file-out; rc=2")
	}
}

func TestBuildReviewAdapter_UnknownOverride(t *testing.T) {
	cfg := &configpb.Config{}
	if _, err := buildReviewAdapter(cfg, "definitely-not-an-adapter", ""); err == nil {
		t.Errorf("expected error for unknown --review value")
	}
}

func TestReview_ConfigPostFlags(t *testing.T) {
	// Drive --config + --post through the public argv entry alongside a
	// noop review adapter. Config loading fails on the empty repo (rc=3),
	// so the run never reaches the post step — but the flags parse, which
	// attributes --config and --post without any external side effect.
	headDir := t.TempDir()
	baseDir := t.TempDir()
	rc := Review([]string{
		"--base", baseDir,
		"--repo", headDir,
		"--config", headDir + "/sheaf.textproto",
		"--review", "noop",
		"--post",
	})
	// rc == 2 would be a flag-parse error; we expect the later config-load
	// failure (rc=3) instead, proving the flags landed.
	if rc == 2 {
		t.Errorf("flag parse failed for review --config/--post; rc=2")
	}
}
