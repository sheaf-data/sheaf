package verify

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// This file holds the doc-URL resolution check (category doc_url). A
// generated doc URL that 404s — usually a slug/underscore convention
// mismatch between the URL template and the published site — is a dead link
// even when the attribution join is correct, and it is the first thing a
// reviewer clicks. The check is a network side effect, so it runs only
// behind BOTH --disk and the explicit --check-urls opt-in.
//
// Honesty rail (see the package doc): a reachable URL that returns non-2xx
// is provable wrongness → a warning with the URL as evidence. A URL we
// cannot reach (offline, timeout, transport error) is NOT a finding — it is
// skipped silently, and if EVERY sampled URL was unreachable the report
// carries a single caveat that the network could not be reached rather than
// any claim about the links.

const (
	// maxDocURLs caps how many distinct URLs are resolved per run so the
	// network work stays bounded; the overflow is named in a caveat.
	maxDocURLs = 20
	// maxDocURLFindings bounds how many per-link dead-link findings are
	// emitted in the mixed case so the ledger never floods.
	maxDocURLFindings = 10
	// systemicMinSample is how many URLs must be checked before an
	// all-dead result is described as a confident base/publication issue
	// rather than a single ambiguous dead link.
	systemicMinSample = 3
	// docURLPerRequestTimeout bounds a single HEAD/GET.
	docURLPerRequestTimeout = 3 * time.Second
	// docURLGlobalBudget bounds the whole check so a slow host can't stall
	// the verify run.
	docURLGlobalBudget = 20 * time.Second
)

// defaultDocURLClient is the production HTTP client: a short per-request
// timeout, default redirect following (a 301→200 is a live link).
func defaultDocURLClient() *http.Client {
	return &http.Client{Timeout: docURLPerRequestTimeout}
}

// docURLRef is one distinct published doc URL plus the element and source
// path it was attributed to (for the finding's evidence). status is filled
// in once the URL is resolved.
type docURLRef struct {
	element, url, path string
	status             int
}

// collectDocURLs gathers the distinct doc URLs across the reported elements
// in deterministic order: elements in report order, URLs within an element
// sorted, first occurrence wins. Determinism matters because the sample cap
// must pick the same URLs on every run.
func collectDocURLs(rd *scanner.ReportData, profByID map[string]map[string]any) []docURLRef {
	seen := map[string]bool{}
	var out []docURLRef
	for i := range rd.Methods {
		m := rd.Methods[i]
		if m.Removed {
			continue
		}
		for _, u := range docURLNodes(profByID[m.Name]) {
			if seen[u.url] {
				continue
			}
			seen[u.url] = true
			out = append(out, docURLRef{element: m.Name, url: u.url, path: u.path})
		}
	}
	return out
}

// docURLNodes walks a profile's docs subtree and returns every node that
// carries a non-empty url, paired with its sibling path, sorted for
// determinism (Go map iteration order is randomized).
func docURLNodes(prof map[string]any) []docURLRef {
	if prof == nil {
		return nil
	}
	var out []docURLRef
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if u, ok := t["url"].(string); ok && u != "" {
				p, _ := t["path"].(string)
				out = append(out, docURLRef{url: u, path: p})
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		case []any:
			for _, vv := range t {
				walk(vv)
			}
		}
	}
	walk(prof["docs"])
	sort.Slice(out, func(i, j int) bool {
		if out[i].url != out[j].url {
			return out[i].url < out[j].url
		}
		return out[i].path < out[j].path
	})
	return out
}

