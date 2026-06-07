// Package fidlmatch extracts likely FIDL invocations from source
// text (C++, Rust, or markdown code blocks). It returns candidate
// ContractElement IDs that the indexer can verify against the
// corpus. The matcher is deliberately liberal — it emits any ID it
// can plausibly construct; the indexer drops ones that don't
// resolve, so noise is bounded by the contract surface.
//
// Two precision tiers:
//  1. Qualified — fuchsia::ui::scenic::Scenic::TakeScreenshot(...)
//     or fidl_fuchsia_ui_scenic::Scenic::take_screenshot(...).
//     No #include/use resolution needed; the namespace is the proof.
//  2. Unqualified — session->TakeScreenshot(...) or
//     session.take_screenshot(...). Requires that the file's
//     #include / use lines bring a known library into scope, AND
//     the variable name (lowercased prefix) matches a known
//     protocol name.
//
// The "fuchsia" namespace is the default but not hardcoded — call
// NewMatcher with a different Config.IDLPrefix to target projects
// whose contract IDs sit under a different top-level namespace
// (e.g. "google", "acme", "openconfig"). All regexes are built
// from the prefix at construction time.
package fidlmatch

import (
	"regexp"
	"sort"
	"strings"
)

// LibrarySet is the union of FIDL libraries currently in scope for
// a given source file. Membership is determined by the caller (e.g.
// the gtest adapter scans #include lines; the rust-test adapter
// scans `use fidl_<prefix>_*` lines).
type LibrarySet map[string]bool

// Config configures a Matcher's prefix patterns. IDLPrefix is the
// top dotted-namespace segment shared by every library this project
// owns (e.g. "fuchsia" → matches fuchsia.io, fuchsia.kernel, …).
// Empty defaults to "fuchsia" for back-compat.
type Config struct {
	IDLPrefix string
}

// Matcher holds the compiled regex set for one IDL prefix. Construct
// once per project / scan and reuse — building it is cheap but
// non-trivial.
type Matcher struct {
	prefix string

	cppQualified  *regexp.Regexp
	cppWireCall   *regexp.Regexp
	cppCall       *regexp.Regexp // prefix-agnostic
	cppInclude1   *regexp.Regexp
	cppInclude2   *regexp.Regexp
	cppWireCallNS *regexp.Regexp

	rustQualified *regexp.Regexp
	rustCall      *regexp.Regexp // prefix-agnostic
	rustUse       *regexp.Regexp
}

// NewMatcher returns a Matcher whose regexes are wired for cfg's
// IDLPrefix. Empty prefix → "fuchsia" (back-compat default).
func NewMatcher(cfg Config) *Matcher {
	prefix := cfg.IDLPrefix
	if prefix == "" {
		prefix = "fuchsia"
	}
	q := regexp.QuoteMeta(prefix)
	return &Matcher{
		prefix: prefix,

		// `<prefix>::ui::scenic::Scenic::TakeScreenshot(...)`
		cppQualified: regexp.MustCompile(
			`\b` + q + `(?:::[a-z][a-z0-9_]*)+::([A-Z][A-Za-z0-9_]*)::([A-Z][A-Za-z0-9_]*)\s*\(`),

		// `fidl::WireCall<<prefix>_io::Directory>(...)->Open(...)` —
		// FIDL-binding-specific path. (fidl::WireCall is the FIDL C++
		// bindings convention; safe to keep here as long as we only
		// run on FIDL-shaped projects.)
		cppWireCall: regexp.MustCompile(
			`fidl::Wire(?:Call|SyncClient)<` + q +
				`(?:_[a-z0-9_]+)+::([A-Z][A-Za-z0-9_]*)>[^;]*?\)\s*[->.]+\s*([A-Z][A-Za-z0-9_]*)\s*\(`),

		// Prefix-agnostic — applies to any `var->Method(` / `var.Method(`.
		cppCall: regexp.MustCompile(`\b([a-z][a-z0-9_]*)(?:->|\.)([A-Z][A-Za-z0-9_]*)\s*\(`),

		// `#include "<prefix>/ui/scenic/cpp/fidl.h"` or `<...>`
		cppInclude1: regexp.MustCompile(
			`#include\s+[<"](` + q + `(?:/[a-z0-9_]+)+)/cpp/[a-z]+\.h[>"]`),

		// `#include "fidl/<prefix>.io/cpp/wire.h"`
		cppInclude2: regexp.MustCompile(
			`#include\s+[<"]fidl/(` + q + `\.[a-z0-9.]+)/cpp/[a-z]+\.h[>"]`),

		cppWireCallNS: regexp.MustCompile(`<(` + q + `(?:_[a-z0-9_]+)+)::`),

		// `fidl_<prefix>_ui_scenic::ScenicProxy::take_screenshot(...)`
		rustQualified: regexp.MustCompile(
			`\b(?:fidl_)?(` + q + `(?:_[a-z0-9_]+)+)::([A-Z][A-Za-z0-9_]*?)(?:Proxy|SynchronousProxy|RequestStream|Marker)?::([a-z][a-z0-9_]*)\s*\(`),

		// Prefix-agnostic.
		rustCall: regexp.MustCompile(`\b([a-z][a-z0-9_]*)\.([a-z][a-z0-9_]*)\s*\(`),

		// `use fidl_<prefix>_io::*;` or `use <prefix>_scenic::Session;`
		rustUse: regexp.MustCompile(`use\s+(?:fidl_)?(` + q + `(?:_[a-z0-9_]+)+)`),
	}
}

