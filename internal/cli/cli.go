// Package cli implements the sheaf binary's subcommands.
//
// Each public function (Scan, Doctor, Version, …) takes the args
// after the subcommand name, processes them, and returns the exit
// code. Returning instead of os.Exit'ing keeps the surface testable.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/llm"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
)

// probeLLMEmbedder is exported for use by Doctor's LLM health check.
func probeLLMEmbedder(ctx context.Context, cfg *configpb.LLMConfig) error {
	return llm.ProbeEmbedder(ctx, cfg)
}

// Build metadata, overridden at link time via -ldflags (see the Makefile
// and .goreleaser.yaml). Defaults apply to plain `go build` / `go run`.
var (
	BuildVersion = "0.1.0-dev"
	BuildCommit  = "unknown"
	BuildDate    = "unknown"
)

// stringsFlag collects a repeatable / comma-separated flag value.
type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

// Scan runs `sheaf scan`.
func Scan(args []string) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath    string
		repoPath      string
		quiet         bool
		manifest      string
		outputDir     string
		configRoot    string
		baseURL       string
		singleFile    bool
		failOnError   bool
		jobs          int
		snapshotCache string
		forceRescan   bool
		auto          bool
		model         string
		llmBackend    string
		attrMaxTests  int
		attrMaxDocs   int
		includeGlobs  stringsFlag
		scopeLibs     stringsFlag
	)
	fs.StringVar(&configPath, "config", "", "Path to sheaf.textproto (default: <repo>/sheaf.textproto)")
	fs.StringVar(&repoPath, "repo", ".", "Path to the project repo root. In --manifest mode this is the default repo root for entries whose config does not pin one.")
	fs.BoolVar(&quiet, "quiet", false, "Suppress per-adapter detail in output")
	fs.StringVar(&manifest, "manifest", "", "Path to a MonorepoManifest textproto. Fans out a scan + render for every entry, writing N reports plus an index.html. Bypasses the single-repo scan path.")
	fs.StringVar(&outputDir, "output-dir", "", "Output directory for --manifest mode (default: the directory containing the manifest). Per-entry output paths resolve relative to it.")
	fs.StringVar(&configRoot, "config-root", "", "In --manifest mode, the directory each entry's relative config_path resolves against (default: the manifest's own directory). Absolute config_path values are unaffected.")
	fs.BoolVar(&failOnError, "fail-on-error", false, "In --manifest mode, return a non-zero exit code if any entry failed (default: continue-on-failure, exit 0 as long as the manifest parsed).")
	fs.StringVar(&baseURL, "base-url", "", "In --manifest mode, the base URL the run will be published at (e.g. https://host/path/). When set, the index and each report's run-switcher emit absolute links so a single shared report file stays navigable.")
	fs.BoolVar(&singleFile, "single-file", false, "In --manifest mode, emit one portable index.html embedding every report as a hash-routed iframe, instead of a directory of separate files. Best for small/medium runs.")
	fs.IntVar(&jobs, "jobs", 0, "In --manifest mode, max entries scanned/rendered concurrently (0 = a machine-sized default: max(2, NumCPU/2) capped at 8). 1 reproduces serial behavior. The orchestrator already parallelizes adapters within an entry, so this is deliberately below NumCPU.")
	fs.IntVar(&jobs, "j", 0, "Shorthand for --jobs.")
	fs.StringVar(&snapshotCache, "snapshot-cache", "", "In --manifest mode, directory for the snapshot reuse cache (default: <output-dir>/.snapshot-cache). A warm run with unchanged inputs renders from cached snapshots instead of re-scanning. Set to a path to relocate it.")
	fs.BoolVar(&forceRescan, "force-rescan", false, "In --manifest mode, bypass the snapshot cache and re-scan every entry (still refreshing the cache). Required after changing sheaf's own scanner/adapter code, which the cache key does not cover.")
	fs.BoolVar(&auto, "auto", false, "Zero-config mode: auto-detect ecosystems, synthesize a config, and emit four artifacts (sheaf-report.html + report/ + sheaf.textproto + sheaf-hardening.md). No sheaf.textproto required.")
	fs.StringVar(&model, "model", "", "Model tag for the LLM tier in --auto mode (default per backend: ollama qwen2.5:14b-instruct, anthropic claude-sonnet-4-6).")
	fs.StringVar(&llmBackend, "llm-backend", "auto", "LLM tier backend for --auto: auto (frontier if ANTHROPIC_API_KEY set, else ollama), ollama, anthropic, or none (deterministic only — no model, fastest; the contract/test/doc join is unaffected).")
	fs.IntVar(&attrMaxTests, "attr-max-tests", 0, "Cap on tests adjudicated by the LLM attribution pass in --auto (0 = all). Bounds frontier-API cost on a cold run.")
	fs.IntVar(&attrMaxDocs, "attr-max-docs", 0, "Cap on docs adjudicated by the LLM attribution pass in --auto (0 = all).")
	fs.Var(&includeGlobs, "include", "In --auto mode, restrict detection/scan to these globs (repeatable / comma-separated). Default: whole repo.")
	fs.Var(&scopeLibs, "scope-library", "In --auto mode, restrict the contract surface to these libraries (repeatable). Keeps the LLM tier bounded.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if auto {
		return runAuto(os.Stdout, os.Stderr, repoPath, outputDir, model, llmBackend, attrMaxTests, attrMaxDocs, includeGlobs, scopeLibs)
	}
	if manifest != "" {
		return runManifestCLI(os.Stdout, os.Stderr, manifest, outputDir, repoPath, configRoot, baseURL, singleFile, failOnError, jobs, snapshotCache, forceRescan)
	}
	return runScan(os.Stdout, os.Stderr, configPath, repoPath, quiet)
}

