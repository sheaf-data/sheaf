package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/grounding"
)

// The concept-docs lens requires --library (or --from-grounding); without
// either, fail fast (exit 2) before touching the grounding pipeline.
func TestRunConceptDocsReport_RequiresLibrary(t *testing.T) {
	var out, errOut bytes.Buffer
	rc := runConceptDocsReport(&out, &errOut, "", ".", "", "", nil, "", "", "", nil)
	if rc != 2 {
		t.Errorf("rc = %d, want 2 when --library is missing", rc)
	}
	if !strings.Contains(errOut.String(), "is required") {
		t.Errorf("expected a --library guard message; got %q", errOut.String())
	}
}

func TestDocGlobFlag_Repeatable(t *testing.T) {
	var d docGlobFlag
	_ = d.Set("docs/a/**/*.md")
	_ = d.Set("docs/b/**/*.md")
	if len(d) != 2 || d[0] != "docs/a/**/*.md" || d[1] != "docs/b/**/*.md" {
		t.Errorf("docGlobFlag did not accumulate repeated values: %v", d)
	}
	if d.String() != "docs/a/**/*.md,docs/b/**/*.md" {
		t.Errorf("String() = %q", d.String())
	}
}

// --from-grounding with several JSON inputs renders one multi-library report
// (region axis = library), no scan needed.
func TestRunConceptDocsReport_FromGroundingMultiLib(t *testing.T) {
	dir := t.TempDir()
	write := func(name, lib string) string {
		rep := grounding.Report{
			Library: lib,
			Summary: grounding.Summary{ElementsTotal: 1, ElementsGrounded: 1},
			Elements: []grounding.Element{
				{ElementID: lib + "/Foo", Display: "Foo", Kind: "TYPE", State: grounding.StateGrounded},
			},
		}
		b, err := json.Marshal(rep)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}
	a := write("a.json", "fuchsia.io")
	b := write("b.json", "fuchsia.ui.gfx")
	out := filepath.Join(dir, "combined.html")

	var so, se bytes.Buffer
	rc := runConceptDocsReport(&so, &se, "", ".", "", "fuchsia.*", nil, "", "", out, []string{a, b})
	if rc != 0 {
		t.Fatalf("rc = %d; stderr = %s", rc, se.String())
	}
	html, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	s := string(html)
	if !strings.Contains(s, "fuchsia.io") || !strings.Contains(s, "fuchsia.ui.gfx") {
		t.Errorf("combined report should name both library regions")
	}
	if !strings.Contains(s, "grouped by library") {
		t.Errorf("multi-library report should group the silent set by library")
	}
}
