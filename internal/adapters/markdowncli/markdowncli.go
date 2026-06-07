// Package markdowncli reads a per-subcommand markdown reference
// bundle (one file per subcommand) and emits DocClaims the indexer
// can cross-reference against cobra-discovered ContractElements.
// It was driven out against docker/cli's docs/reference/commandline/
// but is intentionally generic: any CLI whose docs follow the
// one-file-per-subcommand pattern with a markdown options table
// (or no options table at all) can use this adapter.
//
// Each file maps to one command. The command path is resolved in
// priority order:
//
//  1. Filename-derived path with binary_name prefix
//     (container_run.md + binary="docker" → "docker container run")
//  2. YAML frontmatter `title:` field
//  3. First H1
//
// Substance is graded from the word count of the prose body
// (frontmatter, comments, code fences and table rows stripped).
//
// In addition to the file-level DocClaim, the adapter parses:
//
//   - The Options / Inherited options / Global options table (if
//     present) — one DocClaim per flag row, with ContractRefs like
//     ["<command> --flag-name"].
//   - Each fenced code block — one DocClaimKind_EXAMPLE attributed
//     to the file's subcommand.
//
// Table column positions default to "Name first, Description last"
// (docker/cli's convention). Override via OptionsTable config when
// scanning a CLI that uses a different table layout — or rely on
// the adapter's header-row sniffing, which finds the columns by
// name when the markdown emits an explicit header row.

package markdowncli

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "markdowncli"
const Version = "0.1.0"

const defaultURLBase = ""

// URLStyle controls how the adapter turns a markdown file into the
// canonical URL on the rendered docs site. See `MarkdownCLIConfig.URLStyle`
// in proto/config.proto for the full rationale; in short, docker/cli
// and Hugo-style sites disagree on the slug shape.
type URLStyle int

const (
	// URLStyleCommandPath drops the binary name and joins the
	// remaining subcommand tokens with "/". `container_run.md` →
	// ".../container/run/". Matches docker/cli.
	URLStyleCommandPath URLStyle = 0
	// URLStyleFilePath uses the file's relative path under docs_dir
	// verbatim (extension stripped, `_index` stripped, trailing slash
	// appended). Matches Hugo/Jekyll sites where the URL mirrors the
	// on-disk hierarchy — e.g. kubernetes website's
	// kubectl_get/_index.md is published at ".../kubectl_get/".
	URLStyleFilePath URLStyle = 1
)

type Adapter struct {
	docsDir      string
	urlBase      string
	include      []string
	exclude      []string
	binaryName   string
	optionsTable OptionsTableConfig
	urlStyle     URLStyle
}

type Config struct {
	DocsDir    string
	URLBase    string
	Include    []string
	Exclude    []string
	BinaryName string

	// OptionsTable overrides the options-table parsing defaults.
	// All fields are optional.
	OptionsTable OptionsTableConfig

	// URLStyle picks the URL-derivation strategy. Defaults to
	// URLStyleCommandPath (docker-style).
	URLStyle URLStyle
}

