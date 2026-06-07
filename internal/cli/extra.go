package cli

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sheaf-data/sheaf/internal/analyze"
	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/coverage/refs"
	"github.com/sheaf-data/sheaf/internal/grounding"
	"github.com/sheaf-data/sheaf/internal/llm"
	"github.com/sheaf-data/sheaf/internal/mcp"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	"github.com/sheaf-data/sheaf/internal/prbot"
	"github.com/sheaf-data/sheaf/internal/report/conceptdocs"
	htmlreport "github.com/sheaf-data/sheaf/internal/report/html"
	"github.com/sheaf-data/sheaf/internal/review"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	"google.golang.org/protobuf/encoding/protojson"
)

// ===========================================================
// `sheaf gaps`
// ===========================================================

func Gaps(args []string) int {
	fs := flag.NewFlagSet("gaps", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath, repoPath string
		kind, library        string
		minSeverity          string
		format               string
	)
	fs.StringVar(&configPath, "config", "", "Path to sheaf.textproto")
	fs.StringVar(&repoPath, "repo", ".", "Project repo root")
	fs.StringVar(&kind, "kind", "", "Filter by finding kind (MISSING_IN_CATEGORY, THIN_REFERENCE, …)")
	fs.StringVar(&library, "library", "", "Filter by library / element ID prefix")
	fs.StringVar(&minSeverity, "severity", "INFO", "Minimum severity (INFO|WARNING|ERROR)")
	fs.StringVar(&format, "format", "text", "Output format: text|json|csv")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return runGaps(os.Stdout, os.Stderr, configPath, repoPath, kind, library, minSeverity, format)
}

func runGaps(stdout, stderr io.Writer, configPath, repoPath, kind, library, minSeverity, format string) int {
	res, rc := runFullPipeline(stderr, configPath, repoPath)
	if rc != 0 {
		return rc
	}
	wantKind := strings.ToUpper(strings.TrimPrefix(kind, "FINDING_KIND_"))
	minSev := parseSeverity(minSeverity)
	filtered := res.Findings[:0:len(res.Findings)]
	for _, f := range res.Findings {
		if wantKind != "" && analyze.KindString(f.GetKind()) != wantKind {
			continue
		}
		if library != "" {
			// Match the broadened library filter in
			// internal/mcp/mcp.go opLibrarySnapshot: cobra subjects
			// use space separators, FIDL uses dots, gRPC uses
			// slash-then-dot. Without this, `sheaf gaps --library
			// kubectl` silently drops every TESTED_UNDOCUMENTED
			// finding because the subject is "kubectl annotate
			// --…", not "kubectl/…".
			subj := f.GetSubject()
			if subj != library &&
				!strings.HasPrefix(subj, library+"/") &&
				!strings.HasPrefix(subj, library+".") &&
				!strings.HasPrefix(subj, library+" ") {
				continue
			}
		}
		if f.GetSeverity() < minSev {
			continue
		}
		filtered = append(filtered, f)
	}
	switch format {
	case "json":
		m := protojson.MarshalOptions{Multiline: true, Indent: "  "}
		for _, f := range filtered {
			b, _ := m.Marshal(f)
			fmt.Fprintln(stdout, string(b))
		}
	case "csv":
		fmt.Fprintln(stdout, "id,kind,subject,severity,analyzer,message")
		for _, f := range filtered {
			fmt.Fprintf(stdout, "%s,%s,%s,%s,%s,%q\n",
				f.GetId(),
				analyze.KindString(f.GetKind()),
				f.GetSubject(),
				analyze.SeverityName(f.GetSeverity()),
				f.GetAnalyzer(),
				f.GetMessage())
		}
	default:
		if len(filtered) == 0 {
			fmt.Fprintln(stdout, "No findings.")
			return 0
		}
		fmt.Fprintf(stdout, "%d findings:\n", len(filtered))
		for _, f := range filtered {
			fmt.Fprintf(stdout, "  [%s] %s — %s: %s\n",
				analyze.SeverityName(f.GetSeverity()),
				analyze.KindString(f.GetKind()),
				f.GetSubject(),
				f.GetMessage())
		}
	}
	return 0
}

// ===========================================================
// `sheaf coverage`
// ===========================================================

