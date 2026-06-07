// Package rst implements a doc parser over reStructuredText files.
//
// Mirrors the markdown adapter: walks .rst files, tracks the heading
// stack, emits one DocClaim per substantive mention with section_path
// populated. The narrow rST subset sheaf actually cares about:
//
//   - Underline-style headings (Title\n=====\n etc.). Level assignment
//     follows the document's first-use order — the first new underline
//     char becomes H1, the second new char H2, etc. Overlined headings
//     (===\nTitle\n===) are handled too.
//   - `.. code-block:: <lang>` / `.. sourcecode::` / `.. code::`
//     directives. Body is whatever lines are indented past the
//     directive's own indent.
//   - Inline “double-backtick“ literals as candidate prose mentions.
//   - Sphinx-style inline roles — :role:`text` and :domain:type:`text`
//     (e.g. :py:class:`foo.Bar`, :ref:`label <target>`). The optional
//     `<target>` form takes precedence over the visible label; protomatch
//     also runs over the role content so envoy-style :repo: paths and
//     dotted FQDNs surface as proto refs.
//   - Other Sphinx directive lines — `.. http:post:: /pkg.Svc/Method`,
//     `.. grpc:method:: ...`, etc. The argument after `::` is run
//     through protomatch so grpcurl-style references bridge.
//   - CLI flag mentions (--name) scanned over the full body — same
//     idiom the markdown adapter uses.
package rst

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/cppusage"
	"github.com/sheaf-data/sheaf/internal/fidlmatch"
	"github.com/sheaf-data/sheaf/internal/protomatch"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "rst"
const Version = "0.1.0"

type Parser struct {
	include            []string
	exclude            []string
	codeBlockLanguages map[string]bool
	idlPrefix          string

	inlineLiteralRx *regexp.Regexp
	sphinxRoleRx    *regexp.Regexp
	matcher         *fidlmatch.Matcher
	cppUsage        *cppusage.Extractor
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
		include = []string{"**/*.rst"}
	}
	langs := make(map[string]bool, len(cfg.CodeBlockLanguages))
	for _, l := range cfg.CodeBlockLanguages {
		langs[strings.ToLower(l)] = true
	}
	return &Parser{
		include:            include,
		exclude:            cfg.Exclude,
		codeBlockLanguages: langs,
		idlPrefix:          cfg.IDLPrefix,
		// Double-backtick inline literal: ``foo.Bar`` / ``foo::Bar`` /
		// ``binary subcommand``. Same shape as the markdown mentionRx.
		inlineLiteralRx: regexp.MustCompile("``([A-Za-z_][A-Za-z0-9_.:/ -]{2,80})``"),
		// Sphinx inline role: :role:`text` or :domain:type:`text`.
		// We capture (1) the role name (for telemetry / future
		// filtering) and (2) the backticked content. The role-name
		// pattern allows two colon-separated segments — covering both
		// short roles (:ref:, :doc:, :repo:) and Sphinx-domain roles
		// (:py:class:, :http:get:, :cpp:func:). The content is a
		// single-backtick run so the regex doesn't collide with the
		// double-backtick literal pass above.
		sphinxRoleRx: regexp.MustCompile(":([a-z][a-z0-9_+-]*(?::[a-z][a-z0-9_+-]*)?):" + "`([^`]+)`"),
		matcher:      fidlmatch.NewMatcher(fidlmatch.Config{IDLPrefix: cfg.IDLPrefix}),
		cppUsage:     cppusage.New(cfg.IDLPrefix),
	}
}

func (p *Parser) Name() string               { return Name }
func (p *Parser) Version() string            { return Version }
func (p *Parser) SupportedFormats() []string { return []string{"rst", "restructuredtext"} }