// OptionsTableConfig configures the options-table parser. The
// defaults match docker/cli's convention; override only when your
// CLI's markdown uses a different layout.
type OptionsTableConfig struct {
	// SectionNames are the headers (case-insensitive, leading-#
	// stripped, whitespace-trimmed) that introduce options tables.
	// Default: ["options", "inherited options", "global options",
	// "common options"].
	SectionNames []string
	// NameColumn is the 0-based column index for the flag name when
	// the table has no parseable header row. Default 0 (first
	// column). Header-row sniffing — looking for a cell named "Name"
	// (case-insensitive) — overrides this when available.
	NameColumn int
	// DescriptionColumn is the 0-based column index for the
	// description when sniffing fails. Use -1 for "last column"
	// (default).
	DescriptionColumn int
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.md"}
	}
	urlBase := cfg.URLBase
	if urlBase != "" && !strings.HasSuffix(urlBase, "/") {
		urlBase += "/"
	}
	ot := cfg.OptionsTable
	if len(ot.SectionNames) == 0 {
		ot.SectionNames = []string{
			"options",
			"inherited options",
			"global options",
			"common options",
		}
	}
	if ot.DescriptionColumn == 0 {
		ot.DescriptionColumn = -1 // sentinel: last column
	}
	return &Adapter{
		docsDir:      cfg.DocsDir,
		urlBase:      urlBase,
		include:      include,
		exclude:      cfg.Exclude,
		binaryName:   cfg.BinaryName,
		optionsTable: ot,
		urlStyle:     cfg.URLStyle,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Parse implements adapters.RenderedReferenceParser. The dockerdoc
// adapter walks the docs_dir directly (it has its own filesystem
// path, distinct from the repo scope), so the standard
// RenderedReferenceParser signature with no repoRoot fits.
func (a *Adapter) Parse(ctx context.Context) ([]*docclaimpb.DocClaim, error) {
	if a.docsDir == "" {
		return nil, fmt.Errorf("dockerdoc: docs_dir is empty")
	}
	var out []*docclaimpb.DocClaim
	err := adapters.WalkMatching(a.docsDir, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := filepath.Join(a.docsDir, rel)
		body, err := os.ReadFile(abs)
		if err != nil {
			return fmt.Errorf("dockerdoc: read %s: %w", abs, err)
		}
		claims := a.parseFile(body, rel)
		out = append(out, claims...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, bi := refOrEmpty(out[i]), refOrEmpty(out[j])
		return ai < bi
	})
	return out, nil
}

var (
	frontmatterRx = regexp.MustCompile(`(?s)\A---\s*\n(.*?)\n---\s*\n`)
	titleRx       = regexp.MustCompile(`(?m)^title:\s*(.+?)\s*$`)
	h1Rx          = regexp.MustCompile(`(?m)^#\s+([^\n]+?)\s*$`)
	codeFenceRx   = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\n.*?```")
	htmlCommentRx = regexp.MustCompile(`(?s)<!--.*?-->`)
	tableLineRx   = regexp.MustCompile(`(?m)^\s*\|.*$`)
)

func (a *Adapter) parseFile(body []byte, rel string) []*docclaimpb.DocClaim {
	commandID := a.resolveCommandID(body, rel)
	if commandID == "" {
		return nil
	}
	prose := proseOnly(body)
	words := countWords(prose)
	subcmdURL := ""
	if a.urlBase != "" {
		subcmdURL = a.urlBase + a.urlSlug(commandID, rel)
	}

	out := []*docclaimpb.DocClaim{{
		SourcePath:   rel,
		Location:     &commonpb.SourceLocation{Path: rel, Line: 1},
		RawText:      truncate(strings.TrimSpace(string(prose)), 300),
		ContractRefs: []string{commandID},
		Url:          subcmdURL,
		Substance:    gradeSubstance(words),
		WordCount:    uint32(words),
		Kind:         docclaimpb.DocClaimKind_REFERENCE,
		Adapter:      Name,
	}}

	// Per-flag claims from the options tables. Two extractors run
	// in series: parseFlagTables handles docker/cli's pipe-style
	// markdown tables; parseHTMLFlagTables handles the inline HTML
	// `<tr><td colspan="2">--flag</td></tr><tr><td><p>desc</p></td></tr>`
	// pattern that the kubernetes website ships (auto-generated by
	// kubernetes-sigs/reference-docs). Both run unconditionally —
	// a doc that contains both formats merges its claims.
	flagClaims := a.parseFlagTables(body, commandID)
	flagClaims = append(flagClaims, parseHTMLFlagTables(body, commandID)...)
	for _, fc := range flagClaims {
		fc.SourcePath = rel
		if fc.Location == nil {
			fc.Location = &commonpb.SourceLocation{Path: rel, Line: fc.Location.GetLine()}
		} else {
			fc.Location.Path = rel
		}
		// Anchor URL: most docs sites render flag rows with id="flagname"
		// (no leading dashes). We construct that anchor from the long
		// flag name. Only emit when a URL base is configured.
		if subcmdURL != "" {
			if base := strings.TrimPrefix(strings.TrimPrefix(fc.ContractRefs[0], commandID+" "), "--"); base != "" {
				fc.Url = subcmdURL + "#" + base
			}
		}
		fc.Adapter = Name
		out = append(out, fc)
	}

	// Code-block examples — one DocClaim per fenced block, attributed
	// to the file's subcommand.
	for _, ex := range parseCodeFences(body, rel, commandID) {
		ex.Url = subcmdURL
		ex.Adapter = Name
		out = append(out, ex)
	}

	return out
}

// resolveCommandID picks the most-reliable command path for this
// file. Resolution order:
//
//  1. Filename-derived path (e.g. "container_run.md" + binary="docker"
//     → "docker container run"). This is the most reliable signal
//     when binary_name is configured, because docker/cli's nested
//     files have H1s like "# run" that lack the full path.
//  2. Frontmatter `title:` field — used when binary_name is empty.
//  3. H1 — used when binary_name is empty and no frontmatter exists.
//
// Returns "" if all sources are empty.
func (a *Adapter) resolveCommandID(body []byte, rel string) string {
	if a.binaryName != "" {
		if id := commandPathFromFilename(rel, a.binaryName); id != "" {
			return id
		}
	}
	if m := frontmatterRx.FindSubmatchIndex(body); m != nil {
		fm := body[m[2]:m[3]]
		if tm := titleRx.FindSubmatch(fm); tm != nil {
			if id := normalizeTitle(string(tm[1])); id != "" {
				return id
			}
		}
	}
	stripped := frontmatterRx.ReplaceAll(body, nil)
	if m := h1Rx.FindSubmatch(stripped); m != nil {
		if id := normalizeTitle(string(m[1])); id != "" {
			return id
		}
	}
	return commandPathFromFilename(rel, "")
}

func normalizeTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	if s == "" {
		return ""
	}
	// A title like "docker run" is fine; a title like
	// "Run a command in a new container" isn't a command path —
	// reject it by requiring the first token to be a plausible
	// binary name (alnum + dashes, no spaces in the first token,
	// no embedded punctuation other than dashes).
	first := strings.Fields(s)[0]
	for _, r := range first {
		if !(r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return ""
		}
	}
	return s
}

// commandPathFromFilename derives a command path from the filename,
// optionally prepending binaryName. Underscores become spaces. If
// binaryName is set and the first segment doesn't already match it,
// binaryName is prepended; this lets docker/cli's "container_run.md"
// become "docker container run" while preserving "docker_run.md" if
// some other tool emits already-prefixed names.
//
// Hugo's section convention: when the leaf is `_index.md`, the file
// represents its parent directory rather than itself. Kubernetes
// website ships kubectl reference docs this way:
// kubectl_get/_index.md → "kubectl get",
// kubectl_config/kubectl_config_view.md → "kubectl config view".
// A bare `_index.md` at the docs root has no enclosing command
// directory and returns "" so the adapter can skip it.
func commandPathFromFilename(rel, binaryName string) string {
	base := filepath.Base(rel)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		return ""
	}
	if base == "_index" {
		dir := filepath.Base(filepath.Dir(rel))
		if dir == "" || dir == "." {
			return ""
		}
		base = dir
	}
	parts := strings.Split(base, "_")
	if binaryName != "" && (len(parts) == 0 || parts[0] != binaryName) {
		parts = append([]string{binaryName}, parts...)
	}
	return strings.Join(parts, " ")
}

// urlSlug picks the URL slug for this file based on the configured
// URLStyle. URLStyleCommandPath (default) drops the binary name and
// splits subcommand tokens with "/" — matches docker/cli's docs site
// where `docker container run` is published at ".../container/run/".
// URLStyleFilePath uses the relative path under docs_dir verbatim
// (extension stripped, trailing `_index` stripped, trailing slash
// appended) — matches Hugo/Jekyll sites whose URLs mirror their
// on-disk hierarchy, e.g. kubernetes website where
// `kubectl_get/_index.md` is published at ".../kubectl_get/".
func (a *Adapter) urlSlug(commandID, rel string) string {
	if a.urlStyle == URLStyleFilePath {
		return urlSlugFromFilePath(rel)
	}
	return urlSlugFromCommand(commandID)
}

func urlSlugFromCommand(commandID string) string {
	parts := strings.Fields(commandID)
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[1:], "/") + "/"
}

// urlSlugFromFilePath turns "kubectl_get/_index.md" into
// "kubectl_get/" and "kubectl_config/kubectl_config_view.md" into
// "kubectl_config/kubectl_config_view/". A bare "_index.md" at the
// docs root returns "" (the root URL).
func urlSlugFromFilePath(rel string) string {
	noExt := strings.TrimSuffix(rel, filepath.Ext(rel))
	noExt = strings.TrimSuffix(noExt, "/_index")
	// A bare _index.md at the root has no leading directory; the
	// resulting slug is empty rather than literal "_index/". TrimSuffix
	// is a no-op when the suffix is absent, so no guard is needed.
	noExt = strings.TrimSuffix(noExt, "_index")
	if noExt == "" {
		return ""
	}
	return noExt + "/"
}

// parseFlagTables walks the markdown looking for configured option-
// section headers (e.g. "### Options"), then parses each table that
// follows. Returns one partially-populated DocClaim per flag row;
// the caller fills in URL, adapter, and the source path.
//
// Column positions are auto-detected: when the table's header row
// contains a cell named "Name" (or "Flag" / "Option") and one named
// "Description", those columns are used. When detection fails, the
// adapter's configured NameColumn / DescriptionColumn fall back.
// Default fallback is column-0 / last-column — matching docker/cli.
//
// The Name column may appear as any of:
//
//	`--add-host`
//	`-a`, `--attach`
//	[`--add-host`](#add-host)
//	[`-a`](#attach), [`--attach`](#attach)
//
// We normalize each to "<commandID> --<long-name>", taking the long
// form when both are present.
func (a *Adapter) parseFlagTables(body []byte, commandID string) []*docclaimpb.DocClaim {
	lines := bytes.Split(body, []byte("\n"))
	var out []*docclaimpb.DocClaim

	inFlagSection := false
	inTable := false
	nameCol := a.optionsTable.NameColumn
	descCol := a.optionsTable.DescriptionColumn // -1 sentinel = last
	sawHeader := false
	pendingHeader := []string{} // cells of the row before the separator

	for i, raw := range lines {
		line := bytes.TrimRight(raw, " \t\r")

		// Section headers reset state.
		if bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("#")) {
			text := strings.ToLower(strings.TrimSpace(strings.TrimLeft(string(line), "# ")))
			inFlagSection = false
			for _, name := range a.optionsTable.SectionNames {
				if text == strings.ToLower(name) {
					inFlagSection = true
					break
				}
			}
			inTable = false
			sawHeader = false
			nameCol = a.optionsTable.NameColumn
			descCol = a.optionsTable.DescriptionColumn
			pendingHeader = nil
			continue
		}

		if !inFlagSection {
			continue
		}

		trimmed := bytes.TrimSpace(line)
		// Separator row: opens the table. Use the pending header row
		// (if any) to sniff column positions.
		if bytes.HasPrefix(trimmed, []byte("|")) && bytes.Contains(trimmed, []byte("---")) {
			inTable = true
			if len(pendingHeader) > 0 {
				if nc, dc, ok := sniffColumns(pendingHeader); ok {
					nameCol = nc
					descCol = dc
					sawHeader = true
				}
			}
			continue
		}
		// Non-table line — close the table.
		if !bytes.HasPrefix(trimmed, []byte("|")) {
			inTable = false
			sawHeader = false
			pendingHeader = nil
			continue
		}
		// We're in a `|`-prefixed line. If we haven't seen the
		// separator yet, this is the header row — remember it for
		// sniffing.
		if !inTable {
			pendingHeader = splitTableRow(string(trimmed))
			continue
		}
		_ = sawHeader

		cells := splitTableRow(string(trimmed))
		if len(cells) < 2 {
			continue
		}
		nameIdx := nameCol
		if nameIdx < 0 || nameIdx >= len(cells) {
			nameIdx = 0
		}
		descIdx := descCol
		if descIdx < 0 || descIdx >= len(cells) {
			descIdx = len(cells) - 1
		}
		flagName := extractLongFlag(cells[nameIdx])
		if flagName == "" {
			continue
		}
		desc := cells[descIdx]
		desc = strings.ReplaceAll(desc, "<br>", " ")
		desc = strings.TrimSpace(desc)
		words := countWords([]byte(desc))
		out = append(out, &docclaimpb.DocClaim{
			Location:     &commonpb.SourceLocation{Line: uint32(i + 1)},
			RawText:      truncate(desc, 300),
			ContractRefs: []string{commandID + " " + flagName},
			Substance:    gradeSubstance(words),
			WordCount:    uint32(words),
			Kind:         docclaimpb.DocClaimKind_REFERENCE,
		})
	}
	return out
}

// sniffColumns returns the Name and Description column indices in a
// header row. Recognized name aliases: Name, Flag, Option. Returns
// ok=false when either column can't be located.
func sniffColumns(header []string) (nameCol, descCol int, ok bool) {
	nameCol, descCol = -1, -1
	for i, cell := range header {
		c := strings.ToLower(strings.TrimSpace(cell))
		switch c {
		case "name", "flag", "option":
			if nameCol < 0 {
				nameCol = i
			}
		case "description", "desc", "summary":
			descCol = i
		}
	}
	return nameCol, descCol, nameCol >= 0 && descCol >= 0
}

// splitTableRow splits a markdown table row into trimmed cell values,
// after stripping the leading and trailing pipe.
func splitTableRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	parts := strings.Split(row, "|")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// extractLongFlag pulls "--name" out of various Name-column forms.
// Returns "" if no long-form flag was found.
func extractLongFlag(cell string) string {
	// Strip markdown links: [`--foo`](#foo) → `--foo`
	cell = mdLinkRx.ReplaceAllString(cell, "$1")
	// Each comma-separated alternative is one of: `--name`, `-n`, etc.
	for _, alt := range strings.Split(cell, ",") {
		alt = strings.TrimSpace(alt)
		alt = strings.Trim(alt, "`")
		if strings.HasPrefix(alt, "--") && len(alt) > 2 {
			return alt
		}
	}
	return ""
}

