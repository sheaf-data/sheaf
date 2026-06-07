// Command ghdocs-normalize converts gh's `gen-docs --website` output
// into the canonical per-subcommand markdown the markdowncli adapter
// reads (docker/cli style: an "### Options" section followed by a
// `Name | Description` pipe table).
//
// gh renders each command's flags as an HTML <dl class="flags"> block;
// kubernetes renders them as <tr> pairs; docker writes the pipe table
// directly. Rather than teach the shared markdowncli adapter all three
// dialects, each ecosystem gets a thin EDGE NORMALIZER like this one:
// every bit of gh's HTML-flag-format knowledge lives here, the core
// adapter stays format-stable, and dropping gh means deleting this one
// command — nothing in the core moves. It mirrors the contract side,
// where cmd/kubectl-yamlgen normalizes every CLI's --help into one
// YAML schema the cobra adapter consumes unchanged.
//
// Usage:
//
//	ghdocs-normalize --in  <gh gen-docs --website output dir> \
//	                 --out <destination dir for canonical markdown>
//
// Every *.md under --in is written to --out: files with a
// <dl class="flags"> block get that block rewritten into a pipe table;
// all other content (frontmatter, the H2 title, prose, code fences) is
// copied verbatim so the adapter's command-level and example joins are
// unaffected.
package main

import (
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// flagDLRx isolates a gh flag definition list: <dl class="flags"> … </dl>.
	flagDLRx = regexp.MustCompile(`(?s)<dl\s+class="flags">.*?</dl>`)
	// dtddRx matches one flag entry inside that list: <dt>…</dt> <dd>…</dd>.
	dtddRx = regexp.MustCompile(`(?s)<dt>(.*?)</dt>\s*<dd>(.*?)</dd>`)
	// codeRx matches a <code>…</code> token inside a <dt> (the flag spellings).
	codeRx = regexp.MustCompile(`(?s)<code>(.*?)</code>`)
	// htmlTagRx strips any remaining markup.
	htmlTagRx = regexp.MustCompile(`<[^>]+>`)
	// highlightBlockRx matches a gh worked-example block — Jekyll Liquid
	// highlight tags wrapping the examples, e.g.
	//   {% highlight bash %}{% raw %}\n$ gh … --flag\n{% endraw %}{% endhighlight %}
	// captured as (language, code) so it can become a ```lang fence.
	highlightBlockRx = regexp.MustCompile(`(?s)\{%\s*highlight\s+(\w+)\s*%\}\s*\{%\s*raw\s*%\}(.*?)\{%\s*endraw\s*%\}\s*\{%\s*endhighlight\s*%\}`)
	// liquidTagRx matches any leftover standalone Liquid tag (the
	// document-level {% raw %}/{% endraw %} wrapper gh emits).
	liquidTagRx = regexp.MustCompile(`\{%[^%]*%\}`)
)

func main() {
	in := flag.String("in", "", "directory of gh `gen-docs --website` markdown")
	out := flag.String("out", "", "destination directory for canonical markdown")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "ghdocs-normalize: both --in and --out are required")
		os.Exit(2)
	}
	total, converted, err := run(*in, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghdocs-normalize:", err)
		os.Exit(1)
	}
	fmt.Printf("ghdocs-normalize: wrote %d files to %s (%d had a flags list)\n", total, *out, converted)
}

func run(inDir, outDir string) (total, converted int, err error) {
	err = filepath.WalkDir(inDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, err := filepath.Rel(inDir, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		newBody, didConvert := normalize(string(body))
		dst := filepath.Join(outDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(newBody), 0o644); err != nil {
			return err
		}
		total++
		if didConvert {
			converted++
		}
		return nil
	})
	return total, converted, err
}

// normalize applies gh's three format fixes so the markdowncli adapter
// can read the page: Liquid example blocks become ```fences```, leftover
// Liquid tags are stripped, and <dl class="flags"> option lists become
// pipe tables. Returns the rewritten body and whether a flags list was
// converted (for the run summary). A dl we can't parse is left untouched.
func normalize(body string) (string, bool) {
	// (1) gh wraps worked examples in Jekyll Liquid highlight tags rather
	// than fenced code blocks, so markdowncli's code-fence scan (and its
	// per-flag example pass) never sees them. Convert each block to a
	// ```lang fence. Run BEFORE the Liquid strip so the inner
	// {% raw %}/{% endraw %} are consumed here, not stripped away.
	body = highlightBlockRx.ReplaceAllStringFunc(body, func(m string) string {
		sub := highlightBlockRx.FindStringSubmatch(m)
		return "```" + sub[1] + "\n" + strings.TrimSpace(sub[2]) + "\n```"
	})
	// (2) Strip the remaining document-level {% raw %}/{% endraw %}
	// wrapper — Jekyll templating, not content.
	body = liquidTagRx.ReplaceAllString(body, "")
	// (3) Rewrite gh's <dl class="flags"> option lists into pipe tables.
	found := false
	body = flagDLRx.ReplaceAllStringFunc(body, func(block string) string {
		rows := flagRows(block)
		if len(rows) == 0 {
			return block
		}
		found = true
		var b strings.Builder
		b.WriteString("| Name | Description |\n| --- | --- |\n")
		for _, r := range rows {
			b.WriteString("| ")
			b.WriteString(r[0])
			b.WriteString(" | ")
			b.WriteString(r[1])
			b.WriteString(" |\n")
		}
		return b.String()
	})
	return body, found
}

// flagRows extracts {nameCell, description} pairs from one
// <dl class="flags"> block, in document order.
func flagRows(block string) [][2]string {
	var rows [][2]string
	for _, m := range dtddRx.FindAllStringSubmatch(block, -1) {
		name := nameCell(m[1])
		if name == "" {
			continue
		}
		rows = append(rows, [2]string{name, descCell(m[2])})
	}
	return rows
}

// nameCell turns a <dt> like
//
//	<code>-a</code>, <code>--assignee &lt;string&gt;</code>
//
// into the canonical Name cell "`-a`, `--assignee`" — the long form is
// required, the shorthand is included when present. The type
// placeholder (<string>) is dropped; the adapter keys on the flag name.
func nameCell(dt string) string {
	var long, short string
	for _, c := range codeRx.FindAllStringSubmatch(dt, -1) {
		tok := strings.Fields(plain(c[1]))
		if len(tok) == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(tok[0], "--"):
			if long == "" {
				long = tok[0]
			}
		case strings.HasPrefix(tok[0], "-"):
			if short == "" {
				short = tok[0]
			}
		}
	}
	if long == "" {
		return ""
	}
	if short != "" {
		return "`" + short + "`, `" + long + "`"
	}
	return "`" + long + "`"
}

// descCell flattens a <dd> into one line of plain prose and escapes the
// pipe so a description can't break the markdown table.
func descCell(dd string) string {
	s := strings.Join(strings.Fields(plain(dd)), " ")
	return strings.ReplaceAll(s, "|", "/")
}

// plain strips HTML tags and decodes HTML entities, returning trimmed
// text. Whitespace (including any &nbsp;) is collapsed downstream by the
// caller's strings.Fields, so no explicit nbsp handling is needed here.
func plain(s string) string {
	s = htmlTagRx.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}
