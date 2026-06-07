package pythontest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/ffxinvoke"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// ffxScope is the CLI-shaped scope that turns the extractor on.
var ffxScope = adapters.ScopeConfig{Libraries: []string{"ffx"}}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// refsByName collects the union of ContractRefs across all TestCases whose
// Name matches, for order-independent assertions.
func refsFor(tcs []*testcasepb.TestCase, name string) []string {
	var out []string
	for _, tc := range tcs {
		if tc.GetName() == name {
			out = append(out, tc.GetContractRefs()...)
		}
	}
	sort.Strings(out)
	return out
}

func allRefs(tcs []*testcasepb.TestCase) []string {
	var out []string
	for _, tc := range tcs {
		out = append(out, tc.GetContractRefs()...)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func TestDiscover_LiteralListCommandAndFlags(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"e2e/affordances/session_using_ffx.py": `
class SessionUsingFfx:
    def start(self):
        self._ffx.run(["session", "start"], machine="raw")

    def take(self):
        self._ffx.run(cmd=["target", "screenshot", "--format", "png", "-d"])
`,
	})
	p := New(Config{Include: []string{"**/*.py"}})
	tcs, err := p.Discover(context.Background(), repo, ffxScope)
	if err != nil {
		t.Fatal(err)
	}

	startRefs := refsFor(tcs, "start")
	if !contains(startRefs, "ffx session start") || !contains(startRefs, "ffx session") {
		t.Errorf("start refs = %v, want command refs ffx session / ffx session start", startRefs)
	}

	takeRefs := refsFor(tcs, "take")
	// Command refs + the long flag --format (short -d is intentionally not
	// credited; climatch only emits long flags).
	for _, want := range []string{"ffx target", "ffx target screenshot", "ffx target screenshot --format"} {
		if !contains(takeRefs, want) {
			t.Errorf("take refs missing %q; got %v", want, takeRefs)
		}
	}
}

func TestDiscover_DottedReceiverInFunctionalTest(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"e2e/tests/test_wlan.py": `
class WlanTest:
    def test_iface_methods(self):
        driver_list = self.dut.ffx.run(["driver", "list"])
`,
	})
	p := New(Config{Include: []string{"**/*.py"}})
	tcs, err := p.Discover(context.Background(), repo, ffxScope)
	if err != nil {
		t.Fatal(err)
	}
	refs := refsFor(tcs, "test_iface_methods")
	want := []string{"ffx driver", "ffx driver list"}
	got := refs
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refs = %v, want %v", got, want)
	}
	// Name tokens should drop the "test" prefix marker.
	for _, tc := range tcs {
		if tc.GetName() == "test_iface_methods" {
			if contains(tc.GetNameTokens(), "test") {
				t.Errorf("NameTokens should not contain 'test': %v", tc.GetNameTokens())
			}
		}
	}
}

func TestDiscover_LocallyBuiltListWithExtend(t *testing.T) {
	// The dominant "built-up" idiom: cmd = [...]; cmd.extend([...]);
	// cmd.append("--x"); ffx.run(cmd). The literal flags must be credited.
	repo := writeRepo(t, map[string]string{
		"e2e/tracing_using_ffx.py": `
class TracingUsingFfx:
    def start(self):
        cmd = ["trace", "start", "--background"]
        cmd.extend(["--categories", self._categories])
        cmd.append("--nocompress")
        self._ffx.run(cmd)
`,
	})
	p := New(Config{Include: []string{"**/*.py"}})
	tcs, err := p.Discover(context.Background(), repo, ffxScope)
	if err != nil {
		t.Fatal(err)
	}
	refs := refsFor(tcs, "start")
	for _, want := range []string{
		"ffx trace", "ffx trace start",
		"ffx trace start --background",
		"ffx trace start --categories",
		"ffx trace start --nocompress",
	} {
		if !contains(refs, want) {
			t.Errorf("start refs missing %q; got %v", want, refs)
		}
	}
}

func TestDiscover_ConstantListAndConcat(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"e2e/screenshot_using_ffx.py": `
_FFX_SCREENSHOT_CMD = ["target", "screenshot", "--format", "png"]

class ScreenshotUsingFfx:
    def take(self, temp_dir):
        self._ffx.run(cmd=_FFX_SCREENSHOT_CMD + [temp_dir])
`,
	})
	p := New(Config{Include: []string{"**/*.py"}})
	tcs, err := p.Discover(context.Background(), repo, ffxScope)
	if err != nil {
		t.Fatal(err)
	}
	refs := refsFor(tcs, "take")
	for _, want := range []string{"ffx target", "ffx target screenshot", "ffx target screenshot --format"} {
		if !contains(refs, want) {
			t.Errorf("take refs missing %q; got %v", want, refs)
		}
	}
}

func TestDiscover_FfxTransportSelfRunWithDictConstant(t *testing.T) {
	// Inside the FFX transport (class FFX), bare self.run(cmd=_FFX_CMDS[...])
	// is an ffx invocation; the dict-constant subscript resolves.
	repo := writeRepo(t, map[string]string{
		"transports/ffx/ffx.py": `
_FFX_CMDS = {
    "TARGET_SHOW": ["target", "show"],
    "TARGET_WAIT": ["target", "wait", "--timeout", "0"],
}

class FFX:
    def get_target_information(self):
        cmd = _FFX_CMDS["TARGET_SHOW"]
        output = self.run(cmd=cmd)

    def wait(self):
        self.run(cmd=_FFX_CMDS["TARGET_WAIT"])
`,
	})
	p := New(Config{Include: []string{"**/*.py"}})
	tcs, err := p.Discover(context.Background(), repo, ffxScope)
	if err != nil {
		t.Fatal(err)
	}
	refs := allRefs(tcs)
	for _, want := range []string{
		"ffx target", "ffx target show",
		"ffx target wait", "ffx target wait --timeout",
	} {
		if !contains(refs, want) {
			t.Errorf("transport refs missing %q; got %v", want, refs)
		}
	}
}

