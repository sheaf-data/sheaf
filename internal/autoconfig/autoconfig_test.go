package autoconfig

import (
	"bytes"
	"testing"

	"github.com/sheaf-data/sheaf/internal/autodetect"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	"google.golang.org/protobuf/encoding/prototext"
)

func sampleResult() *autodetect.Result {
	return &autodetect.Result{
		SchemalessHeaders: true,
		Detections: []autodetect.Detection{
			{Adapter: "cppheader", Role: autodetect.RoleContract, Tier: autodetect.TierDeterministic, FileCount: 12, Include: []string{"**/*.h"}},
			{Adapter: "llmextract", Role: autodetect.RoleContract, Tier: autodetect.TierLLM, FileCount: 12, Include: []string{"**/*.h"}},
			{Adapter: "proto", Role: autodetect.RoleContract, Tier: autodetect.TierDeterministic, FileCount: 3, Include: []string{"**/*.proto"}},
			{Adapter: "gtest", Role: autodetect.RoleTest, Tier: autodetect.TierDeterministic, FileCount: 5, Include: []string{"**/*.cc"}},
			{Adapter: "markdown", Role: autodetect.RoleDoc, Tier: autodetect.TierDeterministic, FileCount: 7, Include: []string{"**/*.md"}},
		},
	}
}

// TestMarshal_ByteStable is the load-bearing invariant: the generated
// config is the run-over-run comparability anchor, so marshalling the
// same config twice must yield byte-identical output (prototext's
// whitespace randomization is neutralized).
func TestMarshal_ByteStable(t *testing.T) {
	cfg := Build(Options{ProjectName: "demo", ScopeLibraries: []string{"pw_log"}}, sampleResult())
	var prev []byte
	for i := 0; i < 8; i++ {
		b, err := Marshal(cfg)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if prev != nil && !bytes.Equal(prev, b) {
			t.Fatalf("Marshal not byte-stable across runs:\n--- run %d ---\n%s\n--- run %d ---\n%s", i-1, prev, i, b)
		}
		prev = b
	}
}

// TestMarshal_NoCacheDir confirms the environment-specific cache path is
// not serialized — the committed config must stay portable.
func TestMarshal_NoCacheDir(t *testing.T) {
	cfg := Build(Options{ProjectName: "demo"}, sampleResult())
	b, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(b, []byte("cache_dir")) {
		t.Fatalf("generated config must not contain cache_dir:\n%s", b)
	}
}

// TestBuild_RoundTripsThroughProtext confirms the synthesized config
// parses back as a Config (so config.LoadConfig would accept it). We
// strip the leading comment header, which prototext does not parse.
func TestBuild_RoundTripsThroughProtext(t *testing.T) {
	cfg := Build(Options{ProjectName: "demo", ScopeLibraries: []string{"pw_log"}}, sampleResult())
	b, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := &configpb.Config{}
	if err := prototext.Unmarshal(b, got); err != nil {
		t.Fatalf("generated config does not parse: %v", err)
	}
	if got.GetVersion() != schemaVersion {
		t.Errorf("version = %d, want %d", got.GetVersion(), schemaVersion)
	}
	if got.GetProject().GetName() != "demo" {
		t.Errorf("project.name = %q, want demo", got.GetProject().GetName())
	}
	// cppheader, llmextract, proto contract anchors expected (3).
	if n := len(got.GetContractAnchor()); n != 3 {
		t.Errorf("contract_anchor count = %d, want 3", n)
	}
	if n := len(got.GetTestParser()); n != 1 {
		t.Errorf("test_parser count = %d, want 1", n)
	}
	if n := len(got.GetDocParser()); n != 1 {
		t.Errorf("doc_parser count = %d, want 1", n)
	}
	// emit_macros must be set on the cppheader block.
	var sawCpp bool
	for _, ca := range got.GetContractAnchor() {
		if c := ca.GetCppHeader(); c != nil {
			sawCpp = true
			if !c.GetEmitMacros() {
				t.Error("cppheader emit_macros should be true")
			}
		}
	}
	if !sawCpp {
		t.Error("no cppheader contract anchor in generated config")
	}
}

// TestBuild_NoneBackend confirms --llm-backend none yields a purely
// deterministic config: the llmextract contract anchor and the
// attribution pass are omitted, while every deterministic anchor/parser
// (cppheader, proto, gtest, markdown) is retained.
func TestBuild_NoneBackend(t *testing.T) {
	cfg := Build(Options{ProjectName: "demo", LLMBackend: "none"}, sampleResult())

	if cfg.GetAttribution() != nil {
		t.Errorf("attribution must be nil when the LLM tier is off, got %+v", cfg.GetAttribution())
	}
	for _, ca := range cfg.GetContractAnchor() {
		if ca.GetName() == "llmextract" || ca.GetLlmextract() != nil {
			t.Error("llmextract contract anchor must be omitted when the LLM tier is off")
		}
	}
	// Deterministic anchors survive; only llmextract is dropped, so
	// cppheader + proto remain (2 of the fixture's 3 contract anchors).
	if n := len(cfg.GetContractAnchor()); n != 2 {
		t.Errorf("contract_anchor count = %d, want 2 (cppheader, proto)", n)
	}
	if n := len(cfg.GetTestParser()); n != 1 {
		t.Errorf("test_parser count = %d, want 1", n)
	}
	if n := len(cfg.GetDocParser()); n != 1 {
		t.Errorf("doc_parser count = %d, want 1", n)
	}
}
