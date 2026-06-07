package cppheader

import "strings"

// ----------------------------------------------------------------
// Tokenization
// ----------------------------------------------------------------

type tokenKind int

const (
	tkIdent tokenKind = iota
	tkNumber
	tkPunct
	tkScope // ::
	tkPreproc
)

type token struct {
	kind tokenKind
	text string
	line uint32
}

type docComment struct {
	style   string
	text    string
	startLn uint32
	endLn   uint32
}

func tokenize(src string) ([]token, []docComment) {
	var toks []token
	var comments []docComment
	line := uint32(1)
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '/' && i+1 < n && src[i+1] == '/':
			start := i + 2
			j := start
			for j < n && src[j] != '\n' {
				j++
			}
			text := src[start:j]
			if strings.HasPrefix(text, "/") {
				comments = append(comments, docComment{
					style:   "triple_slash",
					text:    strings.TrimSpace(strings.TrimPrefix(text, "/")),
					startLn: line, endLn: line,
				})
			}
			i = j
		case c == '/' && i+1 < n && src[i+1] == '*':
			start := i + 2
			isJavadoc := start < n && src[start] == '*' && !(start+1 < n && src[start+1] == '/')
			startLn := line
			j := start
			for j+1 < n && !(src[j] == '*' && src[j+1] == '/') {
				if src[j] == '\n' {
					line++
				}
				j++
			}
			if isJavadoc {
				comments = append(comments, docComment{
					style: "javadoc", text: stripJavadoc(src[start:j]),
					startLn: startLn, endLn: line,
				})
			}
			if j+1 < n {
				i = j + 2
			} else {
				i = n
			}
		case c == '"' || c == '\'':
			quote := c
			i++
			for i < n {
				if src[i] == '\\' && i+1 < n {
					if src[i+1] == '\n' {
						line++
					}
					i += 2
					continue
				}
				if src[i] == '\n' {
					line++
					i++
					continue
				}
				if src[i] == quote {
					i++
					break
				}
				i++
			}
		case c == '#':
			startLn := line
			j := i
			for j < n {
				if src[j] == '\\' && j+1 < n && src[j+1] == '\n' {
					line++
					j += 2
					continue
				}
				if src[j] == '\n' {
					break
				}
				j++
			}
			toks = append(toks, token{kind: tkPreproc, text: src[i:j], line: startLn})
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentPart(src[j]) {
				j++
			}
			toks = append(toks, token{kind: tkIdent, text: src[i:j], line: line})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < n && (isIdentPart(src[j]) || src[j] == '.') {
				j++
			}
			toks = append(toks, token{kind: tkNumber, text: src[i:j], line: line})
			i = j
		case c == ':' && i+1 < n && src[i+1] == ':':
			toks = append(toks, token{kind: tkScope, text: "::", line: line})
			i += 2
		default:
			toks = append(toks, token{kind: tkPunct, text: string(c), line: line})
			i++
		}
	}
	return toks, comments
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}
func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func stripJavadoc(text string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "*")
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, " ")
}
