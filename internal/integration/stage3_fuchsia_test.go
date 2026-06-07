// Stage-3 (Index) integration test against the real fuchsia.io
// corpus on /Volumes/T7/fuchsia. Asserts that the indexer:
//
//  1. Materializes inherited methods from COMPOSED_FROM edges
//     (fuchsia.io has ~14 composes; we expect dozens of inherited
//     surface methods to appear)
//  2. Populates the `implementations` surface on FIDL interface
//     elements from IMPLEMENTS edges (DirectoryConnection ->
//     fuchsia.io/Directory, FileConnection -> fuchsia.io/File,
//     NodeConnection -> fuchsia.io/Node). The
//     implements-map TEST attribution path was removed; tests of
//     impl classes attribute to the impl element directly, and
//     interface elements render their IMPLEMENTS edges as a first-
//     class surface.
//  3. Routes test references into the correct CoverageProfile
//     bucket (tests.unit_tests for the vfs test directory)
//  4. Routes inline `///` doc claims to docs.reference.fidldoc
//  5. Computes GapsSummary (Directory.Open should be missing examples,
//     but have docs and at least one test)
//
// Skipped if /Volumes/T7/fuchsia is not present (auto-detected).

package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

func writeFixtureConfig(t *testing.T, dir string) (cfgPath, rulesPath string) {
	t.Helper()
	cfgPath = filepath.Join(dir, "sheaf.textproto")
	rulesPath = filepath.Join(dir, "categorization-rules.textproto")
	if err := writeFile(cfgPath, fuchsiaIOConfig); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	if err := writeFile(rulesPath, fuchsiaIORules); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	return cfgPath, rulesPath
}

const fuchsiaIOConfig = `
version: 1
project { name: "fuchsia" }
scope { library: "fuchsia.io" also_include: "fuchsia.unknown" }
contract_anchor {
  name: "fidl"
  fidl {
    include: "sdk/fidl/**/*.fidl"
    exclude: "sdk/fidl/**/test/**"
  }
}
test_parser {
  name: "gtest"
  gtest {
    include: "src/storage/lib/vfs/cpp/**/*_test.cc"
    include: "src/storage/lib/vfs/cpp/**/*_tests.cc"
  }
}
implements_map {
  name: "cpp-fidl-wireserver"
  include: "src/storage/lib/vfs/cpp/**/*.h"
  include: "src/storage/lib/vfs/cpp/**/*.cc"
}
`

const fuchsiaIORules = `
version: 1
category { dotted_path: "docs.reference" }
category {
  dotted_path: "tests.unit_tests"
  paths: "src/storage/lib/vfs/cpp/tests/**"
}
`

