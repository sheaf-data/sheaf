package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// freePort asks the OS for an unused TCP port on the loopback interface
// and returns it. The listener is closed before returning, so the port is
// free for the caller to bind; the brief reuse window is harmless here.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func mkServer(t *testing.T) (*Server, string) {
	t.Helper()
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Directory.Open", Kind: contractpb.ContractElementKind_METHOD,
		Library:           "fuchsia.io",
		DocCommentExcerpt: "Opens a child node",
	})
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "fuchsia.io/Directory.Open",
		Docs: &coveragepb.DocCoverage{Reference: &coveragepb.DocCoverage_Reference{
			Fidldoc: []*commonpb.DocRef{{
				Substance: commonpb.Substance_SUBSTANTIVE,
				Url:       "https://fuchsia.dev/reference/fidl/fuchsia.io#Directory.Open",
			}},
		}},
		Tests: &coveragepb.TestCoverage{
			Unit: []*commonpb.TestRef{{TestName: "DirectoryTest.OpenWorks"}},
		},
	})
	findings := []*findingpb.Finding{
		{
			Id:       "x:missing:fuchsia.io/Directory.Open:examples",
			Kind:     findingpb.FindingKind_MISSING_IN_CATEGORY,
			Subject:  "fuchsia.io/Directory.Open",
			Severity: commonpb.Severity_WARNING,
			Analyzer: "missing-in-category",
		},
	}
	// Each subtest gets its own OS-assigned free port. A shared fixed
	// port made tests race: a server's listener may not be fully closed
	// when the next test binds the same port, so Start() fails with
	// EADDRINUSE, /healthz never answers, and we hit "server did not
	// become reachable" — flaky on slow CI runners (seen on PR #70's
	// merge run). freePort hands out a distinct, currently-free port.
	srv := New(c, findings, &configpb.MCPServerConfig{
		Bind: "127.0.0.1", Port: uint32(freePort(t)), CacheTtlSeconds: 60,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = srv.Start(ctx)
	}()
	// Wait until /healthz responds.
	addr := "http://" + srv.Addr()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			return srv, addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not become reachable")
	return nil, ""
}

func rpcCall(t *testing.T, addr, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	resp, err := http.Post(addr+"/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, b)
	}
	return r
}