// defaultMatcher backs the package-level shims so existing callers
// (and back-compat sites) keep working when no Matcher is plumbed.
var defaultMatcher = NewMatcher(Config{})

// Extract returns candidate "<library>/<protocol>.<method>" element
// IDs found in body. kind selects the language-specific patterns.
// Pass an empty LibrarySet to extract only qualified hits.
func (m *Matcher) Extract(body string, kind string, scope LibrarySet) []string {
	var refs []string
	switch kind {
	case "cpp":
		refs = append(refs, m.extractCPPQualified(body)...)
		refs = append(refs, m.extractCPPUnqualified(body, scope)...)
	case "rust":
		refs = append(refs, m.extractRustQualified(body)...)
		refs = append(refs, m.extractRustUnqualified(body, scope)...)
	}
	return dedup(refs)
}

// CPPIncludeLibraries scans #include lines and returns the set of
// libraries they bring into scope. Recognized patterns (with prefix
// "fuchsia"):
//
//	#include "fuchsia/ui/scenic/cpp/fidl.h"
//	#include <fuchsia/io/cpp/fidl.h>
//	#include "fidl/fuchsia.io/cpp/wire.h"
func (m *Matcher) CPPIncludeLibraries(body string) LibrarySet {
	out := LibrarySet{}
	for _, mm := range m.cppInclude1.FindAllStringSubmatch(body, -1) {
		out[strings.ReplaceAll(mm[1], "/", ".")] = true
	}
	for _, mm := range m.cppInclude2.FindAllStringSubmatch(body, -1) {
		out[mm[1]] = true
	}
	return out
}

// RustUseLibraries scans `use fidl_<prefix>_*` / `use <prefix>_*`
// statements and returns the libraries those bring into scope.
func (m *Matcher) RustUseLibraries(body string) LibrarySet {
	out := LibrarySet{}
	for _, mm := range m.rustUse.FindAllStringSubmatch(body, -1) {
		out[underscoresToDots(mm[1])] = true
	}
	return out
}

// --- C++ extractors ---

func (m *Matcher) extractCPPQualified(body string) []string {
	var out []string
	for _, mm := range m.cppQualified.FindAllStringSubmatch(body, -1) {
		idx := strings.LastIndex(mm[0], "::"+mm[1]+"::")
		if idx < 0 {
			continue
		}
		ns := mm[0][:idx]
		lib := strings.ReplaceAll(ns, "::", ".")
		out = append(out, lib+"/"+mm[1]+"."+mm[2])
	}
	for _, mm := range m.cppWireCall.FindAllStringSubmatch(body, -1) {
		ns := m.cppWireCallNS.FindStringSubmatch(mm[0])
		if len(ns) < 2 {
			continue
		}
		lib := underscoresToDots(ns[1])
		out = append(out, lib+"/"+mm[1]+"."+mm[2])
	}
	return out
}

func (m *Matcher) extractCPPUnqualified(body string, scope LibrarySet) []string {
	if len(scope) == 0 {
		return nil
	}
	var out []string
	for _, mm := range m.cppCall.FindAllStringSubmatch(body, -1) {
		varName, method := mm[1], mm[2]
		varName = strings.TrimRight(varName, "_")
		if len(varName) < 3 {
			continue
		}
		proto := snakeToCamel(varName)
		for lib := range scope {
			// Specific candidate: var-name-derived protocol.
			out = append(out, lib+"/"+proto+"."+method)
			// Wildcard candidate (fix #3): for distinctive
			// (multi-token CamelCase) method names, also emit
			// `lib/*.Method` so the indexer can fan out to any
			// protocol in lib that has this method. Catches the
			// `parent_session_->SetInfiniteHitRegion(...)` pattern
			// where the semantic var name doesn't equal a protocol.
			//
			// Guarded to multi-token methods so single-word
			// `var->Get()` doesn't over-attribute across every
			// `*Resource.Get` in fuchsia.kernel.
			if isMethodDistinctive(method) {
				out = append(out, lib+"/*."+method)
			}
		}
	}
	return out
}

// --- Rust extractors ---

func (m *Matcher) extractRustQualified(body string) []string {
	var out []string
	for _, mm := range m.rustQualified.FindAllStringSubmatch(body, -1) {
		lib := underscoresToDots(mm[1])
		proto := mm[2]
		method := snakeToCamel(mm[3])
		out = append(out, lib+"/"+proto+"."+method)
	}
	return out
}