var mdLinkRx = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

// HTML flag-table pattern shipped by kubernetes-sigs/reference-docs
// for every page on kubernetes.io/docs/reference/kubectl/. Each flag
// is two consecutive <tr> rows:
//
//	<tr>
//	<td colspan="2">--flag-name string&nbsp;Default: "x"</td>
//	</tr>
//	<tr>
//	<td></td><td …><p>Description …</p></td>
//	</tr>
//
// Two-letter shorthand variants prepend "-X, ": `-f, --filename strings`.
// Bool switches have no trailing type or default: `--force`.
//
// The regex is intentionally narrow — DOTALL on the description cell
// only, no attempt to handle malformed nesting. Pages that don't
// match return zero claims, which is fine because the pipe-table
// parser still runs.
// The flag-declaration cell allows arbitrary nested tags because
// kubectl docs embed <br /> inside default values (e.g.
// `--reject-paths string ... Default: "^/api/.*/pods/.*/exec,<br />^/api/.*/pods/.*/attach"`).
// Stripping tags happens downstream via htmlTagRx.
var htmlFlagPairRx = regexp.MustCompile(`(?s)<tr>\s*<td\s+colspan="2"[^>]*>(.+?)</td>\s*</tr>\s*<tr>\s*<td[^>]*>\s*</td>\s*<td[^>]*>(.+?)</td>\s*</tr>`)

