package conceptdocs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/grounding"
)

// repoRoot walks up from this package (internal/report/conceptdocs) to the
// directory containing go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod: %v", dir, err)
	}
	return dir
}

func loadSample(t *testing.T) *grounding.Report {
	t.Helper()
	p := filepath.Join(repoRoot(t), "docs/grounding/samples/drivers.grounding.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	var rep grounding.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("unmarshal sample: %v", err)
	}
	return &rep
}

func chip(v *View, b Bucket) Chip {
	for _, c := range v.Chips {
		if c.Bucket == b {
			return c
		}
	}
	return Chip{}
}

func region(c Chip, kind string) int {
	for _, r := range c.Regions {
		if r.Kind == kind {
			return r.N
		}
	}
	return -1
}

// The fuchsia.driver.framework sample is the canonical instance: 67 elements,
// 10 grounded / 1 guessing / 56 not_mentioned (Types 48, Methods 8 silent).
func TestBuildView_FuchsiaCounts(t *testing.T) {
	v := BuildView(loadSample(t))

	if v.Total != 67 {
		t.Errorf("Total = %d, want 67", v.Total)
	}
	if got := chip(v, BucketClear).Count; got != 10 {
		t.Errorf("Clear count = %d, want 10", got)
	}
	if got := chip(v, BucketAmbiguous).Count; got != 1 {
		t.Errorf("Ambiguous count = %d, want 1", got)
	}
	if got := chip(v, BucketSilent).Count; got != 56 {
		t.Errorf("Silent count = %d, want 56", got)
	}

	clear := chip(v, BucketClear)
	if got := region(clear, "TYPE"); got != 6 {
		t.Errorf("Clear TYPE region = %d, want 6", got)
	}
	if got := region(clear, "PROTOCOL"); got != 4 {
		t.Errorf("Clear PROTOCOL region = %d, want 4", got)
	}
	// Zero-count regions are omitted, so each chip's breakdown reflects only its
	// own items: Clear has no methods, so the METHOD row is absent (-1) and the
	// chip lists exactly its two non-zero kinds.
	if got := region(clear, "METHOD"); got != -1 {
		t.Errorf("Clear METHOD region = %d, want -1 (zero-count regions omitted)", got)
	}
	if got := len(clear.Regions); got != 2 {
		t.Errorf("Clear regions = %d, want 2 (TYPE, PROTOCOL only)", got)
	}
	// Regions sort high->low: Clear should lead with TYPE (6) then PROTOCOL (4).
	if clear.Regions[0].Kind != "TYPE" || clear.Regions[1].Kind != "PROTOCOL" {
		t.Errorf("Clear regions not sorted desc: %+v", clear.Regions)
	}
}

func TestBuildView_SilentSet(t *testing.T) {
	v := BuildView(loadSample(t))
	if len(v.SilentSet) != 2 {
		t.Fatalf("SilentSet groups = %d, want 2 (Types, Methods); got %+v", len(v.SilentSet), v.SilentSet)
	}
	// Sorted desc by count: Types (48) then Methods (8).
	if v.SilentSet[0].Kind != "TYPE" || v.SilentSet[0].Count != 48 {
		t.Errorf("SilentSet[0] = %s/%d, want TYPE/48", v.SilentSet[0].Kind, v.SilentSet[0].Count)
	}
	if v.SilentSet[1].Kind != "METHOD" || v.SilentSet[1].Count != 8 {
		t.Errorf("SilentSet[1] = %s/%d, want METHOD/8", v.SilentSet[1].Kind, v.SilentSet[1].Count)
	}
	if v.SilentSet[0].Label != "Types" || v.SilentSet[1].Label != "Methods" {
		t.Errorf("kind labels = %q/%q, want Types/Methods", v.SilentSet[0].Label, v.SilentSet[1].Label)
	}
	if len(v.SilentSet[0].Items) != 48 {
		t.Errorf("Types silent items = %d, want 48", len(v.SilentSet[0].Items))
	}
}