func Coverage(args []string) int {
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath, repoPath string
		elementID, format    string
	)
	fs.StringVar(&configPath, "config", "", "Path to sheaf.textproto")
	fs.StringVar(&repoPath, "repo", ".", "Project repo root")
	fs.StringVar(&elementID, "element", "", "ContractElement ID (required)")
	fs.StringVar(&format, "format", "text", "Output format: text|json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if elementID == "" {
		fmt.Fprintln(os.Stderr, "sheaf coverage: --element is required")
		return 2
	}
	return runCoverage(os.Stdout, os.Stderr, configPath, repoPath, elementID, format)
}

func runCoverage(stdout, stderr io.Writer, configPath, repoPath, elementID, format string) int {
	res, rc := runFullPipeline(stderr, configPath, repoPath)
	if rc != 0 {
		return rc
	}
	prof := res.Corpus.Profile(elementID)
	if prof == nil {
		fmt.Fprintf(stderr, "no profile for element %q\n", elementID)
		return 2
	}
	if format == "json" {
		m := protojson.MarshalOptions{Multiline: true, Indent: "  "}
		b, _ := m.Marshal(prof)
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	e := res.Corpus.Element(elementID)
	fmt.Fprintf(stdout, "Element: %s (%s)\n", e.GetId(), e.GetKind().String())
	if e.GetDocCommentExcerpt() != "" {
		fmt.Fprintf(stdout, "Doc:     %s\n", truncMulti(e.GetDocCommentExcerpt(), 240))
	}
	fmt.Fprintln(stdout, "")
	docs, tests := profileTextDump(stdout, prof)
	if docs == 0 {
		fmt.Fprintln(stdout, "Docs:    (none)")
	}
	if tests == 0 {
		fmt.Fprintln(stdout, "Tests:   (none)")
	}
	if g := prof.GetGapsSummary(); g != nil && len(g.GetMissing()) > 0 {
		fmt.Fprintln(stdout, "")
		fmt.Fprintf(stdout, "Missing: %s\n", strings.Join(g.GetMissing(), ", "))
	}
	return 0
}

func profileTextDump(stdout io.Writer, p *coveragepb.CoverageProfile) (docs, tests int) {
	if d := p.GetDocs(); d != nil {
		if r := d.GetReference(); r != nil {
			n := refs.CountReferenceRefs(r)
			if n > 0 {
				fmt.Fprintf(stdout, "Reference docs (%d):\n", n)
				printRefBucket(stdout, "fidldoc", r.GetFidldoc())
				printRefBucket(stdout, "clidoc", r.GetClidoc())
				printRefBucket(stdout, "dockerdoc", r.GetDockerdoc())
				for _, key := range sortedAdapterKeys(r) {
					printRefBucket(stdout, key, r.GetByAdapter()[key].GetRefs())
				}
				docs += n
			}
		}
		if len(d.GetTutorial()) > 0 {
			fmt.Fprintf(stdout, "Tutorial docs (%d):\n", len(d.GetTutorial()))
			for _, ref := range d.GetTutorial() {
				fmt.Fprintf(stdout, "  %s:%d  (%d words)\n", ref.GetPath(), ref.GetLine(), ref.GetWords())
			}
			docs += len(d.GetTutorial())
		}
		if len(d.GetConcept()) > 0 {
			fmt.Fprintf(stdout, "Concept docs (%d):\n", len(d.GetConcept()))
			for _, ref := range d.GetConcept() {
				fmt.Fprintf(stdout, "  %s:%d  (%d words)\n", ref.GetPath(), ref.GetLine(), ref.GetWords())
			}
			docs += len(d.GetConcept())
		}
	}
	if t := p.GetTests(); t != nil {
		if len(t.GetUnit()) > 0 {
			fmt.Fprintf(stdout, "\nUnit tests (%d):\n", len(t.GetUnit()))
			for _, r := range t.GetUnit() {
				fmt.Fprintf(stdout, "  %s  (%s:%d)\n", r.GetTestName(), r.GetPath(), r.GetLine())
			}
			tests += len(t.GetUnit())
		}
		if len(t.GetIntegration()) > 0 {
			fmt.Fprintf(stdout, "\nIntegration tests (%d):\n", len(t.GetIntegration()))
			for _, r := range t.GetIntegration() {
				fmt.Fprintf(stdout, "  %s  (%s:%d)\n", r.GetTestName(), r.GetPath(), r.GetLine())
			}
			tests += len(t.GetIntegration())
		}
	}
	return docs, tests
}

// ===========================================================
// `sheaf report`
// ===========================================================

func Report(args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath, repoPath string
		format               string
		output               string
		lens                 string
		library              string
		libraryLabel         string
		suppress             string
		sourceURLTemplate    string
		docGlobs             docGlobFlag
	)
	fs.StringVar(&configPath, "config", "", "Path to sheaf.textproto")
	fs.StringVar(&repoPath, "repo", ".", "Project repo root")
	fs.StringVar(&format, "format", "csv", "Output format: csv|json|html (coverage lens)")
	fs.StringVar(&output, "output", "", "Output path. Coverage html: a directory. Concept-docs: an .html file or a dir (stdout if empty)")
	fs.StringVar(&lens, "lens", "coverage", "Report lens: coverage | concept-docs")
	fs.StringVar(&library, "library", "", "Library to report on (concept-docs lens)")
	fs.StringVar(&libraryLabel, "library-label", "", "Display label for the library (concept-docs lens)")
	fs.Var(&docGlobs, "doc-glob", "Concept-doc include glob, repeatable (concept-docs lens; overrides config doc_parser)")
	fs.StringVar(&suppress, "suppress", "", "Path to a .sheafignore-style suppression file (concept-docs lens)")
	fs.StringVar(&sourceURLTemplate, "source-url-template", "",
		"URL pattern that turns path:line labels into clickable links. Placeholders: {path}, {abs_path}, {line}. "+
			"Examples: https://github.com/grpc/grpc/blob/master/{path}#L{line} | "+
			"vscode://file/{abs_path}:{line} | cursor://file/{abs_path}:{line} | file://{abs_path}")
	var fromGrounding docGlobFlag
	fs.Var(&fromGrounding, "from-grounding", "Render from pre-emitted GroundingReport JSON(s) (repeatable; concept-docs lens). Skips the scan; multiple inputs roll into one multi-library report.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if lens == "concept-docs" {
		return runConceptDocsReport(os.Stdout, os.Stderr, configPath, repoPath, library, libraryLabel, docGlobs, suppress, sourceURLTemplate, output, fromGrounding)
	}
	return runReport(os.Stdout, os.Stderr, configPath, repoPath, format, output)
}