// flagNameRx pulls the long flag name out of an HTML cell like
// `-f, --filename strings` or `--dry-run string[="unchanged"]`.
var flagNameRx = regexp.MustCompile(`--([A-Za-z][A-Za-z0-9-]*)`)

// htmlTagRx strips remaining markup from the description cell so the
// emitted RawText is plain prose.
var htmlTagRx = regexp.MustCompile(`<[^>]+>`)

// nbspRx and entityRx handle the few HTML entities the kubectl
// reference pages actually emit.
var (
	nbspRx   = regexp.MustCompile(`&nbsp;`)
	entityRx = regexp.MustCompile(`&(amp|quot|lt|gt|#39);`)
)

func parseHTMLFlagTables(body []byte, commandID string) []*docclaimpb.DocClaim {
	matches := htmlFlagPairRx.FindAllSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return nil
	}
	offsets := computeLineOffsets(body)
	out := make([]*docclaimpb.DocClaim, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		decl := body[m[2]:m[3]]
		nm := flagNameRx.FindSubmatch(decl)
		if nm == nil {
			continue
		}
		flag := string(nm[1])
		ref := commandID + " --" + flag
		if _, dup := seen[ref]; dup {
			// Some pages list inherited options below local ones with
			// the same flag name; one DocClaim per flag is enough.
			continue
		}
		seen[ref] = struct{}{}

		descRaw := body[m[4]:m[5]]
		desc := htmlTagRx.ReplaceAll(descRaw, []byte(""))
		desc = nbspRx.ReplaceAll(desc, []byte(" "))
		desc = entityRx.ReplaceAllFunc(desc, func(b []byte) []byte {
			switch string(b) {
			case "&amp;":
				return []byte("&")
			case "&quot;":
				return []byte("\"")
			case "&lt;":
				return []byte("<")
			case "&gt;":
				return []byte(">")
			case "&#39;":
				return []byte("'")
			}
			return b
		})
		descText := strings.TrimSpace(string(desc))

		out = append(out, &docclaimpb.DocClaim{
			Location:     &commonpb.SourceLocation{Line: uint32(lineFromOffset(offsets, m[2]))},
			RawText:      truncate(descText, 300),
			ContractRefs: []string{ref},
			Substance:    gradeSubstance(countWords([]byte(descText))),
			WordCount:    uint32(countWords([]byte(descText))),
			Kind:         docclaimpb.DocClaimKind_REFERENCE,
		})
	}
	return out
}

