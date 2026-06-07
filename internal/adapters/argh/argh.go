// Package argh implements a contract-anchor adapter for Rust CLI
// tools built with the `argh` crate.
//
// argh works by deriving a parser from a `#[derive(FromArgs)]` struct
// at compile time. The struct's fields (annotated with #[argh(option)],
// #[argh(switch)], #[argh(positional)], #[argh(subcommand)]) define
// what the CLI accepts. Doc comments (`///`) above each field become
// the `--help` text.
//
// This adapter does NOT compile or run any Rust code — it walks the
// source, finds `FromArgs` derives, and reconstructs the CLI's
// contract surface by reading the annotations + doc comments.
//
// Subcommand paths are derived from filesystem layout. A struct named
// `Show` in `plugins/component/show/src/args.rs` is the `show`
// subcommand of `component` under the root binary. The root binary's
// name comes from the configured crate roots.

package argh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

const Name = "argh"
const Version = "0.1.0"

type Adapter struct {
	crateRoots []string
	include    []string
	exclude    []string

	// Regexes used to find FromArgs structs and their fields.
	deriveRx     *regexp.Regexp // matches `#[derive(... FromArgs ...)]` followed by struct
	subcommandRx *regexp.Regexp // matches `#[argh(subcommand, name = "X", description = "Y")]`
	optionRx     *regexp.Regexp // matches `#[argh(option, ...)]`
	switchRx     *regexp.Regexp // matches `#[argh(switch, ...)]`
	positionalRx *regexp.Regexp // matches `#[argh(positional)]`
	structDeclRx *regexp.Regexp // matches `struct Name {`
	fieldRx      *regexp.Regexp // matches `name: Type,` field declaration
}

type Config struct {
	CrateRoots []string
	Include    []string
	Exclude    []string
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.rs"}
	}
	exclude := cfg.Exclude
	if len(exclude) == 0 {
		exclude = []string{"**/target/**"}
	}
	return &Adapter{
		crateRoots:   cfg.CrateRoots,
		include:      include,
		exclude:      exclude,
		deriveRx:     regexp.MustCompile(`(?s)#\[derive\(([^)]*FromArgs[^)]*)\)\][^{]*?(pub\s+)?struct\s+(\w+)\s*\{`),
		subcommandRx: regexp.MustCompile(`(?s)#\[argh\(subcommand[^)]*name\s*=\s*"([^"]+)"[^)]*\)\]`),
		// We just match the annotation prefix; the field name is
		// extracted in a second pass that scans for the next
		// `name: Type,` declaration after the annotation closes.
		// This avoids unbalanced-paren issues in `default = "..."`
		// expressions inside annotations.
		optionRx:     regexp.MustCompile(`#\[argh\(\s*option\b`),
		switchRx:     regexp.MustCompile(`#\[argh\(\s*switch\b`),
		positionalRx: regexp.MustCompile(`#\[argh\(\s*positional\b`),
		structDeclRx: regexp.MustCompile(`struct\s+(\w+)`),
		fieldRx:      regexp.MustCompile(`(?m)^\s*(\w+)\s*:\s*([^,\n]+)`),
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Discover scans every configured crate root for FromArgs structs and
// emits ContractElements per discovered subcommand/flag/positional.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	var out []*contractpb.ContractElement
	for _, crate := range a.crateRoots {
		binaryName := binaryNameFromCrateRoot(crate)
		if !libraryInScope(binaryName, scope) {
			continue
		}
		// Walk the crate.
		elems, err := a.walkCrate(ctx, repoRoot, crate, binaryName)
		if err != nil {
			return nil, err
		}
		out = append(out, elems...)
	}
	return out, nil
}