func (m *Matcher) extractRustUnqualified(body string, scope LibrarySet) []string {
	if len(scope) == 0 {
		return nil
	}
	var out []string
	for _, mm := range m.rustCall.FindAllStringSubmatch(body, -1) {
		varName, snakeMethod := mm[1], mm[2]
		if len(varName) < 3 {
			continue
		}
		if isRustStdlibMethod(snakeMethod) {
			continue
		}
		camelMethod := snakeToCamel(snakeMethod)
		for lib := range scope {
			// Specific: var-name-derived protocol.
			out = append(out, lib+"/"+capitalize(varName)+"."+camelMethod)
			// Wildcard (fix #3): same fan-out trick as C++ for
			// distinctive multi-token method names.
			if isMethodDistinctive(camelMethod) {
				out = append(out, lib+"/*."+camelMethod)
			}
		}
	}
	return out
}

// isMethodDistinctive reports whether a CamelCase method name has
// enough tokens that a wildcard fan-out (lib/*.Method) is safe — i.e.
// unlikely to hit many same-named methods across different protocols.
//
// Heuristic: methods with 2+ CamelCase tokens are distinctive
// ("SetInfiniteHitRegion", "GetMemoryStatsExtended"). Single-token
// methods like "Get", "Set", "Open", "Close" are too common to fan
// out — almost every protocol has one, and indiscriminate matching
// would over-attribute test signal to every protocol in scope.
func isMethodDistinctive(method string) bool {
	tokens := splitCamel(method)
	return len(tokens) >= 2
}

// splitCamel: "TakeScreenshot" → ["Take", "Screenshot"]. Same logic
// as indexer.splitCamelCase but kept local to avoid a dep.
func splitCamel(s string) []string {
	if s == "" {
		return nil
	}
	var tokens []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			tokens = append(tokens, string(cur))
			cur = cur[:0]
		}
	}
	for i, r := range s {
		if r >= 'A' && r <= 'Z' && i > 0 {
			prev := rune(s[i-1])
			if prev >= 'a' && prev <= 'z' {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return tokens
}

// ============================================================
// Package-level back-compat shims.
//
// These call the default "fuchsia"-prefix matcher. Existing call
// sites (tests, older adapters) keep working unchanged; new code
// should construct a Matcher and call methods on it directly.
// ============================================================

// Extract is a back-compat shim using the default "fuchsia" prefix.
// Prefer (*Matcher).Extract when you can plumb a Matcher through.
func Extract(body string, kind string, scope LibrarySet) []string {
	return defaultMatcher.Extract(body, kind, scope)
}

// CPPIncludeLibraries — back-compat shim, "fuchsia" prefix.
func CPPIncludeLibraries(body string) LibrarySet {
	return defaultMatcher.CPPIncludeLibraries(body)
}

// RustUseLibraries — back-compat shim, "fuchsia" prefix.
func RustUseLibraries(body string) LibrarySet {
	return defaultMatcher.RustUseLibraries(body)
}

// --- helpers ---

func underscoresToDots(s string) string { return strings.ReplaceAll(s, "_", ".") }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// snakeToCamel: "take_screenshot" → "TakeScreenshot".
func snakeToCamel(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	upper := true
	for _, r := range s {
		if r == '_' {
			upper = true
			continue
		}
		if upper {
			if r >= 'a' && r <= 'z' {
				r = r - 'a' + 'A'
			}
			upper = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// rustStdlibMethods filters out high-frequency stdlib/test method
// names that would generate a huge amount of noise if passed to the
// FIDL matcher. The list is conservative — anything not here gets
// emitted as a candidate, and the indexer filters by existence.
var rustStdlibMethods = map[string]bool{
	"unwrap": true, "expect": true, "clone": true, "to_string": true,
	"as_str": true, "as_ref": true, "as_mut": true, "into": true,
	"from": true, "default": true, "new": true, "with_capacity": true,
	"push": true, "pop": true, "len": true, "is_empty": true,
	"iter": true, "into_iter": true, "next": true, "collect": true,
	"map": true, "filter": true, "fold": true, "for_each": true,
	"and_then": true, "or_else": true, "ok": true, "err": true,
	"borrow": true, "borrow_mut": true, "lock": true, "read": true,
	"write": true, "send": true, "recv": true, "spawn": true,
	"await": true, "poll": true, "ready": true, "pending": true,
	"contains": true, "insert": true, "remove": true, "get": true,
	"set": true, "take": true, "replace": true, "drop": true,
	"as_bytes": true, "to_owned": true, "clone_from": true, "eq": true,
	"hash": true, "cmp": true, "partial_cmp": true, "ne": true,
	"abs": true, "min": true, "max": true, "saturating_add": true,
}

func isRustStdlibMethod(m string) bool { return rustStdlibMethods[m] }

func dedup(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