// parseCodeFences emits EXAMPLE-kind DocClaims per ``` fenced block.
// Two passes per fence:
//
//  1. One claim attributed to the file's subcommand — the "this page
//     shows worked examples" signal. (Existing behavior.)
//  2. One claim per unique `--flag-name` literal occurrence in the
//     fence body, attributed to "<commandID> --<flag-name>" — the
//     "the agent has seen this specific flag in use" signal. Without
//     this, the per-flag Worked-Examples cell of the masthead is
//     forced to read 0% even when the docs page contains a code
//     fence that exercises the flag.
//
// The per-flag scan is intentionally liberal: it emits claims for
// every `--foo` it sees and trusts the indexer's join to drop the
// ones that don't map to a real element. False positives here are
// cheap (the claim drops on the floor); false negatives are not
// (the per-flag KPI rounds down).
//
// Skips empty fences. Does not skip "console" output — the heuristic
// from earlier versions over-trimmed real demos like
// `echo '{...}' | kubectl apply -f -`.
func parseCodeFences(body []byte, rel, commandID string) []*docclaimpb.DocClaim {
	matches := codeFenceWithPosRx.FindAllSubmatchIndex(body, -1)
	offsets := computeLineOffsets(body)
	var out []*docclaimpb.DocClaim
	for _, m := range matches {
		inner := bytes.TrimSpace(body[m[4]:m[5]])
		if len(inner) == 0 {
			continue
		}
		line := lineFromOffset(offsets, m[0])
		words := countWords(inner)
		// Pass 1: per-command example claim.
		out = append(out, &docclaimpb.DocClaim{
			SourcePath:   rel,
			Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
			RawText:      truncate(string(inner), 300),
			ContractRefs: []string{commandID},
			Substance:    gradeSubstance(words),
			WordCount:    uint32(words),
			Kind:         docclaimpb.DocClaimKind_EXAMPLE,
		})
		// Pass 2: per-flag example claims. Walk every --flag literal
		// in the fence; emit one EXAMPLE per unique flag, attributed
		// to "<commandID> --<flag>". Dedup within the fence so a
		// fence that mentions --filename three times still emits one
		// claim. Cross-fence dedup happens later via the indexer.
		seenFlags := map[string]bool{}
		for _, fm := range flagInFenceRx.FindAllSubmatch(inner, -1) {
			flag := string(fm[1])
			if seenFlags[flag] {
				continue
			}
			seenFlags[flag] = true
			out = append(out, &docclaimpb.DocClaim{
				SourcePath:   rel,
				Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
				RawText:      truncate(string(inner), 300),
				ContractRefs: []string{commandID + " --" + flag},
				Substance:    gradeSubstance(words),
				WordCount:    uint32(words),
				Kind:         docclaimpb.DocClaimKind_EXAMPLE,
			})
		}
	}
	return out
}

