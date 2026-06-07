// Package fidl implements a direct FIDL source parser.
//
// This package does NOT validate FIDL — it assumes the source has
// already passed fidlc. It extracts the structural shape Sheaf cares
// about: library, protocols, methods, composes, types, doc comments,
// and @available annotations.

package fidl

import (
	"fmt"
	"strings"
	"unicode"
)

// TokenKind identifies a token's class.
type TokenKind int

const (
	tokEOF        TokenKind = iota
	tokIdent                // identifier or keyword
	tokString               // "..."
	tokNumber               // 123 or 0xFF
	tokDocComment           // ///-prefixed comment block (joined)
	tokAttribute            // @attrname (the @ is consumed; value is the attr name)
	tokLBrace               // {
	tokRBrace               // }
	tokLParen               // (
	tokRParen               // )
	tokLBracket             // [
	tokRBracket             // ]
	tokLAngle               // <
	tokRAngle               // >
	tokColon                // :
	tokComma                // ,
	tokSemi                 // ;
	tokEquals               // =
	tokPipe                 // |
	tokQuestion             // ?
	tokAmpersand            // &
	tokArrow                // ->
	tokPunct                // any other punctuation we don't classify (e.g. /, *, +)
)

// Token is a single lexed token.
type Token struct {
	Kind  TokenKind
	Value string
	Line  uint32
	Col   uint32
}

func (t Token) String() string {
	return fmt.Sprintf("%v(%q)@%d:%d", t.Kind, t.Value, t.Line, t.Col)
}

// Lex tokenizes src. Doc comments (`/// ...`) are coalesced into a
// single tokDocComment containing the joined text of consecutive
// `///` lines (with the prefix stripped). Regular `//` and block
// `/* */` comments are skipped.
func Lex(src string) ([]Token, error) {
	l := &lexer{src: src, line: 1, col: 1}
	for {
		l.skipWhitespaceAndComments()
		if l.pos >= len(l.src) {
			break
		}
		startLine, startCol := l.line, l.col
		ch := l.src[l.pos]
		switch {
		case ch == '/' && l.peek(1) == '/' && l.peek(2) == '/':
			// Doc comment — coalesce consecutive /// lines.
			text := l.readDocCommentBlock()
			l.emit(Token{Kind: tokDocComment, Value: text, Line: startLine, Col: startCol})
		case ch == '@':
			l.advance(1)
			name := l.readIdent()
			if name == "" {
				// Malformed: stray @ with no identifier. Emit a plain
				// punct so the parser can recover instead of aborting
				// the whole file.
				l.emit(Token{Kind: tokPunct, Value: "@", Line: startLine, Col: startCol})
				continue
			}
			l.emit(Token{Kind: tokAttribute, Value: name, Line: startLine, Col: startCol})
		case ch == '"':
			s, err := l.readString()
			if err != nil {
				return nil, err
			}
			l.emit(Token{Kind: tokString, Value: s, Line: startLine, Col: startCol})
		case isIdentStart(rune(ch)):
			id := l.readIdent()
			l.emit(Token{Kind: tokIdent, Value: id, Line: startLine, Col: startCol})
		case isDigit(rune(ch)):
			n := l.readNumber()
			l.emit(Token{Kind: tokNumber, Value: n, Line: startLine, Col: startCol})
		case ch == '-' && l.peek(1) == '>':
			l.advance(2)
			l.emit(Token{Kind: tokArrow, Value: "->", Line: startLine, Col: startCol})
		default:
			// single-char punctuation.
			l.advance(1)
			var k TokenKind
			switch ch {
			case '{':
				k = tokLBrace
			case '}':
				k = tokRBrace
			case '(':
				k = tokLParen
			case ')':
				k = tokRParen
			case '[':
				k = tokLBracket
			case ']':
				k = tokRBracket
			case '<':
				k = tokLAngle
			case '>':
				k = tokRAngle
			case ':':
				k = tokColon
			case ',':
				k = tokComma
			case ';':
				k = tokSemi
			case '=':
				k = tokEquals
			case '|':
				k = tokPipe
			case '?':
				k = tokQuestion
			case '&':
				k = tokAmpersand
			default:
				k = tokPunct
			}
			l.emit(Token{Kind: k, Value: string(ch), Line: startLine, Col: startCol})
		}
	}
	l.emit(Token{Kind: tokEOF, Line: l.line, Col: l.col})
	return l.tokens, nil
}

type lexer struct {
	src    string
	pos    int
	line   uint32
	col    uint32
	tokens []Token
}

func (l *lexer) peek(n int) byte {
	if l.pos+n >= len(l.src) {
		return 0
	}
	return l.src[l.pos+n]
}

func (l *lexer) advance(n int) {
	for i := 0; i < n && l.pos < len(l.src); i++ {
		if l.src[l.pos] == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
		l.pos++
	}
}

