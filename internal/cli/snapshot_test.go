package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheaf-data/sheaf/internal/librarysnapshot"
)

// Tests for `sheaf snapshot`.

func TestRunSnapshot_RequiresLibrary(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runSnapshot(&out, &errOut, "", dir, "", "", "")
	if rc != 2 {
		t.Errorf("expected rc=2 when --library is empty; got %d", rc)
	}
}

func TestRunSnapshot_StdoutEmitsVersionedJSON(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runSnapshot(&out, &errOut, "", dir, "fuchsia.io", "", "")
	if rc != 0 {
		t.Fatalf("runSnapshot returned %d; stderr=%s", rc, errOut.String())
	}
	var snap struct {
		SchemaVersion int              `json:"schema_version"`
		Library       string           `json:"library"`
		Elements      []map[string]any `json:"elements"`
	}
	if err := json.Unmarshal(out.Bytes(), &snap); err != nil {
		t.Fatalf("snapshot stdout is not valid JSON: %v", err)
	}
	if snap.SchemaVersion != librarysnapshot.SchemaVersion {
		t.Errorf("schema_version = %d; want %d", snap.SchemaVersion, librarysnapshot.SchemaVersion)
	}
	if snap.Library != "fuchsia.io" {
		t.Errorf("library = %q; want %q", snap.Library, "fuchsia.io")
	}
}

func TestRunSnapshot_OutWritesFile(t *testing.T) {
	dir := setupFixture(t)
	outPath := filepath.Join(t.TempDir(), "nested", "snap.json")
	var out, errOut bytes.Buffer
	rc := runSnapshot(&out, &errOut, "", dir, "fuchsia.io", "", outPath)
	if rc != 0 {
		t.Fatalf("runSnapshot(--out) returned %d; stderr=%s", rc, errOut.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("snapshot file not written: %v", err)
	}
	var snap struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot file is not valid JSON: %v", err)
	}
	if snap.SchemaVersion != librarysnapshot.SchemaVersion {
		t.Errorf("schema_version = %d; want %d", snap.SchemaVersion, librarysnapshot.SchemaVersion)
	}
}

func TestRunSnapshot_MissingConfig(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runSnapshot(&out, &errOut, "", dir, "fuchsia.io", "", "")
	if rc == 0 {
		t.Errorf("expected non-zero exit on missing config; out=%s err=%s", out.String(), errOut.String())
	}
}

func TestSnapshot_ArgvFlagParsing(t *testing.T) {
	// Drive the public argv entry so --config / --repo / --library-label
	// are parsed from the command line. The runSnapshot tests pass these
	// positionally, leaving those three flags unattributed in the
	// self-scan even though they're exercised here.
	dir := setupFixture(t)
	outPath := filepath.Join(t.TempDir(), "snap.json")
	rc := Snapshot([]string{
		"--config", dir + "/sheaf.textproto",
		"--repo", dir,
		"--library", "fuchsia.io",
		"--library-label", "demo",
		"--out", outPath,
	})
	if rc != 0 {
		t.Fatalf("Snapshot(--config/--repo/--library/--library-label/--out) returned %d; want 0", rc)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("snapshot via argv didn't write --out file: %v", err)
	}
}
