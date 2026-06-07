package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sheaf-data/sheaf/internal/verify"
)

// Verify adversarially checks a scan's top-line numbers against the
// snapshot it was rendered from and (optionally) the repository on disk,
// before a report is shown to anyone. It prints a trust ledger and can
// emit a machine-readable report for an onboarding agent to consume.
func Verify(args []string) int {
	// `sheaf verify summarize` is the second half of the attribution-
	// precision workflow: read agent-verdicted assertions → precision ledger.
	if len(args) > 0 && args[0] == "summarize" {
		return verifySummarize(args[1:])
	}

	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		snapshotPath     string
		configPath       string
		library          string
		repoRoot         string
		ecosystem        string
		threshold        float64
		disk             bool
		checkURLs        bool
		maxDisk          int
		maxAssertions    int
		expectedElements int
		jsonOut          string
		ledgerOut        string
		strict           bool
	)
	fs.StringVar(&snapshotPath, "from-snapshot", "", "Snapshot JSON to verify (produced by `sheaf snapshot`). Provide this or --config.")
	fs.StringVar(&configPath, "config", "", "One-shot: build the snapshot in-process from this sheaf.textproto (with --repo and --library) and verify it, instead of --from-snapshot.")
	fs.StringVar(&library, "library", "", "Library to scan for the --config one-shot path.")
	fs.StringVar(&repoRoot, "repo", "", "Repo root, for disk-oracle checks, source resolution, and the --config one-shot scan.")
	fs.StringVar(&ecosystem, "ecosystem", "", "Ecosystem view id (cli|fidl|proto|cpp|...). Empty uses the scanner default.")
	fs.Float64Var(&threshold, "low-coverage-threshold", 0.15, "Flag any per-tier or pooled coverage at or below this fraction for validation.")
	fs.BoolVar(&disk, "disk", false, "Run the on-disk oracle (TP/FP/FN sampling, doc-URL resolution, ground-truth count). Requires --repo.")
	fs.BoolVar(&checkURLs, "check-urls", false, "With --disk, resolve a bounded sample of published doc URLs (HTTP HEAD/GET) and flag dead links. Network side effect; default off.")
	fs.IntVar(&maxDisk, "max-disk-elements", 0, "Cap how many elements the disk oracle samples (0 = built-in default).")
	fs.IntVar(&maxAssertions, "sample-assertions", 0, "Cap elements sampled into the verify.json `assertions` array for the attribution-precision workflow (0 = built-in default of 50).")
	fs.IntVar(&expectedElements, "expected-elements", -1, "Authoritative element count (from protoc / fidlc --json / the --help tree) to cross-check the denominator against; -1 = unset. Used with --disk.")
	fs.StringVar(&jsonOut, "json", "", "Write the machine-readable verify report here (default: none).")
	fs.StringVar(&ledgerOut, "ledger", "", "Write the human trust ledger here (default: stdout).")
	fs.BoolVar(&strict, "strict", false, "Exit non-zero (3) when the verdict is BROKEN. For CI gating.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return runVerify(os.Stdout, os.Stderr, verifyArgs{
		snapshotPath:     snapshotPath,
		configPath:       configPath,
		library:          library,
		repoRoot:         repoRoot,
		ecosystem:        ecosystem,
		threshold:        threshold,
		disk:             disk,
		checkURLs:        checkURLs,
		maxDisk:          maxDisk,
		maxAssertions:    maxAssertions,
		expectedElements: expectedElements,
		jsonOut:          jsonOut,
		ledgerOut:        ledgerOut,
		strict:           strict,
	})
}

type verifyArgs struct {
	snapshotPath, configPath, library string
	repoRoot, ecosystem               string
	threshold                         float64
	disk                              bool
	checkURLs                         bool
	maxDisk                           int
	maxAssertions                     int
	expectedElements                  int
	jsonOut, ledgerOut                string
	strict                            bool
}

func runVerify(stdout, stderr io.Writer, a verifyArgs) int {
	if a.snapshotPath == "" && a.configPath == "" {
		fmt.Fprintln(stderr, "sheaf verify: --from-snapshot or --config is required.")
		return 2
	}
	if a.snapshotPath == "" && a.library == "" {
		fmt.Fprintln(stderr, "sheaf verify: the --config one-shot requires --library.")
		return 2
	}
	opts := verify.Options{
		SnapshotPath:         a.snapshotPath,
		ConfigPath:           a.configPath,
		Library:              a.library,
		RepoRoot:             a.repoRoot,
		Ecosystem:            a.ecosystem,
		Threshold:            a.threshold,
		DiskChecks:           a.disk,
		MaxDiskElements:      a.maxDisk,
		CheckURLs:            a.checkURLs,
		MaxAssertionElements: a.maxAssertions,
	}
	if a.expectedElements >= 0 {
		v := a.expectedElements
		opts.ExpectedElements = &v
	}
	rep, err := verify.Run(opts)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf verify: %v\n", err)
		return 1
	}

	ledger := verify.RenderLedger(rep)
	if a.ledgerOut != "" {
		if err := os.WriteFile(a.ledgerOut, []byte(ledger), 0o644); err != nil {
			fmt.Fprintf(stderr, "sheaf verify: write ledger: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "trust ledger → %s\n", a.ledgerOut)
	} else {
		fmt.Fprintln(stdout, ledger)
	}

	if a.jsonOut != "" {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "sheaf verify: marshal json: %v\n", err)
			return 1
		}
		if err := os.WriteFile(a.jsonOut, append(data, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "sheaf verify: write json: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "verify report → %s\n", a.jsonOut)
	}

	// The bottom line always goes to stderr, so a caller that redirected
	// the ledger to a file still sees the verdict.
	fmt.Fprintf(stderr, "verdict: %s (%d error(s), %d warning(s))\n", rep.Verdict, rep.Errors, rep.Warnings)

	if a.strict && rep.Verdict == verify.VerdictBroken {
		return 3
	}
	return 0
}

// verifySummarize implements `sheaf verify summarize`: it reads the
// agent-filled attribution verdicts (the assertions `sheaf verify --json`
// sampled into verify.json) and renders a per-library precision +
// confirmed-false-positive ledger. The semantic tp/fp call was the agent's;
// this only does the arithmetic.
func verifySummarize(args []string) int {
	fs := flag.NewFlagSet("verify summarize", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		assertionsPath string
		ledgerOut      string
	)
	fs.StringVar(&assertionsPath, "assertions", "",
		"Verdicted assertions to summarize: JSONL (one per line), a JSON array, or a verify.json. Required.")
	fs.StringVar(&ledgerOut, "ledger", "", "Write the precision ledger here (default: stdout).")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if assertionsPath == "" {
		fmt.Fprintln(os.Stderr, "sheaf verify summarize: --assertions is required (the verdicted assertions the agent produced from verify.json).")
		return 2
	}
	rows, err := verify.LoadAssertions(assertionsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sheaf verify summarize: %v\n", err)
		return 1
	}
	ledger := verify.RenderPrecisionLedger(rows)
	if ledgerOut != "" {
		if err := os.WriteFile(ledgerOut, []byte(ledger), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "sheaf verify summarize: write ledger: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "precision ledger → %s\n", ledgerOut)
	} else {
		fmt.Fprintln(os.Stdout, ledger)
	}
	return 0
}
