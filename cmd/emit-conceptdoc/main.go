// emit-conceptdoc runs the concept-doc ingestion engine in-process against
// a repo + sheaf config and writes the docs.concepts coverage JSON — the
// new, additive narrative-doc coverage surface.
//
// It is the deterministic backend half of the concept-docs feature: no LLM,
// no server. It loads the config, runs the scan pipeline for the contract
// corpus, walks the configured narrative docs, attributes each in-scope
// element to the docs that ANCHOR it (qualified/backticked name, resolving
// link, or defined-term — bare prose collisions do NOT attribute), and
// emits the docs.concepts surface (per-element coverage + per-library
// rollup + the flat anchored DocClaim list).
//
// It is DISTINCT from the FIDL `///` reference docs that feed the existing
// "Concept" tile — this is hand-written narrative coverage, routed into the
// additive docs.concepts surface only.
//
// Usage:
//
//	emit-conceptdoc --config <sheaf.textproto> --repo <root> --library <name>
//	                [--doc-glob '<glob>' ...] [--doc-exclude '<glob>' ...]
//	                [--suppress <.sheafignore>] [-o <out.json>]
//
// The driver-framework example has no markdown doc_parser, so pass the
// narrative-doc globs explicitly, e.g.:
//
//	emit-conceptdoc \
//	  --config docs/examples/fuchsia-coverage-configs/drivers-framework.textproto \
//	  --repo /Volumes/T7/fuchsia --library fuchsia.driver.framework \
//	  --doc-glob 'docs/concepts/drivers/**' \
//	  --doc-glob 'docs/development/drivers/**' \
//	  -o docs/grounding/samples/drivers.conceptdoc.json
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters/conceptdoc"
)

// multiFlag collects repeated glob values.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("emit-conceptdoc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath   string
		repoRoot     string
		library      string
		libraryLabel string
		out          string
		suppress     string
		docGlobs     multiFlag
		docExcludes  multiFlag
		quiet        bool
	)
	fs.StringVar(&configPath, "config", "", "Path to the sheaf .textproto config (required)")
	fs.StringVar(&repoRoot, "repo", ".", "Repo root to scan")
	fs.StringVar(&library, "library", "", "Library to report on (required)")
	fs.StringVar(&libraryLabel, "library-label", "", "Display label for the library (default: config project display_name)")
	fs.StringVar(&out, "o", "", "Output JSON path (default: stdout)")
	fs.StringVar(&suppress, "suppress", "", "Path to a .sheafignore-style suppression file (optional)")
	fs.Var(&docGlobs, "doc-glob", "Narrative-doc include glob (repeatable). Overrides config doc_parser includes.")
	fs.Var(&docExcludes, "doc-exclude", "Narrative-doc exclude glob (repeatable).")
	fs.BoolVar(&quiet, "quiet", false, "Suppress the progress summary on stderr")
	fs.Usage = func() {
		fmt.Fprint(stderr, "emit-conceptdoc — write the docs.concepts (narrative concept-doc) coverage JSON.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if configPath == "" || library == "" {
		fmt.Fprintln(stderr, "emit-conceptdoc: --config and --library are required")
		return 2
	}

	res, err := conceptdoc.BuildConfig(context.Background(), configPath, repoRoot, library, libraryLabel, docGlobs, docExcludes, suppress)
	if err != nil {
		fmt.Fprintf(stderr, "emit-conceptdoc: %v\n", err)
		return 1
	}

	if out == "" {
		if err := conceptdoc.Emit(stdout, res); err != nil {
			fmt.Fprintf(stderr, "emit-conceptdoc: %v\n", err)
			return 1
		}
	} else if err := conceptdoc.EmitFile(out, res); err != nil {
		fmt.Fprintf(stderr, "emit-conceptdoc: %v\n", err)
		return 1
	}

	if !quiet {
		s := res.Summary
		fmt.Fprintf(stderr,
			"emit-conceptdoc: %s — docs.concepts: %d/%d elements covered (%d%%) across %d docs; %d anchored claims\n",
			s.Library, s.ElementsCovered, s.ElementsTotal, s.ElementsPct, s.DocsScanned, s.ClaimsTotal)
		if out != "" {
			fmt.Fprintf(stderr, "emit-conceptdoc: wrote %s\n", out)
		}
	}
	return 0
}