func TestBuildView_DocCards(t *testing.T) {
	v := BuildView(loadSample(t))

	find := func(substr string) (DocCard, bool) {
		for _, d := range v.Docs {
			if strings.Contains(d.Path, substr) {
				return d, true
			}
		}
		return DocCard{}, false
	}

	// driver_framework.md names Node + NodeController via backticked FQNs at
	// 118/119 — clear cites that resolve to those elements.
	df, ok := find("driver_framework.md")
	if !ok {
		t.Fatalf("no card for driver_framework.md; docs: %v", paths(v.Docs))
	}
	if df.Title != "Driver framework" {
		t.Errorf("driver_framework title = %q, want %q", df.Title, "Driver framework")
	}
	var clearResolved []string
	for _, c := range allCites(df) {
		if c.Bucket == BucketClear {
			if c.Verb != "resolves" {
				t.Errorf("clear cite verb = %q, want resolves", c.Verb)
			}
			for _, e := range c.Resolves {
				clearResolved = append(clearResolved, e.Label)
			}
		}
	}
	if !containsSuffix(clearResolved, "Node") || !containsSuffix(clearResolved, "NodeController") {
		t.Errorf("driver_framework clear cites resolved %v, want Node + NodeController", clearResolved)
	}

	// driver_communication.md has the one ambiguous reference: "values" ->
	// NodePropertyValue (a guessing finding), shown with candidates.
	dc, ok := find("driver_communication.md")
	if !ok {
		t.Fatalf("no card for driver_communication.md; docs: %v", paths(v.Docs))
	}
	var ambFound bool
	for _, c := range allCites(dc) {
		if c.Bucket == BucketAmbiguous {
			ambFound = true
			if !strings.Contains(c.Verb, "matches") {
				t.Errorf("ambiguous cite verb = %q, want a \"...\" matches form", c.Verb)
			}
			if len(c.Resolves) == 0 {
				t.Errorf("ambiguous cite has no candidates")
			}
		}
	}
	if !ambFound {
		t.Errorf("driver_communication.md has no ambiguous cite")
	}
}

// The token highlight must be reconstructable: for clear cites with a token,
// Pre+Token+Post equals the original excerpt and Token is non-empty.
func TestBuildView_ExcerptHighlightRoundTrips(t *testing.T) {
	v := BuildView(loadSample(t))
	checked := 0
	for _, d := range v.Docs {
		for _, c := range allCites(d) {
			if c.Token == "" {
				continue
			}
			checked++
			if c.Pre+c.Token+c.Post == "" {
				t.Errorf("empty reconstructed excerpt in %s:%d", d.Path, c.Line)
			}
		}
	}
	if checked == 0 {
		t.Errorf("no cites with a highlighted token — span/fallback split is broken")
	}
}

func TestBuildView_Deterministic(t *testing.T) {
	rep := loadSample(t)
	if !reflect.DeepEqual(BuildView(rep), BuildView(rep)) {
		t.Errorf("BuildView is not deterministic for identical input")
	}
}

func TestBuildView_Nil(t *testing.T) {
	if v := BuildView(nil); v == nil {
		t.Errorf("BuildView(nil) returned nil; want empty View")
	}
}

func paths(ds []DocCard) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Path)
	}
	return out
}

