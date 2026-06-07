package pythontest

import (
	"os"
	"regexp"
	"strings"
)

// This file is the pure-Go regex fallback for the python-test ffx
// invocation extractor, used only when no python3 interpreter is available
// (or the bundled AST helper errors on a file). It targets the dominant
// idiom — an inline literal list passed to an ffx-receiver's run/popen:
//
//	self._ffx.run(["session", "start"], machine=...)
//	self.dut.ffx.run(["driver", "list"])
//	self._ffx.run(cmd=["target", "screenshot", "--format", "png"])
//	self._ffx.popen([ ... ])
//
// It deliberately does NOT chase locally built lists, constant references,
// or dict subscripts — those are the AST helper's job. When it skips a
// dynamic invocation (the run() call has no inline literal list), it counts
// it so the caller logs rather than silently dropping. The AST path is
// strongly preferred; this exists so a python3-less environment still gets
// the literal floor instead of zero.

// reFfxRunCall matches an ffx-receiver run/popen/run_test_component call up
// to the opening of its first argument. Group 1 is the trailing receiver
// identifier (must be ffx-shaped). The receiver may be a bare name or a
// dotted access (`self.dut.ffx`), so we anchor on the LAST identifier
// before the method. \b keeps `.ffxfoo` out.
var reFfxRunCall = regexp.MustCompile(
	`(?:\b|\.)([A-Za-z_][A-Za-z0-9_]*)\.(?:run|popen|run_test_component)\s*\(`)

// reCmdKwarg matches a leading `cmd=` keyword so we can start the list scan
// after it.
var reCmdKwarg = regexp.MustCompile(`^\s*cmd\s*=\s*`)

// extractRegex is the fallback extractor. It scans the file text for
// ffx-receiver run calls and, when the (first positional or cmd=) argument
// is an inline `[ ... ]` literal list, collects its string literals.
func extractRegex(absPath string) ([]invocation, extractStats) {
	body, err := os.ReadFile(absPath)
	if err != nil {
		return nil, extractStats{}
	}
	src := stripPyComments(string(body))
	var invs []invocation
	var stats extractStats

	lineOffsets := computeLineOffsets(src)

	for _, m := range reFfxRunCall.FindAllStringSubmatchIndex(src, -1) {
		recv := src[m[2]:m[3]]
		if !isFfxReceiver(recv) {
			continue
		}
		// Position just after the opening paren.
		open := m[1]
		rest := src[open:]
		// Optional `cmd=` before the list.
		if loc := reCmdKwarg.FindStringIndex(rest); loc != nil {
			rest = rest[loc[1]:]
		}
		rest = strings.TrimLeft(rest, " \t\n\r")
		if !strings.HasPrefix(rest, "[") {
			// No inline literal list (variable / constant / built-up) — the
			// AST path handles these; the fallback can't, so count it.
			stats.fullyDynamic++
			continue
		}
		lits, dynamic, ok := sliceListLiterals(rest[1:])
		if !ok || len(lits) == 0 {
			stats.fullyDynamic++
			continue
		}
		if dynamic {
			stats.lossy++
		}
		invs = append(invs, invocation{
			Args:    lits,
			Line:    lineFromOffset(lineOffsets, m[0]),
			Func:    "", // fallback doesn't track enclosing function
			Dynamic: dynamic,
		})
	}
	return invs, stats
}

// isFfxReceiver reports whether a trailing receiver identifier is ffx-shaped
// (mirrors the AST helper's _receiver_is_ffx for the Name/Attribute case).
func isFfxReceiver(name string) bool {
	return name == "ffx" || name == "_ffx" || strings.HasSuffix(name, "ffx_transport")
}

// sliceListLiterals walks from just past a `[` opener to the matching `]`,
// collecting single- and double-quoted string literals in order and marking
// the list dynamic when it contains any non-literal element. Bracket-depth
// and string aware. Returns (literals, dynamic, ok); ok=false if the list
// was unterminated.
func sliceListLiterals(s string) ([]string, bool, bool) {
	var lits []string
	dynamic := false
	depth := 1
	i := 0
	// Track whether the current top-level element has contributed a literal,
	// so a bare identifier element flips `dynamic`.
	elementHadLiteral := false
	elementHadContent := false
	flushElement := func() {
		if elementHadContent && !elementHadLiteral {
			dynamic = true
		}
		elementHadLiteral = false
		elementHadContent = false
	}
	for i < len(s) {
		c := s[i]
		switch c {
		case '\'', '"':
			lit, next, ok := consumePyString(s, i)
			if !ok {
				return nil, false, false
			}
			if depth == 1 {
				lits = append(lits, lit)
				elementHadLiteral = true
				elementHadContent = true
			}
			i = next
		case '[', '(', '{':
			depth++
			elementHadContent = true
			i++
		case ']':
			depth--
			if depth == 0 {
				flushElement()
				return lits, dynamic, true
			}
			i++
		case ')', '}':
			depth--
			i++
		case ',':
			if depth == 1 {
				flushElement()
			}
			i++
		case ' ', '\t', '\n', '\r':
			i++
		default:
			if depth == 1 {
				elementHadContent = true
			}
			i++
		}
	}
	return nil, false, false // unterminated
}

// consumePyString reads a single- or double-quoted Python string literal
// starting at the opening quote at index i. Handles backslash escapes and
// f-string prefixes are NOT special-cased (an f-string's `{...}` content is
// kept as raw text — acceptable for the fallback, which only mines plain
// command tokens). Returns (contents, indexPastClose, ok).
func consumePyString(s string, i int) (string, int, bool) {
	quote := s[i]
	j := i + 1
	var sb strings.Builder
	for j < len(s) {
		c := s[j]
		if c == '\\' && j+1 < len(s) {
			sb.WriteByte(s[j+1])
			j += 2
			continue
		}
		if c == quote {
			return sb.String(), j + 1, true
		}
		sb.WriteByte(c)
		j++
	}
	return "", 0, false
}

// stripPyComments removes `# ...` line-comment tails so flags mentioned in
// comments don't leak in. String-boundary aware (a `#` inside a quoted
// string is preserved). Reuses the same conservative rule pytest uses.
func stripPyComments(body string) string {
	var sb strings.Builder
	for _, raw := range strings.Split(body, "\n") {
		sb.WriteString(stripLineComment(raw))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func stripLineComment(line string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '\\':
			if inSingle || inDouble {
				i++
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func computeLineOffsets(s string) []int {
	offsets := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
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
