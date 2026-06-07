package protocpp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

func setupRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("writefile: %v", err)
		}
	}
	return dir
}

// hasRef returns true iff want is present in refs.
func hasRef(refs []string, want string) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}

// hasNoRef returns true iff want is NOT present in refs.
func hasNoRef(refs []string, want string) bool {
	return !hasRef(refs, want)
}

// TestDiscover_StringLiteralRPCRef is the canonical envoy idiom: a gRPC
// method-name string literal naming an xDS RPC. Should produce the
// dotted ref the proto adapter aliases the METHOD element under, AND
// the parent-service alias (a test naming a method exercises the
// service too).
func TestDiscover_StringLiteralRPCRef(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/xds_test.cc": `#include "gtest/gtest.h"

TEST(XdsTest, StreamSends) {
  ExpectMethod("envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources");
}
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	for _, want := range []string{
		"envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources",
		"envoy.service.discovery.v3.AggregatedDiscoveryService", // parent-service alias
	} {
		if !hasRef(tests[0].GetContractRefs(), want) {
			t.Errorf("ContractRefs missing %q; got %v", want, tests[0].GetContractRefs())
		}
	}
}

// TestDiscover_QualifiedCPPTypeRef catches the most-common envoy idiom
// for message-type references: a fully-qualified C++ name produced by
// protoc. Should emit BOTH the dotted-package form and the bare local
// name (the proto adapter's aliases).
func TestDiscover_QualifiedCPPTypeRef(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/cluster_test.cc": `#include "gtest/gtest.h"

TEST_F(CdsTest, ParseCluster) {
  envoy::service::cluster::v3::CdsDummy dummy;
  EXPECT_TRUE(dummy.IsInitialized());
}
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	refs := tests[0].GetContractRefs()
	for _, want := range []string{
		"envoy.service.cluster.v3.CdsDummy",
		"CdsDummy",
	} {
		if !hasRef(refs, want) {
			t.Errorf("ContractRefs missing %q; got %v", want, refs)
		}
	}
}

// TestDiscover_PBHIncludeScope: a file with one .pb.h include and a
// bare type used in a fixture should emit the scoped dotted form.
func TestDiscover_PBHIncludeScope(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/endpoint_test.cc": `#include "gtest/gtest.h"
#include "envoy/service/endpoint/v3/eds.pb.h"

using namespace envoy::service::endpoint::v3;

TEST(EdsTest, ParseLeds) {
  LedsDummy fixture;
  EXPECT_TRUE(fixture.IsInitialized());
}
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	refs := tests[0].GetContractRefs()
	for _, want := range []string{
		"LedsDummy",
		"envoy.service.endpoint.v3.LedsDummy",
	} {
		if !hasRef(refs, want) {
			t.Errorf("ContractRefs missing %q; got %v", want, refs)
		}
	}
}

// TestDiscover_PrefixAnchoring: a std::string or absl::flat_hash_map
// must NOT be emitted as a ref. The qualified-type regex is anchored on
// the IDL prefix specifically to prevent this.
func TestDiscover_PrefixAnchoring(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/noise_test.cc": `#include "gtest/gtest.h"

TEST(NoiseTest, NoFalsePositives) {
  std::string s;
  absl::flat_hash_map<int, std::string> m;
  testing::NiceMock<MockConnection> conn;
  EXPECT_TRUE(s.empty());
}
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	for _, unwanted := range []string{
		"std.string", "absl.flat_hash_map", "testing.NiceMock",
		"string", "flat_hash_map", "NiceMock",
	} {
		if !hasNoRef(tests[0].GetContractRefs(), unwanted) {
			t.Errorf("ContractRefs unexpectedly contains %q; got %v",
				unwanted, tests[0].GetContractRefs())
		}
	}
}