// flagInFenceRx matches `--name`, `--name-with-dashes` occurrences in
// a fence body. Excludes `--` (stdin marker) by requiring at least
// one letter after the dashes. Lowercase + dash only — matches the
// long-form flag convention used by every cobra-style CLI; doesn't
// chase short flags (`-f`) because mapping short → long requires the
// element side's shorthand metadata.
var flagInFenceRx = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)

var codeFenceWithPosRx = regexp.MustCompile("(?s)```([a-zA-Z0-9_-]*)\n(.*?)```")

func computeLineOffsets(body []byte) []int {
	offsets := []int{0}
	for i, b := range body {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

func lineFromOffset(offsets []int, off int) int {
	lo, hi := 0, len(offsets)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if offsets[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

func proseOnly(body []byte) []byte {
	body = frontmatterRx.ReplaceAll(body, nil)
	body = htmlCommentRx.ReplaceAll(body, nil)
	body = codeFenceRx.ReplaceAll(body, nil)
	body = tableLineRx.ReplaceAll(body, nil)
	// Strip H1/H2/H3 markup so the headers themselves don't pad the
	// word count, but keep the header text.
	var out bytes.Buffer
	for _, line := range bytes.Split(body, []byte("\n")) {
		trimmed := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trimmed, []byte("#")) {
			// Drop leading '#' chars and one trailing space.
			stripped := bytes.TrimLeft(trimmed, "#")
			out.Write(bytes.TrimSpace(stripped))
			out.WriteByte('\n')
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func countWords(b []byte) int {
	return len(strings.Fields(string(b)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func gradeSubstance(words int) commonpb.Substance {
	switch {
	case words == 0:
		return commonpb.Substance_ABSENT
	case words <= 1:
		return commonpb.Substance_SIGNATURE_ONLY
	case words <= 7:
		return commonpb.Substance_PARTIAL
	default:
		return commonpb.Substance_SUBSTANTIVE
	}
}

func refOrEmpty(c *docclaimpb.DocClaim) string {
	if len(c.GetContractRefs()) == 0 {
		return ""
	}
	return c.GetContractRefs()[0]
}
