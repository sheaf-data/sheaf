// emit-grounding runs the mechanical Grounding detector in-process against
// a repo + sheaf config and writes the GroundingReport JSON (the shape the
// Grounding UI renders, per docs/grounding/grounding.fixture.json).
//
// It is the deterministic backend half of the Grounding feature: no LLM,
// no server. It loads the config, runs the scan pipeline for the contract
// corpus, scans the configured concept docs for collisions with that
// contract surface, classifies each reference grounded/guessing/ungrounded
// with a recorded anchor audit trail, and emits the report.
//
// Usage:
//
//	emit-grounding --config <sheaf.textproto> --repo <root> --library <name>
//	               [--doc-glob '<glob>' ...] [--suppress <.sheafignore>]
//	               [-o <out.json>]
//
// The driver-framework example has no doc_parser, so pass concept-doc
// globs explicitly, e.g.:
//
//	emit-grounding \
//	  --config docs/examples/fuchsia-driver-framework-coverage-config.textproto \
//	  --repo /Volumes/T7/fuchsia --library fuchsia.driver.framework \
//	  --doc-glob 'docs/concepts/drivers/**/*.md' \
//	  -o docs/grounding/samples/drivers.grounding.json
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sheaf-data/sheaf/internal/grounding"
)

// multiFlag collects repeated --doc-glob values.
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
	fs := flag.NewFlagSet("emit-grounding", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath   string
		repoRoot     string
		library      string
		libraryLabel string
		out          string
		suppress     string
		docGlobs     multiFlag
		quiet        bool
	)
	fs.StringVar(&configPath, "config", "", "Path to the sheaf .textproto config (required)")
	fs.StringVar(&repoRoot, "repo", ".", "Repo root to scan")
	fs.StringVar(&library, "library", "", "Library to report on (required)")
	fs.StringVar(&libraryLabel, "library-label", "", "Display label for the library (default: config project display_name)")
	fs.StringVar(&out, "o", "", "Output JSON path (default: stdout)")
	fs.StringVar(&suppress, "suppress", "", "Path to a .sheafignore-style suppression file (optional)")
	fs.Var(&docGlobs, "doc-glob", "Concept-doc include glob (repeatable). Overrides config doc_parser includes.")
	fs.BoolVar(&quiet, "quiet", false, "Suppress the progress summary on stderr")
	fs.Usage = func() {
		fmt.Fprint(stderr, "emit-grounding — write the mechanical Grounding report JSON.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if configPath == "" || library == "" {
		fmt.Fprintln(stderr, "emit-grounding: --config and --library are required")
		return 2
	}

	rep, err := grounding.BuildConfig(context.Background(), configPath, repoRoot, library, libraryLabel, docGlobs, suppress)
	if err != nil {
		fmt.Fprintf(stderr, "emit-grounding: %v\n", err)
		return 1
	}

	if out == "" {
		if err := grounding.Emit(stdout, rep); err != nil {
			fmt.Fprintf(stderr, "emit-grounding: %v\n", err)
			return 1
		}
	} else if err := grounding.EmitFile(out, rep); err != nil {
		fmt.Fprintf(stderr, "emit-grounding: %v\n", err)
		return 1
	}

	if !quiet {
		s := rep.Summary
		fmt.Fprintf(stderr,
			"emit-grounding: %s — %d elements (%d mentioned), %d references: %d grounded / %d guessing / %d ungrounded; forces_a_guess=%d\n",
			rep.Library, s.ElementsTotal, s.ElementsMentioned, s.ReferencesTotal,
			s.ReferencesGrounded, s.ReferencesGuessing, s.ReferencesUngrounded, s.ForcesAGuess)
		if out != "" {
			fmt.Fprintf(stderr, "emit-grounding: wrote %s\n", out)
		}
	}
	return 0
}
