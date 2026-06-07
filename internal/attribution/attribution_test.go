package attribution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// fakeClient returns a fixed response regardless of the prompt — enough
// to drive the gate deterministically in a single-test corpus.
type fakeClient struct{ resp string }

func (f fakeClient) Name() string { return "fake:test" }
func (f fakeClient) Generate(_ context.Context, _ string) (string, error) {
	return f.resp, nil
}

// fakeCachedClient also implements llm.CachedGenerator and records that
// the cached path was taken (and that the prefix was the stable part).
type fakeCachedClient struct {
	resp         string
	cachedCalls  int
	lastPrefix   string
	lastVariable string
}

func (f *fakeCachedClient) Name() string { return "fake-cached:test" }
func (f *fakeCachedClient) Generate(_ context.Context, _ string) (string, error) {
	return f.resp, nil
}
func (f *fakeCachedClient) GenerateCached(_ context.Context, prefix, variable string) (string, error) {
	f.cachedCalls++
	f.lastPrefix = prefix
	f.lastVariable = variable
	return f.resp, nil
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func elem(id, local string, aliases ...string) *contractpb.ContractElement {
	return &contractpb.ContractElement{
		Id:      id,
		Library: "pw_rpc",
		Aliases: aliases,
		Kind:    contractpb.ContractElementKind_CPP_METHOD,
	}
}

// TestRun_GateDropsHallucinations is the safety-critical test: an edge
// whose cited line does NOT reference the element is dropped; a real one
// survives; an alias match is tagged macro-alias.
func TestRun_GateDropsHallucinations(t *testing.T) {
	root := t.TempDir()
	// lines: 1 TEST(...) { · 2 Server server; · 3 server.ProcessPacket(pkt); ·
	//        4 } · 5 // a note about Ghost here  (Ghost is >1 line from any cite)
	writeFile(t, root, "server_test.cc",
		"TEST(Server, Process) {\n  Server server;\n  server.ProcessPacket(pkt);\n}\n// a note about Ghost here\n")

	c := corpus.New()
	c.AddElement(elem("pw_rpc/pw::rpc::Server", "Server"))
	c.AddElement(elem("pw_rpc/pw::rpc::Server::ProcessPacket", "ProcessPacket"))
	c.AddElement(elem("pw_rpc/pw::rpc::Ghost", "Ghost"))
	c.AddTest(&testcasepb.TestCase{
		Id:        "Server.Process",
		Framework: "gtest",
		Location:  &commonpb.SourceLocation{Path: "server_test.cc", Line: 1},
	})

	// Model proposes three edges; Ghost is cited at line 3 which does NOT
	// contain "Ghost" (it's only in the line-4 comment) → must be dropped.
	resp := `[
	  {"element":"pw_rpc/pw::rpc::Server","line":2},
	  {"element":"pw_rpc/pw::rpc::Server::ProcessPacket","line":3},
	  {"element":"pw_rpc/pw::rpc::Ghost","line":2}
	]`
	p := New(Config{}, fakeClient{resp: resp})
	st, err := p.Run(context.Background(), c, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.TestEdges != 2 {
		t.Errorf("TestEdges = %d, want 2 (Server + ProcessPacket)", st.TestEdges)
	}
	if st.CiteDropped != 1 {
		t.Errorf("CiteDropped = %d, want 1 (Ghost)", st.CiteDropped)
	}
	// Both surviving edges must land in the LLM-tier bucket, not the
	// deterministic unit bucket (the moat).
	for _, id := range []string{"pw_rpc/pw::rpc::Server", "pw_rpc/pw::rpc::Server::ProcessPacket"} {
		refs := c.Profile(id).GetTests().GetLlmInferred()
		if len(refs) != 1 {
			t.Fatalf("%s: want 1 llm_inferred ref, got %d", id, len(refs))
		}
		if refs[0].GetProvenance().GetTier() != commonpb.RowProvenance_LLM {
			t.Errorf("%s: edge not LLM-tier", id)
		}
		if n := len(c.Profile(id).GetTests().GetUnit()); n != 0 {
			t.Errorf("%s: LLM edge leaked into deterministic unit bucket (%d)", id, n)
		}
	}
	// Ghost must have no edge.
	if n := len(c.Profile("pw_rpc/pw::rpc::Ghost").GetTests().GetLlmInferred()); n != 0 {
		t.Errorf("Ghost got %d edges, want 0 (hallucination must be dropped)", n)
	}
}

// TestRun_UsesCachedPrefix confirms the pass prefers the cached-prefix
// path when the backend supports it, sends one call per FILE (not per
// test), and puts the candidate list in the cacheable prefix while the
// file source goes in the variable body.
func TestRun_UsesCachedPrefix(t *testing.T) {
	root := t.TempDir()
	// Two tests in ONE file → must be a single cached call for the file.
	writeFile(t, root, "server_test.cc",
		"TEST(Server, A) {\n  Server server;\n}\nTEST(Server, B) {\n  Server s2;\n}\n")

	c := corpus.New()
	c.AddElement(elem("pw_rpc/pw::rpc::Server", "Server"))
	c.AddTest(&testcasepb.TestCase{Id: "Server.A", Framework: "gtest",
		Location: &commonpb.SourceLocation{Path: "server_test.cc", Line: 1}})
	c.AddTest(&testcasepb.TestCase{Id: "Server.B", Framework: "gtest",
		Location: &commonpb.SourceLocation{Path: "server_test.cc", Line: 4}})

	fc := &fakeCachedClient{resp: `[{"element":"pw_rpc/pw::rpc::Server","line":2},{"element":"pw_rpc/pw::rpc::Server","line":5}]`}
	p := New(Config{}, fc)
	st, err := p.Run(context.Background(), c, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fc.cachedCalls != 1 {
		t.Errorf("cachedCalls = %d, want 1 (one call for the one file)", fc.cachedCalls)
	}
	if !strings.Contains(fc.lastPrefix, "Candidate contract elements") {
		t.Errorf("cached prefix should hold the candidate list, got: %q", fc.lastPrefix)
	}
	if strings.Contains(fc.lastPrefix, "Server server;") {
		t.Error("file source leaked into the cached prefix; it must be in the variable body")
	}
	if !strings.Contains(fc.lastVariable, "Server server;") {
		t.Error("variable body should contain the file source")
	}
	// Both reference sites verified; the line-2 edge maps to test A, the
	// line-5 edge to test B (different test names → both kept).
	if st.TestEdges != 2 {
		t.Errorf("TestEdges = %d, want 2 (one per enclosing test)", st.TestEdges)
	}
}

// TestRun_AliasMatchTaggedMacroAlias confirms an edge that gates only via
// an alias is tagged transform=macro-alias (the hardening signal).
func TestRun_AliasMatchTaggedMacroAlias(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "log_test.cc", "TEST(Log, Dbg) {\n  DBG(\"hi\");\n}\n")

	c := corpus.New()
	// Element's canonical local is PW_LOG_DEBUG; DBG is a (macro) alias.
	c.AddElement(elem("pw_log/PW_LOG_DEBUG", "PW_LOG_DEBUG", "DBG"))
	c.AddTest(&testcasepb.TestCase{
		Id: "Log.Dbg", Framework: "gtest",
		Location: &commonpb.SourceLocation{Path: "log_test.cc", Line: 1},
	})
	resp := `[{"element":"pw_log/PW_LOG_DEBUG","line":2}]`
	p := New(Config{}, fakeClient{resp: resp})
	st, err := p.Run(context.Background(), c, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.TestEdges != 1 || st.AliasEdges != 1 {
		t.Fatalf("TestEdges=%d AliasEdges=%d, want 1/1", st.TestEdges, st.AliasEdges)
	}
	ref := c.Profile("pw_log/PW_LOG_DEBUG").GetTests().GetLlmInferred()[0]
	if ref.GetProvenance().GetTransform() != "macro-alias" {
		t.Errorf("transform = %q, want macro-alias", ref.GetProvenance().GetTransform())
	}
}

// TestRun_SkipsRedundantDeterministic confirms an LLM edge is NOT added
// when a deterministic edge already covers that (element, test).
func TestRun_SkipsRedundantDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "server_test.cc", "TEST(Server, Process) {\n  Server server;\n}\n")

	c := corpus.New()
	c.AddElement(elem("pw_rpc/pw::rpc::Server", "Server"))
	c.AddTest(&testcasepb.TestCase{
		Id: "Server.Process", Framework: "gtest",
		Location: &commonpb.SourceLocation{Path: "server_test.cc", Line: 1},
	})
	// Pre-seed a DETERMINISTIC edge for (Server, Server.Process).
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "pw_rpc/pw::rpc::Server",
		Tests: &coveragepb.TestCoverage{Unit: []*commonpb.TestRef{{
			TestName:   "Server.Process",
			Exercises:  "pw_rpc/pw::rpc::Server",
			Provenance: &commonpb.RowProvenance{Tier: commonpb.RowProvenance_DETERMINISTIC, Source: "gtest"},
		}}},
	})
	resp := `[{"element":"pw_rpc/pw::rpc::Server","line":2}]`
	p := New(Config{}, fakeClient{resp: resp})
	st, err := p.Run(context.Background(), c, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Redundant != 1 || st.TestEdges != 0 {
		t.Fatalf("Redundant=%d TestEdges=%d, want 1/0", st.Redundant, st.TestEdges)
	}
	// Still exactly one (deterministic) edge.
	if n := len(c.Profile("pw_rpc/pw::rpc::Server").GetTests().GetUnit()); n != 1 {
		t.Errorf("want 1 edge (the deterministic one), got %d", n)
	}
}
