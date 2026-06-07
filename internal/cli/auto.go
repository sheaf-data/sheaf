package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sheaf-data/sheaf/internal/autoconfig"
	"github.com/sheaf-data/sheaf/internal/autodetect"
	"github.com/sheaf-data/sheaf/internal/hardening"
	"github.com/sheaf-data/sheaf/internal/llm"
	"github.com/sheaf-data/sheaf/internal/llm/ollama"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	htmlreport "github.com/sheaf-data/sheaf/internal/report/html"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	"github.com/sheaf-data/sheaf/utils/scanner"
)

// primaryEcosystem maps the dominant deterministic contract adapter to a
// scanner ecosystem view id (the masthead shape). The LLM tier
// (llmextract) is ignored for view selection — it shares the cpp surface.
// Unknown/empty falls back to the scanner's default view.
func primaryEcosystem(det *autodetect.Result) string {
	toView := map[string]string{
		"cppheader": "cpp",
		"proto":     "proto",
		"fidl":      "fidl",
		"cml":       "proto-config",
		"clap":      "cli",
	}
	best, bestN := "", -1
	for _, d := range det.Contract() {
		view, ok := toView[d.Adapter]
		if !ok {
			continue
		}
		if d.FileCount > bestN {
			best, bestN = view, d.FileCount
		}
	}
	return best
}

// hasDoxygenDocParser reports whether the config wires the doxygen doc-parser.
func hasDoxygenDocParser(cfg *configpb.Config) bool {
	for _, dp := range cfg.GetDocParser() {
		if dp.GetDoxygen() != nil {
			return true
		}
	}
	return false
}

