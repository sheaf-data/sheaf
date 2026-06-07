package fidl

import (
	"testing"
)

func TestLex_KeywordsAndPunctuation(t *testing.T) {
	src := `library fuchsia.io; using zx; protocol Directory { compose Openable; };`
	toks, err := Lex(src)
	if err != nil {
		t.Fatalf("Lex: %v", err)
	}
	// Spot-check key tokens.
	expectAt := func(i int, want TokenKind, val string) {
		t.Helper()
		if toks[i].Kind != want {
			t.Errorf("toks[%d].Kind = %v, want %v (val=%q)", i, toks[i].Kind, want, toks[i].Value)
		}
		if val != "" && toks[i].Value != val {
			t.Errorf("toks[%d].Value = %q, want %q", i, toks[i].Value, val)
		}
	}
	expectAt(0, tokIdent, "library")
	expectAt(1, tokIdent, "fuchsia")
	// '.' is punctuation
	expectAt(2, tokPunct, ".")
	expectAt(3, tokIdent, "io")
	expectAt(4, tokSemi, "")
}

func TestLex_DocCommentBlock(t *testing.T) {
	src := `/// Line one.
/// Line two.
/// Line three.
protocol Foo {};`
	toks, _ := Lex(src)
	if toks[0].Kind != tokDocComment {
		t.Fatalf("toks[0] = %v, want DOC", toks[0].Kind)
	}
	want := "Line one.\nLine two.\nLine three."
	if toks[0].Value != want {
		t.Errorf("doc value = %q, want %q", toks[0].Value, want)
	}
	// Next token should be the protocol ident.
	if toks[1].Kind != tokIdent || toks[1].Value != "protocol" {
		t.Errorf("toks[1] = %+v, want IDENT(protocol)", toks[1])
	}
}

func TestLex_DocCommentNotMergedAcrossBlankLine(t *testing.T) {
	src := `/// One.

/// Two.`
	toks, _ := Lex(src)
	if toks[0].Kind != tokDocComment || toks[0].Value != "One." {
		t.Errorf("toks[0] = %+v", toks[0])
	}
	if toks[1].Kind != tokDocComment || toks[1].Value != "Two." {
		t.Errorf("toks[1] = %+v", toks[1])
	}
}

func TestLex_RegularCommentsSkipped(t *testing.T) {
	src := `// non-doc comment
/* block
   comment */
library foo;`
	toks, _ := Lex(src)
	if toks[0].Kind != tokIdent || toks[0].Value != "library" {
		t.Errorf("toks[0] = %+v, want IDENT(library)", toks[0])
	}
}

func TestLex_String(t *testing.T) {
	src := `const X string = "hello world";`
	toks, _ := Lex(src)
	var found bool
	for _, tk := range toks {
		if tk.Kind == tokString && tk.Value == "hello world" {
			found = true
		}
	}
	if !found {
		t.Errorf("string literal not found in %+v", toks)
	}
}

func TestLex_Attribute(t *testing.T) {
	src := `@available(added=18) protocol Foo {};`
	toks, _ := Lex(src)
	if toks[0].Kind != tokAttribute || toks[0].Value != "available" {
		t.Errorf("toks[0] = %+v, want ATTR(available)", toks[0])
	}
}

func TestLex_Arrow(t *testing.T) {
	src := `Open() -> ();`
	toks, _ := Lex(src)
	var arrowFound bool
	for _, tk := range toks {
		if tk.Kind == tokArrow {
			arrowFound = true
		}
	}
	if !arrowFound {
		t.Errorf("arrow token not found in %+v", toks)
	}
}

func TestLex_HexNumber(t *testing.T) {
	src := `PERM_CONNECT = 0x0001;`
	toks, _ := Lex(src)
	var hexFound bool
	for _, tk := range toks {
		if tk.Kind == tokNumber && tk.Value == "0x0001" {
			hexFound = true
		}
	}
	if !hexFound {
		t.Errorf("hex number not found in %+v", toks)
	}
}

func TestLex_LineAndColumn(t *testing.T) {
	src := "library foo;\nprotocol Bar {};"
	toks, _ := Lex(src)
	// 'protocol' should be on line 2.
	for _, tk := range toks {
		if tk.Value == "protocol" {
			if tk.Line != 2 {
				t.Errorf("protocol on line %d, want 2", tk.Line)
			}
			return
		}
	}
	t.Errorf("protocol token not found")
}

func TestLex_UnterminatedString(t *testing.T) {
	_, err := Lex(`const X = "unterminated`)
	if err == nil {
		t.Errorf("expected error for unterminated string")
	}
}
