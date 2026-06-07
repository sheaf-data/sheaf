package rusttest

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

func setupRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("writefile: %v", err)
		}
	}
	return dir
}

func TestDiscover_Basic(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/foo.rs": `
#[test]
fn returns_true_for_empty_input() {
    assert!(check(""));
}

#[test]
fn returns_false_for_nonempty() {
    assert!(!check("abc"));
}
`,
	})
	p := New(Config{Include: []string{"src/**/*.rs"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("got %d, want 2", len(tests))
	}
	ids := []string{tests[0].GetId(), tests[1].GetId()}
	sort.Strings(ids)
	want := []string{"src::foo::returns_false_for_nonempty", "src::foo::returns_true_for_empty_input"}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("id[%d] = %q, want %q", i, ids[i], w)
		}
	}
}

func TestDiscover_ExtraAttribute(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/lib.rs": `
#[fuchsia::test]
fn does_a_thing() {}
`,
	})
	p := New(Config{
		Include:             []string{"src/**/*.rs"},
		ExtraTestAttributes: []string{"fuchsia::test"},
	})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tests) != 1 || tests[0].GetName() != "does_a_thing" {
		t.Errorf("got %+v", tests)
	}
}

func TestDiscover_AttributeWithArgs(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/lib.rs": `
#[tokio::test(flavor = "multi_thread")]
async fn parallel_test() {}
`,
	})
	p := New(Config{Include: []string{"src/**/*.rs"}})
	tests, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if len(tests) != 1 || tests[0].GetName() != "parallel_test" {
		t.Errorf("got %+v", tests)
	}
}

func TestDiscover_SkipsTargetDirByDefault(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/lib.rs": `
#[test]
fn included() {}
`,
		"target/debug/build/something.rs": `
#[test]
fn excluded() {}
`,
	})
	p := New(Config{})
	tests, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	for _, tc := range tests {
		if tc.GetName() == "excluded" {
			t.Errorf("target/ should be excluded by default; found %q", tc.GetName())
		}
	}
}

func TestDiscover_TokenizeFnName(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/lib.rs": `
#[test]
fn checks_three_separate_things() {}
`,
	})
	p := New(Config{Include: []string{"src/**/*.rs"}})
	tests, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	got := tests[0].GetNameTokens()
	want := []string{"checks", "three", "separate", "things"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("token[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A test body that invokes a CLI binary via a clap-style args slice
// should pick up every long-form flag literal as a ContractRef. The
// extraction is bounded to the function body — flag literals in the
// next function's #[test_case(...)] decorations do NOT bleed back
// into the previous test.
func TestDiscover_FlagLiteralExtraction(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"tests/tests.rs": `
#[test]
fn test_number_parsing_errors() {
    let te = TestEnv::new(&[], &[]);
    te.assert_failure(&["--threads=a"]);
    te.assert_failure(&["--max-depth=a"]);
}

#[test_case("--hidden", &["--no-hidden"] ; "hidden")]
#[test_case("--follow", &["--no-follow"] ; "follow")]
fn test_opposing(flag: &str, opposing: &[&str]) {
    let te = TestEnv::new(&[], &[]);
    te.assert_success(&[flag], &[]);
}
`,
	})
	p := New(Config{Include: []string{"tests/**/*.rs"}})
	tests, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Find test_number_parsing_errors. It should ONLY have flags from
	// its own body — NOT --hidden, --no-hidden, --follow, --no-follow
	// from the next function's #[test_case] decorations.
	var numParsing []string
	for _, tc := range tests {
		if tc.GetName() == "test_number_parsing_errors" {
			numParsing = tc.GetContractRefs()
		}
	}
	if numParsing == nil {
		t.Fatalf("test_number_parsing_errors not discovered")
	}
	got := append([]string(nil), numParsing...)
	sort.Strings(got)
	want := []string{"--max-depth", "--threads"}
	if len(got) != len(want) {
		t.Fatalf("test_number_parsing_errors refs: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("ref[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- ffx-anchored body-invocation extraction (M1 + M2) ---

// ffxScope is the CLI-shaped scope that enables invocation extraction.
var ffxScope = adapters.ScopeConfig{Libraries: []string{"ffx"}}

// testCaseHelper is a tiny projection so the table assertions don't carry
// the full protobuf around.
type testCaseHelper struct {
	name string
	refs []string
}

func discover(t *testing.T, scope adapters.ScopeConfig, files map[string]string) []*testCaseHelper {
	t.Helper()
	repo := setupRepo(t, files)
	p := New(Config{Include: []string{"**/*.rs"}, ExtraTestAttributes: []string{"fuchsia::test"}})
	tests, err := p.Discover(context.Background(), repo, scope)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	out := make([]*testCaseHelper, 0, len(tests))
	for _, tc := range tests {
		out = append(out, &testCaseHelper{name: tc.GetName(), refs: tc.GetContractRefs()})
	}
	return out
}

// findRefs returns the ContractRefs of the discovered test with the given
// function name. Fatals if the test wasn't discovered.
func findRefs(t *testing.T, tests []*testCaseHelper, name string) []string {
	t.Helper()
	for _, tc := range tests {
		if tc.name == name {
			return tc.refs
		}
	}
	t.Fatalf("test %q not discovered", name)
	return nil
}

func hasRef(refs []string, want string) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}

func assertHasRefs(t *testing.T, refs []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !hasRef(refs, w) {
			t.Errorf("missing ref %q; got %v", w, refs)
		}
	}
}

func assertNoRefPrefix(t *testing.T, refs []string, prefix string) {
	t.Helper()
	for _, r := range refs {
		if strings.HasPrefix(r, prefix) {
			t.Errorf("unexpected ref with prefix %q: %q (all: %v)", prefix, r, refs)
		}
	}
}

// M1: a literal ffx-harness call credits the command prefixes AND the
// long flag, canonicalized to the element-ID form.
func TestInvocation_M1_HarnessLiteral(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[test]
fn target_list_json() {
    let out = isolate.ffx(["target", "list", "--format", "json"]).await?;
}
`,
	})
	refs := findRefs(t, tests, "target_list_json")
	assertHasRefs(t, refs, "ffx target", "ffx target list", "ffx target list --format")
}

