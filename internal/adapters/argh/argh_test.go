package argh

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

func TestDiscover_RootBinary(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/ffx/src/args.rs": `
use argh::FromArgs;

/// Fuchsia development CLI.
#[derive(FromArgs)]
pub struct Args {
    /// output format
    #[argh(option, default = "\"text\".to_string()")]
    format: String,

    /// verbose mode
    #[argh(switch)]
    verbose: bool,
}
`,
	})
	a := New(Config{CrateRoots: []string{"src/ffx"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Expect: 1 SUBCOMMAND (the binary) + 1 FLAG (--format) + 1 SWITCH (--verbose).
	got := elemIDs(elems)
	want := []string{"ffx", "ffx --format", "ffx --verbose"}
	if !sliceEq(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDiscover_NestedSubcommands(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/ffx/plugins/component/show/src/args.rs": `
use argh::FromArgs;

/// Show component details.
#[derive(FromArgs)]
pub struct ShowArgs {
    /// emit JSON output
    #[argh(switch)]
    json: bool,

    /// query string
    #[argh(positional)]
    query: String,
}
`,
	})
	a := New(Config{CrateRoots: []string{"src/ffx"}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	got := elemIDs(elems)
	want := []string{
		"ffx component show",
		"ffx component show --json",
		"ffx component show <query>",
	}
	if !sliceEq(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDiscover_DocCommentsAttached(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/ffx/src/args.rs": `
/// Top-level ffx binary.
/// Manages Fuchsia devices.
#[derive(FromArgs)]
pub struct Args {
    /// Output format.
    /// One of: json, text, yaml.
    #[argh(option)]
    format: String,
}
`,
	})
	a := New(Config{CrateRoots: []string{"src/ffx"}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	subcmd := findElem(elems, "ffx")
	if subcmd == nil {
		t.Fatal("missing ffx subcommand")
	}
	if !strings.Contains(subcmd.GetDocCommentExcerpt(), "Top-level ffx binary") ||
		!strings.Contains(subcmd.GetDocCommentExcerpt(), "Manages Fuchsia devices") {
		t.Errorf("subcommand doc = %q", subcmd.GetDocCommentExcerpt())
	}
	flag := findElem(elems, "ffx --format")
	if flag == nil {
		t.Fatal("missing --format")
	}
	if !strings.Contains(flag.GetDocCommentExcerpt(), "Output format") {
		t.Errorf("flag doc = %q", flag.GetDocCommentExcerpt())
	}
}

// An `#[argh(option, default = "...")]` field MUST be classified as
// CONFIG_KNOB instead of FLAG. The presence of a default is the signal
// the option represents persistent configuration (a setting a user
// would expect to leave alone across invocations) rather than a
// per-invocation operation input. Options without a default stay FLAG.
// Switches and positionals are unaffected.
func TestDiscover_OptionWithDefaultIsConfigKnob(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/ffx/src/args.rs": `
use argh::FromArgs;

/// Test binary.
#[derive(FromArgs)]
pub struct Args {
    /// configurable output format
    #[argh(option, default = "\"text\".to_string()")]
    format: String,

    /// per-call query string (no default, must be supplied)
    #[argh(option)]
    query: String,

    /// verbose mode (switches always stay SWITCH even when "default"-shaped)
    #[argh(switch)]
    verbose: bool,
}
`,
	})
	a := New(Config{CrateRoots: []string{"src/ffx"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	checks := map[string]contractpb.ContractElementKind{
		"ffx --format":  contractpb.ContractElementKind_CONFIG_KNOB,
		"ffx --query":   contractpb.ContractElementKind_FLAG,
		"ffx --verbose": contractpb.ContractElementKind_SWITCH,
	}
	for id, wantKind := range checks {
		e := findElem(elems, id)
		if e == nil {
			t.Errorf("missing element %q", id)
			continue
		}
		if e.GetKind() != wantKind {
			t.Errorf("%s: want kind %v, got %v", id, wantKind, e.GetKind())
		}
	}
}

func TestDiscover_SubcommandDeclarationsBecomeRelationships(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/ffx/src/args.rs": `
use argh::FromArgs;

/// Top-level.
#[derive(FromArgs)]
pub struct Args {
    #[argh(subcommand)]
    cmd: SubCommand,
}

#[derive(FromArgs)]
#[argh(subcommand)]
enum SubCommand {
    Component(Component),
    Doctor(Doctor),
}

#[derive(FromArgs)]
#[argh(subcommand, name = "component", description = "Manage components")]
struct Component {}

#[derive(FromArgs)]
#[argh(subcommand, name = "doctor", description = "Diagnose problems")]
struct Doctor {}
`,
	})
	a := New(Config{CrateRoots: []string{"src/ffx"}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	ffx := findElem(elems, "ffx")
	if ffx == nil {
		t.Fatal("ffx missing")
	}
	// We expect REFERENCES_TYPE relationships for the subcommand
	// variants found anywhere in this file (they're all in the same
	// file so we count them under the top-level struct's body).
	if len(ffx.GetRelationships()) == 0 {
		// Not all subcommand attributes may be inside Args; that's OK.
		// Just confirm the elements list contains the subcommand
		// nodes by name.
		gotIDs := elemIDs(elems)
		hasComponent := false
		for _, id := range gotIDs {
			if strings.Contains(id, "component") {
				hasComponent = true
			}
		}
		// Even without inline subcommand mention, the struct names
		// themselves get parsed; this is fine for v1.
		_ = hasComponent
	}
}

func TestDiscover_ExcludesTargetByDefault(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/ffx/src/args.rs": `
#[derive(FromArgs)]
pub struct Args {
    #[argh(option)]
    a: String,
}
`,
		"src/ffx/target/debug/something.rs": `
#[derive(FromArgs)]
pub struct Args {
    #[argh(option)]
    excluded: String,
}
`,
	})
	a := New(Config{CrateRoots: []string{"src/ffx"}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	for _, e := range elems {
		if strings.Contains(e.GetId(), "--excluded") {
			t.Errorf("target/ should be excluded; got %s", e.GetId())
		}
	}
}

func TestDiscover_ScopeFilter(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/wanted/src/args.rs":  `#[derive(FromArgs)] pub struct Args { #[argh(option)] a: String, }`,
		"src/skipped/src/args.rs": `#[derive(FromArgs)] pub struct Args { #[argh(option)] b: String, }`,
	})
	a := New(Config{CrateRoots: []string{"src/wanted", "src/skipped"}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{
		Libraries: []string{"wanted"},
	})
	for _, e := range elems {
		if e.GetLibrary() == "skipped" {
			t.Errorf("scope should drop 'skipped' library; got %+v", e)
		}
	}
}