func TestMCP_Health(t *testing.T) {
	_, addr := mkServer(t)
	resp, err := http.Get(addr + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"status":"ok"`) {
		t.Errorf("body = %s", b)
	}
}

func TestMCP_ToolsList(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "tools/list", map[string]any{})
	res, _ := r["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) < 5 {
		t.Errorf("expected >=5 tools; got %d", len(tools))
	}
}

func TestMCP_QueryContract(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "query_contract", map[string]any{
		"element_id": "fuchsia.io/Directory.Open",
	})
	if r["error"] != nil {
		t.Fatalf("error: %v", r["error"])
	}
	res, _ := r["result"].(map[string]any)
	elem, _ := res["element"].(map[string]any)
	if elem["id"] != "fuchsia.io/Directory.Open" {
		t.Errorf("element id = %v", elem["id"])
	}
	if _, ok := res["coverage"]; !ok {
		t.Errorf("expected coverage in result; got %+v", res)
	}
}

func TestMCP_QueryContract_NotFound(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "query_contract", map[string]any{
		"element_id": "nope/nope",
	})
	if r["error"] == nil {
		t.Errorf("expected error; got %+v", r)
	}
}

func TestMCP_QueryContract_Subtree(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "query_contract", map[string]any{
		"element_id": "fuchsia.io/Directory.Open",
		"subtree":    "tests",
	})
	res, _ := r["result"].(map[string]any)
	if _, ok := res["tests"]; !ok {
		t.Errorf("expected tests subtree; got keys %v", keysOf(res))
	}
	if _, ok := res["coverage"]; ok {
		t.Errorf("subtree shouldn't include full coverage; got %v", res)
	}
}

func TestMCP_Coverage(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "coverage", map[string]any{
		"element_id": "fuchsia.io/Directory.Open",
	})
	res, _ := r["result"].(map[string]any)
	if res["elementId"] != "fuchsia.io/Directory.Open" {
		t.Errorf("elementId = %v", res["elementId"])
	}
}

func TestMCP_FindCoverageGaps(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "find_coverage_gaps", map[string]any{})
	res, _ := r["result"].(map[string]any)
	if int(res["total"].(float64)) != 1 {
		t.Errorf("expected 1 finding; got %v", res["total"])
	}
}

func TestMCP_FindCoverageGaps_FilterByLibrary(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "find_coverage_gaps", map[string]any{
		"library": "nonexistent.lib",
	})
	res, _ := r["result"].(map[string]any)
	if res["total"] == nil || int(res["total"].(float64)) != 0 {
		t.Errorf("library filter should drop all; got %v", res)
	}
}

func TestMCP_FindExamples(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "find_examples", map[string]any{
		"query": "directory open",
	})
	res, _ := r["result"].(map[string]any)
	matches, _ := res["matches"].([]any)
	if len(matches) == 0 {
		t.Errorf("expected at least one match for 'directory open'; got %v", res)
	}
}

func TestMCP_VerifyInvocation_ExactMatch(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "verify_invocation", map[string]any{
		"invocation": "fuchsia.io/Directory.Open",
	})
	res, _ := r["result"].(map[string]any)
	if res["matched"] != true {
		t.Errorf("matched = %v", res["matched"])
	}
}

func TestMCP_VerifyInvocation_FuzzyOnly(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "verify_invocation", map[string]any{
		"invocation": "directory open something",
	})
	res, _ := r["result"].(map[string]any)
	if res["matched"] != false {
		t.Errorf("matched = %v", res["matched"])
	}
	if res["candidate"] != "fuchsia.io/Directory.Open" {
		t.Errorf("candidate = %v", res["candidate"])
	}
}

func TestMCP_BearerAuth(t *testing.T) {
	t.Setenv("SHEAF_TEST_TOKEN", "secret123")
	c := corpus.New()
	srv := New(c, nil, &configpb.MCPServerConfig{
		Bind: "127.0.0.1", Port: 17800,
		Auth: &configpb.AuthConfig{Mode: configpb.AuthConfig_BEARER, BearerTokenEnv: "SHEAF_TEST_TOKEN"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Start(ctx) }()
	addr := "http://" + srv.Addr()
	// Wait until reachable.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", addr+"/healthz", nil)
		req.Header.Set("Authorization", "Bearer secret123")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Unauthorized → 401
	resp, err := http.Get(addr + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 without auth; got %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Wrong token → 401
	req, _ := http.NewRequest("GET", addr+"/healthz", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401 with wrong token; got %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Correct → 200
	req, _ = http.NewRequest("GET", addr+"/healthz", nil)
	req.Header.Set("Authorization", "Bearer secret123")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 with right token; got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestMCP_ListLibraries(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "list_libraries", map[string]any{})
	if r["error"] != nil {
		t.Fatalf("error: %v", r["error"])
	}
	res, _ := r["result"].(map[string]any)
	libs, _ := res["libraries"].([]any)
	if len(libs) != 1 {
		t.Fatalf("expected 1 library; got %d (%v)", len(libs), libs)
	}
	first, _ := libs[0].(map[string]any)
	if first["library"] != "fuchsia.io" {
		t.Errorf("library = %v; want fuchsia.io", first["library"])
	}
	if int(first["elements"].(float64)) != 1 {
		t.Errorf("elements = %v; want 1", first["elements"])
	}
	if int(first["profiles"].(float64)) != 1 {
		t.Errorf("profiles = %v; want 1", first["profiles"])
	}
	if int(first["findings"].(float64)) != 1 {
		t.Errorf("findings = %v; want 1", first["findings"])
	}
}

func TestMCP_LibrarySnapshot(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "library_snapshot", map[string]any{"library": "fuchsia.io"})
	if r["error"] != nil {
		t.Fatalf("error: %v", r["error"])
	}
	res, _ := r["result"].(map[string]any)
	if res["library"] != "fuchsia.io" {
		t.Errorf("library = %v", res["library"])
	}
	elems, _ := res["elements"].([]any)
	if len(elems) != 1 {
		t.Errorf("elements = %d; want 1", len(elems))
	}
	profs, _ := res["profiles"].([]any)
	if len(profs) != 1 {
		t.Errorf("profiles = %d; want 1", len(profs))
	}
	finds, _ := res["findings"].([]any)
	if len(finds) != 1 {
		t.Errorf("findings = %d; want 1", len(finds))
	}
}

func TestMCP_LibrarySnapshot_RequiresLibrary(t *testing.T) {
	_, addr := mkServer(t)
	r := rpcCall(t, addr, "library_snapshot", map[string]any{})
	if r["error"] == nil {
		t.Errorf("expected error for missing library param; got %v", r)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Silence unused import warning for fmt in some build configs.
var _ = fmt.Sprintf
