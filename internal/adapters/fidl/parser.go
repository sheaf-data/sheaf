package fidl

import (
	"fmt"
	"strings"
)

// File represents one parsed .fidl source file.
type File struct {
	Library          string     // e.g. "fuchsia.io"
	LibraryAvailable *Available // @available preceding the `library` keyword
	Path             string     // repo-relative source path
	Imports          []string   // libraries from `using x.y;`
	Protocols        []*Protocol
	Types            []*TypeDecl
	Constants        []*ConstDecl
	Aliases          []*AliasDecl
}

// Protocol declaration.
type Protocol struct {
	Name      string
	Doc       string
	Available *Available
	Composes  []ComposeRef // `compose Foo;`
	Methods   []*Method
	Line      uint32
	Openness  string // "open" | "closed" | "ajar" | "" (default: ambient)
}

// ComposeRef points at a composed parent.
type ComposeRef struct {
	Target string // e.g. "Openable" or "fuchsia.unknown/Cloneable" — un-resolved
	Line   uint32
}

// Method declaration.
type Method struct {
	Name           string
	Doc            string
	Available      *Available
	HasRequest     bool   // request is present (vs. event)
	HasResponse    bool   // a `-> ()` exists
	HasError       bool   // `... error T` is present
	Flexibility    string // "flexible" | "strict" | ""
	Line           uint32
	ParamTypeRefs  []string // best-effort: identifiers seen in param lists
	ResultTypeRefs []string // identifiers in the response payload
}

// TypeDecl describes a `type X = ...;` declaration.
type TypeDecl struct {
	Name      string
	Kind      string // "struct" | "table" | "union" | "enum" | "bits" | "alias" | "other"
	Doc       string
	Available *Available
	Line      uint32
}

// ConstDecl describes a `const X type = ...;` declaration.
type ConstDecl struct {
	Name      string
	Doc       string
	Available *Available
	Line      uint32
}

// AliasDecl describes an `alias X = ...;` declaration.
type AliasDecl struct {
	Name      string
	Doc       string
	Available *Available
	Line      uint32
}

// Available captures the most useful fields from `@available(...)`.
type Available struct {
	Added      string
	Deprecated string
	Removed    string
	Note       string
}

// Parse parses one .fidl source file. The returned *File holds only
// the structure Sheaf consumes (no full AST). Errors are returned only
// for catastrophic lexer failures; per-declaration parse errors are
// silently recovered.
func Parse(src, path string) (*File, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, path: path}
	return p.parseFile()
}

type parser struct {
	toks []Token
	pos  int
	path string

	// transient state collected between declarations
	pendingDoc       string
	pendingAvailable *Available
}

func (p *parser) parseFile() (*File, error) {
	f := &File{Path: p.path}
	for !p.atEOF() {
		// Skip attributes that aren't @available; we keep track of the
		// most recent doc + available, which apply to the next decl.
		if p.peek().Kind == tokDocComment {
			p.pendingDoc = p.consume().Value
			continue
		}
		if p.peek().Kind == tokAttribute {
			p.parseAttribute()
			continue
		}
		if p.peek().Kind != tokIdent {
			p.advance()
			continue
		}
		tk := p.consume()
		switch tk.Value {
		case "library":
			f.Library = p.parseDottedIdentifier()
			// Capture any @available that preceded `library ...;` —
			// this is how a whole library is marked deprecated, e.g.
			// @available(deprecated=13, removed=17) library fuchsia.ui.scenic;
			if p.pendingAvailable != nil {
				f.LibraryAvailable = p.pendingAvailable
			}
			p.expectSemi()
			p.resetPending()
		case "using":
			imp := p.parseDottedIdentifier()
			f.Imports = append(f.Imports, imp)
			p.skipToSemi()
			p.resetPending()
		case "protocol", "open", "closed", "ajar":
			openness := ""
			if tk.Value != "protocol" {
				openness = tk.Value
				// next must be `protocol`
				if p.peek().Kind == tokIdent && p.peek().Value == "protocol" {
					p.advance()
				}
			}
			pr := p.parseProtocol(tk.Line, openness)
			if pr != nil {
				f.Protocols = append(f.Protocols, pr)
			}
			p.resetPending()
		case "type":
			td := p.parseTypeDecl(tk.Line)
			if td != nil {
				f.Types = append(f.Types, td)
			}
			p.resetPending()
		case "const":
			cd := p.parseConstDecl(tk.Line)
			if cd != nil {
				f.Constants = append(f.Constants, cd)
			}
			p.resetPending()
		case "alias":
			ad := p.parseAliasDecl(tk.Line)
			if ad != nil {
				f.Aliases = append(f.Aliases, ad)
			}
			p.resetPending()
		case "service", "resource_definition":
			// Not modeled in v1; skip to ;
			p.skipBalancedThenSemi()
			p.resetPending()
		default:
			// Unknown top-level — advance until we recover to a semi.
			p.skipToSemi()
			p.resetPending()
		}
	}
	return f, nil
}