// checkDocURLs resolves a bounded, deterministic sample of published doc
// URLs and reports dead links. It is pure-ish: all network goes through
// client, so a test injects an httptest client and never touches the real
// network.
//
// The emission obeys the honesty rail. A URL we cannot reach is a network
// condition, never a finding. A reachable non-2xx is a dead link — but the
// *cause* is only asserted when it is observable: when sibling URLs on the
// same site resolve, a dead one is a per-link slug mismatch; when EVERY
// reachable URL is dead, the cause is the publication base (repo not public,
// wrong base URL, template bug), so it collapses to one finding rather than
// flooding the ledger with N identical, miscaused per-link claims.
func checkDocURLs(rep *Report, rd *scanner.ReportData, profByID map[string]map[string]any, _ Options, client *http.Client) {
	refs := collectDocURLs(rd, profByID)
	if len(refs) == 0 {
		return // no published URLs to resolve — nothing to claim
	}
	if distinct := len(refs); distinct > maxDocURLs {
		refs = refs[:maxDocURLs]
		rep.Caveats = append(rep.Caveats, fmt.Sprintf(
			"Doc-URL check resolved the first %d of %d distinct published URLs; the remaining %d were not checked (bounded to keep the run fast).",
			maxDocURLs, distinct, distinct-maxDocURLs))
	}

	ctx, cancel := context.WithTimeout(context.Background(), docURLGlobalBudget)
	defer cancel()

	checked, unreachable := 0, 0
	var dead []docURLRef
	for _, r := range refs {
		if ctx.Err() != nil {
			break // global budget spent — stop rather than hammer
		}
		status, reachable := resolveDocURL(ctx, client, r.url)
		if !reachable {
			unreachable++
			continue // one unreachable URL → skip silently, never a finding
		}
		checked++
		if status < 200 || status >= 300 {
			r.status = status
			dead = append(dead, r)
		}
	}

	// Honesty: if nothing was reachable, that is a network condition, not a
	// claim about the links. One caveat, no findings.
	if checked == 0 {
		if unreachable > 0 {
			rep.Caveats = append(rep.Caveats,
				"Doc-URL check could not reach the network (all sampled URLs timed out or were unreachable); published URLs are unverified.")
		}
		return
	}
	if len(dead) == 0 {
		return // every reachable URL is live
	}

	// No URL resolved → there is no working sibling to prove a slug bug.
	// Attribute the failure to the publication base, not to N slug
	// mismatches, and collapse to a single finding. (With a tiny sample the
	// cause is genuinely ambiguous, so the wording stays softer.)
	if len(dead) == checked {
		title := fmt.Sprintf("Published doc URL does not resolve — HTTP %d", dead[0].status)
		detail := "The sampled published doc URL(s) did not return 2xx, and no sampled URL resolved. This may be a dead link (a slug/underscore template mismatch) or a base/publication issue (the site isn't public yet, or the base URL is wrong). Verify the base and the URL template before treating it as an individual dead link."
		if checked >= systemicMinSample {
			title = fmt.Sprintf("None of the %d sampled published doc URLs resolve (e.g. HTTP %d) — a base/publication issue, not per-link", checked, dead[0].status)
			detail = "Every sampled published doc URL returned a non-2xx status. When all of them fail at once, the cause is almost always the publication base — the repository or site is not public yet, the base URL is wrong, or a path-template bug breaks every link — rather than N independent slug mismatches. Confirm the base resolves before treating these as individual dead links."
		}
		rep.add(Finding{
			Category: CatDocURL, Severity: SeverityWarn, Surface: "docs.reference",
			Title:    title,
			Detail:   detail,
			Actual:   fmt.Sprintf("HTTP %d", dead[0].status),
			Evidence: docURLEvidence(dead, 5),
			Fix:      "Verify the published base resolves (the repo/site is live and the URL template's base + path scheme are correct), then re-run --check-urls.",
		})
		return
	}

	// Mixed: the base demonstrably works for the live URLs, so each dead URL
	// is a genuine per-link mismatch. Emit per-link findings, bounded.
	shown := dead
	if len(shown) > maxDocURLFindings {
		shown = shown[:maxDocURLFindings]
		rep.Caveats = append(rep.Caveats, fmt.Sprintf(
			"Doc-URL check found %d dead links among resolving siblings; the first %d are listed as findings.",
			len(dead), maxDocURLFindings))
	}
	for _, r := range shown {
		ev := []string{r.url}
		if r.path != "" {
			ev = append(ev, "source: "+r.path)
		}
		rep.add(Finding{
			Category: CatDocURL, Severity: SeverityWarn,
			Element: r.element, Surface: "docs.reference",
			Title:    fmt.Sprintf("Published doc URL does not resolve — HTTP %d", r.status),
			Detail:   "The published doc URL returned a non-2xx status while sibling URLs on the same site resolve: a slug/underscore convention mismatch in the URL template most likely produced this dead link. The attribution join may be correct, but the link a reviewer clicks is broken.",
			Actual:   fmt.Sprintf("HTTP %d", r.status),
			Evidence: ev,
			Fix:      "Reconcile the doc-URL template with the published site's slug convention (underscores vs hyphens, trailing slash, anchor casing); the link must resolve before the report is shared.",
		})
	}
}

// docURLEvidence renders up to n dead URLs as evidence lines, with an
// overflow marker.
func docURLEvidence(refs []docURLRef, n int) []string {
	var ev []string
	for i, r := range refs {
		if i >= n {
			ev = append(ev, fmt.Sprintf("…and %d more", len(refs)-n))
			break
		}
		ev = append(ev, fmt.Sprintf("HTTP %d — %s", r.status, r.url))
	}
	return ev
}

// resolveDocURL resolves one URL: HEAD first, falling back to a ranged GET
// when the server rejects HEAD (405/501) or HEAD fails at the transport.
// Returns (statusCode, true) when an HTTP response was obtained, or
// (0, false) when the URL could not be reached at all.
func resolveDocURL(ctx context.Context, client *http.Client, rawURL string) (int, bool) {
	if req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil); err == nil {
		if resp, err := client.Do(req); err == nil {
			status := resp.StatusCode
			_ = resp.Body.Close()
			if status != http.StatusMethodNotAllowed && status != http.StatusNotImplemented {
				return status, true
			}
		}
	}
	// Fall back to a ranged GET — many static hosts/CDNs reject HEAD.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	_ = resp.Body.Close()
	return resp.StatusCode, true
}
