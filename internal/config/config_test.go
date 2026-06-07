package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

// ----- LoadConfig: golden minimal config -----

func TestLoadConfig_Minimal(t *testing.T) {
	dir := t.TempDir()
	body := `
version: 1
project { name: "demo" }
contract_anchor {
  name: "fidl"
  fidl {
    fidlc_path: "fidlc"
    available: "HEAD"
    include: "*.fidl"
  }
}
cache { store: "filesystem" filesystem { path: "~/.sheaf/cache" } }
`
	p := writeFile(t, dir, "sheaf.textproto", body)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.GetProject().GetName(); got != "demo" {
		t.Errorf("project.name = %q, want demo", got)
	}
	if len(cfg.GetContractAnchor()) != 1 {
		t.Fatalf("contract_anchor count = %d, want 1", len(cfg.GetContractAnchor()))
	}
	if cfg.GetContractAnchor()[0].GetFidl().GetFidlcPath() != "fidlc" {
		t.Errorf("fidlc_path round-trip wrong: %q", cfg.GetContractAnchor()[0].GetFidl().GetFidlcPath())
	}
}

// ----- Version checks -----

func TestLoadConfig_UnknownVersion(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "sheaf.textproto", `version: 99 project { name: "demo" }`)
	_, err := LoadConfig(p)
	if !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("expected ErrUnknownVersion, got %v", err)
	}
}

func TestLoadConfig_MissingVersion(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "sheaf.textproto", `project { name: "demo" }`)
	_, err := LoadConfig(p)
	if !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("missing version should also produce ErrUnknownVersion; got %v", err)
	}
}

// ----- Env var expansion -----

func TestExpandEnvInPlace(t *testing.T) {
	t.Setenv("FUCHSIA_SDK_ROOT", "/sdk")
	t.Setenv("HOME", "/Users/demo")

	cfg := &configpb.Config{
		ContractAnchor: []*configpb.ContractAnchorConfig{
			{
				Name: "fidl",
				PerAdapter: &configpb.ContractAnchorConfig_Fidl{
					Fidl: &configpb.FIDLAnchorConfig{
						FidlcPath: "${FUCHSIA_SDK_ROOT}/bin/fidlc",
						Include:   []string{"${FUCHSIA_SDK_ROOT}/fidl/**/*.fidl"},
					},
				},
			},
		},
		RenderedReference: []*configpb.RenderedReferenceConfig{
			{
				Name: "fidldoc",
				PerAdapter: &configpb.RenderedReferenceConfig_Fidldoc{
					Fidldoc: &configpb.FIDLDocConfig{
						BundlePath: "${FUCHSIA_SDK_ROOT}/docs/fidldoc.zip",
					},
				},
			},
		},
		Cache: &configpb.CacheStoreConfig{
			PerBackend: &configpb.CacheStoreConfig_Filesystem{
				Filesystem: &configpb.FilesystemCacheConfig{Path: "~/.sheaf/cache"},
			},
		},
	}
	ExpandEnvInPlace(cfg)

	if got := cfg.GetContractAnchor()[0].GetFidl().GetFidlcPath(); got != "/sdk/bin/fidlc" {
		t.Errorf("fidlc_path expansion = %q", got)
	}
	if got := cfg.GetRenderedReference()[0].GetFidldoc().GetBundlePath(); got != "/sdk/docs/fidldoc.zip" {
		t.Errorf("bundle_path expansion = %q", got)
	}
	if got := cfg.GetCache().GetFilesystem().GetPath(); got != "/Users/demo/.sheaf/cache" {
		t.Errorf("cache path tilde expansion = %q", got)
	}
}

// ----- Validate: contract_anchor -----

func TestValidate_ContractAnchorRequiresOneofMatch(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		ContractAnchor: []*configpb.ContractAnchorConfig{
			{Name: "fidl"}, // missing fidl { ... } block
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), `requires a fidl`) {
		t.Errorf("expected oneof-mismatch error, got %v", err)
	}
}

func TestValidate_ContractAnchorUnknownName(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		ContractAnchor: []*configpb.ContractAnchorConfig{
			{Name: "capnproto"},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), `unknown contract_anchor name "capnproto"`) {
		t.Errorf("expected unknown-name error, got %v", err)
	}
}

func TestValidate_CobraRequiresYamlDir(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		ContractAnchor: []*configpb.ContractAnchorConfig{
			{
				Name: "cobra",
				PerAdapter: &configpb.ContractAnchorConfig_Cobra{
					Cobra: &configpb.CobraAnchorConfig{BinaryName: "docker"},
				},
			},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "cobra.yaml_dir") {
		t.Errorf("expected yaml_dir error, got %v", err)
	}
}

func TestValidate_CobraRequiresBinaryName(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		ContractAnchor: []*configpb.ContractAnchorConfig{
			{
				Name: "cobra",
				PerAdapter: &configpb.ContractAnchorConfig_Cobra{
					Cobra: &configpb.CobraAnchorConfig{YamlDir: "_output/yaml"},
				},
			},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "cobra.binary_name") {
		t.Errorf("expected binary_name error, got %v", err)
	}
}

func TestValidate_CobraOK(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		ContractAnchor: []*configpb.ContractAnchorConfig{
			{
				Name: "cobra",
				PerAdapter: &configpb.ContractAnchorConfig_Cobra{
					Cobra: &configpb.CobraAnchorConfig{
						YamlDir:    "_output/yaml",
						BinaryName: "docker",
					},
				},
			},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Errorf("expected valid cobra config; got %v", err)
	}
}