func (p *parser) parseProtocol(line uint32, openness string) *Protocol {
	if p.peek().Kind != tokIdent {
		p.skipBalancedThenSemi()
		return nil
	}
	name := p.consume().Value
	pr := &Protocol{
		Name:      name,
		Doc:       p.pendingDoc,
		Available: p.pendingAvailable,
		Line:      line,
		Openness:  openness,
	}
	if p.peek().Kind != tokLBrace {
		// missing body — bail
		p.skipBalancedThenSemi()
		return pr
	}
	p.advance() // {

	// Track per-method pending doc/available.
	var methodDoc string
	var methodAvail *Available
	var methodFlex string

	for !p.atEOF() && p.peek().Kind != tokRBrace {
		switch p.peek().Kind {
		case tokDocComment:
			methodDoc = p.consume().Value
			continue
		case tokAttribute:
			a := p.parseAttribute()
			if a != nil {
				methodAvail = a
			}
			continue
		}
		tk := p.peek()
		if tk.Kind != tokIdent {
			p.advance()
			continue
		}
		switch tk.Value {
		case "compose":
			p.advance()
			target := p.parseQualifiedComposeName()
			pr.Composes = append(pr.Composes, ComposeRef{Target: target, Line: tk.Line})
			p.expectSemi()
			methodDoc, methodAvail, methodFlex = "", nil, ""
		case "flexible", "strict":
			methodFlex = tk.Value
			p.advance()
		default:
			// Method declaration: Name(...) [-> (...)] [error T];
			m := p.parseMethod()
			if m != nil {
				m.Doc = methodDoc
				m.Available = methodAvail
				if m.Flexibility == "" {
					m.Flexibility = methodFlex
				}
				pr.Methods = append(pr.Methods, m)
			}
			methodDoc, methodAvail, methodFlex = "", nil, ""
		}
	}
	if p.peek().Kind == tokRBrace {
		p.advance()
	}
	// Optional trailing `;`
	if p.peek().Kind == tokSemi {
		p.advance()
	}
	return pr
}

func (p *parser) parseMethod() *Method {
	// Method-name is the current ident.
	tk := p.consume()
	m := &Method{Name: tk.Value, Line: tk.Line}
	// Optional event form: `-> Name(...)`. The parser shouldn't reach
	// this branch via parseProtocol's main loop (event form starts with
	// `->`, not an ident); we treat all current-form decls as having a
	// request.
	m.HasRequest = true
	if p.peek().Kind != tokLParen {
		// Not a method, skip declaration up to ;.
		p.skipToSemi()
		return nil
	}
	// Parse request params — we just collect identifiers we see.
	p.advance() // (
	m.ParamTypeRefs = p.collectIdentsUntil(tokRParen)
	if p.peek().Kind == tokRParen {
		p.advance()
	}
	// Optional `-> ( ... )`
	if p.peek().Kind == tokArrow {
		p.advance()
		m.HasResponse = true
		if p.peek().Kind == tokLParen {
			p.advance()
			m.ResultTypeRefs = p.collectIdentsUntil(tokRParen)
			if p.peek().Kind == tokRParen {
				p.advance()
			}
		}
		// Optional `error T`
		if p.peek().Kind == tokIdent && p.peek().Value == "error" {
			p.advance()
			m.HasError = true
			// skip the error type expression up to ;
		}
	}
	p.skipToSemi()
	return m
}

