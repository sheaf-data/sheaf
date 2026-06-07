// Package clap implements a contract-anchor adapter for Rust CLI
// tools built with the `clap` crate's derive API.
//
// clap's derive API works by deriving Parser / Args / Subcommand for
// a struct (or enum) at compile time. Struct fields annotated with
// `#[arg(...)]` and `#[command(...)]` describe the CLI surface; doc
// comments (`///`) become help text. Sibling enums annotated with
// `#[derive(Subcommand)]` enumerate subcommands.
//
// This adapter does NOT compile or run any Rust code — it walks the
// source, finds Parser / Subcommand derives, and reconstructs the
// CLI's contract surface by reading the annotations + doc comments.
//
// What's modeled
//
//   - Root binary: the first `#[derive(... Parser ...)]` struct seen
//     under a configured crate root. Its `#[command(name = "X")]` (if
//     present) sets the binary name; otherwise the crate-root basename
//     is used.
//
//   - Flags / switches / positionals: every field with `#[arg(...)]`
//     becomes one ContractElement. `long = "X"` → `--X`; bare `long`
//     → `--<kebab(field_name)>`; no `long`/`short` → POSITIONAL.
//     Type `bool` (or `action = ArgAction::SetTrue|Count`) → SWITCH;
//     otherwise FLAG. An `#[arg(... default_value(_t)? = ...)]` clause
//     promotes the element to CONFIG_KNOB — mirrors the argh adapter.
//
//   - Subcommands: a field marked `#[command(subcommand)]` references
//     an enum derived with `#[derive(Subcommand)]`. Each variant of
//     that enum becomes a child SUBCOMMAND element under the parent
//     command. Variant args (variant tuple structs that themselves
//     derive Args/Parser) are walked recursively to attach the
//     variant's flags.
//
// What's deliberately skipped for v1
//
//   - `#[arg(hide = true)]` fields. clap-derived CLIs use these for
//     internal back-pointers like `relative_path: ()` that exist
//     only to satisfy `overrides_with` plumbing — they never appear
//     in `--help` and aren't part of the contract surface.
//
//   - Builder-style `Arg::new(...)` declarations (no derive). fd uses
//     this for `--exec`/`--exec-batch` because the derive API doesn't
//     support grouped values yet; the adapter doesn't parse these.
//     A `BUILDER_FLAGS_TBD` note is logged in element metadata when
//     a `#[command(flatten)]` field points at such a struct so the
//     gap is visible in coverage rather than silent.
//
//   - `ValueEnum` enums. These describe value types of flags
//     (`ColorWhen::{Auto, Always, Never}`), not new contract elements.

package clap

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

const Name = "clap"
const Version = "0.1.0"

type Adapter struct {
	crateRoots []string
	include    []string
	exclude    []string
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
		crateRoots: cfg.CrateRoots,
		include:    include,
		exclude:    exclude,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Discover scans every configured crate root for Parser/Subcommand
// derives and emits ContractElements.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	var out []*contractpb.ContractElement
	for _, crate := range a.crateRoots {
		crateBinary := binaryNameFromCrateRoot(crate)
		// We don't yet know the in-source `#[command(name = "...")]`
		// override; scope filtering happens after we've assembled the
		// crate's elements and learned the binary name.
		elems, err := a.walkCrate(ctx, repoRoot, crate, crateBinary)
		if err != nil {
			return nil, err
		}
		// Resolve binary name override: the first emitted SUBCOMMAND
		// rooted at the crate represents the root binary; its ID is
		// the binary name.
		actualBinary := crateBinary
		for _, e := range elems {
			if e.GetKind() == contractpb.ContractElementKind_SUBCOMMAND && !strings.Contains(e.GetId(), " ") {
				actualBinary = e.GetId()
				break
			}
		}
		if !libraryInScope(actualBinary, scope) {
			continue
		}
		out = append(out, elems...)
	}
	return out, nil
}

