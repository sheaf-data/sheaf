package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// Render runs `sheaf render` — it loads a previously-saved Snapshot JSON
// (the shape `sheaf snapshot` and the MCP server's library_snapshot op
// emit) and renders the canonical self-contained HTML report from it,
// entirely in-process: no server connect, no rescan. It is the in-binary
// replacement for `scanner --from-snapshot`, sharing the exact same core
// (scanner.RenderSnapshotFileReport) so the two paths can't diverge.
//
// When --repo-root points at a real git working tree, the report's Lag
// section is computed and rendered — the headline reason to render a
// snapshot against a checkout rather than the bare snapshot alone.
func Render(args []string) int {
	return runRender(os.Stdout, os.Stderr, args)
}

func runRender(stdout, stderr *os.File, args []string) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		fromSnapshot      string
		library           string
		ecosystem         string
		apiLevel          string
		out               string
		quiet             bool
		sourceURLTemplate string
		repoRoot          string
		headerStyle       string
		commit            string
		conceptDocsHref   string
		mockOverlap       bool
	)
	fs.StringVar(&fromSnapshot, "from-snapshot", "",
		"Path to a Snapshot JSON (e.g. from `sheaf snapshot --out`). Rendered directly, no server.")
	fs.StringVar(&library, "library", "", "Library label for the report. Defaults to the snapshot's own library field.")
	fs.StringVar(&ecosystem, "ecosystem", "fidl", "Ecosystem id (rendering shape): fidl | cli | proto | cpp | openapi | ...")
	fs.StringVar(&apiLevel, "api-level", "HEAD", "Target API level for removed-in-the-past detection. Numeric (e.g. 27), HEAD (default — dev tip), or NEXT.")
	fs.StringVar(&out, "o", "", "Output HTML path (default: <library>-report.html in the cwd)")
	fs.StringVar(&out, "output", "", "Alias for -o.")
	fs.BoolVar(&quiet, "quiet", false, "Suppress chatty progress output")
	fs.StringVar(&sourceURLTemplate, "source-url-template", "",
		"URL pattern that turns path:line labels into clickable links. Placeholders: {path}, {abs_path}, {line}. "+
			"Examples: https://github.com/grpc/grpc/blob/master/{path}#L{line} | "+
			"vscode://file/{abs_path}:{line} | cursor://file/{abs_path}:{line} | file://{abs_path}")
	fs.StringVar(&repoRoot, "repo-root", "",
		"Absolute path of the scanned repo root. Required for {abs_path} links; also enables the Lag section "+
			"when it points at a git working tree.")
	fs.StringVar(&headerStyle, "header-style", "full",
		"Masthead layout: full (5-KPI numtable, default), hero (single % bridged tile), minimal (one-line identification strip, no tiles).")
	fs.StringVar(&commit, "commit", "",
		"Optional short git hash of the scanned repo. Rendered in the minimal/hero header strip. Caller's responsibility to provide.")
	fs.StringVar(&conceptDocsHref, "concept-docs-href", "",
		"Relative href to this library's Concept Docs report (the doc-centric clear/ambiguous/silent sibling). "+
			"When set, the coverage report's concept-doc reach line links to it; empty omits the link.")
	fs.BoolVar(&mockOverlap, "mock-overlap", false,
		"For design iteration only: replace the Overlap (UpSet) row data with a richer fictional set.")
	fs.Usage = func() {
		fmt.Fprint(stderr, `sheaf render — render a Sheaf coverage report from a saved Snapshot JSON.

Usage:
  sheaf render --from-snapshot <file.json> [flags]

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(stderr, `
Example:
  sheaf snapshot --library docker --out docker-snap.json
  sheaf render --from-snapshot docker-snap.json --ecosystem cli \
    --repo-root /path/to/docker -o docker.html
`)
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fromSnapshot == "" {
		fmt.Fprintln(stderr, "sheaf render: --from-snapshot is required")
		fs.Usage()
		return 2
	}

	// When the caller didn't pass an explicit --commit but did point at a
	// repo root, derive the scanned repo's short HEAD from it — mirroring
	// what the multi-library fanout already does via gitShortCommit, and
	// the grounding/concept-docs path does at emit time. Without this, the
	// regen render_one path (render --from-snapshot ... --repo-root, no
	// --commit) leaves commit "" and every coverage report's header drops
	// the sha. gitShortCommit returns "" for a non-git dir, so non-git
	// repo-roots stay empty (deterministic). An explicit --commit always
	// wins — it's never overwritten.
	if commit == "" && repoRoot != "" {
		commit = gitShortCommit(repoRoot)
	}

	if !quiet {
		fmt.Fprintf(stderr, "sheaf render: loading snapshot from %s ...\n", fromSnapshot)
	}
	r, code, err := scanner.RenderSnapshotFileReport(fromSnapshot, out, library, ecosystem,
		sourceURLTemplate, repoRoot, commit, apiLevel, headerStyle, conceptDocsHref, mockOverlap, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf render: %v\n", err)
		return code
	}
	outPath := out
	if outPath == "" {
		outPath = filepath.Join(".", safeRenderFilename(r.Library)+"-report.html")
	}
	suffix := ""
	if r.RemovedCount > 0 {
		suffix = fmt.Sprintf(" (excluded %d removed @ API %s)", r.RemovedCount, r.TargetAPILevel)
	}
	fmt.Fprintf(stdout, "wrote %s — %d %s, %d bridged, %d%% substantive%s\n",
		outPath, r.Total, r.NounPlural, r.Bridged, r.SubstantivePct, suffix)
	return 0
}

// safeRenderFilename keeps a usable suffix for file-on-disk output even
// when the library has slashes or odd characters. Mirrors the scanner
// CLI / utils/scanner helpers of the same intent.
func safeRenderFilename(s string) string {
	repl := strings.NewReplacer("/", "_", " ", "_", string(os.PathSeparator), "_")
	return repl.Replace(s)
}
