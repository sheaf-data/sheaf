package scanner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// gitLogLineCtime runs `git log -1 --format=%ct -L l,l:rel` — the exact
// per-line query ctimeAt used before the blame rewrite — and returns the
// committer-time of the line's last-touching commit. This is the ground
// truth the blame-derived value must equal. Returns 0 on error/no output.
func gitLogLineCtime(t *testing.T, dir, rel string, line int) int64 {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%ct",
		"-L", fmt.Sprintf("%d,%d:%s", line, line, rel)).Output()
	if err != nil {
		t.Fatalf("git log -L %d:%s: %v", line, rel, err)
	}
	s := strings.TrimLeft(string(out), "\n\r ")
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

// gitRun runs a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitEnv pins author+committer identity and timestamp so the
// computed lag is deterministic regardless of the host's git config.
func commitEnv(unix int64) []string {
	ts := isoFromUnix(unix)
	return []string{
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_AUTHOR_DATE=" + ts, "GIT_COMMITTER_DATE=" + ts,
	}
}

// isoFromUnix formats a unix timestamp as a git-accepted "@<unix> +0000".
func isoFromUnix(unix int64) string {
	return "@" + strconv.FormatInt(unix, 10) + " +0000"
}

// writeFile writes a file under dir, creating parents.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// initRepo creates a fresh git working tree and returns its root.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, nil, "init", "-q")
	gitRun(t, dir, nil, "config", "user.name", "t")
	gitRun(t, dir, nil, "config", "user.email", "t@t")
	return dir
}

const day = int64(86400)

// snapWith builds a minimal Snapshot pairing one code element with the
// given doc references (each a path+line under docs.concept).
func snapWith(codePath string, codeLine int, docRefs ...map[string]any) *Snapshot {
	refs := make([]any, 0, len(docRefs))
	for _, r := range docRefs {
		refs = append(refs, r)
	}
	return &Snapshot{
		Library: "t",
		Elements: []map[string]any{
			{"id": "E1", "location": map[string]any{"path": codePath, "line": codeLine}},
		},
		Profiles: []map[string]any{
			{"elementId": "E1", "docs": map[string]any{"concept": refs}},
		},
	}
}

// TestComputeLag_UnresolvedPairBecomesUnknown proves a pair whose doc
// path has no git history is dropped from Pairs/Buckets and disclosed
// as Unknown — not silently counted as Fresh.
func TestComputeLag_UnresolvedPairBecomesUnknown(t *testing.T) {
	root := initRepo(t)
	base := int64(1_700_000_000)

	// Commit a code file at base time.
	writeFile(t, root, "src/code.go", "package code\n")
	gitRun(t, root, nil, "add", "src/code.go")
	gitRun(t, root, commitEnv(base), "commit", "-q", "-m", "code")

	// The doc reference points at a path that was never committed, so
	// docTS == 0 and the pair must be dropped as Unknown.
	snap := snapWith("src/code.go", 1, map[string]any{"path": "docs/missing.md", "line": 1})

	res := computeLag(snap, root)
	if res.Unknown != 1 {
		t.Errorf("Unknown = %d, want 1", res.Unknown)
	}
	if res.Pairs != 0 {
		t.Errorf("Pairs = %d, want 0 (unresolved pair must not be counted)", res.Pairs)
	}
	var bucketSum int
	for _, b := range res.Buckets {
		bucketSum += b
	}
	if bucketSum != 0 {
		t.Errorf("Buckets sum = %d, want 0 (unresolved pair must not land in a band)", bucketSum)
	}
}