// Regression (the e2e_emu shape): a multi-line ffx invocation in a test
// HELPER method — outside any #[test] body — must still be credited. The
// per-test range never sees it; the whole-file pass emits it on the
// synthetic <ffx-invocations> TestCase. Also exercises a trailing dynamic
// token (&self.name) being skipped without truncating the literals before it.
func TestInvocation_HelperMethodOutsideTest(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/e2e_emu.rs": `
struct Harness { name: String }
impl Harness {
    async fn start(&self) {
        self.ffx(&[
            "emu",
            "start",
            "--headless",
            "--net",
            &self.name,
        ]).await.unwrap();
    }
}

#[fuchsia::test]
fn uses_the_harness() {
    let h = Harness { name: "x".into() };
    h.start().await;
}
`,
	})
	// emu start lives in start(), not the #[test] — so it comes through the
	// file-level pass, not the per-test one.
	refs := findRefs(t, tests, "<ffx-invocations>")
	assertHasRefs(t, refs, "ffx emu", "ffx emu start",
		"ffx emu start --headless", "ffx emu start --net")
}

// M1 with the `&[...]` borrow form and a method-style receiver.
func TestInvocation_M1_HarnessBorrowSlice(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[fuchsia::test]
async fn run_component() {
    emu.ffx(&["component", "list"]).await.unwrap();
}
`,
	})
	refs := findRefs(t, tests, "run_component")
	assertHasRefs(t, refs, "ffx component", "ffx component list")
}

// M1 multi-line array, with dynamic tokens interleaved. Literal subcommand
// tokens are collected; the dynamic value tokens are skipped; the long
// flags are credited at the resolved command path.
func TestInvocation_M1_MultiLineWithDynamic(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[test]
fn heapdump_download() {
    emu.ffx(&[
        "profile",
        "heapdump",
        "download",
        "--snapshot-id",
        &snapshot_id.to_string(),
        "--output-file",
        profile_path.to_str().unwrap(),
    ])
    .await
    .expect("download");
}
`,
	})
	refs := findRefs(t, tests, "heapdump_download")
	assertHasRefs(t, refs,
		"ffx profile",
		"ffx profile heapdump",
		"ffx profile heapdump download",
		"ffx profile heapdump download --snapshot-id",
		"ffx profile heapdump download --output-file",
	)
}

// M1 with leading ffx GLOBAL flags before the subcommand. The globals
// (--target, --machine) and their values must be stripped so the command
// path resolves past them, and the globals themselves are credited at the
// ffx root. The trailing per-command flags must still attribute.
func TestInvocation_M1_LeadingGlobalFlags(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[test]
fn target_list_filtered() {
    isolate.ffx(&["--target", "[::1]:8022", "target", "list", "--format", "a", "--no-probe"]).await?;
}
`,
	})
	refs := findRefs(t, tests, "target_list_filtered")
	assertHasRefs(t, refs,
		"ffx target",
		"ffx target list",
		"ffx target list --format",
		"ffx target list --no-probe",
		"ffx --target", // global credited at the root
	)
	// The global value token "[::1]:8022" must not have leaked into a
	// command path, and the bare-flag literal "--target" must not be
	// emitted as a command-scoped flag ref.
	assertNoRefPrefix(t, refs, "ffx target list --target")
}

