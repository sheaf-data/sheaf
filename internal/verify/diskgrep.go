package verify

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// diskOracle runs the repo-grep checks that need the source tree. Today
// that is a high-precision false-negative search: for elements the report
// calls untested, it greps the tree for the element's distinctive name and
// surfaces unattributed hits in test files as CANDIDATES.
//
// It is conservative by construction — it only searches names that have a
// distinctive form (a flag literal, or a qualified Parent.Leaf) and skips
// single-common-token names that would produce noisy hits. A hit is a
// candidate (the name appears in a test file the scan didn't attribute to
// this element), not a confirmed miss; confirming it is the agent's job.
//
// Runs only when --disk is set and a repo root is available. Uses
// `git grep` so the oracle needs no extra tool (git is already required
// for doc-lag) and only searches the tracked tree.
func diskOracle(rep *Report, rd *scanner.ReportData, profByID map[string]map[string]any, opts Options) {
	if _, err := exec.LookPath("git"); err != nil {
		rep.add(Finding{
			Category: CatUnverifiable, Severity: SeverityWarn,
			Title:  "git not found — false-negative search skipped",
			Detail: "The repo-grep oracle uses `git grep` to scan the tracked source tree. With git unavailable, run the false-negative search by hand per the onboarding procedure.",
		})
		return
	}

	maxElems := opts.MaxDiskElements
	if maxElems <= 0 {
		maxElems = 40
	}
	const maxHitsPerElem = 5

	type target struct {
		id, term   string
		attributed map[string]bool
	}
	var targets []target
	for i := range rd.Methods {
		m := rd.Methods[i]
		if m.Removed || m.Test > 0 {
			continue
		}
		term := fnSearchTerm(m.Name, m.Kind)
		if term == "" {
			continue // no distinctive form — skip rather than guess noisily
		}
		targets = append(targets, target{
			id: m.Name, term: term, attributed: attributedTestFiles(profByID[m.Name]),
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].id < targets[j].id })

	total := len(targets)
	if total > maxElems {
		targets = targets[:maxElems]
		rep.Caveats = append(rep.Caveats, fmt.Sprintf(
			"False-negative search capped at %d of %d reportedly-untested elements with a distinctive name (raise --max-disk-elements to cover more).",
			maxElems, total))
	}

	type cand struct {
		element, path, term string
		line                int
	}
	var cands []cand
	for _, t := range targets {
		hits := 0
		for _, h := range gitGrep(opts.RepoRoot, t.term) {
			if hits >= maxHitsPerElem {
				break
			}
			if !looksLikeTest(h.path) || contaminatingSegment(h.path) != "" {
				continue
			}
			if t.attributed[h.path] {
				continue // already attributed to this element — not a miss
			}
			cands = append(cands, cand{element: t.id, path: h.path, line: h.line, term: t.term})
			hits++
		}
	}

	if len(cands) == 0 {
		if len(targets) > 0 {
			rep.Caveats = append(rep.Caveats, fmt.Sprintf(
				"False-negative search: grepped %d reportedly-untested element(s) with a distinctive name; found no unattributed test mentions.", len(targets)))
		}
		return
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].element != cands[j].element {
			return cands[i].element < cands[j].element
		}
		return cands[i].path < cands[j].path
	})
	ev := make([]string, 0, len(cands))
	for _, c := range cands {
		if len(ev) >= 25 {
			ev = append(ev, fmt.Sprintf("…and %d more", len(cands)-25))
			break
		}
		ev = append(ev, fmt.Sprintf("%s ← %s:%d (matched %q)", c.element, c.path, c.line, c.term))
	}
	rep.add(Finding{
		Category: CatFalseNegative, Severity: SeverityWarn,
		Title:    fmt.Sprintf("%d false-negative candidate(s): untested elements whose name appears in unattributed test files", len(cands)),
		Detail:   "Each element below is reported as having no tests, yet its distinctive name appears in a test file the scan did not attribute to it — either a real missed edge (the adapter's matcher has a blind spot) or a coincidental mention.",
		Evidence: ev,
		Fix:      "Open a few cited lines: if the test genuinely exercises the element, the adapter is under-attributing this idiom (worth an adapter note); if not, it's a coincidental mention and the untested verdict stands.",
	})
}