// walkCrate visits every .rs file under crateRoot, parses FromArgs
// structs, and assembles subcommand paths from the file location.
func (a *Adapter) walkCrate(ctx context.Context, repoRoot, crateRoot, binaryName string) ([]*contractpb.ContractElement, error) {
	var out []*contractpb.ContractElement
	walkBase := filepath.Join(repoRoot, crateRoot)
	// Use the shared walker; we filter on include/exclude within the
	// crate's subtree only.
	err := adapters.WalkMatching(walkBase, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// rel is relative to walkBase (the crate root); reconstruct
		// the repo-relative path for emitted Locations.
		repoRel := filepath.ToSlash(filepath.Join(crateRoot, rel))
		body, err := adapters.ReadFile(repoRoot, repoRel)
		if err != nil {
			return fmt.Errorf("argh: read %s: %w", repoRel, err)
		}
		// Derive the subcommand-path-prefix from the file's directory.
		// `plugins/component/show/src/args.rs` (relative to crate root)
		// → subcommand path `component show` under `binaryName`.
		subcmdPath := subcommandPathFromFile(rel, binaryName)
		elems := a.parseFile(string(body), repoRel, subcmdPath, binaryName)
		out = append(out, elems...)
		return nil
	})
	return out, err
}

// parseFile extracts FromArgs structs from the file and emits
// ContractElements. subcmdPath is the path prefix derived from the
// file's location (e.g., "ffx component show"). It's used as the
// element ID base; nested subcommands extend it further.
func (a *Adapter) parseFile(body, path, subcmdPath, binaryName string) []*contractpb.ContractElement {
	var out []*contractpb.ContractElement
	offsets := computeLineOffsets([]byte(body))

	// 1. Find every #[derive(... FromArgs ...)] struct.
	matches := a.deriveRx.FindAllStringSubmatchIndex(body, -1)
	for _, m := range matches {
		structName := body[m[6]:m[7]]
		structStart := m[0]
		structOpenBrace := m[1] - 1 // the `{`
		structEnd := findMatchingBrace(body, structOpenBrace)
		if structEnd < 0 {
			continue
		}
		structBody := body[structOpenBrace+1 : structEnd]

		// Skip the parent struct itself (it represents either the root
		// binary or an intermediate subcommand container — we emit
		// for the subcommand path, not the wrapping struct).

		// 1a. Look for #[argh(subcommand, name = "X")] inside this
		//     struct's enum-variants (if any) — these declare nested
		//     subcommands.
		subcommandMatches := a.subcommandRx.FindAllStringSubmatch(structBody, -1)
		nestedNames := make([]string, 0, len(subcommandMatches))
		for _, sm := range subcommandMatches {
			nestedNames = append(nestedNames, sm[1])
		}

		// 1b. Emit a SUBCOMMAND element for the current path itself
		//     (the struct represents one subcommand level).
		commandID := strings.TrimSpace(subcmdPath)
		if commandID == "" {
			commandID = binaryName
		}
		elem := &contractpb.ContractElement{
			Id:        commandID,
			Kind:      contractpb.ContractElementKind_SUBCOMMAND,
			Ecosystem: "argh",
			Library:   binaryName,
			Location: &commonpb.SourceLocation{
				Path: path,
				Line: uint32(lineFromOffset(offsets, structStart)),
			},
			DocCommentExcerpt: extractDocCommentAbove(body, structStart),
		}
		// Relationships to nested subcommands.
		for _, n := range nestedNames {
			elem.Relationships = append(elem.Relationships, &contractpb.Relationship{
				Kind:            contractpb.RelationshipKind_REFERENCES_TYPE,
				TargetElementId: commandID + " " + n,
				Note:            "subcommand: " + n,
			})
		}
		out = append(out, elem)

		// 2. Parse options / switches / positionals in this struct.
		out = append(out, a.parseFieldAnnotations(structBody, path, commandID, offsets, structOpenBrace)...)
		_ = structName
	}
	return out
}

