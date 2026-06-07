package clap

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

// A Parser struct with a #[command(name = "...")] attribute should
// emit one SUBCOMMAND for the binary plus one element per #[arg(...)]
// field.
func TestDiscover_RootBinaryWithCommandName(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
use clap::Parser;

/// A line-oriented search tool.
#[derive(Parser)]
#[command(name = "rg", about = "search stuff")]
pub struct Opts {
    /// case-sensitive search
    #[arg(long, short = 's')]
    pub case_sensitive: bool,

    /// pattern to match
    #[arg(value_name = "pattern")]
    pub pattern: String,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []string{"rg", "rg --case-sensitive", "rg <pattern>"}
	if !sliceEq(elemIDs(elems), want) {
		t.Errorf("got %v, want %v", elemIDs(elems), want)
	}
	// Kind check: the bool field becomes a SWITCH; the positional, POSITIONAL.
	if findElem(elems, "rg --case-sensitive").GetKind() != contractpb.ContractElementKind_SWITCH {
		t.Errorf("expected SWITCH for --case-sensitive")
	}
	if findElem(elems, "rg <pattern>").GetKind() != contractpb.ContractElementKind_POSITIONAL {
		t.Errorf("expected POSITIONAL for <pattern>")
	}
}

// When the struct has no #[command(name = "...")], the crate-root
// basename is used as the binary name.
func TestDiscover_BinaryNameFromCrateRoot(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"crates/fd/src/cli.rs": `
use clap::Parser;
#[derive(Parser)]
pub struct Opts {
    #[arg(long)]
    pub verbose: bool,
}
`,
	})
	a := New(Config{CrateRoots: []string{"crates/fd"}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if !sliceEq(elemIDs(elems), []string{"fd", "fd --verbose"}) {
		t.Errorf("got %v", elemIDs(elems))
	}
}

// snake_case fields become kebab-case flags. Explicit `long = "X"`
// overrides the derived name.
func TestDiscover_FlagNaming(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
#[derive(Parser)]
#[command(name = "demo")]
pub struct Opts {
    #[arg(long)]
    pub output_format: String,

    #[arg(long = "max-depth")]
    pub depth: usize,

    #[arg(long = "no-ignore", short = 'I')]
    pub no_ignore: bool,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	want := []string{"demo", "demo --max-depth", "demo --no-ignore", "demo --output-format"}
	if !sliceEq(elemIDs(elems), want) {
		t.Errorf("got %v", elemIDs(elems))
	}
}

// #[arg(default_value = ...)] promotes a flag to CONFIG_KNOB. A bool
// field stays a SWITCH even if it has a default.
func TestDiscover_DefaultValueBecomesConfigKnob(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
#[derive(Parser)]
#[command(name = "demo")]
pub struct Opts {
    /// always-set color choice
    #[arg(long, default_value_t = ColorWhen::Auto)]
    pub color: ColorWhen,

    /// just a per-call switch
    #[arg(long)]
    pub verbose: bool,

    /// per-invocation value, no default
    #[arg(long)]
    pub pattern: String,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	checks := map[string]contractpb.ContractElementKind{
		"demo --color":   contractpb.ContractElementKind_CONFIG_KNOB,
		"demo --verbose": contractpb.ContractElementKind_SWITCH,
		"demo --pattern": contractpb.ContractElementKind_FLAG,
	}
	for id, want := range checks {
		e := findElem(elems, id)
		if e == nil {
			t.Errorf("missing %s", id)
			continue
		}
		if e.GetKind() != want {
			t.Errorf("%s: got %v, want %v", id, e.GetKind(), want)
		}
	}
}

// #[arg(hide = true)] fields are skipped (they don't show in --help).
// hide_short_help / hide_possible_values / hide_default_value are
// distinct keywords and must NOT hide the element.
func TestDiscover_HideTrueIsSkipped(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
#[derive(Parser)]
#[command(name = "demo")]
pub struct Opts {
    #[arg(long, hide = true)]
    pub internal_only: bool,

    /// visible in --help
    #[arg(long, hide_short_help = true)]
    pub visible_long_help_only: bool,

    /// visible in --help (hide_possible_values is about value list)
    #[arg(long, hide_possible_values = true)]
    pub regular_flag: bool,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	got := elemIDs(elems)
	want := []string{"demo", "demo --regular-flag", "demo --visible-long-help-only"}
	if !sliceEq(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Subcommand enum variants become child SUBCOMMAND elements. When a
// variant carries an Args-derived struct, that struct's flags attach
// under the child command's path.
func TestDiscover_SubcommandsAndNestedFlags(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
use clap::{Parser, Subcommand, Args};

/// gh CLI.
#[derive(Parser)]
#[command(name = "gh")]
pub struct Cli {
    #[command(subcommand)]
    pub cmd: Cmd,
}

#[derive(Subcommand)]
pub enum Cmd {
    /// Auth ops.
    Auth(AuthArgs),
    /// Issue ops.
    Issue(IssueArgs),
}

#[derive(Args)]
pub struct AuthArgs {
    /// login subcommand stub
    #[arg(long)]
    pub login: bool,
}

#[derive(Args)]
pub struct IssueArgs {
    /// owner/repo
    #[arg(long, short = 'R')]
    pub repo: String,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	got := elemIDs(elems)
	want := []string{
		"gh",
		"gh auth",
		"gh auth --login",
		"gh issue",
		"gh issue --repo",
	}
	if !sliceEq(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Parent SUBCOMMAND should reference its children via Relationships.
	parent := findElem(elems, "gh")
	if len(parent.GetRelationships()) != 2 {
		t.Errorf("expected 2 child relationships under gh, got %d", len(parent.GetRelationships()))
	}
}

// `#[command(name = "X")]` on a Subcommand variant overrides the
// camelCase→kebab-case default.
func TestDiscover_SubcommandNameOverride(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
#[derive(Parser)]
#[command(name = "tool")]
pub struct Cli {
    #[command(subcommand)]
    pub cmd: Cmd,
}

#[derive(Subcommand)]
pub enum Cmd {
    #[command(name = "do-thing")]
    DoThing,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if findElem(elems, "tool do-thing") == nil {
		t.Errorf("expected `tool do-thing` (renamed via #[command(name)]); got %v", elemIDs(elems))
	}
}

// #[command(flatten)] pulls the named Args struct's fields into the
// parent's flag list, not as a child subcommand.
func TestDiscover_CommandFlatten(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
#[derive(Parser)]
#[command(name = "fd")]
pub struct Opts {
    /// the search pattern
    #[arg(value_name = "pattern")]
    pub pattern: String,

    #[command(flatten)]
    pub exec: Exec,
}

#[derive(Args)]
pub struct Exec {
    /// -x / --exec
    #[arg(long, short = 'x')]
    pub exec: Option<String>,

    /// -X / --exec-batch
    #[arg(long = "exec-batch", short = 'X')]
    pub exec_batch: Option<String>,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	got := elemIDs(elems)
	want := []string{"fd", "fd --exec", "fd --exec-batch", "fd <pattern>"}
	if !sliceEq(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Doc comments above the struct become the binary's DocCommentExcerpt;
// doc comments above each field become the field element's excerpt.
// Falls back to #[arg(help = "...")] when there's no /// comment.
func TestDiscover_DocCommentsAttached(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
/// Line one.
/// Line two.
#[derive(Parser)]
#[command(name = "demo")]
pub struct Opts {
    /// Verbose output.
    #[arg(long)]
    pub verbose: bool,

    #[arg(long, help = "Pattern to match")]
    pub pattern: String,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	bin := findElem(elems, "demo")
	if !strings.Contains(bin.GetDocCommentExcerpt(), "Line one") || !strings.Contains(bin.GetDocCommentExcerpt(), "Line two") {
		t.Errorf("binary doc = %q", bin.GetDocCommentExcerpt())
	}
	v := findElem(elems, "demo --verbose")
	if !strings.Contains(v.GetDocCommentExcerpt(), "Verbose output") {
		t.Errorf("verbose doc = %q", v.GetDocCommentExcerpt())
	}
	// Falls back to help= when there's no ///.
	p := findElem(elems, "demo --pattern")
	if !strings.Contains(p.GetDocCommentExcerpt(), "Pattern to match") {
		t.Errorf("pattern doc = %q", p.GetDocCommentExcerpt())
	}
}

// target/ subtree is excluded by default.
func TestDiscover_ExcludesTargetByDefault(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
#[derive(Parser)]
#[command(name = "demo")]
pub struct Opts {
    #[arg(long)]
    pub keep: bool,
}
`,
		"target/debug/build/x.rs": `
#[derive(Parser)]
#[command(name = "demo")]
pub struct Opts {
    #[arg(long)]
    pub drop: bool,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	for _, e := range elems {
		if strings.Contains(e.GetId(), "--drop") {
			t.Errorf("target/ should be excluded; got %s", e.GetId())
		}
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

// Every FLAG / SWITCH / CONFIG_KNOB advertises its bare long form as
// an alias (e.g. "--hidden"), plus the bare short form ("-H") when
// declared. The root SUBCOMMAND element gets no aliases.
func TestDiscover_FlagAliases(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/cli.rs": `
#[derive(Parser)]
#[command(name = "demo")]
pub struct Opts {
    #[arg(long = "hidden", short = 'H')]
    pub hidden: bool,

    #[arg(long = "max-depth")]
    pub max_depth: usize,
}
`,
	})
	a := New(Config{CrateRoots: []string{"."}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	checks := map[string][]string{
		"demo --hidden":    {"--hidden", "-H"},
		"demo --max-depth": {"--max-depth"},
		"demo":             nil,
	}
	for id, want := range checks {
		e := findElem(elems, id)
		if e == nil {
			t.Errorf("missing %s", id)
			continue
		}
		got := e.GetAliases()
		if !sliceEq(got, want) {
			t.Errorf("%s: got aliases %v, want %v", id, got, want)
		}
	}
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