// M2: Command::new(FFX_TOOL_PATH).args(vec![...]) — the scrutiny pattern.
// The &format!(...) arg is dynamic and skipped; the literal subcommand
// tokens resolve to the command path.
func TestInvocation_M2_CommandNewFfxToolPath(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
const FFX_TOOL_PATH: &str = env!("FFX_TOOL_PATH");
#[test]
fn extract_blobfs() {
    assert!(Command::new(FFX_TOOL_PATH)
        .args(vec![
            "scrutiny",
            "verify",
        ])
        .status()
        .unwrap()
        .success());
}
`,
	})
	refs := findRefs(t, tests, "extract_blobfs")
	assertHasRefs(t, refs, "ffx scrutiny", "ffx scrutiny verify")
}

// M2 same-line Command::new("ffx").args([...]) literal program.
func TestInvocation_M2_CommandNewLiteralFfx(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[test]
fn completion_help() {
    Command::new("ffx").args(["config", "get"]).output().unwrap();
}
`,
	})
	refs := findRefs(t, tests, "completion_help")
	assertHasRefs(t, refs, "ffx config", "ffx config get")
}

// NEGATIVE: a non-ffx `.args([...])` (llvm-profdata: cmd.args(["merge",
// "--sparse", "--output"])) has NO ffx anchor and must produce NO ffx
// refs. The receiver `cmd` is not the ffx harness and there is no
// Command::new(<ffx-path>) on this chain.
func TestInvocation_Negative_NonFfxArgs(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[test]
fn merges_profraws() {
    let mut cmd = Command::new(&self.llvm_profdata_bin);
    cmd.args(["merge", "--sparse", "--output"]).arg(&temp_profdata);
    cmd.status().unwrap();
}
`,
	})
	refs := findRefs(t, tests, "merges_profraws")
	assertNoRefPrefix(t, refs, "ffx ")
}

// NEGATIVE: Command::new on a clearly non-ffx binary must not anchor even
// though a `.args` follows on the same chain.
func TestInvocation_Negative_CommandNewKill(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[test]
fn kills_process() {
    Command::new("kill").args(["-9", "123"]).status().unwrap();
}
`,
	})
	refs := findRefs(t, tests, "kills_process")
	assertNoRefPrefix(t, refs, "ffx ")
}

// A fully-dynamic ffx invocation (no literal subcommand token) yields no
// command refs — we don't emit a bare "ffx".
func TestInvocation_FullyDynamicSkipped(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[test]
fn dynamic_only() {
    isolate.ffx(&[subcommand, &arg]).await?;
}
`,
	})
	refs := findRefs(t, tests, "dynamic_only")
	assertNoRefPrefix(t, refs, "ffx ")
}

// Short flags (-o) carry no contract mapping at the climatch layer and
// must NOT be emitted as command-scoped refs. (The bare-literal pass may
// still emit a "-o" alias ref, but never an "ffx … -o".)
func TestInvocation_ShortFlagsSkipped(t *testing.T) {
	tests := discover(t, ffxScope, map[string]string{
		"src/x.rs": `
#[test]
fn daemon_stop_short() {
    isolate.ffx(&["daemon", "stop", "-t", "3000"]).await?;
}
`,
	})
	refs := findRefs(t, tests, "daemon_stop_short")
	assertHasRefs(t, refs, "ffx daemon", "ffx daemon stop")
	for _, r := range refs {
		if strings.Contains(r, "ffx") && strings.Contains(r, " -t") {
			t.Errorf("short flag leaked into command-scoped ref: %q", r)
		}
	}
}

// GATING: with no CLI-shaped scope library, the extractor is a complete
// no-op — an ffx-harness call produces no command/flag refs. This is what
// preserves every non-CLI rust-test config (kubectl/docker/etc.).
func TestInvocation_NoOpWithoutCLIScope(t *testing.T) {
	tests := discover(t, adapters.ScopeConfig{Libraries: []string{"fuchsia.driver.framework"}}, map[string]string{
		"src/x.rs": `
#[test]
fn target_list_json() {
    isolate.ffx(&["target", "list", "--format", "json"]).await?;
}
`,
	})
	refs := findRefs(t, tests, "target_list_json")
	assertNoRefPrefix(t, refs, "ffx ")
}