// TestDiscover_MultipleTests_PerTestScoping: refs are scoped to the
// per-test body window. A literal in test A's body should NOT show up
// on test B's ContractRefs.
func TestDiscover_MultipleTests_PerTestScoping(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/multi_test.cc": `#include "gtest/gtest.h"

TEST(One, FirstTest) {
  ExpectMethod("envoy.service.cluster.v3.ClusterDiscoveryService.StreamClusters");
}

TEST(Two, SecondTest) {
  ExpectMethod("envoy.service.route.v3.RouteDiscoveryService.StreamRoutes");
}
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("got %d tests, want 2", len(tests))
	}
	// Build {testId: refs} map (order-independent).
	byID := map[string][]string{}
	for _, tc := range tests {
		byID[tc.GetId()] = tc.GetContractRefs()
	}
	first := "envoy.service.cluster.v3.ClusterDiscoveryService.StreamClusters"
	second := "envoy.service.route.v3.RouteDiscoveryService.StreamRoutes"
	if !hasRef(byID["One.FirstTest"], first) {
		t.Errorf("One.FirstTest missing %q; got %v", first, byID["One.FirstTest"])
	}
	if hasRef(byID["One.FirstTest"], second) {
		t.Errorf("One.FirstTest leaked Two's ref %q; got %v", second, byID["One.FirstTest"])
	}
	if !hasRef(byID["Two.SecondTest"], second) {
		t.Errorf("Two.SecondTest missing %q; got %v", second, byID["Two.SecondTest"])
	}
	if hasRef(byID["Two.SecondTest"], first) {
		t.Errorf("Two.SecondTest leaked One's ref %q; got %v", first, byID["Two.SecondTest"])
	}
}

// TestDiscover_PreambleConstantPropagatesWhenReferenced: a preamble
// constant's refs flow into every test that NAMES the constant, but
// not into tests that don't. Prevents the false positive where tests
// at the end of a file got attributed to a constant only used by tests
// at the top.
func TestDiscover_PreambleConstantPropagatesWhenReferenced(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/header_const_test.cc": `#include "gtest/gtest.h"

namespace {
constexpr char kAdsMethod[] =
    "envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources";
}

TEST(UsesIt, One) { ExpectMethod(kAdsMethod); }
TEST(UsesIt, Two) { ExpectMethod(kAdsMethod); }
TEST(DoesNotUseIt, Unrelated) { EXPECT_TRUE(1 + 1 == 2); }
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 3 {
		t.Fatalf("got %d tests, want 3", len(tests))
	}
	byID := map[string][]string{}
	for _, tc := range tests {
		byID[tc.GetId()] = tc.GetContractRefs()
	}
	wantMethod := "envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources"
	wantService := "envoy.service.discovery.v3.AggregatedDiscoveryService"
	// Tests that name the constant get both method + parent service.
	for _, id := range []string{"UsesIt.One", "UsesIt.Two"} {
		for _, want := range []string{wantMethod, wantService} {
			if !hasRef(byID[id], want) {
				t.Errorf("%s missing %q; got %v", id, want, byID[id])
			}
		}
	}
	// Test that doesn't reference the constant gets nothing.
	for _, unwanted := range []string{wantMethod, wantService} {
		if hasRef(byID["DoesNotUseIt.Unrelated"], unwanted) {
			t.Errorf("DoesNotUseIt.Unrelated unexpectedly contains %q; got %v",
				unwanted, byID["DoesNotUseIt.Unrelated"])
		}
	}
}

// TestDiscover_ParentServiceFromMethodRef: a single method literal
// emits BOTH the fully-qualified method form AND the parent service
// form. Without this, every test that exercises a method on
// AggregatedDiscoveryService leaves the service element's tests
// bucket empty — a chronic FN.
func TestDiscover_ParentServiceFromMethodRef(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/parent_test.cc": `#include "gtest/gtest.h"

TEST(Mux, Init) {
  FindMethodByName("envoy.service.cluster.v3.ClusterDiscoveryService.StreamClusters");
}
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	refs := tests[0].GetContractRefs()
	for _, want := range []string{
		"envoy.service.cluster.v3.ClusterDiscoveryService.StreamClusters",
		"envoy.service.cluster.v3.ClusterDiscoveryService",
	} {
		if !hasRef(refs, want) {
			t.Errorf("ContractRefs missing %q; got %v", want, refs)
		}
	}
}

