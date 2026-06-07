package cppheader

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// runFixture copies one testdata/*.h into a temp repo (under
// public/<lib>/) so libraryFromPath produces a predictable library
// slug, then runs Discover with the supplied config.
func runFixture(t *testing.T, name string, cfg Config) []*contractpb.ContractElement {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name+".h"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	repo := t.TempDir()
	rel := filepath.Join("testlib", "public", "testlib", name+".h")
	full := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a := New(cfg, nil)
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return elems
}

type wantElem struct {
	id      string
	kind    contractpb.ContractElementKind
	docHas  string // optional doc-excerpt substring
	aliases []string
}

func assertElems(t *testing.T, got []*contractpb.ContractElement, want []wantElem) {
	t.Helper()
	gotIDs := elemIDs(got)
	sort.Strings(gotIDs)
	for _, w := range want {
		found := findElem(got, w.id)
		if found == nil {
			t.Errorf("missing element %q. got: %v", w.id, gotIDs)
			continue
		}
		if found.GetKind() != w.kind {
			t.Errorf("element %q kind = %v, want %v", w.id, found.GetKind(), w.kind)
		}
		if w.docHas != "" && !strings.Contains(found.GetDocCommentExcerpt(), w.docHas) {
			t.Errorf("element %q doc = %q, want substr %q", w.id, found.GetDocCommentExcerpt(), w.docHas)
		}
		if found.GetEcosystem() != Name {
			t.Errorf("element %q ecosystem = %q, want %q", w.id, found.GetEcosystem(), Name)
		}
		for _, a := range w.aliases {
			if !containsStr(found.GetAliases(), a) {
				t.Errorf("element %q missing alias %q, have %v", w.id, a, found.GetAliases())
			}
		}
	}
}

func assertNotPresent(t *testing.T, got []*contractpb.ContractElement, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if findElem(got, id) != nil {
			t.Errorf("unexpected element %q present", id)
		}
	}
}

func TestFixture_Basic(t *testing.T) {
	elems := runFixture(t, "basic", Config{})
	assertElems(t, elems, []wantElem{
		{id: "testlib/Server", kind: contractpb.ContractElementKind_CPP_CLASS, docHas: "Server runs"},
		{id: "testlib/Server::Server", kind: contractpb.ContractElementKind_METHOD, docHas: "Construct"},
		{id: "testlib/Server::Start", kind: contractpb.ContractElementKind_METHOD, docHas: "begins serving"},
		{id: "testlib/Server::Stop", kind: contractpb.ContractElementKind_METHOD, docHas: "halts the loop"},
	})
	if len(elems) != 4 {
		t.Errorf("len(elems) = %d, want 4: %v", len(elems), elemIDs(elems))
	}
}

func TestFixture_Namespaced(t *testing.T) {
	elems := runFixture(t, "namespaced", Config{})
	assertElems(t, elems, []wantElem{
		{
			id:      "testlib/foo::bar::Baz",
			kind:    contractpb.ContractElementKind_CPP_CLASS,
			docHas:  "Baz lives",
			aliases: []string{"Baz", "foo.bar.Baz"},
		},
		{
			id:      "testlib/foo::bar::Baz::Quux",
			kind:    contractpb.ContractElementKind_METHOD,
			docHas:  "Quux does",
			aliases: []string{"Quux", "foo.bar.Baz.Quux"},
		},
	})
}

func TestFixture_FreeFunctions(t *testing.T) {
	elems := runFixture(t, "free_functions", Config{})
	assertElems(t, elems, []wantElem{
		{id: "testlib/plain_int", kind: contractpb.ContractElementKind_CPP_FREE_FUNCTION, docHas: "Plain int"},
		{id: "testlib/return_ptr", kind: contractpb.ContractElementKind_CPP_FREE_FUNCTION},
		{id: "testlib/return_ref", kind: contractpb.ContractElementKind_CPP_FREE_FUNCTION},
		{id: "testlib/return_cref", kind: contractpb.ContractElementKind_CPP_FREE_FUNCTION},
		{id: "testlib/return_vec", kind: contractpb.ContractElementKind_CPP_FREE_FUNCTION},
	})
}

func TestFixture_Macros_Gated(t *testing.T) {
	// Default: emit_macros false. No CPP_MACRO elements.
	off := runFixture(t, "macros", Config{})
	for _, e := range off {
		if e.GetKind() == contractpb.ContractElementKind_CPP_MACRO {
			t.Errorf("unexpected CPP_MACRO with emit_macros=false: %v", e.GetId())
		}
	}

	// With emit_macros on, both #defines emit; include guard is skipped.
	on := runFixture(t, "macros", Config{EmitMacros: true})
	assertElems(t, on, []wantElem{
		{id: "testlib/PW_LOG_DEBUG", kind: contractpb.ContractElementKind_CPP_MACRO, docHas: "DEBUG"},
		{id: "testlib/PW_LOG_LEVEL_INFO", kind: contractpb.ContractElementKind_CPP_MACRO, docHas: "Bare define"},
	})
	assertNotPresent(t, on, "testlib/TEST_MACROS_H_")
}

func TestFixture_PrivateMethods(t *testing.T) {
	elems := runFixture(t, "private_methods", Config{})
	assertElems(t, elems, []wantElem{
		{id: "testlib/Cache", kind: contractpb.ContractElementKind_CPP_CLASS},
		{id: "testlib/Cache::Get", kind: contractpb.ContractElementKind_METHOD},
		{id: "testlib/Cache::Put", kind: contractpb.ContractElementKind_METHOD},
	})
	assertNotPresent(t, elems,
		"testlib/Cache::rehash_",
		"testlib/Cache::on_resize_",
	)
}

