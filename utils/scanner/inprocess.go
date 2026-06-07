// In-process render path. The scanner CLI talks to a running
// `sheaf serve` over HTTP; the monorepo fan-out runner instead drives
// a scan + render entirely in-process, without standing up a server
// per manifest entry. Render is the shared entry point: it loads the
// config, runs the orchestrator, projects the corpus into the same
// Snapshot shape the MCP server emits, and renders the HTML report.
package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheaf-data/sheaf/internal/adapters/conceptdoc"
	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/librarysnapshot"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// Render runs the scan + render pipeline programmatically. Equivalent
// to invoking the scanner binary against an in-process `sheaf serve`,
// but without the HTTP roundtrip. Returns the element count and the
// bridged count from the rendered report.
//
// configPath is the sheaf.textproto for the scan target. A sibling
// categorization-rules.textproto, if present in repoRoot, is loaded
// to drive categorization (matching `sheaf scan`'s own behavior).
// The repoRoot, library, ecosystem, sourceURLTemplate, and outputPath
// arguments map 1:1 to the existing scanner CLI flags. library accepts
// a comma-separated list to roll several libraries into one report,
// just like the CLI's --library flag; libraryLabel sets the rendered
// label for the multi-library case.
func Render(ctx context.Context, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, outputPath string) (elementCount, bridgedCount int, err error) {
	st, err := RenderStats(ctx, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, "", outputPath, nil)
	if err != nil {
		return 0, 0, err
	}
	return st.Total, st.Bridged, nil
}

// NavContext, when attached to a ReportData, renders the run-switcher +
// home link in the report's sticky strip (monorepo fan-out). Hrefs are
// relative by default, or absolute when the fan-out ran with --base-url
// (so a single shared report still navigates to its run). nil for a
// standalone report.
type NavContext struct {
	IndexHref string
	Position  int
	Total     int
	Commit    string
	Groups    []NavGroup
	// ConceptDocsHref, when non-empty, is threaded onto the rendered
	// ReportData so the fan-out's per-entry concept-doc reach-stats link
	// lights up — the manifest-fan-out twin of cmd/scanner's
	// --concept-docs-href flag (which only the single-report CLI sets).
	ConceptDocsHref string
}

type NavGroup struct {
	Name string
	Libs []NavLib
}

type NavLib struct {
	Name    string
	Href    string
	Current bool
}

// LibraryStats is the per-library coverage summary the monorepo index
// renders. Each per-surface field counts live elements carrying at
// least one of that surface. Completeness is the bridge-completeness
// distribution over the three core surfaces (docs + tests + examples),
// indexed by how many of the three an element carries — index 0 is an
// orphan (no doc/test/example), index 3 is fully bridged on the triple.
//
// The conditional workflow surface is reported (Workflows) but
// intentionally does NOT gate completeness: a library strong on the
// triple is never dragged toward "unbridged" by a thin workflow signal.
// (This is the deliberate split from the per-report bridged predicate,
// which tightens to require a workflow when the workflows adapter is
// configured — see compute.go.)
type LibraryStats struct {
	Total   int
	Bridged int // the report's own bridged predicate, kept for back-compat
	Docs    int
	Tests   int
	// Examples and Workflows are the underlying per-surface counts. The
	// index renderer presents them merged as "usage" via Usage; we keep
	// the splits here so per-report rendering and analyzers that care
	// about the distinction (e.g. the conditional-workflow gate) still
	// work, and so we never have to recompute the union from a sum that
	// would double-count elements with both.
	Examples     int
	Workflows    int
	Usage        int // elements with example > 0 OR workflow > 0 (union, not sum)
	Completeness [4]int
	// Lag is the per-library doc-staleness distribution (median +
	// quartiles + per-pair sorted values for per-domain re-percentiling).
	Lag LagResult
}

// RenderStats runs the same scan + render pipeline as Render and returns
// the per-library coverage stats the monorepo index consumes.
//
// rulesPath, when non-empty, names the categorization-rules.textproto to
// load directly (the fan-out runner passes each entry's own sibling rules
// so concurrent entries never clobber a shared repoRoot file). When empty,
// rules are loaded from repoRoot/categorization-rules.textproto — the
// historical single-render / `sheaf scan` convention.
func RenderStats(ctx context.Context, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, rulesPath, outputPath string, nav *NavContext) (LibraryStats, error) {
	r, err := renderInternal(ctx, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, rulesPath, outputPath, nav)
	if err != nil {
		return LibraryStats{}, err
	}
	return computeLibraryStats(r), nil
}

