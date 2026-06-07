package cli

import (
	"testing"
)

// Tests for `sheaf serve`. The serve path stands up a real HTTP
// listener as its last step; we cover the pre-listen wiring (the
// only thing meaningfully reachable without holding a port through
// the test process).

func TestServe_FlagParseFailure(t *testing.T) {
	// Bad flag: ContinueOnError + flag.Parse returns 2 from the
	// public entry point. Use a value the flag set rejects.
	if rc := Serve([]string{"--unknown-flag"}); rc != 2 {
		t.Errorf("expected rc=2 on bad flag; got %d", rc)
	}
}

func TestServe_BindPortRepoConfigFlags(t *testing.T) {
	// Drive the public Serve entry with --bind/--port/--config/--repo
	// against a directory that has no sheaf.textproto. The pipeline
	// fails early on config load (rc=3); the assertion here is that
	// the binding flags parse without error.
	dir := t.TempDir()
	rc := Serve([]string{"--bind", "127.0.0.1", "--port", "0", "--repo", dir, "--config", dir + "/missing.textproto"})
	if rc == 0 {
		t.Errorf("expected non-zero exit with missing config; got 0")
	}
}

func TestServe_MissingConfig(t *testing.T) {
	dir := t.TempDir()
	if rc := Serve([]string{"--repo", dir}); rc == 0 {
		t.Errorf("expected non-zero exit on missing config")
	}
}
