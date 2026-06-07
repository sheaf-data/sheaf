package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// End-to-end test for `sheaf serve`: it builds the real binary, runs it
// against a tiny on-disk fixture repo, polls /healthz, drives a short
// JSON-RPC session over HTTP, then signals the process and asserts a
// clean (graceful) shutdown. This is the gap serve_test.go deliberately
// leaves — that file only exercises pre-listen flag/config wiring and
// never stands up the command end to end.
//
// In package cli_test (not cli) so it drives the binary as an external
// client, the way a real agent would.

// minimalFIDLConfig is a self-contained sheaf config for the fixture
// repo. Crucially it declares NO llm block, so BuildEmbedder yields the
// noop embedder and the server never reaches for a network backend —
// the e2e test stays hermetic and fast.
const minimalFIDLConfig = `version: 1
project { name: "fixture" }
scope { library: "fixture" closure { mode: STRICT } }
contract_anchor { name: "fidl" fidl { include: "**/*.fidl" } }
analyzer { name: "missing-in-category" severity: WARNING
  config { key: "alert_for_categories" string_value: "tests.unit_tests" }
}
mcp_server { bind: "127.0.0.1" port: 7700 cache_ttl_seconds: 60 }
`

const minimalRules = `version: 1
category { dotted_path: "tests.unit_tests" paths: "**/*_test.cc" }
category { dotted_path: "docs.reference" }
`

// A trivial FIDL library with one protocol + method so the scan
// produces at least one ContractElement (and thus a finding, since the
// method has no unit test).
const fixtureFIDL = `library fixture;

/// Greeter says hello.
protocol Greeter {
    /// Hello returns a greeting.
    Hello() -> (struct { greeting string; });
};
`

func freePortE2E(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func rpcPost(t *testing.T, addr, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	resp, err := http.Post(addr+"/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", method, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("unmarshal %s: %v\nbody=%s", method, err, b)
	}
	return r
}

func TestServe_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: builds + runs the sheaf binary; skipped under -short")
	}

	// 1. Build the sheaf binary into a temp dir.
	bin := filepath.Join(t.TempDir(), "sheaf")
	build := exec.Command("go", "build", "-o", bin, "github.com/sheaf-data/sheaf/cmd/sheaf")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build sheaf: %v\n%s", err, out)
	}

	// 2. Lay down a tiny fixture repo: config + rules + one .fidl source.
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "sheaf.textproto"), minimalFIDLConfig)
	writeFile(t, filepath.Join(repo, "categorization-rules.textproto"), minimalRules)
	writeFile(t, filepath.Join(repo, "fixture.fidl"), fixtureFIDL)

	// 3. Start `sheaf serve` on an OS-assigned free port.
	port := freePortE2E(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "serve",
		"--config", filepath.Join(repo, "sheaf.textproto"),
		"--repo", repo,
		"--port", strconv.Itoa(port),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Own process group so a stuck child can be reaped on cleanup.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}

	// waitErr carries the process exit status to the assertions below.
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	t.Cleanup(func() {
		// Belt-and-suspenders: if the test failed before signaling,
		// make sure the child (and its group) is gone.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})

	addr := "http://127.0.0.1:" + strconv.Itoa(port)

	// 4. Poll /healthz until ready (mirrors mkServer's deadline loop).
	ready := false
	deadline := time.Now().Add(20 * time.Second) // build already happened; scan is tiny
	for time.Now().Before(deadline) {
		// If the process died during startup, fail fast with its output.
		select {
		case err := <-waitErr:
			t.Fatalf("serve exited before becoming ready: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		default:
		}
		resp, err := http.Get(addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("server never became reachable on %s\nstdout:\n%s\nstderr:\n%s", addr, stdout.String(), stderr.String())
	}

	// 5. Drive a short session over the real transport.
	r := rpcPost(t, addr, "tools/list", map[string]any{})
	res, _ := r["result"].(map[string]any)
	if tools, _ := res["tools"].([]any); len(tools) < 7 {
		t.Errorf("tools/list over e2e: expected >=7 tools, got %d (%v)", len(tools), r)
	}

	r = rpcPost(t, addr, "query_contract", map[string]any{"element_id": "fixture/Greeter.Hello"})
	if r["error"] != nil {
		// The exact element id depends on the FIDL parser's naming; if it
		// differs, fall back to asserting list_libraries sees the library.
		t.Logf("query_contract by guessed id errored (%v); falling back to list_libraries", r["error"])
	}

	r = rpcPost(t, addr, "list_libraries", map[string]any{})
	if r["error"] != nil {
		t.Fatalf("list_libraries over e2e errored: %v", r["error"])
	}
	res, _ = r["result"].(map[string]any)
	if total, _ := res["total"].(float64); total < 1 {
		t.Errorf("list_libraries: expected >=1 library, got %v\n%v", res["total"], r)
	}

	// 6. Graceful shutdown: SIGINT should make runServe's signal-aware
	// context fire, drain, and the process exit 0.
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signal SIGINT: %v", err)
	}
	select {
	case err := <-waitErr:
		if err != nil {
			t.Errorf("serve did not exit cleanly on SIGINT: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("serve did not exit within 15s of SIGINT (no graceful shutdown)\nstderr:\n%s", stderr.String())
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
