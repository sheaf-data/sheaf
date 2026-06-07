// Package orchestrator wires a Config + Rules into a working scan.
// Adapters are constructed from the config, run in parallel against
// the repo, and their output is collected into a Corpus.
//
// v1 does NOT yet run the indexer or analyzers from here — those
// land in subsequent build passes and will be invoked after the
// adapter phase completes.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/adapters/argh"
	"github.com/sheaf-data/sheaf/internal/adapters/bats"
	"github.com/sheaf-data/sheaf/internal/adapters/clap"
	"github.com/sheaf-data/sheaf/internal/adapters/clidoc"
	"github.com/sheaf-data/sheaf/internal/adapters/cml"
	"github.com/sheaf-data/sheaf/internal/adapters/cobra"
	"github.com/sheaf-data/sheaf/internal/adapters/cppheader"
	"github.com/sheaf-data/sheaf/internal/adapters/crd"
	"github.com/sheaf-data/sheaf/internal/adapters/doxygen"
	"github.com/sheaf-data/sheaf/internal/adapters/fidl"
	"github.com/sheaf-data/sheaf/internal/adapters/fidldoc"
	"github.com/sheaf-data/sheaf/internal/adapters/gotest"
	"github.com/sheaf-data/sheaf/internal/adapters/gtest"
	"github.com/sheaf-data/sheaf/internal/adapters/helmvalues"
	"github.com/sheaf-data/sheaf/internal/adapters/implementsmap"
	"github.com/sheaf-data/sheaf/internal/adapters/k8smanifest"
	"github.com/sheaf-data/sheaf/internal/adapters/llmextract"
	"github.com/sheaf-data/sheaf/internal/adapters/markdown"
	"github.com/sheaf-data/sheaf/internal/adapters/markdowncli"
	protoadapter "github.com/sheaf-data/sheaf/internal/adapters/proto"
	"github.com/sheaf-data/sheaf/internal/adapters/protocpp"
	"github.com/sheaf-data/sheaf/internal/adapters/pytest"
	"github.com/sheaf-data/sheaf/internal/adapters/pythontest"
	"github.com/sheaf-data/sheaf/internal/adapters/rst"
	"github.com/sheaf-data/sheaf/internal/adapters/rusttest"
	"github.com/sheaf-data/sheaf/internal/adapters/workflows"
	"github.com/sheaf-data/sheaf/internal/adapters/yamlworkflows"
	"github.com/sheaf-data/sheaf/internal/affordance"
	"github.com/sheaf-data/sheaf/internal/analyze"
	"github.com/sheaf-data/sheaf/internal/attribution"
	"github.com/sheaf-data/sheaf/internal/buildgraph"
	"github.com/sheaf-data/sheaf/internal/categorize"
	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/indexer"
	"github.com/sheaf-data/sheaf/internal/llm"
	"github.com/sheaf-data/sheaf/internal/workflowextract"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// Orchestrator owns the per-scan setup and runs the pipeline.
type Orchestrator struct {
	cfg      *configpb.Config
	rules    *categorizationpb.Rules
	repoRoot string

	// llmUsage accumulates token usage across every LLM stage.
	llmUsage *llm.UsageAccumulator

	// Resolved adapters. Built once at New(); reused across Run calls.
	contractAnchors []adapters.ContractAnchorParser
	testParsers     []adapters.TestParser
	docParsers      []adapters.DocParser
	renderedRefs    []adapters.RenderedReferenceParser
	implementsMaps  []adapters.ImplementsMapper
}

// Result is the per-scan output.
type Result struct {
	Corpus           *corpus.Corpus
	Findings         []*findingpb.Finding
	Duration         time.Duration
	IngestDuration   time.Duration
	IndexDuration    time.Duration
	AnalyzeDuration  time.Duration
	IndexStats       indexer.Stats
	AffordanceStats  affordance.Stats
	WorkflowStats    workflowextract.Stats
	AttributionStats attribution.Stats
	LLMUsage         llm.Usage // token usage across all LLM stages (0 on a fully-cached or LLM-free run)
	Warnings         []string
	AdapterErrors    []AdapterError
}

// AdapterError captures a per-adapter failure without aborting the
// whole scan. The orchestrator continues with other adapters.
type AdapterError struct {
	AdapterName string
	Err         error
}

func (a AdapterError) Error() string {
	return fmt.Sprintf("adapter %s: %v", a.AdapterName, a.Err)
}

