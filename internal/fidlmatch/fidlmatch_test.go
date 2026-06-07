package fidlmatch

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestExtractCPP_Qualified(t *testing.T) {
	body := `
		auto result = fuchsia::ui::scenic::Scenic::TakeScreenshot(handle);
		fuchsia::io::Directory::Open(...);
		some_other::Class::method();  // not fuchsia, ignored
	`
	got := Extract(body, "cpp", nil)
	want := []string{
		"fuchsia.io/Directory.Open",
		"fuchsia.ui.scenic/Scenic.TakeScreenshot",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestExtractCPP_WireCall(t *testing.T) {
	body := `
		auto resp = fidl::WireCall<fuchsia_io::Directory>(handle)->Open(flags, path);
	`
	got := Extract(body, "cpp", nil)
	want := []string{"fuchsia.io/Directory.Open"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestCPPIncludeLibraries(t *testing.T) {
	body := `
		#include "fuchsia/ui/scenic/cpp/fidl.h"
		#include <fuchsia/io/cpp/fidl.h>
		#include "fidl/fuchsia.net.filter/cpp/wire.h"
		#include "src/foo/bar.h"
	`
	got := CPPIncludeLibraries(body)
	for _, want := range []string{"fuchsia.ui.scenic", "fuchsia.io", "fuchsia.net.filter"} {
		if !got[want] {
			t.Errorf("missing library %s in %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected 3 libraries; got %d (%v)", len(got), got)
	}
}

func TestExtractCPP_Unqualified(t *testing.T) {
	body := `
		#include "fuchsia/ui/scenic/cpp/fidl.h"
		TEST(SessionTest, EnqueuesAndPresents) {
			session_->Enqueue(commands);
			session_->Present(0, callback);
			scenic_.GetDisplayInfo(&info);
		}
	`
	scope := CPPIncludeLibraries(body)
	got := Extract(body, "cpp", scope)
	// Should include the unqualified hits resolved into fuchsia.ui.scenic.
	wantContains := []string{
		"fuchsia.ui.scenic/Session.Enqueue",
		"fuchsia.ui.scenic/Session.Present",
		"fuchsia.ui.scenic/Scenic.GetDisplayInfo",
	}
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	for _, w := range wantContains {
		if !gotSet[w] {
			t.Errorf("missing %s in %v", w, got)
		}
	}
}

// Regression: multi-word C++ variable names (Fuchsia member-var
// convention is snake_case + trailing _) must convert to CamelCase
// so they resolve to FIDL protocol names like CpuResource.
func TestExtractCPP_UnqualifiedSnakeCaseVar(t *testing.T) {
	body := `
		#include "fuchsia/kernel/cpp/fidl.h"
		TEST(KernelTest, GetsCpuResource) {
			cpu_resource_->Get();
			info_resource_->Get();
		}
	`
	scope := CPPIncludeLibraries(body)
	got := Extract(body, "cpp", scope)
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	for _, want := range []string{
		"fuchsia.kernel/CpuResource.Get",
		"fuchsia.kernel/InfoResource.Get",
	} {
		if !gotSet[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}

func TestExtractRust_Qualified(t *testing.T) {
	body := `
		let resp = fidl_fuchsia_ui_scenic::ScenicProxy::take_screenshot(&proxy, ()).await;
		fuchsia_scenic::Session::enqueue(&session, commands);
		fidl_fuchsia_io::DirectoryProxy::open(&dir, flags, path);
	`
	got := Extract(body, "rust", nil)
	want := []string{
		"fuchsia.io/Directory.Open",
		"fuchsia.scenic/Session.Enqueue",
		"fuchsia.ui.scenic/Scenic.TakeScreenshot",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestRustUseLibraries(t *testing.T) {
	body := `
		use fidl_fuchsia_io::*;
		use fidl_fuchsia_ui_scenic as scenic;
		use fuchsia_scenic::Session;
	`
	got := RustUseLibraries(body)
	for _, want := range []string{"fuchsia.io", "fuchsia.ui.scenic", "fuchsia.scenic"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}

func TestExtractRust_Unqualified(t *testing.T) {
	body := `
		use fidl_fuchsia_ui_scenic::*;
		async fn test_present(session: SessionProxy) {
			session.present(0).await.unwrap();
			session.take_screenshot().await;
		}
	`
	scope := RustUseLibraries(body)
	got := Extract(body, "rust", scope)
	wantContains := []string{
		"fuchsia.ui.scenic/Session.Present",
		"fuchsia.ui.scenic/Session.TakeScreenshot",
	}
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	for _, w := range wantContains {
		if !gotSet[w] {
			t.Errorf("missing %s in %v", w, got)
		}
	}
}

func TestExtractRust_StdlibFiltered(t *testing.T) {
	body := `
		use fidl_fuchsia_io::*;
		let x = something.unwrap();
		let y = something.clone();
	`
	scope := RustUseLibraries(body)
	got := Extract(body, "rust", scope)
	for _, g := range got {
		if g == "fuchsia.io/Something.Unwrap" || g == "fuchsia.io/Something.Clone" {
			t.Errorf("stdlib method %s should have been filtered; got %v", g, got)
		}
	}
}

// Verifies the Matcher works against a non-fuchsia IDL prefix.
// Construct with Config{IDLPrefix:"acme"} and the regexes should
// match acme.* instead of fuchsia.*.
func TestMatcher_CustomPrefix(t *testing.T) {
	m := NewMatcher(Config{IDLPrefix: "acme"})

	// C++ qualified call on acme namespace.
	cpp := `
		#include "acme/widgets/cpp/fidl.h"
		TEST(Foo, Bar) {
			acme::widgets::Widget::Spin(seed);
			widget_->Halt();
		}
	`
	scope := m.CPPIncludeLibraries(cpp)
	if !scope["acme.widgets"] {
		t.Errorf("CPPIncludeLibraries: missing acme.widgets in %v", scope)
	}
	if scope["fuchsia.widgets"] {
		t.Errorf("CPPIncludeLibraries: unexpectedly matched fuchsia.widgets in %v", scope)
	}
	got := m.Extract(cpp, "cpp", scope)
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	if !gotSet["acme.widgets/Widget.Spin"] {
		t.Errorf("Extract: missing acme.widgets/Widget.Spin in %v", got)
	}
	if !gotSet["acme.widgets/Widget.Halt"] {
		t.Errorf("Extract: missing acme.widgets/Widget.Halt in %v", got)
	}

	// Negative — the default matcher (fuchsia prefix) should not pick
	// up the acme calls.
	if defaultGot := Extract(cpp, "cpp", CPPIncludeLibraries(cpp)); len(defaultGot) > 0 {
		t.Errorf("default fuchsia matcher leaked into acme namespace: %v", defaultGot)
	}

	// Rust check.
	rust := `
		use fidl_acme_widgets::*;
		fn t() { widget.spin(0); }
	`
	rscope := m.RustUseLibraries(rust)
	if !rscope["acme.widgets"] {
		t.Errorf("RustUseLibraries: missing acme.widgets in %v", rscope)
	}
}

// Verifies fix #3: when a C++ test uses a semantic variable name
// (parent_session_, child_view_) that doesn't equal a real protocol
// name, fidlmatch emits a wildcard "lib/*.Method" candidate alongside
// the specific one, so the indexer can fan out to any matching
// protocol in the library.
func TestExtractCPP_WildcardForSemanticVars(t *testing.T) {
	body := `
		#include "fuchsia/ui/composition/cpp/fidl.h"
		TEST(Touch, HitRegion) {
			parent_session_->SetInfiniteHitRegion(args);
		}
	`
	scope := CPPIncludeLibraries(body)
	got := Extract(body, "cpp", scope)
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	// Specific candidate (won't resolve — no ParentSession protocol)
	if !gotSet["fuchsia.ui.composition/ParentSession.SetInfiniteHitRegion"] {
		t.Errorf("missing specific candidate; got %v", got)
	}
	// Wildcard candidate (the fix)
	if !gotSet["fuchsia.ui.composition/*.SetInfiniteHitRegion"] {
		t.Errorf("missing wildcard candidate; got %v", got)
	}
}

// Verifies the wildcard guard: single-word methods don't fan out.
// Critical because `var->Get()` is common across many protocols, and
// indiscriminate fan-out would over-attribute (e.g. fuchsia.kernel
// has every Resource.Get sharing the name).
func TestExtractCPP_NoWildcardForSingleWordMethod(t *testing.T) {
	body := `
		#include "fuchsia/kernel/cpp/fidl.h"
		TEST(K, X) {
			cpu_resource_->Get();
		}
	`
	scope := CPPIncludeLibraries(body)
	got := Extract(body, "cpp", scope)
	for _, g := range got {
		if strings.Contains(g, "/*.") {
			t.Errorf("single-word method should NOT produce wildcard candidate; got %v", g)
		}
	}
}

func TestIsMethodDistinctive(t *testing.T) {
	cases := map[string]bool{
		"Get":                      false,
		"Set":                      false,
		"Open":                     false,
		"TakeScreenshot":           true,
		"SetInfiniteHitRegion":     true,
		"GetMemoryStats":           true,
		"RegisterBufferCollection": true,
	}
	for in, want := range cases {
		if got := isMethodDistinctive(in); got != want {
			t.Errorf("isMethodDistinctive(%q) = %v; want %v", in, got, want)
		}
	}
}

func TestSnakeToCamel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"take_screenshot", "TakeScreenshot"},
		{"open", "Open"},
		{"set_debug_name", "SetDebugName"},
		{"", ""},
	}
	for _, c := range cases {
		if got := snakeToCamel(c.in); got != c.want {
			t.Errorf("snakeToCamel(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