func containsSuffix(ss []string, suffix string) bool {
	for _, s := range ss {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

func allCites(d DocCard) []Cite {
	out := make([]Cite, 0, len(d.Cites)+len(d.More))
	out = append(out, d.Cites...)
	out = append(out, d.More...)
	return out
}

// A doc that names the contract many times is capped: the card shows at most
// citeCapPerDoc cites and folds the rest into More (the "show all" expander).
// driver_framework.md cites the contract well over the cap.
func TestBuildView_CapsLongDoc(t *testing.T) {
	v := BuildView(loadSample(t))
	var df DocCard
	for _, d := range v.Docs {
		if strings.Contains(d.Path, "driver_framework.md") {
			df = d
		}
	}
	if df.Path == "" {
		t.Fatal("no driver_framework.md card")
	}
	if len(df.Cites) > citeCapPerDoc {
		t.Errorf("shown cites = %d, want <= cap %d", len(df.Cites), citeCapPerDoc)
	}
	if len(df.More) == 0 {
		t.Errorf("driver_framework.md should overflow into More (it has many refs)")
	}
	if df.ID == "" {
		t.Errorf("doc card needs a slug ID for the expander")
	}
}

// A single-library report keeps the kind axis (regression).
func TestBuildView_SingleLibraryUsesKindAxis(t *testing.T) {
	if v := BuildView(loadSample(t)); v.RegionNoun != "kind" {
		t.Errorf("RegionNoun = %q, want kind for a single-library report", v.RegionNoun)
	}
}

// WithSourceURLTemplate linkifies each doc card's path (line 1, since the path
// is file-level), but a synthesized sheaf-ffx-gen/ doc — which has no real
// source location — stays plain so its link can't 404. Without the option, all
// hrefs stay empty (the golden byte-stability guarantee).
func TestBuildView_LinkifiesDocPaths(t *testing.T) {
	sp := func(s string) *string { return &s }
	rep := &grounding.Report{
		Library: "ffx",
		Summary: grounding.Summary{ElementsTotal: 1, ElementsGuessing: 1},
		Elements: []grounding.Element{
			{ElementID: "ffx/Foo", Display: "Foo", Kind: "COMMAND", State: grounding.StateGuessing},
		},
		Findings: []grounding.Finding{
			{
				ID: "f1", ElementID: "ffx/Foo", State: grounding.StateGuessing,
				SourcePath: "src/developer/ffx/docs/analytics.md", Line: 12, Token: "foo",
				Candidates: []grounding.Candidate{{ElementID: sp("ffx/Foo"), Label: "Foo", Kind: "contract", IsContract: true}},
			},
			{
				ID: "f2", ElementID: "ffx/Foo", State: grounding.StateGuessing,
				SourcePath: "sheaf-ffx-gen/ffx-golden-examples.md", Line: 3, Token: "foo",
				Candidates: []grounding.Candidate{{ElementID: sp("ffx/Foo"), Label: "Foo", Kind: "contract", IsContract: true}},
			},
		},
	}
	tmpl := "https://cs.opensource.google/fuchsia/fuchsia/+/main:{path};l={line}"

	hrefByPath := func(v *View) map[string]string {
		m := map[string]string{}
		for _, d := range v.Docs {
			m[d.Path] = d.Href
		}
		return m
	}

	// With the template: the real doc links to line 1; the synthesized doc stays plain.
	got := hrefByPath(BuildView(rep, WithSourceURLTemplate(tmpl)))
	wantReal := "https://cs.opensource.google/fuchsia/fuchsia/+/main:src/developer/ffx/docs/analytics.md;l=1"
	if got["src/developer/ffx/docs/analytics.md"] != wantReal {
		t.Errorf("real doc Href = %q, want %q", got["src/developer/ffx/docs/analytics.md"], wantReal)
	}
	if h := got["sheaf-ffx-gen/ffx-golden-examples.md"]; h != "" {
		t.Errorf("synthesized sheaf-ffx-gen doc should stay plain, got Href = %q", h)
	}

	// Without the template: every Href empty (bare paths — golden stays byte-identical).
	for p, h := range hrefByPath(BuildView(rep)) {
		if h != "" {
			t.Errorf("no template should leave Href empty, but %s has %q", p, h)
		}
	}
}

// A view merged across libraries switches the region axis to library.
func TestBuildViewAll_MultiLibraryRegions(t *testing.T) {
	mk := func(lib string, grounded, silent int) *grounding.Report {
		rep := &grounding.Report{Library: lib}
		add := func(state grounding.State, n int, base rune) {
			for i := 0; i < n; i++ {
				s := "T" + string(base+rune(i))
				rep.Elements = append(rep.Elements, grounding.Element{
					ElementID: lib + "/" + s, Display: s, Kind: "TYPE", State: state,
				})
			}
		}
		add(grounding.StateGrounded, grounded, 'A')
		add(grounding.StateNotMentioned, silent, 'a')
		rep.Summary.ElementsTotal = grounded + silent
		rep.Summary.ElementsGrounded = grounded
		rep.Summary.ElementsNotMentioned = silent
		return rep
	}
	v := BuildViewAll([]*grounding.Report{mk("fuchsia.io", 2, 3), mk("fuchsia.ui.gfx", 1, 4)}, "fuchsia.*")

	if v.RegionNoun != "library" {
		t.Errorf("RegionNoun = %q, want library", v.RegionNoun)
	}
	if v.Total != 10 {
		t.Errorf("Total = %d, want 10", v.Total)
	}
	clear := v.Chips[0]
	if clear.Bucket != BucketClear || clear.Count != 3 {
		t.Errorf("Clear chip = %v/%d, want clear/3", clear.Bucket, clear.Count)
	}
	gotLibs := map[string]int{}
	for _, r := range clear.Regions {
		gotLibs[r.Kind] = r.N // Kind holds the region key (a library, here)
	}
	if gotLibs["fuchsia.io"] != 2 || gotLibs["fuchsia.ui.gfx"] != 1 {
		t.Errorf("clear regions by library = %v, want io:2 gfx:1", gotLibs)
	}
	if len(v.SilentSet) != 2 {
		t.Errorf("silent groups = %d, want 2 (one per library)", len(v.SilentSet))
	}
}