// TestComputeLag_DocNewerThanCodeIsFresh proves a resolved pair whose
// doc was committed after the code counts as Fresh (lag 0) and does NOT
// increment Unknown.
func TestComputeLag_DocNewerThanCodeIsFresh(t *testing.T) {
	root := initRepo(t)
	base := int64(1_700_000_000)

	writeFile(t, root, "src/code.go", "package code\n")
	gitRun(t, root, nil, "add", "src/code.go")
	gitRun(t, root, commitEnv(base), "commit", "-q", "-m", "code")

	// Doc committed 10 days AFTER the code: codeTS <= docTS, so lag 0.
	writeFile(t, root, "docs/guide.md", "see code\n")
	gitRun(t, root, nil, "add", "docs/guide.md")
	gitRun(t, root, commitEnv(base+10*day), "commit", "-q", "-m", "doc")

	snap := snapWith("src/code.go", 1, map[string]any{"path": "docs/guide.md", "line": 1})

	res := computeLag(snap, root)
	if res.Unknown != 0 {
		t.Errorf("Unknown = %d, want 0 (resolved pair must not be unknown)", res.Unknown)
	}
	if res.Pairs != 1 {
		t.Fatalf("Pairs = %d, want 1", res.Pairs)
	}
	if res.Buckets[0] != 1 {
		t.Errorf("Buckets[0] (Fresh) = %d, want 1", res.Buckets[0])
	}
	if res.P50 != 0 {
		t.Errorf("P50 = %d, want 0 (doc newer than code is zero lag)", res.P50)
	}
}

// TestComputeLag_StaleCodeNewerThanDoc proves a resolved pair whose code
// is much newer than the doc lands in a lag band and is not Unknown.
func TestComputeLag_StaleCodeNewerThanDoc(t *testing.T) {
	root := initRepo(t)
	base := int64(1_700_000_000)

	// Doc first, then code 200 days later → >180d Stale band.
	writeFile(t, root, "docs/guide.md", "see code\n")
	gitRun(t, root, nil, "add", "docs/guide.md")
	gitRun(t, root, commitEnv(base), "commit", "-q", "-m", "doc")

	writeFile(t, root, "src/code.go", "package code\n")
	gitRun(t, root, nil, "add", "src/code.go")
	gitRun(t, root, commitEnv(base+200*day), "commit", "-q", "-m", "code")

	snap := snapWith("src/code.go", 1, map[string]any{"path": "docs/guide.md", "line": 1})

	res := computeLag(snap, root)
	if res.Unknown != 0 {
		t.Errorf("Unknown = %d, want 0", res.Unknown)
	}
	if res.Pairs != 1 {
		t.Fatalf("Pairs = %d, want 1", res.Pairs)
	}
	if res.Buckets[3] != 1 {
		t.Errorf("Buckets[3] (Stale) = %d, want 1; buckets=%v", res.Buckets[3], res.Buckets)
	}
}