func TestFixture_Enums(t *testing.T) {
	elems := runFixture(t, "enums", Config{})
	assertElems(t, elems, []wantElem{
		{id: "testlib/Color", kind: contractpb.ContractElementKind_TYPE, docHas: "Plain enum"},
		{id: "testlib/Status", kind: contractpb.ContractElementKind_TYPE, docHas: "Scoped enum"},
	})
	// Enum values are NOT emitted as separate elements.
	assertNotPresent(t, elems, "testlib/RED", "testlib/Color::RED", "testlib/Status::kOk")
}

func TestFixture_Templates(t *testing.T) {
	elems := runFixture(t, "templates", Config{})
	// Primary template emitted; explicit specialization skipped.
	assertElems(t, elems, []wantElem{
		{id: "testlib/Box", kind: contractpb.ContractElementKind_CPP_CLASS, docHas: "Primary template"},
		{id: "testlib/Box::Get", kind: contractpb.ContractElementKind_METHOD},
	})
	// The explicit specialization Box<int> should not double-emit.
	if findCount(elems, "testlib/Box") != 1 {
		t.Errorf("Box emitted %d times, want 1", findCount(elems, "testlib/Box"))
	}
}

func TestFixture_DocComments(t *testing.T) {
	elems := runFixture(t, "doc_comments", Config{})
	triple := findElem(elems, "testlib/WithTripleSlash")
	if triple == nil || !strings.Contains(triple.GetDocCommentExcerpt(), "Triple-slash doc on a class") {
		t.Errorf("WithTripleSlash doc = %q", excerpt(triple))
	}
	javadoc := findElem(elems, "testlib/WithJavadoc")
	if javadoc == nil || !strings.Contains(javadoc.GetDocCommentExcerpt(), "Javadoc doc on a class") {
		t.Errorf("WithJavadoc doc = %q", excerpt(javadoc))
	}
	// NoDoc precedes a plain // comment, which is not a doc-style.
	noDoc := findElem(elems, "testlib/NoDoc")
	if noDoc == nil {
		t.Fatalf("NoDoc missing")
	}
	if noDoc.GetDocCommentExcerpt() != "" {
		t.Errorf("NoDoc unexpectedly has doc: %q", noDoc.GetDocCommentExcerpt())
	}
}

func TestFixture_AttributeMacros(t *testing.T) {
	cfg := Config{IgnoredAttributeMacros: []string{"ABSL_DEPRECATED", "PW_NO_LINT"}}
	elems := runFixture(t, "attribute_macros", cfg)
	assertElems(t, elems, []wantElem{
		{id: "testlib/OldThing", kind: contractpb.ContractElementKind_CPP_FREE_FUNCTION, docHas: "Free function"},
		{id: "testlib/Widget", kind: contractpb.ContractElementKind_CPP_CLASS, docHas: "Class preceded"},
		{id: "testlib/Widget::OldMethod", kind: contractpb.ContractElementKind_METHOD, docHas: "Method preceded"},
	})
}

func TestFixture_AnonNamespace(t *testing.T) {
	elems := runFixture(t, "anon_namespace", Config{})
	if len(elems) != 0 {
		t.Errorf("expected zero elements from anon namespace, got: %v", elemIDs(elems))
	}
}

func TestFixture_Empty_NoError(t *testing.T) {
	elems := runFixture(t, "empty", Config{})
	if len(elems) != 0 {
		t.Errorf("expected zero elements from empty header, got: %v", elemIDs(elems))
	}
}

func TestLibraryFromPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"pw_log/public/pw_log/log.h", "pw_log"},
		{"include/llvm/IR/Module.h", "llvm"},
		{"foo/bar/baz.h", "foo"},
		{"top.h", "cpp"},
	}
	for _, c := range cases {
		got := libraryFromPath(c.in)
		if got != c.want {
			t.Errorf("libraryFromPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildHints_PrivatePathSkipped(t *testing.T) {
	repo := t.TempDir()
	rel := filepath.Join("lib", "public", "lib", "x.h")
	full := filepath.Join(repo, rel)
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	_ = os.WriteFile(full, []byte("class Hidden { public: void M(); };\n"), 0o644)
	a := New(Config{}, stubHints{known: true, public: false})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 0 {
		t.Errorf("expected hint-skipped file to yield 0 elements, got %v", elemIDs(elems))
	}
}

type stubHints struct{ known, public bool }

func (s stubHints) IsPublic(string) (bool, bool)               { return s.public, s.known }
func (s stubHints) FacadeOf(string) (string, string, bool)     { return "", "", false }
func (s stubHints) FacadeModule(string) (string, bool)         { return "", false }
func (s stubHints) FacadeSymbol(string, string) (string, bool) { return "", false }

// ----------------------------------------------------------------
// helpers
// ----------------------------------------------------------------

func elemIDs(elems []*contractpb.ContractElement) []string {
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		out = append(out, e.GetId())
	}
	return out
}

func findElem(elems []*contractpb.ContractElement, id string) *contractpb.ContractElement {
	for _, e := range elems {
		if e.GetId() == id {
			return e
		}
	}
	return nil
}

func findCount(elems []*contractpb.ContractElement, id string) int {
	n := 0
	for _, e := range elems {
		if e.GetId() == id {
			n++
		}
	}
	return n
}

func containsStr(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func excerpt(e *contractpb.ContractElement) string {
	if e == nil {
		return "<nil>"
	}
	return e.GetDocCommentExcerpt()
}
