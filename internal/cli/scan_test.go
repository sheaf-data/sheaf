package cli

import (
	"bytes"
	"strings"
	"testing"
)

// Tests for `sheaf scan`. Kept in a dedicated file so the gotest
// adapter's path-based subcommand attribution lands these on the
// `sheaf scan` ContractElement.

func TestRunScan_FixtureProject(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runScan(&out, &errOut, "", dir, false)
	if rc != 0 {
		t.Fatalf("runScan returned %d; stderr=%s", rc, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"Contract elements: 0",
		"Test cases:        1",
		"Doc claims:",
		"FooTest.BarReturnsTrue",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestRunScan_MissingConfig(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runScan(&out, &errOut, "", dir, false)
	if rc == 0 {
		t.Errorf("expected non-zero exit on missing config; out=%s err=%s", out.String(), errOut.String())
	}
}

func TestRunScan_RepoConfigFlags(t *testing.T) {
	// Exercise the --repo + --config wiring: a config path provided
	// explicitly should be honored even when --repo names a directory
	// that itself contains no sheaf.textproto.
	dir := setupFixture(t)
	otherRepo := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runScan(&out, &errOut, dir+"/sheaf.textproto", otherRepo, true)
	if rc != 0 {
		t.Fatalf("runScan(--config) returned %d; stderr=%s", rc, errOut.String())
	}
}

func TestRunScan_QuietSuppressesSamples(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runScan(&out, &errOut, "", dir, true /* quiet */)
	if rc != 0 {
		t.Fatalf("runScan returned %d; stderr=%s", rc, errOut.String())
	}
	if strings.Contains(out.String(), "Sample tests:") || strings.Contains(out.String(), "Sample doc mentions:") {
		t.Errorf("--quiet did not suppress sample blocks; got %s", out.String())
	}
}

func TestScan_ArgvFlagParsing(t *testing.T) {
	// Drive the public argv entry so --repo and --config are parsed from
	// the command line. The runScan tests name --config in a string only
	// incidentally and --repo only in a comment, so --repo never gets
	// attributed; this exercises the real flag wiring end to end.
	dir := setupFixture(t)
	if rc := Scan([]string{"--config", dir + "/sheaf.textproto", "--repo", dir, "--quiet"}); rc != 0 {
		t.Errorf("Scan(--config,--repo,--quiet) returned %d; want 0", rc)
	}
}
