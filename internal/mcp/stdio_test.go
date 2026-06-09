package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestServeStdio drives a full MCP session over the stdio transport with
// an in-memory pipe: the initialize handshake, the initialized
// notification (which must be silent), tools/list, and a tools/call that
// round-trips through a real op. It is the proof that a desktop MCP client
// (Claude Desktop, Cursor, Cline) speaking newline-delimited JSON-RPC gets
// correct answers.
func TestServeStdio(t *testing.T) {
	srv, _ := mkServer(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query_contract","arguments":{"element_id":"fuchsia.io/Directory.Open"}}}`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := srv.ServeStdio(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}

	lines := nonEmptyLines(out.String())
	// Three requests carried an id; the notification must produce nothing.
	if len(lines) != 3 {
		t.Fatalf("got %d response lines, want 3 (the notification must be silent):\n%s", len(lines), out.String())
	}

	// initialize handshake.
	initResp := decodeRPC(t, lines[0])
	if string(initResp.ID) != "1" || initResp.Error != nil {
		t.Fatalf("initialize: id=%s err=%v", initResp.ID, initResp.Error)
	}
	res := asMap(t, initResp.Result)
	if res["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", res["protocolVersion"])
	}
	if si := asMap(t, res["serverInfo"]); si["name"] != "sheaf" {
		t.Errorf("serverInfo.name = %v, want sheaf", si["name"])
	}
	if _, ok := asMap(t, res["capabilities"])["tools"]; !ok {
		t.Errorf("capabilities.tools missing: %v", res["capabilities"])
	}

	// tools/list.
	listResp := decodeRPC(t, lines[1])
	tools, ok := asMap(t, listResp.Result)["tools"].([]any)
	if !ok || len(tools) < 7 {
		t.Errorf("tools/list returned %d tools, want >=7", len(tools))
	}

	// tools/call round-trip.
	callResp := decodeRPC(t, lines[2])
	if callResp.Error != nil {
		t.Fatalf("tools/call protocol error: %+v", callResp.Error)
	}
	callRes := asMap(t, callResp.Result)
	if callRes["isError"] == true {
		t.Fatalf("tools/call returned isError (the op failed): %v", callRes)
	}
	content, ok := callRes["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tools/call content empty: %v", callRes)
	}
	first := asMap(t, content[0])
	if first["type"] != "text" || first["text"] == "" {
		t.Errorf("tools/call content[0] = %v, want non-empty text", first)
	}
	if _, ok := callRes["structuredContent"]; !ok {
		t.Errorf("tools/call missing structuredContent: %v", callRes)
	}
	// The op actually ran against the requested element (else it would
	// have errored), so its id appears in the serialized result.
	if !strings.Contains(first["text"].(string), "Directory.Open") {
		t.Errorf("tools/call text does not mention the queried element: %s", first["text"])
	}
}

// TestServeStdio_UnknownToolIsProtocolError confirms an unknown tool name
// is a JSON-RPC error (not a silent drop or a crash) and that a trailing
// notification still produces no output.
func TestServeStdio_UnknownToolIsProtocolError(t *testing.T) {
	srv, _ := mkServer(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		"",
	}, "\n")

	var out bytes.Buffer
	if err := srv.ServeStdio(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	lines := nonEmptyLines(out.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (unknown tool errors, notification silent):\n%s", len(lines), out.String())
	}
	r := decodeRPC(t, lines[0])
	if r.Error == nil || r.Error.Code != -32602 {
		t.Errorf("want -32602 for unknown tool, got error=%+v result=%v", r.Error, r.Result)
	}
}

// TestServeStdio_GarbageLineRecovers confirms a malformed line yields a
// parse-error response rather than tearing down the stream, and that a
// valid request after it is still answered.
func TestServeStdio_GarbageLineRecovers(t *testing.T) {
	srv, _ := mkServer(t)
	input := "this is not json\n" +
		`{"jsonrpc":"2.0","id":7,"method":"tools/list"}` + "\n"

	var out bytes.Buffer
	if err := srv.ServeStdio(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	lines := nonEmptyLines(out.String())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (parse error + recovered tools/list):\n%s", len(lines), out.String())
	}
	if e := decodeRPC(t, lines[0]).Error; e == nil || e.Code != -32700 {
		t.Errorf("first line: want -32700 parse error, got %+v", e)
	}
	if r := decodeRPC(t, lines[1]); string(r.ID) != "7" || r.Error != nil {
		t.Errorf("second line: want successful tools/list id=7, got id=%s err=%v", r.ID, r.Error)
	}
}

// --- helpers ---

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func decodeRPC(t *testing.T, line string) rpcResponse {
	t.Helper()
	var r rpcResponse
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	return r
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T: %v", v, v)
	}
	return m
}