func TestDiscover_ScopeGate_NoFfxNoOp(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"e2e/x.py": `
class C:
    def f(self):
        self._ffx.run(["target", "list"])
`,
	})
	p := New(Config{Include: []string{"**/*.py"}})
	// Non-ffx scope: the extractor must be a complete no-op.
	tcs, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{Libraries: []string{"kubectl"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tcs) != 0 {
		t.Errorf("expected no TestCases for non-ffx scope, got %d", len(tcs))
	}
}

func TestDiscover_NonFfxReceiverIgnored(t *testing.T) {
	// A same-named method on a non-ffx receiver must not be read as ffx.
	repo := writeRepo(t, map[string]string{
		"e2e/x.py": `
class C:
    def f(self):
        self.sl4f.run(["not", "ffx"])
        self.fastboot.run(["also", "not", "ffx"])
`,
	})
	p := New(Config{Include: []string{"**/*.py"}})
	tcs, err := p.Discover(context.Background(), repo, ffxScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(tcs) != 0 {
		t.Errorf("expected no TestCases (non-ffx receivers), got %d: %v", len(tcs), allRefs(tcs))
	}
}

func TestDiscover_CommentedOutInvocationSkipped(t *testing.T) {
	repo := writeRepo(t, map[string]string{
		"e2e/x.py": `
class C:
    def f(self):
        # self._ffx.run(["target", "list", "--commented"])
        self._ffx.run(["target", "show"])
`,
	})
	p := New(Config{Include: []string{"**/*.py"}})
	tcs, err := p.Discover(context.Background(), repo, ffxScope)
	if err != nil {
		t.Fatal(err)
	}
	refs := allRefs(tcs)
	if contains(refs, "ffx target list --commented") {
		t.Errorf("commented-out flag was extracted: %v", refs)
	}
	if !contains(refs, "ffx target show") {
		t.Errorf("expected ffx target show; got %v", refs)
	}
}

// ---- Pure-Go regex fallback (used when python3 is unavailable) ----

func TestRegexFallback_LiteralListAndDynamicSkip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.py")
	src := `
class C:
    def f(self):
        self._ffx.run(["session", "start"])
        self._ffx.run(cmd=["target", "list", "--no-probe"])
        self._ffx.run(cmd)                      # dynamic: bare var, must be skipped+counted
        self._ffx.run(["session", "add", url])  # literal subcommand + dynamic element
`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	invs, stats := extractRegex(p)

	// Collect the literal args of each extracted invocation.
	var firstTokens []string
	for _, inv := range invs {
		if len(inv.Args) > 0 {
			firstTokens = append(firstTokens, inv.Args[0]+"/"+inv.Args[len(inv.Args)-1])
		}
	}
	if len(invs) != 3 {
		t.Fatalf("expected 3 literal-bearing invocations, got %d (%v)", len(invs), firstTokens)
	}
	// The bare-var run(cmd) call had no inline literal list → counted dynamic.
	if stats.fullyDynamic != 1 {
		t.Errorf("fullyDynamic = %d, want 1 (the bare-var run)", stats.fullyDynamic)
	}
	// The "session add" call kept its literal subcommand but elided `url`.
	foundSessionAdd := false
	for _, inv := range invs {
		if reflect.DeepEqual(inv.Args, []string{"session", "add"}) {
			foundSessionAdd = true
			if !inv.Dynamic {
				t.Errorf("session add invocation should be marked dynamic (elided url)")
			}
		}
	}
	if !foundSessionAdd {
		t.Errorf("did not find the session add invocation; got %+v", invs)
	}
}

func TestRegexFallback_FlagExtraction(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.py")
	src := `self._ffx.run(["target", "list", "--no-usb", "--no-probe"])`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	invs, _ := extractRegex(p)
	if len(invs) != 1 {
		t.Fatalf("want 1 invocation, got %d", len(invs))
	}
	want := []string{"target", "list", "--no-usb", "--no-probe"}
	if !reflect.DeepEqual(invs[0].Args, want) {
		t.Errorf("args = %v, want %v", invs[0].Args, want)
	}
	// And the canonicalized refs (sanity that fallback feeds the same pipeline).
	cmds, flags, _ := ffxinvoke.Canonicalize(invs[0].Args)
	if !contains(cmds, "ffx target list") {
		t.Errorf("cmd refs missing ffx target list: %v", cmds)
	}
	if !contains(flags, "ffx target list --no-usb") || !contains(flags, "ffx target list --no-probe") {
		t.Errorf("flag refs missing: %v", flags)
	}
}

func TestAST_HelperAvailableInThisEnv(t *testing.T) {
	// The high-fidelity assertions above rely on the AST path. Make the
	// dependence explicit: if python3 is missing here, those tests silently
	// exercised the fallback instead. Skip-with-note rather than fail.
	if findPython() == "" {
		t.Skip("python3 not available; AST-path tests above ran via the Go regex fallback")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
}
