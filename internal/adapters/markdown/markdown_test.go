package markdown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
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

func TestParse_SectionPath_HeadingStack(t *testing.T) {
	// README with H1/H2/H3 nesting and a flag mention inside each
	// level. Verifies the heading stack on each emitted claim.
	body := strings.Join([]string{
		"# fd",
		"",
		"Top-level mention of `--hidden`.",
		"",
		"## How to use",
		"",
		"### Pattern syntax",
		"",
		"Use `--hidden` to include hidden files.",
		"",
		"### Command-line options",
		"",
		"```",
		"  -H, --hidden    Search hidden files",
		"```",
		"",
		"## Installation",
		"",
		"Run `brew install --foo` to install.",
	}, "\n")
	repo := setupRepo(t, map[string]string{"README.md": body})
	p := New(Config{Include: []string{"README.md"}})
	claims, err := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	type wantPath struct {
		flag    string
		section []string
	}
	wants := []wantPath{
		{"--hidden", []string{"fd"}},                                       // preamble after H1
		{"--hidden", []string{"fd", "How to use", "Pattern syntax"}},       // H3
		{"--hidden", []string{"fd", "How to use", "Command-line options"}}, // inside code block under H3
		{"--foo", []string{"fd", "Installation"}},                          // H2
	}
	var got []wantPath
	for _, c := range claims {
		if c.GetKind() != docclaimpb.DocClaimKind_PROSE_MENTION {
			continue
		}
		for _, r := range c.GetContractRefs() {
			if strings.HasPrefix(r, "--") {
				got = append(got, wantPath{flag: r, section: append([]string(nil), c.GetSectionPath()...)})
			}
		}
	}
	if len(got) != len(wants) {
		t.Fatalf("expected %d flag claims, got %d: %+v", len(wants), len(got), got)
	}
	for i, w := range wants {
		if got[i].flag != w.flag {
			t.Errorf("claim[%d].flag = %q, want %q", i, got[i].flag, w.flag)
		}
		if !equalStrings(got[i].section, w.section) {
			t.Errorf("claim[%d].section = %v, want %v", i, got[i].section, w.section)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParse_QualifiedMentions(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"docs/io.md": "Use `fuchsia.io/Directory.Open` to open a directory. " +
			"You can also call `Node.Sync` afterwards. " +
			"Avoid `nope` — bare identifiers are skipped.",
	})
	p := New(Config{Include: []string{"docs/**/*.md"}})
	claims, err := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			got[r] = true
		}
	}
	if !got["fuchsia.io/Directory.Open"] {
		t.Errorf("missing fuchsia.io/Directory.Open mention: %v", got)
	}
	if !got["Node.Sync"] {
		t.Errorf("missing Node.Sync mention: %v", got)
	}
	if got["nope"] {
		t.Errorf("bare identifier 'nope' should NOT be surfaced")
	}
}

func TestParse_CodeBlocksOptIn(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"docs/x.md": "Here's some Rust code:\n\n```rust\nlet x = open(\"/foo\")?;\nx.read()?;\n```\n\nAnd opaque:\n\n```\nrandom text\n```\n",
	})
	// First with code blocks disabled.
	p := New(Config{Include: []string{"docs/**/*.md"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	for _, c := range claims {
		if c.GetKind() == docclaimpb.DocClaimKind_EXAMPLE {
			t.Errorf("expected no EXAMPLE claims without opt-in; got %v", c)
		}
	}
	// Now with rust enabled.
	p = New(Config{
		Include:            []string{"docs/**/*.md"},
		CodeBlockLanguages: []string{"rust"},
	})
	claims, _ = p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	var examples int
	for _, c := range claims {
		if c.GetKind() == docclaimpb.DocClaimKind_EXAMPLE {
			examples++
			if !strings.Contains(c.GetRawText(), "open") {
				t.Errorf("EXAMPLE content missing: %q", c.GetRawText())
			}
		}
	}
	if examples != 1 {
		t.Errorf("expected 1 EXAMPLE, got %d", examples)
	}
}

func TestParse_CodeBlocksDontProduceMentions(t *testing.T) {
	// A bare identifier inside a code block must NOT generate a prose mention.
	repo := setupRepo(t, map[string]string{
		"docs/x.md": "```cpp\nfuchsia.io/Directory.Open(x);\n```\n",
	})
	p := New(Config{Include: []string{"docs/**/*.md"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	for _, c := range claims {
		if c.GetKind() == docclaimpb.DocClaimKind_PROSE_MENTION {
			t.Errorf("identifier inside code block should not produce a prose mention; got %+v", c)
		}
	}
}

func TestParse_SubstanceClassification(t *testing.T) {
	cases := []struct {
		text string
		want commonpb.Substance
	}{
		{"`Proto.M`", commonpb.Substance_SIGNATURE_ONLY},
		{"In short, `Proto.M` opens a thing for you somehow.", commonpb.Substance_PARTIAL},
		{strings.Repeat("word ", 30) + " `Proto.M` " + strings.Repeat("word ", 30), commonpb.Substance_SUBSTANTIVE},
	}
	for _, c := range cases {
		got := classifySubstance(c.text)
		if got != c.want {
			t.Errorf("classifySubstance(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestParse_DerivesLineNumbers(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"docs/x.md": "Intro line.\n\nSecond paragraph mentioning `foo.Bar`.\n\nThird line.",
	})
	p := New(Config{Include: []string{"docs/**/*.md"}})
	claims, _ := p.Parse(context.Background(), repo, adapters.ScopeConfig{})
	if len(claims) == 0 {
		t.Fatalf("no claims")
	}
	if claims[0].GetLocation().GetLine() != 3 {
		t.Errorf("line = %d, want 3", claims[0].GetLocation().GetLine())
	}
}
