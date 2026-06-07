package cli

import (
	"bytes"
	"testing"
)

// Tests for `sheaf coverage`.

func TestRunCoverage_UnknownElement(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runCoverage(&out, &errOut, "", dir, "definitely-not-a-real-element", "text")
	if rc != 2 {
		t.Errorf("expected rc=2 for unknown element; got %d; stderr=%s", rc, errOut.String())
	}
}

func TestRunCoverage_FormatRepoFlags(t *testing.T) {
	// --format json with a bogus --element still resolves the
	// pipeline and prints the no-profile error. Path coverage check
	// for the --repo + --format flag attribution.
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runCoverage(&out, &errOut, "", dir, "nope-not-real", "json")
	if rc != 2 {
		t.Errorf("expected rc=2; got %d", rc)
	}
}

func TestRunCoverage_MissingConfig(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runCoverage(&out, &errOut, "", dir, "anything", "text")
	if rc == 0 {
		t.Errorf("expected non-zero exit on missing config; out=%s err=%s", out.String(), errOut.String())
	}
}

func TestCoverage_RequiresElement(t *testing.T) {
	// Empty --element exits 2 from the public argv entry with the
	// required-flag message, before runCoverage is ever reached.
	if rc := Coverage([]string{"--repo", t.TempDir()}); rc != 2 {
		t.Errorf("expected rc=2 when --element is omitted; got %d", rc)
	}
}

func TestCoverage_ArgvFlagParsing(t *testing.T) {
	// Drive the public argv entry so --config / --repo / --element /
	// --format are parsed from the command line. The runCoverage tests
	// above pass those values positionally, which never exercises the
	// flag wiring (nor attributes the flags in the self-scan); this
	// closes both gaps in one real end-to-end parse.
	dir := setupFixture(t)
	rc := Coverage([]string{
		"--config", dir + "/sheaf.textproto",
		"--repo", dir,
		"--element", "nope-not-real",
		"--format", "json",
	})
	if rc != 2 {
		t.Errorf("expected rc=2 for unknown --element via argv; got %d", rc)
	}
}
