package grounding

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"time"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// BuildConfig is the in-process entry point: it loads the sheaf config,
// runs the scan pipeline to get the contract corpus, reads the configured
// concept docs off disk, runs the Grounding detector, and returns the
// Report. This is the producer behind `emit-grounding` and any future MCP
// `grounding` op — no server round trip.
//
// docGlobs overrides the concept-doc include set. When empty, the globs
// are taken from the config's doc_parser markdown include lists; if the
// config declares none (the driver-framework config, for instance, has no
// doc_parser), the caller MUST pass docGlobs or no docs are scanned. The
// scan is rooted at repoRoot and matched the same way the adapters match.
func BuildConfig(ctx context.Context, configPath, repoRoot, library, libraryLabel string, docGlobs []string, suppressPath string) (*Report, error) {
	if configPath == "" {
		return nil, fmt.Errorf("grounding.BuildConfig: empty config path")
	}
	if library == "" {
		return nil, fmt.Errorf("grounding.BuildConfig: empty library")
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	// Run the scan pipeline for the contract corpus. Categorization rules
	// are optional and irrelevant to grounding (we only need elements), so
	// we run the orchestrator directly with nil rules.
	o, err := orchestrator.New(cfg, nil, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("grounding.BuildConfig: orchestrator: %w", err)
	}
	res, err := o.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("grounding.BuildConfig: scan: %w", err)
	}
	elements := res.Corpus.Elements()

	// Resolve concept-doc globs: explicit override wins; else the config's
	// markdown doc_parser includes.
	include, exclude := docGlobsFromConfig(cfg)
	if len(docGlobs) > 0 {
		include = docGlobs
	}
	docs, err := readDocs(repoRoot, include, exclude)
	if err != nil {
		return nil, fmt.Errorf("grounding.BuildConfig: read docs: %w", err)
	}

	supp, err := LoadSuppression(suppressPath)
	if err != nil {
		return nil, fmt.Errorf("grounding.BuildConfig: suppression: %w", err)
	}

	libDisplay := libraryLabel
	if libDisplay == "" {
		libDisplay = projectDisplay(cfg, library)
	}

	return Build(Options{
		Library:        library,
		LibraryDisplay: libDisplay,
		Repo:           projectRepo(cfg),
		// Stamp the scanned repo's git short-sha at the source so EVERY
		// consumer (emit-grounding -> .grounding.json -> rollup, and the
		// in-process concept-docs path) carries the commit through the
		// rep.Commit -> view.Commit -> {{shortsha .Commit}} plumbing. Empty
		// for a non-git repoRoot (e.g. unit/fixture temp dirs), keeping
		// those scans deterministic.
		Commit:      gitShortCommit(repoRoot),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Elements:    elements,
		Docs:        docs,
		Suppress:    supp,
	})
}

// docGlobsFromConfig collects the include/exclude globs from every
// markdown doc_parser in the config. RST parsers are skipped (this detector
// scans Markdown; RST support can follow the same shape later).
func docGlobsFromConfig(cfg *configpb.Config) (include, exclude []string) {
	for _, dp := range cfg.GetDocParser() {
		if md := dp.GetMarkdown(); md != nil {
			include = append(include, md.GetInclude()...)
			exclude = append(exclude, md.GetExclude()...)
		}
	}
	return include, exclude
}

// readDocs walks repoRoot for files matching include/exclude and returns
// them as in-memory Docs (repo-relative path + bytes), sorted by path for
// deterministic finding-ID assignment.
func readDocs(repoRoot string, include, exclude []string) ([]Doc, error) {
	if len(include) == 0 {
		return nil, nil
	}
	var docs []Doc
	err := adapters.WalkMatching(repoRoot, include, exclude, func(rel string, _ fs.DirEntry) error {
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

// projectDisplay returns the rendered library label: the config project's
// display_name when set, else the bare library name.
func projectDisplay(cfg *configpb.Config, library string) string {
	if p := cfg.GetProject(); p != nil && p.GetDisplayName() != "" {
		return p.GetDisplayName()
	}
	return library
}

// projectRepo returns the config project's name as the repo stamp.
func projectRepo(cfg *configpb.Config) string {
	if p := cfg.GetProject(); p != nil {
		return p.GetName()
	}
	return ""
}
