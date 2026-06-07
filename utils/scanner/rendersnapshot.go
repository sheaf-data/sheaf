// Offline snapshot render path. RenderSnapshotFile loads a previously
// saved Snapshot JSON (the shape `sheaf snapshot` and the MCP server's
// library_snapshot op emit) and renders the canonical HTML report from
// it — no server connect, no rescan. It is the shared core behind both
// `scanner --from-snapshot` and `sheaf render --from-snapshot`, so the
// two paths can never diverge. Unlike the bare BuildReportWithOptions
// call the scanner CLI historically made on its own, this helper also
// computes lag against the supplied repo root, so a snapshot rendered
// against a real git working tree gets its Lag section.
package scanner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sheaf-data/sheaf/internal/librarysnapshot"
)

// RenderSnapshotFile loads the Snapshot JSON at in, builds the report,
// computes lag when repoRoot is non-empty, and writes the HTML to out.
//
// It returns a process exit code and an error. The code mirrors the
// scanner CLI's historical exits: 0 on success, 2 on a usage/schema
// problem (a hard schema-version mismatch, or a snapshot with no
// library and no --library override), 3 on an IO/render failure.
// Progress and warnings are written to stderr; the helper does not
// touch stdout (callers print their own "wrote …" summary from the
// returned ReportData if they want one — see RenderSnapshotFileReport).
func RenderSnapshotFile(in, out, library, ecosystem, sourceURLTemplate, repoRoot, commit, apiLevel, headerStyle, conceptDocsHref string, mockOverlap bool, stderr io.Writer) (int, error) {
	_, code, err := RenderSnapshotFileReport(in, out, library, ecosystem, sourceURLTemplate, repoRoot, commit, apiLevel, headerStyle, conceptDocsHref, mockOverlap, stderr)
	return code, err
}

// RenderSnapshotFileReport is RenderSnapshotFile but also returns the
// built *ReportData so callers can print a summary line. On a non-zero
// exit the *ReportData is nil.
func RenderSnapshotFileReport(in, out, library, ecosystem, sourceURLTemplate, repoRoot, commit, apiLevel, headerStyle, conceptDocsHref string, mockOverlap bool, stderr io.Writer) (*ReportData, int, error) {
	data, err := os.ReadFile(in)
	if err != nil {
		return nil, 3, fmt.Errorf("read %s: %w", in, err)
	}
	var loaded Snapshot
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, 3, fmt.Errorf("parse snapshot %s: %w", in, err)
	}
	snap := &loaded
	// A snapshot is only safe to render with the schema it was written
	// for. Version 0 predates versioning (an older --snapshot-out file):
	// render best-effort with a warning. Any other mismatch is a hard
	// error — the projection shape differs and the report would render
	// wrong without saying so.
	if loaded.SchemaVersion == 0 {
		fmt.Fprintf(stderr, "WARNING — %s has no schema_version (written before versioning); rendering best-effort\n", in)
	} else if loaded.SchemaVersion != librarysnapshot.SchemaVersion {
		return nil, 2, fmt.Errorf("%s has schema_version %d but this build supports %d; regenerate it with `sheaf snapshot`",
			in, loaded.SchemaVersion, librarysnapshot.SchemaVersion)
	}
	if library == "" {
		library = snap.Library
	}
	if library == "" {
		return nil, 2, fmt.Errorf("--from-snapshot file has no library field; pass --library too")
	}

	absRepoRoot := ""
	if repoRoot != "" {
		abs, aerr := filepath.Abs(repoRoot)
		if aerr != nil {
			return nil, 3, fmt.Errorf("--repo-root %q is not a usable path: %w", repoRoot, aerr)
		}
		absRepoRoot = abs
	}
	r := BuildReportWithOptions(snap, ecosystem,
		time.Now().UTC().Format("2006-01-02 15:04 UTC"), apiLevel,
		sourceURLTemplate, absRepoRoot, headerStyle, commit)
	// Doc-lag (the Lag section) and per-element lag buckets are computed
	// inside BuildReportWithOptions now, so the snapshot render path no
	// longer computes them separately.
	if mockOverlap {
		r.Overlap = MockOverlap()
	}
	// The single-report CLI links the coverage report's concept-doc reach
	// line to its sibling concept-docs report when given --concept-docs-href;
	// the fan-out carries the same value per manifest entry via NavContext.
	if conceptDocsHref != "" {
		r.ConceptDocsHref = conceptDocsHref
	}

	if out == "" {
		out = filepath.Join(".", safeRenderFilename(library)+"-report.html")
	}
	f, err := os.Create(out)
	if err != nil {
		return nil, 3, fmt.Errorf("create %s: %w", out, err)
	}
	defer f.Close()
	if err := RenderHTML(f, r); err != nil {
		return nil, 3, fmt.Errorf("render: %w", err)
	}
	return r, 0, nil
}
