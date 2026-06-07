package conceptdoc

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/grounding"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// BuildConfig is the in-process entry point for concept-doc ingestion: it
// loads the sheaf config, runs the scan pipeline to get the contract
// corpus, reads the configured narrative docs off disk, runs the anchored-
// mention engine, and returns the docs.concepts Result. This is the
// producer behind the emit-conceptdoc CLI and the path phases 2/3 call —
// no server round trip.
//
// Config-driven doc source: the narrative-doc globs are declared in the
// project config the SAME way Grounding declares them — via the markdown
// doc_parser include/exclude lists. That keeps Phase 1 additive (no proto
// change) and lets one config drive both the existing markdown coverage and
// the new docs.concepts surface off the same declared globs. When the
// config declares no markdown doc_parser (the driver-framework example has
// none), the caller MUST pass docGlobs explicitly or no docs are scanned.
// An explicit docGlobs argument always overrides the config globs.
//
// The scan is rooted at repoRoot and globs are matched exactly as the
// adapters match (internal/adapters.WalkMatching).
func BuildConfig(ctx context.Context, configPath, repoRoot, library, libraryLabel string, docGlobs, docExcludes []string, suppressPath string) (*Result, error) {
	if configPath == "" {
		return nil, fmt.Errorf("conceptdoc.BuildConfig: empty config path")
	}
	if library == "" {
		return nil, fmt.Errorf("conceptdoc.BuildConfig: empty library")
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	// Run the scan pipeline for the contract corpus. Categorization rules
	// are irrelevant to attribution (we only need the element set), so we
	// run the orchestrator directly with nil rules.
	o, err := orchestrator.New(cfg, nil, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("conceptdoc.BuildConfig: orchestrator: %w", err)
	}
	res, err := o.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("conceptdoc.BuildConfig: scan: %w", err)
	}
	elements := res.Corpus.Elements()

	// Resolve doc globs: explicit override wins; else the config's markdown
	// doc_parser includes/excludes.
	include, exclude := docGlobsFromConfig(cfg)
	if len(docGlobs) > 0 {
		include = docGlobs
		exclude = docExcludes
	} else if len(docExcludes) > 0 {
		exclude = append(exclude, docExcludes...)
	}
	docs, err := readDocs(repoRoot, include, exclude)
	if err != nil {
		return nil, fmt.Errorf("conceptdoc.BuildConfig: read docs: %w", err)
	}

	supp, err := grounding.LoadSuppression(suppressPath)
	if err != nil {
		return nil, fmt.Errorf("conceptdoc.BuildConfig: suppression: %w", err)
	}

	libDisplay := libraryLabel
	if libDisplay == "" {
		libDisplay = projectDisplay(cfg, library)
	}

	return Detect(Options{
		Library:        library,
		LibraryDisplay: libDisplay,
		Elements:       elements,
		Docs:           docs,
		Suppress:       supp,
	}), nil
}

// DetectForConfig runs the anchored-mention engine over the config's declared
// narrative docs against an ALREADY-COMPUTED contract corpus, returning the
// docs.concepts Result. It is the integration entry point for the live scan
// pipeline (utils/scanner.BuildSnapshot): that path has already run the
// orchestrator, so re-running it via BuildConfig would scan twice. Pass the
// corpus elements directly and skip the second pipeline run.
//
// library scopes attribution exactly like Detect: "" attributes across every
// library present in elements (the right call for a multi-FIDL-library fuchsia
// domain config), a non-empty value restricts to that library. The doc globs
// come from the config's markdown doc_parser (the concept-doc source
// declaration); when the config declares none, the result is empty (no docs
// scanned) and every element reads not-covered — the correct additive default.
//
// Returns (nil, nil) — not an error — when the config declares no markdown
// doc_parser, so a caller can wire this unconditionally into every scan and
// only pay for detection when a concept-doc source is actually configured.
func DetectForConfig(cfg *configpb.Config, repoRoot, library string, elements []*contractpb.ContractElement, suppressPath string) (*Result, error) {
	if cfg == nil {
		return nil, nil
	}
	include, exclude := docGlobsFromConfig(cfg)
	if len(include) == 0 {
		// No concept-doc source declared — nothing to attribute. Not an
		// error; the docs.concepts surface simply stays empty for this scan.
		return nil, nil
	}
	docs, err := readDocs(repoRoot, include, exclude)
	if err != nil {
		return nil, fmt.Errorf("conceptdoc.DetectForConfig: read docs: %w", err)
	}
	if len(docs) == 0 {
		return nil, nil
	}
	supp, err := grounding.LoadSuppression(suppressPath)
	if err != nil {
		return nil, fmt.Errorf("conceptdoc.DetectForConfig: suppression: %w", err)
	}
	return Detect(Options{
		Library:        library,
		LibraryDisplay: projectDisplay(cfg, library),
		Elements:       elements,
		Docs:           docs,
		Suppress:       supp,
	}), nil
}

