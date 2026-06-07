package gtest

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
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

func TestDiscover_BasicTEST(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/foo/foo_test.cc": `#include "foo.h"

TEST(FooTest, BarReturnsTrue) {
  EXPECT_TRUE(Bar());
}

TEST_F(BazFixture, QuxHandlesEmpty) {
  ASSERT_EQ(Qux(""), 0);
}
`,
	})
	p := New(Config{Include: []string{"src/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("got %d tests, want 2", len(tests))
	}
	ids := []string{tests[0].GetId(), tests[1].GetId()}
	want := []string{"FooTest.BarReturnsTrue", "BazFixture.QuxHandlesEmpty"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if tests[0].GetLocation().GetLine() != 3 {
		t.Errorf("line = %d, want 3", tests[0].GetLocation().GetLine())
	}
}

func TestDiscover_TEST_P(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/x_test.cc": `TEST_P(ParamSuite, RunsAllParams) {}`,
	})
	p := New(Config{Include: []string{"src/**/*_test.cc"}})
	tests, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if len(tests) != 1 || tests[0].GetId() != "ParamSuite.RunsAllParams" {
		t.Errorf("got %+v", tests)
	}
}

func TestDiscover_ExtraMacros(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/x_test.cc": `FUCHSIA_TEST(MySuite, MyTest) {}`,
	})
	p := New(Config{
		Include:         []string{"src/**/*_test.cc"},
		ExtraTestMacros: []string{"FUCHSIA_TEST"},
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 {
		t.Fatalf("got %d, want 1", len(tests))
	}
	if tests[0].GetId() != "MySuite.MyTest" {
		t.Errorf("id = %q", tests[0].GetId())
	}
}

func TestDiscover_ExcludeWorks(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/a_test.cc":      `TEST(A, A1) {}`,
		"src/skip/b_test.cc": `TEST(B, B1) {}`,
	})
	p := New(Config{
		Include: []string{"src/**/*_test.cc"},
		Exclude: []string{"src/skip/**"},
	})
	tests, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if len(tests) != 1 || tests[0].GetId() != "A.A1" {
		t.Errorf("expected only A.A1, got %+v", tests)
	}
}

func TestDiscover_NoFiles(t *testing.T) {
	repo := setupRepo(t, nil)
	p := New(Config{Include: []string{"src/**/*_test.cc"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 0 {
		t.Errorf("expected no tests, got %d", len(tests))
	}
}

func TestDiscover_DefaultIncludeMatches(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/foo_test.cc":     `TEST(A, B) {}`,
		"src/bar_unittest.cc": `TEST(C, D) {}`,
		"src/regular.cc":      `TEST(E, F) {}`, // doesn't match default include
	})
	p := New(Config{}) // no include — uses defaults
	tests, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	ids := make(map[string]bool)
	for _, tc := range tests {
		ids[tc.GetId()] = true
	}
	if !ids["A.B"] || !ids["C.D"] {
		t.Errorf("expected A.B and C.D in default discovery; got %v", ids)
	}
	if ids["E.F"] {
		t.Errorf("regular.cc shouldn't match default *_test.cc / *_unittest.cc patterns")
	}
}

func TestDiscover_TestNameTokenization(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/x_test.cc": `TEST(FidlReadTest, ReturnsErrInvalidArgs) {}`,
	})
	p := New(Config{Include: []string{"src/**/*_test.cc"}})
	tests, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	want := []string{"fidl", "read", "test", "returns", "err", "invalid", "args"}
	got := tests[0].GetNameTokens()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %v, want %v", got, want)
	}
}

func TestComputeLineOffsetsAndLookup(t *testing.T) {
	body := []byte("a\nbb\nccc\n")
	offs := computeLineOffsets(body)
	cases := []struct {
		off  int
		want int
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{4, 2},
		{5, 3},
		{8, 3},
	}
	for _, c := range cases {
		if got := lineFromOffset(offs, c.off); got != c.want {
			t.Errorf("lineFromOffset(%d) = %d, want %d", c.off, got, c.want)
		}
	}
}

func TestExtractGtestProtoRefs_StubCall(t *testing.T) {
	body := []byte(`
#include "src/proto/grpc/channelz/channelz.pb.h"
namespace { Channelz::Stub channelz_stub_; }

TEST_P(ChannelzServerTest, FailedRequestTest) {
  GetChannelRequest request;
  GetChannelResponse response;
  request.set_channel_id(GetChannelId(0));
  ClientContext context;
  Status s = channelz_stub_->GetChannel(&context, request, &response);
  EXPECT_TRUE(s.ok());
}
`)
	refs := extractGtestProtoRefs(body)
	// Cross-product: Channelz × {GetChannel, ...} → "Channelz.GetChannel".
	if !containsStr(refs, "Channelz.GetChannel") {
		t.Errorf("expected Channelz.GetChannel in refs, got %v", refs)
	}
	// Message fixtures.
	if !containsStr(refs, "GetChannelRequest") {
		t.Errorf("expected GetChannelRequest in refs, got %v", refs)
	}
	if !containsStr(refs, "GetChannelResponse") {
		t.Errorf("expected GetChannelResponse in refs, got %v", refs)
	}
}

func TestExtractGtestProtoRefs_NoStubNoCrossproduct(t *testing.T) {
	body := []byte(`
TEST(Foo, Bar) {
  some_obj->DoStuff();
  std::vector<int> v;
  v.push_back(1);
}
`)
	refs := extractGtestProtoRefs(body)
	// No Service::Stub / Service::Service mentioned, so no
	// cross-product method refs should be emitted.
	for _, r := range refs {
		if strings.Contains(r, ".") {
			t.Errorf("no cross-product expected with no service in scope; got %v", refs)
		}
	}
}

func TestExtractGtestProtoRefs_SkipsCommonAccessors(t *testing.T) {
	// When a Service is in scope, generic .Get()/.Set()/.Size() etc.
	// accessor calls must not be cross-producted into spurious refs
	// like "BarService.Get" — those names collide with virtually
	// every stdlib container method.
	body := []byte(`
class Foo : public BarService::Service {};

TEST(FooTest, Bar) {
  obj->Get();
  obj->Set();
  obj->Size();
  obj->Reset();
  obj->RealMethod();
}
`)
	refs := extractGtestProtoRefs(body)
	for _, r := range refs {
		if r == "BarService.Get" || r == "BarService.Set" ||
			r == "BarService.Size" || r == "BarService.Reset" {
			t.Errorf("generic accessor %q should be in skip list", r)
		}
	}
	if !containsStr(refs, "BarService.RealMethod") {
		t.Errorf("expected BarService.RealMethod in refs, got %v", refs)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
