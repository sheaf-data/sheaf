package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// protoFixture is a minimal proto3 contract surface. The proto adapter is
// deterministic (no LLM), so autodetect.Detect finds it and the pipeline
// produces a non-empty report fully offline.
const protoFixture = `syntax = "proto3";

package demo;

message Foo {
  string id = 1;
}

service FooService {
  rpc GetFoo(Foo) returns (Foo);
}
`

// writeProtoFixture lays down a tiny detectable repo and returns its root
// plus a sibling output dir.
func writeProtoFixture(t *testing.T) (repo, out string) {
	t.Helper()
	repo = t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "api.proto"), []byte(protoFixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	out = t.TempDir()
	return repo, out
}

// assertArtifacts checks the four --auto artifacts exist on disk.
func assertArtifacts(t *testing.T, out string) {
	t.Helper()
	for _, rel := range []string{
		"sheaf-report.html",
		filepath.Join("report", "index.html"),
		"sheaf.textproto",
		"sheaf-hardening.md",
	} {
		p := filepath.Join(out, rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected artifact %s: %v", rel, err)
		}
	}
}

// TestRunAuto_Offline exercises the deterministic --auto path end to end:
// a bare proto fixture yields a detection, the pipeline runs with no LLM
// call, and all four artifacts land on disk. No network, no API key.
func TestRunAuto_Offline(t *testing.T) {
	repo, out := writeProtoFixture(t)

	var stdout, stderr bytes.Buffer
	code := runAuto(&stdout, &stderr, repo, out, "", "auto", 0, 0, nil, nil)
	if code != 0 {
		t.Fatalf("runAuto exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	assertArtifacts(t, out)
}

// TestRunAuto_NoKeyAnthropic confirms the no-key warn path: with the
// anthropic backend explicitly selected and ANTHROPIC_API_KEY unset, the
// LLM tier just warns and stays empty while the deterministic proto
// adapter still produces all four artifacts. runAuto must still exit 0.
func TestRunAuto_NoKeyAnthropic(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	repo, out := writeProtoFixture(t)

	var stdout, stderr bytes.Buffer
	code := runAuto(&stdout, &stderr, repo, out, "", "anthropic", 0, 0, nil, nil)
	if code != 0 {
		t.Fatalf("runAuto exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	assertArtifacts(t, out)
}