func (a *Adapter) parseFieldAnnotations(structBody, path, commandID string, offsets []int, bodyOffset int) []*contractpb.ContractElement {
	var out []*contractpb.ContractElement

	emit := func(rx *regexp.Regexp, kindFn func(annotationBody string) contractpb.ContractElementKind, idSuffixFn func(string) string) {
		matches := rx.FindAllStringIndex(structBody, -1)
		for _, m := range matches {
			// Find the end of this annotation: scan for the closing `)]`
			// honoring string literals (so default = "...()..." is safe).
			annotationEnd := findAnnotationEnd(structBody, m[0])
			if annotationEnd < 0 {
				continue
			}
			// Find the next field declaration after the annotation.
			fieldName, fieldOffset := findNextFieldName(structBody, annotationEnd)
			if fieldName == "" {
				continue
			}
			docExcerpt := extractDocCommentAbove(structBody, m[0])
			line := lineFromOffset(offsets, bodyOffset+1+m[0])
			annotationBody := structBody[m[0]:annotationEnd]
			id := commandID + " " + idSuffixFn(fieldName)
			out = append(out, &contractpb.ContractElement{
				Id:        id,
				Kind:      kindFn(annotationBody),
				Ecosystem: "argh",
				Library:   parentLibrary(commandID),
				Location: &commonpb.SourceLocation{
					Path: path,
					Line: uint32(line),
				},
				DocCommentExcerpt: docExcerpt,
			})
			_ = fieldOffset
		}
	}

	// Options with `default = "..."` represent persistent configuration
	// state (settings the tool reads), not per-invocation inputs. Emit
	// them as CONFIG_KNOB so downstream coverage/drift treats them as
	// config-surface elements alongside .cml knobs.
	emit(a.optionRx, optionKindFn, func(field string) string {
		// --foo for option named foo (Rust convention: snake_case fields → kebab-case flags)
		return "--" + strings.ReplaceAll(field, "_", "-")
	})
	emit(a.switchRx, staticKind(contractpb.ContractElementKind_SWITCH), func(field string) string {
		return "--" + strings.ReplaceAll(field, "_", "-")
	})
	emit(a.positionalRx, staticKind(contractpb.ContractElementKind_POSITIONAL), func(field string) string {
		return "<" + field + ">"
	})

	return out
}

// optionKindFn returns CONFIG_KNOB when the argh option annotation
// contains a `default = "..."` clause, FLAG otherwise.
func optionKindFn(annotationBody string) contractpb.ContractElementKind {
	if reArghDefault.MatchString(annotationBody) {
		return contractpb.ContractElementKind_CONFIG_KNOB
	}
	return contractpb.ContractElementKind_FLAG
}

// staticKind builds a per-match kind resolver that ignores the annotation
// body and always returns the same kind.
func staticKind(k contractpb.ContractElementKind) func(string) contractpb.ContractElementKind {
	return func(string) contractpb.ContractElementKind { return k }
}

var reArghDefault = regexp.MustCompile(`\bdefault\s*=\s*"`)

// findAnnotationEnd returns the offset just after the `)]` that
// closes an attribute starting at `#[argh(...)`. Honors string
// literals so parentheses inside `default = "..."` don't confuse us.
func findAnnotationEnd(body string, start int) int {
	// We know body[start] == '#'; advance to the '(' after argh.
	i := start
	for i < len(body) && body[i] != '(' {
		i++
	}
	if i >= len(body) {
		return -1
	}
	i++ // past '('
	depth := 1
	inString := false
	var stringDelim byte
	for ; i < len(body); i++ {
		c := body[i]
		if inString {
			if c == '\\' && i+1 < len(body) {
				i++
				continue
			}
			if c == stringDelim {
				inString = false
			}
			continue
		}
		switch c {
		case '"', '\'':
			inString = true
			stringDelim = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				// Expect `]` next.
				if i+1 < len(body) && body[i+1] == ']' {
					return i + 2
				}
				return -1
			}
		}
	}
	return -1
}

