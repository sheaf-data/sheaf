package pwfacade

import (
	"context"
	"path/filepath"
	"testing"
)

// runWalk is the standard test driver: New + Walk against a testdata
// subdirectory, returning the resolved hints.
func runWalk(t *testing.T, dir string) *hints {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", dir))
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{})
	bh, err := r.Walk(context.Background(), root)
	if err != nil {
		t.Fatalf("Walk(%s): %v", dir, err)
	}
	h, ok := bh.(*hints)
	if !ok {
		t.Fatalf("Walk(%s): want *hints, got %T", dir, bh)
	}
	return h
}

func TestBasic(t *testing.T) {
	h := runWalk(t, "basic")

	// Every backend header should resolve to (pw_chrono, pw_chrono_stl).
	expectFacade(t, h, "pw_chrono_stl/public/pw_chrono_stl/system_clock_config.h", "pw_chrono", "pw_chrono_stl")
	expectFacade(t, h, "pw_chrono_stl/public/pw_chrono_stl/system_clock_inline.h", "pw_chrono", "pw_chrono_stl")
	expectFacade(t, h, "pw_chrono_stl/public/pw_chrono_stl/system_timer_inline.h", "pw_chrono", "pw_chrono_stl")

	// Facade-module headers are NOT backend-headers, so FacadeOf returns false.
	if f, b, ok := h.FacadeOf("pw_chrono/public/pw_chrono/system_clock.h"); ok {
		t.Errorf("facade-module header should not resolve as backend: got (%q, %q, true)", f, b)
	}

	// IsPublic always (false, false) in v1.
	if ok, known := h.IsPublic("pw_chrono/public/pw_chrono/system_clock.h"); ok || known {
		t.Errorf("IsPublic: want (false, false), got (%v, %v)", ok, known)
	}
}

func TestNoMatch(t *testing.T) {
	h := runWalk(t, "no_match")
	// No sibling module backs the facade, so every lookup returns false.
	for _, p := range []string{
		"pw_chrono/public/pw_chrono/system_clock.h",
		"pw_chrono_stl/public/pw_chrono_stl/system_clock_inline.h",
		"random.h",
	} {
		if f, b, ok := h.FacadeOf(p); ok {
			t.Errorf("FacadeOf(%q): want zero, got (%q, %q, true)", p, f, b)
		}
	}
}

func TestMultipleBackends(t *testing.T) {
	h := runWalk(t, "multiple_backends")

	cases := []struct {
		path    string
		backend string
	}{
		{"pw_chrono_stl/public/pw_chrono_stl/system_clock_inline.h", "pw_chrono_stl"},
		{"pw_chrono_freertos/public/pw_chrono_freertos/system_clock_inline.h", "pw_chrono_freertos"},
		{"pw_chrono_zephyr/public/pw_chrono_zephyr/system_clock_inline.h", "pw_chrono_zephyr"},
	}
	for _, c := range cases {
		expectFacade(t, h, c.path, "pw_chrono", c.backend)
	}
}

func TestMalformedBuildGN(t *testing.T) {
	// Walk must NOT return an error — malformed files are skipped with
	// a warning log. Recognizer continues with whatever it could parse.
	h := runWalk(t, "malformed_build_gn")
	// No backend module declared, so the (un-parseable) facade entry
	// shouldn't surface anything. The whole walk just no-ops.
	if f, b, ok := h.FacadeOf("pw_chrono/public/pw_chrono/system_clock.h"); ok {
		t.Errorf("FacadeOf after malformed parse: want zero, got (%q, %q, true)", f, b)
	}
}

func TestNonPigweedRepo(t *testing.T) {
	h := runWalk(t, "non_pigweed_repo")
	// No pw_facade declarations -> empty map. Recognizer is a no-op.
	if len(h.fileBacks) != 0 {
		t.Errorf("non-Pigweed repo: want empty fileBacks, got %d entries: %v", len(h.fileBacks), h.fileBacks)
	}
}

