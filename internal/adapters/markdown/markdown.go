// Package markdown implements a doc parser over Markdown files.
//
// Emits DocClaims for two things:
//   - Qualified mentions of contract elements in prose text
//     (heuristic: backticked identifiers that look like Proto.Method,
//     Proto::Method, or `binary subcommand`).
//   - Fenced code blocks whose language tag is in the configured
//     allowlist (extracted as EXAMPLE-kind claims).
//
// The cross-reference of "qualified mention" → ContractElement ID is
// completed by the indexer; this adapter just produces the candidate
// excerpts and the indexer matches.
package markdown

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/climatch"
	"github.com/sheaf-data/sheaf/internal/cppusage"
	"github.com/sheaf-data/sheaf/internal/fidlmatch"
	"github.com/sheaf-data/sheaf/internal/protomatch"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "markdown"
const Version = "0.1.0"

type Parser struct {
	include            []string
	exclude            []string
	codeBlockLanguages map[string]bool

	// regex for backticked qualified identifiers we'll surface as mentions.
	mentionRx *regexp.Regexp
	// regex for fenced code blocks; captures language and body.
	codeFenceRx *regexp.Regexp
	// Matcher used to extract FIDL refs from code blocks.
	matcher *fidlmatch.Matcher
	// Complements matcher with plain-C++ API usage in code blocks.
	cppUsage *cppusage.Extractor
}

type Config struct {
	Include            []string
	Exclude            []string
	CodeBlockLanguages []string
	// IDLPrefix selects the fidlmatch Matcher for code-block FIDL
	// extraction. Empty = "fuchsia" (default).
	IDLPrefix string
}

func New(cfg Config) *Parser {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.md"}
	}
	langs := make(map[string]bool, len(cfg.CodeBlockLanguages))
	for _, l := range cfg.CodeBlockLanguages {
		langs[strings.ToLower(l)] = true
	}
	return &Parser{
		include:            include,
		exclude:            cfg.Exclude,
		codeBlockLanguages: langs,
		// Backticked tokens: `foo.Bar` `foo::Bar` `binary subcommand sub`
		// (multi-word backticked sequences containing alpha + . / :: / space).
		mentionRx: regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_.:/ -]{2,80})`"),
		// Allow up to 3 leading spaces of indent before the fence per
		// CommonMark — grpc docs put many fences inside list items
		// with 2- or 4-space indentation. The closing ``` may also
		// be indented; we allow any whitespace prefix. `[^\n]*` after
		// the language token consumes the CommonMark info string —
		// e.g. Fuchsia devsite's ```posix-terminal {:.devsite-…} — so
		// the language still captures cleanly and the fence still matches.
		codeFenceRx: regexp.MustCompile("(?ms)^ {0,3}```([a-zA-Z0-9_+-]*)[^\\n]*\\n(.*?)\\n[ \\t]*```"),
		matcher:     fidlmatch.NewMatcher(fidlmatch.Config{IDLPrefix: cfg.IDLPrefix}),
		cppUsage:    cppusage.New(cfg.IDLPrefix),
	}
}

func (p *Parser) Name() string               { return Name }
func (p *Parser) Version() string            { return Version }
func (p *Parser) SupportedFormats() []string { return []string{"markdown"} }