// findNextFieldName scans forward from `from` for the next
// `<ident>:` field declaration and returns the ident.
func findNextFieldName(body string, from int) (string, int) {
	// Skip whitespace, attributes, and doc comments.
	i := from
	for i < len(body) {
		// Skip whitespace.
		for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
			i++
		}
		if i >= len(body) {
			return "", -1
		}
		// Skip another attribute (multiple #[...] can stack).
		if body[i] == '#' {
			end := findAnnotationEnd(body, i)
			if end < 0 {
				return "", -1
			}
			i = end
			continue
		}
		// Skip a doc comment.
		if i+2 < len(body) && body[i] == '/' && body[i+1] == '/' && body[i+2] == '/' {
			for i < len(body) && body[i] != '\n' {
				i++
			}
			continue
		}
		// Skip pub/mut keywords.
		if strings.HasPrefix(body[i:], "pub ") {
			i += 4
			continue
		}
		break
	}
	// Now we should be at the field identifier.
	start := i
	for i < len(body) && (isIdentByte(body[i])) {
		i++
	}
	if i == start {
		return "", -1
	}
	name := body[start:i]
	// Confirm we're at a `:` (possibly after whitespace).
	j := i
	for j < len(body) && (body[j] == ' ' || body[j] == '\t') {
		j++
	}
	if j >= len(body) || body[j] != ':' {
		return "", -1
	}
	return name, start
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// extractDocCommentAbove walks backward from offset, collecting
// consecutive `///` lines, and returns the joined text (prefix
// stripped). Returns empty string if there are no doc comments.
func extractDocCommentAbove(body string, offset int) string {
	// Find the start of the current line.
	start := offset
	for start > 0 && body[start-1] != '\n' {
		start--
	}
	var lines []string
	for {
		// Walk to the start of the previous line.
		if start == 0 {
			break
		}
		end := start - 1 // skip the newline
		lineStart := end
		for lineStart > 0 && body[lineStart-1] != '\n' {
			lineStart--
		}
		line := strings.TrimSpace(body[lineStart:end])
		if !strings.HasPrefix(line, "///") {
			break
		}
		lines = append([]string{strings.TrimSpace(strings.TrimPrefix(line, "///"))}, lines...)
		start = lineStart
	}
	return strings.Join(lines, "\n")
}

// findMatchingBrace returns the index of the `}` that closes the `{`
// at openIdx, or -1 if not balanced.
func findMatchingBrace(body string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// binaryNameFromCrateRoot turns "src/developer/ffx" into "ffx".
func binaryNameFromCrateRoot(crate string) string {
	parts := strings.Split(filepath.ToSlash(crate), "/")
	if len(parts) == 0 {
		return crate
	}
	return parts[len(parts)-1]
}

// subcommandPathFromFile derives the path prefix from where the .rs
// file lives inside the crate root.
//
//	args.rs at crate root          → ""              (the root binary)
//	plugins/component/src/args.rs  → "component"
//	plugins/component/show/src/args.rs → "component show"
//
// Heuristic: collect every path segment that isn't `plugins`, `src`,
// `bin`, `mod`, or a file name. The result is space-joined to give
// the human-readable subcommand path. binaryName is prepended.
func subcommandPathFromFile(rel, binaryName string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// Drop the filename.
	if len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	var kept []string
	for _, p := range parts {
		switch p {
		case "plugins", "src", "bin", "mod", "lib", "":
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return binaryName
	}
	return binaryName + " " + strings.Join(kept, " ")
}

// parentLibrary extracts the binary name from an element ID like
// "ffx component show --json" → "ffx".
func parentLibrary(commandID string) string {
	if i := strings.Index(commandID, " "); i >= 0 {
		return commandID[:i]
	}
	return commandID
}

func libraryInScope(lib string, scope adapters.ScopeConfig) bool {
	for _, ex := range scope.Exclude {
		if matchLibrary(ex, lib) {
			return false
		}
	}
	if len(scope.Libraries) == 0 && len(scope.AlsoInclude) == 0 {
		return true
	}
	for _, l := range scope.Libraries {
		if matchLibrary(l, lib) {
			return true
		}
	}
	for _, l := range scope.AlsoInclude {
		if matchLibrary(l, lib) {
			return true
		}
	}
	return false
}

func matchLibrary(pattern, lib string) bool {
	if pattern == lib {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(lib, prefix)
	}
	return false
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

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

var _ = hashBytes