func (p *Parser) Parse(ctx context.Context, repoRoot string, _ adapters.ScopeConfig) ([]*docclaimpb.DocClaim, error) {
	var out []*docclaimpb.DocClaim
	err := adapters.WalkMatching(repoRoot, p.include, p.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("rst: read %s: %w", rel, err)
		}
		offsets := computeLineOffsets(body)

		blocks, sectionPathAt := parseStructure(body)

		// Strip code-block ranges from prose so identifiers that
		// appear inside a code block don't double-count as prose
		// mentions.
		blockRanges := make([][]int, 0, len(blocks))
		for _, b := range blocks {
			blockRanges = append(blockRanges, []int{b.start, b.contentEnd})
		}
		stripped := stripRanges(body, blockRanges)

		for _, blk := range blocks {
			lang := blk.lang
			content := body[blk.contentStart:blk.contentEnd]
			protoRefs := protomatch.Extract(string(content))
			if p.codeBlockLanguages[lang] {
				line := lineFromOffset(offsets, blk.start)
				excerpt := string(content)
				if len(excerpt) > 300 {
					excerpt = excerpt[:300] + "…"
				}
				var refs []string
				switch lang {
				case "cpp", "c++", "cc":
					refs = p.matcher.Extract(string(content), "cpp",
						p.matcher.CPPIncludeLibraries(string(content)))
					// fidlmatch covers FIDL-protocol shapes; add plain-C++
					// API usage (macros, prefix-qualified names) so a
					// cppheader library's code-block examples bridge too.
					refs = mergeRefs(refs, p.cppUsage.Extract(string(content)))
				case "rust", "rs":
					refs = p.matcher.Extract(string(content), "rust",
						p.matcher.RustUseLibraries(string(content)))
				}
				refs = mergeRefs(refs, protoRefs)
				out = append(out, &docclaimpb.DocClaim{
					SourcePath:   rel,
					Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
					RawText:      excerpt,
					WordCount:    uint32(countWords(string(content))),
					Substance:    commonpb.Substance_SUBSTANCE_UNSPECIFIED,
					Kind:         docclaimpb.DocClaimKind_EXAMPLE,
					Adapter:      Name,
					ContractRefs: refs,
					SectionPath:  sectionPathAt(blk.start),
				})
			} else if len(protoRefs) > 0 {
				line := lineFromOffset(offsets, blk.start)
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
					SectionPath:  sectionPathAt(blk.start),
				})
			}
		}

		// Prose mentions (inline literals).
		mentions := p.inlineLiteralRx.FindAllSubmatchIndex(stripped, -1)
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
				ContractRefs: []string{text},
				Adapter:      Name,
				SectionPath:  sectionPathAt(m[0]),
			})
		}

		// Sphinx inline roles: :role:`text` and :domain:type:`text`.
		// Two forms to handle:
		//   1. Bare form `:role:`<content>`` — content is the reference.
		//   2. Angle-bracket form `:role:`<label> <target>`` — both the
		//      visible label and the angle-bracket target can carry a
		//      contract ref. envoy's :ref: roles use the visible label
		//      as the dotted FQDN (e.g. `envoy.config.listener.v3.Listener.name`)
		//      and the angle-bracket target as Sphinx's internal anchor
		//      slug (e.g. `envoy_v3_api_field_config.listener.v3...`).
		//      Other roles like :repo: do it the other way around
		//      (visible label is a filename, target is the repo path).
		//      Try BOTH; protomatch runs over the combined text so
		//      dotted FQDNs surface as proto IDs.
		roles := p.sphinxRoleRx.FindAllSubmatchIndex(stripped, -1)
		for _, m := range roles {
			raw := string(stripped[m[4]:m[5]])
			label, target := splitRoleContent(raw)
			var refs []string
			if looksQualified(label) {
				refs = append(refs, label)
			}
			if target != "" && target != label && looksQualified(target) {
				refs = append(refs, target)
			}
			protoRefs := protomatch.Extract(label)
			if target != "" {
				protoRefs = mergeRefs(protoRefs, protomatch.Extract(target))
				// Sphinx anchor slugs (e.g. envoy's
				// `envoy_v3_api_msg_config.listener.v3.Listener`)
				// encode the canonical FQDN with the project prefix
				// stripped and a kind tag inserted. Decode back to
				// the bare FQDN so protomatch can extract it.
				if decoded := sphinxLabelDecode(target, p.idlPrefix); decoded != "" {
					protoRefs = mergeRefs(protoRefs, protomatch.Extract(decoded))
				}
			}
			refs = mergeRefs(refs, protoRefs)
			if len(refs) == 0 {
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
				ContractRefs: refs,
				Adapter:      Name,
				SectionPath:  sectionPathAt(m[0]),
			})
		}

		// Sphinx directives (other than code-block / sourcecode / code,
		// which are already consumed as code blocks). Lines of the
		// form `.. domain:method:: <arg>` are scanned for proto refs
		// in <arg> — covers `.. http:post:: /pkg.Svc/Method` and
		// `.. grpc:method:: pkg.Svc.Method` and the like.
		for _, ln := range splitLines(body) {
			if inBlockRange(blocks, ln.off) {
				continue
			}
			trimmed := strings.TrimRight(ln.text, " \t")
			if reCodeDirective.MatchString(trimmed) {
				continue
			}
			dm := reSphinxDirective.FindStringSubmatch(trimmed)
			if dm == nil {
				continue
			}
			arg := strings.TrimSpace(dm[3])
			protoRefs := protomatch.Extract(arg)
			if len(protoRefs) == 0 {
				continue
			}
			line := lineFromOffset(offsets, ln.off)
			excerpt := trimmed
			out = append(out, &docclaimpb.DocClaim{
				SourcePath:   rel,
				Location:     &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
				RawText:      excerpt,
				WordCount:    uint32(countWords(excerpt)),
				Substance:    classifySubstance(excerpt),
				Kind:         docclaimpb.DocClaimKind_PROSE_MENTION,
				ContractRefs: protoRefs,
				Adapter:      Name,
				SectionPath:  sectionPathAt(ln.off),
			})
		}

		// CLI-flag mentions over the FULL body — code-block command
		// examples are exactly where flags show up.
		seenFlag := make(map[int]bool)
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