// runManifestCLI resolves the default output directory and invokes the
// fan-out runner. outputDir defaults to the manifest's own directory; the
// snapshot cache defaults to <outputDir>/.snapshot-cache so a warm re-run
// reuses snapshots without any extra flags.
func runManifestCLI(stdout, stderr io.Writer, manifestPath, outputDir, defaultRepo, configRoot, baseURL string, singleFile, failOnError bool, jobs int, snapshotCache string, forceRescan bool) int {
	if outputDir == "" {
		outputDir = filepath.Dir(manifestPath)
	}
	if configRoot != "" {
		if abs, err := filepath.Abs(configRoot); err == nil {
			configRoot = abs
		}
	}
	// Caching is ON by default: an empty --snapshot-cache resolves to a
	// hidden dir under the output directory. --force-rescan still refreshes
	// it. Passing an explicit empty value isn't reachable via the CLI, so
	// "" here always means "use the default location".
	if snapshotCache == "" {
		snapshotCache = filepath.Join(outputDir, ".snapshot-cache")
	}
	if err := RunManifest(context.Background(), manifestPath, outputDir, defaultRepo, configRoot, baseURL, singleFile, failOnError, jobs, snapshotCache, forceRescan, stdout); err != nil {
		fmt.Fprintf(stderr, "sheaf scan --manifest: %v\n", err)
		return 1
	}
	return 0
}

// Doctor runs `sheaf doctor`.
func Doctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath string
		repoPath   string
	)
	fs.StringVar(&configPath, "config", "", "Path to sheaf.textproto (default: <repo>/sheaf.textproto)")
	fs.StringVar(&repoPath, "repo", ".", "Path to the project repo root")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return runDoctor(os.Stdout, os.Stderr, configPath, repoPath)
}

// Version runs `sheaf version`, printing the embedded build metadata.
func Version(_ []string) int {
	fmt.Printf("sheaf %s\n", BuildVersion)
	fmt.Printf("  commit: %s\n", BuildCommit)
	fmt.Printf("  built:  %s\n", BuildDate)
	return 0
}

// resolveConfigPaths derives the actual sheaf.textproto and
// source-map (categorization-rules.textproto) paths given user input.
func resolveConfigPaths(configPath, repoPath string) (string, string, error) {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve --repo: %w", err)
	}
	if configPath == "" {
		configPath = filepath.Join(absRepo, "sheaf.textproto")
	}
	rulesPath := filepath.Join(absRepo, "categorization-rules.textproto")
	return configPath, rulesPath, nil
}

