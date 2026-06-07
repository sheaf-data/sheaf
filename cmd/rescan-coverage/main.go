// rescan-coverage is a THROWAWAY analysis harness for the concept-docs
// (docs.concepts) coverage re-scan. For each sheaf domain config it mirrors
// the WIRED in-process path the live scan uses (config.LoadConfig ->
// orchestrator.Run for the contract corpus -> conceptdoc.Detect over the
// config's concept-docs globs with library="" so attribution spans every
// FIDL library in the corpus, exactly as utils/scanner.BuildSnapshot does via
// DetectForConfig) and prints the per-domain coverage rollup
// (elements covered / total / %, claim count, docs scanned, edge count).
//
// It adds a new cmd only; it touches no shipped code. Run it once before the
// precision fixes to capture the Phase-2 baseline and again after to capture
// the honest post-fix numbers; the JSON it emits is diffed to produce the
// per-domain delta + corpus total + edge shrink factor.
//
// Usage:
//
//	rescan-coverage --configs <dir> --repo <root> [-o <out.json>]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/adapters/conceptdoc"
	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/grounding"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// domainCoverage is the per-config concept-docs coverage rollup.
type domainCoverage struct {
	Domain          string `json:"domain"`
	Config          string `json:"config"`
	ElementsTotal   int    `json:"elements_total"`
	ElementsCovered int    `json:"elements_covered"`
	Pct             int    `json:"pct"`
	ClaimsTotal     int    `json:"claims_total"` // anchored attribution edges (== claims)
	DocsScanned     int    `json:"docs_scanned"`
	Err             string `json:"err,omitempty"`
}

type envelope struct {
	Repo                string           `json:"repo"`
	Domains             []domainCoverage `json:"domains"`
	CorpusElementsTotal int              `json:"corpus_elements_total"`
	CorpusElementsCovrd int              `json:"corpus_elements_covered"`
	CorpusPct           int              `json:"corpus_pct"`
	CorpusClaimsTotal   int              `json:"corpus_claims_total"`
	CorpusDocsScanned   int              `json:"corpus_docs_scanned"`
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs2 := flag.NewFlagSet("rescan-coverage", flag.ContinueOnError)
	var (
		configsDir string
		repoRoot   string
		out        string
	)
	fs2.StringVar(&configsDir, "configs", "docs/examples/fuchsia-coverage-configs", "directory of .textproto domain configs")
	fs2.StringVar(&repoRoot, "repo", ".", "repo root to scan")
	fs2.StringVar(&out, "o", "", "output JSON path (default stdout)")
	if err := fs2.Parse(args); err != nil {
		return 2
	}

	entries, err := os.ReadDir(configsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rescan-coverage: read configs dir: %v\n", err)
		return 1
	}
	var cfgPaths []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".textproto") {
			continue
		}
		cfgPaths = append(cfgPaths, filepath.Join(configsDir, e.Name()))
	}
	sort.Strings(cfgPaths)

	env := envelope{Repo: repoRoot}
	for _, cp := range cfgPaths {
		dc := scanOne(cp, repoRoot)
		env.Domains = append(env.Domains, dc)
		env.CorpusElementsTotal += dc.ElementsTotal
		env.CorpusElementsCovrd += dc.ElementsCovered
		env.CorpusClaimsTotal += dc.ClaimsTotal
		env.CorpusDocsScanned += dc.DocsScanned
		fmt.Fprintf(os.Stderr, "rescan-coverage: %-28s %4d/%-4d (%3d%%)  claims=%-6d docs=%d %s\n",
			dc.Domain, dc.ElementsCovered, dc.ElementsTotal, dc.Pct, dc.ClaimsTotal, dc.DocsScanned, dc.Err)
	}
	env.CorpusPct = pct(env.CorpusElementsCovrd, env.CorpusElementsTotal)

	var w *os.File
	if out == "" {
		w = os.Stdout
	} else {
		if dir := filepath.Dir(out); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		f, e := os.Create(out)
		if e != nil {
			fmt.Fprintf(os.Stderr, "rescan-coverage: create %s: %v\n", out, e)
			return 1
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		fmt.Fprintf(os.Stderr, "rescan-coverage: encode: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "rescan-coverage: corpus %d/%d (%d%%) claims=%d docs=%d across %d domains\n",
		env.CorpusElementsCovrd, env.CorpusElementsTotal, env.CorpusPct, env.CorpusClaimsTotal, env.CorpusDocsScanned, len(env.Domains))
	return 0
}

// scanOne mirrors the wired DetectForConfig path: load config, run the
// orchestrator for the contract corpus, then run conceptdoc.Detect with
// library="" over the config's concept-docs globs.
func scanOne(configPath, repoRoot string) domainCoverage {
	dc := domainCoverage{
		Domain: strings.TrimSuffix(filepath.Base(configPath), ".textproto"),
		Config: configPath,
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		dc.Err = "load: " + err.Error()
		return dc
	}
	o, err := orchestrator.New(cfg, nil, repoRoot)
	if err != nil {
		dc.Err = "orchestrator: " + err.Error()
		return dc
	}
	res, err := o.Run(context.Background())
	if err != nil {
		dc.Err = "scan: " + err.Error()
		return dc
	}
	elements := res.Corpus.Elements()

	include, exclude := docGlobsFromConfig(cfg)
	docs, err := readDocs(repoRoot, include, exclude)
	if err != nil {
		dc.Err = "read docs: " + err.Error()
		return dc
	}
	// library="" spans every FIDL library in the corpus, exactly as the wired
	// DetectForConfig default for a multi-FIDL-library fuchsia domain config.
	out := conceptdoc.Detect(conceptdoc.Options{
		Library:  "",
		Elements: elements,
		Docs:     docs,
	})
	dc.ElementsTotal = out.Summary.ElementsTotal
	dc.ElementsCovered = out.Summary.ElementsCovered
	dc.Pct = out.Summary.ElementsPct
	dc.ClaimsTotal = out.Summary.ClaimsTotal
	dc.DocsScanned = out.Summary.DocsScanned
	return dc
}

// docGlobsFromConfig mirrors conceptdoc.docGlobsFromConfig: prefer the
// "concept-docs" markdown doc_parser; else fall back to plain "markdown".
func docGlobsFromConfig(cfg *configpb.Config) (include, exclude []string) {
	var ci, ce, mi, me []string
	for _, dp := range cfg.GetDocParser() {
		md := dp.GetMarkdown()
		if md == nil {
			continue
		}
		if dp.GetName() == "concept-docs" {
			ci = append(ci, md.GetInclude()...)
			ce = append(ce, md.GetExclude()...)
		} else {
			mi = append(mi, md.GetInclude()...)
			me = append(me, md.GetExclude()...)
		}
	}
	if len(ci) > 0 {
		return ci, ce
	}
	return mi, me
}

// readDocs mirrors conceptdoc.readDocs.
func readDocs(repoRoot string, include, exclude []string) ([]grounding.Doc, error) {
	if len(include) == 0 {
		return nil, nil
	}
	var docs []grounding.Doc
	err := adapters.WalkMatching(repoRoot, include, exclude, func(rel string, _ fs.DirEntry) error {
		switch strings.ToLower(filepath.Ext(rel)) {
		case ".md", ".markdown":
		default:
			return nil
		}
		body, rerr := adapters.ReadFile(repoRoot, rel)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", rel, rerr)
		}
		docs = append(docs, grounding.Doc{Path: filepath.ToSlash(rel), Body: body})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs, nil
}

func pct(covered, total int) int {
	if total <= 0 {
		return 0
	}
	return int(float64(covered)*100.0/float64(total) + 0.5)
}