func (a *Adapter) walkCrate(ctx context.Context, repoRoot, crateRoot, binaryName string) ([]*contractpb.ContractElement, error) {
	walkBase := filepath.Join(repoRoot, crateRoot)
	// Two passes: first collect every Parser/Args struct and
	// Subcommand enum across all files (so cross-file variant→args
	// resolution works); then emit elements rooted at any Parser
	// struct.
	c := newCollector()
	err := adapters.WalkMatching(walkBase, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		repoRel := filepath.ToSlash(filepath.Join(crateRoot, rel))
		body, err := adapters.ReadFile(repoRoot, repoRel)
		if err != nil {
			return fmt.Errorf("clap: read %s: %w", repoRel, err)
		}
		c.ingestFile(string(body), repoRel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c.emit(binaryName), nil
}

// collector accumulates parsed structs/enums across a crate.
type collector struct {
	// Parser-derived structs (roots). Key: struct name.
	parsers map[string]*parsedStruct
	// Args-derived structs (subcommand variant args). Key: struct name.
	args map[string]*parsedStruct
	// Subcommand-derived enums. Key: enum name.
	subcmds map[string]*parsedEnum
	// Order Parser structs were seen, so deterministic emission picks
	// the first crate-root Parser as "the root binary".
	parserOrder []string
}

type parsedStruct struct {
	name        string
	path        string
	line        int
	commandAttr commandAttr // from #[command(...)] above the struct
	docComment  string
	fields      []parsedField
}

type parsedEnum struct {
	name     string
	path     string
	line     int
	variants []enumVariant
}

type enumVariant struct {
	name       string // Rust ident, e.g. "Component"
	overrideID string // from #[command(name = "X")] on the variant
	docComment string
	argsType   string // when variant is Component(ComponentArgs), this is "ComponentArgs"
	line       int
}

type parsedField struct {
	rustName   string // snake_case field name in the struct
	rustType   string // raw type text, e.g. "Option<Vec<String>>"
	argAttr    argAttr
	cmdAttr    commandAttr // when #[command(...)] on a field (subcommand/flatten markers)
	docComment string
	line       int
}

type commandAttr struct {
	present    bool
	name       string // #[command(name = "X")]
	about      string
	subcommand bool // #[command(subcommand)]
	flatten    bool // #[command(flatten)]
}

type argAttr struct {
	present      bool
	long         string // explicit long="X"; "" if `long` bare; absent → notLong=true
	hasLong      bool   // `long` keyword appeared
	short        byte   // 0 if none
	hasShort     bool
	help         string
	longHelp     string
	hasDefault   bool
	hide         bool
	valueName    string
	isSwitchHint bool // ArgAction::SetTrue / SetFalse / Count
}

func newCollector() *collector {
	return &collector{
		parsers: map[string]*parsedStruct{},
		args:    map[string]*parsedStruct{},
		subcmds: map[string]*parsedEnum{},
	}
}

// ingestFile parses one .rs source file, registering every
// Parser/Args struct and Subcommand enum found.
func (c *collector) ingestFile(body, path string) {
	offsets := computeLineOffsets([]byte(body))
	// Walk linearly to keep #[derive(...)] + struct/enum pairing local.
	i := 0
	for i < len(body) {
		// Find next `#[derive(`.
		j := indexFrom(body, "#[derive(", i)
		if j < 0 {
			return
		}
		// Parse the derive list.
		derivesEnd := findAnnotationEnd(body, j)
		if derivesEnd < 0 {
			return
		}
		derivesBody := body[j:derivesEnd]
		hasParser := containsDerive(derivesBody, "Parser")
		hasArgs := containsDerive(derivesBody, "Args")
		hasSubcommand := containsDerive(derivesBody, "Subcommand")
		// Skip past stacked attributes (e.g. #[command(...)]) and doc
		// comments to reach the struct/enum declaration. Also pick up
		// any #[command(...)] attribute that sits between the derive
		// and the declaration — it applies to the struct/enum.
		var stackedCmd commandAttr
		k := derivesEnd
		for {
			k = skipWhitespace(body, k)
			if k >= len(body) {
				return
			}
			if body[k] == '#' {
				end := findAnnotationEnd(body, k)
				if end < 0 {
					return
				}
				attr := body[k:end]
				if ca, ok := parseCommandAttr(attr); ok {
					// Merge — later attrs override earlier on same key.
					stackedCmd = mergeCommandAttr(stackedCmd, ca)
				}
				k = end
				continue
			}
			if k+2 < len(body) && body[k] == '/' && body[k+1] == '/' && body[k+2] == '/' {
				// Doc comment; skip to end of line.
				for k < len(body) && body[k] != '\n' {
					k++
				}
				continue
			}
			break
		}
		// Now expect `pub? (struct|enum) Name`.
		k = skipKeyword(body, k, "pub")
		k = skipWhitespace(body, k)
		kind := ""
		if strings.HasPrefix(body[k:], "struct ") {
			kind = "struct"
			k += len("struct ")
		} else if strings.HasPrefix(body[k:], "enum ") {
			kind = "enum"
			k += len("enum ")
		} else {
			// Not a declaration we care about; advance past this derive.
			i = derivesEnd
			continue
		}
		k = skipWhitespace(body, k)
		nameStart := k
		for k < len(body) && isIdentByte(body[k]) {
			k++
		}
		typeName := body[nameStart:k]
		// Skip generic params, where-clauses, and align on `{`.
		braceIdx := indexFrom(body, "{", k)
		if braceIdx < 0 {
			return
		}
		bodyEnd := findMatchingBrace(body, braceIdx)
		if bodyEnd < 0 {
			return
		}
		blockBody := body[braceIdx+1 : bodyEnd]
		declLine := lineFromOffset(offsets, j)
		docCmt := extractDocCommentAbove(body, j)

		switch {
		case kind == "struct" && (hasParser || hasArgs):
			ps := &parsedStruct{
				name:        typeName,
				path:        path,
				line:        declLine,
				commandAttr: stackedCmd,
				docComment:  docCmt,
				fields:      parseStructFields(blockBody, braceIdx+1, offsets),
			}
			if hasParser {
				if _, dup := c.parsers[typeName]; !dup {
					c.parserOrder = append(c.parserOrder, typeName)
				}
				c.parsers[typeName] = ps
			}
			if hasArgs && !hasParser {
				c.args[typeName] = ps
			}
		case kind == "enum" && hasSubcommand:
			c.subcmds[typeName] = parseSubcommandEnum(typeName, blockBody, braceIdx+1, path, declLine, offsets)
		}
		i = bodyEnd + 1
	}
}

// emit walks the collector's Parser structs and produces
// ContractElements. fallbackBinary is used when a struct lacks a
// `#[command(name = "...")]` override.
func (c *collector) emit(fallbackBinary string) []*contractpb.ContractElement {
	if len(c.parserOrder) == 0 {
		return nil
	}
	// Pick the first Parser struct seen as the root.
	root := c.parsers[c.parserOrder[0]]
	binaryName := root.commandAttr.name
	if binaryName == "" {
		binaryName = fallbackBinary
	}
	var out []*contractpb.ContractElement
	out = append(out, c.emitCommand(root, binaryName, binaryName)...)
	return out
}

// emitCommand emits one SUBCOMMAND element for this struct plus
// elements for every field, then recurses into any subcommand enum.
// commandID is the space-joined path ("fd", "fd component show").
func (c *collector) emitCommand(ps *parsedStruct, commandID, binaryName string) []*contractpb.ContractElement {
	var out []*contractpb.ContractElement
	cmdElem := &contractpb.ContractElement{
		Id:        commandID,
		Kind:      contractpb.ContractElementKind_SUBCOMMAND,
		Ecosystem: "clap",
		Library:   binaryName,
		Location: &commonpb.SourceLocation{
			Path: ps.path,
			Line: uint32(ps.line),
		},
		DocCommentExcerpt: ps.docComment,
	}
	out = append(out, cmdElem)

	var subcmdEnumName string
	for _, f := range ps.fields {
		// Subcommand pointer: #[command(subcommand)] field.
		if f.cmdAttr.present && f.cmdAttr.subcommand {
			subcmdEnumName = strings.TrimSpace(extractTypeName(f.rustType))
			continue
		}
		// Flattened args: #[command(flatten)] field pulls in another
		// Args struct's fields. We recurse into it.
		if f.cmdAttr.present && f.cmdAttr.flatten {
			flatName := strings.TrimSpace(extractTypeName(f.rustType))
			if ps2, ok := c.args[flatName]; ok {
				for _, ff := range ps2.fields {
					if e := emitField(ff, commandID, binaryName, ps2.path); e != nil {
						out = append(out, e)
					}
				}
			}
			continue
		}
		if !f.argAttr.present {
			continue
		}
		if e := emitField(f, commandID, binaryName, ps.path); e != nil {
			out = append(out, e)
		}
	}

	// Recurse into subcommand enum variants.
	if subcmdEnumName != "" {
		if en, ok := c.subcmds[subcmdEnumName]; ok {
			for _, v := range en.variants {
				childName := v.overrideID
				if childName == "" {
					childName = camelToKebab(v.name)
				}
				childID := commandID + " " + childName
				cmdElem.Relationships = append(cmdElem.Relationships, &contractpb.Relationship{
					Kind:            contractpb.RelationshipKind_REFERENCES_TYPE,
					TargetElementId: childID,
					Note:            "subcommand: " + childName,
				})
				if v.argsType != "" {
					if ps2, ok := c.args[v.argsType]; ok {
						out = append(out, c.emitCommand(ps2, childID, binaryName)...)
						continue
					}
					if ps2, ok := c.parsers[v.argsType]; ok {
						out = append(out, c.emitCommand(ps2, childID, binaryName)...)
						continue
					}
				}
				// No args-type or unresolved → emit a bare SUBCOMMAND.
				out = append(out, &contractpb.ContractElement{
					Id:                childID,
					Kind:              contractpb.ContractElementKind_SUBCOMMAND,
					Ecosystem:         "clap",
					Library:           binaryName,
					Location:          &commonpb.SourceLocation{Path: en.path, Line: uint32(v.line)},
					DocCommentExcerpt: v.docComment,
				})
			}
		}
	}
	return out
}

// emitField materializes one field into a ContractElement, or returns
// nil for fields that should be skipped (hidden, no #[arg]).
func emitField(f parsedField, commandID, binaryName, path string) *contractpb.ContractElement {
	if f.argAttr.hide {
		return nil
	}
	kind, suffix := classifyField(f)
	id := commandID + " " + suffix
	// Aliases let rust-test / markdown body-token matchers attribute
	// when they extract bare flag literals from test code or prose.
	// We emit the bare long form ("--hidden") and the bare short form
	// ("-H") when defined. Strategy 1 in the indexer matches refs
	// against aliases by exact string equality, so these are the
	// shapes those adapters need to produce.
	var aliases []string
	if kind == contractpb.ContractElementKind_FLAG ||
		kind == contractpb.ContractElementKind_SWITCH ||
		kind == contractpb.ContractElementKind_CONFIG_KNOB {
		aliases = append(aliases, suffix) // "--hidden"
		if f.argAttr.hasShort && f.argAttr.short != 0 {
			aliases = append(aliases, "-"+string(f.argAttr.short))
		}
	}
	return &contractpb.ContractElement{
		Id:        id,
		Kind:      kind,
		Ecosystem: "clap",
		Library:   binaryName,
		Aliases:   aliases,
		Location: &commonpb.SourceLocation{
			Path: path,
			Line: uint32(f.line),
		},
		DocCommentExcerpt: bestDocText(f),
	}
}

// classifyField returns (kind, idSuffix) for a parsed field. Mirrors
// argh's promotion of `default_value` options to CONFIG_KNOB.
func classifyField(f parsedField) (contractpb.ContractElementKind, string) {
	a := f.argAttr
	// Positional: no long, no short — value is a positional argument.
	if !a.hasLong && !a.hasShort {
		name := a.valueName
		if name == "" {
			name = f.rustName
		}
		return contractpb.ContractElementKind_POSITIONAL, "<" + name + ">"
	}
	// Switch: ArgAction::SetTrue|Count, OR field type is bool.
	isSwitch := a.isSwitchHint || isBoolType(f.rustType)
	flagName := a.long
	if flagName == "" {
		flagName = strings.ReplaceAll(f.rustName, "_", "-")
	}
	suffix := "--" + flagName
	if isSwitch {
		return contractpb.ContractElementKind_SWITCH, suffix
	}
	if a.hasDefault {
		return contractpb.ContractElementKind_CONFIG_KNOB, suffix
	}
	return contractpb.ContractElementKind_FLAG, suffix
}

func bestDocText(f parsedField) string {
	// Prefer the doc comment (`///`) above the field — that's the
	// canonical clap-derive help source. Falls back to the explicit
	// `help = "..."` annotation.
	if f.docComment != "" {
		return f.docComment
	}
	if f.argAttr.longHelp != "" {
		return f.argAttr.longHelp
	}
	return f.argAttr.help
}

// ---------- parsing helpers ----------

var (
	reCmdName  = regexp.MustCompile(`\bname\s*=\s*"([^"]+)"`)
	reCmdAbout = regexp.MustCompile(`\babout\s*=\s*"([^"]+)"`)
	// `long = "X"` with explicit name. The trailing `(?:[\s,)=]|$)`
	// is implicit because the value capture ends at `"`.
	reArgLongNamed  = regexp.MustCompile(`(?:^|[,(\s])long\s*=\s*"([^"]+)"`)
	reArgShortNamed = regexp.MustCompile(`(?:^|[,(\s])short\s*=\s*'(.)'`)
	// `long` / `short` bare — match the IDENT then look-ahead for a
	// non-ident character (anything other than `[a-zA-Z0-9_]`). The
	// look-ahead is what stops `long_help` from being mistaken for
	// the bare `long` keyword.
	reArgLongBare   = regexp.MustCompile(`(?:^|[,(\s])long([^a-zA-Z0-9_]|$)`)
	reArgShortBare  = regexp.MustCompile(`(?:^|[,(\s])short([^a-zA-Z0-9_]|$)`)
	reArgHelp       = regexp.MustCompile(`(?:^|[,(\s])help\s*=\s*"([^"\\]*(?:\\.[^"\\]*)*)"`)
	reArgLongHelpEq = regexp.MustCompile(`(?:^|[,(\s])long_help\s*=\s*"([^"\\]*(?:\\.[^"\\]*)*)"`)
	reArgDefault    = regexp.MustCompile(`(?:^|[,(\s])default_(?:value|value_t|values|values_t|missing_value)\b`)
	reArgHideEqTrue = regexp.MustCompile(`(?:^|[,(\s])hide\s*=\s*true\b`)
	reArgHideBare   = regexp.MustCompile(`(?:^|[,(\s])hide([^a-zA-Z0-9_]|$)`)
	reArgValueName  = regexp.MustCompile(`(?:^|[,(\s])value_name\s*=\s*"([^"]+)"`)
	reArgActionSet  = regexp.MustCompile(`\bArgAction::(SetTrue|SetFalse|Count)\b`)
)

// containsDerive returns true if the parenthesized derive-list body
// contains the given trait name. The body has the form
// `#[derive(A, B, C)]`; we scan for the bare ident.
func containsDerive(derivesBody, name string) bool {
	rx := regexp.MustCompile(`(^|[(,\s])` + regexp.QuoteMeta(name) + `(\s|,|\))`)
	return rx.MatchString(derivesBody)
}

// parseCommandAttr extracts `#[command(...)]` content. Returns
// (attr, true) when the attribute is `#[command(...)]`; (zero, false)
// when the attribute is something else (e.g. `#[cfg(...)]`).
func parseCommandAttr(attr string) (commandAttr, bool) {
	if !strings.HasPrefix(attr, "#[command(") && !strings.HasPrefix(attr, "#[clap(") {
		return commandAttr{}, false
	}
	ca := commandAttr{present: true}
	if m := reCmdName.FindStringSubmatch(attr); m != nil {
		ca.name = m[1]
	}
	if m := reCmdAbout.FindStringSubmatch(attr); m != nil {
		ca.about = m[1]
	}
	if strings.Contains(attr, "subcommand") && !strings.Contains(attr, "subcommand_") {
		// Cheap disambiguation: distinguishes the marker `subcommand`
		// from longer identifiers like `subcommand_help_heading`.
		// The marker has no `=` after it.
		ca.subcommand = isBareKeyword(attr, "subcommand")
	}
	if strings.Contains(attr, "flatten") {
		ca.flatten = isBareKeyword(attr, "flatten")
	}
	return ca, true
}

func mergeCommandAttr(a, b commandAttr) commandAttr {
	out := a
	out.present = true
	if b.name != "" {
		out.name = b.name
	}
	if b.about != "" {
		out.about = b.about
	}
	if b.subcommand {
		out.subcommand = true
	}
	if b.flatten {
		out.flatten = true
	}
	return out
}

// parseArgAttr extracts the `#[arg(...)]` body of a field.
func parseArgAttr(attr string) (argAttr, bool) {
	if !strings.HasPrefix(attr, "#[arg(") && !strings.HasPrefix(attr, "#[clap(") {
		return argAttr{}, false
	}
	a := argAttr{present: true}
	if m := reArgLongNamed.FindStringSubmatch(attr); m != nil {
		a.hasLong = true
		a.long = m[1]
	} else if reArgLongBare.MatchString(attr) {
		a.hasLong = true
	}
	if m := reArgShortNamed.FindStringSubmatch(attr); m != nil {
		a.hasShort = true
		a.short = m[1][0]
	} else if reArgShortBare.MatchString(attr) {
		a.hasShort = true
	}
	if m := reArgHelp.FindStringSubmatch(attr); m != nil {
		a.help = m[1]
	}
	if m := reArgLongHelpEq.FindStringSubmatch(attr); m != nil {
		a.longHelp = m[1]
	}
	if reArgDefault.MatchString(attr) {
		a.hasDefault = true
	}
	if reArgHideEqTrue.MatchString(attr) {
		a.hide = true
	} else if reArgHideBare.MatchString(attr) {
		a.hide = true
	}
	if m := reArgValueName.FindStringSubmatch(attr); m != nil {
		a.valueName = m[1]
	}
	if reArgActionSet.MatchString(attr) {
		a.isSwitchHint = true
	}
	return a, true
}

// parseStructFields scans a struct body for fields with their
// preceding attributes and doc comments. bodyOffset is the offset of
// structBody[0] inside the original file body, used to recover source
// line numbers; offsets is the line index of the FULL file.
func parseStructFields(structBody string, bodyOffset int, offsets []int) []parsedField {
	var out []parsedField
	i := 0
	for i < len(structBody) {
		// Skip whitespace.
		for i < len(structBody) && isWhitespace(structBody[i]) {
			i++
		}
		if i >= len(structBody) {
			break
		}
		// Collect leading attributes + doc comments.
		var attrs []string
		var docLines []string
		for i < len(structBody) {
			for i < len(structBody) && isWhitespace(structBody[i]) {
				i++
			}
			if i >= len(structBody) {
				break
			}
			if i+2 < len(structBody) && structBody[i] == '/' && structBody[i+1] == '/' && structBody[i+2] == '/' {
				ls := i + 3
				le := ls
				for le < len(structBody) && structBody[le] != '\n' {
					le++
				}
				docLines = append(docLines, strings.TrimSpace(structBody[ls:le]))
				i = le
				continue
			}
			if structBody[i] == '#' {
				end := findAnnotationEnd(structBody, i)
				if end < 0 {
					// Malformed; bail.
					return out
				}
				attrs = append(attrs, structBody[i:end])
				i = end
				continue
			}
			break
		}
		// Optional `pub`.
		i = skipKeyword(structBody, i, "pub")
		i = skipWhitespace(structBody, i)
		if i >= len(structBody) {
			break
		}
		// Now expect `ident : Type ,` (or `}` end).
		if structBody[i] == '}' {
			break
		}
		nameStart := i
		for i < len(structBody) && isIdentByte(structBody[i]) {
			i++
		}
		if i == nameStart {
			// Not an identifier — skip a character and continue (handles
			// stray syntax we don't model).
			i++
			continue
		}
		fieldName := structBody[nameStart:i]
		// Skip whitespace then ':'.
		for i < len(structBody) && isWhitespace(structBody[i]) {
			i++
		}
		if i >= len(structBody) || structBody[i] != ':' {
			// Not a field declaration; resync to next comma.
			i = advancePastComma(structBody, i)
			continue
		}
		i++ // past ':'
		// Read type up to comma at brace-depth 0 (to keep generic <>
		// balanced).
		typeStart := i
		typeEnd := readTypeEnd(structBody, i)
		if typeEnd < 0 {
			return out
		}
		rustType := strings.TrimSpace(structBody[typeStart:typeEnd])
		fieldLine := lineFromOffset(offsets, bodyOffset+nameStart)

		f := parsedField{
			rustName:   fieldName,
			rustType:   rustType,
			docComment: strings.Join(docLines, "\n"),
			line:       fieldLine,
		}
		for _, attr := range attrs {
			if ca, ok := parseCommandAttr(attr); ok {
				f.cmdAttr = mergeCommandAttr(f.cmdAttr, ca)
			}
			if aa, ok := parseArgAttr(attr); ok {
				f.argAttr = aa
			}
		}
		out = append(out, f)
		// Advance past comma.
		i = typeEnd
		if i < len(structBody) && structBody[i] == ',' {
			i++
		}
	}
	return out
}

// parseSubcommandEnum parses an enum body that derives Subcommand.
func parseSubcommandEnum(enumName, body string, bodyOffset int, path string, declLine int, offsets []int) *parsedEnum {
	en := &parsedEnum{name: enumName, path: path, line: declLine}
	i := 0
	for i < len(body) {
		for i < len(body) && isWhitespace(body[i]) {
			i++
		}
		if i >= len(body) {
			break
		}
		// Leading attributes + doc comments per variant.
		var attrs []string
		var docLines []string
		for i < len(body) {
			for i < len(body) && isWhitespace(body[i]) {
				i++
			}
			if i >= len(body) {
				break
			}
			if i+2 < len(body) && body[i] == '/' && body[i+1] == '/' && body[i+2] == '/' {
				ls := i + 3
				le := ls
				for le < len(body) && body[le] != '\n' {
					le++
				}
				docLines = append(docLines, strings.TrimSpace(body[ls:le]))
				i = le
				continue
			}
			if body[i] == '#' {
				end := findAnnotationEnd(body, i)
				if end < 0 {
					return en
				}
				attrs = append(attrs, body[i:end])
				i = end
				continue
			}
			break
		}
		if i >= len(body) || body[i] == '}' {
			break
		}
		// Variant ident.
		nameStart := i
		for i < len(body) && isIdentByte(body[i]) {
			i++
		}
		if i == nameStart {
			i++
			continue
		}
		variantName := body[nameStart:i]
		v := enumVariant{
			name:       variantName,
			docComment: strings.Join(docLines, "\n"),
			line:       lineFromOffset(offsets, bodyOffset+nameStart),
		}
		// Optional `(VariantArgs)` tuple.
		for i < len(body) && isWhitespace(body[i]) {
			i++
		}
		if i < len(body) && body[i] == '(' {
			j := i + 1
			depth := 1
			for j < len(body) && depth > 0 {
				switch body[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			inner := strings.TrimSpace(body[i+1 : j-1])
			v.argsType = extractTypeName(inner)
			i = j
		} else if i < len(body) && body[i] == '{' {
			// Struct-variant: `Variant { ... }`. Skip to matching brace.
			end := findMatchingBrace(body, i)
			if end > 0 {
				i = end + 1
			}
		}
		// Pull #[command(name = "X")] override off the variant.
		for _, a := range attrs {
			if ca, ok := parseCommandAttr(a); ok && ca.name != "" {
				v.overrideID = ca.name
			}
		}
		en.variants = append(en.variants, v)
		// Skip to next variant (past comma).
		for i < len(body) && body[i] != ',' && body[i] != '}' {
			i++
		}
		if i < len(body) && body[i] == ',' {
			i++
		}
	}
	return en
}

// ---------- small string utilities ----------

func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	i := strings.Index(s[from:], sub)
	if i < 0 {
		return -1
	}
	return from + i
}

func skipWhitespace(s string, i int) int {
	for i < len(s) && isWhitespace(s[i]) {
		i++
	}
	return i
}

func skipKeyword(s string, i int, kw string) int {
	j := skipWhitespace(s, i)
	if strings.HasPrefix(s[j:], kw+" ") || strings.HasPrefix(s[j:], kw+"\t") || strings.HasPrefix(s[j:], kw+"\n") {
		return j + len(kw)
	}
	return i
}

func advancePastComma(s string, i int) int {
	for i < len(s) && s[i] != ',' {
		i++
	}
	if i < len(s) {
		return i + 1
	}
	return i
}

// readTypeEnd returns the index of the comma (or `}`) ending the
// type expression starting at `from`. Tracks <>, () and string
// literals so commas inside generics or default expressions don't
// terminate prematurely.
func readTypeEnd(body string, from int) int {
	depthAngle := 0
	depthParen := 0
	depthBracket := 0
	inString := false
	var stringDelim byte
	for i := from; i < len(body); i++ {
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
		case '"':
			inString = true
			stringDelim = '"'
		case '<':
			depthAngle++
		case '>':
			if depthAngle > 0 {
				depthAngle--
			}
		case '(':
			depthParen++
		case ')':
			if depthParen > 0 {
				depthParen--
			}
		case '[':
			depthBracket++
		case ']':
			if depthBracket > 0 {
				depthBracket--
			}
		case ',':
			if depthAngle == 0 && depthParen == 0 && depthBracket == 0 {
				return i
			}
		case '}':
			if depthAngle == 0 && depthParen == 0 && depthBracket == 0 {
				return i
			}
		}
	}
	return len(body)
}

func isWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// isBoolType: simple heuristic — bare `bool` or `u8` (used by clap for
// counted switches like `-vvv`).
func isBoolType(t string) bool {
	tt := strings.TrimSpace(t)
	return tt == "bool" || tt == "u8"
}

// isBareKeyword returns true when `kw` appears in `attr` either at
// the start of a token (after `(` or `,`) and is NOT followed by a
// `=` (which would mean it's a keyword=value pair, not a bare marker).
func isBareKeyword(attr, kw string) bool {
	idx := 0
	for {
		j := strings.Index(attr[idx:], kw)
		if j < 0 {
			return false
		}
		j += idx
		// Check left boundary.
		left := byte('(')
		if j > 0 {
			left = attr[j-1]
		}
		if !(left == '(' || left == ',' || left == ' ' || left == '\t' || left == '\n') {
			idx = j + 1
			continue
		}
		// Check right boundary.
		after := j + len(kw)
		// Skip whitespace.
		for after < len(attr) && isWhitespace(attr[after]) {
			after++
		}
		if after >= len(attr) {
			return true
		}
		if attr[after] == '=' {
			// Not a bare marker.
			idx = j + 1
			continue
		}
		return true
	}
}

// findAnnotationEnd: same logic as argh's — returns the offset just
// after the closing `)]` of an attribute starting at body[start]=='#'.
func findAnnotationEnd(body string, start int) int {
	i := start
	for i < len(body) && body[i] != '(' && body[i] != '[' {
		i++
	}
	if i >= len(body) {
		return -1
	}
	// Skip `[` then expect `ident(`. We need the FIRST `(` of the
	// content, which is what we have here when body[i] == '[' (for
	// the attribute `[`). Advance to the `(` after the attr name.
	if body[i] == '[' {
		i++
		for i < len(body) && body[i] != '(' {
			i++
		}
		if i >= len(body) {
			// No `(` — bare attribute like `#[derive]`? Find `]`.
			j := strings.Index(body[start:], "]")
			if j < 0 {
				return -1
			}
			return start + j + 1
		}
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
		// Skip `// ... \n` line comments inside the attribute body —
		// real-world clap derives (fd, ripgrep) put inline `//`
		// comments inside `#[arg(...)]` whose text may contain
		// apostrophes ("ripgrep's flag"). Without this, the lone `'`
		// is misread as a char-literal opener and depth tracking
		// never reaches zero.
		if c == '/' && i+1 < len(body) && body[i+1] == '/' {
			for i < len(body) && body[i] != '\n' {
				i++
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			stringDelim = c
		case '\'':
			// Char-literal disambiguation: `'X'` is a char only when
			// the apostrophe sits between two ASCII-text positions
			// AND the closing apostrophe is two bytes ahead (or three
			// for `'\n'` etc.). When unsure, treat as a lifetime tick
			// (`'a`) or stray apostrophe and don't open a string.
			if isCharLiteral(body, i) {
				inString = true
				stringDelim = c
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				if i+1 < len(body) && body[i+1] == ']' {
					return i + 2
				}
				return -1
			}
		}
	}
	return -1
}

// isCharLiteral returns true when body[i]=='\” opens a Rust char
// literal (`'X'` or `'\n'`) as opposed to a lifetime tick (`'a`).
func isCharLiteral(body string, i int) bool {
	// `'\X'` (4 bytes) or `'X'` (3 bytes).
	if i+2 < len(body) && body[i+2] == '\'' {
		return true
	}
	if i+3 < len(body) && body[i+1] == '\\' && body[i+3] == '\'' {
		return true
	}
	return false
}

// findMatchingBrace: same as argh's.
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

// extractDocCommentAbove: same as argh's.
func extractDocCommentAbove(body string, offset int) string {
	start := offset
	for start > 0 && body[start-1] != '\n' {
		start--
	}
	var lines []string
	for {
		if start == 0 {
			break
		}
		end := start - 1
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

func binaryNameFromCrateRoot(crate string) string {
	cl := filepath.ToSlash(filepath.Clean(crate))
	parts := strings.Split(cl, "/")
	if len(parts) == 0 {
		return crate
	}
	last := parts[len(parts)-1]
	if last == "." || last == "" {
		// Crate root is the repo root — caller should rely on
		// #[command(name = "...")] for the binary name; return empty.
		return ""
	}
	return last
}

// extractTypeName pulls the bare type identifier from a field type.
// Handles `Option<X>`, `Vec<X>`, references, etc., by returning the
// innermost ident.
func extractTypeName(t string) string {
	t = strings.TrimSpace(t)
	// Trim trailing parens/commas.
	t = strings.TrimRight(t, " ,)>")
	// Strip leading `& ` / `&'a ` / `mut ` etc.
	for {
		switch {
		case strings.HasPrefix(t, "&"):
			t = strings.TrimSpace(t[1:])
			// Skip optional lifetime + mut.
			if strings.HasPrefix(t, "'") {
				if sp := strings.IndexByte(t, ' '); sp > 0 {
					t = strings.TrimSpace(t[sp+1:])
				}
			}
			if strings.HasPrefix(t, "mut ") {
				t = strings.TrimSpace(t[4:])
			}
			continue
		}
		break
	}
	// If wrapped in generic, peel.
	if i := strings.Index(t, "<"); i > 0 {
		// Recurse into the inner-most type argument.
		return extractTypeName(t[i+1:])
	}
	return strings.TrimSpace(t)
}

// camelToKebab: ComponentShow → component-show.
func camelToKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// computeLineOffsets / lineFromOffset: shared with argh's style.
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

// ---------- scope filtering (mirrors argh) ----------

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