// docGlobsFromConfig collects the include/exclude globs that declare the
// concept-doc source. It prefers doc_parsers named "concept-docs" — the
// dedicated, anchored-only narrative source that the orchestrator does NOT
// route through the loose markdown reference adapter. Only when the config
// declares no "concept-docs" parser does it fall back to plain "markdown"
// parsers (back-compat: the self-scan config declares its narrative source as
// name:"markdown", and that markdown adapter's prose mentions ARE the concept
// surface there). Either way RST parsers are skipped — this engine scans
// Markdown.
//
// Keeping the two cleanly separated means a config can carry BOTH a real
// "markdown" reference parser (counted into docs.reference by the orchestrator)
// AND a "concept-docs" narrative parser (counted into docs.concepts here)
// without the two globs cross-contaminating.
func docGlobsFromConfig(cfg *configpb.Config) (include, exclude []string) {
	var conceptInclude, conceptExclude []string
	var mdInclude, mdExclude []string
	for _, dp := range cfg.GetDocParser() {
		md := dp.GetMarkdown()
		if md == nil {
			continue
		}
		if dp.GetName() == "concept-docs" {
			conceptInclude = append(conceptInclude, md.GetInclude()...)
			conceptExclude = append(conceptExclude, md.GetExclude()...)
		} else {
			mdInclude = append(mdInclude, md.GetInclude()...)
			mdExclude = append(mdExclude, md.GetExclude()...)
		}
	}
	if len(conceptInclude) > 0 {
		return conceptInclude, conceptExclude
	}
	return mdInclude, mdExclude
}

// readDocs walks repoRoot for files matching include/exclude and returns
// them as in-memory Docs (repo-relative slash path + bytes), sorted by path
// for deterministic claim ordering.
//
// Non-markdown files are skipped even if a glob matches them: this engine is a
// markdown scanner, and a broad glob (e.g. "docs/concepts/drivers/**") will
// otherwise sweep in the .png/.jpg assets that sit beside the prose. Feeding a
// binary to the scanner yields garbage tokens and an excerpt of raw bytes that
// is not valid UTF-8, which protojson then refuses to marshal. Gating on the
// .md extension keeps the corpus to actual narrative docs (the configs also
// scope their globs to **/*.md, so this is defense in depth).
func readDocs(repoRoot string, include, exclude []string) ([]Doc, error) {
	if len(include) == 0 {
		return nil, nil
	}
	var docs []Doc
	err := adapters.WalkMatching(repoRoot, include, exclude, func(rel string, _ fs.DirEntry) error {
		if !isMarkdownPath(rel) {
			return nil
		}
		body, rerr := adapters.ReadFile(repoRoot, rel)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", rel, rerr)
		}
		docs = append(docs, Doc{Path: filepath.ToSlash(rel), Body: body})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs, nil
}

// isMarkdownPath reports whether rel names a Markdown file. The narrative
// concept docs across the Fuchsia tree are uniformly .md; .markdown is
// accepted for portability. Everything else (images, code, RST, YAML) is not
// this engine's input.
func isMarkdownPath(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".md", ".markdown":
		return true
	default:
		return false
	}
}

// projectDisplay returns the rendered library label: the config project's
// display_name when set, else the bare library name.
func projectDisplay(cfg *configpb.Config, library string) string {
	if p := cfg.GetProject(); p != nil && p.GetDisplayName() != "" {
		return p.GetDisplayName()
	}
	return library
}