// TestBackendSelfNamedMatch covers the Pigweed convention where a
// backend module pw_X_Y provides a source-set named after itself
// (pw_X_Y), not after the facade — the case real pw_log backends use.
// Also exercises the module-level (FacadeModule) and symbol-level
// (FacadeSymbol) queries the facade post-pass relies on.
func TestBackendSelfNamedMatch(t *testing.T) {
	facades := map[string]map[string]bool{
		"pw_log": {"pw_log": true}, // pw_facade("pw_log") in module pw_log
	}
	sources := map[string]map[string][]string{
		"pw_log_basic": {
			"pw_log_basic": {"pw_log_basic/public/pw_log_basic/log_basic.h"},
		},
	}
	h := newHints(facades, sources)

	// The backend's self-named source-set header resolves to the facade.
	expectFacade(t, h, "pw_log_basic/public/pw_log_basic/log_basic.h", "pw_log", "pw_log_basic")

	// Module-level backing relation.
	if f, ok := h.FacadeModule("pw_log_basic"); !ok || f != "pw_log" {
		t.Errorf("FacadeModule(pw_log_basic): want (pw_log, true), got (%q, %v)", f, ok)
	}
	if _, ok := h.FacadeModule("pw_log"); ok {
		t.Errorf("FacadeModule(pw_log): a facade module is not a backend; want false")
	}

	// Symbol convention: PW_HANDLE_<X> -> PW_<X> for facade pw_<x>.
	if name, ok := h.FacadeSymbol("pw_log_basic", "PW_HANDLE_LOG"); !ok || name != "PW_LOG" {
		t.Errorf("FacadeSymbol(pw_log_basic, PW_HANDLE_LOG): want (PW_LOG, true), got (%q, %v)", name, ok)
	}
	// A non-handler name has no opinion (same-name fallback applies).
	if name, ok := h.FacadeSymbol("pw_log_basic", "pw_Log"); ok {
		t.Errorf("FacadeSymbol(pw_log_basic, pw_Log): want no opinion, got (%q, true)", name)
	}
	// An unrecognized backend module yields no opinion.
	if _, ok := h.FacadeSymbol("pw_unknown", "PW_HANDLE_LOG"); ok {
		t.Errorf("FacadeSymbol(pw_unknown, ...): want false for non-backend module")
	}
}

// --- helpers ---

func expectFacade(t *testing.T, h *hints, relPath, wantFacade, wantBackend string) {
	t.Helper()
	f, b, ok := h.FacadeOf(relPath)
	if !ok {
		t.Errorf("FacadeOf(%q): want (%q, %q, true), got (_, _, false)", relPath, wantFacade, wantBackend)
		return
	}
	if f != wantFacade || b != wantBackend {
		t.Errorf("FacadeOf(%q): want (%q, %q), got (%q, %q)", relPath, wantFacade, wantBackend, f, b)
	}
}

// --- unit-level parser tests ---

func TestParseBuildGN_Inline(t *testing.T) {
	body := `pw_facade("system_clock") {
  backend = pw_chrono_SYSTEM_CLOCK_BACKEND
  public = [
    "public/pw_chrono/system_clock.h",
  ]
}

pw_source_set("system_clock") {
  public = [ "public/pw_chrono_stl/system_clock_inline.h" ]
}
`
	decls, err := parseBuildGN(body)
	if err != nil {
		t.Fatalf("parseBuildGN: %v", err)
	}
	if len(decls) != 2 {
		t.Fatalf("want 2 decls, got %d", len(decls))
	}
	if decls[0].kind != "pw_facade" || decls[0].name != "system_clock" {
		t.Errorf("decls[0] = %+v", decls[0])
	}
	if got := decls[0].public; len(got) != 1 || got[0] != "public/pw_chrono/system_clock.h" {
		t.Errorf("decls[0].public = %v", got)
	}
	if decls[1].kind != "pw_source_set" || decls[1].name != "system_clock" {
		t.Errorf("decls[1] = %+v", decls[1])
	}
}

func TestParseBuildGN_NestedBraces(t *testing.T) {
	// Make sure braces inside the target block (e.g. from an if/else)
	// don't fool the depth counter.
	body := `pw_source_set("x") {
  if (foo) {
    sources = [ "a.cc" ]
  } else {
    sources = [ "b.cc" ]
  }
  public = [ "x.h" ]
}
`
	decls, err := parseBuildGN(body)
	if err != nil {
		t.Fatalf("parseBuildGN: %v", err)
	}
	if len(decls) != 1 || decls[0].name != "x" {
		t.Fatalf("want one decl named x, got %+v", decls)
	}
	if got := decls[0].public; len(got) != 1 || got[0] != "x.h" {
		t.Errorf("public = %v", got)
	}
}

func TestParseBuildGN_Unterminated(t *testing.T) {
	body := `pw_facade("system_clock") {
  public = [ "x.h" ]
`
	_, err := parseBuildGN(body)
	if err == nil {
		t.Fatalf("want error on unterminated block")
	}
}

func TestModuleFromBuildGN(t *testing.T) {
	cases := map[string]string{
		"pw_chrono/BUILD.gn":     "pw_chrono",
		"pw_chrono_stl/BUILD.gn": "pw_chrono_stl",
		"x/y/pw_chrono/BUILD.gn": "pw_chrono",
		"BUILD.gn":               "",
	}
	for in, want := range cases {
		if got := moduleFromBuildGN(in); got != want {
			t.Errorf("moduleFromBuildGN(%q): want %q, got %q", in, want, got)
		}
	}
}