// New constructs an Orchestrator. Returns an error if any adapter
// name in cfg is unrecognized or has an invalid per-adapter config.
// `rules` may be nil, in which case the indexer runs without
// categorization (all refs go to default buckets).
func New(cfg *configpb.Config, rules *categorizationpb.Rules, repoRoot string) (*Orchestrator, error) {
	if cfg == nil {
		return nil, errors.New("orchestrator: nil config")
	}
	o := &Orchestrator{cfg: cfg, rules: rules, repoRoot: repoRoot, llmUsage: &llm.UsageAccumulator{}}
	if err := o.resolveAdapters(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Orchestrator) resolveAdapters() error {
	// Project-level IDL prefix drives the body-parsing matchers in
	// the test and doc parsers. Empty string → fidlmatch defaults to
	// "fuchsia" for back-compat.
	idlPrefix := o.cfg.GetProject().GetIdlPrefix()
	// Test parsers
	for i, tp := range o.cfg.GetTestParser() {
		switch tp.GetName() {
		case "gtest":
			g := tp.GetGtest()
			o.testParsers = append(o.testParsers, gtest.New(gtest.Config{
				Include:         g.GetInclude(),
				Exclude:         g.GetExclude(),
				ExtraTestMacros: g.GetExtraTestMacros(),
				IDLPrefix:       idlPrefix,
			}))
		case "rust-test":
			r := tp.GetRustTest()
			o.testParsers = append(o.testParsers, rusttest.New(rusttest.Config{
				Include:             r.GetInclude(),
				Exclude:             r.GetExclude(),
				ExtraTestAttributes: r.GetExtraTestAttributes(),
				IDLPrefix:           idlPrefix,
			}))
		case "bats":
			b := tp.GetBats()
			o.testParsers = append(o.testParsers, bats.New(bats.Config{
				Include: b.GetInclude(),
				Exclude: b.GetExclude(),
			}))
		case "gotest":
			g := tp.GetGotest()
			o.testParsers = append(o.testParsers, gotest.New(gotest.Config{
				Include:    g.GetInclude(),
				Exclude:    g.GetExclude(),
				BinaryName: g.GetBinaryName(),
			}))
		case "protocpp":
			p := tp.GetProtocpp()
			o.testParsers = append(o.testParsers, protocpp.New(protocpp.Config{
				Include:                  p.GetInclude(),
				Exclude:                  p.GetExclude(),
				ExtraTestMacros:          p.GetExtraTestMacros(),
				IDLPrefix:                idlPrefix,
				ExtractFreeFunctionCalls: p.GetExtractFreeFunctionCalls(),
			}))
		case "pytest":
			py := tp.GetPytest()
			// Per-adapter idl_prefix takes precedence over the project
			// default when set — the same convention other test parsers
			// follow when they expose a config-level override.
			prefix := py.GetIdlPrefix()
			if prefix == "" {
				prefix = idlPrefix
			}
			o.testParsers = append(o.testParsers, pytest.New(pytest.Config{
				Include:       py.GetInclude(),
				Exclude:       py.GetExclude(),
				IDLPrefix:     prefix,
				ModuleAliases: py.GetModuleAliases(),
			}))
		case "python-test":
			pt := tp.GetPythonTest()
			o.testParsers = append(o.testParsers, pythontest.New(pythontest.Config{
				Include: pt.GetInclude(),
				Exclude: pt.GetExclude(),
			}))
		default:
			return fmt.Errorf("orchestrator: test_parser[%d]: unknown name %q", i, tp.GetName())
		}
	}
	// Doc parsers
	for i, dp := range o.cfg.GetDocParser() {
		switch dp.GetName() {
		case "markdown":
			m := dp.GetMarkdown()
			o.docParsers = append(o.docParsers, markdown.New(markdown.Config{
				Include:            m.GetInclude(),
				Exclude:            m.GetExclude(),
				CodeBlockLanguages: m.GetCodeBlockLanguages(),
				IDLPrefix:          idlPrefix,
			}))
		case "rst":
			r := dp.GetRst()
			o.docParsers = append(o.docParsers, rst.New(rst.Config{
				Include:            r.GetInclude(),
				Exclude:            r.GetExclude(),
				CodeBlockLanguages: r.GetCodeBlockLanguages(),
				IDLPrefix:          idlPrefix,
			}))
		case "doxygen":
			d := dp.GetDoxygen()
			o.docParsers = append(o.docParsers, doxygen.New(doxygen.Config{
				XMLDir:  d.GetXmlDir(),
				URLBase: d.GetUrlBase(),
				Include: d.GetInclude(),
				Exclude: d.GetExclude(),
			}))
		case "concept-docs":
			// Concept-docs source for the additive docs.concepts surface.
			// Deliberately NOT wired as an orchestrator doc-parser: the loose
			// markdown adapter's prose-collision matching is exactly what this
			// surface must avoid (REQUIREMENTS-concept-ingest.md decision 2).
			// The anchored-mention engine (internal/adapters/conceptdoc) reads
			// these globs from the config AFTER the scan and grafts only the
			// high-confidence anchored attributions onto the snapshot. Skipping
			// it here keeps the contract corpus and every other surface
			// untouched by the narrative-doc globs.
			continue
		default:
			return fmt.Errorf("orchestrator: doc_parser[%d]: unknown name %q", i, dp.GetName())
		}
	}
	// Contract anchors.
	for i, ca := range o.cfg.GetContractAnchor() {
		switch ca.GetName() {
		case "fidl":
			fc := ca.GetFidl()
			o.contractAnchors = append(o.contractAnchors, fidl.New(fidl.Config{
				Include: fc.GetInclude(),
				Exclude: fc.GetExclude(),
			}))
		case "argh":
			ac := ca.GetArgh()
			o.contractAnchors = append(o.contractAnchors, argh.New(argh.Config{
				CrateRoots: ac.GetCrateRoots(),
				Include:    ac.GetInclude(),
				Exclude:    ac.GetExclude(),
			}))
		case "clap":
			cc := ca.GetClap()
			o.contractAnchors = append(o.contractAnchors, clap.New(clap.Config{
				CrateRoots: cc.GetCrateRoots(),
				Include:    cc.GetInclude(),
				Exclude:    cc.GetExclude(),
			}))
		case "cobra":
			cc := ca.GetCobra()
			o.contractAnchors = append(o.contractAnchors, cobra.New(cobra.Config{
				YAMLDir:    cc.GetYamlDir(),
				Include:    cc.GetInclude(),
				Exclude:    cc.GetExclude(),
				BinaryName: cc.GetBinaryName(),
				URLBase:    cc.GetUrlBase(),
			}))
		case "proto":
			pc := ca.GetProto()
			o.contractAnchors = append(o.contractAnchors, protoadapter.New(protoadapter.Config{
				ProtocPath: pc.GetProtocPath(),
				Include:    pc.GetInclude(),
				Exclude:    pc.GetExclude(),
				ProtoPath:  pc.GetProtoPath(),
			}))
		case "cml":
			cc := ca.GetCml()
			o.contractAnchors = append(o.contractAnchors, cml.New(cml.Config{
				Include: cc.GetInclude(),
				Exclude: cc.GetExclude(),
			}))
		case "crd":
			cc := ca.GetCrd()
			o.contractAnchors = append(o.contractAnchors, crd.New(crd.Config{
				Include: cc.GetInclude(),
				Exclude: cc.GetExclude(),
			}))
		case "k8smanifest":
			cc := ca.GetK8SManifest()
			o.contractAnchors = append(o.contractAnchors, k8smanifest.New(k8smanifest.Config{
				Include: cc.GetInclude(),
				Exclude: cc.GetExclude(),
			}))
		case "helmvalues":
			cc := ca.GetHelmValues()
			o.contractAnchors = append(o.contractAnchors, helmvalues.New(helmvalues.Config{
				Include: cc.GetInclude(),
				Exclude: cc.GetExclude(),
			}))
		case "cppheader":
			cc := ca.GetCppHeader()
			// Constructed with NopHints; Run injects the real composite
			// BuildHints (from buildgraph.Run) via SetHints before the
			// anchor's Discover runs, so the IsPublic filter respects the
			// build graph.
			o.contractAnchors = append(o.contractAnchors, cppheader.New(cppheader.Config{
				Include:                cc.GetInclude(),
				Exclude:                cc.GetExclude(),
				EmitMacros:             cc.GetEmitMacros(),
				DocCommentStyles:       cc.GetDocCommentStyles(),
				IgnoredAttributeMacros: cc.GetIgnoredAttributeMacros(),
			}, adapters.NopHints{}))
		case "llmextract":
			lc := ca.GetLlmextract()
			// Backend-aware client selection: "auto" picks the frontier
			// API when ANTHROPIC_API_KEY is set, else local ollama. A
			// generous per-request timeout covers the local model's
			// cold-start on the first uncached file; cached files and the
			// frontier API are fast.
			client, cerr := llm.NewClient(lc.GetBackend(), lc.GetModel(), 600*time.Second, o.llmUsage)
			if cerr != nil {
				return fmt.Errorf("orchestrator: contract_anchor[%d] llmextract: %w", i, cerr)
			}
			cacheDir := lc.GetCacheDir()
			if cacheDir != "" && !filepath.IsAbs(cacheDir) {
				cacheDir = filepath.Join(o.repoRoot, cacheDir)
			}
			o.contractAnchors = append(o.contractAnchors, llmextract.New(llmextract.Config{
				Include:  lc.GetInclude(),
				Exclude:  lc.GetExclude(),
				CacheDir: cacheDir,
			}, client))
		default:
			return fmt.Errorf("orchestrator: contract_anchor[%d]: unknown name %q", i, ca.GetName())
		}
	}
	// Rendered references.
	for i, rr := range o.cfg.GetRenderedReference() {
		switch rr.GetName() {
		case "fidldoc":
			f := rr.GetFidldoc()
			o.renderedRefs = append(o.renderedRefs, fidldoc.New(fidldoc.Config{
				BundlePath: f.GetBundlePath(),
				URLBase:    f.GetUrlBase(),
				Library:    f.GetLibrary(),
			}))
		case "clidoc":
			c := rr.GetClidoc()
			o.renderedRefs = append(o.renderedRefs, clidoc.New(clidoc.Config{
				BundlePath:  c.GetBundlePath(),
				SectionPath: c.GetSectionPath(),
				URLBase:     c.GetUrlBase(),
			}))
		case "markdowncli":
			m := rr.GetMarkdowncli()
			docsDir := m.GetDocsDir()
			if !filepath.IsAbs(docsDir) {
				docsDir = filepath.Join(o.repoRoot, docsDir)
			}
			ot := markdowncli.OptionsTableConfig{}
			if cfg := m.GetOptionsTable(); cfg != nil {
				ot.SectionNames = cfg.GetSectionNames()
				ot.NameColumn = int(cfg.GetNameColumn())
				ot.DescriptionColumn = int(cfg.GetDescriptionColumn())
			}
			urlStyle := markdowncli.URLStyleCommandPath
			if m.GetUrlStyle() == configpb.MarkdownCLIConfig_URL_STYLE_FILE_PATH {
				urlStyle = markdowncli.URLStyleFilePath
			}
			o.renderedRefs = append(o.renderedRefs, markdowncli.New(markdowncli.Config{
				DocsDir:      docsDir,
				URLBase:      m.GetUrlBase(),
				Include:      m.GetInclude(),
				Exclude:      m.GetExclude(),
				BinaryName:   m.GetBinaryName(),
				OptionsTable: ot,
				URLStyle:     urlStyle,
			}))
		case "workflows":
			w := rr.GetWorkflows()
			docsDir := w.GetDocsDir()
			if !filepath.IsAbs(docsDir) {
				docsDir = filepath.Join(o.repoRoot, docsDir)
			}
			o.renderedRefs = append(o.renderedRefs, workflows.New(workflows.Config{
				DocsDir:     docsDir,
				Include:     w.GetInclude(),
				Exclude:     w.GetExclude(),
				BinaryName:  w.GetBinaryName(),
				MinElements: int(w.GetMinElements()),
				URLBase:     w.GetUrlBase(),
			}))
		case "yaml-workflows":
			yw := rr.GetYamlWorkflows()
			docsDir := yw.GetDocsDir()
			if !filepath.IsAbs(docsDir) {
				docsDir = filepath.Join(o.repoRoot, docsDir)
			}
			o.renderedRefs = append(o.renderedRefs, yamlworkflows.New(yamlworkflows.Config{
				DocsDir:     docsDir,
				Include:     yw.GetInclude(),
				Exclude:     yw.GetExclude(),
				IDLPrefix:   yw.GetIdlPrefix(),
				MinElements: int(yw.GetMinElements()),
				URLBase:     yw.GetUrlBase(),
			}))
		default:
			return fmt.Errorf("orchestrator: rendered_reference[%d]: unknown name %q", i, rr.GetName())
		}
	}
	// Implements maps.
	for i, im := range o.cfg.GetImplementsMap() {
		switch im.GetName() {
		case "cpp-fidl-wireserver":
			o.implementsMaps = append(o.implementsMaps, implementsmap.New(implementsmap.Config{
				Include:     im.GetInclude(),
				Exclude:     im.GetExclude(),
				CPPPatterns: im.GetPattern(),
			}))
		default:
			return fmt.Errorf("orchestrator: implements_map[%d]: unknown name %q", i, im.GetName())
		}
	}
	return nil
}

// Run executes the scan. Adapters run in parallel; their results are
// drained into a single Corpus. Per-adapter errors are collected on
// the Result rather than aborting the whole run.
func (o *Orchestrator) Run(ctx context.Context) (*Result, error) {
	start := time.Now()
	c := corpus.New()
	scope := o.scopeFromConfig()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []AdapterError
	)

	// Stage 0: build-graph recognizers. Run BEFORE contract / test /
	// doc adapters so the resulting BuildHints can feed both the
	// adapters that consume it (e.g. cppheader's IsPublic filter, once
	// wired) and the indexer's facade post-pass. A nil build_graph
	// block yields NopHints.
	hints, hintsErr := buildgraph.Run(ctx, o.cfg.GetBuildGraph(), o.repoRoot)
	if hintsErr != nil {
		errs = append(errs, AdapterError{AdapterName: "build_graph", Err: hintsErr})
		hints = adapters.NopHints{}
	}
	// Inject the real hints into anchors that consume them (cppheader's
	// IsPublic filter). Done before the Discover loop launches goroutines,
	// so the field write is not racing any reader.
	for _, ap := range o.contractAnchors {
		if hc, ok := ap.(interface {
			SetHints(adapters.BuildHints)
		}); ok {
			hc.SetHints(hints)
		}
	}
	addErr := func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, AdapterError{AdapterName: name, Err: err})
	}

	for _, ap := range o.contractAnchors {
		wg.Add(1)
		go func(ap adapters.ContractAnchorParser) {
			defer wg.Done()
			// Some contract anchors additionally emit inline DocClaims
			// from documentation that ships in the contract source itself —
			// FIDL's `///` comments, a CRD schema's OpenAPI `description`
			// fields. Any adapter implementing the DiscoverWithDocs entry
			// point routes through it so those inline docs feed the
			// doc-coverage join; the engine stays decoupled from the
			// concrete adapter type.
			if dwd, ok := ap.(interface {
				DiscoverWithDocs(context.Context, string, adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, error)
			}); ok {
				elems, claims, err := dwd.DiscoverWithDocs(ctx, o.repoRoot, scope)
				if err != nil {
					addErr(ap.Name(), err)
					return
				}
				stampElementProvenance(elems, ap.Name())
				stampDocProvenance(claims, ap.Name())
				c.AddElements(elems)
				c.AddDocClaims(claims)
				return
			}
			elems, err := ap.Discover(ctx, o.repoRoot, scope)
			if err != nil {
				addErr(ap.Name(), err)
				return
			}
			stampElementProvenance(elems, ap.Name())
			c.AddElements(elems)
		}(ap)
	}
	for _, tp := range o.testParsers {
		wg.Add(1)
		go func(tp adapters.TestParser) {
			defer wg.Done()
			tests, err := tp.Discover(ctx, o.repoRoot, scope)
			if err != nil {
				addErr(tp.Name(), err)
				return
			}
			stampTestProvenance(tests, tp.Name())
			c.AddTests(tests)
		}(tp)
	}
	for _, dp := range o.docParsers {
		wg.Add(1)
		go func(dp adapters.DocParser) {
			defer wg.Done()
			claims, err := dp.Parse(ctx, o.repoRoot, scope)
			if err != nil {
				addErr(dp.Name(), err)
				return
			}
			stampDocProvenance(claims, dp.Name())
			c.AddDocClaims(claims)
		}(dp)
	}
	for _, rr := range o.renderedRefs {
		wg.Add(1)
		go func(rr adapters.RenderedReferenceParser) {
			defer wg.Done()
			claims, err := rr.Parse(ctx)
			if err != nil {
				addErr(rr.Name(), err)
				return
			}
			stampDocProvenance(claims, rr.Name())
			c.AddDocClaims(claims)
		}(rr)
	}
	for _, im := range o.implementsMaps {
		wg.Add(1)
		go func(im adapters.ImplementsMapper) {
			defer wg.Done()
			elems, err := im.Discover(ctx, o.repoRoot, scope)
			if err != nil {
				addErr(im.Name(), err)
				return
			}
			stampElementProvenance(elems, im.Name())
			c.AddElements(elems)
		}(im)
	}
	wg.Wait()

	// Stage 1.5: affordance matching. Walks the post-Discover element
	// corpus and adds SAME_AFFORDANCE relationships across rows from
	// different adapters that represent the same underlying capability.
	// Mutates elements in place via shared pointers. Idempotent.
	affordanceStats := affordance.New(affordance.Config{}).Annotate(c.Elements())

	ingestEnd := time.Now()

	// Stage 2/3: categorize + index. Indexer consumes the now-built
	// corpus and produces CoverageProfiles in place.
	var cat *categorize.Categorizer
	if o.rules != nil {
		built, err := categorize.New(o.rules)
		if err == nil {
			cat = built
		}
	}
	bridges, bridgeErr := indexer.ParseBridges(o.cfg.GetCodegenBridges())
	if bridgeErr != nil {
		errs = append(errs, AdapterError{AdapterName: "codegen_bridge", Err: bridgeErr})
		bridges = nil
	}
	idx := indexer.NewWithOptions(c, cat, indexer.Options{
		NoisyWords:     o.cfg.GetProject().GetNoisyWords(),
		CodegenBridges: bridges,
		BuildHints:     hints,
	})
	indexStats := idx.BuildWithStats()
	indexEnd := time.Now()

	// Stage 3.5: citation-gated LLM attribution (optional). Runs AFTER
	// the deterministic indexer so it can append LLM-tier edges the join
	// missed (recall) without touching the deterministic edges (the
	// trusted number). Soft-fails: a backend/construction error is
	// recorded and the scan continues with deterministic coverage only.
	var attrStats attribution.Stats
	if ac := o.cfg.GetAttribution(); ac.GetEnabled() {
		client, cerr := llm.NewClient(ac.GetBackend(), ac.GetModel(), 600*time.Second, o.llmUsage)
		if cerr != nil {
			errs = append(errs, AdapterError{AdapterName: "attribution", Err: cerr})
		} else {
			cacheDir := ac.GetCacheDir()
			if cacheDir != "" && !filepath.IsAbs(cacheDir) {
				cacheDir = filepath.Join(o.repoRoot, cacheDir)
			}
			pass := attribution.New(attribution.Config{
				CacheDir:      cacheDir,
				MaxTests:      int(ac.GetMaxTests()),
				MaxDocs:       int(ac.GetMaxDocs()),
				MaxCandidates: int(ac.GetMaxCandidates()),
			}, client)
			st, aerr := pass.Run(ctx, c, o.repoRoot)
			if aerr != nil {
				errs = append(errs, AdapterError{AdapterName: "attribution", Err: aerr})
			}
			attrStats = st
		}
	}

	// Stage 3.6: LLM WORKFLOW (composition) extraction (optional). Runs
	// AFTER the deterministic indexer and soft-fails. It proposes ordered
	// element sequences per prose doc, citation-gates each element, and
	// adds LLM-tier WORKFLOW DocClaims to the corpus. These are
	// additive/flagged: the moat in indexer.placeDocRef keeps them out of
	// the deterministic workflow-surface count. Gated behind
	// attribution.extract_workflows so the default pipeline is unaffected.
	var workflowStats workflowextract.Stats
	if ac := o.cfg.GetAttribution(); ac.GetEnabled() && ac.GetExtractWorkflows() {
		client, cerr := llm.NewClient(ac.GetBackend(), ac.GetModel(), 600*time.Second, o.llmUsage)
		if cerr != nil {
			errs = append(errs, AdapterError{AdapterName: "workflowextract", Err: cerr})
		} else {
			cacheDir := ac.GetCacheDir()
			if cacheDir != "" && !filepath.IsAbs(cacheDir) {
				cacheDir = filepath.Join(o.repoRoot, cacheDir)
			}
			pass := workflowextract.New(workflowextract.Config{
				CacheDir:      cacheDir,
				MaxDocs:       int(ac.GetMaxDocs()),
				MaxCandidates: int(ac.GetMaxCandidates()),
			}, client)
			st, werr := pass.Run(ctx, c, o.repoRoot)
			if werr != nil {
				errs = append(errs, AdapterError{AdapterName: "workflowextract", Err: werr})
			}
			workflowStats = st
		}
	}

	// Stage 4: analyze.
	findings, err := analyze.RunAll(ctx, c, o.cfg)
	if err != nil {
		// Treat analyzer errors as soft — record warning, don't abort.
		errs = append(errs, AdapterError{AdapterName: "analyzer", Err: err})
	}
	analyzeEnd := time.Now()

	return &Result{
		Corpus:           c,
		Findings:         findings,
		Duration:         time.Since(start),
		IngestDuration:   ingestEnd.Sub(start),
		IndexDuration:    indexEnd.Sub(ingestEnd),
		AnalyzeDuration:  analyzeEnd.Sub(indexEnd),
		IndexStats:       indexStats,
		AffordanceStats:  affordanceStats,
		WorkflowStats:    workflowStats,
		AttributionStats: attrStats,
		LLMUsage:         o.llmUsage.Total(),
		AdapterErrors:    errs,
	}, nil
}

