package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for `sheaf doctor`. Lives in its own file so path-based
// attribution lands these on the `sheaf doctor` ContractElement.

func TestRunDoctor_FixtureProject(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runDoctor(&out, &errOut, "", dir)
	if rc != 0 {
		t.Fatalf("runDoctor returned %d; stderr=%s", rc, errOut.String())
	}
	got := out.String()
	for _, want := range []string{"[OK]", "gtest", "markdown", "Project: demo"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n%s", want, got)
		}
	}
}

func TestRunDoctor_RulesMissingIsTolerated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sheaf.textproto"), []byte(fixtureConfig), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out, errOut bytes.Buffer
	rc := runDoctor(&out, &errOut, "", dir)
	if rc != 0 {
		t.Fatalf("runDoctor returned %d; stderr=%s", rc, errOut.String())
	}
	if !strings.Contains(out.String(), "[MISSING]") {
		t.Errorf("expected [MISSING] marker; got %s", out.String())
	}
}

func TestRunDoctor_RepoFlag(t *testing.T) {
	// Doctor's --repo flag drives the config + rules path resolution.
	// Confirm a non-default --repo still finds the fixture's
	// sheaf.textproto.
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runDoctor(&out, &errOut, "", dir)
	if rc != 0 {
		t.Fatalf("runDoctor(--repo) returned %d; stderr=%s", rc, errOut.String())
	}
}

func TestRunDoctor_MissingConfig(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runDoctor(&out, &errOut, "", dir)
	if rc == 0 {
		t.Errorf("expected non-zero exit on missing config; out=%s err=%s", out.String(), errOut.String())
	}
}

func TestDoctor_ArgvFlagParsing(t *testing.T) {
	// Drive the public argv entry so --config and --repo are parsed
	// from the command line (the runDoctor tests pass them positionally,
	// which neither exercises flag wiring nor attributes --config).
	dir := setupFixture(t)
	if rc := Doctor([]string{"--config", dir + "/sheaf.textproto", "--repo", dir}); rc != 0 {
		t.Errorf("Doctor(--config,--repo) returned %d; want 0", rc)
	}
}
