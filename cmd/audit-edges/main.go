// audit-edges is a THROWAWAY analysis harness for the Phase 3 precision
// audit of the docs.concepts (concept-docs) surface. It mirrors
// conceptdoc.BuildConfig's pipeline (load config -> run orchestrator for the
// contract corpus -> resolve the concept-docs globs -> read the markdown
// docs) but, instead of rolling up to coverage, it dumps EVERY anchored
// mention as a flat JSON record including the anchor kind, anchor detail, the
// verbatim surface token, the source path+line, the section path, and the
// excerpt — the fields conceptdoc.docClaimFor drops on the floor and which a
// precision audit needs to stratify and judge.
//
// It adds a new cmd only; it touches no shipped code. Not wired into the
// build's release surface — purely an audit instrument.
//
// Usage:
//
//	audit-edges --config <cfg.textproto> --repo <root> --library <name>
//	            [--doc-glob '<glob>' ...] [--doc-exclude '<glob>' ...]
//	            [-o <out.json>]
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
	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/grounding"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// edge is one anchored mention, flattened for sampling/judging.
type edge struct {
	ElementID    string `json:"element_id"`
	Display      string `json:"display"`
	Kind         string `json:"kind"`
	Anchor       string `json:"anchor"`        // qualified_mention | link | defined_term
	AnchorDetail string `json:"anchor_detail"` // "named X" / "backticked `X`" / "qualified X" / ...
	Token        string `json:"token"`         // verbatim surface token as written
	DocPath      string `json:"doc_path"`
	Line         int    `json:"line"`
	SectionPath  string `json:"section_path"`
	Excerpt      string `json:"excerpt"`
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs2 := flag.NewFlagSet("audit-edges", flag.ContinueOnError)
	var (
		configPath  string
		repoRoot    string
		library     string
		out         string
		docGlobs    multiFlag
		docExcludes multiFlag
	)
	fs2.StringVar(&configPath, "config", "", "sheaf .textproto config (required)")
	fs2.StringVar(&repoRoot, "repo", ".", "repo root to scan")
	fs2.StringVar(&library, "library", "", "library to report on (\"\" = all in corpus)")
	fs2.StringVar(&out, "o", "", "output JSON path (default stdout)")
	fs2.Var(&docGlobs, "doc-glob", "doc include glob (repeatable; overrides config)")
	fs2.Var(&docExcludes, "doc-exclude", "doc exclude glob (repeatable)")
	if err := fs2.Parse(args); err != nil {
		return 2
	}
	if configPath == "" {
		fmt.Fprintln(os.Stderr, "audit-edges: --config required")
		return 2
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-edges: load config: %v\n", err)
		return 1
	}
	o, err := orchestrator.New(cfg, nil, repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-edges: orchestrator: %v\n", err)
		return 1
	}
	res, err := o.Run(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-edges: scan: %v\n", err)
		return 1
	}
	elements := res.Corpus.Elements()

	include, exclude := docGlobsFromConfig(cfg)
	if len(docGlobs) > 0 {
		include = docGlobs
		exclude = docExcludes
	} else if len(docExcludes) > 0 {
		exclude = append(exclude, docExcludes...)
	}
	docs, err := readDocs(repoRoot, include, exclude)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit-edges: read docs: %v\n", err)
		return 1
	}

	mentions := grounding.AnchoredMentions(library, elements, docs, nil)

	edges := make([]edge, 0, len(mentions))
	for i := range mentions {
		m := &mentions[i]
		edges = append(edges, edge{
			ElementID:    m.ElementID,
			Display:      m.ElementDisplay,
			Kind:         m.ElementKind,
			Anchor:       string(m.Anchor),
			AnchorDetail: m.AnchorDetail,
			Token:        m.Token,
			DocPath:      m.DocPath,
			Line:         m.Line,
			SectionPath:  strings.Join(m.SectionPath, " > "),
			Excerpt:      m.Excerpt,
		})
	}

	type envelope struct {
		Library     string `json:"library"`
		DocsScanned int    `json:"docs_scanned"`
		EdgesTotal  int    `json:"edges_total"`
		Edges       []edge `json:"edges"`
	}
	env := envelope{Library: library, DocsScanned: len(docs), EdgesTotal: len(edges), Edges: edges}

	var w *os.File
	if out == "" {
		w = os.Stdout
	} else {
		if dir := filepath.Dir(out); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		f, e := os.Create(out)
		if e != nil {
			fmt.Fprintf(os.Stderr, "audit-edges: create %s: %v\n", out, e)
			return 1
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		fmt.Fprintf(os.Stderr, "audit-edges: encode: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "audit-edges: %s — %d docs, %d anchored edges\n", library, len(docs), len(edges))
	return 0
}

// docGlobsFromConfig mirrors conceptdoc.docGlobsFromConfig: prefer the
// "concept-docs" markdown doc_parser; else fall back to plain "markdown"
// parsers. RST is ignored.
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

// readDocs mirrors conceptdoc.readDocs: walk include/exclude, keep only .md /
// .markdown, return path+bytes sorted by path.
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
