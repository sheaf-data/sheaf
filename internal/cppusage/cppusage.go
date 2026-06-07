// Package cppusage extracts candidate contract-element references from a C++
// code block — the "this snippet demonstrates API X" signal that feeds the
// examples/Usage surface of a cppheader contract.
//
// It is deliberately narrow. It emits only the two shapes of C++ API use that
// are almost never anything but a real reference:
//
//   - prefix-qualified names — pw::OkStatus, pw::Status::Update — gated on the
//     project's idl_prefix so unrelated std::/absl:: names don't leak; and
//   - ALL-CAPS macro invocations — PW_TRY(...), PW_TRY_WITH_SIZE(...).
//
// Bare lowercase method calls (status.ok()) are intentionally NOT extracted:
// their local tokens collide with common words and would smear example credit
// across unrelated elements. Downstream name-matching keeps only refs that
// resolve to a real element, so a token that matches nothing is harmless; the
// risk this narrowness guards against is a token that coincidentally matches
// an element name.
//
// fidlmatch handles the FIDL-protocol code shapes; this is its plain-C++
// complement. The rst and markdown doc adapters union the two.
package cppusage

import "regexp"

// An ALL-CAPS identifier (>=3 chars) immediately followed by a call paren.
// Captures macro invocations like PW_TRY( and PW_TRY_WITH_SIZE(.
var macroCallRx = regexp.MustCompile(`\b([A-Z][A-Z0-9_]{2,})\s*\(`)

// Extractor pulls plain-C++ usage references out of code blocks. Build one
// per parser (the qualified-name pattern is compiled once from the prefix).
type Extractor struct {
	prefix    string
	qualified *regexp.Regexp // nil when prefix is empty
}

// New returns an Extractor scoped to idlPrefix. An empty prefix disables
// qualified-name extraction (macro extraction still applies).
func New(idlPrefix string) *Extractor {
	e := &Extractor{prefix: idlPrefix}
	if idlPrefix != "" {
		// <prefix>::Seg(::Seg)* — at least one ::Segment after the prefix.
		e.qualified = regexp.MustCompile(
			`\b` + regexp.QuoteMeta(idlPrefix) + `(?:::[A-Za-z_][A-Za-z0-9_]*)+`)
	}
	return e
}

// Extract returns the deduped candidate references found in body, in first-
// seen order.
func (e *Extractor) Extract(body string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, m := range macroCallRx.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	if e.qualified != nil {
		for _, m := range e.qualified.FindAllString(body, -1) {
			add(m)
		}
	}
	return out
}
