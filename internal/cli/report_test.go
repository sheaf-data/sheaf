package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for `sheaf report`.

func TestRunReport_CSVFormat(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runReport(&out, &errOut, "", dir, "csv", "")
	if rc != 0 {
		t.Fatalf("runReport returned %d; stderr=%s", rc, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "element_id,tests,docs,examples,missing") {
		t.Errorf("expected CSV header at start; got %q", firstLine(out.String()))
	}
}

func TestRunReport_HTMLRequiresOutput(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runReport(&out, &errOut, "", dir, "html", "")
	if rc != 2 {
		t.Errorf("expected rc=2 when --format html lacks --output; got %d", rc)
	}
}

func TestRunReport_HTMLWritesIndex(t *testing.T) {
	dir := setupFixture(t)
	outDir := filepath.Join(t.TempDir(), "report")
	var out, errOut bytes.Buffer
	rc := runReport(&out, &errOut, "", dir, "html", outDir)
	if rc != 0 {
		t.Fatalf("runReport(html) returned %d; stderr=%s", rc, errOut.String())
	}
	if !strings.Contains(out.String(), "HTML report written") {
		t.Errorf("expected success line; got %q", out.String())
	}
}

func TestRunReport_RepoConfigFlags(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runReport(&out, &errOut, dir+"/sheaf.textproto", dir, "csv", "")
	if rc != 0 {
		t.Fatalf("runReport(--config,--repo) returned %d", rc)
	}
}

func TestRunReport_UnknownFormat(t *testing.T) {
	dir := setupFixture(t)
	var out, errOut bytes.Buffer
	rc := runReport(&out, &errOut, "", dir, "yaml", "")
	if rc != 2 {
		t.Errorf("expected rc=2 for unknown format; got %d", rc)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
