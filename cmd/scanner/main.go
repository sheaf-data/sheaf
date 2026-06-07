// scanner — connect to a running sheaf MCP server, pick a library,
// and write a self-contained HTML report per
// docs/scanner/sheaf-report-generator-requirements.md.
//
// Usage:
//
//	scanner [--server URL] [--token-env VAR] [--library NAME]
//	        [--ecosystem ID] [--list] [-o PATH]
//
// With no --library, the user is shown a numbered menu of libraries
// pulled from the MCP and prompted to select one. With --library,
// the picker is skipped and the report is generated immediately.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

const defaultServer = "http://127.0.0.1:7700"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scanner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		server            string
		tokenEnv          string
		library           string
		ecosystem         string
		apiLevel          string
		listOnly          bool
		out               string
		quiet             bool
		sourceURLTemplate string
	)
	fs.StringVar(&server, "server", defaultServer, "Sheaf MCP server URL (http://host:port)")
	fs.StringVar(&tokenEnv, "token-env", "", "Env var holding the bearer token (if the server requires auth)")
	fs.StringVar(&library, "library", "", "Library name to report on (skip the interactive picker). Accepts a comma-separated list to roll multiple libraries into one report; the rendered Library label is set from --library-label or auto-joined.")
	var libraryLabel string
	fs.StringVar(&libraryLabel, "library-label", "", "Library label to show on a multi-library report. Default: comma-joined input. Ignored when --library names one library.")
	fs.StringVar(&ecosystem, "ecosystem", "fidl", "Ecosystem id (rendering shape): fidl | cli | proto | cpp")
	fs.StringVar(&apiLevel, "api-level", "HEAD", "Target API level for removed-in-the-past detection. Numeric (e.g. 27), HEAD (default — dev tip), or NEXT.")
	fs.BoolVar(&listOnly, "list", false, "List libraries on the server and exit")
	fs.StringVar(&out, "o", "", "Output HTML path (default: <library>-report.html in the cwd)")
	fs.BoolVar(&quiet, "quiet", false, "Suppress chatty progress output")
	var repoRoot string
	var headerStyle string
	fs.StringVar(&sourceURLTemplate, "source-url-template", "",
		"URL pattern that turns path:line labels into clickable links. Placeholders: {path}, {abs_path}, {line}. "+
			"Examples: https://github.com/grpc/grpc/blob/master/{path}#L{line} | "+
			"vscode://file/{abs_path}:{line} | cursor://file/{abs_path}:{line} | file://{abs_path}")
	fs.StringVar(&repoRoot, "repo-root", "",
		"Absolute path of the scanned repo root. Required only when --source-url-template uses {abs_path}.")
	fs.StringVar(&headerStyle, "header-style", "full",
		"Masthead layout: full (5-KPI numtable, default), hero (single % bridged tile), minimal (one-line identification strip, no tiles).")
	var commit string
	fs.StringVar(&commit, "commit", "",
		"Optional short git hash of the scanned repo. Rendered in the minimal/hero header strip. Caller's responsibility to provide.")
	var conceptDocsHref string
	fs.StringVar(&conceptDocsHref, "concept-docs-href", "",
		"Relative href to this library's Concept Docs report (the doc-centric clear/ambiguous/silent sibling). "+
			"When set, the coverage report's masthead links to it; empty omits the link.")
	var mockOverlap bool
	fs.BoolVar(&mockOverlap, "mock-overlap", false,
		"For design iteration only: replace the Overlap (UpSet) row data with a richer fictional set so the visualization can be evaluated on libraries whose real distribution is uninteresting.")
	var fromSnapshot string
	fs.StringVar(&fromSnapshot, "from-snapshot", "",
		"Path to a previously-saved Snapshot JSON (e.g. the one written by --snapshot-out on an earlier run). "+
			"When set, the scanner skips the MCP server connect entirely and renders directly from the file. "+
			"Useful for re-rendering after a template change without standing the server back up.")
	var snapshotOut string
	fs.StringVar(&snapshotOut, "snapshot-out", "",
		"DEPRECATED: use `sheaf snapshot` to produce snapshots in-process (no server). "+
			"Still works: also writes the fetched Snapshot here as JSON for later --from-snapshot replay.")
	fs.Usage = func() {
		fmt.Fprint(stderr, `scanner — generate a Sheaf fragmentation report from a running MCP server.

Usage:
  scanner [flags]

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(stderr, `
Examples:
  scanner                              # interactive picker against localhost:7700
  scanner --library fuchsia.io         # skip picker
  scanner --list                       # show libraries and exit
  scanner --server http://10.0.0.4:7700 --token-env SHEAF_TOKEN

  # Produce a snapshot in-process (no server), then render offline:
  sheaf snapshot --library docker --out docker-snap.json
  scanner --from-snapshot docker-snap.json -o docker.html
`)
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var snap *scanner.Snapshot
	if fromSnapshot != "" {
		// Offline path — skip the server entirely. The load + schema
		// check + build + lag + render is the exact core shared with
		// `sheaf render --from-snapshot`, so the two can't diverge.
		if !quiet {
			fmt.Fprintf(stderr, "scanner: loading snapshot from %s ...\n", fromSnapshot)
		}
		r, code, err := scanner.RenderSnapshotFileReport(fromSnapshot, out, library, ecosystem,
			sourceURLTemplate, repoRoot, commit, apiLevel, headerStyle, conceptDocsHref, mockOverlap, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "scanner: %v\n", err)
			return code
		}
		outPath := out
		if outPath == "" {
			outPath = filepath.Join(".", safeFilename(r.Library)+"-report.html")
		}
		suffix := ""
		if r.RemovedCount > 0 {
			suffix = fmt.Sprintf(" (excluded %d removed @ API %s)", r.RemovedCount, r.TargetAPILevel)
		}
		fmt.Fprintf(stdout, "wrote %s — %d %s, %d bridged, %d%% substantive%s\n",
			outPath, r.Total, r.NounPlural, r.Bridged, r.SubstantivePct, suffix)
		return 0
	} else {
		// Server-backed path.
		c := scanner.NewClient(server, tokenEnv)
		if !quiet {
			fmt.Fprintf(stderr, "scanner: connecting to %s ...\n", c.URL)
		}
		health, err := c.Health()
		if err != nil {
			fmt.Fprintf(stderr, "scanner: %v\n", err)
			return 3
		}
		if !quiet {
			fmt.Fprintf(stderr, "scanner: server ok — %d elements, %d profiles, %d findings\n",
				intFromAny(health["elements"]), intFromAny(health["profiles"]), intFromAny(health["findings"]))
		}

		libs, err := c.ListLibraries()
		if err != nil {
			fmt.Fprintf(stderr, "scanner: list_libraries: %v\n", err)
			return 3
		}
		if len(libs) == 0 {
			fmt.Fprintln(stderr, "scanner: server reports zero libraries — nothing to do")
			return 1
		}

		if listOnly {
			printLibraries(stdout, libs)
			return 0
		}

		if library == "" {
			picked, err := pickLibrary(stdin, stdout, libs)
			if err != nil {
				fmt.Fprintf(stderr, "scanner: %v\n", err)
				return 1
			}
			library = picked
		}
		names := splitLibraryList(library)
		for _, name := range names {
			if !libraryExists(libs, name) {
				fmt.Fprintf(stderr, "scanner: library %q not found on server (use --list to see options)\n", name)
				return 1
			}
		}
		var merged *scanner.Snapshot
		for _, name := range names {
			if !quiet {
				fmt.Fprintf(stderr, "scanner: fetching library_snapshot for %s ...\n", name)
			}
			s, err := c.LibrarySnapshot(name)
			if err != nil {
				fmt.Fprintf(stderr, "scanner: %v\n", err)
				return 3
			}
			if merged == nil {
				merged = s
			} else {
				merged.Elements = append(merged.Elements, s.Elements...)
				merged.Profiles = append(merged.Profiles, s.Profiles...)
				merged.Findings = append(merged.Findings, s.Findings...)
			}
		}
		if len(names) > 1 {
			if libraryLabel != "" {
				merged.Library = libraryLabel
			} else {
				merged.Library = strings.Join(names, ",")
			}
			library = merged.Library
		}
		snap = merged
		if snapshotOut != "" {
			fmt.Fprintln(stderr, "scanner: WARNING — --snapshot-out is deprecated; produce snapshots in-process with `sheaf snapshot --out <path>` (no server needed)")
			data, err := json.MarshalIndent(snap, "", "  ")
			if err == nil {
				if werr := os.WriteFile(snapshotOut, data, 0o644); werr != nil {
					fmt.Fprintf(stderr, "scanner: WARNING — couldn't write --snapshot-out %s: %v\n", snapshotOut, werr)
				} else if !quiet {
					fmt.Fprintf(stderr, "scanner: wrote snapshot to %s (%d bytes)\n", snapshotOut, len(data))
				}
			}
		}
		if !quiet {
			fmt.Fprintf(stderr, "scanner: building report (%d elements, %d profiles, %d findings)\n",
				len(snap.Elements), len(snap.Profiles), len(snap.Findings))
		}
	}

	absRepoRoot := ""
	if repoRoot != "" {
		if abs, err := filepath.Abs(repoRoot); err == nil {
			absRepoRoot = abs
		} else {
			fmt.Fprintf(stderr, "scanner: --repo-root %q is not a usable path: %v\n", repoRoot, err)
			return 3
		}
	}
	r := scanner.BuildReportWithOptions(snap, ecosystem, time.Now().UTC().Format("2006-01-02 15:04 UTC"), apiLevel, sourceURLTemplate, absRepoRoot, headerStyle, commit)
	if mockOverlap {
		r.Overlap = scanner.MockOverlap()
	}
	if conceptDocsHref != "" {
		r.ConceptDocsHref = conceptDocsHref
	}

	if out == "" {
		out = filepath.Join(".", safeFilename(library)+"-report.html")
	}
	f, err := os.Create(out)
	if err != nil {
		fmt.Fprintf(stderr, "scanner: create %s: %v\n", out, err)
		return 3
	}
	defer f.Close()
	if err := scanner.RenderHTML(f, r); err != nil {
		fmt.Fprintf(stderr, "scanner: render: %v\n", err)
		return 3
	}
	suffix := ""
	if r.RemovedCount > 0 {
		suffix = fmt.Sprintf(" (excluded %d removed @ API %s)", r.RemovedCount, r.TargetAPILevel)
	}
	fmt.Fprintf(stdout, "wrote %s — %d %s, %d bridged, %d%% substantive%s\n",
		out, r.Total, r.NounPlural, r.Bridged, r.SubstantivePct, suffix)
	return 0
}

func printLibraries(w io.Writer, libs []scanner.LibraryEntry) {
	fmt.Fprintf(w, "%-30s %10s %10s %10s\n", "LIBRARY", "ELEMENTS", "PROFILES", "FINDINGS")
	for _, l := range libs {
		fmt.Fprintf(w, "%-30s %10d %10d %10d\n", l.Library, l.Elements, l.Profiles, l.Findings)
	}
}

// splitLibraryList parses a comma-separated --library value. Empty
// entries are dropped; surrounding whitespace is trimmed. A
// single-name input round-trips as a one-element slice.
func splitLibraryList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// pickLibrary shows a numbered menu and reads the user's choice. The
// selection can be either the number (1-based) or the literal name.
func pickLibrary(stdin io.Reader, stdout io.Writer, libs []scanner.LibraryEntry) (string, error) {
	fmt.Fprintln(stdout, "Available libraries:")
	for i, l := range libs {
		fmt.Fprintf(stdout, "  [%d] %-30s  %d elements · %d profiles · %d findings\n",
			i+1, l.Library, l.Elements, l.Profiles, l.Findings)
	}
	fmt.Fprint(stdout, "\nSelect a library (number or name): ")
	r := bufio.NewReader(stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read selection: %w", err)
	}
	choice := strings.TrimSpace(line)
	if choice == "" {
		return "", fmt.Errorf("no selection")
	}
	if n, err := strconv.Atoi(choice); err == nil {
		if n < 1 || n > len(libs) {
			return "", fmt.Errorf("selection %d out of range (1..%d)", n, len(libs))
		}
		return libs[n-1].Library, nil
	}
	if libraryExists(libs, choice) {
		return choice, nil
	}
	return "", fmt.Errorf("no library named %q", choice)
}

func libraryExists(libs []scanner.LibraryEntry, name string) bool {
	for _, l := range libs {
		if l.Library == name {
			return true
		}
	}
	return false
}

// safeFilename keeps a usable suffix for file-on-disk output even
// when the library has slashes or odd characters.
func safeFilename(s string) string {
	repl := strings.NewReplacer("/", "_", " ", "_", string(os.PathSeparator), "_")
	return repl.Replace(s)
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
