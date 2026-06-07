package grounding

import (
	"regexp"
	"strings"
)

// This file ports the markdown-adapter primitives the Grounding detector
// needs — the H1/H2/H3 section index, line lookup, and fenced-code-block
// detection — into the grounding package. They mirror the unexported
// helpers in internal/adapters/markdown/markdown.go (buildSectionIndex,
// computeLineOffsets, lineFromOffset, the code-fence regex) so the two
// surfaces stamp section_path the same way. They are reproduced rather
// than imported because the originals are package-private; behavior is
// intentionally identical (see markdown_test.go's section-stack cases).

// codeFenceRx matches fenced code blocks, allowing up to 3 leading spaces
// of indent per CommonMark. Same pattern as the markdown adapter.
var codeFenceRx = regexp.MustCompile("(?ms)^ {0,3}```([a-zA-Z0-9_+-]*)\\s*\\n(.*?)\\n[ \\t]*```")

// computeLineOffsets returns the byte offset at which each line starts.
func computeLineOffsets(body []byte) []int {
	offsets := []int{0}
	for i, b := range body {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

// lineFromOffset binary-searches the 1-based line number for a byte offset.
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

// fenceRanges returns the [start,end) byte ranges of fenced code blocks.
func fenceRanges(body []byte) [][]int {
	m := codeFenceRx.FindAllSubmatchIndex(body, -1)
	out := make([][]int, 0, len(m))
	for _, r := range m {
		out = append(out, []int{r[0], r[1]})
	}
	return out
}

// headingRanges returns the [start,end) byte ranges of ATX heading lines
// (outside fenced code blocks). A heading's words are structural — they are
// captured by the page_title / heading anchors, not as prose references —
// so the prose scan blanks them out before matching, exactly as it blanks
// code blocks. The range covers the whole line including the leading #s.
func headingRanges(body []byte) [][]int {
	var out [][]int
	inFence := false
	off := 0
	for off < len(body) {
		end := off
		for end < len(body) && body[end] != '\n' {
			end++
		}
		line := body[off:end]
		i := 0
		for i < len(line) && i < 3 && line[i] == ' ' {
			i++
		}
		isFence := i+3 <= len(line) && ((line[i] == '`' && line[i+1] == '`' && line[i+2] == '`') ||
			(line[i] == '~' && line[i+1] == '~' && line[i+2] == '~'))
		if isFence {
			inFence = !inFence
		} else if !inFence {
			j := i
			for j < len(line) && line[j] == '#' {
				j++
			}
			hc := j - i
			if hc >= 1 && hc <= 6 && j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				out = append(out, []int{off, end})
			}
		}
		off = end + 1
	}
	return out
}

// stripRanges replaces each [start,end) range with spaces (preserving
// newlines and offsets) so prose scanning skips code-block content without
// shifting line/offset math. Same as the markdown adapter's helper.
func stripRanges(body []byte, ranges [][]int) []byte {
	if len(ranges) == 0 {
		return body
	}
	out := make([]byte, len(body))
	copy(out, body)
	for _, r := range ranges {
		for i := r[0]; i < r[1] && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	return out
}

// buildSectionIndex scans body for H1/H2/H3 headings outside fenced code
// blocks and returns sectionPathAt(off) -> the live heading stack at that
// offset (outermost first), with trailing empty slots packed out. H4+ roll
// up to the H3 slot. Verbatim port of the markdown adapter's function.
func buildSectionIndex(body []byte) func(off int) []string {
	type ev struct {
		off   int
		level int
		text  string
	}
	var events []ev
	inFence := false
	fenceLine := func(line []byte) bool {
		i := 0
		for i < len(line) && i < 3 && line[i] == ' ' {
			i++
		}
		if i+3 > len(line) {
			return false
		}
		return (line[i] == '`' && line[i+1] == '`' && line[i+2] == '`') ||
			(line[i] == '~' && line[i+1] == '~' && line[i+2] == '~')
	}
	off := 0
	for off < len(body) {
		end := off
		for end < len(body) && body[end] != '\n' {
			end++
		}
		line := body[off:end]
		if fenceLine(line) {
			inFence = !inFence
		} else if !inFence {
			i := 0
			for i < len(line) && i < 3 && line[i] == ' ' {
				i++
			}
			hashStart := i
			for i < len(line) && line[i] == '#' {
				i++
			}
			hashCount := i - hashStart
			if hashCount >= 1 && hashCount <= 6 && i < len(line) && (line[i] == ' ' || line[i] == '\t') {
				for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
					i++
				}
				text := strings.TrimRight(string(line[i:]), " \t")
				text = strings.TrimRight(text, "#")
				text = strings.TrimSpace(text)
				if text != "" {
					level := hashCount
					if level > 3 {
						level = 3
					}
					events = append(events, ev{off: off, level: level, text: text})
				}
			}
		}
		off = end + 1
	}
	if len(events) == 0 {
		return func(int) []string { return nil }
	}
	return func(off int) []string {
		var stack [3]string
		for _, e := range events {
			if e.off > off {
				break
			}
			stack[e.level-1] = e.text
			for i := e.level; i < 3; i++ {
				stack[i] = ""
			}
		}
		out := make([]string, 0, 3)
		for _, s := range stack {
			if s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
}

// pageTitle returns the document's first H1 (the page title), or "" if the
// doc has no H1. Scans outside fenced code blocks.
func pageTitle(body []byte) string {
	inFence := false
	off := 0
	for off < len(body) {
		end := off
		for end < len(body) && body[end] != '\n' {
			end++
		}
		line := body[off:end]
		off = end + 1
		// fence toggle
		i := 0
		for i < len(line) && i < 3 && line[i] == ' ' {
			i++
		}
		if i+3 <= len(line) && ((line[i] == '`' && line[i+1] == '`' && line[i+2] == '`') ||
			(line[i] == '~' && line[i+1] == '~' && line[i+2] == '~')) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		j := i
		for j < len(line) && line[j] == '#' {
			j++
		}
		if j-i == 1 && j < len(line) && (line[j] == ' ' || line[j] == '\t') {
			for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				j++
			}
			t := strings.TrimRight(string(line[j:]), " \t")
			t = strings.TrimRight(t, "#")
			return strings.TrimSpace(t)
		}
	}
	return ""
}

// sentenceBounds returns the [start,end) byte range of the sentence
// containing offset `pos` within body, where sentence boundaries are
// '.', '!', '?' followed by whitespace, plus hard breaks (newline-newline)
// and the body ends. Used to lift the verbatim excerpt for a finding.
func sentenceBounds(body []byte, pos int) (int, int) {
	n := len(body)
	// Backward to sentence start.
	start := pos
	for start > 0 {
		c := body[start-1]
		if c == '\n' && start >= 2 && body[start-2] == '\n' {
			break
		}
		if (c == '.' || c == '!' || c == '?') && isSpaceByte(atByte(body, start)) {
			break
		}
		start--
	}
	// Trim leading whitespace.
	for start < pos && isSpaceByte(body[start]) {
		start++
	}
	// Forward to sentence end (inclusive of the terminator).
	end := pos
	for end < n {
		c := body[end]
		if c == '\n' && end+1 < n && body[end+1] == '\n' {
			break
		}
		if c == '.' || c == '!' || c == '?' {
			// include the terminator; stop if next is space/EOL.
			if end+1 >= n || isSpaceByte(body[end+1]) {
				end++
				break
			}
		}
		end++
	}
	if end > n {
		end = n
	}
	return start, end
}

func atByte(body []byte, i int) byte {
	if i < 0 || i >= len(body) {
		return ' '
	}
	return body[i]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// bytesToLower lowercases ASCII A-Z in place on a copy, preserving length
// and byte offsets so case-insensitive matching keeps the same span math.
// Non-ASCII bytes are left untouched (UTF-8 multibyte runes are not
// case-folded — contract names are ASCII identifiers).
func bytesToLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		} else {
			out[i] = c
		}
	}
	return out
}

// indexFrom returns the index of the first occurrence of needle in
// haystack at or after `from`, or -1. Operates on the lowercased byte
// slice produced by bytesToLower so callers compare apples to apples.
func indexFrom(haystack []byte, needle string, from int) int {
	if from < 0 {
		from = 0
	}
	if from > len(haystack) {
		return -1
	}
	i := indexBytesString(haystack[from:], needle)
	if i < 0 {
		return -1
	}
	return from + i
}

// indexBytesString is strings.Index over a []byte without allocating.
func indexBytesString(haystack []byte, needle string) int {
	n := len(needle)
	if n == 0 {
		return 0
	}
	if n > len(haystack) {
		return -1
	}
	c0 := needle[0]
	for i := 0; i+n <= len(haystack); i++ {
		if haystack[i] != c0 {
			continue
		}
		if string(haystack[i:i+n]) == needle {
			return i
		}
	}
	return -1
}
