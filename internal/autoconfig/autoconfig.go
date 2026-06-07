// Package autoconfig turns an autodetect.Result into a sheaf.textproto
// Config — the structural FREEZE that `sheaf scan --auto` emits as one of
// its three artifacts.
//
// The generated config is the run-over-run comparability anchor: it pins
// scope, ecosystem→adapter assignment, and (later) logical groupings, so
// a subsequent `sheaf scan` (no --auto) reproduces the same structure
// deterministically and the LLM tier only fills the residue. For that to
// hold, Marshal must be BYTE-STABLE: an unchanged repo must regenerate a
// no-op config diff, and a genuine shift in detection must surface as a
// reviewable diff rather than churn. We therefore neutralize prototext's
// intentional whitespace randomization (see Marshal).
package autoconfig

import (
	"regexp"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"

	"github.com/sheaf-data/sheaf/internal/autodetect"
	"github.com/sheaf-data/sheaf/internal/llm"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// schemaVersion must match config.SchemaVersion so the generated config
// loads back through config.LoadConfig.
const schemaVersion = 1

// DoxygenXMLDir is the conventional, repo-relative location the doxygen
// doc-parser reads and the --auto runner generates the Doxygen XML into. It
// sits under the default --auto output dir (sheaf-auto/), so the committed,
// byte-stable config and the generated artifacts line up for the common case.
const DoxygenXMLDir = "sheaf-auto/doxygen/xml"

// Options carries the structural decisions that are not derivable from
// file-sniffing alone (project identity, scope, LLM backend knobs).
type Options struct {
	ProjectName    string
	ProjectDisplay string
	IDLPrefix      string
	ScopeLibraries []string
	// LLMModel overrides the default ollama model on the llmextract block.
	// The model is a structural decision (it pins extraction
	// reproducibility and the cache key), so it IS serialized. The cache
	// directory is environment state, not structure, and is deliberately
	// NOT emitted here — the caller sets it on the in-memory config for
	// the run, keeping the committed config portable and byte-stable.
	LLMModel string
	// LLMBackend is "" / "auto" (frontier if ANTHROPIC_API_KEY set, else
	// ollama), "ollama", or "anthropic". Only an EXPLICIT backend is
	// serialized; "auto" is left empty so the generated config re-resolves
	// against the environment on replay (portability).
	LLMBackend string
}

// Build synthesizes a Config from the detection result. Repeated blocks
// are emitted in the order det already sorted them (contract, then test,
// then doc; adapter name within each), keeping Marshal byte-stable.
func Build(opts Options, det *autodetect.Result) *configpb.Config {
	cfg := &configpb.Config{
		Version: schemaVersion,
		Project: &configpb.Project{
			Name:        opts.ProjectName,
			DisplayName: opts.ProjectDisplay,
			IdlPrefix:   opts.IDLPrefix,
		},
	}
	if len(opts.ScopeLibraries) > 0 {
		cfg.Scope = &configpb.Scope{Library: opts.ScopeLibraries}
	}

	// cppHeader drives the C++ tuning path: when the contract surface is C++
	// headers, --auto synthesizes the config the sheaf-onboard procedure
	// otherwise applies by hand — protocpp over gtest, the surveyed custom
	// test + attribute macros, bare free-function call extraction, and the
	// Doxygen reference surface — so a fresh C++ onboarding is trustworthy
	// from one command with no hand-edits.
	cppHeader := det.Has("cppheader")

	// When the LLM tier is disabled (--llm-backend none), omit every block
	// that would invoke a model — the llmextract contract anchor and the
	// attribution pass — so the frozen config and the run that emits it are
	// purely deterministic. Deterministic anchors/parsers are unaffected.
	llmOff := llm.Disabled(opts.LLMBackend)

	for _, d := range det.Contract() {
		switch d.Adapter {
		case "cppheader":
			ch := &configpb.CppHeaderAnchorConfig{
				// Header-qualify the include. The scoped --auto path passes the
				// raw scope glob (e.g. "pw_string/**"), which would make
				// cppheader parse every file in scope — .cc, .rst, BUILD files —
				// and emit bogus elements that inflate the denominator. Restrict
				// to C/C++ header extensions.
				Include: headerGlobs(d.Include),
				// C/C++ public APIs are commonly macro-shaped
				// (PW_LOG_*, ASSERT_*, …); emit them so the
				// deterministic baseline covers that surface.
				EmitMacros: true,
				// Keep implementation detail out of the contract so the
				// element denominator isn't inflated (every % would read low).
				Exclude: []string{"**/internal/**", "**/impl/**"},
				// Skip leading attribute macros (`class PW_LOCKABLE Foo`) so
				// cppheader reads the real type name, not the macro — without
				// this the annotated type is silently dropped from the surface.
				IgnoredAttributeMacros: det.CppAttributeMacros,
			}
			cfg.ContractAnchor = append(cfg.ContractAnchor, &configpb.ContractAnchorConfig{
				Name:       "cppheader",
				PerAdapter: &configpb.ContractAnchorConfig_CppHeader{CppHeader: ch},
			})
		case "llmextract":
			if llmOff {
				continue
			}
			lx := &configpb.LLMExtractAnchorConfig{Include: d.Include}
			if opts.LLMModel != "" {
				lx.Model = opts.LLMModel
			}
			if opts.LLMBackend != "" && opts.LLMBackend != "auto" {
				lx.Backend = opts.LLMBackend
			}
			cfg.ContractAnchor = append(cfg.ContractAnchor, &configpb.ContractAnchorConfig{
				Name:       "llmextract",
				PerAdapter: &configpb.ContractAnchorConfig_Llmextract{Llmextract: lx},
			})
		case "proto":
			cfg.ContractAnchor = append(cfg.ContractAnchor, &configpb.ContractAnchorConfig{
				Name:       "proto",
				PerAdapter: &configpb.ContractAnchorConfig_Proto{Proto: &configpb.ProtoAnchorConfig{Include: d.Include}},
			})
		case "fidl":
			cfg.ContractAnchor = append(cfg.ContractAnchor, &configpb.ContractAnchorConfig{
				Name:       "fidl",
				PerAdapter: &configpb.ContractAnchorConfig_Fidl{Fidl: &configpb.FIDLAnchorConfig{Include: d.Include}},
			})
		case "cml":
			cfg.ContractAnchor = append(cfg.ContractAnchor, &configpb.ContractAnchorConfig{
				Name:       "cml",
				PerAdapter: &configpb.ContractAnchorConfig_Cml{Cml: &configpb.CMLAnchorConfig{Include: d.Include}},
			})
		case "clap":
			cfg.ContractAnchor = append(cfg.ContractAnchor, &configpb.ContractAnchorConfig{
				Name: "clap",
				PerAdapter: &configpb.ContractAnchorConfig_Clap{Clap: &configpb.ClapAnchorConfig{
					// clap requires at least one crate root; "." scans
					// the whole scoped tree, the conservative default.
					CrateRoots: []string{"."},
					Include:    d.Include,
				}},
			})
		}
	}

	for _, d := range det.Tests() {
		switch d.Adapter {
		case "gtest":
			if cppHeader {
				// C++ header contract: protocpp is the right test parser (a
				// gtest superset that adds qualified-name + — opted-in here —
				// bare free-function call attribution), not stock gtest. Fold
				// in the surveyed custom test macros so constexpr/data-driven
				// tests register, and enable bare-call extraction so a
				// free-function-heavy module doesn't read ~0% tested.
				cfg.TestParser = append(cfg.TestParser, &configpb.TestParserConfig{
					Name: "protocpp",
					PerAdapter: &configpb.TestParserConfig_Protocpp{Protocpp: &configpb.ProtoCPPConfig{
						Include:                  d.Include,
						ExtraTestMacros:          det.CppTestMacros,
						ExtractFreeFunctionCalls: true,
					}},
				})
			} else {
				cfg.TestParser = append(cfg.TestParser, &configpb.TestParserConfig{
					Name:       "gtest",
					PerAdapter: &configpb.TestParserConfig_Gtest{Gtest: &configpb.GTestConfig{Include: d.Include}},
				})
			}
		case "rust-test":
			cfg.TestParser = append(cfg.TestParser, &configpb.TestParserConfig{
				Name:       "rust-test",
				PerAdapter: &configpb.TestParserConfig_RustTest{RustTest: &configpb.RustTestConfig{Include: d.Include}},
			})
		}
	}

	for _, d := range det.Docs() {
		switch d.Adapter {
		case "markdown":
			cfg.DocParser = append(cfg.DocParser, &configpb.DocParserConfig{
				Name:       "markdown",
				PerAdapter: &configpb.DocParserConfig_Markdown{Markdown: &configpb.MarkdownConfig{Include: d.Include}},
			})
		case "rst":
			cfg.DocParser = append(cfg.DocParser, &configpb.DocParserConfig{
				Name:       "rst",
				PerAdapter: &configpb.DocParserConfig_Rst{Rst: &configpb.RstConfig{Include: d.Include}},
			})
		}
	}

	// Doxygen reference surface. When a C++ header contract ships a Doxyfile,
	// the authoritative API docs are the Doxygen `///` comments, NOT the rst/
	// markdown prose — so wire the doxygen doc-parser for the docs.reference
	// surface (it coexists with rst/markdown, which feed docs.concepts). The
	// xml_dir is the conventional generated location; the --auto runner
	// generates the XML there before the scan. Byte-stable: a fixed string.
	if cppHeader && det.HasDoxygen {
		cfg.DocParser = append(cfg.DocParser, &configpb.DocParserConfig{
			Name: "doxygen",
			PerAdapter: &configpb.DocParserConfig_Doxygen{Doxygen: &configpb.DoxygenConfig{
				XmlDir: DoxygenXMLDir,
			}},
		})
	}

	// Enable the citation-gated LLM attribution pass when there is
	// something to attribute (tests or docs) and a contract surface. Only
	// an explicit backend is serialized ("auto" stays empty for
	// portability); the cache dir is set by the runner, not here.
	if !llmOff && (len(cfg.TestParser) > 0 || len(cfg.DocParser) > 0) && len(cfg.ContractAnchor) > 0 {
		ac := &configpb.AttributionConfig{Enabled: true}
		if opts.LLMBackend != "" && opts.LLMBackend != "auto" {
			ac.Backend = opts.LLMBackend
		}
		cfg.Attribution = ac
	}

	return cfg
}

// headerGlobs ensures cppheader's include globs target header files. A glob
// already ending in a header extension is kept; any other glob (a raw scope
// like "pw_string/**" or the whole-repo "**/*") is rewritten to the C/C++
// header extensions under its directory prefix, so cppheader never parses
// non-header files.
func headerGlobs(globs []string) []string {
	exts := []string{"*.h", "*.hpp", "*.hh"}
	seen := map[string]bool{}
	out := make([]string, 0, len(globs)*len(exts))
	add := func(g string) {
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	for _, g := range globs {
		if strings.HasSuffix(g, ".h") || strings.HasSuffix(g, ".hpp") || strings.HasSuffix(g, ".hh") {
			add(g)
			continue
		}
		base := g
		for _, suf := range []string{"/**", "/*", "**", "/"} {
			base = strings.TrimSuffix(base, suf)
		}
		base = strings.TrimSuffix(base, "/")
		for _, ext := range exts {
			if base == "" {
				add("**/" + ext)
			} else {
				add(base + "/**/" + ext)
			}
		}
	}
	if len(out) == 0 {
		for _, ext := range exts {
			add("**/" + ext)
		}
	}
	return out
}

// header is prepended to the generated config so a human opening it knows
// it is machine-generated and how to evolve it (edit structure here; work
// the hardening backlog to shrink the LLM tier).
const header = `# Generated by ` + "`sheaf scan --auto`" + ` — the structural freeze.
#
# This file pins the structural decisions of the cold run (scope,
# ecosystem->adapter assignment) so subsequent ` + "`sheaf scan`" + ` runs are
# comparable. It is byte-stable: re-running --auto on an unchanged repo
# regenerates an identical file. Edit it to correct structure (merge or
# split a grouping, relabel, adjust scope) — those edits are durable
# intent the next run honors. See sheaf-hardening.md for what to replace
# with deterministic adapters.

`

// colonWS collapses prototext's intentionally-randomized separator (a
// field's "key:" may be followed by one OR two spaces, varying per
// process) down to a single space, so the output is byte-stable. It
// anchors on a snake_case field name + colon at line start (after
// indentation), which never matches inside a quoted string value.
var colonWS = regexp.MustCompile(`(?m)^(\s*[a-z0-9_]+:)[ ]{2,}`)

// Marshal renders cfg as a byte-stable textproto with the generated
// header. Determinism is the whole point (see package doc): we marshal
// multiline with fixed indentation, then neutralize prototext's
// whitespace randomization.
func Marshal(cfg *configpb.Config) ([]byte, error) {
	raw, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	body := colonWS.ReplaceAll(raw, []byte("$1 "))
	out := header + string(body)
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out), nil
}
