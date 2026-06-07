package pytest

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

// setupRepo writes the given files into a temp dir and returns the
// dir's path. Used as the repoRoot for Discover() in every fixture
// test below.
func setupRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return dir
}

func discover(t *testing.T, files map[string]string, cfg Config) []testCase {
	t.Helper()
	repo := setupRepo(t, files)
	tcs, err := New(cfg).Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	out := make([]testCase, 0, len(tcs))
	for _, tc := range tcs {
		out = append(out, testCase{
			Name: tc.GetName(),
			Path: tc.GetLocation().GetPath(),
			Line: int(tc.GetLocation().GetLine()),
			Refs: append([]string(nil), tc.GetContractRefs()...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

type testCase struct {
	Name string
	Path string
	Line int
	Refs []string
}

func names(tcs []testCase) []string {
	out := make([]string, len(tcs))
	for i, tc := range tcs {
		out[i] = tc.Name
	}
	return out
}

func wantNames(t *testing.T, got []testCase, want []string) {
	t.Helper()
	gotNames := names(got)
	if !sliceEq(gotNames, want) {
		t.Errorf("test names = %v, want %v", gotNames, want)
	}
}

func wantRefContains(t *testing.T, refs []string, want string) {
	t.Helper()
	for _, r := range refs {
		if r == want {
			return
		}
	}
	t.Errorf("missing ref %q; got %v", want, refs)
}

func wantNoRef(t *testing.T, refs []string, banned string) {
	t.Helper()
	for _, r := range refs {
		if r == banned {
			t.Errorf("unexpected ref %q in %v", banned, refs)
		}
	}
}

// ---- 1. basic_module_test ----

func TestDiscover_BasicModuleTest(t *testing.T) {
	got := discover(t, map[string]string{
		"basic/test_basic.py": `
def test_foo():
    x = pw.log.LogEntry()
    return x
`,
	}, Config{})
	wantNames(t, got, []string{"test_foo"})
	if len(got) != 1 {
		t.Fatalf("got %d test cases", len(got))
	}
	wantRefContains(t, got[0].Refs, "pw.log.LogEntry")
	wantRefContains(t, got[0].Refs, "pw.log/LogEntry")
}

// ---- 2. class_test ----

func TestDiscover_ClassTest(t *testing.T) {
	got := discover(t, map[string]string{
		"cls/test_cls.py": `
class TestThing:
    def test_bar(self):
        pw.log.LogEntry()
`,
	}, Config{})
	wantNames(t, got, []string{"TestThing.test_bar"})
}

// ---- 3. nested_class_skipped ----

func TestDiscover_NestedClassSkipped(t *testing.T) {
	got := discover(t, map[string]string{
		"nested/test_nested.py": `
class TestOuter:
    class TestInner:
        def test_x(self):
            pw.log.LogEntry()

    def test_outer(self):
        pw.log.LogRequest()
`,
	}, Config{})
	// Only the outer-class test_* is discovered; the inner is skipped.
	wantNames(t, got, []string{"TestOuter.test_outer"})
}

// ---- 4. import_alias ----

func TestDiscover_ImportAlias(t *testing.T) {
	got := discover(t, map[string]string{
		"impa/test_impa.py": `
from pw_log import LogEntry

def test_x():
    LogEntry()
`,
	}, Config{ModuleAliases: []string{"pw_log=pw.log"}})
	if len(got) != 1 {
		t.Fatalf("got %d test cases", len(got))
	}
	wantRefContains(t, got[0].Refs, "pw.log.LogEntry")
	wantRefContains(t, got[0].Refs, "pw.log/LogEntry")
}

// ---- 5. import_as ----

func TestDiscover_ImportAs(t *testing.T) {
	got := discover(t, map[string]string{
		"impas/test_impas.py": `
import pw_log as L

def test_x():
    L.LogEntry()
`,
	}, Config{ModuleAliases: []string{"pw_log=pw.log"}})
	if len(got) != 1 {
		t.Fatalf("got %d test cases", len(got))
	}
	wantRefContains(t, got[0].Refs, "pw.log.LogEntry")
	wantRefContains(t, got[0].Refs, "pw.log/LogEntry")
}

// ---- 6. parametrize_decorator ----

func TestDiscover_ParametrizeDecorator(t *testing.T) {
	got := discover(t, map[string]string{
		"par/test_par.py": `
import pytest

@pytest.mark.parametrize("v", [1, 2, 3])
def test_x(v):
    return v
`,
	}, Config{})
	if len(got) != 1 {
		t.Fatalf("expected exactly one TestCase even with parametrize decorator; got %d", len(got))
	}
	wantNames(t, got, []string{"test_x"})
}

// ---- 7. string_literal_ref ----

func TestDiscover_StringLiteralRef(t *testing.T) {
	got := discover(t, map[string]string{
		"str/test_str.py": `
def test_x():
    client.call("pw.log.Logs/Listen")
`,
	}, Config{IDLPrefix: "pw"})
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	wantRefContains(t, got[0].Refs, "pw.log.Logs/Listen")
	// Parent (service) also emitted.
	wantRefContains(t, got[0].Refs, "pw.log.Logs")
}

// ---- 8. multi_line_import ----

func TestDiscover_MultiLineImport(t *testing.T) {
	got := discover(t, map[string]string{
		"mli/test_mli.py": `
from pw_log import (
    LogEntry,
    LogRequest,
)

def test_x():
    LogEntry()
    LogRequest()
`,
	}, Config{ModuleAliases: []string{"pw_log=pw.log"}})
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	wantRefContains(t, got[0].Refs, "pw.log.LogEntry")
	wantRefContains(t, got[0].Refs, "pw.log.LogRequest")
}

// ---- 9. non_test_function ----

func TestDiscover_NonTestFunctionIgnored(t *testing.T) {
	got := discover(t, map[string]string{
		"nt/test_nt.py": `
def helper():
    return pw.log.LogEntry()

def test_real():
    return helper()
`,
	}, Config{})
	wantNames(t, got, []string{"test_real"})
}

// ---- 10. test_in_non_test_class ----

func TestDiscover_TestInNonTestClassIgnored(t *testing.T) {
	got := discover(t, map[string]string{
		"nt/test_helper.py": `
class Helper:
    def test_x(self):
        pw.log.LogEntry()

def test_keep():
    pass
`,
	}, Config{})
	wantNames(t, got, []string{"test_keep"})
}

// ---- 11. empty_test_body ----

func TestDiscover_EmptyTestBody(t *testing.T) {
	got := discover(t, map[string]string{
		"empty/test_empty.py": `
def test_x():
    pass
`,
	}, Config{})
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if len(got[0].Refs) != 0 {
		t.Errorf("expected zero refs for empty body; got %v", got[0].Refs)
	}
}

// ---- supplementary: unittest.TestCase subclass (real-world pigweed shape) ----

func TestDiscover_UnittestTestCaseSubclass(t *testing.T) {
	got := discover(t, map[string]string{
		"ut/test_ut.py": `
import unittest

class ClientTest(unittest.TestCase):
    def test_send(self):
        pw.rpc.Client()

class _Base(unittest.TestCase):
    def helper(self):
        pass
`,
	}, Config{})
	// ClientTest is a unittest.TestCase subclass (and *Test suffix), so
	// its test_* method is discovered even though it doesn't start with
	// "Test". helper() in _Base is not a test_* method.
	wantNames(t, got, []string{"ClientTest.test_send"})
	wantRefContains(t, got[0].Refs, "pw.rpc.Client")
}

// ---- supplementary: refs don't leak across adjacent tests ----

func TestDiscover_RefsScopedToBody(t *testing.T) {
	got := discover(t, map[string]string{
		"sep/test_sep.py": `
def test_first():
    pw.log.LogEntry()

def test_second():
    pw.transfer.Reader()
`,
	}, Config{})
	if len(got) != 2 {
		t.Fatalf("got %d tests, want 2", len(got))
	}
	wantRefContains(t, got[0].Refs, "pw.log.LogEntry")
	wantNoRef(t, got[0].Refs, "pw.transfer.Reader")
	wantRefContains(t, got[1].Refs, "pw.transfer.Reader")
	wantNoRef(t, got[1].Refs, "pw.log.LogEntry")
}

// ---- supplementary: comments don't produce refs ----

func TestDiscover_CommentsStripped(t *testing.T) {
	got := discover(t, map[string]string{
		"c/test_c.py": `
def test_x():
    # pw.log.NotARef
    pass
`,
	}, Config{})
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	wantNoRef(t, got[0].Refs, "pw.log.NotARef")
}

// ---- supplementary: include/exclude globs honored ----

func TestDiscover_ExcludeHonored(t *testing.T) {
	got := discover(t, map[string]string{
		"keep/test_keep.py":            "def test_keep():\n    pass\n",
		"third_party/foo/test_skip.py": "def test_skip():\n    pass\n",
	}, Config{Exclude: []string{"**/third_party/**"}})
	wantNames(t, got, []string{"test_keep"})
}

// ---- helpers ----

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func TestTokenizePyTestName(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"test_foo", []string{"foo"}},
		{"test_foo_bar", []string{"foo", "bar"}},
		{"TestEchoService.test_echo", []string{"echo", "service", "echo"}},
	}
	for _, c := range cases {
		got := tokenizePyTestName(c.in)
		if !sliceEq(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsTestClass(t *testing.T) {
	cases := []struct {
		name, bases string
		want        bool
	}{
		{"TestThing", "", true},
		{"ClientTest", "unittest.TestCase", true},
		{"PacketsTests", "", true},
		{"MethodTest", "", true},
		{"_Base", "unittest.TestCase", true},
		{"Helper", "", false},
		{"Client", "object", false},
	}
	for _, c := range cases {
		if got := isTestClass(c.name, c.bases); got != c.want {
			t.Errorf("isTestClass(%q, %q) = %v, want %v", c.name, c.bases, got, c.want)
		}
	}
}

func TestStripLineComment(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`foo  # comment`, `foo  `},
		{`x = "# not a comment"`, `x = "# not a comment"`},
		{`x = '# not a comment' # but this is`, `x = '# not a comment' `},
		{`escaped \# stays`, `escaped \`},
	}
	for _, c := range cases {
		got := stripLineComment(c.in)
		if got != c.want {
			t.Errorf("strip(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseImports(t *testing.T) {
	src := `
import pw_log
import pw_log as L
from pw_log import LogEntry
from pw_log import LogRequest as LR
from pw_log import (
    Foo,
    Bar,
)
`
	imps := parseImports(strings.Split(src, "\n"))
	if got := imps.modulePrefix["L"]; got != "pw_log" {
		t.Errorf("modulePrefix[L] = %q", got)
	}
	if got := imps.modulePrefix["pw_log"]; got != "pw_log" {
		t.Errorf("modulePrefix[pw_log] = %q", got)
	}
	if got := imps.bareNames["LogEntry"]; got != "pw_log.LogEntry" {
		t.Errorf("bareNames[LogEntry] = %q", got)
	}
	if got := imps.bareNames["LR"]; got != "pw_log.LogRequest" {
		t.Errorf("bareNames[LR] = %q", got)
	}
	if got := imps.bareNames["Foo"]; got != "pw_log.Foo" {
		t.Errorf("bareNames[Foo] = %q", got)
	}
	if got := imps.bareNames["Bar"]; got != "pw_log.Bar" {
		t.Errorf("bareNames[Bar] = %q", got)
	}
}
