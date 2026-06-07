package cppheader

import (
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// ----------------------------------------------------------------
// Declaration parsers
// ----------------------------------------------------------------

func (w *walker) parseNamespace(i int) int {
	j := i + 1
	if j < len(w.toks) && w.toks[j].kind == tkIdent && w.toks[j].text == "inline" {
		j++
	}
	var segs []string
	for j < len(w.toks) {
		if w.toks[j].kind != tkIdent {
			break
		}
		segs = append(segs, w.toks[j].text)
		j++
		if j < len(w.toks) && w.toks[j].kind == tkScope {
			j++
			continue
		}
		break
	}
	if j >= len(w.toks) {
		return i + 1
	}
	if w.toks[j].kind == tkPunct && w.toks[j].text == "=" {
		return w.skipStatement(i)
	}
	if !(w.toks[j].kind == tkPunct && w.toks[j].text == "{") {
		return i + 1
	}
	w.braceDepth++
	if len(segs) == 0 {
		w.scopes = append(w.scopes, scope{kind: skAnonNamespace, depth: w.braceDepth})
	} else {
		for _, s := range segs {
			w.scopes = append(w.scopes, scope{kind: skNamespace, name: s, depth: w.braceDepth})
		}
	}
	return j + 1
}

func (w *walker) parseTemplate(i int) int {
	j := i + 1
	if j >= len(w.toks) || !(w.toks[j].kind == tkPunct && w.toks[j].text == "<") {
		return i + 1
	}
	depth, start := 1, j
	j++
	for j < len(w.toks) && depth > 0 {
		t := w.toks[j]
		if t.kind == tkPunct {
			switch t.text {
			case "<":
				depth++
			case ">":
				depth--
			}
		}
		j++
	}
	if depth != 0 {
		return i + 1
	}
	if j-1-start <= 1 {
		w.pendingSpec = true
		w.pendingTemplate = false
		w.templateText = "template<>"
	} else {
		w.pendingSpec = false
		w.pendingTemplate = true
		w.templateText = "template<...>"
	}
	return j
}

func (w *walker) parseClassLike(i int, kind scopeKind, defaultAccess string) (int, *contractpb.ContractElement) {
	declLine := w.toks[i].line
	hadSpec := w.pendingSpec
	templateText := w.templateText
	w.pendingTemplate, w.pendingSpec, w.templateText = false, false, ""

	j := i + 1
	j = w.skipAttributeMacros(j)
	if j >= len(w.toks) || w.toks[j].kind != tkIdent {
		return i + 1, nil
	}
	name := w.toks[j].text
	j++
	j = w.skipAttributeMacros(j)
	if j < len(w.toks) && w.toks[j].kind == tkIdent && w.toks[j].text == "final" {
		j++
	}
	if j < len(w.toks) && w.toks[j].kind == tkPunct && w.toks[j].text == ":" {
		for j < len(w.toks) && !(w.toks[j].kind == tkPunct && (w.toks[j].text == "{" || w.toks[j].text == ";")) {
			j++
		}
	}
	if j >= len(w.toks) {
		return i + 1, nil
	}
	if w.toks[j].text == ";" {
		return j + 1, nil
	}
	if w.toks[j].text != "{" {
		return i + 1, nil
	}
	if hadSpec {
		w.braceDepth++
		w.scopes = append(w.scopes, scope{kind: kind, name: name, access: defaultAccess, depth: w.braceDepth})
		return j + 1, nil
	}
	doc := combineDoc(templateText, w.findDocCommentBefore(declLine))
	qn := w.qualifiedName(name)
	elem := &contractpb.ContractElement{
		Id:                w.library + "/" + qn,
		Kind:              contractpb.ContractElementKind_CPP_CLASS,
		Ecosystem:         Name,
		Library:           w.library,
		Location:          &commonpb.SourceLocation{Path: w.path, Line: declLine},
		DocCommentExcerpt: doc,
		Aliases:           cppAliases(qn, name),
	}
	w.braceDepth++
	w.scopes = append(w.scopes, scope{kind: kind, name: name, access: defaultAccess, depth: w.braceDepth})
	return j + 1, elem
}

func (w *walker) parseEnum(i int) (int, *contractpb.ContractElement) {
	declLine := w.toks[i].line
	w.pendingTemplate, w.pendingSpec, w.templateText = false, false, ""
	j := i + 1
	if j < len(w.toks) && w.toks[j].kind == tkIdent && (w.toks[j].text == "class" || w.toks[j].text == "struct") {
		j++
	}
	if j >= len(w.toks) || w.toks[j].kind != tkIdent {
		return i + 1, nil
	}
	name := w.toks[j].text
	j++
	if j < len(w.toks) && w.toks[j].kind == tkPunct && w.toks[j].text == ":" {
		for j < len(w.toks) && !(w.toks[j].kind == tkPunct && (w.toks[j].text == "{" || w.toks[j].text == ";")) {
			j++
		}
	}
	if j >= len(w.toks) {
		return i + 1, nil
	}
	if w.toks[j].text == ";" {
		return j + 1, nil
	}
	if w.toks[j].text != "{" {
		return i + 1, nil
	}
	qn := w.qualifiedName(name)
	elem := &contractpb.ContractElement{
		Id:                w.library + "/" + qn,
		Kind:              contractpb.ContractElementKind_TYPE,
		Ecosystem:         Name,
		Library:           w.library,
		Location:          &commonpb.SourceLocation{Path: w.path, Line: declLine},
		DocCommentExcerpt: w.findDocCommentBefore(declLine),
		Aliases:           cppAliases(qn, name),
	}
	w.braceDepth++
	w.scopes = append(w.scopes, scope{kind: skEnum, name: name, depth: w.braceDepth})
	return j + 1, elem
}

func (w *walker) parseDefine(t token) *contractpb.ContractElement {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t.text), "#"))
	if !strings.HasPrefix(s, "define") {
		return nil
	}
	rest := strings.TrimSpace(strings.TrimPrefix(s, "define"))
	end := 0
	for end < len(rest) && isIdentPart(rest[end]) {
		end++
	}
	if end == 0 {
		return nil
	}
	name := rest[:end]
	// Skip include guards.
	if strings.HasSuffix(name, "_H") || strings.HasSuffix(name, "_HPP") || strings.HasSuffix(name, "_H_") {
		return nil
	}
	qn := w.qualifiedName(name)
	return &contractpb.ContractElement{
		Id:                w.library + "/" + qn,
		Kind:              contractpb.ContractElementKind_CPP_MACRO,
		Ecosystem:         Name,
		Library:           w.library,
		Location:          &commonpb.SourceLocation{Path: w.path, Line: t.line},
		DocCommentExcerpt: w.findDocCommentBefore(t.line),
		Aliases:           cppAliases(qn, name),
	}
}