// generateDoxygenXML writes a minimal Doxyfile over the C++ header tree and
// runs doxygen to emit XML under <absOut>/doxygen. INPUT is derived from the
// cppheader anchor's include globs (their literal dir prefixes) so a scoped
// --auto run only documents the scoped headers. Returns an error (which the
// caller turns into a warning) when doxygen is absent or the run fails.
func generateDoxygenXML(ctx context.Context, absRepo, absOut string, cfg *configpb.Config, stdout, stderr io.Writer) error {
	dox, err := exec.LookPath("doxygen")
	if err != nil {
		return fmt.Errorf("doxygen not on PATH — install it to populate docs.reference")
	}
	outDir := filepath.Join(absOut, "doxygen")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("PROJECT_NAME = sheaf-auto\n")
	b.WriteString("GENERATE_HTML = NO\nGENERATE_LATEX = NO\nGENERATE_XML = YES\nXML_OUTPUT = xml\n")
	fmt.Fprintf(&b, "OUTPUT_DIRECTORY = %s\n", outDir)
	b.WriteString("RECURSIVE = YES\nEXTRACT_ALL = NO\nQUIET = YES\nWARNINGS = NO\nWARN_IF_UNDOCUMENTED = NO\n")
	b.WriteString("FILE_PATTERNS = *.h *.hpp *.hh\n")
	b.WriteString("EXCLUDE_PATTERNS = */internal/* */impl/*\n")
	for _, in := range doxygenInputs(absRepo, cfg) {
		fmt.Fprintf(&b, "INPUT += %s\n", in)
	}
	doxyfile := filepath.Join(outDir, "Doxyfile")
	if err := os.WriteFile(doxyfile, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Generating Doxygen XML for the docs.reference surface…")
	cmd := exec.CommandContext(ctx, dox, doxyfile)
	cmd.Stderr = stderr
	return cmd.Run()
}

// doxygenInputs derives Doxygen INPUT dirs from the cppheader include globs'
// literal prefixes (so a scoped scan only documents the scoped tree), falling
// back to the whole repo.
func doxygenInputs(absRepo string, cfg *configpb.Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, ca := range cfg.GetContractAnchor() {
		ch := ca.GetCppHeader()
		if ch == nil {
			continue
		}
		for _, g := range ch.GetInclude() {
			dir := absRepo
			if pfx := globPrefix(g); pfx != "" {
				dir = filepath.Join(absRepo, pfx)
			}
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}
	if len(out) == 0 {
		out = []string{absRepo}
	}
	return out
}

// globPrefix returns the literal directory prefix of a glob (the part before
// the first wildcard segment): "pw_string/public/**/*.h" -> "pw_string/public",
// "**/*.h" -> "".
func globPrefix(g string) string {
	i := strings.IndexAny(g, "*?[")
	if i < 0 {
		return strings.TrimSuffix(g, "/")
	}
	g = g[:i]
	if j := strings.LastIndexByte(g, '/'); j >= 0 {
		return g[:j]
	}
	return ""
}

// runAuto implements `sheaf scan --auto`: a zero-config scan that
// auto-detects ecosystems, synthesizes a config, runs the pipeline, and
// emits the three artifacts (report.html + generated sheaf.textproto +
// sheaf-hardening.md) into outputDir.
//
// `include` optionally narrows the tree that is detected/scanned; an
// empty list scans everything. `scopeLibs` restricts the contract
// surface to the named libraries (mirrors the config's scope.library) —
// crucial for keeping the LLM-tier cost bounded, since llmextract only
// calls the model for in-scope files.
func runAuto(stdout, stderr io.Writer, repoPath, outputDir, model, llmBackend string, attrMaxTests, attrMaxDocs int, include, scopeLibs []string) int {
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: %v\n", err)
		return 3
	}
	if outputDir == "" {
		outputDir = filepath.Join(absRepo, "sheaf-auto")
	}
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: %v\n", err)
		return 3
	}
	ctx := context.Background()

	// 1. Detect ecosystems.
	det, err := autodetect.Detect(absRepo, include, nil)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: detect: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Auto-detected adapters:")
	for _, d := range det.Detections {
		fmt.Fprintf(stdout, "  %-10s %-9s %-13s %d file(s)\n", d.Adapter, d.Role, d.Tier, d.FileCount)
	}

	// 2. Resolve + report the LLM backend. Warn (don't fail) if the LLM
	//    tier's backend is unreachable — deterministic adapters still
	//    produce a report; the LLM rows just won't appear. With --llm-backend
	//    none the tier is off by construction (autoconfig omits it), so we
	//    say so plainly and skip the reachability probe entirely.
	backend := llm.ResolveBackendName(llmBackend)
	switch {
	case backend == llm.BackendNone:
		fmt.Fprintln(stdout, "LLM tier: off (--llm-backend none) — deterministic join only")
	case det.Has("llmextract"):
		fmt.Fprintf(stdout, "LLM tier backend: %s\n", backend)
		switch backend {
		case llm.BackendAnthropic:
			if os.Getenv("ANTHROPIC_API_KEY") == "" {
				fmt.Fprintln(stderr, "warning: anthropic backend selected but ANTHROPIC_API_KEY is not set; the LLM tier will be empty this run")
			}
		default: // ollama
			if err := ollama.Ping(ctx, "", 0); err != nil {
				fmt.Fprintf(stderr, "warning: ollama not reachable (%v); the LLM tier will be empty this run\n", err)
			}
		}
	}

	// 3. Synthesize the config (the structural freeze).
	projectName := filepath.Base(absRepo)
	cfg := autoconfig.Build(autoconfig.Options{
		ProjectName:    projectName,
		ScopeLibraries: scopeLibs,
		LLMModel:       model,
		LLMBackend:     llmBackend,
	}, det)

	if err := os.MkdirAll(absOut, 0o755); err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: %v\n", err)
		return 1
	}

	// 4. Artifact 2: the generated, byte-stable sheaf.textproto. Serialize
	//    BEFORE stamping the environment-specific cache dir below, so the
	//    committed config stays portable (pure structure, no machine path).
	cfgBytes, err := autoconfig.Marshal(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: marshal config: %v\n", err)
		return 1
	}
	cfgOut := filepath.Join(absOut, "sheaf.textproto")
	if err := os.WriteFile(cfgOut, cfgBytes, 0o644); err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: write config: %v\n", err)
		return 1
	}

	// Stamp the content-hash cache dirs on the in-memory config (not the
	// serialized one) so the LLM tier's extractions/attributions are
	// cached/reused, and apply the cost caps.
	cacheDir := filepath.Join(absOut, "llmextract-cache")
	for _, ca := range cfg.GetContractAnchor() {
		if lx := ca.GetLlmextract(); lx != nil {
			lx.CacheDir = cacheDir
		}
	}
	if ac := cfg.GetAttribution(); ac != nil {
		ac.CacheDir = filepath.Join(absOut, "attribution-cache")
		ac.MaxTests = uint32(attrMaxTests)
		ac.MaxDocs = uint32(attrMaxDocs)
	}

	// 4.5. Doxygen XML for the docs.reference surface. When a doxygen
	//      doc-parser was wired (a C++ header contract shipping a Doxyfile),
	//      generate the XML in THIS command so docs.reference is populated
	//      with no hand-edit. Best-effort: if `doxygen` is not installed we
	//      warn and continue (the surface reads 0 — an honest gap). The
	//      generated absolute path is stamped on the in-memory config only;
	//      the committed config keeps the repo-relative default (portable).
	if hasDoxygenDocParser(cfg) {
		if err := generateDoxygenXML(ctx, absRepo, absOut, cfg, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "warning: Doxygen XML not generated (%v); docs.reference will be empty this run\n", err)
		} else {
			xmlAbs := filepath.Join(absOut, "doxygen", "xml")
			for _, dp := range cfg.GetDocParser() {
				if dx := dp.GetDoxygen(); dx != nil {
					dx.XmlDir = xmlAbs
				}
			}
		}
	}

	// 5. Run the pipeline against the synthesized config.
	o, err := orchestrator.New(cfg, nil, repoPath)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: %v\n", err)
		return 1
	}
	res, err := o.Run(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: %v\n", err)
		return 1
	}
	stats := res.Corpus.Stats()

	// 6. Artifact 1: the two-tier confidence report (basic multi-page
	//    renderer) into report/. Provenance is threaded into the element
	//    views and the masthead splits deterministic vs LLM-inferred.
	twoTierDir := filepath.Join(absOut, "report")
	w := &htmlreport.Writer{Project: projectName, OutDir: twoTierDir}
	pages, err := w.Write(res.Corpus, res.Findings)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: render two-tier report: %v\n", err)
		return 1
	}

	// 7. Artifact 3: sheaf-hardening.md.
	md := hardening.Generate(hardening.Input{
		RepoRoot:  absRepo,
		ProjectID: projectName,
		Detection: det,
		Corpus:    res.Corpus,
	})
	hardOut := filepath.Join(absOut, "sheaf-hardening.md")
	if err := os.WriteFile(hardOut, []byte(md), 0o644); err != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: write hardening: %v\n", err)
		return 1
	}

	// 8. Artifact 4: the canonical sheaf scanner report (the
	//    sheaf-self.html style rich single-file report), rendered from the
	//    SAME computed Result so the LLM tier is not re-issued. The
	//    library list and ecosystem view come from scope/detection.
	stdReportOut := filepath.Join(absOut, "sheaf-report.html")
	stdElems, _, rerr := scanner.RenderFromResult(res, cfg,
		strings.Join(scopeLibs, ","), projectName, primaryEcosystem(det), repoPath, stdReportOut)
	if rerr != nil {
		fmt.Fprintf(stderr, "sheaf scan --auto: render standard report: %v\n", rerr)
		return 1
	}

	fmt.Fprintf(stdout, "\nScan complete in %s\n", res.Duration.Round(1_000_000))
	fmt.Fprintf(stdout, "  Contract elements: %d\n", stats.Elements)
	fmt.Fprintf(stdout, "  Test cases:        %d\n", stats.Tests)
	fmt.Fprintf(stdout, "  Doc claims:        %d\n", stats.DocClaims)
	if as := res.AttributionStats; as.TestsScanned+as.DocsScanned > 0 {
		fmt.Fprintf(stdout, "  LLM attribution:   %d test edges, %d doc edges added (%d proposed, %d cite-dropped, %d redundant, %d via alias) over %d tests / %d docs\n",
			as.TestEdges, as.DocEdges, as.Proposed, as.CiteDropped, as.Redundant, as.AliasEdges, as.TestsScanned, as.DocsScanned)
	}
	if u := res.LLMUsage; u.Calls > 0 {
		fmt.Fprintf(stdout, "  LLM usage:         %d calls, %d input + %d output tokens",
			u.Calls, u.TotalInputTokens(), u.OutputTokens)
		if u.CacheCreationInputTokens+u.CacheReadInputTokens > 0 {
			fmt.Fprintf(stdout, " (cache: %d written, %d read)", u.CacheCreationInputTokens, u.CacheReadInputTokens)
		}
		fmt.Fprintln(stdout)
		if usd, known := llm.EstimateCostUSD(llmBackend, model, u); known {
			if backend == llm.BackendOllama {
				fmt.Fprintln(stdout, "  Estimated cost:    $0.00 (local ollama — no marginal cost)")
			} else {
				fmt.Fprintf(stdout, "  Estimated cost:    ~$%.4f (%s list pricing, approximate)\n", usd, backend)
			}
		} else {
			fmt.Fprintf(stdout, "  Estimated cost:    unknown (no rate table for %s model %q)\n", backend, model)
		}
	}
	fmt.Fprintln(stdout, "\nFour artifacts written to", absOut+":")
	fmt.Fprintf(stdout, "  - sheaf-report.html  (canonical scanner report, %d elements)  file://%s\n", stdElems, stdReportOut)
	fmt.Fprintf(stdout, "  - report/index.html  (two-tier confidence view, %d pages)\n", pages)
	fmt.Fprintf(stdout, "  - sheaf.textproto    (the structural freeze)\n")
	fmt.Fprintf(stdout, "  - sheaf-hardening.md (deterministic backlog)\n")
	if len(res.AdapterErrors) > 0 {
		fmt.Fprintln(stderr, "\nAdapter errors (non-fatal):")
		for _, ae := range res.AdapterErrors {
			fmt.Fprintf(stderr, "  - %s\n", ae)
		}
	}
	return 0
}
