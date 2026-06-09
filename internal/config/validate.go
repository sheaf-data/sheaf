package config

import (
	"errors"
	"fmt"
	"strings"

	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// Validate runs resolve-time checks per docs/config.md §3.3 and §7.
// It returns the first validation error encountered with a path
// prefix identifying the offending field.
func Validate(c *configpb.Config) error {
	if c == nil {
		return errors.New("config is nil")
	}
	if c.GetProject().GetName() == "" {
		return errors.New("project.name: required")
	}

	for i, anchor := range c.GetContractAnchor() {
		if err := validateContractAnchor(anchor); err != nil {
			return fmt.Errorf("contract_anchor[%d]: %w", i, err)
		}
	}
	for i, rr := range c.GetRenderedReference() {
		if err := validateRenderedReference(rr); err != nil {
			return fmt.Errorf("rendered_reference[%d]: %w", i, err)
		}
	}
	for i, tp := range c.GetTestParser() {
		if err := validateTestParser(tp); err != nil {
			return fmt.Errorf("test_parser[%d]: %w", i, err)
		}
	}
	for i, dp := range c.GetDocParser() {
		if err := validateDocParser(dp); err != nil {
			return fmt.Errorf("doc_parser[%d]: %w", i, err)
		}
	}
	for i, st := range c.GetSubstanceThresholds() {
		if err := validateSubstanceThresholds(st); err != nil {
			return fmt.Errorf("substance_thresholds[%d]: %w", i, err)
		}
	}
	if err := validateMCPServer(c.GetMcpServer()); err != nil {
		return fmt.Errorf("mcp_server: %w", err)
	}
	if err := validateAnnotation(c.GetAnnotation()); err != nil {
		return fmt.Errorf("annotation: %w", err)
	}
	if err := validateLLM(c.GetLlm()); err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	return nil
}

// validateExternal checks the shared external (runtime adapter) block
// used by every adapter role. The block is required and must name a
// command; everything else is optional and validated by the plugin at
// run time.
func validateExternal(e *configpb.ExternalAdapterConfig) error {
	if e == nil {
		return errors.New(`name="external" requires an external { ... } block`)
	}
	if e.GetCommand() == "" {
		return errors.New("external.command: required")
	}
	return nil
}

func validateContractAnchor(a *configpb.ContractAnchorConfig) error {
	if a.GetName() == "" {
		return errors.New("name: required")
	}
	switch a.GetName() {
	case "external":
		return validateExternal(a.GetExternal())
	case "fidl":
		f := a.GetFidl()
		if f == nil {
			return errors.New(`name="fidl" requires a fidl { ... } block`)
		}
		// fidlc_path / prebuilt_ir_dir are both optional; when neither
		// is set the adapter falls back to direct source parsing. The
		// adapter still needs `include` globs to find anything, but
		// the default ("**/*.fidl") is fine if absent.
		_ = f
	case "argh":
		a2 := a.GetArgh()
		if a2 == nil {
			return errors.New(`name="argh" requires an argh { ... } block`)
		}
		if len(a2.GetCrateRoots()) == 0 {
			return errors.New("argh.crate_roots: at least one entry required")
		}
	case "clap":
		c := a.GetClap()
		if c == nil {
			return errors.New(`name="clap" requires a clap { ... } block`)
		}
		if len(c.GetCrateRoots()) == 0 {
			return errors.New("clap.crate_roots: at least one entry required")
		}
	case "cobra":
		c := a.GetCobra()
		if c == nil {
			return errors.New(`name="cobra" requires a cobra { ... } block`)
		}
		if c.GetYamlDir() == "" {
			return errors.New("cobra.yaml_dir: required")
		}
		if c.GetBinaryName() == "" {
			return errors.New("cobra.binary_name: required")
		}
	case "proto":
		p := a.GetProto()
		if p == nil {
			return errors.New(`name="proto" requires a proto { ... } block`)
		}
		// protoc_path is optional (defaults to PATH lookup); include
		// defaults to "**/*.proto"; proto_path defaults to repo root.
		// No required fields beyond the block itself.
	case "cml":
		c := a.GetCml()
		if c == nil {
			return errors.New(`name="cml" requires a cml { ... } block`)
		}
		// include defaults to "**/*.cml"; exclude defaults to []. No
		// required fields beyond the block itself.
		_ = c
	case "cppheader":
		c := a.GetCppHeader()
		if c == nil {
			return errors.New(`name="cppheader" requires a cpp_header { ... } block`)
		}
		// include defaults to "**/*.h" + "**/*.hpp"; all other fields
		// optional. No required fields beyond the block itself.
		_ = c
	case "crd":
		c := a.GetCrd()
		if c == nil {
			return errors.New(`name="crd" requires a crd { ... } block`)
		}
		// include defaults to "**/*.yaml" + "**/*.yml"; exclude defaults
		// to []. Non-CRD YAML is content-gated out by the adapter, so no
		// required fields beyond the block itself.
		_ = c
	case "k8smanifest":
		c := a.GetK8SManifest()
		if c == nil {
			return errors.New(`name="k8smanifest" requires a k8s_manifest { ... } block`)
		}
		// include defaults to "**/*.yaml" + "**/*.yml"; exclude defaults
		// to []. Documents lacking apiVersion + kind are skipped by the
		// adapter, so no required fields beyond the block itself.
		_ = c
	case "helmvalues":
		c := a.GetHelmValues()
		if c == nil {
			return errors.New(`name="helmvalues" requires a helm_values { ... } block`)
		}
		// include defaults to "**/values.schema.json" + "**/values.yaml"
		// + "**/values.yml"; exclude defaults to []. The schema path wins
		// over values.yaml per chart, so no required fields beyond the
		// block itself.
		_ = c
	case "llmextract":
		c := a.GetLlmextract()
		if c == nil {
			return errors.New(`name="llmextract" requires an llmextract { ... } block`)
		}
		// include defaults to "**/*.h" + "**/*.hpp"; model defaults to
		// qwen2.5:14b-instruct; cache_dir optional. No required fields.
		_ = c
	default:
		return fmt.Errorf("unknown contract_anchor name %q", a.GetName())
	}
	return nil
}

func validateRenderedReference(r *configpb.RenderedReferenceConfig) error {
	if r.GetName() == "" {
		return errors.New("name: required")
	}
	switch r.GetName() {
	case "external":
		return validateExternal(r.GetExternal())
	case "fidldoc":
		f := r.GetFidldoc()
		if f == nil {
			return errors.New(`name="fidldoc" requires a fidldoc { ... } block`)
		}
		if f.GetBundlePath() == "" {
			return errors.New("fidldoc.bundle_path: required")
		}
		if f.GetUrlBase() == "" {
			return errors.New("fidldoc.url_base: required")
		}
	case "clidoc":
		cd := r.GetClidoc()
		if cd == nil {
			return errors.New(`name="clidoc" requires a clidoc { ... } block`)
		}
		if cd.GetBundlePath() == "" {
			return errors.New("clidoc.bundle_path: required")
		}
		if cd.GetSectionPath() == "" {
			return errors.New("clidoc.section_path: required")
		}
	case "markdowncli":
		m := r.GetMarkdowncli()
		if m == nil {
			return errors.New(`name="markdowncli" requires a markdowncli { ... } block`)
		}
		if m.GetDocsDir() == "" {
			return errors.New("markdowncli.docs_dir: required")
		}
	case "workflows":
		w := r.GetWorkflows()
		if w == nil {
			return errors.New(`name="workflows" requires a workflows { ... } block`)
		}
		if w.GetDocsDir() == "" {
			return errors.New("workflows.docs_dir: required")
		}
		if w.GetBinaryName() == "" {
			return errors.New("workflows.binary_name: required")
		}
	case "yaml-workflows":
		yw := r.GetYamlWorkflows()
		if yw == nil {
			return errors.New(`name="yaml-workflows" requires a yaml_workflows { ... } block`)
		}
		if yw.GetDocsDir() == "" {
			return errors.New("yaml_workflows.docs_dir: required")
		}
	default:
		return fmt.Errorf("unknown rendered_reference name %q", r.GetName())
	}
	return nil
}

func validateTestParser(tp *configpb.TestParserConfig) error {
	if tp.GetName() == "" {
		return errors.New("name: required")
	}
	switch tp.GetName() {
	case "external":
		return validateExternal(tp.GetExternal())
	case "gtest":
		if tp.GetGtest() == nil {
			return errors.New(`name="gtest" requires a gtest { ... } block`)
		}
	case "rust-test":
		if tp.GetRustTest() == nil {
			return errors.New(`name="rust-test" requires a rust_test { ... } block`)
		}
	case "bats":
		if tp.GetBats() == nil {
			return errors.New(`name="bats" requires a bats { ... } block`)
		}
	case "gotest":
		if tp.GetGotest() == nil {
			return errors.New(`name="gotest" requires a gotest { ... } block`)
		}
	case "protocpp":
		if tp.GetProtocpp() == nil {
			return errors.New(`name="protocpp" requires a protocpp { ... } block`)
		}
	case "python-test":
		if tp.GetPythonTest() == nil {
			return errors.New(`name="python-test" requires a python_test { ... } block`)
		}
	default:
		return fmt.Errorf("unknown test_parser name %q", tp.GetName())
	}
	return nil
}

func validateDocParser(dp *configpb.DocParserConfig) error {
	if dp.GetName() == "" {
		return errors.New("name: required")
	}
	switch dp.GetName() {
	case "external":
		return validateExternal(dp.GetExternal())
	case "markdown":
		if dp.GetMarkdown() == nil {
			return errors.New(`name="markdown" requires a markdown { ... } block`)
		}
	case "concept-docs":
		// The concept-docs source for the additive docs.concepts surface.
		// It reuses the markdown { include/exclude } block to declare its
		// narrative-doc globs, but is DISTINCT from the loose "markdown"
		// doc-parser: the orchestrator does NOT run the loose markdown
		// reference adapter for it (that adapter's prose-collision matching
		// is exactly what the docs.concepts surface must not use). Instead
		// the anchored-mention engine (internal/adapters/conceptdoc) reads
		// these globs post-scan and attributes only high-confidence anchored
		// references. See REQUIREMENTS-concept-ingest.md decision 2.
		if dp.GetMarkdown() == nil {
			return errors.New(`name="concept-docs" requires a markdown { ... } block`)
		}
	case "rst":
		if dp.GetRst() == nil {
			return errors.New(`name="rst" requires a rst { ... } block`)
		}
	case "doxygen":
		if dp.GetDoxygen() == nil {
			return errors.New(`name="doxygen" requires a doxygen { ... } block`)
		}
		if dp.GetDoxygen().GetXmlDir() == "" {
			return errors.New(`doxygen: xml_dir is required (the Doxygen XML output dir)`)
		}
	default:
		return fmt.Errorf("unknown doc_parser name %q", dp.GetName())
	}
	return nil
}

func validateSubstanceThresholds(st *configpb.SubstanceThresholds) error {
	if st.GetEcosystem() == "" {
		return errors.New("ecosystem: required")
	}
	if st.GetKind() == "" {
		return errors.New("kind: required")
	}
	if st.GetPartialMax() < st.GetSignatureOnlyMax() {
		return fmt.Errorf("partial_max (%d) < signature_only_max (%d)",
			st.GetPartialMax(), st.GetSignatureOnlyMax())
	}
	return nil
}

func validateMCPServer(m *configpb.MCPServerConfig) error {
	if m == nil {
		return nil
	}
	bind := m.GetBind()
	if bind == "" {
		bind = "127.0.0.1"
	}
	if !isLocalhost(bind) {
		mode := m.GetAuth().GetMode()
		if mode == configpb.AuthConfig_NONE || mode == configpb.AuthConfig_MODE_UNSPECIFIED {
			return fmt.Errorf("bind=%q requires auth.mode != NONE", bind)
		}
	}
	if m.GetAuth().GetMode() == configpb.AuthConfig_BEARER && m.GetAuth().GetBearerTokenEnv() == "" {
		return errors.New("auth.mode=BEARER requires auth.bearer_token_env")
	}
	return nil
}

func isLocalhost(bind string) bool {
	return bind == "127.0.0.1" || bind == "localhost" || bind == "::1"
}

func validateAnnotation(a *configpb.AnnotationConfig) error {
	if a == nil {
		return nil
	}
	if a.GetEnabled() {
		// D6 gate: rejected in v1 builds.
		return errors.New("enabled=true is a v2+ feature; current build is v1")
	}
	return nil
}

func validateLLM(l *configpb.LLMConfig) error {
	if l == nil {
		return nil
	}
	switch l.GetClient() {
	case "", "noop":
		// fine; LLM disabled
	case "local-llama":
		if l.GetLocalLlama() == nil {
			return errors.New(`client="local-llama" requires local_llama { ... } block`)
		}
		if l.GetLocalLlama().GetModel() == "" {
			return errors.New("local_llama.model: required")
		}
	default:
		return fmt.Errorf("unknown llm.client %q", l.GetClient())
	}
	return nil
}

// ValidateRules runs resolve-time checks on categorization rules.
// Currently only basic structural checks; the dotted-path-to-CoverageProfile
// validation happens once the indexer knows the coverage schema (TODO).
func ValidateRules(r *categorizationpb.Rules) error {
	if r == nil {
		return errors.New("rules is nil")
	}
	seen := make(map[string]bool)
	for i, c := range r.GetCategory() {
		if c.GetDottedPath() == "" {
			return fmt.Errorf("category[%d].dotted_path: required", i)
		}
		if strings.Contains(c.GetDottedPath(), " ") {
			return fmt.Errorf("category[%d].dotted_path: %q contains whitespace", i, c.GetDottedPath())
		}
		if seen[c.GetDottedPath()] {
			return fmt.Errorf("category[%d].dotted_path: %q duplicated", i, c.GetDottedPath())
		}
		seen[c.GetDottedPath()] = true
	}
	for i, o := range r.GetOwnership() {
		if o.GetCategory() == "" {
			return fmt.Errorf("ownership[%d].category: required", i)
		}
		if o.GetOwner() == "" {
			return fmt.Errorf("ownership[%d].owner: required", i)
		}
		if !seen[o.GetCategory()] {
			return fmt.Errorf("ownership[%d].category: %q has no matching category", i, o.GetCategory())
		}
	}
	return nil
}