// tryParseFunc dispatches on current scope to recognize a function-
// shaped declaration: free function at file/ns scope, public member
// inside a class, or out-of-class definition `T Class::method(...)`.
func (w *walker) tryParseFunc(i int) (int, *contractpb.ContractElement) {
	atFile := w.atFileOrNSScope() && !w.inAnonNS()
	cs := w.topClassLike()
	inPublicClass := cs != nil && cs.access == "public" && !w.inAnonNS() && !w.inFunc()

	declLine := w.toks[i].line
	hadSpec := w.pendingSpec
	templateText := w.templateText

	// Out-of-class definition (only valid at file/ns scope).
	if atFile {
		if ni, elem := w.parseOutOfClass(i, declLine, hadSpec, templateText); ni > i {
			return ni, elem
		}
	}

	if !atFile && !inPublicClass {
		return i, nil
	}

	parenIdx, nameIdx := w.findFreeFuncOpener(i)
	if parenIdx < 0 {
		return i, nil
	}
	if hadSpec {
		w.resetTemplate()
		return w.skipStatement(i), nil
	}
	closeIdx := w.matchParen(parenIdx)
	if closeIdx < 0 {
		return i, nil
	}
	termIdx, termKind := w.findStatementTerminator(closeIdx + 1)
	if termIdx < 0 {
		return i, nil
	}
	kind := contractpb.ContractElementKind_CPP_FREE_FUNCTION
	if inPublicClass {
		kind = contractpb.ContractElementKind_METHOD
	}
	name := w.toks[nameIdx].text
	qn := w.qualifiedName(name)
	elem := &contractpb.ContractElement{
		Id:                w.library + "/" + qn,
		Kind:              kind,
		Ecosystem:         Name,
		Library:           w.library,
		Location:          &commonpb.SourceLocation{Path: w.path, Line: w.toks[nameIdx].line},
		DocCommentExcerpt: combineDoc(templateText, w.findDocCommentBefore(declLine)),
		Aliases:           cppAliases(qn, name),
	}
	w.resetTemplate()
	if termKind == "{" {
		w.braceDepth++
		w.scopes = append(w.scopes, scope{kind: skFunc, depth: w.braceDepth})
	}
	return termIdx + 1, elem
}