func runReport(stdout, stderr io.Writer, configPath, repoPath, format, output string) int {
	res, rc := runFullPipeline(stderr, configPath, repoPath)
	if rc != 0 {
		return rc
	}
	cfgPath, _, _ := resolveConfigPaths(configPath, repoPath)
	cfg, _ := config.LoadConfig(cfgPath)
	projectName := "sheaf"
	projectDisplay := ""
	if cfg != nil && cfg.GetProject() != nil {
		projectName = cfg.GetProject().GetName()
		projectDisplay = cfg.GetProject().GetDisplayName()
	}
	switch format {
	case "json":
		m := protojson.MarshalOptions{Multiline: true, Indent: "  "}
		for _, p := range res.Corpus.Profiles() {
			b, _ := m.Marshal(p)
			fmt.Fprintln(stdout, string(b))
		}
		return 0
	case "html":
		if output == "" {
			fmt.Fprintln(stderr, "sheaf report: --output <dir> is required when --format html")
			return 2
		}
		w := &htmlreport.Writer{
			Project:        projectName,
			ProjectDisplay: projectDisplay,
			OutDir:         output,
		}
		pages, err := w.Write(res.Corpus, res.Findings)
		if err != nil {
			fmt.Fprintf(stderr, "sheaf report: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "HTML report written to %s (%d pages)\n", output, pages)
		fmt.Fprintf(stdout, "  Open: file://%s/index.html\n", absPath(output))
		return 0
	case "csv", "":
		fmt.Fprintln(stdout, "element_id,tests,docs,examples,missing")
		for _, p := range res.Corpus.Profiles() {
			tests, docs, examples := profileCounts(p)
			missing := ""
			if g := p.GetGapsSummary(); g != nil {
				missing = strings.Join(g.GetMissing(), "|")
			}
			fmt.Fprintf(stdout, "%s,%d,%d,%d,%q\n", p.GetElementId(), tests, docs, examples, missing)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "sheaf report: unknown format %q (want csv|json|html)\n", format)
		return 2
	}
}

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// docGlobFlag collects repeated --doc-glob values for the concept-docs lens.
type docGlobFlag []string

func (d *docGlobFlag) String() string     { return strings.Join(*d, ",") }
func (d *docGlobFlag) Set(v string) error { *d = append(*d, v); return nil }

// runConceptDocsReport renders the Concept Docs report — the doc-centric
// clear/ambiguous/silent map — from the grounding surface. By default it scans
// one library via grounding.BuildConfig (the same GroundingReport as
// emit-grounding); with --from-grounding it renders from pre-emitted JSON(s),
// rolling several libraries into one multi-library report.
func runConceptDocsReport(stdout, stderr io.Writer, configPath, repoPath, library, libraryLabel string, docGlobs []string, suppress, sourceURLTemplate, output string, fromGrounding []string) int {
	var view *conceptdocs.View
	var sum grounding.Summary
	subject := libraryLabel

	// Linkify doc-card paths to the source browser when a template is given;
	// empty leaves paths plain (the --from-grounding rollups and the committed
	// golden render without it, so they stay byte-identical).
	var viewOpts []conceptdocs.Option
	if sourceURLTemplate != "" {
		viewOpts = append(viewOpts, conceptdocs.WithSourceURLTemplate(sourceURLTemplate))
	}

	if len(fromGrounding) > 0 {
		reps, err := loadGroundingReports(fromGrounding)
		if err != nil {
			fmt.Fprintf(stderr, "sheaf report: %v\n", err)
			return 1
		}
		for _, rep := range reps {
			sum.ElementsTotal += rep.Summary.ElementsTotal
			sum.ElementsGrounded += rep.Summary.ElementsGrounded
			sum.ElementsGuessing += rep.Summary.ElementsGuessing
			sum.ElementsUngrounded += rep.Summary.ElementsUngrounded
			sum.ElementsNotMentioned += rep.Summary.ElementsNotMentioned
		}
		if len(reps) == 1 {
			view = conceptdocs.BuildView(reps[0], viewOpts...)
			// reps[0].Commit was stamped at emit time by grounding.BuildConfig
			// (the scanned repo's git short-sha lands in the .grounding.json),
			// so view.Commit already carries the right corpus's sha — no
			// per-call --repo stamping at render time.
			if subject == "" {
				subject = reps[0].Library
			}
		} else {
			if subject == "" {
				subject = "these libraries"
			}
			view = conceptdocs.BuildViewAll(reps, subject, viewOpts...)
		}
	} else {
		if library == "" {
			fmt.Fprintln(stderr, "sheaf report --lens concept-docs: --library (or --from-grounding) is required")
			return 2
		}
		cfgPath, _, err := resolveConfigPaths(configPath, repoPath)
		if err != nil {
			fmt.Fprintf(stderr, "sheaf report: %v\n", err)
			return 2
		}
		rep, err := grounding.BuildConfig(context.Background(), cfgPath, repoPath, library, libraryLabel, docGlobs, suppress)
		if err != nil {
			fmt.Fprintf(stderr, "sheaf report: %v\n", err)
			return 1
		}
		view = conceptdocs.BuildView(rep, viewOpts...)
		// rep.Commit is stamped at the source by grounding.BuildConfig (the
		// scanned repoPath's git short-sha), so view.Commit already carries
		// the sha — for ffx this is the FFX_CHECKOUT HEAD, matching the
		// coverage report's header. No post-hoc stamping needed here.
		sum = rep.Summary
		subject = rep.Library
	}

	if output == "" {
		if err := conceptdocs.Render(stdout, view); err != nil {
			fmt.Fprintf(stderr, "sheaf report: %v\n", err)
			return 1
		}
		return 0
	}
	out := output
	if !strings.HasSuffix(strings.ToLower(out), ".html") {
		out = filepath.Join(out, "concept-docs.html")
	}
	if dir := filepath.Dir(out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(stderr, "sheaf report: %v\n", err)
			return 1
		}
	}
	f, err := os.Create(out)
	if err != nil {
		fmt.Fprintf(stderr, "sheaf report: %v\n", err)
		return 1
	}
	if err := conceptdocs.Render(f, view); err != nil {
		f.Close()
		fmt.Fprintf(stderr, "sheaf report: %v\n", err)
		return 1
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(stderr, "sheaf report: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Concept Docs report written to %s\n", out)
	fmt.Fprintf(stdout, "  %s — %d elements: %d clear / %d ambiguous / %d silent\n",
		subject, sum.ElementsTotal, sum.ElementsGrounded, sum.ElementsGuessing+sum.ElementsUngrounded, sum.ElementsNotMentioned)
	return 0
}

// loadGroundingReports reads GroundingReport JSON files (as written by
// emit-grounding) for the concept-docs --from-grounding path.
func loadGroundingReports(paths []string) ([]*grounding.Report, error) {
	out := make([]*grounding.Report, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var rep grounding.Report
		if err := json.Unmarshal(b, &rep); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		out = append(out, &rep)
	}
	return out, nil
}

// ===========================================================
// `sheaf serve`
// ===========================================================

func Serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath, repoPath string
		bind                 string
		port                 int
	)
	fs.StringVar(&configPath, "config", "", "Path to sheaf.textproto")
	fs.StringVar(&repoPath, "repo", ".", "Project repo root")
	fs.StringVar(&bind, "bind", "", "Override mcp_server.bind")
	fs.IntVar(&port, "port", 0, "Override mcp_server.port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return runServe(os.Stdout, os.Stderr, configPath, repoPath, bind, port)
}

func runServe(stdout, stderr io.Writer, configPath, repoPath, bind string, port int) int {
	res, rc := runFullPipeline(stderr, configPath, repoPath)
	if rc != 0 {
		return rc
	}
	cfgPath, rulesPath, _ := resolveConfigPaths(configPath, repoPath)
	_ = rulesPath
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}
	mcpCfg := cfg.GetMcpServer()
	if mcpCfg == nil {
		mcpCfg = &configpb.MCPServerConfig{Bind: "127.0.0.1", Port: 7700, CacheTtlSeconds: 3600}
	}
	if bind != "" {
		mcpCfg.Bind = bind
	}
	if port != 0 {
		mcpCfg.Port = uint32(port)
	}
	srv := mcp.New(res.Corpus, res.Findings, mcpCfg)
	// Structured per-call logging on stderr (stdout carries the banner
	// + a possible future stdio transport). Text by default; JSON when
	// SHEAF_LOG_FORMAT=json. Env-driven to avoid a proto change for now.
	srv = srv.WithLogger(slog.New(newLogHandler(stderr, os.Getenv("SHEAF_LOG_FORMAT"))))
	// Wire LLM-backed semantic search if configured.
	if embedder, err := llm.BuildEmbedder(cfg.GetLlm()); err == nil {
		cache, _ := llm.BuildCache(cfg.GetCache())
		srv = srv.WithEmbedder(embedder, cache)
		if _, noop := embedder.(llm.NoopEmbedder); !noop {
			fmt.Fprintf(stdout, "Semantic find_examples enabled (embedder=%s)\n", embedder.Name())
		}
	} else {
		fmt.Fprintf(stderr, "warning: embedder construction failed: %v (semantic search disabled)\n", err)
	}
	// Wire the review_pr operation. The same config used for the
	// startup scan is reused to scan PR base+head workspaces; the
	// caller hands those paths in per-request. We always enable
	// review_pr (the noop adapter is fine for render-only callers).
	var rules *categorizationpb_Rules
	if _, err := os.Stat(rulesPath); err == nil {
		rules, _ = config.LoadRules(rulesPath)
	}
	reviewAdapter, _ := review.Build(cfg.GetReview())
	srv = srv.WithReview(cfg, rules, reviewAdapter)
	if reviewAdapter != nil {
		fmt.Fprintf(stdout, "review_pr enabled (adapter=%s)\n", reviewAdapter.Name())
	}
	fmt.Fprintf(stdout, "MCP server listening on %s\n", srv.Addr())
	// Signal-aware context so the server drains in-flight requests on
	// SIGINT/SIGTERM instead of being hard-killed (audit #6). This also
	// retires the previously-permanent shutdown goroutine in Start: the
	// goroutine now unblocks on signal and Start returns nil after the
	// drain. stop() releases the signal handlers on the way out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Start(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// newLogHandler builds the slog handler for the MCP server. format
// selects JSON vs the default text handler; both write to w (stderr in
// production). Centralized so the e2e/integration tests can mirror it.
func newLogHandler(w io.Writer, format string) slog.Handler {
	if strings.EqualFold(format, "json") {
		return slog.NewJSONHandler(w, nil)
	}
	return slog.NewTextHandler(w, nil)
}

// ===========================================================
// `sheaf review`
// ===========================================================

func Review(args []string) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath, repoPath string
		baseRepoPath         string
		prRef                string
		post                 bool
		reviewOverride       string
		fileOut              string
		emitJSON             string
		emitSystem           string
		emitBaseRef          string
		emitHeadRef          string
		emitScannedAt        string
	)
	fs.StringVar(&configPath, "config", "", "Path to sheaf.textproto (used for both base + head)")
	fs.StringVar(&repoPath, "repo", ".", "Project repo root (PR head)")
	fs.StringVar(&baseRepoPath, "base", "", "Base repo root (PR base) — required for delta")
	fs.StringVar(&prRef, "pr", "PR#unknown", "PR reference for the comment header")
	fs.BoolVar(&post, "post", false, "Post the rendered comment via the configured review adapter (default: print to stdout)")
	fs.StringVar(&reviewOverride, "review", "", "Override the configured review adapter: noop | file | gerrit | github")
	fs.StringVar(&fileOut, "file-out", "", "When --review=file, write here. Directory → one file per PR; file path → single file. Defaults to env SHEAF_REVIEW_FILE_OUT.")
	fs.StringVar(&emitJSON, "emit-json", "", "If set, also write the structured delta.json artifact to this path.")
	fs.StringVar(&emitSystem, "emit-system", "", "System name to record in delta.json (e.g. fd, envoy).")
	fs.StringVar(&emitBaseRef, "emit-base-ref", "", "Base ref (full SHA) to record in delta.json.")
	fs.StringVar(&emitHeadRef, "emit-head-ref", "", "Head ref (full SHA) to record in delta.json.")
	fs.StringVar(&emitScannedAt, "emit-scanned-at", "", "Override the scanned_at timestamp in delta.json (RFC3339). Pass the head ref's commit time for byte-stable regeneration.")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if baseRepoPath == "" {
		fmt.Fprintln(os.Stderr, "sheaf review: --base is required")
		return 2
	}
	return runReview(os.Stdout, os.Stderr, configPath, repoPath, baseRepoPath, prRef, post, reviewOverride, fileOut,
		emitJSON, emitSystem, emitBaseRef, emitHeadRef, emitScannedAt)
}