// TestDiscover_HarnessIncludeFollowed: when a test .cc #includes a
// harness header that wires a method descriptor, the per-test refs
// pick up the descriptor even though the call site never appears in
// the .cc body. Critical for envoy's `*_test_harness.h` pattern.
func TestDiscover_HarnessIncludeFollowed(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/common/config/grpc_subscription_test_harness.h": `#pragma once

class GrpcSubscriptionTestHarness {
 public:
  GrpcSubscriptionTestHarness() {
    method_descriptor_ = google::protobuf::DescriptorPool::generated_pool()
        ->FindMethodByName(
            "envoy.service.endpoint.v3.EndpointDiscoveryService.StreamEndpoints");
  }
};
`,
		"test/common/config/grpc_subscription_test.cc": `#include "gtest/gtest.h"
#include "test/common/config/grpc_subscription_test_harness.h"

TEST_F(GrpcSubscriptionTest, SendsRequest) {
  // body doesn't textually mention the descriptor, but the harness ctor
  // wired StreamEndpoints into method_descriptor_ above.
  harness.sendRequest();
}
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	for _, want := range []string{
		"envoy.service.endpoint.v3.EndpointDiscoveryService.StreamEndpoints",
		"envoy.service.endpoint.v3.EndpointDiscoveryService",
	} {
		if !hasRef(tests[0].GetContractRefs(), want) {
			t.Errorf("ContractRefs missing harness-derived %q; got %v",
				want, tests[0].GetContractRefs())
		}
	}
}

// TestDiscover_HarnessIncludeCached: the harness scan is cached across
// test files, so two files including the same harness produce
// identical harness-derived refs (and we don't re-read the header).
// This is a behavioral check: not whether the cache exists but that
// the resulting refs are consistent.
func TestDiscover_HarnessIncludeCached(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/x/foo_test_harness.h": `#pragma once
// FindMethodByName("envoy.service.runtime.v3.RuntimeDiscoveryService.StreamRuntime")
class FooHarness {
 public:
  FooHarness() { x = "envoy.service.runtime.v3.RuntimeDiscoveryService.StreamRuntime"; }
};
`,
		"test/x/a_test.cc": `#include "gtest/gtest.h"
#include "test/x/foo_test_harness.h"

TEST(A, One) { EXPECT_TRUE(true); }
`,
		"test/x/b_test.cc": `#include "gtest/gtest.h"
#include "test/x/foo_test_harness.h"

TEST(B, One) { EXPECT_TRUE(true); }
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("got %d tests, want 2", len(tests))
	}
	want := "envoy.service.runtime.v3.RuntimeDiscoveryService.StreamRuntime"
	for _, tc := range tests {
		if !hasRef(tc.GetContractRefs(), want) {
			t.Errorf("%s missing %q; got %v", tc.GetId(), want, tc.GetContractRefs())
		}
	}
}