func runScan(stdout, stderr io.Writer, configPath, repoPath string, quiet bool) int {
	cfgPath, rulesPath, err := resolveConfigPaths(configPath, repoPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan: %v\n", err)
		return 3
	}
	// Rules are optional; indexer runs without categorization if absent.
	var rules *categorizationpb.Rules
	if _, err := os.Stat(rulesPath); err == nil {
		rules, err = config.LoadRules(rulesPath)
		if err != nil {
			fmt.Fprintf(stderr, "sheaf scan: %v\n", err)
			return 3
		}
	} else {
		fmt.Fprintln(stderr, "warning: no source map (categorization-rules.textproto) found; categorization will be skipped")
	}

	o, err := orchestrator.New(cfg, rules, repoPath)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan: %v\n", err)
		return 3
	}
	res, err := o.Run(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan: %v\n", err)
		return 3
	}
	stats := res.Corpus.Stats()
	fmt.Fprintf(stdout, "Scan complete in %s (ingest %s, index %s, analyze %s)\n",
		res.Duration.Round(1_000_000),
		res.IngestDuration.Round(1_000_000),
		res.IndexDuration.Round(1_000_000),
		res.AnalyzeDuration.Round(1_000_000))
	fmt.Fprintf(stdout, "  Contract elements: %d (incl. %d inherited via composition)\n",
		stats.Elements, res.IndexStats.InheritedMethods)
	fmt.Fprintf(stdout, "  Test cases:        %d\n", stats.Tests)
	fmt.Fprintf(stdout, "  Doc claims:        %d\n", stats.DocClaims)
	fmt.Fprintf(stdout, "  Coverage profiles: %d (%d test refs, %d doc refs, %d implements links)\n",
		res.IndexStats.ProfilesBuilt,
		res.IndexStats.TestRefsByElement,
		res.IndexStats.DocRefsByElement,
		res.IndexStats.ImplementsLinks)
	fmt.Fprintf(stdout, "  Facade-implements edges: %d\n", res.IndexStats.FacadeEdges)
	if as := res.AttributionStats; as.TestsScanned+as.DocsScanned > 0 {
		detTested, llmOnlyTested := tierCoverageCounts(res.Corpus)
		fmt.Fprintf(stdout, "  LLM attribution:   +%d test edges, +%d doc edges (%d proposed, %d cite-dropped, %d redundant, %d via alias) over %d tests / %d docs\n",
			as.TestEdges, as.DocEdges, as.Proposed, as.CiteDropped, as.Redundant, as.AliasEdges, as.TestsScanned, as.DocsScanned)
		fmt.Fprintf(stdout, "  Recall lift:       %d elements had deterministic test coverage; +%d gained LLM-only test coverage (%d total)\n",
			detTested, llmOnlyTested, detTested+llmOnlyTested)
	}
	if len(res.Findings) > 0 {
		fmt.Fprintf(stdout, "  Findings:          %d\n", len(res.Findings))
		// Per-kind breakdown
		byKind := make(map[string]int)
		for _, f := range res.Findings {
			byKind[strings.TrimPrefix(f.GetKind().String(), "FINDING_KIND_")]++
		}
		var kinds []string
		for k := range byKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			fmt.Fprintf(stdout, "    %-24s %d\n", k, byKind[k])
		}
	}
	if !quiet && stats.DocClaims > 0 {
		fmt.Fprintln(stdout, "\nSample doc mentions:")
		for i, dc := range res.Corpus.DocClaims() {
			if i >= 5 {
				break
			}
			fmt.Fprintf(stdout, "  %s:%d %v\n", dc.GetSourcePath(), dc.GetLocation().GetLine(), dc.GetContractRefs())
		}
	}
	if !quiet && stats.Tests > 0 {
		fmt.Fprintln(stdout, "\nSample tests:")
		for i, tc := range res.Corpus.Tests() {
			if i >= 5 {
				break
			}
			fmt.Fprintf(stdout, "  [%s] %s (%s:%d)\n", tc.GetFramework(), tc.GetId(), tc.GetLocation().GetPath(), tc.GetLocation().GetLine())
		}
	}
	if len(res.AdapterErrors) > 0 {
		fmt.Fprintln(stderr, "\nAdapter errors:")
		for _, ae := range res.AdapterErrors {
			fmt.Fprintf(stderr, "  - %s\n", ae)
		}
		return 1
	}
	return 0
}