// commonFlags are flag names shared across many commands/binaries. A
// flag-literal search for one of these (e.g. "--repo", "--config") matches
// unrelated commands' tests, so we skip the FN search for them rather than
// surface cross-command noise — the element stays honestly untested.
var commonFlags = map[string]bool{
	"repo": true, "config": true, "help": true, "verbose": true, "quiet": true,
	"output": true, "out": true, "format": true, "port": true, "version": true,
	"debug": true, "force": true, "dry-run": true, "yes": true, "file": true,
	"dir": true, "path": true, "name": true, "all": true, "json": true, "base": true,
	"library": true, "ecosystem": true,
}

// fnSearchTerm returns a distinctive search string for an element, or ""
// when the name is too generic to search without noise.
func fnSearchTerm(id, kind string) string {
	switch kind {
	case "FLAG", "SWITCH", "CONFIG_KNOB":
		l := strings.TrimLeft(lastSegment(id), "-")
		if len(l) < 2 || commonFlags[strings.ToLower(l)] {
			return "" // too short, or shared across commands → unsearchable without noise
		}
		return "--" + l
	case "METHOD", "PROTOCOL", "TYPE", "SYSCALL", "CPP_METHOD", "CPP_CLASS", "RUST_TYPE", "CPP_FREE_FUNCTION":
		// Distinctive only when qualified (Parent.Leaf / Parent::Leaf).
		q := id
		if i := strings.LastIndex(q, "/"); i >= 0 {
			q = q[i+1:]
		}
		if !strings.ContainsAny(q, ".:") {
			return ""
		}
		return q
	}
	return ""
}

// lastSegment returns the last identifier segment of an element id,
// preserving case. "sheaf scan quiet" -> "quiet"; "ns::C::run" -> "run".
func lastSegment(id string) string {
	s := id
	for _, sep := range []string{"/", " ", "::", "."} {
		if i := strings.LastIndex(s, sep); i >= 0 {
			s = s[i+len(sep):]
		}
	}
	return strings.TrimSpace(s)
}

// attributedTestFiles is the set of repo-relative paths already attributed
// as tests for an element — used to exclude them from the FN search.
func attributedTestFiles(prof map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, r := range testRefs(prof) {
		if p, _ := r["path"].(string); p != "" {
			out[p] = true
		}
	}
	return out
}

// looksLikeTest reports whether a path is a test file by convention.
// Deliberately tight so "latest.go" and the like don't match.
func looksLikeTest(p string) bool {
	lp := strings.ToLower(p)
	b := filepath.Base(lp)
	wrapped := "/" + lp
	switch {
	case strings.Contains(b, "_test."):
		return true
	case strings.HasPrefix(b, "test_"):
		return true
	case strings.HasSuffix(b, ".bats"):
		return true
	case strings.Contains(b, "_spec."):
		return true
	case strings.Contains(wrapped, "/test/") || strings.Contains(wrapped, "/tests/"):
		return true
	}
	return false
}

type grepHit struct {
	path string
	line int
}

// gitGrep runs `git grep` for a fixed-string term over the tracked tree and
// returns parsed, repo-root-relative hits. git grep exits 1 on no-match —
// that's not an error here. --full-name keeps paths relative to the repo
// toplevel so they line up with the snapshot's paths.
func gitGrep(repoRoot, term string) []grepHit {
	cmd := exec.Command("git", "grep", "--full-name", "-n", "-F", "-I", "--no-color", "-e", term)
	cmd.Dir = repoRoot
	var buf bytes.Buffer
	cmd.Stdout = &buf
	_ = cmd.Run()

	var hits []grepHit
	sc := bufio.NewScanner(&buf)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		// path:line:content
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) < 3 {
			continue
		}
		ln, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		hits = append(hits, grepHit{path: strings.TrimPrefix(parts[0], "./"), line: ln})
	}
	return hits
}
