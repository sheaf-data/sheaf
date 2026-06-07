package cppheader

import (
	"strings"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// ----------------------------------------------------------------
// Walking
// ----------------------------------------------------------------

type scopeKind int

const (
	skNamespace scopeKind = iota
	skAnonNamespace
	skClass
	skStruct
	skUnion
	skEnum
	skFunc
	skOther
)

type scope struct {
	kind   scopeKind
	name   string
	access string
	depth  int
}

type walker struct {
	toks            []token
	comments        []docComment
	a               *Adapter
	path            string
	library         string
	scopes          []scope
	braceDepth      int
	pendingTemplate bool
	pendingSpec     bool
	templateText    string
}

func (a *Adapter) parseFile(path, library, src string) []*contractpb.ContractElement {
	toks, comments := tokenize(src)
	w := &walker{toks: toks, comments: comments, a: a, path: path, library: library}
	return w.walk()
}

func (w *walker) walk() []*contractpb.ContractElement {
	var out []*contractpb.ContractElement
	emit := func(e *contractpb.ContractElement) {
		if e != nil && !w.inAnonNS() {
			out = append(out, e)
		}
	}
	step := func(i, ni int) (int, bool) {
		if ni > i {
			return ni, true
		}
		return i, false
	}
	for i := 0; i < len(w.toks); {
		t := w.toks[i]
		if t.kind == tkPreproc {
			if w.a.emitMacros && !w.inAnonNS() && w.atFileOrNSScope() {
				emit(w.parseDefine(t))
			}
			i++
			continue
		}
		if t.kind == tkPunct {
			switch t.text {
			case "{":
				w.braceDepth++
				w.scopes = append(w.scopes, scope{kind: skOther, depth: w.braceDepth})
				i++
				continue
			case "}":
				w.popBrace()
				i++
				continue
			}
		}
		if t.kind == tkIdent {
			switch t.text {
			case "namespace":
				if ni, ok := step(i, w.parseNamespace(i)); ok {
					i = ni
					continue
				}
			case "class", "struct", "union":
				kind, acc := skClass, "private"
				if t.text == "struct" {
					kind, acc = skStruct, "public"
				} else if t.text == "union" {
					kind, acc = skUnion, "public"
				}
				ni, elem := w.parseClassLike(i, kind, acc)
				emit(elem)
				if ni > i {
					i = ni
					continue
				}
			case "enum":
				ni, elem := w.parseEnum(i)
				emit(elem)
				if ni > i {
					i = ni
					continue
				}
			case "template":
				if ni, ok := step(i, w.parseTemplate(i)); ok {
					i = ni
					continue
				}
			case "public", "private", "protected":
				if cs := w.topClassLike(); cs != nil && i+1 < len(w.toks) && w.toks[i+1].kind == tkPunct && w.toks[i+1].text == ":" {
					cs.access = t.text
					i += 2
					continue
				}
			case "friend", "using", "typedef":
				i = w.skipStatement(i)
				continue
			}
		}
		// Function-shaped declarations.
		if ni, elem := w.tryParseFunc(i); ni > i {
			emit(elem)
			i = ni
			continue
		}
		i++
	}
	return out
}

func (w *walker) popBrace() {
	if w.braceDepth == 0 {
		return
	}
	for len(w.scopes) > 0 && w.scopes[len(w.scopes)-1].depth == w.braceDepth {
		w.scopes = w.scopes[:len(w.scopes)-1]
	}
	w.braceDepth--
}

func (w *walker) inAnonNS() bool {
	for _, s := range w.scopes {
		if s.kind == skAnonNamespace {
			return true
		}
	}
	return false
}

func (w *walker) inFunc() bool {
	for _, s := range w.scopes {
		if s.kind == skFunc {
			return true
		}
	}
	return false
}

func (w *walker) atFileOrNSScope() bool {
	if len(w.scopes) == 0 {
		return true
	}
	switch w.scopes[len(w.scopes)-1].kind {
	case skNamespace, skAnonNamespace:
		return true
	default:
		return false
	}
}

func (w *walker) topClassLike() *scope {
	for i := len(w.scopes) - 1; i >= 0; i-- {
		s := &w.scopes[i]
		switch s.kind {
		case skClass, skStruct, skUnion:
			return s
		case skOther:
			continue
		default:
			return nil
		}
	}
	return nil
}

func (w *walker) nsPath() string {
	var parts []string
	for _, s := range w.scopes {
		if s.kind == skNamespace && s.name != "" {
			parts = append(parts, s.name)
		}
	}
	return strings.Join(parts, "::")
}

func (w *walker) classPath() string {
	var parts []string
	for _, s := range w.scopes {
		if s.kind == skClass || s.kind == skStruct || s.kind == skUnion {
			parts = append(parts, s.name)
		}
	}
	return strings.Join(parts, "::")
}

func (w *walker) qualifiedName(local string) string {
	var parts []string
	if ns := w.nsPath(); ns != "" {
		parts = append(parts, ns)
	}
	if cl := w.classPath(); cl != "" {
		parts = append(parts, cl)
	}
	parts = append(parts, local)
	return strings.Join(parts, "::")
}
