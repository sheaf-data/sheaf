package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// syncBuffer is a goroutine-safe bytes.Buffer. The slog handler writes
// from the server's request goroutine while the test reads from the
// test goroutine, so both sides must hold the lock — the race detector
// flags an unsynchronized bytes.Buffer otherwise.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// mkIntegrationServer stands up a live HTTP server on a free port with a
// richer fixture corpus than mkServer (so library_snapshot /
// verify_invocation / find_examples all have data), a JSON slog logger
// writing into the returned buffer (so log assertions can parse
// records), and any test hooks installed before Start. Returns the
// base URL and the log buffer.
func mkIntegrationServer(t *testing.T, hooks map[string]func(json.RawMessage) (any, *rpcError)) (string, *syncBuffer) {
	t.Helper()
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Directory.Open", Kind: contractpb.ContractElementKind_METHOD,
		Library:           "fuchsia.io",
		DocCommentExcerpt: "Opens a child node relative to this directory",
		Location:          &commonpb.SourceLocation{Path: "fuchsia.io.fidl", Line: 10},
	})
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Directory.Close", Kind: contractpb.ContractElementKind_METHOD,
		Library:           "fuchsia.io",
		DocCommentExcerpt: "Closes the directory",
		Location:          &commonpb.SourceLocation{Path: "fuchsia.io.fidl", Line: 20},
	})
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "fuchsia.io/Directory.Open",
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
	srv := New(c, findings, &configpb.MCPServerConfig{
		Bind: "127.0.0.1", Port: uint32(freePort(t)), CacheTtlSeconds: 60,
	})
	logBuf := &syncBuffer{}
	srv = srv.WithLogger(slog.New(slog.NewJSONHandler(logBuf, nil)))
	if hooks != nil {
		srv.testHooks = hooks
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Start(ctx) }()
	addr := "http://" + srv.Addr()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			return addr, logBuf
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server did not become reachable")
	return "", nil
}

// rpcRaw posts a raw (possibly malformed) body and returns the decoded
// JSON-RPC response envelope. Unlike rpcCall it does not assume the
// body is well-formed JSON-RPC, so it can drive the parse-error case.
func rpcRaw(t *testing.T, addr, body string) map[string]any {
	t.Helper()
	resp, err := http.Post(addr+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("unmarshal response: %v\nbody=%s", err, b)
	}
	return r
}

// errorCode extracts result.error.code from a decoded response, or
// fails the test if there is no error object.
func errorCode(t *testing.T, r map[string]any) int {
	t.Helper()
	e, ok := r["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error object; got %+v", r)
	}
	code, ok := e["code"].(float64)
	if !ok {
		t.Fatalf("error has no numeric code; got %+v", e)
	}
	return int(code)
}

// TestIntegration_OrderedSession drives a representative client session
// against a live server over HTTP: discovery → one success per read op →
// the documented error matrix. This is the integration-level coverage
// the unit tests in mcp_test.go approximate per-op but never as an
// ordered whole.
func TestIntegration_OrderedSession(t *testing.T) {
	addr, _ := mkIntegrationServer(t, nil)

	// 1. Discovery: tools/list must advertise the full base set (7 read
	// tools; review_pr is only added when WithReview is configured,
	// which this fixture does not do).
	r := rpcCall(t, addr, "tools/list", map[string]any{})
	res, _ := r["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	if len(tools) < 7 {
		t.Errorf("tools/list: expected >=7 tools, got %d", len(tools))
	}

	// 2. One success per read op.
	successes := []struct {
		name   string
		params map[string]any
		check  func(t *testing.T, res map[string]any)
	}{
		{"query_contract", map[string]any{"element_id": "fuchsia.io/Directory.Open"}, func(t *testing.T, res map[string]any) {
			elem, _ := res["element"].(map[string]any)
			if elem["id"] != "fuchsia.io/Directory.Open" {
				t.Errorf("query_contract: element id = %v", elem["id"])
			}
		}},
		{"coverage", map[string]any{"element_id": "fuchsia.io/Directory.Open"}, func(t *testing.T, res map[string]any) {
			if res["elementId"] != "fuchsia.io/Directory.Open" {
				t.Errorf("coverage: elementId = %v", res["elementId"])
			}
		}},
		{"find_coverage_gaps", map[string]any{}, func(t *testing.T, res map[string]any) {
			if int(res["total"].(float64)) != 1 {
				t.Errorf("find_coverage_gaps: total = %v, want 1", res["total"])
			}
		}},
		{"find_examples", map[string]any{"query": "directory open"}, func(t *testing.T, res map[string]any) {
			if m, _ := res["matches"].([]any); len(m) == 0 {
				t.Errorf("find_examples: no matches")
			}
		}},
		{"verify_invocation", map[string]any{"invocation": "fuchsia.io/Directory.Open"}, func(t *testing.T, res map[string]any) {
			if res["matched"] != true {
				t.Errorf("verify_invocation: matched = %v", res["matched"])
			}
		}},
		{"list_libraries", map[string]any{}, func(t *testing.T, res map[string]any) {
			if int(res["total"].(float64)) != 1 {
				t.Errorf("list_libraries: total = %v, want 1", res["total"])
			}
		}},
		{"library_snapshot", map[string]any{"library": "fuchsia.io"}, func(t *testing.T, res map[string]any) {
			if res["library"] != "fuchsia.io" {
				t.Errorf("library_snapshot: library = %v", res["library"])
			}
			if elems, _ := res["elements"].([]any); len(elems) != 2 {
				t.Errorf("library_snapshot: elements = %d, want 2", len(elems))
			}
		}},
	}
	for _, tc := range successes {
		r := rpcCall(t, addr, tc.name, tc.params)
		if r["error"] != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, r["error"])
		}
		res, ok := r["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s: result not an object: %+v", tc.name, r)
		}
		tc.check(t, res)
	}

	// 3. Error matrix — the documented JSON-RPC codes.
	if code := errorCode(t, rpcRaw(t, addr, `{not json`)); code != -32700 {
		t.Errorf("malformed body: code = %d, want -32700", code)
	}
	if code := errorCode(t, rpcCall(t, addr, "nope", map[string]any{})); code != -32601 {
		t.Errorf("unknown method: code = %d, want -32601", code)
	}
	if code := errorCode(t, rpcCall(t, addr, "library_snapshot", map[string]any{})); code != -32602 {
		t.Errorf("missing required param: code = %d, want -32602", code)
	}
	if code := errorCode(t, rpcCall(t, addr, "query_contract", map[string]any{"element_id": "no/such"})); code != -32004 {
		t.Errorf("not-found element: code = %d, want -32004", code)
	}
}