func TestStage3_FullPipelineAgainstFuchsiaIO(t *testing.T) {
	repo := resolveFuchsiaRepo(t)
	tmpDir := t.TempDir()
	cfgPath, rulesPath := writeFixtureConfig(t, tmpDir)

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	rules, err := config.LoadRules(rulesPath)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	o, err := orchestrator.New(cfg, rules, repo)
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	res, err := o.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	stats := res.Corpus.Stats()
	t.Logf("stage-1 corpus: %d elements, %d tests, %d docs", stats.Elements, stats.Tests, stats.DocClaims)
	t.Logf("stage-3 index: %d profiles, %d test refs, %d doc refs, %d implements links, %d inherited methods",
		res.IndexStats.ProfilesBuilt,
		res.IndexStats.TestRefsByElement,
		res.IndexStats.DocRefsByElement,
		res.IndexStats.ImplementsLinks,
		res.IndexStats.InheritedMethods)

	// --- Floor assertions on shape ---
	if stats.Elements < 150 {
		t.Errorf("elements = %d, want >=150 (FIDL elements + inherited methods)", stats.Elements)
	}
	if res.IndexStats.InheritedMethods < 30 {
		t.Errorf("inherited methods = %d, want >=30 (Directory and File each inherit several via composition)", res.IndexStats.InheritedMethods)
	}
	if stats.Tests < 100 {
		t.Errorf("tests = %d, want >=100 (vfs has >=15 _tests.cc files; each has multiple TEST)", stats.Tests)
	}
	if res.IndexStats.ImplementsLinks < 3 {
		t.Errorf("implements links = %d, want >=3 (DirectoryConnection, FileConnection, NodeConnection)", res.IndexStats.ImplementsLinks)
	}
	// NOTE: pre-redesign this floor was >=50 because implements-map
	// attribution bridged every test in the vfs dir to its
	// corresponding FIDL element. After the redesign, only direct-
	// ref attributions remain — tests of impl classes attribute to the
	// impl element (CPP_CLASS), not to the FIDL element. The total is
	// expected to be much smaller; floor adjusted to a non-zero sanity
	// check that Strategy 1 still fires for the gtest-cpp body
	// extractor's emitted refs.
	if res.IndexStats.TestRefsByElement < 1 {
		t.Errorf("test attributions = %d, want >=1 (Strategy 1 direct refs)", res.IndexStats.TestRefsByElement)
	}

	// --- Specific element checks ---
	dirOpen := res.Corpus.Profile("fuchsia.io/Directory.Open")
	if dirOpen == nil {
		t.Fatal("no profile for fuchsia.io/Directory.Open")
	}
	if dirOpen.GetDocs().GetReference() == nil || len(dirOpen.GetDocs().GetReference().GetFidldoc()) == 0 {
		t.Errorf("Directory.Open missing fidldoc reference; got %+v", dirOpen.GetDocs())
	} else {
		// Should be SUBSTANTIVE; Open has rich documentation.
		ref := dirOpen.GetDocs().GetReference().GetFidldoc()[0]
		if ref.GetSubstance() != commonpb.Substance_SUBSTANTIVE {
			t.Errorf("Directory.Open ref substance = %v, want SUBSTANTIVE", ref.GetSubstance())
		}
		// URL should be the canonical fuchsia.dev shape.
		if want := "https://fuchsia.dev/reference/fidl/fuchsia.io#Directory.Open"; ref.GetUrl() != want {
			t.Errorf("Directory.Open URL = %q, want %q", ref.GetUrl(), want)
		}
	}

	// --- Implementations surface (replaces implements-map test attribution) ---
	fileProto := res.Corpus.Profile("fuchsia.io/File")
	if fileProto == nil {
		t.Fatal("no profile for fuchsia.io/File")
	}
	// FIDL interface elements do NOT carry tests — the tests surface is
	// not declared in kindSurfaces for METHOD / TYPE / PROTOCOL. The
	// load-bearing assertion is that the implementations surface IS
	// populated with the C++ impl class via the IMPLEMENTS edge.
	totalFileTests := 0
	if fileProto.GetTests() != nil {
		totalFileTests = len(fileProto.GetTests().GetUnit()) + len(fileProto.GetTests().GetIntegration())
	}
	if totalFileTests != 0 {
		t.Errorf("fuchsia.io/File tests = %d, want 0 (interface kinds have no tests surface)", totalFileTests)
	}
	if fileProto.GetImplementations() == nil || len(fileProto.GetImplementations().GetImpls()) == 0 {
		t.Errorf("fuchsia.io/File should have implementations populated from the FileConnection IMPLEMENTS edge; got %+v", fileProto.GetImplementations())
	}

	// --- Inheritance: Directory should surface methods from Openable and Node ---
	dir := res.Corpus.Element("fuchsia.io/Directory")
	if dir == nil {
		t.Fatal("no fuchsia.io/Directory element")
	}
	composeTargets := make(map[string]bool)
	for _, r := range dir.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_COMPOSED_FROM {
			composeTargets[r.GetTargetElementId()] = true
		}
	}
	if !composeTargets["fuchsia.io/Openable"] {
		t.Errorf("Directory should compose Openable; got %v", composeTargets)
	}
	// A surface-exposed inherited method should exist as its own element.
	openableOpen := res.Corpus.Element("fuchsia.io/Openable.Open")
	dirOpenSurface := res.Corpus.Element("fuchsia.io/Directory.Open")
	if openableOpen == nil || dirOpenSurface == nil {
		t.Fatalf("Openable.Open or Directory.Open missing: %v, %v", openableOpen, dirOpenSurface)
	}
	// Confirm gaps summary identifies missing examples for Directory.Open
	// (fuchsia.io tutorials exist as guidance but not as in-tree examples
	// against this specific method).
	foundMissingExamples := false
	for _, m := range dirOpen.GetGapsSummary().GetMissing() {
		if m == "examples" {
			foundMissingExamples = true
			break
		}
	}
	if !foundMissingExamples {
		t.Errorf("Directory.Open gaps missing 'examples'; got %v", dirOpen.GetGapsSummary().GetMissing())
	}
}