// computeLibraryStats derives per-surface coverage + the triple-based
// completeness distribution from the live element rows. Removed elements
// are excluded; the distribution sums to Total by construction.
func computeLibraryStats(r *ReportData) LibraryStats {
	st := LibraryStats{Bridged: r.Bridged, Lag: r.Lag}
	for i := range r.Methods {
		m := &r.Methods[i]
		if m.Removed {
			continue
		}
		st.Total++
		present := 0
		if m.Concept > 0 {
			st.Docs++
			present++
		}
		if m.Test > 0 {
			st.Tests++
			present++
		}
		if m.Example > 0 {
			st.Examples++
			present++
		}
		if m.Workflow > 0 {
			st.Workflows++
		}
		// Usage is the union — covered by an example OR a workflow.
		// Counted once per element regardless of how many of the two it
		// has, so 100% is reachable without double-counting.
		if m.Example > 0 || m.Workflow > 0 {
			st.Usage++
		}
		st.Completeness[present]++
	}
	return st
}

// BuildSnapshot runs the scan pipeline in-process and returns the merged
// library Snapshot — the same data the report builder consumes and the MCP
// server's library_snapshot op emits, with no server round trip. library
// accepts a comma-separated list to roll several libraries into one
// snapshot; libraryLabel sets the rolled-up Library label (defaults to the
// comma-joined names). This is the producer behind `sheaf snapshot` and the
// render path's own data step.
//
// rulesPath, when non-empty, names the categorization-rules.textproto to
// load directly. When empty, rules load from
// repoRoot/categorization-rules.textproto (the historical convention shared
// with `sheaf scan`). The fan-out runner passes each entry's own sibling
// rules path so concurrent entries never write into a shared repoRoot file.
func BuildSnapshot(ctx context.Context, configPath, repoRoot, library, libraryLabel, rulesPath string) (*Snapshot, error) {
	if configPath == "" {
		return nil, fmt.Errorf("scanner.BuildSnapshot: empty config path")
	}
	if library == "" {
		return nil, fmt.Errorf("scanner.BuildSnapshot: empty library")
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	// Rules are optional; the indexer runs without categorization when
	// absent — same convention as `sheaf scan`. An empty rulesPath
	// preserves that convention by resolving against repoRoot; a non-empty
	// rulesPath is loaded as given (the fan-out's per-entry sibling rules).
	if rulesPath == "" {
		rulesPath = filepath.Join(repoRoot, "categorization-rules.textproto")
	}
	rules, rerr := loadOptionalRules(rulesPath)
	if rerr != nil {
		return nil, rerr
	}

	o, err := orchestrator.New(cfg, rules, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("scanner.BuildSnapshot: orchestrator: %w", err)
	}
	res, err := o.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("scanner.BuildSnapshot: scan: %w", err)
	}

	names := splitLibraryList(library)
	var merged *Snapshot
	for _, name := range names {
		s := snapshotFromResult(res, cfg, name)
		if merged == nil {
			merged = s
		} else {
			merged.Elements = append(merged.Elements, s.Elements...)
			merged.Profiles = append(merged.Profiles, s.Profiles...)
			merged.Findings = append(merged.Findings, s.Findings...)
		}
	}
	if merged == nil {
		merged = &Snapshot{SchemaVersion: librarysnapshot.SchemaVersion, Library: library}
	}
	if len(names) > 1 {
		if libraryLabel != "" {
			merged.Library = libraryLabel
		} else {
			merged.Library = strings.Join(names, ",")
		}
	}

	// Additive docs.concepts surface. When the config declares a markdown
	// doc_parser (the concept-doc source), run the ANCHORED-ONLY engine over
	// those narrative docs and graft the per-element claims onto the snapshot
	// profiles' docs.concepts bucket — the surface countConceptDoc reads. This
	// is deliberately the conceptdoc engine (grounding.AnchoredMentions), NOT
	// the loose markdown reference adapter: a bare prose collision must never
	// attribute. The detection runs across the whole corpus (Library="") and
	// the injector only writes the buckets for elements actually present in
	// this snapshot, so a multi-library domain config attributes correctly. A
	// config with no doc_parser yields a nil result and leaves every element
	// not-covered — strictly additive, the existing `///`-fed Concept surface
	// is untouched. A detection failure must not fail the scan: the narrative
	// surface is supplemental, so we drop it and keep the report.
	if cdRes, cdErr := conceptdoc.DetectForConfig(cfg, repoRoot, "", res.Corpus.Elements(), ""); cdErr == nil && cdRes != nil {
		// A concept-doc source was configured AND scanned — gate the report's
		// reach line on. Graft both the per-element claim buckets AND the
		// clear/ambiguous verdict (the partition the reach line rolls up).
		merged.ConceptDocSource = true
		conceptdoc.InjectResultIntoProfiles(merged.Profiles, cdRes)
	}

	// Record each rendered_reference surface's docs_dir so the
	// render-from-snapshot path can resolve a doc's git timestamp in the
	// repo that tracks it (cross-repo doc-lag). Keyed by adapter name.
	if merged != nil {
		if dirs := docSurfaceDirs(cfg); len(dirs) > 0 {
			merged.DocSurfaceDirs = dirs
		}
	}
	return merged, nil
}