func (p *parser) parseTypeDecl(line uint32) *TypeDecl {
	if p.peek().Kind != tokIdent {
		p.skipBalancedThenSemi()
		return nil
	}
	name := p.consume().Value
	// expect `=`
	if p.peek().Kind != tokEquals {
		p.skipBalancedThenSemi()
		return &TypeDecl{Name: name, Kind: "other", Doc: p.pendingDoc, Available: p.pendingAvailable, Line: line}
	}
	p.advance() // =
	// Skip optional `strict`/`flexible`/`resource` modifiers.
	for p.peek().Kind == tokIdent {
		v := p.peek().Value
		if v == "strict" || v == "flexible" || v == "resource" {
			p.advance()
			continue
		}
		break
	}
	kind := "other"
	if p.peek().Kind == tokIdent {
		switch p.peek().Value {
		case "struct", "table", "union", "enum", "bits":
			kind = p.consume().Value
		}
	}
	p.skipBalancedThenSemi()
	return &TypeDecl{Name: name, Kind: kind, Doc: p.pendingDoc, Available: p.pendingAvailable, Line: line}
}

func (p *parser) parseConstDecl(line uint32) *ConstDecl {
	if p.peek().Kind != tokIdent {
		p.skipToSemi()
		return nil
	}
	name := p.consume().Value
	p.skipToSemi()
	return &ConstDecl{Name: name, Doc: p.pendingDoc, Available: p.pendingAvailable, Line: line}
}

func (p *parser) parseAliasDecl(line uint32) *AliasDecl {
	if p.peek().Kind != tokIdent {
		p.skipToSemi()
		return nil
	}
	name := p.consume().Value
	p.skipToSemi()
	return &AliasDecl{Name: name, Doc: p.pendingDoc, Available: p.pendingAvailable, Line: line}
}

// parseAttribute reads `@name` followed by optional `(args)`.
// If the attribute is `@available`, the parsed values are stored
// as p.pendingAvailable. All others are ignored (but consumed) so
// they don't pollute parsing.
func (p *parser) parseAttribute() *Available {
	tk := p.consume() // the attribute name token
	if tk.Kind != tokAttribute {
		return nil
	}
	name := tk.Value
	var raw string
	if p.peek().Kind == tokLParen {
		raw = p.captureBalancedParens()
	}
	if name == "available" {
		a := parseAvailableArgs(raw)
		p.pendingAvailable = a
		return a
	}
	return nil
}

// captureBalancedParens consumes a balanced (...) and returns the
// raw text contained inside.
func (p *parser) captureBalancedParens() string {
	if p.peek().Kind != tokLParen {
		return ""
	}
	p.advance()
	var b strings.Builder
	depth := 1
	for !p.atEOF() && depth > 0 {
		tk := p.consume()
		switch tk.Kind {
		case tokLParen:
			depth++
			b.WriteString("(")
		case tokRParen:
			depth--
			if depth == 0 {
				return b.String()
			}
			b.WriteString(")")
		case tokString:
			b.WriteString(`"`)
			b.WriteString(tk.Value)
			b.WriteString(`"`)
		default:
			b.WriteString(tk.Value)
		}
		b.WriteByte(' ')
	}
	return b.String()
}