// TestCtimeAt_BlameLineLevel proves the blame-based ctimeAt resolves
// each source line to its last-touching commit's committer-time, agrees
// with the old `git log -L` query line-for-line, falls back to file-level
// for out-of-range lines, and runs `git blame` exactly once per file no
// matter how many lines are queried.
func TestCtimeAt_BlameLineLevel(t *testing.T) {
	root := initRepo(t)
	t1 := int64(1_700_000_000)
	t2 := t1 + 50*day // a different, later commit for the modified line

	// Commit a 3-line file at T1.
	writeFile(t, root, "src/code.go", "line one\nline two\nline three\n")
	gitRun(t, root, nil, "add", "src/code.go")
	gitRun(t, root, commitEnv(t1), "commit", "-q", "-m", "initial")

	// Modify ONLY line 2, commit at T2. Lines 1 and 3 stay at T1.
	writeFile(t, root, "src/code.go", "line one\nLINE TWO CHANGED\nline three\n")
	gitRun(t, root, nil, "add", "src/code.go")
	gitRun(t, root, commitEnv(t2), "commit", "-q", "-m", "edit line 2")

	lc := newLagComputer(root)

	// Line 1 (untouched since T1) → T1; line 2 (edited at T2) → T2.
	if got := lc.ctimeAt("src/code.go", 1); got != t1 {
		t.Errorf("ctimeAt(line 1) = %d, want T1 %d", got, t1)
	}
	if got := lc.ctimeAt("src/code.go", 2); got != t2 {
		t.Errorf("ctimeAt(line 2) = %d, want T2 %d", got, t2)
	}
	if got := lc.ctimeAt("src/code.go", 3); got != t1 {
		t.Errorf("ctimeAt(line 3) = %d, want T1 %d", got, t1)
	}

	// CROSS-CHECK: blame-derived value must equal the old `git log -L`
	// query for every line. They name the same last-touching commit, so
	// %ct (== committer-time) must agree.
	for line := 1; line <= 3; line++ {
		want := gitLogLineCtime(t, root, "src/code.go", line)
		if got := lc.ctimeAt("src/code.go", line); got != want {
			t.Errorf("line %d: blame ctimeAt=%d, git log -L=%d (must agree)", line, got, want)
		}
		if want == 0 {
			t.Errorf("line %d: git log -L returned 0 (test setup broken)", line)
		}
	}

	// Out-of-range line (file has 3 lines) falls back to file-level — the
	// most recent commit touching the file is the T2 edit — and is non-zero.
	fileLevel := lc.ctimeAt("src/code.go", 0)
	if fileLevel == 0 {
		t.Fatalf("file-level ctimeAt = 0, want non-zero")
	}
	if fileLevel != t2 {
		t.Errorf("file-level ctimeAt = %d, want T2 %d (most recent commit)", fileLevel, t2)
	}
	if got := lc.ctimeAt("src/code.go", 999); got != fileLevel {
		t.Errorf("out-of-range ctimeAt(999) = %d, want file-level fallback %d", got, fileLevel)
	}

	// blame must have run ONCE for this one file, despite the many
	// ctimeAt(line>0) calls above. (File-level + out-of-range calls go
	// through `git log`, not blame, so they don't bump the counter.)
	if lc.blameCalls != 1 {
		t.Errorf("blameCalls = %d, want 1 (blame must be cached per file)", lc.blameCalls)
	}
}

// TestCtimeAt_BlameFailureCachesEmpty proves that when blame can't
// resolve a path (here: a path with no git history at all), the empty
// slice is cached so blame is not re-run, and every line-level query
// falls back to file-level (which is also 0 for an unknown path).
func TestCtimeAt_UntrackedPathSkipsBlame(t *testing.T) {
	root := initRepo(t)
	// A real repo with one commit, so the bulk `git ls-files` tracked-set
	// resolves (and contains exactly src/real.go).
	writeFile(t, root, "src/real.go", "package real\n")
	gitRun(t, root, nil, "add", "src/real.go")
	gitRun(t, root, commitEnv(1_700_000_000), "commit", "-q", "-m", "real")

	lc := newLagComputer(root)
	// A path git doesn't track (never committed) resolves to 0 (unknown) via
	// the one-shot ls-files tracked-set check — WITHOUT spawning a per-file
	// blame. This is the bulk lookup that keeps synthesized-contract scans
	// (elements pointing at generated, uncommitted files) from paying one
	// always-failing git spawn per element.
	if got := lc.ctimeAt("src/ghost.go", 5); got != 0 {
		t.Errorf("ctimeAt(untracked path, line 5) = %d, want 0", got)
	}
	// Repeated queries stay cheap map lookups — no blame, no repeated ls-files.
	_ = lc.ctimeAt("src/ghost.go", 6)
	_ = lc.ctimeAt("src/ghost.go", 7)
	if lc.blameCalls != 0 {
		t.Errorf("blameCalls = %d, want 0 (untracked paths resolve via the bulk ls-files check, never blamed)", lc.blameCalls)
	}

	// Sanity: a TRACKED file still resolves through blame (one cached call).
	if got := lc.ctimeAt("src/real.go", 1); got == 0 {
		t.Errorf("ctimeAt(tracked file) = 0, want a real commit time")
	}
	if lc.blameCalls != 1 {
		t.Errorf("blameCalls = %d, want 1 (the tracked file is blamed once)", lc.blameCalls)
	}
}
