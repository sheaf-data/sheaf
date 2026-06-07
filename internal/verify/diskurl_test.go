package verify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// --- helpers ---------------------------------------------------------------

// docURLProfile builds a profile whose docs.reference surface carries one
// reference ref with the given published url — the shape `sheaf snapshot`
// emits (docs.reference.byAdapter.<adapter>.refs[].url).
func docURLProfile(url, path string) map[string]any {
	return map[string]any{
		"docs": map[string]any{
			"reference": map[string]any{
				"byAdapter": map[string]any{
					"markdowncli": map[string]any{
						"refs": []any{
							map[string]any{"url": url, "path": path, "line": float64(1)},
						},
					},
				},
			},
		},
	}
}

type elemURL struct{ elem, url string }

// runDocURLCheckN drives checkDocURLs over several elements, each with one
// published URL, preserving the given order (so sampling is deterministic).
func runDocURLCheckN(refs []elemURL, client *http.Client) *Report {
	rep := &Report{}
	methods := make([]scanner.MethodRow, 0, len(refs))
	profByID := map[string]map[string]any{}
	for _, r := range refs {
		methods = append(methods, scanner.MethodRow{Name: r.elem})
		profByID[r.elem] = docURLProfile(r.url, "doc.md")
	}
	rd := &scanner.ReportData{Methods: methods}
	checkDocURLs(rep, rd, profByID, Options{}, client)
	return rep
}

func runDocURLCheck(elem, url string, client *http.Client) *Report {
	return runDocURLCheckN([]elemURL{{elem, url}}, client)
}

func findingsWith(r *Report, c Category) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Category == c {
			out = append(out, f)
		}
	}
	return out
}

func evidenceHas(f Finding, sub string) bool {
	for _, e := range f.Evidence {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func caveatsHave(r *Report, sub string) bool {
	for _, c := range r.Caveats {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// --- doc-URL resolution ----------------------------------------------------

// A reachable URL that returns 404 is provable wrongness — a dead link → a
// doc_url warning citing the URL, and NOT a network caveat.
func TestCheckDocURLs_DeadLinkFlagged(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer ts.Close()

	rep := runDocURLCheck("elem-dead", ts.URL+"/docs/missing/", ts.Client())

	fs := findingsWith(rep, CatDocURL)
	if len(fs) != 1 {
		t.Fatalf("want exactly 1 doc_url finding for a 404, got %d (%+v)", len(fs), rep.Findings)
	}
	if fs[0].Severity != SeverityWarn {
		t.Errorf("doc_url must be a warning (honesty rail), got %q", fs[0].Severity)
	}
	if !evidenceHas(fs[0], ts.URL+"/docs/missing/") {
		t.Errorf("finding must cite the dead URL in evidence, got %v", fs[0].Evidence)
	}
	if len(rep.Caveats) != 0 {
		t.Errorf("a reachable 404 is provable, not a network caveat: %v", rep.Caveats)
	}
}

// A reachable 200 is a live link — no finding, no caveat.
func TestCheckDocURLs_LiveLinkNoFinding(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	rep := runDocURLCheck("elem-ok", ts.URL+"/docs/coverage/", ts.Client())

	if n := len(findingsWith(rep, CatDocURL)); n != 0 {
		t.Fatalf("a 200 link must not be flagged, got %d findings (%+v)", n, rep.Findings)
	}
	if len(rep.Caveats) != 0 {
		t.Errorf("a reachable 200 must not add caveats: %v", rep.Caveats)
	}
}

// Many static hosts/CDNs reject HEAD with 405; the check must fall back to a
// ranged GET and treat a GET-200 as a live link.
func TestCheckDocURLs_HeadRejectedFallsBackToGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	rep := runDocURLCheck("elem-headless", ts.URL+"/docs/x/", ts.Client())

	if n := len(findingsWith(rep, CatDocURL)); n != 0 {
		t.Fatalf("HEAD-405 + GET-200 is a live link via fallback, got %d findings (%+v)", n, rep.Findings)
	}
}

// An unreachable URL (server closed → connection refused) is a network
// condition, never a finding; when EVERY sampled URL is unreachable the
// report carries a single honest caveat instead.
func TestCheckDocURLs_UnreachableYieldsCaveatNotFinding(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL + "/docs/x/"
	ts.Close() // henceforth refuses connections

	rep := runDocURLCheck("elem-unreach", url, &http.Client{Timeout: 2 * time.Second})

	if n := len(findingsWith(rep, CatDocURL)); n != 0 {
		t.Fatalf("an unreachable URL must never be a finding, got %d (%+v)", n, rep.Findings)
	}
	if !caveatsHave(rep, "could not reach the network") {
		t.Fatalf("all-unreachable must add a single network caveat, got %v", rep.Caveats)
	}
}

// When EVERY reachable URL is dead, there is no resolving sibling to prove a
// per-link slug bug — the cause is the publication base. The check must
// collapse to ONE finding, not flood the ledger with N identical claims, and
// must not assert a slug mismatch it cannot observe.
func TestCheckDocURLs_AllDeadCollapsesToOneFinding(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer ts.Close()

	rep := runDocURLCheckN([]elemURL{
		{"e1", ts.URL + "/a/"},
		{"e2", ts.URL + "/b/"},
		{"e3", ts.URL + "/c/"},
		{"e4", ts.URL + "/d/"},
	}, ts.Client())

	fs := findingsWith(rep, CatDocURL)
	if len(fs) != 1 {
		t.Fatalf("all-dead must collapse to exactly 1 finding, got %d (%+v)", len(fs), rep.Findings)
	}
	if !strings.Contains(fs[0].Title, "base/publication") {
		t.Errorf("all-dead (n>=3) finding should name the base/publication cause, got title %q", fs[0].Title)
	}
	if strings.Contains(fs[0].Detail, "slug/underscore convention mismatch in the URL template most likely") {
		t.Errorf("must NOT assert a per-link slug mismatch when no sibling resolves: %q", fs[0].Detail)
	}
}

// When some siblings resolve, a dead URL is a genuine per-link mismatch —
// emit one finding per dead URL, citing only the dead ones.
func TestCheckDocURLs_MixedFlagsPerLink(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bad") {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	rep := runDocURLCheckN([]elemURL{
		{"ok1", ts.URL + "/ok/one/"},
		{"bad1", ts.URL + "/bad/one/"},
		{"ok2", ts.URL + "/ok/two/"},
		{"bad2", ts.URL + "/bad/two/"},
	}, ts.Client())

	fs := findingsWith(rep, CatDocURL)
	if len(fs) != 2 {
		t.Fatalf("mixed result must flag the 2 dead links per-link, got %d (%+v)", len(fs), rep.Findings)
	}
	for _, f := range fs {
		if !evidenceHas(f, "/bad/") {
			t.Errorf("per-link finding must cite a dead (/bad/) URL, got %v", f.Evidence)
		}
		if evidenceHas(f, "/ok/") {
			t.Errorf("a live (/ok/) URL must not be flagged, got %v", f.Evidence)
		}
	}
}
