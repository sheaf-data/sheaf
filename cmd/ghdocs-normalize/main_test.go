package main

import (
	"strings"
	"testing"
)

// A trimmed slice of a real gh `gen-docs --website` page: an H2 title,
// a usage fence, an "### Options" header, and the <dl class="flags">
// block (with a type-placeholder flag, a shorthand+long flag, and a
// bool switch). Backticks are concatenated to avoid clashing with the
// raw-string literal.
var sampleDoc = "## gh pr list\n\n" +
	"```\ngh pr list [flags]\n```\n\n" +
	"### Options\n\n\n" +
	`<dl class="flags">
	<dt>
		<code>--app &lt;string&gt;</code></dt>
	<dd>Filter by GitHub App author</dd>

	<dt><code>-a</code>,
		<code>--assignee &lt;string&gt;</code></dt>
	<dd>Filter by assignee</dd>

	<dt><code>-d</code>,
		<code>--draft</code></dt>
	<dd>Filter by draft state</dd>
</dl>
`

func TestNormalize_GhFlagDL(t *testing.T) {
	out, found := normalize(sampleDoc)
	if !found {
		t.Fatal("expected the <dl class=\"flags\"> block to be converted")
	}
	if strings.Contains(out, "<dl") || strings.Contains(out, "<dt>") || strings.Contains(out, "<dd>") {
		t.Errorf("HTML flag list survived normalization:\n%s", out)
	}
	for _, want := range []string{
		"| Name | Description |",
		"| --- | --- |",
		"| `--app` | Filter by GitHub App author |",
		"| `-a`, `--assignee` | Filter by assignee |",
		"| `-d`, `--draft` | Filter by draft state |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing canonical row %q in:\n%s", want, out)
		}
	}
	// Non-flag content must survive verbatim so the adapter still makes
	// the command-level and code-fence joins.
	if !strings.Contains(out, "## gh pr list") || !strings.Contains(out, "gh pr list [flags]") {
		t.Error("normalize dropped non-flag content")
	}
}

func TestNormalize_PipeInDescriptionEscaped(t *testing.T) {
	in := `<dl class="flags">
	<dt><code>--state &lt;string&gt;</code></dt>
	<dd>Filter by state: {open|closed|all}</dd>
</dl>`
	out, found := normalize(in)
	if !found {
		t.Fatal("expected conversion")
	}
	// The literal pipe in the description must not introduce phantom
	// table columns.
	row := ""
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "--state") {
			row = ln
		}
	}
	if strings.Count(row, "|") != 3 { // leading, separator, trailing
		t.Errorf("description pipe broke the table row: %q", row)
	}
}

func TestNormalize_NoFlagList(t *testing.T) {
	body := "## gh version\n\nShow the version of gh in use.\n"
	out, found := normalize(body)
	if found {
		t.Error("no <dl class=\"flags\"> present, but a conversion was reported")
	}
	if out != body {
		t.Errorf("a body with no flags list should pass through unchanged:\ngot:  %q\nwant: %q", out, body)
	}
}

func TestNormalize_LiquidExamplesToFence(t *testing.T) {
	// gh emits worked examples wrapped in Jekyll Liquid highlight tags,
	// not fences — so markdowncli's per-flag example scan can't see them.
	in := "{% raw %}## gh issue create\n\n" +
		"```\ngh issue create [flags]\n```\n{% endraw %}\n" +
		"### Examples\n\n" +
		"{% highlight bash %}{% raw %}\n" +
		"$ gh issue create --title \"Bug\" --body \"x\"\n" +
		"$ gh issue create --label bug\n" +
		"{% endraw %}{% endhighlight %}\n"
	out, _ := normalize(in)
	if strings.Contains(out, "{%") {
		t.Errorf("Liquid tags survived normalization:\n%s", out)
	}
	if !strings.Contains(out, "```bash\n$ gh issue create --title") {
		t.Errorf("examples were not wrapped in a bash fence:\n%s", out)
	}
	for _, f := range []string{"--title", "--body", "--label"} {
		if !strings.Contains(out, f) {
			t.Errorf("example flag %s missing from fenced output", f)
		}
	}
	// The usage synopsis fence and the H2 title must survive.
	if !strings.Contains(out, "## gh issue create") || !strings.Contains(out, "gh issue create [flags]") {
		t.Error("non-example content was dropped")
	}
}
