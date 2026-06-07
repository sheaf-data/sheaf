package rusttest

import (
	"log"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/ffxinvoke"
)

// This file implements ffx-anchored subprocess-invocation extraction from
// Rust test bodies (the M1 + M2 milestones). A behavior test that runs
// `ffx <cmd> --flag` as a subprocess is direct evidence the command AND
// the flag are exercised — but the name/path/directory anchors the
// adapter already uses never read the body, so those flags score 0%.
//
// We anchor strictly to the ffx *binary*. ffx test files also shell out to
// llvm-cov / llvm-profdata / qemu / ssh via `Command::new(x).args([...])`;
// reading those `.args` as ffx would be a false positive. So extraction
// only fires when the receiver is provably the ffx harness (M1) or the
// program is provably an ffx-binary variable (M2).
//
// The extracted literal args are canonicalized through internal/climatch
// — the same shared "what command does this invoke" parser the markdown
// and workflows adapters use — so the emitted ContractRefs equal the cobra
// element-ID form exactly (`ffx target list`, `ffx target list --format`)
// and the indexer's Strategy 1 matches them directly.

// reFfxHarnessCall matches an M1 ffx-harness invocation up to the opening
// bracket of its argument array. The receiver is the ffx harness, so the
// binary is unambiguously ffx:
//
//	isolate.ffx(&["target", "list"])      .ffx([...])    .ffx(vec![...])
//	emu.ffx(&[ ... ])                      .run_ffx([...])
//
// Group 1 is the array "opener" (`[`, `&[`, or `vec![`) so the slicer
// knows where the literal list begins. The method name is bounded by a
// preceding `.` so `.prefix_ffx(` / `.ffx_path(` don't match — only
// `.ffx(` and `.run_ffx(`. \b after the name keeps `.ffxfoo(` out.
var reFfxHarnessCall = regexp.MustCompile(`\.(?:run_ffx|ffx)\b\s*\(\s*(&?\[|vec!\s*\[)`)

// reCommandNewFfx matches an M2 `Command::new(<ffx-path>)` where the
// program argument is clearly an ffx-binary variable. We accept the
// ffx-path forms the spec calls out — `FFX_TOOL_PATH`, `FFX_BIN*`, any
// identifier containing `ffx_path` — with an optional leading `&` and
// optional receiver prefix (`self.ffx_path`), plus a literal `"ffx"`.
// Anything else (`Command::new("kill")`, `Command::new(&self.adb)`,
// `Command::new(&self.llvm_profdata_bin)`) does NOT match, so non-ffx
// subprocesses are never read as ffx.
var reCommandNewFfx = regexp.MustCompile(`\bCommand::new\(\s*&?\s*(?:` +
	`FFX_TOOL_PATH|` + // const FFX_TOOL_PATH = env!("FFX_TOOL_PATH")
	`FFX_BIN[A-Z0-9_]*|` + // FFX_BIN, FFX_BIN_PATH, …
	`(?:[A-Za-z0-9_]+\.)*[A-Za-z0-9_]*ffx_path[A-Za-z0-9_]*|` + // ffx_path, self.ffx_path, args.ffx_path
	`"ffx"` + // Command::new("ffx")
	`)\s*\)`)

// reArgsCall matches a `.args(...)` up to the opening bracket of its array,
// for the M2 association step. Group 1 is the array opener.
var reArgsCall = regexp.MustCompile(`\.args\s*\(\s*(&?\[|vec!\s*\[)`)

// invocationStats accumulates what extraction skipped, so a regen can
// report it instead of silently truncating. Returned alongside the refs.
// The fully-dynamic tally is delegated to the shared ffxinvoke
// canonicalizer (ffxinvoke.Stats); m2NoArgs is rust-specific.
type invocationStats struct {
	ffxinvoke.Stats
	// m2NoArgs counts M2 `Command::new(<ffx>)` sites where we could not
	// find an associated `.args([...])` within the same statement window
	// (fragile cross-line association we deliberately don't chase).
	m2NoArgs int
}

func (s *invocationStats) add(o invocationStats) {
	s.Stats.Add(o.Stats)
	s.m2NoArgs += o.m2NoArgs
}