// docSurfaceDirs extracts each rendered_reference surface's docs_dir
// from the config, keyed by adapter name ("workflows", "markdowncli").
// These travel in the Snapshot so the render-from-snapshot path can read
// a doc's git timestamp in the repo that actually tracks it — the basis
// for cross-repo doc-lag (e.g. github/docs guides vs cli/cli code).
func docSurfaceDirs(cfg *configpb.Config) map[string]string {
	out := map[string]string{}
	for _, rr := range cfg.GetRenderedReference() {
		switch rr.GetName() {
		case "workflows":
			if d := rr.GetWorkflows().GetDocsDir(); d != "" {
				out["workflows"] = d
			}
		case "markdowncli":
			if d := rr.GetMarkdowncli().GetDocsDir(); d != "" {
				out["markdowncli"] = d
			}
		}
	}
	return out
}

// buildReportData runs the scan pipeline and returns the built
// ReportData with nav attached. No file is written. Shared by the
// file-writing path (renderInternal) and the in-memory path
// (RenderStatsString).
func buildReportData(ctx context.Context, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, rulesPath string, nav *NavContext) (*ReportData, error) {
	if configPath == "" {
		return nil, fmt.Errorf("scanner.Render: empty config path")
	}
	if library == "" {
		return nil, fmt.Errorf("scanner.Render: empty library")
	}
	merged, err := BuildSnapshot(ctx, configPath, repoRoot, library, libraryLabel, rulesPath)
	if err != nil {
		return nil, err
	}
	// The cold path and the render-from-snapshot path build the report the
	// same way (same BuildReportWithOptions call); sharing reportFromSnapshot
	// here guarantees a cached snapshot replays to identical bytes.
	return reportFromSnapshot(merged, repoRoot, ecosystem, sourceURLTemplate, nav), nil
}

// reportFromSnapshot builds the ReportData from an ALREADY-COMPUTED
// *Snapshot, with nav attached — no orchestrator.Run, no re-scan. It is
// the render-from-snapshot twin of buildReportData: the file-writing and
// in-memory render-from-snapshot paths both go through it so they cannot
// drift from the cold path's BuildReportWithOptions call. The snapshot
// already carries the merged library set and DocSurfaceDirs the cold path
// stamps in BuildSnapshot, so caching the snapshot and replaying it here
// reproduces the same report bytes (modulo the always-current GeneratedAt
// timestamp and any template/UI change, which render correctly precisely
// because render always re-runs).
func reportFromSnapshot(snap *Snapshot, repoRoot, ecosystem, sourceURLTemplate string, nav *NavContext) *ReportData {
	absRepoRoot := ""
	if repoRoot != "" {
		if abs, aerr := filepath.Abs(repoRoot); aerr == nil {
			absRepoRoot = abs
		}
	}
	commit := ""
	if nav != nil {
		commit = nav.Commit
	}
	r := BuildReportWithOptions(snap, ecosystem,
		time.Now().UTC().Format("2006-01-02 15:04 UTC"), "HEAD",
		sourceURLTemplate, absRepoRoot, "full", commit)
	r.Nav = nav
	// Thread the per-entry concept-doc href the same way commit is threaded:
	// the single-report CLI sets r.ConceptDocsHref from --concept-docs-href,
	// the fan-out carries it on the NavContext per manifest entry.
	if nav != nil && nav.ConceptDocsHref != "" {
		r.ConceptDocsHref = nav.ConceptDocsHref
	}
	return r
}

