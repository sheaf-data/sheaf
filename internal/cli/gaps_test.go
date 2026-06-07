package cli

import (
	"bytes"
	"strings"
	"testing"
)

// Tests for `sheaf gaps`.

func TestRunGaps_TextFormat(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runGaps(&out, &errOut, "", dir, "", "", "INFO", "text")
	if rc != 0 {
		t.Fatalf("runGaps returned %d; stderr=%s", rc, errOut.String())
	}
	// The fixture project has no contract surface configured, so the
	// finding list is empty. The text formatter emits the "No findings."
	// sentinel in that case — confirm the success path stays runnable.
	if got := out.String(); !strings.Contains(got, "No findings.") && !strings.Contains(got, "findings:") {
		t.Errorf("expected gaps output; got %q", got)
	}
}

func TestRunGaps_CSVFormat(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runGaps(&out, &errOut, "", dir, "", "", "INFO", "csv")
	if rc != 0 {
		t.Fatalf("runGaps returned %d; stderr=%s", rc, errOut.String())
	}
	if !strings.Contains(out.String(), "id,kind,subject,severity,analyzer,message") {
		t.Errorf("expected CSV header; got %q", out.String())
	}
}

func TestRunGaps_LibraryRepoKindFlags(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runGaps(&out, &errOut, "", dir, "THIN_REFERENCE", "demo", "WARNING", "text")
	if rc != 0 {
		t.Fatalf("runGaps with --library/--repo/--kind returned %d; stderr=%s", rc, errOut.String())
	}
}

func TestRunGaps_MissingConfig(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runGaps(&out, &errOut, "", dir, "", "", "INFO", "text")
	if rc == 0 {
		t.Errorf("expected non-zero exit on missing config; out=%s err=%s", out.String(), errOut.String())
	}
}

func TestParseSeverity(t *testing.T) {
	cases := map[string]string{
		"INFO":    "INFO",
		"WARN":    "WARNING",
		"WARNING": "WARNING",
		"ERROR":   "ERROR",
		"garbage": "INFO",
	}
	for in, want := range cases {
		got := parseSeverity(in)
		if got.String() != want {
			t.Errorf("parseSeverity(%q) = %s; want %s", in, got.String(), want)
		}
	}
}

func TestGaps_ArgvFlagParsing(t *testing.T) {
	// Drive the public argv entry so every gaps flag is parsed from the
	// command line. The runGaps tests pass these positionally, which
	// leaves --config / --format / --severity unattributed in the
	// self-scan even though they're exercised here end to end.
	dir := setupFixture(t)
	rc := Gaps([]string{
		"--config", dir + "/sheaf.textproto",
		"--repo", dir,
		"--severity", "WARNING",
		"--format", "csv",
		"--kind", "THIN_REFERENCE",
		"--library", "demo",
	})
	if rc != 0 {
		t.Errorf("Gaps(--config/--repo/--severity/--format/--kind/--library) returned %d; want 0", rc)
	}
}