func (w *walker) parseOutOfClass(i int, declLine uint32, hadSpec bool, templateText string) (int, *contractpb.ContractElement) {
	j := i
	for k := 0; k < 30 && j+3 < len(w.toks); k++ {
		if w.toks[j].kind == tkIdent && w.toks[j+1].kind == tkScope && w.toks[j+2].kind == tkIdent && w.toks[j+3].kind == tkPunct && w.toks[j+3].text == "(" {
			break
		}
		if w.toks[j].kind == tkPunct {
			switch w.toks[j].text {
			case ";", "{", "}":
				return i, nil
			}
		}
		j++
	}
	if j+3 >= len(w.toks) {
		return i, nil
	}
	if !(w.toks[j].kind == tkIdent && w.toks[j+1].kind == tkScope && w.toks[j+2].kind == tkIdent && w.toks[j+3].kind == tkPunct && w.toks[j+3].text == "(") {
		return i, nil
	}
	className, methodName := w.toks[j].text, w.toks[j+2].text
	closeIdx := w.matchParen(j + 3)
	if closeIdx < 0 {
		return i, nil
	}
	termIdx, termKind := w.findStatementTerminator(closeIdx + 1)
	if termIdx < 0 {
		return i, nil
	}
	if hadSpec {
		w.resetTemplate()
		if termKind == "{" {
			w.braceDepth++
			w.scopes = append(w.scopes, scope{kind: skFunc, depth: w.braceDepth})
		}
		return termIdx + 1, nil
	}
	qn := className + "::" + methodName
	if ns := w.nsPath(); ns != "" {
		qn = ns + "::" + qn
	}
	elem := &contractpb.ContractElement{
		Id:                w.library + "/" + qn,
		Kind:              contractpb.ContractElementKind_METHOD,
		Ecosystem:         Name,
		Library:           w.library,
		Location:          &commonpb.SourceLocation{Path: w.path, Line: w.toks[j].line},
		DocCommentExcerpt: combineDoc(templateText, w.findDocCommentBefore(declLine)),
		Aliases:           cppAliases(qn, methodName),
	}
	w.resetTemplate()
	if termKind == "{" {
		w.braceDepth++
		w.scopes = append(w.scopes, scope{kind: skFunc, depth: w.braceDepth})
	}
	return termIdx + 1, elem
}

func (w *walker) resetTemplate() {
	w.pendingSpec, w.pendingTemplate, w.templateText = false, false, ""
}

// findFreeFuncOpener walks forward from i looking for `Ident (` that
// represents a function name + opening paren. Skips attribute macros
// and `[[ ... ]]` C++ attributes.
func (w *walker) findFreeFuncOpener(i int) (parenIdx, nameIdx int) {
	j := i
	for k := 0; k < 40 && j < len(w.toks); k++ {
		t := w.toks[j]
		if t.kind == tkPunct {
			switch t.text {
			case ";", "}", "{":
				return -1, -1
			case "[":
				if j+1 < len(w.toks) && w.toks[j+1].kind == tkPunct && w.toks[j+1].text == "[" {
					depth := 2
					j += 2
					for j < len(w.toks) && depth > 0 {
						if w.toks[j].kind == tkPunct {
							if w.toks[j].text == "[" {
								depth++
							} else if w.toks[j].text == "]" {
								depth--
							}
						}
						j++
					}
					continue
				}
				return -1, -1
			case "(":
				closer := w.matchParen(j)
				if closer < 0 {
					return -1, -1
				}
				j = closer + 1
				continue
			}
		}
		if t.kind == tkIdent && w.a.ignoredAttrs[t.text] {
			j++
			if j < len(w.toks) && w.toks[j].kind == tkPunct && w.toks[j].text == "(" {
				closer := w.matchParen(j)
				if closer < 0 {
					return -1, -1
				}
				j = closer + 1
			}
			continue
		}
		if t.kind == tkPunct && t.text == "~" && j+2 < len(w.toks) && w.toks[j+1].kind == tkIdent && w.toks[j+2].kind == tkPunct && w.toks[j+2].text == "(" {
			return j + 2, j + 1
		}
		if t.kind == tkIdent && j+1 < len(w.toks) && w.toks[j+1].kind == tkPunct && w.toks[j+1].text == "(" {
			if isReservedNonFunc(t.text) {
				j++
				continue
			}
			return j + 1, j
		}
		j++
	}
	return -1, -1
}