// RenderStatsFromSnapshot renders a report to outputPath from a given
// in-memory *Snapshot and returns the per-library stats — WITHOUT running
// the orchestrator. This is the cheap "render" half of the fan-out's
// get-snapshot-then-render split: a cache-hit entry loads its persisted
// Snapshot and calls this instead of paying for a re-scan. The output is
// byte-identical to the cold RenderStats path for the same snapshot, save
// the always-current generated-at timestamp.
//
// rulesPath is accepted for signature symmetry with RenderStats but is
// unused here: categorization was already resolved into the snapshot's
// elements when it was first built, so there is nothing left to load.
func RenderStatsFromSnapshot(ctx context.Context, snap *Snapshot, repoRoot, ecosystem, sourceURLTemplate, rulesPath, outputPath string, nav *NavContext) (LibraryStats, error) {
	if snap == nil {
		return LibraryStats{}, fmt.Errorf("scanner.RenderStatsFromSnapshot: nil snapshot")
	}
	r := reportFromSnapshot(snap, repoRoot, ecosystem, sourceURLTemplate, nav)
	if outputPath == "" {
		outputPath = filepath.Join(".", safeRenderFilename(r.Library)+"-report.html")
	}
	if dir := filepath.Dir(outputPath); dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return LibraryStats{}, fmt.Errorf("scanner.RenderStatsFromSnapshot: create output dir %s: %w", dir, mkErr)
		}
	}
	f, err := os.Create(outputPath)
	if err != nil {
		return LibraryStats{}, fmt.Errorf("scanner.RenderStatsFromSnapshot: create %s: %w", outputPath, err)
	}
	defer f.Close()
	if err := RenderHTML(f, r); err != nil {
		return LibraryStats{}, fmt.Errorf("scanner.RenderStatsFromSnapshot: render: %w", err)
	}
	return computeLibraryStats(r), nil
}

// RenderStatsStringFromSnapshot renders a report to an HTML string from a
// given in-memory *Snapshot (no file, no re-scan) and returns it with the
// stats. The single-file bundle path's cache-hit twin of RenderStatsString.
func RenderStatsStringFromSnapshot(ctx context.Context, snap *Snapshot, repoRoot, ecosystem, sourceURLTemplate, rulesPath string, nav *NavContext) (string, LibraryStats, error) {
	if snap == nil {
		return "", LibraryStats{}, fmt.Errorf("scanner.RenderStatsStringFromSnapshot: nil snapshot")
	}
	r := reportFromSnapshot(snap, repoRoot, ecosystem, sourceURLTemplate, nav)
	var buf strings.Builder
	if err := RenderHTML(&buf, r); err != nil {
		return "", LibraryStats{}, fmt.Errorf("scanner.RenderStatsStringFromSnapshot: render: %w", err)
	}
	return buf.String(), computeLibraryStats(r), nil
}

// RenderStatsString renders the report to an HTML string (no file) and
// returns it with the stats. Used by the single-file bundle path.
//
// rulesPath follows the same convention as RenderStats: non-empty loads
// that categorization-rules.textproto directly; empty resolves against
// repoRoot.
func RenderStatsString(ctx context.Context, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, rulesPath string, nav *NavContext) (string, LibraryStats, error) {
	r, err := buildReportData(ctx, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, rulesPath, nav)
	if err != nil {
		return "", LibraryStats{}, err
	}
	var buf strings.Builder
	if err := RenderHTML(&buf, r); err != nil {
		return "", LibraryStats{}, fmt.Errorf("scanner.Render: render: %w", err)
	}
	return buf.String(), computeLibraryStats(r), nil
}

// renderInternal builds the ReportData and writes the HTML to
// outputPath. Shared by Render (back-compat int return) and RenderStats.
func renderInternal(ctx context.Context, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, rulesPath, outputPath string, nav *NavContext) (*ReportData, error) {
	r, err := buildReportData(ctx, configPath, repoRoot, library, libraryLabel, ecosystem, sourceURLTemplate, rulesPath, nav)
	if err != nil {
		return nil, err
	}
	if outputPath == "" {
		outputPath = filepath.Join(".", safeRenderFilename(r.Library)+"-report.html")
	}
	if dir := filepath.Dir(outputPath); dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return nil, fmt.Errorf("scanner.Render: create output dir %s: %w", dir, mkErr)
		}
	}
	f, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("scanner.Render: create %s: %w", outputPath, err)
	}
	defer f.Close()
	if err := RenderHTML(f, r); err != nil {
		return nil, fmt.Errorf("scanner.Render: render: %w", err)
	}
	return r, nil
}

// splitLibraryList parses a comma-separated --library value. Empty
// entries are dropped; surrounding whitespace is trimmed. A
// single-name input round-trips as a one-element slice. Mirrors the
// CLI helper of the same name.
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