func (o *Orchestrator) scopeFromConfig() adapters.ScopeConfig {
	s := o.cfg.GetScope()
	return adapters.ScopeConfig{
		Libraries:   s.GetLibrary(),
		AlsoInclude: s.GetAlsoInclude(),
		Exclude:     s.GetExclude(),
	}
}

// provenanceFor maps an adapter name to its row provenance. The
// llmextract contract anchor is the LLM tier: its elements are stamped
// LLM/"llm" so the indexer moat keeps them out of the trusted
// deterministic counts and the corpus tier-merge lets deterministic rows
// win. Every other adapter wired in this build is a deterministic
// schema/grammar adapter, so the tier is DETERMINISTIC and the source is
// the adapter name. An adapter that has already stamped its own
// provenance is left untouched by the callers below (they guard on
// GetProvenance() == nil).
func provenanceFor(adapterName string) *commonpb.RowProvenance {
	if adapterName == llmextract.Name {
		return &commonpb.RowProvenance{
			Tier:   commonpb.RowProvenance_LLM,
			Source: "llm",
		}
	}
	return &commonpb.RowProvenance{
		Tier:   commonpb.RowProvenance_DETERMINISTIC,
		Source: adapterName,
	}
}

// stampElementProvenance tags each element with its adapter's row
// provenance unless the adapter already set one. Idempotent.
func stampElementProvenance(elems []*contractpb.ContractElement, adapterName string) {
	p := provenanceFor(adapterName)
	for _, e := range elems {
		if e.GetProvenance() == nil {
			e.Provenance = p
		}
	}
}

