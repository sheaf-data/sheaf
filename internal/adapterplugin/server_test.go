package adapterplugin

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	pluginpb "github.com/sheaf-data/sheaf/proto/adapterplugin"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

var testInfo = Info{
	Name:    "fake",
	Version: "9.9.9",
	Roles:   []pluginpb.Role{pluginpb.Role_ROLE_TEST_PARSER},
}

// writeReq marshals a DiscoverRequest into a framed byte stream.
func writeReq(t *testing.T, req *pluginpb.DiscoverRequest) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteMessage(&buf, req); err != nil {
		t.Fatalf("frame request: %v", err)
	}
	return &buf
}

func TestServeConn_HappyPath(t *testing.T) {
	in := writeReq(t, &pluginpb.DiscoverRequest{
		ProtocolVersion: ProtocolVersion,
		Role:            pluginpb.Role_ROLE_TEST_PARSER,
		RepoPath:        "/repo",
	})
	h := func(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
		if req.GetRepoPath() != "/repo" {
			t.Errorf("handler saw repo_path %q", req.GetRepoPath())
		}
		return &pluginpb.DiscoverResponse{Tests: []*testcasepb.TestCase{{Id: "t1"}}}, nil
	}
	var out bytes.Buffer
	if err := ServeConn(context.Background(), in, &out, testInfo, h); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	resp := decodeResp(t, &out)
	if resp.GetProtocolVersion() != ProtocolVersion {
		t.Errorf("response protocol_version = %d, want %d", resp.GetProtocolVersion(), ProtocolVersion)
	}
	if got := resp.GetTests(); len(got) != 1 || got[0].GetId() != "t1" {
		t.Errorf("tests = %v, want one with id t1", got)
	}
	if resp.GetError() != "" {
		t.Errorf("unexpected error: %q", resp.GetError())
	}
}

func TestServeConn_ProtocolMismatch(t *testing.T) {
	in := writeReq(t, &pluginpb.DiscoverRequest{ProtocolVersion: ProtocolVersion + 1})
	called := false
	h := func(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
		called = true
		return &pluginpb.DiscoverResponse{}, nil
	}
	var out bytes.Buffer
	if err := ServeConn(context.Background(), in, &out, testInfo, h); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	resp := decodeResp(t, &out)
	if called {
		t.Error("handler must not run on a protocol mismatch")
	}
	if !strings.Contains(resp.GetError(), "protocol version mismatch") {
		t.Errorf("error = %q, want a protocol-version mismatch", resp.GetError())
	}
	// Even an error response reports the plugin's real protocol version.
	if resp.GetProtocolVersion() != ProtocolVersion {
		t.Errorf("response protocol_version = %d, want %d", resp.GetProtocolVersion(), ProtocolVersion)
	}
}

func TestServeConn_HandlerError(t *testing.T) {
	in := writeReq(t, &pluginpb.DiscoverRequest{ProtocolVersion: ProtocolVersion})
	h := func(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
		return nil, errors.New("boom")
	}
	var out bytes.Buffer
	if err := ServeConn(context.Background(), in, &out, testInfo, h); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	resp := decodeResp(t, &out)
	if resp.GetError() != "boom" {
		t.Errorf("error = %q, want boom", resp.GetError())
	}
}

func TestServeConn_HandlerPanicRecovered(t *testing.T) {
	in := writeReq(t, &pluginpb.DiscoverRequest{ProtocolVersion: ProtocolVersion})
	h := func(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
		panic("kaboom")
	}
	var out bytes.Buffer
	if err := ServeConn(context.Background(), in, &out, testInfo, h); err != nil {
		t.Fatalf("ServeConn should recover the panic, got: %v", err)
	}
	resp := decodeResp(t, &out)
	if !strings.Contains(resp.GetError(), "panic") || !strings.Contains(resp.GetError(), "kaboom") {
		t.Errorf("error = %q, want it to mention the recovered panic", resp.GetError())
	}
}

func TestServeConn_NilResponseOK(t *testing.T) {
	in := writeReq(t, &pluginpb.DiscoverRequest{ProtocolVersion: ProtocolVersion})
	h := func(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
		return nil, nil // no rows, no error
	}
	var out bytes.Buffer
	if err := ServeConn(context.Background(), in, &out, testInfo, h); err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
	resp := decodeResp(t, &out)
	if resp.GetError() != "" || len(resp.GetTests()) != 0 {
		t.Errorf("want empty response, got %v", resp)
	}
}

func TestServeMain_InfoProbe(t *testing.T) {
	var out, errw bytes.Buffer
	h := func(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
		t.Error("handler must not run for the info probe")
		return nil, nil
	}
	code := serveMain([]string{InfoFlag}, bytes.NewReader(nil), &out, &errw, testInfo, h)
	if code != 0 {
		t.Fatalf("exit code = %d (%s)", code, errw.String())
	}
	var pi pluginpb.PluginInfo
	if err := ReadMessage(&out, &pi); err != nil {
		t.Fatalf("decode PluginInfo: %v", err)
	}
	if pi.GetName() != "fake" || pi.GetVersion() != "9.9.9" || pi.GetProtocolVersion() != ProtocolVersion {
		t.Errorf("PluginInfo = %v, want name=fake version=9.9.9 proto=%d", &pi, ProtocolVersion)
	}
	if len(pi.GetRoles()) != 1 || pi.GetRoles()[0] != pluginpb.Role_ROLE_TEST_PARSER {
		t.Errorf("roles = %v, want [ROLE_TEST_PARSER]", pi.GetRoles())
	}
}

func TestServeMain_DiscoverViaMain(t *testing.T) {
	in := writeReq(t, &pluginpb.DiscoverRequest{ProtocolVersion: ProtocolVersion, RepoPath: "/x"})
	h := func(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
		return &pluginpb.DiscoverResponse{Tests: []*testcasepb.TestCase{{Id: "ok"}}}, nil
	}
	var out, errw bytes.Buffer
	if code := serveMain(nil, in, &out, &errw, testInfo, h); code != 0 {
		t.Fatalf("exit code = %d (%s)", code, errw.String())
	}
	resp := decodeResp(t, &out)
	if len(resp.GetTests()) != 1 || resp.GetTests()[0].GetId() != "ok" {
		t.Errorf("tests = %v", resp.GetTests())
	}
}

func decodeResp(t *testing.T, r *bytes.Buffer) *pluginpb.DiscoverResponse {
	t.Helper()
	var resp pluginpb.DiscoverResponse
	if err := ReadMessage(r, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &resp
}