func safeRenderFilename(s string) string {
	repl := strings.NewReplacer("/", "_", " ", "_", string(os.PathSeparator), "_")
	return repl.Replace(s)
}

// loadOptionalRules loads categorization rules when the file exists,
// returning (nil, nil) when absent. A genuine parse error is returned.
func loadOptionalRules(path string) (*categorizationpb.Rules, error) {
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, nil
	}
	return config.LoadRules(path)
}

// snapshotFromResult projects an orchestrator Result into the Snapshot
// shape the report builder consumes and the MCP server's library_snapshot
// op emits. The projection logic itself lives in internal/librarysnapshot
// so the in-process path here and the server-backed op cannot drift; this
// wrapper assembles the struct and stamps the schema version.
func snapshotFromResult(res *orchestrator.Result, cfg *configpb.Config, library string) *Snapshot {
	proj := librarysnapshot.Project(res.Corpus, res.Findings, cfg, library)
	return &Snapshot{
		SchemaVersion:    librarysnapshot.SchemaVersion,
		Library:          library,
		Elements:         proj.Elements,
		Profiles:         proj.Profiles,
		Findings:         proj.Findings,
		Analyzers:        proj.Analyzers,
		SurfacesRequired: proj.SurfacesRequired,
	}
}

// RenderFromResult renders the rich standard HTML report (the
// sheaf-self.html style) from an ALREADY-COMPUTED orchestrator Result,
// without re-running the scan. `sheaf scan --auto` uses this so the
// onboarding flow produces the canonical scanner report as a fourth
// artifact without paying for a second pipeline run (which, with the LLM
// tier, would re-issue model calls).
//
// library is a comma-separated list rolled into one report (like the CLI
// --library flag); an empty list renders every library present in the
// corpus. libraryLabel sets the rendered label for the multi-library
// case. ecosystem selects the masthead view (unknown/empty falls back to
// a default view).
func RenderFromResult(res *orchestrator.Result, cfg *configpb.Config, library, libraryLabel, ecosystem, repoRoot, outputPath string) (elementCount, bridgedCount int, err error) {
	if res == nil {
		return 0, 0, fmt.Errorf("scanner.RenderFromResult: nil result")
	}
	names := splitLibraryList(library)
	if len(names) == 0 {
		names = distinctLibraries(res)
	}
	var merged *Snapshot
	for _, name := range names {
		s := snapshotFromResult(res, cfg, name)
		if merged == nil {
			merged = s
		} else {
			merged.Elements = append(merged.Elements, s.Elements...)
			merged.Profiles = append(merged.Profiles, s.Profiles...)
			merged.Findings = append(merged.Findings, s.Findings...)
		}
	}
	if merged == nil {
		merged = &Snapshot{SchemaVersion: librarysnapshot.SchemaVersion, Library: libraryLabel}
	}
	if len(names) != 1 {
		if libraryLabel != "" {
			merged.Library = libraryLabel
		} else {
			merged.Library = strings.Join(names, ",")
		}
	}

	absRepoRoot := ""
	if repoRoot != "" {
		if abs, aerr := filepath.Abs(repoRoot); aerr == nil {
			absRepoRoot = abs
		}
	}
	r := BuildReportWithOptions(merged, ecosystem,
		time.Now().UTC().Format("2006-01-02 15:04 UTC"), "HEAD",
		"", absRepoRoot, "full", "")

	if outputPath == "" {
		outputPath = filepath.Join(".", safeRenderFilename(merged.Library)+"-report.html")
	}
	if dir := filepath.Dir(outputPath); dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return 0, 0, fmt.Errorf("scanner.RenderFromResult: create output dir %s: %w", dir, mkErr)
		}
	}
	f, err := os.Create(outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("scanner.RenderFromResult: create %s: %w", outputPath, err)
	}
	defer f.Close()
	if err := RenderHTML(f, r); err != nil {
		return 0, 0, fmt.Errorf("scanner.RenderFromResult: render: %w", err)
	}
	return r.Total, r.Bridged, nil
}

// distinctLibraries returns the sorted set of library names present in
// the corpus, so an unscoped --auto run rolls every library into the
// standard report.
func distinctLibraries(res *orchestrator.Result) []string {
	seen := map[string]bool{}
	for _, e := range res.Corpus.Elements() {
		if lib := e.GetLibrary(); lib != "" {
			seen[lib] = true
		}
	}
	out := make([]string, 0, len(seen))
	for lib := range seen {
		out = append(out, lib)
	}
	sort.Strings(out)
	return out
}