// stampTestProvenance tags each test case with its adapter's row
// provenance unless the adapter already set one. Idempotent.
func stampTestProvenance(tests []*testcasepb.TestCase, adapterName string) {
	p := provenanceFor(adapterName)
	for _, t := range tests {
		if t.GetProvenance() == nil {
			t.Provenance = p
		}
	}
}

// stampDocProvenance tags each doc claim with its adapter's row
// provenance unless the adapter already set one. Idempotent.
func stampDocProvenance(claims []*docclaimpb.DocClaim, adapterName string) {
	p := provenanceFor(adapterName)
	for _, dc := range claims {
		if dc.GetProvenance() == nil {
			dc.Provenance = p
		}
	}
}

// AdapterSummary returns a human-readable summary of which adapters
// are configured. Used by `sheaf doctor`.
type AdapterSummary struct {
	ContractAnchors []string
	TestParsers     []string
	DocParsers      []string
	RenderedRefs    []string
	ImplementsMaps  []string
}

func (o *Orchestrator) Summary() AdapterSummary {
	s := AdapterSummary{}
	for _, ap := range o.contractAnchors {
		s.ContractAnchors = append(s.ContractAnchors, ap.Name())
	}
	for _, tp := range o.testParsers {
		s.TestParsers = append(s.TestParsers, tp.Name())
	}
	for _, dp := range o.docParsers {
		s.DocParsers = append(s.DocParsers, dp.Name())
	}
	for _, rr := range o.renderedRefs {
		s.RenderedRefs = append(s.RenderedRefs, rr.Name())
	}
	for _, im := range o.implementsMaps {
		s.ImplementsMaps = append(s.ImplementsMaps, im.Name())
	}
	return s
}