func (l *lexer) emit(t Token) {
	l.tokens = append(l.tokens, t)
}

func (l *lexer) errf(line, col uint32, format string, a ...any) error {
	return fmt.Errorf("fidl: line %d col %d: "+format, append([]any{line, col}, a...)...)
}

// skipWhitespaceAndComments advances over whitespace, `//` line
// comments (non-doc), and `/* */` block comments. Doc comments (`///`)
// are NOT skipped — they're real tokens.
func (l *lexer) skipWhitespaceAndComments() {
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			l.advance(1)
		case ch == '/' && l.peek(1) == '/' && l.peek(2) != '/':
			// non-doc line comment
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.advance(1)
			}
		case ch == '/' && l.peek(1) == '*':
			l.advance(2)
			for l.pos < len(l.src) {
				if l.src[l.pos] == '*' && l.peek(1) == '/' {
					l.advance(2)
					break
				}
				l.advance(1)
			}
		default:
			return
		}
	}
}

// readDocCommentBlock consumes a run of consecutive `///` lines and
// returns the concatenated text with the prefix and one leading space
// (if present) stripped. Lines are joined with single newlines.
func (l *lexer) readDocCommentBlock() string {
	var lines []string
	for {
		// Already positioned at start of a /// line.
		if l.pos+3 > len(l.src) || l.src[l.pos:l.pos+3] != "///" {
			break
		}
		l.advance(3)
		// Strip a single leading space if present.
		if l.pos < len(l.src) && l.src[l.pos] == ' ' {
			l.advance(1)
		}
		start := l.pos
		for l.pos < len(l.src) && l.src[l.pos] != '\n' {
			l.advance(1)
		}
		lines = append(lines, l.src[start:l.pos])
		// consume newline
		if l.pos < len(l.src) && l.src[l.pos] == '\n' {
			l.advance(1)
		}
		// skip leading whitespace on next line; check whether it begins ///.
		mark := l.pos
		for l.pos < len(l.src) && (l.src[l.pos] == ' ' || l.src[l.pos] == '\t') {
			l.advance(1)
		}
		if l.pos+3 > len(l.src) || l.src[l.pos:l.pos+3] != "///" {
			// Not another doc-comment line — rewind whitespace consumed.
			l.pos = mark
			break
		}
	}
	return strings.Join(lines, "\n")
}

func (l *lexer) readString() (string, error) {
	// opening "
	startLine, startCol := l.line, l.col
	l.advance(1)
	var b strings.Builder
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == '"' {
			l.advance(1)
			return b.String(), nil
		}
		if ch == '\\' && l.pos+1 < len(l.src) {
			next := l.src[l.pos+1]
			b.WriteByte(next)
			l.advance(2)
			continue
		}
		b.WriteByte(ch)
		l.advance(1)
	}
	return "", l.errf(startLine, startCol, "unterminated string")
}

func (l *lexer) readIdent() string {
	start := l.pos
	for l.pos < len(l.src) && (isIdentPart(rune(l.src[l.pos]))) {
		l.advance(1)
	}
	return l.src[start:l.pos]
}

func (l *lexer) readNumber() string {
	start := l.pos
	// Hex prefix
	if l.peek(0) == '0' && (l.peek(1) == 'x' || l.peek(1) == 'X') {
		l.advance(2)
		for l.pos < len(l.src) && isHexDigit(rune(l.src[l.pos])) {
			l.advance(1)
		}
		return l.src[start:l.pos]
	}
	for l.pos < len(l.src) && (isDigit(rune(l.src[l.pos])) || l.src[l.pos] == '.') {
		l.advance(1)
	}
	return l.src[start:l.pos]
}

func isIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}
func isIdentPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}
func isHexDigit(r rune) bool {
	return isDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func (k TokenKind) String() string {
	switch k {
	case tokEOF:
		return "EOF"
	case tokIdent:
		return "IDENT"
	case tokString:
		return "STRING"
	case tokNumber:
		return "NUMBER"
	case tokDocComment:
		return "DOC"
	case tokAttribute:
		return "ATTR"
	case tokLBrace:
		return "{"
	case tokRBrace:
		return "}"
	case tokLParen:
		return "("
	case tokRParen:
		return ")"
	case tokLBracket:
		return "["
	case tokRBracket:
		return "]"
	case tokLAngle:
		return "<"
	case tokRAngle:
		return ">"
	case tokColon:
		return ":"
	case tokComma:
		return ","
	case tokSemi:
		return ";"
	case tokEquals:
		return "="
	case tokPipe:
		return "|"
	case tokQuestion:
		return "?"
	case tokAmpersand:
		return "&"
	case tokArrow:
		return "->"
	default:
		return "PUNCT"
	}
}
