package verify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// This file holds the structural, snapshot-derived signals that need no
// network and no external tools — they run on every verify. The
// repo-grep oracle (false-negative search, attribution sampling, doc-URL
// resolution, ground-truth element count) lives behind the --disk flag and
// is added separately.

// --- contamination: refs sourced from vendored/generated/worktree trees --

// contaminatingDirs are path segments that are (almost) never a legitimate
// source location for an attribution. A test/doc attributed from one of
// these is a phantom duplicate that inflates counts. Kept deliberately
// conservative — only segments that are unambiguously not first-party
// source — so the signal stays low-false-positive.
var contaminatingDirs = []string{
	"vendor", "third_party", "node_modules", "bazel-out", ".git", ".claude", "worktrees",
}

// contaminatingSegment returns the contaminating directory in a path, or ""
// if the path is clean. Matches on whole path segments only.
func contaminatingSegment(path string) string {
	wrapped := "/" + strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/") + "/"
	for _, d := range contaminatingDirs {
		if strings.Contains(wrapped, "/"+d+"/") {
			return d
		}
	}
	return ""
}

// allRefPaths collects every distinct source path referenced under a
// profile's tests/docs/examples surfaces (any nested object carrying a
// "path"). Robust to bucket-shape changes — it walks the subtree rather
// than hard-coding each bucket.
func allRefPaths(prof map[string]any) []string {
	if prof == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if p, ok := t["path"].(string); ok && p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			for _, vv := range t {
				walk(vv)
			}
		case []any:
			for _, vv := range t {
				walk(vv)
			}
		}
	}
	for _, key := range []string{"tests", "docs", "examples"} {
		walk(prof[key])
	}
	return out
}

// detectContamination flags attributions whose evidence lives in a
// vendored / generated / git-worktree tree — the scan's include globs
// reached somewhere they should skip, and the counts are inflated with
// phantom duplicates of the real sources. Snapshot-derived; always runs.
func detectContamination(rep *Report, rd *scanner.ReportData, profByID map[string]map[string]any) {
	hits := map[string]int{}      // dir -> ref count
	sample := map[string]string{} // dir -> one example path
	for i := range rd.Methods {
		for _, p := range allRefPaths(profByID[rd.Methods[i].Name]) {
			if d := contaminatingSegment(p); d != "" {
				hits[d]++
				if sample[d] == "" {
					sample[d] = p
				}
			}
		}
	}
	if len(hits) == 0 {
		return
	}
	dirs := make([]string, 0, len(hits))
	for d := range hits {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var ev []string
	total := 0
	for _, d := range dirs {
		total += hits[d]
		ev = append(ev, fmt.Sprintf("%s/ — %d ref(s), e.g. %s", d, hits[d], sample[d]))
	}
	rep.add(Finding{
		Category: CatContamination, Severity: SeverityWarn,
		Title:    fmt.Sprintf("%d attribution(s) come from vendored/generated/worktree trees", total),
		Detail:   "Tests or docs attributed from a vendored, generated, or git-worktree copy are phantom duplicates of the real sources — they inflate coverage counts. The scan's include/exclude globs reached a tree they should skip (the classic case: `**/*_test.go` walking into git worktrees under .claude/).",
		Evidence: ev,
		Fix:      "Add exclude globs for these directories (e.g. exclude: \".claude/**\", \"vendor/**\") and re-scan.",
	})
}

// --- name collisions: single-common-word element names with attributions --

// collisionWords are common English identifiers that, when they are an
// element's whole local name, make name-token matching collide with
// unrelated tests/docs. Attributions on these are the likeliest false
// positives and the first to spot-check.
var collisionWords = map[string]bool{
	"run": true, "get": true, "list": true, "set": true, "add": true, "delete": true,
	"create": true, "update": true, "value": true, "event": true, "command": true,
	"status": true, "result": true, "client": true, "server": true, "handler": true,
	"context": true, "request": true, "response": true, "error": true, "config": true,
	"name": true, "type": true, "data": true, "info": true, "item": true, "node": true,
	"key": true, "id": true, "state": true, "message": true, "option": true, "options": true,
	"start": true, "stop": true, "open": true, "close": true, "read": true, "write": true,
}

// localName returns the last identifier segment of an element id, lowered.
// "lib/Service.Method" -> "method"; "tool sub" -> "sub";
// "ns::Class::run" -> "run".
func localName(id string) string {
	s := id
	for _, sep := range []string{"/", " ", "::", "."} {
		if i := strings.LastIndex(s, sep); i >= 0 {
			s = s[i+len(sep):]
		}
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// detectNameCollision flags attributed elements whose local name is a
// single common word — the construction most prone to name-token false
// positives. Aggregated into one finding so it never floods the ledger.
// Snapshot-derived; always runs.
func detectNameCollision(rep *Report, rd *scanner.ReportData, _ map[string]map[string]any) {
	var prone []string
	for i := range rd.Methods {
		m := rd.Methods[i]
		if m.Removed || m.Test == 0 {
			continue
		}
		if collisionWords[localName(m.Name)] {
			prone = append(prone, m.Name)
		}
	}
	if len(prone) == 0 {
		return
	}
	sort.Strings(prone)
	ev := prone
	if len(ev) > 15 {
		ev = append(append([]string{}, prone[:15]...), fmt.Sprintf("…and %d more", len(prone)-15))
	}
	rep.add(Finding{
		Category: CatNameCollision, Severity: SeverityWarn,
		Title:    fmt.Sprintf("%d attributed element(s) have single-common-word names — likeliest false positives", len(prone)),
		Detail:   "Name-token matching attributes a test/doc to an element by shared identifier tokens. When the element's local name is a common word (run, get, value, …), unrelated tests collide with it. These are the first attributions to spot-check.",
		Evidence: ev,
		Fix:      "Read a sample of these elements' attributed tests on disk; confirm each test really exercises the element rather than merely sharing a word.",
	})
}