// extractInvocationRefs scans one test-function body for ffx-anchored
// subprocess invocations (M1 harness calls + M2 Command::new(ffx).args)
// and returns the climatch-canonicalized command + flag ContractRefs,
// plus skip stats. binaries is the CLI-shaped scope (cliBinariesFromScope);
// extraction is a no-op when ffx is not among them — preserving every
// non-CLI config (kubectl/docker/etc. that don't declare a bare binary).
//
// We only anchor on a binary literally named "ffx" because the harness
// receiver (`.ffx(`) and the canonical command prefix (`"ffx " + …`) are
// ffx-specific. The gate is still generic: any project whose scope.library
// is a bare CLI binary turns the feature on; ffx is simply the first (and,
// for the harness form, the only) consumer today.
func extractInvocationRefs(body string, binaries []string) ([]string, invocationStats) {
	var stats invocationStats
	if !containsFfx(binaries) {
		return nil, stats
	}
	var refs []string
	seen := map[string]bool{}
	emit := func(rs []string) {
		for _, r := range rs {
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			refs = append(refs, r)
		}
	}

	// M1: ffx-harness receiver calls.
	for _, m := range reFfxHarnessCall.FindAllStringSubmatchIndex(body, -1) {
		// m[2]:m[3] is the array opener; the literal list starts at m[3].
		args, ok := sliceArrayLiterals(body, m[3])
		if !ok {
			continue
		}
		cr, fr, st := ffxinvoke.Canonicalize(args)
		stats.Stats.Add(st)
		emit(cr)
		emit(fr)
	}

	// M2: Command::new(<ffx-path>) … .args([...]). Associate the args with
	// the Command on the same line or an adjacent line of the same builder
	// chain. We bound the search to a small window after the
	// `Command::new(...)` match so we don't grab an unrelated later
	// `.args` (e.g. a second Command in the same body).
	for _, m := range reCommandNewFfx.FindAllStringIndex(body, -1) {
		argsLits, ok := m2ArgsAfter(body, m[1])
		if !ok {
			stats.m2NoArgs++
			continue
		}
		cr, fr, st := ffxinvoke.Canonicalize(argsLits)
		stats.Stats.Add(st)
		emit(cr)
		emit(fr)
	}

	return refs, stats
}

// m2ArgsAfter finds the first `.args([...])` literal list that belongs to
// the Command::new() ending at byte offset `from`. To keep cross-line
// association from being fragile, we only look within a bounded window and
// stop at a statement terminator (`;`) — a `.args` past the end of this
// builder statement isn't ours. Returns the literal args and whether a
// usable `.args` array was found.
func m2ArgsAfter(body string, from int) ([]string, bool) {
	const window = 800 // generous enough for a multi-line vec! arg list
	end := from + window
	if end > len(body) {
		end = len(body)
	}
	// Truncate the window at the first `;` so we don't cross into the next
	// statement (and pick up a different command's `.args`). The `.args`
	// for THIS Command always precedes the `.status()/.output()/.spawn();`
	// terminator of the same chain, and the chain itself contains no bare
	// `;`, so cutting at the first `;` keeps the whole chain in view while
	// excluding anything after it.
	seg := body[from:end]
	if semi := strings.IndexByte(seg, ';'); semi >= 0 {
		seg = seg[:semi]
	}
	loc := reArgsCall.FindStringSubmatchIndex(seg)
	if loc == nil {
		return nil, false
	}
	args, ok := sliceArrayLiterals(seg, loc[3])
	if !ok {
		return nil, false
	}
	return args, true
}

// sliceArrayLiterals walks from `start` (just past an array opener `[`)
// to the matching close bracket, collecting the double-quoted string
// literals in order and skipping every dynamic token. It is bracket-depth
// aware so a nested `[...]` (rare in arg arrays) doesn't end the scan
// early, and string-aware so a `]` inside a literal isn't mistaken for the
// array close. Returns the literals and whether a matching close bracket
// was found (false = malformed / unterminated within the window).
func sliceArrayLiterals(body string, start int) ([]string, bool) {
	depth := 1
	i := start
	var lits []string
	for i < len(body) && depth > 0 {
		switch body[i] {
		case '"':
			// Consume the whole string literal and record it.
			lit, next := consumeString(body, i)
			lits = append(lits, lit)
			i = next
		case '[':
			depth++
			i++
		case ']':
			depth--
			i++
		default:
			i++
		}
	}
	if depth != 0 {
		return nil, false
	}
	return lits, true
}

// consumeString reads a double-quoted Rust string literal starting at the
// opening quote at index i and returns the unquoted contents plus the
// index just past the closing quote. Handles backslash escapes minimally
// (we keep the raw inner text; arg literals here are plain command tokens,
// not escape-heavy strings).
func consumeString(body string, i int) (string, int) {
	// i points at the opening quote.
	j := i + 1
	var sb strings.Builder
	for j < len(body) {
		c := body[j]
		if c == '\\' && j+1 < len(body) {
			// Preserve the escaped char verbatim (drop the backslash).
			sb.WriteByte(body[j+1])
			j += 2
			continue
		}
		if c == '"' {
			j++
			break
		}
		sb.WriteByte(c)
		j++
	}
	return sb.String(), j
}

// containsFfx reports whether the CLI-shaped scope binaries include "ffx",
// the only binary the harness-anchored extractor knows how to read.
func containsFfx(binaries []string) bool {
	for _, b := range binaries {
		if b == "ffx" {
			return true
		}
	}
	return false
}

// logInvocationSkips emits a single summary line per file when extraction
// skipped any invocation, so a regen surfaces what was dropped rather than
// truncating silently. No-op when nothing was skipped.
func logInvocationSkips(rel string, stats invocationStats) {
	if stats.FullyDynamic == 0 && stats.m2NoArgs == 0 {
		return
	}
	log.Printf("rust-test: %s: skipped invocations: %d fully-dynamic (no literal subcommand), %d M2 Command::new(ffx) without associable .args",
		rel, stats.FullyDynamic, stats.m2NoArgs)
}