var reFlagMention = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(--[a-z][a-z0-9-]*[a-z0-9])(?:[^A-Za-z0-9_-]|$)`)

// reCodeDirective matches a rST code-block directive opener. Captures
// the leading indent and (optional) language token.
var reCodeDirective = regexp.MustCompile(`^(\s*)\.\. (?:code-block|sourcecode|code)::\s*(\S*)\s*$`)

// reSphinxDirective matches any other `.. domain:method:: <arg>` line.
// Captures (1) leading indent, (2) directive name, (3) argument.
// Excludes the bare `.. ` (comment) form by requiring `::` after the
// directive name.
var reSphinxDirective = regexp.MustCompile(`^(\s*)\.\. ([a-z][a-z0-9_:-]*)::\s*(\S.*?)\s*$`)

// sphinxLabelDecode reverses the Sphinx-anchor encoding many proto
// projects (notably envoy) use for cross-reference labels. The encoded
// form is `<idlPrefix>_<ver>_api_<kind>_<rest>` where:
//
//   - idlPrefix is the project's namespace ("envoy"),
//   - ver is a version namespace ("v3", "v2alpha"),
//   - kind is the Sphinx domain object kind ("msg", "field", "enum",
//     "enum_value", "file"),
//   - rest is the canonical dotted FQDN with the project's leading
//     namespace stripped (e.g. "config.listener.v3.Listener.name").
//
// Returns the reconstructed FQDN ("<idlPrefix>.<rest>") so protomatch
// can extract it the same way it extracts any bare-FQDN mention.
// Returns "" when the input doesn't match the expected shape or
// idlPrefix is empty.
//
// Example: envoy_v3_api_msg_config.listener.v3.Listener with idlPrefix
// "envoy" → envoy.config.listener.v3.Listener.
func sphinxLabelDecode(target, idlPrefix string) string {
	if idlPrefix == "" {
		return ""
	}
	pref := idlPrefix + "_"
	if !strings.HasPrefix(target, pref) {
		return ""
	}
	rest := target[len(pref):]
	// Strip the version token (e.g. "v3_") — must end in a digit
	// (or "alpha"/"beta") to avoid eating real path components.
	verEnd := strings.IndexByte(rest, '_')
	if verEnd <= 0 {
		return ""
	}
	ver := rest[:verEnd]
	if !looksLikeVersion(ver) {
		return ""
	}
	rest = rest[verEnd+1:]
	// Must be the literal "api_" separator now.
	const apiSep = "api_"
	if !strings.HasPrefix(rest, apiSep) {
		return ""
	}
	rest = rest[len(apiSep):]
	// Strip the kind token (msg / field / enum / enum_value / file).
	// For multi-token kinds like "enum_value" we accept the longest
	// known prefix; fall through to the first underscore otherwise.
	for _, kind := range []string{"enum_value", "msg_field", "msg", "field", "enum", "file"} {
		if strings.HasPrefix(rest, kind+"_") {
			rest = rest[len(kind)+1:]
			return idlPrefix + "." + rest
		}
	}
	return ""
}

// looksLikeVersion is true for tokens like "v3", "v2alpha", "v1beta1".
func looksLikeVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, c := range s[1:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

// splitRoleContent parses Sphinx role content into the visible label
// and the optional angle-bracket target. For `:ref:`label <target>“
// returns (label, target); for the bare `:ref:`content“ form returns
// (content, ""). Both halves can carry a contract ref depending on
// the role's convention — see the role-handling loop in Parse.
func splitRoleContent(text string) (label, target string) {
	text = strings.TrimSpace(text)
	if !strings.HasSuffix(text, ">") {
		return text, ""
	}
	i := strings.LastIndex(text, "<")
	if i <= 0 {
		return text, ""
	}
	label = strings.TrimSpace(text[:i])
	target = strings.TrimSpace(text[i+1 : len(text)-1])
	return label, target
}

// inBlockRange returns true when off falls inside any code block in
// blocks. Used by the Sphinx directive pass to skip lines that are
// already part of an emitted EXAMPLE.
func inBlockRange(blocks []codeBlock, off int) bool {
	for _, b := range blocks {
		if off >= b.start && off < b.contentEnd {
			return true
		}
	}
	return false
}

// underlineChars is the canonical set of rST section adornment chars.
// We only care about which char was seen, not its conventional meaning
// (rST itself derives level from first-seen order, not from char).
const underlineChars = `=-~^"'` + "`" + `:.+*#_`