func runDoctor(stdout, stderr io.Writer, configPath, repoPath string) int {
	cfgPath, rulesPath, err := resolveConfigPaths(configPath, repoPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}
	fmt.Fprintf(stdout, "Config:                %s ", cfgPath)
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(stdout, "[FAIL]")
		fmt.Fprintf(stderr, "  %v\n", err)
		return 3
	}
	fmt.Fprintln(stdout, "[OK]")

	fmt.Fprintf(stdout, "Source map:            %s ", rulesPath)
	if _, err := os.Stat(rulesPath); err != nil {
		fmt.Fprintln(stdout, "[MISSING]")
	} else {
		rules, err := config.LoadRules(rulesPath)
		if err != nil {
			fmt.Fprintln(stdout, "[FAIL]")
			fmt.Fprintf(stderr, "  %v\n", err)
			return 3
		}
		fmt.Fprintf(stdout, "[OK] (%d categories, %d ownership entries)\n",
			len(rules.GetCategory()), len(rules.GetOwnership()))
	}

	fmt.Fprintln(stdout, "\nAdapters:")
	o, err := orchestrator.New(cfg, nil, repoPath)
	if err != nil {
		fmt.Fprintf(stderr, "  resolve: %v\n", err)
		return 3
	}
	s := o.Summary()
	for _, name := range s.ContractAnchors {
		fmt.Fprintf(stdout, "  %-20s [OK]\n", name)
	}
	for _, name := range s.TestParsers {
		fmt.Fprintf(stdout, "  %-20s [OK]\n", name)
	}
	for _, name := range s.DocParsers {
		fmt.Fprintf(stdout, "  %-20s [OK]\n", name)
	}
	for _, name := range s.RenderedRefs {
		fmt.Fprintf(stdout, "  %-20s [OK]\n", name)
	}
	for _, name := range s.ImplementsMaps {
		fmt.Fprintf(stdout, "  %-20s [OK]\n", name)
	}
	if len(s.ContractAnchors)+len(s.TestParsers)+len(s.DocParsers)+len(s.RenderedRefs)+len(s.ImplementsMaps) == 0 {
		fmt.Fprintln(stdout, "  (none configured)")
	}

	// LLM / Embedder probe.
	if llmCfg := cfg.GetLlm(); llmCfg != nil && llmCfg.GetEmbeddings() != "" && llmCfg.GetEmbeddings() != "noop" {
		fmt.Fprintln(stdout, "\nLLM:")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := probeLLMEmbedder(ctx, llmCfg); err != nil {
			fmt.Fprintf(stdout, "  %-20s [FAIL]  %v\n", llmCfg.GetEmbeddings(), err)
		} else {
			fmt.Fprintf(stdout, "  %-20s [OK]    reachable\n", llmCfg.GetEmbeddings())
		}
	}

	fmt.Fprintln(stdout, "\nProject:", cfg.GetProject().GetName())
	if dn := cfg.GetProject().GetDisplayName(); dn != "" {
		fmt.Fprintln(stdout, "Display:", dn)
	}
	return 0
}

// tierCoverageCounts reports, across all coverage profiles, how many
// elements have deterministic test coverage vs. how many gained only
// LLM-tier test edges (the recall lift the attribution pass produced).
// The deterministic count is the trusted number; the LLM-only count is
// the flagged-tier addition.
func tierCoverageCounts(c *corpus.Corpus) (detTested, llmOnlyTested int) {
	for _, p := range c.Profiles() {
		det := len(concatTestRefs(p.GetTests()))  // deterministic buckets only
		llm := len(p.GetTests().GetLlmInferred()) // the flagged LLM tier
		switch {
		case det > 0:
			detTested++
		case llm > 0:
			llmOnlyTested++
		}
	}
	return
}

func concatTestRefs(t *coveragepb.TestCoverage) []*commonpb.TestRef {
	if t == nil {
		return nil
	}
	var out []*commonpb.TestRef
	out = append(out, t.GetUnit()...)
	out = append(out, t.GetIntegration()...)
	out = append(out, t.GetE2E()...)
	out = append(out, t.GetCtf()...)
	out = append(out, t.GetPerformance()...)
	out = append(out, t.GetFuzz()...)
	out = append(out, t.GetGolden()...)
	return out
}
