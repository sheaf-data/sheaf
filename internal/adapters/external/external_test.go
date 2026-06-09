package external

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

// Built once in TestMain and shared across tests.
var (
	fakePluginBin   string
	gotestPluginBin string
)

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("go"); err != nil {
		// No toolchain to build the plugin binaries against; these tests
		// can't run. Skip the whole package cleanly.
		os.Stderr.WriteString("external: `go` not on PATH; skipping subprocess tests\n")
		os.Exit(0)
	}
	tmp, err := os.MkdirTemp("", "external-plugins-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	fakePluginBin = filepath.Join(tmp, "fakeplugin")
	gotestPluginBin = filepath.Join(tmp, "sheaf-adapter-gotest")
	if err := goBuild(fakePluginBin, "github.com/sheaf-data/sheaf/internal/adapters/external/testdata/fakeplugin"); err != nil {
		panic("build fakeplugin: " + err.Error())
	}
	if err := goBuild(gotestPluginBin, "github.com/sheaf-data/sheaf/cmd/sheaf-adapter-gotest"); err != nil {
		panic("build gotest plugin: " + err.Error())
	}
	os.Exit(m.Run())
}

func goBuild(out, importPath string) error {
	cmd := exec.Command("go", "build", "-o", out, importPath)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// fakeTP builds a testParser backed by the fake plugin in the given mode.
func fakeTP(t *testing.T, opt map[string]string, timeout time.Duration) adapters.TestParser {
	t.Helper()
	tp, err := NewTestParser(Config{
		Command: fakePluginBin,
		Option:  opt,
		Timeout: timeout,
		Name:    "fake",
	})
	if err != nil {
		t.Fatalf("NewTestParser: %v", err)
	}
	return tp
}

func TestExternal_HappyPath(t *testing.T) {
	tp := fakeTP(t, map[string]string{"mode": "ok", "id": "mytest"}, 0)
	got, err := tp.Discover(context.Background(), t.TempDir(), adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0].GetId() != "mytest" {
		t.Fatalf("tests = %v, want one with id mytest", got)
	}
	if tp.Name() != "fake" {
		t.Errorf("Name() = %q, want fake", tp.Name())
	}
}

func TestExternal_PluginErrorField(t *testing.T) {
	tp := fakeTP(t, map[string]string{"mode": "error", "msg": "cannot parse"}, 0)
	_, err := tp.Discover(context.Background(), t.TempDir(), adapters.ScopeConfig{})
	if err == nil || !strings.Contains(err.Error(), "cannot parse") {
		t.Fatalf("err = %v, want it to carry the plugin's error", err)
	}
}

func TestExternal_NonZeroExit(t *testing.T) {
	tp := fakeTP(t, map[string]string{"mode": "exit", "code": "5"}, 0)
	_, err := tp.Discover(context.Background(), t.TempDir(), adapters.ScopeConfig{})
	if err == nil || !strings.Contains(err.Error(), "fake") {
		t.Fatalf("err = %v, want a non-zero-exit failure", err)
	}
}

func TestExternal_ErrorResponseThenNonZeroExit(t *testing.T) {
	// The plugin writes a clean error response and THEN exits non-zero;
	// the host should surface the response's message, not the exit code.
	tp := fakeTP(t, map[string]string{"mode": "exitafter"}, 0)
	_, err := tp.Discover(context.Background(), t.TempDir(), adapters.ScopeConfig{})
	if err == nil || !strings.Contains(err.Error(), "boom-then-exit") {
		t.Fatalf("err = %v, want the response error preferred over exit code", err)
	}
}

func TestExternal_GarbageOutput(t *testing.T) {
	tp := fakeTP(t, map[string]string{"mode": "garbage"}, 0)
	_, err := tp.Discover(context.Background(), t.TempDir(), adapters.ScopeConfig{})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("err = %v, want a decode failure", err)
	}
}

func TestExternal_ProtocolMismatch(t *testing.T) {
	tp := fakeTP(t, map[string]string{"mode": "badversion"}, 0)
	_, err := tp.Discover(context.Background(), t.TempDir(), adapters.ScopeConfig{})
	if err == nil || !strings.Contains(err.Error(), "protocol version mismatch") {
		t.Fatalf("err = %v, want a protocol-version mismatch", err)
	}
}

func TestExternal_Timeout(t *testing.T) {
	tp := fakeTP(t, map[string]string{"mode": "sleep", "sleep_ms": "10000"}, 200*time.Millisecond)
	start := time.Now()
	_, err := tp.Discover(context.Background(), t.TempDir(), adapters.ScopeConfig{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Discover took %s; timeout was not enforced", elapsed)
	}
}

func TestExternal_ContextCancel(t *testing.T) {
	tp := fakeTP(t, map[string]string{"mode": "sleep", "sleep_ms": "10000"}, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := tp.Discover(ctx, t.TempDir(), adapters.ScopeConfig{})
	if err == nil {
		t.Fatal("want an error after context cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Discover took %s; context cancellation was not honored", elapsed)
	}
}

func TestExternal_MissingCommand(t *testing.T) {
	tp, err := NewTestParser(Config{Command: filepath.Join(t.TempDir(), "does-not-exist"), Name: "ghost"})
	if err != nil {
		t.Fatalf("NewTestParser should not fail eagerly: %v", err)
	}
	_, err = tp.Discover(context.Background(), t.TempDir(), adapters.ScopeConfig{})
	if err == nil {
		t.Fatal("want an error spawning a missing command")
	}
}

func TestExternal_EmptyCommandRejected(t *testing.T) {
	if _, err := NewTestParser(Config{Command: "  "}); err == nil {
		t.Fatal("want an error for an empty command")
	}
}

func TestExternal_VersionProbe(t *testing.T) {
	tp := fakeTP(t, map[string]string{"mode": "ok"}, 0)
	if v := tp.Version(); v != "0.0.1" {
		t.Fatalf("Version() = %q, want 0.0.1 from the info probe", v)
	}
}

func TestExternal_DefaultNameIsBasename(t *testing.T) {
	tp, err := NewTestParser(Config{Command: fakePluginBin}) // no Name set
	if err != nil {
		t.Fatalf("NewTestParser: %v", err)
	}
	if got := tp.Name(); got != "fakeplugin" {
		t.Fatalf("Name() = %q, want the command basename fakeplugin", got)
	}
}
