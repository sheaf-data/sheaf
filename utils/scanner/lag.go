package scanner

import (
	"bufio"
	"bytes"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// LagResult is the per-library (or per-run) lag distribution computed
// from git commit timestamps over (element source, doc reference) pairs.
// All values are in whole days; the metric is pure mechanical — every
// lag value decomposes to a specific commit timestamp.
type LagResult struct {
	Pairs   int    // cross-file (element, doc) pairs counted toward the metric
	Inline  int    // same-file pairs excluded from the metric (in-source doc comments)
	Unknown int    // pairs whose git history could not be resolved (dropped from the metric, disclosed as the unknown rate)
	P50     int    // median lag in days across Pairs; 0 when Pairs == 0
	P90     int    // long tail across Pairs; 0 when Pairs == 0
	Buckets [4]int // Fresh (=0d) / Recent (1-30d) / Aging (31-180d) / Stale (>180d)
	Sorted  []int  // sorted lag values per pair; re-percentilable per-domain by callers
	// ElementBuckets maps an element id to its worst (stalest) lag
	// bucket — FRESH / RECENT / AGING / STALE — taken as the max lag
	// across that element's cross-file doc pairs. Elements with no
	// countable pair are absent. Drives the per-element data-lag flag
	// the Lag quartile bands filter the Evidence rail on, the same way
	// the Depth bands filter on data-substance.
	ElementBuckets map[string]string
	// Unavailable is set when the lag metric cannot be computed honestly —
	// currently a shallow clone, where git history is truncated so every file
	// resolves to the same boundary commit and the distribution would be a
	// false "0 days, all fresh." When true, callers MUST suppress the
	// distribution and surface UnavailableReason instead of a fake zero.
	Unavailable       bool
	UnavailableReason string
}

// lagComputer caches per-file and per-(file,line) commit timestamps so
// the same lookup isn't repeated across a run.
type lagComputer struct {
	repoRoot string
	cache    map[lagKey]int64 // (path, line) → committer unix time; 0 == unknown
	// blame caches one committer-time per 1-indexed source line per file,
	// from a SINGLE `git blame --line-porcelain` per path (index 0 unused).
	// A nil entry means "not yet loaded"; an empty (len 0, non-nil) slice
	// means "blame failed for this path, never retry" — see lineCtime.
	blame map[string][]int64
	// blameCalls counts how many times git blame actually ran (one per
	// distinct path that resolved or failed). Used by tests to prove the
	// per-file blame is cached, not re-run per line.
	blameCalls int
	// tracked is the set of git-tracked paths (repo-relative), loaded once
	// via a SINGLE `git ls-files` so we never spawn a per-file git process
	// for a path git doesn't track — synthesized/generated contract sources
	// (e.g. cobra YAML produced into the checkout) and in-tarball doc paths
	// (clidoc/ffx.md). On a huge tree (fuchsia) that turns hundreds of
	// always-failing blame spawns into one bulk lookup. nil until loaded.
	tracked       map[string]bool
	trackedLoaded bool
}

// lagKey caches by file when line == 0, else by the specific line.
type lagKey struct {
	path string
	line int
}

func newLagComputer(repoRoot string) *lagComputer {
	return &lagComputer{
		repoRoot: repoRoot,
		cache:    map[lagKey]int64{},
		blame:    map[string][]int64{},
	}
}

// isShallowRepo reports whether repoRoot is a shallow git clone (e.g. cloned
// with --depth 1). `git rev-parse --is-shallow-repository` prints "true" /
// "false" (git 2.15+); on older git or a non-git directory it errors, in which
// case we fail-OPEN (treat as a full checkout) so lag degrades to its prior
// behavior rather than being suppressed on a false positive.
func isShallowRepo(repoRoot string) bool {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--is-shallow-repository").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// isTracked reports whether path is a git-tracked file under repoRoot,
// resolving the full tracked set once via a single `git ls-files`. This is
// the bulk lookup that keeps a synthesized-contract scan (the contract
// elements point at generated YAML that isn't committed) from spawning one
// always-failing `git blame` per element — on a large history that per-file
// spawn cost, not the blame itself, is what dominated. Fail-OPEN: if
// ls-files errors (not a git tree / shallow / permissions) tracked stays nil
// and every path is treated as tracked, so behavior degrades to exactly the
// prior per-file path rather than zeroing all lag.
func (l *lagComputer) isTracked(path string) bool {
	if !l.trackedLoaded {
		l.trackedLoaded = true
		if out, err := exec.Command("git", "-C", l.repoRoot, "ls-files", "-z").Output(); err == nil {
			m := make(map[string]bool, 1<<14)
			for _, p := range bytes.Split(out, []byte{0}) {
				if len(p) > 0 {
					m[string(p)] = true
				}
			}
			l.tracked = m
		}
	}
	if l.tracked == nil {
		return true // ls-files unavailable: don't suppress, behave as before
	}
	return l.tracked[path]
}

// lineCtime returns the committer unix time of the most recent commit
// touching the given 1-indexed source line in path, using a single
// cached `git blame --line-porcelain` per file. Returns 0 when the line
// is out of range, the file isn't blamable (renamed / not committed /
// shallow), or the line's blame block is an uncommitted (zero-sha)
// region. ctimeAt turns a 0 here into the file-level fallback.
//
// Why blame instead of `git log -L`: `git log -L <l>,<l>:<path>` walks
// line history and is the single slowest git operation — it was called
// once per element, hundreds of times per domain, serially, and on a
// huge history (fuchsia) a single domain scan sat ~100% blocked in git.
// `git blame --line-porcelain` resolves the last-touching commit for
// EVERY line of the file in one pass, so we pay one git call per file
// instead of one per (path,line). The per-line last-touching commit git
// blame reports is the same commit `git log -1 -L <l>,<l>` would name,
// and committer-time == %ct, so the produced timestamps are identical —
// just far cheaper. (A unit test cross-checks the two agree.)
func (l *lagComputer) lineCtime(path string, line int) int64 {
	if line <= 0 {
		return 0
	}
	lines, ok := l.blame[path]
	if !ok {
		lines = l.loadBlame(path)
		l.blame[path] = lines
	}
	if line < len(lines) && lines[line] > 0 {
		return lines[line]
	}
	return 0
}

// loadBlame runs `git blame --line-porcelain` ONCE for path and parses
// one committer-time per source line, in file order (1-indexed; index 0
// is left zero and unused). On any error / non-zero exit it returns a
// non-nil empty slice so the caller caches "blame unavailable" and never
// retries this path.
//
// --line-porcelain emits, per source line, a header block followed by
// the content line. The header carries `committer-time <unix>`; the
// content line is the only line that begins with a TAB. We track the
// most recent `committer-time` we saw and append it when the matching
// content (TAB-prefixed) line arrives — yielding exactly one timestamp
// per source line, in order. A boundary/uncommitted region whose blob is
// the all-zero sha still carries a committer-time field, but its commit
// is synthetic ("Not Committed Yet"); we detect the zero-sha header and
// record 0 for that line so ctimeAt falls back to file-level rather than
// trusting a bogus working-tree timestamp.
func (l *lagComputer) loadBlame(path string) []int64 {
	l.blameCalls++
	// No --no-color: porcelain output is uncolored by definition, and
	// `--no-color` is ambiguous on some git versions (--no-color-lines vs
	// --no-color-by-age), which would make blame exit non-zero.
	out, err := exec.Command("git", "-C", l.repoRoot,
		"blame", "--line-porcelain", "--", path).Output()
	if err != nil {
		return []int64{} // non-nil empty: cache the failure, never retry
	}
	// res[0] unused; res grows as content lines are seen, so its final
	// length is (number of source lines + 1).
	res := []int64{0}
	var cur int64    // most recent committer-time seen in a header block
	var zeroSha bool // current block's commit is the all-zero (uncommitted) sha
	sc := bufio.NewScanner(bytes.NewReader(out))
	// Source lines can be long; raise the scanner's token cap well above
	// the 64KiB default so a single long line never aborts the parse.
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		ln := sc.Text()
		if strings.HasPrefix(ln, "\t") {
			// Content line: closes the current block. Record this line's
			// timestamp (0 if the block was an uncommitted/zero-sha region).
			if zeroSha {
				res = append(res, 0)
			} else {
				res = append(res, cur)
			}
			cur, zeroSha = 0, false
			continue
		}
		if strings.HasPrefix(ln, "committer-time ") {
			cur, _ = strconv.ParseInt(strings.TrimSpace(ln[len("committer-time "):]), 10, 64)
			continue
		}
		// A porcelain header block starts with "<40-hex-sha> <orig> <final> [n]".
		// Detect the uncommitted sentinel (all-zero sha) so its line is
		// recorded as unresolved rather than carrying a working-tree time.
		if isBlameHeader(ln) && strings.HasPrefix(ln, "0000000000000000000000000000000000000000 ") {
			zeroSha = true
		}
	}
	if err := sc.Err(); err != nil {
		// Truncated/unreadable output: treat as blame-unavailable so the
		// caller falls back to file-level rather than trusting a partial map.
		return []int64{}
	}
	return res
}

// isBlameHeader reports whether ln is a porcelain commit-header line:
// it starts with a 40-char lowercase-hex sha followed by a space. This
// distinguishes the per-block header from field lines (author, summary,
// previous, filename, …) and from TAB-prefixed content lines.
func isBlameHeader(ln string) bool {
	if len(ln) < 41 || ln[40] != ' ' {
		return false
	}
	for i := 0; i < 40; i++ {
		c := ln[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ctimeAt returns the unix timestamp of the most recent commit touching
// the given 1-indexed line in path. When line == 0, falls back to
// file-level. Critical for honest doc-lag: a markdown file touched
// yesterday for an unrelated typo can still hide a paragraph that's
// been wrong for years. Line-range blame measures the right thing —
// when *this specific* doc reference was last touched.
func (l *lagComputer) ctimeAt(path string, line int) int64 {
	if path == "" || l.repoRoot == "" {
		return 0
	}
	key := lagKey{path: path, line: line}
	if t, ok := l.cache[key]; ok {
		return t
	}
	// Bulk pre-check: a path git doesn't track has no commit history to
	// resolve — return 0 (unknown) without spawning git. This is the single
	// biggest win on synthesized-contract scans, where every element points
	// at generated, uncommitted YAML.
	if !l.isTracked(path) {
		l.cache[key] = 0
		return 0
	}
	var t int64
	if line > 0 {
		// Line-level: one cached `git blame --line-porcelain` per file
		// gives the last-touching commit's committer-time for every line.
		// This replaces a per-element `git log -L <l>,<l>:<path>` — the
		// slowest git op, which dominated scans of large histories.
		t = l.lineCtime(path, line)
		if t == 0 {
			// Out of range, renamed/not-yet-committed line, or blame
			// unavailable: fall back to file-level so a malformed line
			// ref doesn't silently zero the pair. Mirrors the prior
			// `-L`-failure fallback exactly.
			t = l.ctimeAt(path, 0)
		}
	} else {
		// File-level: unchanged. Plain `git log` emits just <ts>.
		out, err := exec.Command("git", "-C", l.repoRoot,
			"log", "-1", "--format=%ct", "--", path).Output()
		if err == nil {
			s := strings.TrimLeft(string(out), "\n\r ")
			if i := strings.IndexAny(s, "\n\r"); i >= 0 {
				s = s[:i]
			}
			t, _ = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		}
	}
	l.cache[key] = t
	return t
}

// lagBucketName maps a whole-day lag to the four-band bucket shared by
// the run-level Lag distribution, the per-element data-lag flag, and
// per-workflow staleness. Fresh (=0d) / Recent (1-30d) / Aging
// (31-180d) / Stale (>180d).
func lagBucketName(d int) string {
	switch {
	case d <= 0:
		return "FRESH"
	case d <= 30:
		return "RECENT"
	case d <= 180:
		return "AGING"
	default:
		return "STALE"
	}
}

// WorkflowRow is one documented workflow — a doc that sequences two or
// more contract elements — with its staleness. A workflow is at risk the
// moment ANY element it orchestrates moves past it (a renamed param, a
// new required step, a deprecated call anywhere in the chain can break
// the sequence), so LagDays is the MAX lag over its referenced elements:
// the time since its most-recently-changed dependency was committed past
// the doc. That is the "go review this sequence" signal a maintainer
// needs — the least-recently-changed element would flatter a workflow
// that a freshly-changed endpoint already broke.
type WorkflowRow struct {
	Path         string // workflow doc source path
	ShortName    string // basename, for display
	URL          string // canonical published URL (workflows adapter's url_base + slug); empty when no url_base configured
	Length       int    // distinct elements the workflow sequences
	LagDays      int    // max(element_code_ts - doc_ts) over referenced elements; 0 when the doc is newer than all of them
	LagBucket    string // FRESH / RECENT / AGING / STALE
	WorstElement string // element id driving LagDays (the most-recently-changed dependency)
}

// computeWorkflowLag scores each workflow doc by the max lag over the
// elements it sequences. `edges` is path→element-id-set (the
// workflowEdgesByPath map the report already builds). Returns rows
// sorted worst (stalest) first. Empty when repoRoot is not a git tree.
func computeWorkflowLag(snap *Snapshot, repoRoot string, edges map[string]map[string]bool, urlByPath map[string]string) []WorkflowRow {
	if snap == nil || repoRoot == "" || len(edges) == 0 {
		return nil
	}
	type loc struct {
		path string
		line int
	}
	elemLoc := make(map[string]loc, len(snap.Elements))
	for _, e := range snap.Elements {
		id := firstString(e["id"])
		if id == "" {
			continue
		}
		l, _ := e["location"].(map[string]any)
		elemLoc[id] = loc{firstString(l["path"]), numAsInt(l["line"])}
	}
	lc := newLagComputer(repoRoot)
	rows := make([]WorkflowRow, 0, len(edges))
	for path, elems := range edges {
		docTS := lc.ctimeAt(path, 0)
		maxLag, worst := 0, ""
		if docTS > 0 {
			for id := range elems {
				el, ok := elemLoc[id]
				if !ok || el.path == "" {
					continue
				}
				codeTS := lc.ctimeAt(el.path, el.line)
				if codeTS > docTS {
					if d := int((codeTS - docTS) / 86400); d > maxLag {
						maxLag, worst = d, id
					}
				}
			}
		}
		rows = append(rows, WorkflowRow{
			Path:         path,
			ShortName:    path[strings.LastIndexByte(path, '/')+1:],
			URL:          urlByPath[path],
			Length:       len(elems),
			LagDays:      maxLag,
			LagBucket:    lagBucketName(maxLag),
			WorstElement: worst,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].LagDays != rows[j].LagDays {
			return rows[i].LagDays > rows[j].LagDays
		}
		return rows[i].Path < rows[j].Path
	})
	return rows
}

// GuideRow is one hand-written guide — a docs.github.com page that
// sequences two or more commands — with its cross-repo staleness.
// Unlike WorkflowRow (which scores first-class WORKFLOW elements against
// a single repo), a guide lives in a DIFFERENT repo from the code it
// teaches (github/docs vs cli/cli). LagDays is the MAX lag over the
// commands the guide walks — the time since its most-recently-changed
// command was committed past the guide. Honest because guide_ts is read
// from the docs repo and code_ts from the code repo: every figure still
// decomposes to a real commit, just across two git trees.
type GuideRow struct {
	Path         string // guide doc path, relative to the workflows docs_dir
	ShortName    string // basename, for display
	URL          string // canonical docs.github.com URL
	Length       int    // distinct commands the guide sequences
	LagDays      int    // max(command_code_ts - guide_ts); 0 when the guide is newer than all
	LagBucket    string // FRESH / RECENT / AGING / STALE
	WorstCommand string // command id driving LagDays
}

// commandOf strips a trailing " --flag" from an element id, yielding the
// owning command: "gh issue create --assignee" → "gh issue create";
// "gh issue create" is returned unchanged.
func commandOf(id string) string {
	if i := strings.Index(id, " --"); i >= 0 {
		return id[:i]
	}
	return id
}

// pathDir returns the directory of a forward-slash repo-relative path.
func pathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

// firstTestDir returns the package directory of the first test reference
// in a profile's tests block (tests are keyed by category — "unit",
// "integration", … — each a list of {path, line, …}). Empty when the
// profile has no test evidence.
func firstTestDir(tests map[string]any) string {
	for _, v := range tests {
		list, ok := v.([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if p := firstString(m["path"]); p != "" {
				return pathDir(p)
			}
		}
	}
	return ""
}

// elementTestCtime dates an element by the most recent committer time across
// its git-tracked test FILES — the proxy computeConceptDocLag uses for "when
// this command's code last changed" when the contract location is generated
// (no git history of its own). Files, not their package dir: ctimeAt resolves
// against a git ls-files index that lists files only, so a directory path
// zeroes out. max (not first) keeps it deterministic and picks the freshest
// signal. Returns 0 when the element has no datable test file.
func elementTestCtime(prof map[string]any, lc *lagComputer) int64 {
	tests, _ := prof["tests"].(map[string]any)
	if tests == nil {
		return 0
	}
	var maxTS int64
	for _, v := range tests {
		list, _ := v.([]any)
		for _, item := range list {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			if p := firstString(m["path"]); p != "" {
				if ts := lc.ctimeAt(p, 0); ts > maxTS {
					maxTS = ts
				}
			}
		}
	}
	return maxTS
}

// guideDocRef is a workflows-surface doc reference (the path + canonical URL).
type guideDocRef struct{ path, url string }

// workflowRefs pulls the workflows-adapter doc references out of a
// profile's docs.reference.byAdapter.workflows.refs list.
func workflowRefs(p map[string]any) []guideDocRef {
	docs, _ := p["docs"].(map[string]any)
	if docs == nil {
		return nil
	}
	ref, _ := docs["reference"].(map[string]any)
	if ref == nil {
		return nil
	}
	ba, _ := ref["byAdapter"].(map[string]any)
	if ba == nil {
		return nil
	}
	wf, _ := ba["workflows"].(map[string]any)
	if wf == nil {
		return nil
	}
	list, _ := wf["refs"].([]any)
	out := make([]guideDocRef, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, guideDocRef{firstString(m["path"]), firstString(m["url"])})
	}
	return out
}

// gitToplevel returns the absolute toplevel of the git work tree that
// contains dir, or "" if dir is empty or not inside a git tree.
func gitToplevel(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// computeGuideLag scores each authored guide (a workflows-surface doc
// that sequences commands) by how stale it is relative to the commands
// it teaches — ACROSS TWO REPOS. codeRepoRoot is the scanned code repo
// (e.g. cli/cli); the guides live under snap.DocSurfaceDirs["workflows"]
// (e.g. a github/docs checkout), a different git tree. Each command is
// dated by its test-evidence package directory, because its contract
// element points at generated YAML with no git history. The two repos
// are queried with two lagComputers — git -C <root> resolves each
// pathspec in the tree that root belongs to. Rows are sorted worst
// (stalest) first; nil when the workflows docs dir is unknown or no
// guide pairs can be formed.
func computeGuideLag(snap *Snapshot, codeRepoRoot string) []GuideRow {
	if snap == nil || codeRepoRoot == "" {
		return nil
	}
	docsDir := snap.DocSurfaceDirs["workflows"]
	if docsDir == "" {
		return nil
	}
	// Guide-lag is the CROSS-repo case: the authored docs live in a
	// different git tree from the code. When docs and code share a repo
	// (or the docs aren't in git), the run-level Lag distribution already
	// covers doc drift — we don't engage here, which also keeps the
	// "cross-repo" framing honest.
	docsTop := gitToplevel(docsDir)
	if docsTop == "" || docsTop == gitToplevel(codeRepoRoot) {
		return nil
	}

	// command id → git-tracked source package dir (from test evidence).
	cmdDir := map[string]string{}
	for _, p := range snap.Profiles {
		id := firstString(p["elementId"])
		if id == "" {
			continue
		}
		tests, _ := p["tests"].(map[string]any)
		dir := firstTestDir(tests)
		if dir == "" {
			continue
		}
		if cmd := commandOf(id); cmdDir[cmd] == "" {
			cmdDir[cmd] = dir
		}
	}

	// guide path → {commands it references, canonical url}.
	type guideAgg struct {
		cmds map[string]bool
		url  string
	}
	guides := map[string]*guideAgg{}
	for _, p := range snap.Profiles {
		cmd := commandOf(firstString(p["elementId"]))
		for _, r := range workflowRefs(p) {
			if r.path == "" {
				continue
			}
			g := guides[r.path]
			if g == nil {
				g = &guideAgg{cmds: map[string]bool{}, url: r.url}
				guides[r.path] = g
			}
			g.cmds[cmd] = true
			if g.url == "" {
				g.url = r.url
			}
		}
	}
	if len(guides) == 0 {
		return nil
	}

	codeLC := newLagComputer(codeRepoRoot)
	docsLC := newLagComputer(docsDir)
	rows := make([]GuideRow, 0, len(guides))
	for path, g := range guides {
		guideTS := docsLC.ctimeAt(path, 0)
		maxLag, worst := 0, ""
		if guideTS > 0 {
			for cmd := range g.cmds {
				dir := cmdDir[cmd]
				if dir == "" {
					continue
				}
				codeTS := codeLC.ctimeAt(dir, 0)
				if codeTS > guideTS {
					if d := int((codeTS - guideTS) / 86400); d > maxLag {
						maxLag, worst = d, cmd
					}
				}
			}
		}
		rows = append(rows, GuideRow{
			Path:         path,
			ShortName:    path[strings.LastIndexByte(path, '/')+1:],
			URL:          g.url,
			Length:       len(g.cmds),
			LagDays:      maxLag,
			LagBucket:    lagBucketName(maxLag),
			WorstCommand: worst,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].LagDays != rows[j].LagDays {
			return rows[i].LagDays > rows[j].LagDays
		}
		return rows[i].Path < rows[j].Path
	})
	return rows
}

// computeLag walks the snapshot's elements and their doc CodeRefs,
// pairs each documented element with each doc file, and returns the
// run-level lag distribution. Orphan elements (no doc) contribute no
// pairs — the orphan count is surfaced separately as Completeness[0].
// Returns a zero LagResult when no pairs can be formed (e.g. no docs,
// or repoRoot empty / not a git working tree).
func computeLag(snap *Snapshot, repoRoot string) LagResult {
	if snap == nil || repoRoot == "" {
		return LagResult{}
	}
	// A shallow clone has only the boundary commit(s), so `git blame`
	// attributes every line of every file to the same commit. Both sides of
	// each (element, doc) pair then resolve to that one timestamp — lag == 0
	// for everything — and it is NOT caught as unresolved (the boundary commit
	// is a real sha, not the uncommitted zero-sha). The result is a confident,
	// uniform, FALSE "0 days behind, all fresh." Refuse to compute it: signal
	// unavailable so the renderer shows a caveat instead of a fake zero.
	if isShallowRepo(repoRoot) {
		return LagResult{
			Unavailable:       true,
			UnavailableReason: "shallow clone — git history truncated; run `git fetch --unshallow` for accurate doc-lag",
		}
	}
	profByID := map[string]map[string]any{}
	for _, p := range snap.Profiles {
		if id := firstString(p["elementId"]); id != "" {
			profByID[id] = p
		}
	}
	lc := newLagComputer(repoRoot)
	var lags []int
	elementMaxLag := map[string]int{}
	inlineN := 0
	unknownN := 0
	type docRef struct {
		path string
		line int
	}
	for _, e := range snap.Elements {
		loc, _ := e["location"].(map[string]any)
		codePath := firstString(loc["path"])
		if codePath == "" {
			continue
		}
		codeLine := numAsInt(loc["line"])
		id := firstString(e["id"])
		prof := profByID[id]
		if prof == nil {
			continue
		}
		docs, _ := prof["docs"].(map[string]any)
		if docs == nil {
			continue
		}
		var refs []docRef
		walkDocPaths(docs, func(p string, l int) { refs = append(refs, docRef{p, l}) })
		if len(refs) == 0 {
			continue
		}
		codeTS := lc.ctimeAt(codePath, codeLine)
		for _, dr := range refs {
			// Skip in-source doc comments (the doc reference and the
			// element source live in the same file). They're tautologically
			// version-locked — counting them as "0 days lag" would dilute
			// the median dishonestly. Lag is a cross-file drift metric;
			// inline refs are tracked separately as Inline for transparency.
			if dr.path == codePath {
				inlineN++
				continue
			}
			// When the element source itself has no resolvable history, the
			// pair is unknown regardless of the doc — short-circuit before
			// the (potentially expensive) doc blame. Output-identical to the
			// combined `codeTS == 0 || docTS == 0` check below; it just skips
			// the wasted doc lookup on synthesized-contract scans.
			if codeTS == 0 {
				unknownN++
				continue
			}
			docTS := lc.ctimeAt(dr.path, dr.line)
			// A pair with no resolvable git timestamp (shallow clone,
			// renamed file, out-of-bounds line ref that even file-level
			// blame can't recover) is DROPPED, not counted as Fresh —
			// counting it as lag=0 would dishonestly inflate the Fresh
			// band. Dropped pairs are disclosed as the unknown rate.
			if docTS == 0 {
				unknownN++
				continue
			}
			lag := 0
			if codeTS > docTS {
				lag = int((codeTS - docTS) / 86400)
			}
			lags = append(lags, lag)
			if cur, ok := elementMaxLag[id]; !ok || lag > cur {
				elementMaxLag[id] = lag
			}
		}
	}
	if len(lags) == 0 {
		return LagResult{Inline: inlineN, Unknown: unknownN}
	}
	return finalizeLagResult(lags, elementMaxLag, inlineN, unknownN)
}

// finalizeLagResult turns a slice of per-pair lag values (+ each element's
// worst lag) into the sorted LagResult the Lag section renders: percentiles,
// the four Fresh/Recent/Aging/Stale bucket counts, and the per-element
// worst-bucket map. Shared by computeLag and computeConceptDocLag so both
// produce an identically-shaped distribution.
func finalizeLagResult(lags []int, elementMaxLag map[string]int, inlineN, unknownN int) LagResult {
	sort.Ints(lags)
	elementBuckets := make(map[string]string, len(elementMaxLag))
	for id, d := range elementMaxLag {
		elementBuckets[id] = lagBucketName(d)
	}
	res := LagResult{
		Pairs:          len(lags),
		Inline:         inlineN,
		Unknown:        unknownN,
		P50:            intPercentile(lags, 50),
		P90:            intPercentile(lags, 90),
		Sorted:         lags,
		ElementBuckets: elementBuckets,
	}
	for _, d := range lags {
		switch {
		case d <= 0:
			res.Buckets[0]++
		case d <= 30:
			res.Buckets[1]++
		case d <= 180:
			res.Buckets[2]++
		default:
			res.Buckets[3]++
		}
	}
	return res
}

// computeConceptDocLag is the fallback doc-lag computation for scans whose
// element CONTRACT carries no git history — e.g. ffx, whose 680-command
// surface is synthesized from CLI goldens into generated cobra YAML with no
// commits to difference. The run-level computeLag finds no cross-file pairs
// there, so the Lag section would wrongly read "no documentation surface"
// even though the narrative concept docs plainly exist (the rules file routes
// them to docs.concepts precisely as the doc-lag carrier). This dates each
// element instead by its git-tracked TEST-EVIDENCE package dir — the same
// proxy computeGuideLag uses for commands — and differences the CONCEPT docs
// against it: lag = max(0, element_test_dir_ts − concept_doc_ts) per
// (element, concept-doc) pair. Same-repo, unlike computeGuideLag's cross-repo
// guides: both the docs and the test evidence live in the scanned tree.
// Returns a LagResult shaped exactly like computeLag's, so the Lag section
// renders its Fresh/Recent/Aging/Stale distribution unchanged. Empty when no
// element pairs a concept doc with a datable test dir (callers keep the
// existing empty-state in that case).
func computeConceptDocLag(snap *Snapshot, repoRoot string) LagResult {
	if snap == nil || repoRoot == "" {
		return LagResult{}
	}
	profByID := map[string]map[string]any{}
	for _, p := range snap.Profiles {
		if id := firstString(p["elementId"]); id != "" {
			profByID[id] = p
		}
	}
	lc := newLagComputer(repoRoot)
	var lags []int
	elementMaxLag := map[string]int{}
	unknownN := 0
	type cdRef struct {
		path string
		line int
	}
	for _, e := range snap.Elements {
		id := firstString(e["id"])
		if id == "" {
			continue
		}
		prof := profByID[id]
		if prof == nil {
			continue
		}
		docs, _ := prof["docs"].(map[string]any)
		if docs == nil {
			continue
		}
		// The narrative concept-doc surface is docs.concepts (plural) — a list
		// of DocClaims the conceptdoc engine emits, each with a sourcePath (the
		// guide file) and a location (the in-file anchor). This is distinct
		// from the legacy docs.concept (singular) `///`-fed refs that
		// walkDocPaths reads; it's the same bucket countConceptDoc tallies and
		// the surface the rules file flags as the doc-lag carrier.
		claims, _ := docs["concepts"].([]any)
		var refs []cdRef
		for _, item := range claims {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			path := firstString(m["sourcePath"])
			line := 0
			if loc, ok := m["location"].(map[string]any); ok {
				if path == "" {
					path = firstString(loc["path"])
				}
				line = numAsInt(loc["line"])
			}
			if path == "" {
				continue
			}
			refs = append(refs, cdRef{path, line})
		}
		if len(refs) == 0 {
			continue
		}
		// The contract location is generated (no history); date the element by
		// the most recent commit across its git-tracked test files instead —
		// the closest proxy for "when this command's code last changed".
		// Test FILES (not their package dir): lagComputer.ctimeAt resolves
		// tracked files from a git ls-files index that has no directory
		// entries, so a dir path would zero out. Elements with no datable test
		// file can't be placed on the timeline — skip them (no false freshness).
		codeTS := elementTestCtime(prof, lc)
		if codeTS == 0 {
			unknownN++
			continue
		}
		for _, dr := range refs {
			docTS := lc.ctimeAt(dr.path, dr.line)
			if docTS == 0 {
				unknownN++
				continue
			}
			lag := 0
			if codeTS > docTS {
				lag = int((codeTS - docTS) / 86400)
			}
			lags = append(lags, lag)
			if cur, ok := elementMaxLag[id]; !ok || lag > cur {
				elementMaxLag[id] = lag
			}
		}
	}
	if len(lags) == 0 {
		return LagResult{Unknown: unknownN}
	}
	return finalizeLagResult(lags, elementMaxLag, 0, unknownN)
}

// walkDocPaths visits every DocRef (path + line) under a profile's `docs`
// block, regardless of nesting: reference.{fidldoc, clidoc, dockerdoc,
// byAdapter[*].refs}, concept, tutorial, guide.{migration, troubleshooting,
// cookbook}, proposal.{rfc, design}, releaseNotes, faq. Mirrors the
// categories in proto/coverage_profile.proto's DocCoverage. Line is the
// 1-indexed anchor line of the reference within the doc file; 0 when the
// adapter didn't populate it.
func walkDocPaths(docs map[string]any, fn func(path string, line int)) {
	if docs == nil {
		return
	}
	if ref, ok := docs["reference"].(map[string]any); ok {
		for _, key := range []string{"fidldoc", "clidoc", "dockerdoc"} {
			collectDocRefPaths(ref[key], fn)
		}
		if ba, ok := ref["byAdapter"].(map[string]any); ok {
			for k, v := range ba {
				// Workflows are scored separately — each workflow gets its
				// own staleness (computeWorkflowLag), not one per-pair lag
				// per element it sequences. Folding them in here would let a
				// single 20-endpoint workflow doc add 20 lag pairs and
				// over-weight the distribution.
				if k == "workflows" {
					continue
				}
				if list, ok := v.(map[string]any); ok {
					collectDocRefPaths(list["refs"], fn)
				}
			}
		}
	}
	for _, key := range []string{"concept", "tutorial", "releaseNotes", "faq"} {
		collectDocRefPaths(docs[key], fn)
	}
	if g, ok := docs["guide"].(map[string]any); ok {
		for _, key := range []string{"migration", "troubleshooting", "cookbook"} {
			collectDocRefPaths(g[key], fn)
		}
	}
	if p, ok := docs["proposal"].(map[string]any); ok {
		for _, key := range []string{"rfc", "design"} {
			collectDocRefPaths(p[key], fn)
		}
	}
}

func collectDocRefPaths(v any, fn func(string, int)) {
	list, _ := v.([]any)
	for _, item := range list {
		ref, _ := item.(map[string]any)
		if p := firstString(ref["path"]); p != "" {
			fn(p, numAsInt(ref["line"]))
		}
	}
}

// intPercentile returns the q-th percentile (0..100) of an already-
// sorted []int. Linear interpolation; rounded to the nearest int.
func intPercentile(sorted []int, q int) int {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	pos := float64(q) / 100.0 * float64(n-1)
	lo := int(pos)
	frac := pos - float64(lo)
	if lo+1 < n {
		return int(float64(sorted[lo])*(1-frac) + float64(sorted[lo+1])*frac + 0.5)
	}
	return sorted[lo]
}