// codeBlock is one detected `.. code-block::` (or sourcecode / code)
// directive plus its indented content range. start points at the
// directive line; contentStart..contentEnd brackets the body bytes.
type codeBlock struct {
	start        int
	contentStart int
	contentEnd   int
	lang         string
}

type srcLine struct {
	text string
	off  int
}

// parseStructure does one O(n) walk to collect code-block ranges and
// heading events, then returns a sectionPathAt closure built over the
// events. Headings inside code-block ranges are ignored.
func parseStructure(body []byte) ([]codeBlock, func(int) []string) {
	type headingEvent struct {
		off   int
		level int
		text  string
	}
	var (
		blocks      []codeBlock
		events      []headingEvent
		levelByChar = map[byte]int{}
		nextLevel   = 1
	)
	lines := splitLines(body)

	registerHeading := func(off int, ch byte, text string) {
		level, ok := levelByChar[ch]
		if !ok {
			level = nextLevel
			levelByChar[ch] = level
			nextLevel++
		}
		if level > 3 {
			level = 3
		}
		events = append(events, headingEvent{off: off, level: level, text: text})
	}

	i := 0
	for i < len(lines) {
		ln := lines[i]
		indent := leadingSpaces(ln.text)
		trimmedRight := strings.TrimRight(ln.text, " \t")

		// Code-block directive.
		if m := reCodeDirective.FindStringSubmatch(trimmedRight); m != nil {
			directiveIndent := indent
			lang := strings.ToLower(m[2])
			j := i + 1
			// Skip option lines (`:name:` or `:name: value` indented
			// past the directive) and blank lines until the real body
			// starts. Strict: the `:name:` form must be followed by
			// EOL or whitespace — otherwise content like a Sphinx
			// role on its own line (`:ref:`foo.Bar``) would be
			// misclassified as an option and the code block would
			// emit nothing.
			for j < len(lines) {
				t := lines[j].text
				ts := strings.TrimSpace(t)
				if ts == "" {
					j++
					continue
				}
				ind := leadingSpaces(t)
				if ind > directiveIndent && isDirectiveOption(ts) {
					j++
					continue
				}
				break
			}
			contentStartLine := j
			for j < len(lines) {
				t := lines[j].text
				ts := strings.TrimSpace(t)
				if ts == "" {
					j++
					continue
				}
				ind := leadingSpaces(t)
				if ind <= directiveIndent {
					break
				}
				j++
			}
			contentEndLine := j
			for contentEndLine > contentStartLine && strings.TrimSpace(lines[contentEndLine-1].text) == "" {
				contentEndLine--
			}
			if contentEndLine > contentStartLine {
				cs := lines[contentStartLine].off
				var ce int
				if contentEndLine < len(lines) {
					ce = lines[contentEndLine].off
				} else {
					ce = len(body)
				}
				blocks = append(blocks, codeBlock{
					start:        ln.off,
					contentStart: cs,
					contentEnd:   ce,
					lang:         lang,
				})
			}
			i = j
			continue
		}

		// Overlined heading (overline + text + underline).
		if i+2 < len(lines) {
			line0 := strings.TrimRight(ln.text, " \t")
			line1 := strings.TrimSpace(lines[i+1].text)
			line2 := strings.TrimRight(lines[i+2].text, " \t")
			if len(line0) >= 2 && len(line1) > 0 && len(line2) >= len(line1) &&
				strings.IndexByte(underlineChars, line0[0]) >= 0 &&
				isAllChar(line0, line0[0]) && isAllChar(line2, line0[0]) {
				registerHeading(lines[i+1].off, line0[0], line1)
				i += 3
				continue
			}
		}

		// Plain underlined heading (text + underline).
		if i+1 < len(lines) {
			text := strings.TrimSpace(ln.text)
			if text != "" {
				under := strings.TrimRight(lines[i+1].text, " \t")
				if len(under) >= len(text) && len(under) >= 2 &&
					strings.IndexByte(underlineChars, under[0]) >= 0 &&
					isAllChar(under, under[0]) {
					registerHeading(ln.off, under[0], text)
					i += 2
					continue
				}
			}
		}
		i++
	}

	sectionPathAt := func(off int) []string {
		var stack [3]string
		for _, e := range events {
			if e.off > off {
				break
			}
			stack[e.level-1] = e.text
			for k := e.level; k < 3; k++ {
				stack[k] = ""
			}
		}
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
	return blocks, sectionPathAt
}

// isDirectiveOption returns true when s looks like a directive option
// line — `:name:` or `:name: value`, where `name` is lowercase letters,
// digits, and dashes only, and the closing `:` is followed by EOL or
// whitespace. Crucially excludes Sphinx inline roles like `:ref:`foo“
// (which has “ ` “ after the closing colon, not whitespace).
func isDirectiveOption(s string) bool {
	if len(s) < 2 || s[0] != ':' {
		return false
	}
	i := 1
	for i < len(s) {
		c := s[i]
		if c == '-' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	if i == 1 || i >= len(s) || s[i] != ':' {
		return false
	}
	// After the closing colon: EOL or whitespace only.
	if i+1 == len(s) {
		return true
	}
	return s[i+1] == ' ' || s[i+1] == '\t'
}

func isAllChar(s string, ch byte) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != ch {
			return false
		}
	}
	return true
}

func splitLines(body []byte) []srcLine {
	out := make([]srcLine, 0, 64)
	off := 0
	for off < len(body) {
		end := off
		for end < len(body) && body[end] != '\n' {
			end++
		}
		out = append(out, srcLine{text: string(body[off:end]), off: off})
		off = end + 1
	}
	return out
}

func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	return n
}

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

func countWords(s string) int { return len(strings.Fields(s)) }

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

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