// TestDiscover_ExtraTestMacros wires through additional macro names so
// envoy's rare TYPED_TEST and project-custom macros still produce
// TestCase entries.
func TestDiscover_ExtraTestMacros(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/typed_test.cc": `#include "gtest/gtest.h"

TYPED_TEST(TypedFixture, HandlesAll) {
  envoy::service::secret::v3::SdsDummy dummy;
  EXPECT_TRUE(dummy.IsInitialized());
}
`,
	})
	p := New(Config{
		Include:         []string{"test/**/*_test.cc"},
		ExtraTestMacros: []string{"TYPED_TEST"},
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if tests[0].GetId() != "TypedFixture.HandlesAll" {
		t.Errorf("id = %q, want TypedFixture.HandlesAll", tests[0].GetId())
	}
	want := "envoy.service.secret.v3.SdsDummy"
	if !hasRef(tests[0].GetContractRefs(), want) {
		t.Errorf("ContractRefs missing %q; got %v", want, tests[0].GetContractRefs())
	}
}

// TestDiscover_CustomIDLPrefix verifies the prefix knob: an alternate
// project (say "myorg") should match its own dotted strings but NOT
// the default envoy literals.
func TestDiscover_CustomIDLPrefix(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/cross_test.cc": `#include "gtest/gtest.h"

TEST(Cross, Both) {
  ExpectMethod("myorg.service.foo.v1.FooService.DoIt");
  ExpectMethod("envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources");
  myorg::service::foo::v1::FooRequest req;
  envoy::service::discovery::v3::DiscoveryRequest other;
}
`,
	})
	p := New(Config{
		Include:   []string{"test/**/*_test.cc"},
		IDLPrefix: "myorg",
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	refs := tests[0].GetContractRefs()
	for _, want := range []string{
		"myorg.service.foo.v1.FooService.DoIt",
		"myorg.service.foo.v1.FooRequest",
		"FooRequest",
	} {
		if !hasRef(refs, want) {
			t.Errorf("myorg-prefix ContractRefs missing %q; got %v", want, refs)
		}
	}
	for _, unwanted := range []string{
		"envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources",
		"envoy.service.discovery.v3.DiscoveryRequest",
		"DiscoveryRequest",
	} {
		if hasRef(refs, unwanted) {
			t.Errorf("myorg-prefix ContractRefs unexpectedly contains %q; got %v", unwanted, refs)
		}
	}
}

// TestDiscover_LineNumbers makes sure the per-test line number reports
// the line where the TEST() macro starts, not 1.
func TestDiscover_LineNumbers(t *testing.T) {
	body := `#include "gtest/gtest.h"


TEST(A, First) { EXPECT_TRUE(true); }

TEST_F(B, Second) { EXPECT_TRUE(true); }
`
	repo := setupRepo(t, map[string]string{"test/lines_test.cc": body})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("got %d tests, want 2", len(tests))
	}
	byID := map[string]uint32{}
	for _, tc := range tests {
		byID[tc.GetId()] = tc.GetLocation().GetLine()
	}
	if byID["A.First"] != 4 {
		t.Errorf("A.First line = %d, want 4", byID["A.First"])
	}
	if byID["B.Second"] != 6 {
		t.Errorf("B.Second line = %d, want 6", byID["B.Second"])
	}
}

// TestDiscover_ContractRefsAreSorted: deterministic ordering is part of
// the snapshot contract — downstream consumers (and the snapshot hash)
// depend on it.
func TestDiscover_ContractRefsAreSorted(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/sorted_test.cc": `#include "gtest/gtest.h"

TEST(S, Many) {
  envoy::service::discovery::v3::DiscoveryRequest req;
  envoy::service::cluster::v3::CdsDummy cds;
  ExpectMethod("envoy.service.route.v3.RouteDiscoveryService.StreamRoutes");
  ExpectMethod("envoy.service.listener.v3.ListenerDiscoveryService.StreamListeners");
}
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	refs := tests[0].GetContractRefs()
	sorted := append([]string(nil), refs...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(refs, sorted) {
		t.Errorf("refs not sorted; got %v", refs)
	}
}

// ----------------------------------------------------------------
// Namespace-scope-aware fixture extractor (PR 2). Tests cover the
// canonical Pigweed pattern (`namespace pw::rpc { TEST(EchoService, …) }`)
// and the precision-critical edge cases: braces in strings/comments,
// anonymous-namespace transparency, prefix-mismatched namespaces,
// file-scope tests with no namespace, and the extra_test_macros knob.
// ----------------------------------------------------------------

// TestDiscover_NSFlat: flat `namespace pw::rpc { TEST(EchoService, …) }`
// with idl_prefix=pw should emit the bare "EchoService" ref so the
// indexer's alias-match path joins it to the pw.rpc/EchoService
// element.
func TestDiscover_NSFlat(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/echo_service_test.cc": `#include "gtest/gtest.h"

namespace pw::rpc {

TEST(EchoService, EchoesPayload) {
  EXPECT_TRUE(true);
}

}  // namespace pw::rpc
`,
	})
	p := New(Config{
		Include:   []string{"test/**/*_test.cc"},
		IDLPrefix: "pw",
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if !hasRef(tests[0].GetContractRefs(), "EchoService") {
		t.Errorf("ContractRefs missing bare 'EchoService'; got %v", tests[0].GetContractRefs())
	}
}

// TestDiscover_NSNestedBraces: `namespace pw { namespace rpc { TEST(...) } }`
// must accumulate the namespace stack via brace-nested form just as
// well as the C++17 flat form.
func TestDiscover_NSNestedBraces(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/nested_test.cc": `#include "gtest/gtest.h"

namespace pw {
namespace rpc {

TEST(EchoService, EchoesPayload) {
  EXPECT_TRUE(true);
}

}  // namespace rpc
}  // namespace pw
`,
	})
	p := New(Config{
		Include:   []string{"test/**/*_test.cc"},
		IDLPrefix: "pw",
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if !hasRef(tests[0].GetContractRefs(), "EchoService") {
		t.Errorf("ContractRefs missing bare 'EchoService'; got %v", tests[0].GetContractRefs())
	}
}

// TestDiscover_NSAnonymousInsideNS: an anonymous namespace inside a
// named one is transparent — the test should still emit the bare
// fixture ref because the enclosing pw::rpc namespace is still in
// effect.
func TestDiscover_NSAnonymousInsideNS(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/anon_test.cc": `#include "gtest/gtest.h"

namespace pw::rpc {
namespace {

TEST(Helper, DoesWork) {
  EXPECT_TRUE(true);
}

}  // namespace
}  // namespace pw::rpc
`,
	})
	p := New(Config{
		Include:   []string{"test/**/*_test.cc"},
		IDLPrefix: "pw",
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if !hasRef(tests[0].GetContractRefs(), "Helper") {
		t.Errorf("ContractRefs missing bare 'Helper'; got %v", tests[0].GetContractRefs())
	}
}

// TestDiscover_NSMismatch: `namespace foo::bar { TEST(Thing, …) }` with
// idl_prefix=pw must NOT emit a bare ref. The namespace anchor exists
// specifically to filter out unrelated namespaces.
func TestDiscover_NSMismatch(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/foreign_test.cc": `#include "gtest/gtest.h"

namespace foo::bar {

TEST(Thing, DoesStuff) {
  EXPECT_TRUE(true);
}

}  // namespace foo::bar
`,
	})
	p := New(Config{
		Include:   []string{"test/**/*_test.cc"},
		IDLPrefix: "pw",
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if hasRef(tests[0].GetContractRefs(), "Thing") {
		t.Errorf("ContractRefs unexpectedly contains bare 'Thing'; got %v",
			tests[0].GetContractRefs())
	}
}

// TestDiscover_NSFileScope: TEST() at file scope (no enclosing
// namespace) must NOT emit a bare ref — the extractor requires the
// namespace anchor to admit a candidate.
func TestDiscover_NSFileScope(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/filescope_test.cc": `#include "gtest/gtest.h"

TEST(Thing, DoesStuff) {
  EXPECT_TRUE(true);
}
`,
	})
	p := New(Config{
		Include:   []string{"test/**/*_test.cc"},
		IDLPrefix: "pw",
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if hasRef(tests[0].GetContractRefs(), "Thing") {
		t.Errorf("ContractRefs unexpectedly contains bare 'Thing' at file scope; got %v",
			tests[0].GetContractRefs())
	}
}

// TestDiscover_NSBraceInString: a `}` inside a string literal must NOT
// pop the namespace stack. Precision-critical: a single mis-pop would
// drop subsequent tests out of the namespace they're really in and
// silently lose attribution on real code.
func TestDiscover_NSBraceInString(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/brace_string_test.cc": `#include "gtest/gtest.h"

namespace pw::rpc {

const char* kJson = "{ \"close\": \"} namespace fake {\" }";

TEST(EchoService, EchoesPayload) {
  EXPECT_TRUE(true);
}

}  // namespace pw::rpc
`,
	})
	p := New(Config{
		Include:   []string{"test/**/*_test.cc"},
		IDLPrefix: "pw",
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if !hasRef(tests[0].GetContractRefs(), "EchoService") {
		t.Errorf("brace-in-string broke namespace tracking; ContractRefs missing 'EchoService'; got %v",
			tests[0].GetContractRefs())
	}
}

// TestDiscover_NSBraceInComment: `// }` and `/* } */` must NOT pop the
// namespace stack. Same precision concern as brace-in-string.
func TestDiscover_NSBraceInComment(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/brace_comment_test.cc": `#include "gtest/gtest.h"

namespace pw::rpc {

// closing brace: } should not pop
/* block brace: } */ /* another } here */

TEST(EchoService, EchoesPayload) {
  EXPECT_TRUE(true);
}

}  // namespace pw::rpc
`,
	})
	p := New(Config{
		Include:   []string{"test/**/*_test.cc"},
		IDLPrefix: "pw",
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if !hasRef(tests[0].GetContractRefs(), "EchoService") {
		t.Errorf("brace-in-comment broke namespace tracking; ContractRefs missing 'EchoService'; got %v",
			tests[0].GetContractRefs())
	}
}

// TestDiscover_NSExtraMacros: extra_test_macros adds custom macro
// names to the fixture-emitting set so projects with their own test
// wrappers participate without code change.
func TestDiscover_NSExtraMacros(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/extra_test.cc": `#include "gtest/gtest.h"

namespace pw::rpc {

PW_PWPB_TEST_METHOD_CONTEXT_TEST(EchoService, EchoesPayload) {
  EXPECT_TRUE(true);
}

}  // namespace pw::rpc
`,
	})
	p := New(Config{
		Include:         []string{"test/**/*_test.cc"},
		IDLPrefix:       "pw",
		ExtraTestMacros: []string{"PW_PWPB_TEST_METHOD_CONTEXT_TEST"},
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1; tests=%v", len(tests), tests)
	}
	if !hasRef(tests[0].GetContractRefs(), "EchoService") {
		t.Errorf("ContractRefs missing 'EchoService' from extra macro; got %v",
			tests[0].GetContractRefs())
	}
}

// TestDiscover_NSNoIDLPrefix: empty idl_prefix means the matcher has
// no anchor, so the namespace extractor must emit nothing — otherwise
// every test in the file would get its fixture name attached as a
// bare ref against unrelated namespaces.
//
// New(Config{}) sets IDLPrefix to "envoy" via the empty-string
// default; to verify the no-anchor behavior we construct a Parser
// with the field explicitly cleared after New.
func TestDiscover_NSNoIDLPrefix(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/no_prefix_test.cc": `#include "gtest/gtest.h"

namespace pw::rpc {

TEST(EchoService, EchoesPayload) {
  EXPECT_TRUE(true);
}

}  // namespace pw::rpc
`,
	})
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	p.idlPrefix = "" // force no-anchor mode
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	if hasRef(tests[0].GetContractRefs(), "EchoService") {
		t.Errorf("no-prefix mode emitted bare 'EchoService'; got %v",
			tests[0].GetContractRefs())
	}
	// Also exercise the standalone API directly.
	body := `namespace pw::rpc { TEST(EchoService, Foo) {} }`
	if got := extractNamespaceScopedFixtureRefs(body, "", nil); got != nil {
		t.Errorf("extractNamespaceScopedFixtureRefs(empty prefix) = %v, want nil", got)
	}
}

// TestDiscover_NSNoRegression: a file that previously produced refs
// via the OTHER four extractors must continue to produce them, and
// the new extractor must not emit any unwanted bare refs. The new
// extractor is purely additive.
func TestDiscover_NSNoRegression(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"test/legacy_test.cc": `#include "gtest/gtest.h"

TEST(XdsTest, StreamSends) {
  ExpectMethod("envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources");
  envoy::service::cluster::v3::CdsDummy dummy;
}
`,
	})
	// Default envoy prefix. The TEST() is at file scope so the new
	// extractor emits no bare suite ref.
	p := New(Config{Include: []string{"test/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d tests, want 1", len(tests))
	}
	refs := tests[0].GetContractRefs()
	// Legacy refs still present.
	for _, want := range []string{
		"envoy.service.discovery.v3.AggregatedDiscoveryService.StreamAggregatedResources",
		"envoy.service.discovery.v3.AggregatedDiscoveryService",
		"envoy.service.cluster.v3.CdsDummy",
		"CdsDummy",
	} {
		if !hasRef(refs, want) {
			t.Errorf("ContractRefs missing legacy ref %q; got %v", want, refs)
		}
	}
	// New extractor should NOT emit the suite name at file scope.
	if hasRef(refs, "XdsTest") {
		t.Errorf("namespace extractor emitted bare 'XdsTest' at file scope; got %v", refs)
	}
}

// TestNamespaceAt_UnitCases exercises the namespace tracker in
// isolation across the precision-critical edge cases — easier to
// debug when the full Discover loop hides which step misbehaved.
func TestNamespaceAt_UnitCases(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		marker string // first occurrence locates the query offset
		want   string
	}{
		{
			name:   "file scope",
			body:   `int x; /*marker*/ int y;`,
			marker: "/*marker*/",
			want:   "",
		},
		{
			name:   "flat ns",
			body:   `namespace pw::rpc { int x; /*marker*/ }`,
			marker: "/*marker*/",
			want:   "pw.rpc",
		},
		{
			name:   "nested via braces",
			body:   `namespace pw { namespace rpc { int x; /*marker*/ } }`,
			marker: "/*marker*/",
			want:   "pw.rpc",
		},
		{
			name:   "anonymous inside named",
			body:   `namespace pw::rpc { namespace { int x; /*marker*/ } }`,
			marker: "/*marker*/",
			want:   "pw.rpc",
		},
		{
			name:   "after close",
			body:   `namespace pw::rpc { int x; } /*marker*/ int y;`,
			marker: "/*marker*/",
			want:   "",
		},
		{
			name:   "brace in string",
			body:   `namespace pw::rpc { const char* s = "}"; /*marker*/ }`,
			marker: "/*marker*/",
			want:   "pw.rpc",
		},
		{
			name:   "brace in line comment",
			body:   "namespace pw::rpc { // }\n /*marker*/ }",
			marker: "/*marker*/",
			want:   "pw.rpc",
		},
		{
			name:   "brace in block comment",
			body:   `namespace pw::rpc { /* } */ /*marker*/ }`,
			marker: "/*marker*/",
			want:   "pw.rpc",
		},
		{
			name:   "using namespace ignored",
			body:   `using namespace foo; /*marker*/ int x;`,
			marker: "/*marker*/",
			want:   "",
		},
		{
			name:   "namespace alias ignored",
			body:   `namespace alias = foo::bar; /*marker*/ int x;`,
			marker: "/*marker*/",
			want:   "",
		},
		{
			name:   "non-ns block transparent",
			body:   `namespace pw::rpc { class C { int x; /*marker*/ }; }`,
			marker: "/*marker*/",
			want:   "pw.rpc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := strings.Index(tc.body, tc.marker)
			if idx < 0 {
				t.Fatalf("marker %q not found in body", tc.marker)
			}
			got := namespaceAt(tc.body, idx)
			if got != tc.want {
				t.Errorf("namespaceAt(...) = %q, want %q", got, tc.want)
			}
		})
	}
}