func parseAvailableArgs(raw string) *Available {
	a := &Available{}
	// raw looks like: added = 7 , deprecated = 27 , removed = HEAD , note = "..."
	pairs := splitTopLevelCommas(raw)
	for _, kv := range pairs {
		kv = strings.TrimSpace(kv)
		eq := strings.Index(kv, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(kv[:eq])
		val := strings.TrimSpace(kv[eq+1:])
		val = strings.Trim(val, `"`)
		switch key {
		case "added":
			a.Added = val
		case "deprecated":
			a.Deprecated = val
		case "removed":
			a.Removed = val
		case "note":
			a.Note = val
		}
	}
	return a
}

func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	last := 0
	for i, ch := range s {
		switch ch {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	out = append(out, s[last:])
	return out
}

// parseDottedIdentifier reads `a.b.c` and returns the joined string.
func (p *parser) parseDottedIdentifier() string {
	var parts []string
	for {
		if p.peek().Kind != tokIdent {
			break
		}
		parts = append(parts, p.consume().Value)
		if p.peek().Kind == tokPunct && p.peek().Value == "." {
			p.advance()
			continue
		}
		break
	}
	return strings.Join(parts, ".")
}

// parseQualifiedComposeName reads `Foo` or `lib.path/Name` or
// `lib.path.Name` (the dotted form). Returns the raw text read.
func (p *parser) parseQualifiedComposeName() string {
	var parts []string
	for {
		tk := p.peek()
		switch tk.Kind {
		case tokIdent, tokNumber:
			parts = append(parts, p.consume().Value)
		case tokPunct:
			if tk.Value == "." || tk.Value == "/" {
				parts = append(parts, tk.Value)
				p.advance()
				continue
			}
			return strings.Join(parts, "")
		default:
			return strings.Join(parts, "")
		}
	}
}

// collectIdentsUntil collects all tokIdent values seen until the
// given closing token kind (paren). Useful for harvesting type names
// from parameter lists without modeling parameters fully.
func (p *parser) collectIdentsUntil(closing TokenKind) []string {
	var idents []string
	depth := 0
	for !p.atEOF() {
		tk := p.peek()
		if tk.Kind == closing && depth == 0 {
			return idents
		}
		switch tk.Kind {
		case tokLParen, tokLBrace, tokLAngle, tokLBracket:
			depth++
		case tokRParen, tokRBrace, tokRAngle, tokRBracket:
			if depth == 0 {
				return idents
			}
			depth--
		case tokIdent:
			v := tk.Value
			if !isFidlKeyword(v) {
				idents = append(idents, v)
			}
		}
		p.advance()
	}
	return idents
}

func isFidlKeyword(s string) bool {
	switch s {
	case "struct", "table", "union", "enum", "bits", "resource", "vector",
		"string", "array", "bool", "byte", "client_end", "server_end",
		"strict", "flexible", "reserved", "uint8", "uint16", "uint32",
		"uint64", "int8", "int16", "int32", "int64", "float32", "float64":
		return true
	}
	return false
}

// resetPending clears the pending doc + available; called after a
// declaration has consumed them.
func (p *parser) resetPending() {
	p.pendingDoc = ""
	p.pendingAvailable = nil
}

// expectSemi consumes a ; if present; tolerates absence.
func (p *parser) expectSemi() {
	if p.peek().Kind == tokSemi {
		p.advance()
	}
}

// skipToSemi advances until the next ; at depth 0 (parens/braces).
func (p *parser) skipToSemi() {
	depth := 0
	for !p.atEOF() {
		tk := p.peek()
		switch tk.Kind {
		case tokLParen, tokLBrace, tokLAngle, tokLBracket:
			depth++
		case tokRParen, tokRBrace, tokRAngle, tokRBracket:
			if depth > 0 {
				depth--
			}
		case tokSemi:
			if depth == 0 {
				p.advance()
				return
			}
		}
		p.advance()
	}
}

// skipBalancedThenSemi advances until { ... } is balanced AND a ;
// is reached at depth 0 (the trailing semi after type bodies).
func (p *parser) skipBalancedThenSemi() {
	depth := 0
	sawBrace := false
	for !p.atEOF() {
		tk := p.peek()
		switch tk.Kind {
		case tokLBrace:
			depth++
			sawBrace = true
		case tokRBrace:
			if depth > 0 {
				depth--
			}
		case tokSemi:
			if depth == 0 && (sawBrace || true) {
				p.advance()
				return
			}
		}
		p.advance()
	}
}

func (p *parser) peek() Token {
	if p.pos >= len(p.toks) {
		return Token{Kind: tokEOF}
	}
	return p.toks[p.pos]
}

func (p *parser) consume() Token {
	tk := p.peek()
	p.advance()
	return tk
}

func (p *parser) advance() {
	p.pos++
}

func (p *parser) atEOF() bool {
	return p.peek().Kind == tokEOF
}

// String returns a debug rendering of the parsed file.
func (f *File) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "library %s\n", f.Library)
	for _, p := range f.Protocols {
		fmt.Fprintf(&b, "  protocol %s (composes=%d, methods=%d)\n", p.Name, len(p.Composes), len(p.Methods))
		for _, c := range p.Composes {
			fmt.Fprintf(&b, "    compose %s\n", c.Target)
		}
		for _, m := range p.Methods {
			fmt.Fprintf(&b, "    %s%s%s\n", m.Name, twoWayMarker(m), errorMarker(m))
		}
	}
	for _, t := range f.Types {
		fmt.Fprintf(&b, "  type %s (%s)\n", t.Name, t.Kind)
	}
	return b.String()
}

func twoWayMarker(m *Method) string {
	if m.HasResponse {
		return " -> "
	}
	return " (oneway)"
}
func errorMarker(m *Method) string {
	if m.HasError {
		return " error"
	}
	return ""
}