func isReservedNonFunc(s string) bool {
	switch s {
	case "if", "while", "for", "switch", "return", "throw", "sizeof", "alignof",
		"static_cast", "dynamic_cast", "reinterpret_cast", "const_cast",
		"decltype", "typeid", "noexcept", "requires":
		return true
	}
	return false
}

func (w *walker) matchParen(openIdx int) int {
	depth, j := 1, openIdx+1
	for j < len(w.toks) {
		t := w.toks[j]
		if t.kind == tkPunct {
			if t.text == "(" {
				depth++
			} else if t.text == ")" {
				depth--
				if depth == 0 {
					return j
				}
			}
		}
		j++
	}
	return -1
}

func (w *walker) findStatementTerminator(i int) (int, string) {
	j := i
	for k := 0; k < 80 && j < len(w.toks); k++ {
		t := w.toks[j]
		if t.kind == tkPunct {
			switch t.text {
			case ";":
				return j, ";"
			case "{":
				return j, "{"
			case "}":
				return -1, ""
			}
		}
		j++
	}
	return -1, ""
}

func (w *walker) skipStatement(i int) int {
	j, depth := i, 0
	for j < len(w.toks) {
		t := w.toks[j]
		if t.kind == tkPunct {
			switch t.text {
			case "{":
				depth++
			case "}":
				if depth == 0 {
					return j
				}
				depth--
			case ";":
				if depth == 0 {
					return j + 1
				}
			}
		}
		j++
	}
	return j
}

func (w *walker) skipAttributeMacros(j int) int {
	for j < len(w.toks) {
		t := w.toks[j]
		if t.kind == tkIdent && w.a.ignoredAttrs[t.text] {
			j++
			if j < len(w.toks) && w.toks[j].kind == tkPunct && w.toks[j].text == "(" {
				closer := w.matchParen(j)
				if closer < 0 {
					return j
				}
				j = closer + 1
			}
			continue
		}
		if t.kind == tkPunct && t.text == "[" && j+1 < len(w.toks) && w.toks[j+1].kind == tkPunct && w.toks[j+1].text == "[" {
			depth := 2
			j += 2
			for j < len(w.toks) && depth > 0 {
				if w.toks[j].kind == tkPunct {
					if w.toks[j].text == "[" {
						depth++
					} else if w.toks[j].text == "]" {
						depth--
					}
				}
				j++
			}
			continue
		}
		return j
	}
	return j
}

// findDocCommentBefore finds the nearest enabled-style doc comment
// whose end line is within ~3 lines above declLine.
func (w *walker) findDocCommentBefore(declLine uint32) string {
	if declLine == 0 || len(w.comments) == 0 {
		return ""
	}
	for i := len(w.comments) - 1; i >= 0; i-- {
		c := w.comments[i]
		if !w.a.docStyles[c.style] {
			continue
		}
		if c.endLn >= declLine {
			continue
		}
		if declLine-c.endLn <= 3 {
			text := strings.TrimSpace(c.text)
			if len(text) > 240 {
				text = text[:240] + "…"
			}
			return text
		}
		return ""
	}
	return ""
}

func combineDoc(templateText, doc string) string {
	if templateText == "" {
		return doc
	}
	if doc == "" {
		return templateText
	}
	return templateText + " " + doc
}

// cppAliases returns the bare local name plus a dotted form of the
// qualified name (so codegen_bridge can match against proto-style
// dotted packages).
func cppAliases(qualifiedName, localName string) []string {
	out := []string{localName}
	dotted := strings.ReplaceAll(qualifiedName, "::", ".")
	if dotted != localName && dotted != qualifiedName {
		out = append(out, dotted)
	}
	return out
}

var _ adapters.ContractAnchorParser = (*Adapter)(nil)