func (p *Parser) Parse(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*docclaimpb.DocClaim, error) {
	var out []*docclaimpb.DocClaim
	// CLI-shaped scope libraries (bare binary names like "ffx", "kubectl")
	// let us pull `<binary> <subcommand>` invocations out of shell/terminal
	// example fences and attribute them to command elements — the CLI
	// analogue of the fidlmatch/protomatch extraction below.
	cliBinaries := cliBinariesFromScope(scope)
	err := adapters.WalkMatching(repoRoot, p.include, p.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("markdown: read %s: %w", rel, err)
		}
		offsets := computeLineOffsets(body)

		// 1. Code blocks first — we'll strip them from prose before
		//    extracting mentions so we don't double-count code-block
		//    identifiers as prose mentions.
		blocks := p.codeFenceRx.FindAllSubmatchIndex(body, -1)
		stripped := stripRanges(body, blocks)

		// Pre-compute the H1/H2/H3 heading stack at every byte
		// offset in the document, ignoring # lines inside fenced
		// code blocks. sectionPathAt(off) returns the live heading
		// stack we stamp on each emitted claim.
		sectionPathAt := buildSectionIndex(body, blocks)
		for _, b := range blocks {
			lang := strings.ToLower(string(body[b[2]:b[3]]))
			content := body[b[4]:b[5]]
			// Always run protomatch — it recognizes proto/gRPC patterns
			// (`package`, `service`, `rpc`, FQDN, grpcurl `/Method`) and
			// is shape-strict, so it costs essentially nothing on
			// non-proto blocks. Catches the case where gRPC docs put
			// proto schemas in untagged or mis-tagged code fences.
			protoRefs := protomatch.Extract(string(content))
			if p.codeBlockLanguages[lang] {
				line := lineFromOffset(offsets, b[0])
				excerpt := string(content)
				if len(excerpt) > 300 {
					excerpt = excerpt[:300] + "…"
				}
				// Run fidlmatch over the code block so this example
				// can be attributed to specific FIDL method(s). The
				// language tag tells us which matcher to use; unknown
				// languages emit no contract refs.
				var refs []string
				switch lang {
				case "cpp", "c++", "cc":
					refs = p.matcher.Extract(string(content), "cpp",
						p.matcher.CPPIncludeLibraries(string(content)))
					// Plain-C++ API usage (macros, prefix-qualified names)
					// so a cppheader library's fenced examples bridge too.
					refs = mergeRefs(refs, p.cppUsage.Extract(string(content)))
				case "rust", "rs":
					refs = p.matcher.Extract(string(content), "rust",
						p.matcher.RustUseLibraries(string(content)))
				}
				refs = mergeRefs(refs, protoRefs)
				// Shell/terminal example fences (posix-terminal, sh, bash,
				// console, …) carry `<binary> <subcommand>` invocations; pull
				// the command paths so the example attributes to the CLI
				// element. No-op when scope has no CLI-shaped library.
				refs = mergeRefs(refs, extractCLIExampleRefs(string(content), cliBinaries))
				out = append(out, &docclaimpb.DocClaim{
					SourcePath:   rel,
					Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
					RawText:      excerpt,
					WordCount:    uint32(countWords(string(content))),
					Substance:    commonpb.Substance_SUBSTANCE_UNSPECIFIED,
					Kind:         docclaimpb.DocClaimKind_EXAMPLE,
					Adapter:      Name,
					ContractRefs: refs,
					SectionPath:  sectionPathAt(b[0]),
				})
			} else if len(protoRefs) > 0 {
				// Block isn't in the configured EXAMPLE allowlist, but
				// protomatch found contract refs in it. Emit a
				// REFERENCE-kind claim so the join hits without
				// claiming this snippet as a "working example."
				line := lineFromOffset(offsets, b[0])
				excerpt := string(content)
				if len(excerpt) > 300 {
					excerpt = excerpt[:300] + "…"
				}
				out = append(out, &docclaimpb.DocClaim{
					SourcePath:   rel,
					Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
					RawText:      excerpt,
					WordCount:    uint32(countWords(string(content))),
					Substance:    classifySubstance(excerpt),
					Kind:         docclaimpb.DocClaimKind_REFERENCE,
					Adapter:      Name,
					ContractRefs: protoRefs,
					SectionPath:  sectionPathAt(b[0]),
				})
			}
		}

		// 2. Prose mentions.
		mentions := p.mentionRx.FindAllSubmatchIndex(stripped, -1)
		for _, m := range mentions {
			text := string(stripped[m[2]:m[3]])
			if !looksQualified(text) {
				continue
			}
			line := lineFromOffset(offsets, m[0])
			excerptStart := max0(m[0] - 80)
			excerptEnd := min(len(stripped), m[1]+80)
			excerpt := string(stripped[excerptStart:excerptEnd])
			out = append(out, &docclaimpb.DocClaim{
				SourcePath:   rel,
				Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
				RawText:      excerpt,
				WordCount:    uint32(countWords(excerpt)),
				Substance:    classifySubstance(excerpt),
				Kind:         docclaimpb.DocClaimKind_PROSE_MENTION,
				ContractRefs: []string{text}, // candidate; indexer canonicalizes
				Adapter:      Name,
				SectionPath:  sectionPathAt(m[0]),
			})
		}

		// 3. CLI-flag mentions. README and CHANGELOG prose plus fenced
		//    code examples routinely reference flags by their long form
		//    ("--hidden", "--exec-batch"). The clap / cobra / argh
		//    adapters emit each flag's long form as an alias on the
		//    corresponding element, so a bare `--flag` ref attributes
		//    directly via Strategy 1. We scan the FULL body (not the
		//    code-block-stripped version), since command-line examples
		//    inside ``` blocks are exactly where flags appear.
		seenFlag := make(map[int]bool) // start offset → already emitted
		for _, m := range reFlagMention.FindAllSubmatchIndex(body, -1) {
			if seenFlag[m[2]] {
				continue
			}
			seenFlag[m[2]] = true
			flag := string(body[m[2]:m[3]])
			line := lineFromOffset(offsets, m[2])
			excerptStart := max0(m[2] - 80)
			excerptEnd := min(len(body), m[3]+80)
			excerpt := string(body[excerptStart:excerptEnd])
			out = append(out, &docclaimpb.DocClaim{
				SourcePath:   rel,
				Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
				RawText:      excerpt,
				WordCount:    uint32(countWords(excerpt)),
				Substance:    classifySubstance(excerpt),
				Kind:         docclaimpb.DocClaimKind_PROSE_MENTION,
				ContractRefs: []string{flag},
				Adapter:      Name,
				SectionPath:  sectionPathAt(m[2]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// reFlagMention matches long-form CLI flags ("--name") with a word
// boundary on each side so we don't pick up "--foo" inside a larger
// run of hyphens or `---` horizontal rules. Captures the flag itself.
var reFlagMention = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(--[a-z][a-z0-9-]*[a-z0-9])(?:[^A-Za-z0-9_-]|$)`)

// looksQualified accepts strings that have at least one of `.` `::`
// or a space (suggesting a multi-token qualified identifier).
func looksQualified(s string) bool {
	if strings.Contains(s, ".") {
		return true
	}
	if strings.Contains(s, "::") {
		return true
	}
	if strings.Contains(s, " ") {
		return true
	}
	return false
}

// classifySubstance grades the surrounding prose. Uses the
// fuchsia.io-calibrated 5/20 thresholds as defaults; the per-(ecosystem,
// kind) overrides happen at indexer time when we know the element kind.
func classifySubstance(s string) commonpb.Substance {
	wc := countWords(s)
	switch {
	case wc <= 4:
		return commonpb.Substance_SIGNATURE_ONLY
	case wc <= 19:
		return commonpb.Substance_PARTIAL
	default:
		return commonpb.Substance_SUBSTANTIVE
	}
}

func countWords(s string) int {
	return len(strings.Fields(s))
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// mergeRefs returns a-then-b with duplicates removed, preserving
// first-seen order. Used to merge fidlmatch and protomatch outputs
// on a code block before stamping ContractRefs.
func mergeRefs(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// cliBinariesFromScope returns scope libraries that look like CLI binary
// names — a single bareword with no "." or "/" (so "ffx"/"kubectl" qualify
// but "fuchsia.driver.framework" / "google/protobuf" don't). These drive
// `<binary> <subcommand>` extraction from shell example fences.
func cliBinariesFromScope(scope adapters.ScopeConfig) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range append(append([]string{}, scope.Libraries...), scope.AlsoInclude...) {
		l = strings.TrimSpace(l)
		if l == "" || strings.ContainsAny(l, "./") || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// extractCLIExampleRefs pulls `<binary> <subcommand>` command paths out of a
// fenced code block. It emits every 2+-deep prefix (mirroring the workflows
// adapter) so the indexer's longest-prefix join attributes the example to
// the deepest command element that actually exists.
func extractCLIExampleRefs(block string, binaries []string) []string {
	if len(binaries) == 0 {
		return nil
	}
	var refs []string
	for _, line := range strings.Split(block, "\n") {
		for _, bin := range binaries {
			if p := climatch.InvocationRef(line, bin); p != "" {
				refs = append(refs, climatch.Prefixes(p, 2)...)
				// Credit flag elements named on this line at the
				// most-specific command path (e.g.
				// "ffx audio gen sine --duration"). Flows through the
				// same EXAMPLE-claim path as the command prefixes.
				refs = append(refs, climatch.FlagRefs(line, bin, p)...)
			}
		}
	}
	return dedupStrings(refs)
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// stripRanges returns body with all [start,end) ranges replaced by
// spaces of equal length, preserving offsets so line lookups still
// work on the result.
func stripRanges(body []byte, ranges [][]int) []byte {
	if len(ranges) == 0 {
		return body
	}
	out := make([]byte, len(body))
	copy(out, body)
	for _, r := range ranges {
		for i := r[0]; i < r[1] && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return out
}

func computeLineOffsets(body []byte) []int {
	offsets := []int{0}
	for i, b := range body {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

// buildSectionIndex scans body for H1/H2/H3 headings outside fenced
// code blocks and returns a function that, for any byte offset,
// returns the heading stack live at that point (outermost first).
// H4+ headings are tracked as updates to the H3 slot — they roll
// up to the nearest tracked level so deep nesting still narrows
// the section_path appropriately.
//
// The returned function trims trailing empty slots so a claim
// under an H2 with no H1 returns ["", "<h2>"] becomes ["<h2>"]
// (callers see a length-1 slice). Heading text is the verbatim
// text after the # run, trimmed of surrounding whitespace and
// trailing closing-# markers.
func buildSectionIndex(body []byte, _ [][]int) func(off int) []string {
	type ev struct {
		off   int
		level int // 1, 2, 3
		text  string
	}
	var events []ev
	// Toggle-based fence detection: walk line by line, flipping
	// inFence whenever we see a ``` (or ~~~) opener/closer with up
	// to 3 leading spaces. Simpler and more robust than relying on
	// the prose-stripping codeFenceRx, which pairs ``` markers
	// greedily in either direction depending on how the
	// language-tagged vs untagged fences alternate.
	inFence := false
	fenceLine := func(line []byte) bool {
		i := 0
		for i < len(line) && i < 3 && (line[i] == ' ') {
			i++
		}
		if i+3 > len(line) {
			return false
		}
		return (line[i] == '`' && line[i+1] == '`' && line[i+2] == '`') ||
			(line[i] == '~' && line[i+1] == '~' && line[i+2] == '~')
	}
	off := 0
	for off < len(body) {
		end := off
		for end < len(body) && body[end] != '\n' {
			end++
		}
		line := body[off:end]
		if fenceLine(line) {
			inFence = !inFence
		} else if !inFence {
			// ATX heading detection inline so we keep one O(n)
			// pass over the document.
			i := 0
			for i < len(line) && i < 3 && line[i] == ' ' {
				i++
			}
			hashStart := i
			for i < len(line) && line[i] == '#' {
				i++
			}
			hashCount := i - hashStart
			if hashCount >= 1 && hashCount <= 6 && i < len(line) && (line[i] == ' ' || line[i] == '\t') {
				// Skip whitespace before heading text.
				for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
					i++
				}
				text := strings.TrimRight(string(line[i:]), " \t")
				text = strings.TrimRight(text, "#")
				text = strings.TrimSpace(text)
				if text != "" {
					level := hashCount
					if level > 3 {
						level = 3
					}
					events = append(events, ev{off: off, level: level, text: text})
				}
			}
		}
		off = end + 1
	}
	if len(events) == 0 {
		return func(int) []string { return nil }
	}
	return func(off int) []string {
		var stack [3]string
		for _, e := range events {
			if e.off > off {
				break
			}
			stack[e.level-1] = e.text
			// Clear deeper slots whenever a higher-level heading
			// appears (h2 resets h3; h1 resets h2 + h3).
			for i := e.level; i < 3; i++ {
				stack[i] = ""
			}
		}
		// Pack non-empty entries in stack order.
		out := make([]string, 0, 3)
		for _, s := range stack {
			if s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
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