// TestIntegration_PanicRecovery asserts the 14ABA714 contract: a panic
// inside an op is converted to a JSON-RPC -32603 (not a dropped
// connection), and the server survives — a subsequent normal request
// still succeeds.
func TestIntegration_PanicRecovery(t *testing.T) {
	hooks := map[string]func(json.RawMessage) (any, *rpcError){
		"_test_panic": func(json.RawMessage) (any, *rpcError) {
			panic("boom: simulated op panic on client input")
		},
	}
	addr, logBuf := mkIntegrationServer(t, hooks)

	// The panicking op returns a well-formed -32603 with a generic
	// message (no panic text leaked) and a correlation id in data.
	r := rpcCall(t, addr, "_test_panic", map[string]any{})
	if code := errorCode(t, r); code != -32603 {
		t.Fatalf("panic path: code = %d, want -32603", code)
	}
	e := r["error"].(map[string]any)
	if msg, _ := e["message"].(string); msg != "internal error" {
		t.Errorf("panic path: message = %q, want generic 'internal error'", msg)
	}
	if strings.Contains(strings.ToLower(jsonString(e)), "boom") {
		t.Errorf("panic path leaked the panic text to the client: %v", e)
	}
	data, _ := e["data"].(map[string]any)
	corr, _ := data["correlation_id"].(string)
	if corr == "" {
		t.Errorf("panic path: missing correlation_id in error data: %v", e)
	}

	// The server is still up: a normal request after the panic succeeds.
	r2 := rpcCall(t, addr, "tools/list", map[string]any{})
	if r2["error"] != nil {
		t.Fatalf("server did not survive panic: tools/list error %v", r2["error"])
	}

	// The panic was logged at error level with method + stack + the same
	// correlation id the client received.
	logs := logBuf.String()
	if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, "panic in op") {
		t.Errorf("expected an ERROR-level 'panic in op' log; got:\n%s", logs)
	}
	if !strings.Contains(logs, corr) {
		t.Errorf("panic log missing client-facing correlation id %q; got:\n%s", corr, logs)
	}
}

// TestIntegration_PerCallLogging asserts every dispatched call emits one
// structured record carrying method, id, duration and outcome, and that
// an error call records the code at warn level.
func TestIntegration_PerCallLogging(t *testing.T) {
	addr, logBuf := mkIntegrationServer(t, nil)

	rpcCall(t, addr, "list_libraries", map[string]any{})                        // ok
	rpcCall(t, addr, "library_snapshot", map[string]any{})                      // -32602 (warn)
	rpcCall(t, addr, "query_contract", map[string]any{"element_id": "no/such"}) // -32004 (warn)

	type record struct {
		Msg     string `json:"msg"`
		Level   string `json:"level"`
		Method  string `json:"method"`
		ID      string `json:"id"`
		Dur     int64  `json:"dur"`
		Outcome string `json:"outcome"`
		Code    int    `json:"code"`
	}
	var got []record
	sc := bufio.NewScanner(strings.NewReader(logBuf.String()))
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		if rec.Msg == "rpc call" {
			got = append(got, rec)
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rpc-call log records; got %d:\n%s", len(got), logBuf.String())
	}
	// Every record carries method + a duration + an outcome.
	for _, rec := range got {
		if rec.Method == "" {
			t.Errorf("record missing method: %+v", rec)
		}
		if rec.Dur < 0 {
			t.Errorf("record has negative duration: %+v", rec)
		}
		if rec.Outcome != "ok" && rec.Outcome != "error" {
			t.Errorf("record has bad outcome %q: %+v", rec.Outcome, rec)
		}
	}
	if got[0].Method != "list_libraries" || got[0].Outcome != "ok" || got[0].Level != "INFO" {
		t.Errorf("first record = %+v; want list_libraries/ok/INFO", got[0])
	}
	if got[1].Outcome != "error" || got[1].Code != -32602 || got[1].Level != "WARN" {
		t.Errorf("second record = %+v; want error/-32602/WARN", got[1])
	}
	if got[2].Code != -32004 || got[2].Level != "WARN" {
		t.Errorf("third record = %+v; want -32004/WARN", got[2])
	}
}

// jsonString marshals v to a string for substring leak checks.
func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