func runReview(stdout, stderr io.Writer, configPath, headPath, basePath, prRef string, post bool, reviewOverride, fileOut,
	emitJSON, emitSystem, emitBaseRef, emitHeadRef, emitScannedAt string) int {
	cfgPath, rulesPath, err := resolveConfigPaths(configPath, headPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}
	var rules *categorizationpb_Rules
	if _, err := os.Stat(rulesPath); err == nil {
		r, err := config.LoadRules(rulesPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 3
		}
		rules = r
	}

	// Build the review adapter — honor --review override or fall back
	// to whatever the project config declares.
	adapter, err := buildReviewAdapter(cfg, reviewOverride, fileOut)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}

	res, err := prbot.RunReview(context.Background(), prbot.RunOptions{
		Config:   cfg,
		Rules:    rules,
		BaseRoot: basePath,
		HeadRoot: headPath,
		PRRef:    prRef,
		Post:     post,
		Adapter:  adapter,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if post {
		fmt.Fprintf(stdout, "Posted via %s → %s\n", res.Adapter, res.PostedTo)
		fmt.Fprintln(stdout, "---")
	}
	fmt.Fprint(stdout, res.Comment.Body)
	if emitJSON != "" {
		if rc := writeDeltaJSON(stderr, res, emitJSON, emitSystem, emitBaseRef, emitHeadRef, prRef, cfgPath, emitScannedAt); rc != 0 {
			return rc
		}
	}
	return 0
}

// writeDeltaJSON serializes the structured delta artifact alongside
// the markdown body. The renderer-only re-run path (sheaf review-html
// --delta) consumes this file.
func writeDeltaJSON(stderr io.Writer, res *prbot.RunResult, path, system, baseRef, headRef, prRef, configPath, scannedAt string) int {
	in := prbot.DeltaInputs{
		System:         system,
		Config:         configPath,
		BaseRef:        baseRef,
		HeadRef:        headRef,
		BaseShort:      shortSHA(baseRef),
		HeadShort:      shortSHA(headRef),
		ScanID:         scanID(system, headRef),
		SheafVersion:   BuildVersion,
		PRRefDisplayed: prRef,
	}
	if scannedAt != "" {
		t, err := time.Parse(time.RFC3339, scannedAt)
		if err != nil {
			fmt.Fprintf(stderr, "sheaf review: invalid --emit-scanned-at %q: %v\n", scannedAt, err)
			return 2
		}
		in.ScannedAt = t
	}
	art := prbot.BuildDeltaArtifact(in, res.BaseCorpus, res.HeadCorpus, res.Comment)
	b, err := prbot.MarshalDelta(art)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func shortSHA(ref string) string {
	if len(ref) < 7 {
		return ref
	}
	return ref[:7]
}

func scanID(system, headRef string) string {
	if system == "" || headRef == "" {
		return ""
	}
	return "sheaf-" + system + "-" + shortSHA(headRef)
}

func buildReviewAdapter(cfg *configpb.Config, override, fileOut string) (review.Adapter, error) {
	if override == "" {
		return review.Build(cfg.GetReview())
	}
	switch override {
	case "noop":
		return review.Noop{}, nil
	case "file":
		if fileOut == "" {
			return review.NewFileFromEnv(), nil
		}
		// If fileOut ends with a separator or names an existing dir,
		// use NewFileDir; else NewFile.
		if st, err := os.Stat(fileOut); err == nil && st.IsDir() {
			return review.NewFileDir(fileOut), nil
		}
		if strings.HasSuffix(fileOut, string(os.PathSeparator)) {
			return review.NewFileDir(fileOut), nil
		}
		return review.NewFile(fileOut), nil
	case "gerrit", "github":
		// Defer to the typed block in cfg.
		return review.Build(cfg.GetReview())
	}
	return nil, fmt.Errorf("sheaf review: unknown --review value %q", override)
}

// ===========================================================
// `sheaf review-html`
// ===========================================================

// ReviewHTML renders a self-contained HTML artifact from a delta.json
// produced by `sheaf review --emit-json`. This is the regenerate-
// from-preserved-JSON loop the example-pr-feedback regen scripts use.
func ReviewHTML(args []string) int {
	fs := flag.NewFlagSet("review-html", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		deltaPath string
		outPath   string
	)
	fs.StringVar(&deltaPath, "delta", "", "Path to delta.json (required)")
	fs.StringVar(&outPath, "o", "", "Output HTML path (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if deltaPath == "" || outPath == "" {
		fmt.Fprintln(os.Stderr, "sheaf review-html: --delta and -o are required")
		return 2
	}
	art, err := prbot.LoadDeltaArtifact(deltaPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	html, err := prbot.RenderHTML(art)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(outPath, html, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Wrote %s\n", outPath)
	return 0
}

// ===========================================================
// `sheaf init`
// ===========================================================

func Init(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		repoPath string
		template string
	)
	fs.StringVar(&repoPath, "repo", ".", "Where to write the config files")
	fs.StringVar(&template, "template", "minimal", "Template: minimal | argh-cli | fuchsia-internal")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return runInit(os.Stdout, os.Stderr, repoPath, template)
}

func runInit(stdout, stderr io.Writer, repoPath, template string) int {
	cfgPath := filepath.Join(repoPath, "sheaf.textproto")
	rulesPath := filepath.Join(repoPath, "categorization-rules.textproto")
	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Fprintln(stderr, "sheaf init: sheaf.textproto already exists — refusing to overwrite")
		return 2
	}
	cfgBody, rulesBody, ok := templateBody(template)
	if !ok {
		fmt.Fprintf(stderr, "sheaf init: unknown template %q\n", template)
		return 2
	}
	// Auto-detect a docs/ tree. If the project ships narrative docs but the
	// chosen template scaffolds no doc_parser, pre-populate a markdown
	// doc_parser so the concept-doc surface isn't a false "silent" on the
	// first scan — an unconfigured doc corpus reads as a missing API, not an
	// undocumented one. Conservative starter glob; the user narrows it per
	// library. A template that already declares a doc_parser is left alone.
	docsDetected := false
	if !strings.Contains(cfgBody, "doc_parser") {
		if fi, err := os.Stat(filepath.Join(repoPath, "docs")); err == nil && fi.IsDir() {
			cfgBody = appendMarkdownDocParser(cfgBody)
			docsDetected = true
		}
	}
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.WriteFile(rulesPath, []byte(rulesBody), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Bootstrapped Sheaf config from template %q:\n", template)
	fmt.Fprintf(stdout, "  → %s\n", cfgPath)
	fmt.Fprintf(stdout, "  → %s\n", rulesPath)
	if docsDetected {
		fmt.Fprintln(stdout, "\nDetected a docs/ tree — added a markdown doc_parser (docs/**/*.md) so the")
		fmt.Fprintln(stdout, "concept-doc surface is populated on the first scan. Narrow the globs per library as needed.")
	}
	fmt.Fprintln(stdout, "\nNext: edit project.name in sheaf.textproto, then run `sheaf doctor`.")
	return 0
}

// appendMarkdownDocParser appends a top-level markdown doc_parser block to a
// textproto config body, matching the hand-formatted one-liner style of the
// init templates. Used by runInit when a docs/ tree is detected and the
// chosen template declares no doc_parser of its own.
func appendMarkdownDocParser(body string) string {
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body +
		"\n# Auto-added by `sheaf init`: a docs/ tree was detected. Narrow per library as needed.\n" +
		"doc_parser { name: \"markdown\" markdown { include: \"docs/**/*.md\" } }\n"
}

// ===========================================================
// Shared helpers
// ===========================================================

// Aliased to avoid pulling categorization package into a non-test file.
type categorizationpb_Rules = ourCategorizationRules

// runFullPipeline loads config + rules, runs the orchestrator end to end.
func runFullPipeline(stderr io.Writer, configPath, repoPath string) (*orchestrator.Result, int) {
	cfgPath, rulesPath, err := resolveConfigPaths(configPath, repoPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 3
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 3
	}
	var rules *categorizationpb_Rules
	if _, err := os.Stat(rulesPath); err == nil {
		r, err := config.LoadRules(rulesPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return nil, 3
		}
		rules = r
	}
	o, err := orchestrator.New(cfg, rules, repoPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 3
	}
	res, err := o.Run(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	return res, 0
}

func parseSeverity(s string) commonpb.Severity {
	switch strings.ToUpper(s) {
	case "INFO":
		return commonpb.Severity_INFO
	case "WARNING", "WARN":
		return commonpb.Severity_WARNING
	case "ERROR":
		return commonpb.Severity_ERROR
	}
	return commonpb.Severity_INFO
}

// printRefBucket renders one reference adapter's refs as indented
// lines beneath a "Reference docs (N):" header. Skips empty lists so
// callers don't need to guard.
func printRefBucket(stdout io.Writer, name string, list []*commonpb.DocRef) {
	for _, ref := range list {
		fmt.Fprintf(stdout, "  %-10s %s:%d  (%s, %d words)  %s\n",
			name+":", ref.GetPath(), ref.GetLine(),
			shortSubstance(ref.GetSubstance()), ref.GetWords(), ref.GetUrl())
	}
}

// sortedAdapterKeys returns the by_adapter map's keys in
// deterministic order for stable CLI output.
func sortedAdapterKeys(r *coveragepb.DocCoverage_Reference) []string {
	if r == nil || len(r.GetByAdapter()) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.GetByAdapter()))
	for k := range r.GetByAdapter() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func shortSubstance(s commonpb.Substance) string {
	switch s {
	case commonpb.Substance_ABSENT:
		return "ABSENT"
	case commonpb.Substance_SIGNATURE_ONLY:
		return "SIGNATURE_ONLY"
	case commonpb.Substance_PARTIAL:
		return "PARTIAL"
	case commonpb.Substance_SUBSTANTIVE:
		return "SUBSTANTIVE"
	}
	return "?"
}

func truncMulti(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func profileCounts(p *coveragepb.CoverageProfile) (tests, docs, examples int) {
	if t := p.GetTests(); t != nil {
		tests = len(t.GetUnit()) + len(t.GetIntegration()) + len(t.GetE2E()) + len(t.GetCtf()) +
			len(t.GetPerformance()) + len(t.GetFuzz()) + len(t.GetGolden())
	}
	if d := p.GetDocs(); d != nil {
		if r := d.GetReference(); r != nil {
			docs += refs.CountReferenceRefs(r)
		}
		docs += len(d.GetConcept()) + len(d.GetTutorial()) + len(d.GetReleaseNotes()) + len(d.GetFaq())
		if g := d.GetGuide(); g != nil {
			docs += len(g.GetMigration()) + len(g.GetTroubleshooting()) + len(g.GetCookbook())
		}
	}
	if e := p.GetExamples(); e != nil {
		examples = len(e.GetInTree()) + len(e.GetInDocs()) + len(e.GetExternal())
	}
	return
}

// ===========================================================
// Templates shipped with `sheaf init`.
// ===========================================================

// Init templates ship on disk under internal/cli/templates/<name>/ and
// are embedded into the binary at build time, so `sheaf init --template
// fuchsia-internal` works on a fresh `go install` with no external
// files. To add a new template, drop a new <name>/ subdirectory under
// internal/cli/templates/ — the embed directive picks it up
// automatically; the templateBody switch below also needs the new
// case so an unknown name still errors cleanly.
//
//go:embed all:templates
var initTemplatesFS embed.FS

func templateBody(name string) (cfg, rules string, ok bool) {
	switch name {
	case "minimal", "argh-cli", "fuchsia-internal":
		// known template; fall through to the file read
	default:
		return "", "", false
	}
	cfgBytes, err := initTemplatesFS.ReadFile("templates/" + name + "/sheaf.textproto")
	if err != nil {
		return "", "", false
	}
	rulesBytes, err := initTemplatesFS.ReadFile("templates/" + name + "/categorization-rules.textproto")
	if err != nil {
		return "", "", false
	}
	return string(cfgBytes), string(rulesBytes), true
}