func TestValidate_MarkdownCLIRequiresDocsDir(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		RenderedReference: []*configpb.RenderedReferenceConfig{
			{
				Name: "markdowncli",
				PerAdapter: &configpb.RenderedReferenceConfig_Markdowncli{
					Markdowncli: &configpb.MarkdownCLIConfig{},
				},
			},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "markdowncli.docs_dir") {
		t.Errorf("expected docs_dir error, got %v", err)
	}
}

func TestValidate_FidlDirectSourceModeOK(t *testing.T) {
	// With neither fidlc_path nor prebuilt_ir_dir set, the adapter
	// falls back to direct source parsing — this should validate.
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		ContractAnchor: []*configpb.ContractAnchorConfig{
			{
				Name: "fidl",
				PerAdapter: &configpb.ContractAnchorConfig_Fidl{
					Fidl: &configpb.FIDLAnchorConfig{Include: []string{"*.fidl"}},
				},
			},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Errorf("expected no error in direct-source mode; got %v", err)
	}
}

// ----- Validate: substance_thresholds ordering (D3) -----

func TestValidate_SubstanceThresholds_Inverted(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		SubstanceThresholds: []*configpb.SubstanceThresholds{
			{Ecosystem: "fidl", Kind: "METHOD", SignatureOnlyMax: 20, PartialMax: 5},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "partial_max") {
		t.Errorf("expected threshold-ordering error, got %v", err)
	}
}

// ----- Validate: mcp_server auth (D5) -----

func TestValidate_NonLocalhostBindRequiresAuth(t *testing.T) {
	cfg := &configpb.Config{
		Project:   &configpb.Project{Name: "demo"},
		McpServer: &configpb.MCPServerConfig{Bind: "0.0.0.0", Port: 7700},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), `auth.mode != NONE`) {
		t.Errorf("expected non-localhost-auth error, got %v", err)
	}
}

func TestValidate_BearerRequiresEnv(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		McpServer: &configpb.MCPServerConfig{
			Bind: "127.0.0.1", Port: 7700,
			Auth: &configpb.AuthConfig{Mode: configpb.AuthConfig_BEARER},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "bearer_token_env") {
		t.Errorf("expected bearer-env error, got %v", err)
	}
}

func TestValidate_NonLocalhostBearerOK(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		McpServer: &configpb.MCPServerConfig{
			Bind: "0.0.0.0", Port: 7700,
			Auth: &configpb.AuthConfig{Mode: configpb.AuthConfig_BEARER, BearerTokenEnv: "SHEAF_TOKEN"},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

// ----- Validate: annotation gate (D6) -----

func TestValidate_AnnotationEnabledIsV2(t *testing.T) {
	cfg := &configpb.Config{
		Project:    &configpb.Project{Name: "demo"},
		Annotation: &configpb.AnnotationConfig{Enabled: true},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "v2+") {
		t.Errorf("expected v2+ gate error, got %v", err)
	}
}

// ----- Validate: llm client (D2 surface) -----

func TestValidate_LLMLocalLlamaRequiresModel(t *testing.T) {
	cfg := &configpb.Config{
		Project: &configpb.Project{Name: "demo"},
		Llm: &configpb.LLMConfig{
			Client: "local-llama",
			PerClient: &configpb.LLMConfig_LocalLlama{
				LocalLlama: &configpb.LocalLlamaConfig{Host: "127.0.0.1", Port: 11434},
			},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Errorf("expected local_llama.model error, got %v", err)
	}
}

// ----- LoadRules: golden -----

func TestLoadRules_Minimal(t *testing.T) {
	dir := t.TempDir()
	body := `
version: 1
category {
  dotted_path: "tests.unit_tests"
  paths: "src/**/*_test.rs"
}
category { dotted_path: "docs.reference" }
ownership {
  category: "tests.unit_tests"
  owner: "@team"
  subscribe: true
}
`
	p := writeFile(t, dir, "categorization-rules.textproto", body)
	r, err := LoadRules(p)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(r.GetCategory()) != 2 {
		t.Errorf("category count = %d, want 2", len(r.GetCategory()))
	}
	if len(r.GetOwnership()) != 1 {
		t.Errorf("ownership count = %d, want 1", len(r.GetOwnership()))
	}
}

func TestValidateRules_DuplicateCategory(t *testing.T) {
	r := &categorizationpb.Rules{
		Version: 1,
		Category: []*categorizationpb.Category{
			{DottedPath: "tests.unit_tests", Paths: []string{"a"}},
			{DottedPath: "tests.unit_tests", Paths: []string{"b"}},
		},
	}
	if err := ValidateRules(r); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Errorf("expected duplicate error, got %v", err)
	}
}

func TestValidateRules_OwnershipUnknownCategory(t *testing.T) {
	r := &categorizationpb.Rules{
		Version: 1,
		Category: []*categorizationpb.Category{
			{DottedPath: "tests.unit_tests"},
		},
		Ownership: []*categorizationpb.Ownership{
			{Category: "tests.does_not_exist", Owner: "@team"},
		},
	}
	if err := ValidateRules(r); err == nil || !strings.Contains(err.Error(), "no matching category") {
		t.Errorf("expected unknown-category error, got %v", err)
	}
}