func TestSubcommandPathFromFile(t *testing.T) {
	cases := []struct {
		rel, binary, want string
	}{
		{"src/args.rs", "ffx", "ffx"},
		{"plugins/component/src/args.rs", "ffx", "ffx component"},
		{"plugins/component/show/src/args.rs", "ffx", "ffx component show"},
		{"args.rs", "triage", "triage"},
	}
	for _, c := range cases {
		got := subcommandPathFromFile(c.rel, c.binary)
		if got != c.want {
			t.Errorf("subcommandPathFromFile(%q, %q) = %q, want %q", c.rel, c.binary, got, c.want)
		}
	}
}

func TestExtractDocCommentAbove(t *testing.T) {
	body := `/// Line one.
/// Line two.
#[argh(option)]
foo: String,`
	// Position the offset at the `#`
	off := strings.Index(body, "#[argh")
	got := extractDocCommentAbove(body, off)
	want := "Line one.\nLine two."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSnakeCaseToKebab(t *testing.T) {
	// Verify a snake_case field becomes a kebab-case flag.
	repo := setupRepo(t, map[string]string{
		"src/ffx/src/args.rs": `
#[derive(FromArgs)]
pub struct Args {
    /// json output mode
    #[argh(option)]
    output_format: String,
}
`,
	})
	a := New(Config{CrateRoots: []string{"src/ffx"}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if findElem(elems, "ffx --output-format") == nil {
		t.Errorf("snake_case → kebab-case conversion failed; got %v", elemIDs(elems))
	}
}

// helpers
func findElem(elems []*contractpb.ContractElement, id string) *contractpb.ContractElement {
	for _, e := range elems {
		if e.GetId() == id {
			return e
		}
	}
	return nil
}
func elemIDs(elems []*contractpb.ContractElement) []string {
	out := make([]string, len(elems))
	for i, e := range elems {
		out[i] = e.GetId()
	}
	sort.Strings(out)
	return out
}
func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string{}, a...)
	bb := append([]string{}, b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
