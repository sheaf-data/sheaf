package scanner

import (
	"strings"
	"testing"
)

func TestBuildReport_SuppressPrimaryTier_SingleBinary(t *testing.T) {
	snap := &Snapshot{
		Library: "fd",
		Elements: []map[string]any{
			{"id": "fd", "kind": "SUBCOMMAND"},
			{"id": "fd --hidden", "kind": "FLAG"},
			{"id": "fd --no-ignore", "kind": "FLAG"},
		},
		Profiles:         []map[string]any{},
		SurfacesRequired: []string{"docs.concepts", "tests"},
	}
	r := BuildReport(snap, "cli", "now", "HEAD")
	if !r.SuppressPrimaryTier {
		t.Errorf("expected SuppressPrimaryTier=true; PrimaryTotal=%d, singlePrimary should equal library", r.PrimaryTotal)
	}
	if r.BinaryRoot != "fd" {
		t.Errorf("BinaryRoot = %q, want %q", r.BinaryRoot, "fd")
	}
}

func TestBuildReport_SuppressPrimary_SwitchesNounAndSubstance(t *testing.T) {
	// fd-shaped: 1 SUBCOMMAND root + multiple flags. Surfaces
	// required = {concepts, tests}. The substance bar should
	// measure flag-tier docs (modifier substance), not the
	// always-zero primary-tier; the canonical noun should
	// switch to "flag" so all downstream sentences (popups,
	// hero, substance synopsis) read against the same denominator.
	snap := &Snapshot{
		Library: "fd",
		Elements: []map[string]any{
			{"id": "fd", "kind": "SUBCOMMAND"},
			{"id": "fd --a", "kind": "FLAG"},
			{"id": "fd --b", "kind": "FLAG"},
		},
		Profiles: []map[string]any{
			{"elementId": "fd --a", "docs": map[string]any{
				"concept": []any{map[string]any{"substance": "SUBSTANTIVE"}},
			}, "tests": map[string]any{
				"unit": []any{map[string]any{"path": "t.rs"}},
			}},
			{"elementId": "fd --b", "docs": map[string]any{
				"concept": []any{map[string]any{"substance": "SUBSTANTIVE"}},
			}},
		},
		SurfacesRequired: []string{"docs.concepts", "tests"},
	}
	r := BuildReport(snap, "cli", "now", "HEAD")
	if !r.SuppressPrimaryTier {
		t.Fatalf("expected SuppressPrimaryTier=true")
	}
	if r.NounPlural != "flags" {
		t.Errorf("NounPlural = %q, want \"flags\"", r.NounPlural)
	}
	if r.NounSingular != "flag" {
		t.Errorf("NounSingular = %q, want \"flag\"", r.NounSingular)
	}
	// Substance counts should come from the modifier tier (2 flags),
	// not the primary tier (1 binary with no docs).
	if r.SubstanceTotal != 2 {
		t.Errorf("SubstanceTotal = %d, want 2 (modifier tier)", r.SubstanceTotal)
	}
	if r.SubstanceCounts["SUBSTANTIVE"] != 2 {
		t.Errorf("SUBSTANTIVE count = %d, want 2", r.SubstanceCounts["SUBSTANTIVE"])
	}
}

func TestComboFixFor_SurfaceAware(t *testing.T) {
	// With examples not required, c-and-t should be bridged and
	// the "noexample" key should never fire.
	twoSurface := []string{"docs.concepts", "tests"}
	key, _, _ := comboFixFor(true, true, false, twoSurface)
	if key != "bridged" {
		t.Errorf("c+t (no examples required) should be bridged; got %q", key)
	}
	key, _, _ = comboFixFor(true, true, true, twoSurface)
	if key != "bridged" {
		t.Errorf("c+t+e (no examples required) should still be bridged; got %q", key)
	}
	key, _, _ = comboFixFor(true, false, false, twoSurface)
	if key != "untested" {
		t.Errorf("c only (no examples required) should be untested; got %q", key)
	}
	// Fallback (empty surfaces) preserves v0 three-surface behavior.
	key, _, _ = comboFixFor(true, true, false, nil)
	if key != "noexample" {
		t.Errorf("c+t (fallback) should be noexample; got %q", key)
	}
}

func TestBuildReport_KeepsPrimaryTier_MultiSubcommand(t *testing.T) {
	snap := &Snapshot{
		Library: "kubectl",
		Elements: []map[string]any{
			{"id": "kubectl", "kind": "SUBCOMMAND"},
			{"id": "kubectl get", "kind": "SUBCOMMAND"},
			{"id": "kubectl apply", "kind": "SUBCOMMAND"},
			{"id": "kubectl get -o", "kind": "FLAG"},
		},
		Profiles: []map[string]any{},
	}
	r := BuildReport(snap, "cli", "now", "HEAD")
	if r.SuppressPrimaryTier {
		t.Errorf("multi-subcommand project should keep Primary tier; got SuppressPrimaryTier=true")
	}
	if r.BinaryRoot != "" {
		t.Errorf("BinaryRoot should be empty when tier shown; got %q", r.BinaryRoot)
	}
}

func TestBridgedFromSurfaces(t *testing.T) {
	cases := []struct {
		name             string
		row              MethodRow
		surfacesRequired []string
		want             bool
	}{
		{
			name:             "empty surfaces → fallback v0 (all three required)",
			row:              MethodRow{Concept: 1, Test: 1, Example: 0},
			surfacesRequired: nil,
			want:             false,
		},
		{
			name:             "empty surfaces, full coverage → bridged",
			row:              MethodRow{Concept: 1, Test: 1, Example: 1},
			surfacesRequired: nil,
			want:             true,
		},
		{
			name:             "concept+tests required, example missing → bridged",
			row:              MethodRow{Concept: 1, Test: 1, Example: 0},
			surfacesRequired: []string{"docs.concepts", "tests"},
			want:             true,
		},
		{
			name:             "concept+tests required, test missing → not bridged",
			row:              MethodRow{Concept: 1, Test: 0, Example: 99},
			surfacesRequired: []string{"docs.concepts", "tests"},
			want:             false,
		},
		{
			name:             "concept+tests required, concept missing → not bridged",
			row:              MethodRow{Concept: 0, Test: 1, Example: 99},
			surfacesRequired: []string{"docs.concepts", "tests"},
			want:             false,
		},
		{
			name:             "case-insensitive alias 'concepts' matches",
			row:              MethodRow{Concept: 1, Test: 1, Example: 0},
			surfacesRequired: []string{"concepts", "tests"},
			want:             true,
		},
		{
			name:             "all four surfaces required, examples missing → not bridged",
			row:              MethodRow{Concept: 1, Test: 1, Example: 0},
			surfacesRequired: []string{"docs.concepts", "tests", "examples"},
			want:             false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bridgedFromSurfaces(c.row, c.surfacesRequired)
			if got != c.want {
				t.Errorf("bridgedFromSurfaces(%+v, %v) = %v, want %v",
					c.row, c.surfacesRequired, got, c.want)
			}
		})
	}
}

func TestIsRemovedAt(t *testing.T) {
	cases := []struct {
		name     string
		removed  string
		target   string
		expected bool
	}{
		// No removed marker → never removed.
		{"no marker", "", "HEAD", false},
		{"no marker numeric", "", "27", false},
		// HEAD target — the dev tip.
		{"HEAD target, numeric removed", "29", "HEAD", true},
		{"HEAD target, HEAD removed", "HEAD", "HEAD", true},
		{"HEAD target, NEXT removed", "NEXT", "HEAD", false}, // NEXT is ahead of HEAD
		{"empty target defaults to HEAD", "29", "", true},
		// Numeric target.
		{"num27 vs removed29", "29", "27", false}, // 29 > 27, not yet removed
		{"num27 vs removed27", "27", "27", true},  // removed AT this level
		{"num30 vs removed27", "27", "30", true},  // long past
		{"num27 vs HEAD removed", "HEAD", "27", false},
		{"num27 vs NEXT removed", "NEXT", "27", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &Deprecation{RemovedIn: c.removed}
			got := d.IsRemovedAt(c.target)
			if got != c.expected {
				t.Errorf("IsRemovedAt(removed=%q, target=%q) = %v; want %v",
					c.removed, c.target, got, c.expected)
			}
		})
	}
}

func TestIsRemovedAt_NilSafe(t *testing.T) {
	var d *Deprecation
	if d.IsRemovedAt("HEAD") {
		t.Error("nil Deprecation should never be removed")
	}
}

// parseAPILevel moved to internal/versionscheme as parseFIDLLevel.
// Its tests live there alongside the FIDL scheme implementation.

// TestBuildReport_FIDLTiers exercises the EcosystemView migration.
// The FIDL view declares three tiers (Protocols, Methods, Types —
// the last hidden from the header), and BuildReport should populate
// counts for each from the element walk.
func TestBuildReport_FIDLTiers(t *testing.T) {
	snap := &Snapshot{
		Library: "tiers.demo",
		Elements: []map[string]any{
			{"id": "tiers.demo/SvcA", "kind": "PROTOCOL", "library": "tiers.demo"},
			{"id": "tiers.demo/SvcA.Open", "kind": "METHOD", "library": "tiers.demo"},
			{"id": "tiers.demo/SvcA.Close", "kind": "METHOD", "library": "tiers.demo"},
			{"id": "tiers.demo/SvcB", "kind": "PROTOCOL", "library": "tiers.demo"},
			{"id": "tiers.demo/SvcB.Ping", "kind": "METHOD", "library": "tiers.demo"},
			{"id": "tiers.demo/SomeType", "kind": "TYPE", "library": "tiers.demo"},
		},
	}
	r := BuildReport(snap, "fidl", "2026-05-25 12:00 UTC", "HEAD")

	if got := len(r.Tiers); got != 3 {
		t.Fatalf("Tiers length = %d; want 3 (Protocols, Methods, Types)", got)
	}
	wantTiers := []struct {
		ID    string
		Label string
		Count int
		Show  bool
	}{
		{"container", "Protocols", 2, true},
		{"primary", "Methods", 3, true},
		{"type", "Types", 1, false},
	}
	for i, w := range wantTiers {
		got := r.Tiers[i]
		if got.ID != w.ID || got.Label != w.Label || got.Count != w.Count || got.ShowInHeader != w.Show {
			t.Errorf("Tiers[%d] = {%s,%s,%d,show=%v}; want {%s,%s,%d,show=%v}",
				i, got.ID, got.Label, got.Count, got.ShowInHeader,
				w.ID, w.Label, w.Count, w.Show)
		}
	}

	// HeaderTiers must be the visible-in-header subset of Tiers
	// (Protocols + Methods; Types is hidden). The template ranges over
	// HeaderTiers for the section header.
	if got := len(r.HeaderTiers); got != 2 {
		t.Errorf("HeaderTiers length = %d; want 2 (Protocols, Methods)", got)
	}
	if r.HeaderTiers[0].Label != "Protocols" || r.HeaderTiers[0].Count != 2 {
		t.Errorf("HeaderTiers[0] = {%s, %d}; want {Protocols, 2}", r.HeaderTiers[0].Label, r.HeaderTiers[0].Count)
	}
	if r.HeaderTiers[1].Label != "Methods" || r.HeaderTiers[1].Count != 3 {
		t.Errorf("HeaderTiers[1] = {%s, %d}; want {Methods, 3}", r.HeaderTiers[1].Label, r.HeaderTiers[1].Count)
	}
	// Substance is scoped to primary-detail kinds (METHOD for FIDL).
	// All three method profiles are nil here, so each grades ABSENT —
	// what matters is the *denominator* (SubstanceTotal) equals the
	// METHOD-kind count, since the substance check routes through
	// view.PrimaryDetailKinds() rather than a hardcoded `kind == METHOD`.
	if r.SubstanceTotal != 3 {
		t.Errorf("SubstanceTotal = %d; want 3 (one per METHOD-kind element)", r.SubstanceTotal)
	}
}

// TestEmptyFindings_AnalyzerDisambiguation was removed when the
// Findings section's empty-state messages ("All N configured
// analyzers ran clean" / "No analyzers are configured") were folded
// away during the multi-ecosystem redesign (commit 27f4738). The
// worklist + UpSet rows now carry the analyzer-disambiguation work
// implicitly. If the empty-state language returns, restore this test
// alongside.

// TestNumWord locks the headline/italic-subheadline "Zero" substitution
// behavior. Counts of zero render as the spelled-out word so they read
// as a deliberate finding rather than a missing number; non-zero
// counts render as bare digits.
func TestNumWord(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "Zero"},
		{1, "1"},
		{42, "42"},
		{1000, "1000"},
	}
	for _, c := range cases {
		if got := numWord(c.n); got != c.want {
			t.Errorf("numWord(%d) = %q; want %q", c.n, got, c.want)
		}
	}
}

// TestCategorizationInsight_ZeroSubstitution makes sure the italic
// "Docs cover N …; tests cover N; usage covers N" sentence uses "Zero"
// in place of 0 — house style for italic subheadlines.
func TestCategorizationInsight_ZeroSubstitution(t *testing.T) {
	r := &ReportData{
		Total:        10,
		ConceptCount: 0,
		TestCount:    5,
		ExampleCount: 0,
		UsageCount:   0,
		NounSingular: "method",
		NounPlural:   "methods",
	}
	got := categorizationInsight(r)
	// Both ConceptCount and UsageCount are 0 → both should render as
	// "Zero" (capitalized) in the sentence.
	for _, want := range []string{"cover Zero methods", "usage covers Zero"} {
		if !strings.Contains(got, want) {
			t.Errorf("categorizationInsight missing %q\n  got: %s", want, got)
		}
	}
	// TestCount is 5 → renders as "5", not "Zero".
	if strings.Contains(got, "tests cover Zero") {
		t.Errorf("categorizationInsight wrongly substituted Zero for tests count 5\n  got: %s", got)
	}
}

// TestBuildReport_CobraTiers exercises the cobra ecosystem view: the
// docker CLI snapshot has SUBCOMMAND + FLAG + SWITCH elements, no
// PROTOCOL/METHOD, and the header should reflect Commands + Flags —
// not the FIDL-shaped "Zero Protocols · Zero Methods" the FIDL view
// would produce. Also locks the noun substitution and the substance
// synopsis denominator (which should be the SUBCOMMAND count, not 0).
func TestBuildReport_CobraTiers(t *testing.T) {
	snap := &Snapshot{
		Library: "demo-cli",
		Elements: []map[string]any{
			{"id": "demo", "kind": "SUBCOMMAND", "library": "demo-cli"},
			{"id": "demo container", "kind": "SUBCOMMAND", "library": "demo-cli"},
			{"id": "demo container run", "kind": "SUBCOMMAND", "library": "demo-cli"},
			{"id": "demo --config", "kind": "FLAG", "library": "demo-cli"},
			{"id": "demo --debug", "kind": "SWITCH", "library": "demo-cli"},
			{"id": "demo container --tty", "kind": "SWITCH", "library": "demo-cli"},
		},
	}
	r := BuildReport(snap, "cli", "2026-05-25 13:00 UTC", "HEAD")

	if r.NounPlural != "commands" {
		t.Errorf("NounPlural = %q; want %q (from cobraView.Noun)", r.NounPlural, "commands")
	}
	// HeaderTiers should be exactly [Commands, Flags] with the right
	// counts — invisible-in-header tiers don't apply here.
	if got := len(r.HeaderTiers); got != 2 {
		t.Fatalf("HeaderTiers length = %d; want 2 (Commands, Flags)", got)
	}
	wantTiers := []struct {
		Label string
		Count int
	}{
		{"Commands", 3}, // 3 SUBCOMMAND elements
		{"Flags", 3},    // 1 FLAG + 2 SWITCH
	}
	for i, w := range wantTiers {
		got := r.HeaderTiers[i]
		if got.Label != w.Label || got.Count != w.Count {
			t.Errorf("HeaderTiers[%d] = {%s,%d}; want {%s,%d}",
				i, got.Label, got.Count, w.Label, w.Count)
		}
	}
	// SubstanceTotal is the SUBCOMMAND count (the primary-detail tier
	// for cobra), not 0 — which is what the FIDL view would have given
	// us before the cobra view was registered.
	if r.SubstanceTotal != 3 {
		t.Errorf("SubstanceTotal = %d; want 3 (one per SUBCOMMAND)", r.SubstanceTotal)
	}
}

// TestCobraView_ContainerOf locks the cobra parent rule — strip the
// last space-separated token. Applies to both subcommands ("docker
// container run" → "docker container") and flags ("docker --debug" →
// "docker"). Root commands return "".
func TestCobraView_ContainerOf(t *testing.T) {
	v := cobraView{}
	cases := []struct {
		id, want string
	}{
		{"docker", ""},                                 // root command
		{"docker container", "docker"},                 // group command
		{"docker container run", "docker container"},   // leaf command
		{"docker --debug", "docker"},                   // root-level switch
		{"docker container --tty", "docker container"}, // group-level switch
	}
	for _, c := range cases {
		got := v.ContainerOf(c.id, nil)
		if got != c.want {
			t.Errorf("ContainerOf(%q) = %q; want %q", c.id, got, c.want)
		}
	}
}

// TestFIDLView_ContainerOf checks the FIDL container-of derivation
// (used in Phase 3 for the per-protocol tree view, but defined and
// tested in Phase 1 so the rule is fixed before any consumer exists).
func TestFIDLView_ContainerOf(t *testing.T) {
	v := fidlView{}
	cases := []struct {
		id   string
		want string
	}{
		{"fuchsia.io/Directory.Open", "fuchsia.io/Directory"},
		{"docker.lib/Subcmd.Run", "docker.lib/Subcmd"},
		{"lib/StandaloneType", ""},   // no dot in local part
		{"BareName", ""},             // no slash at all
		{"WithDot.Local", "WithDot"}, // no slash, last dot strips
	}
	for _, c := range cases {
		got := v.ContainerOf(c.id, nil)
		if got != c.want {
			t.Errorf("ContainerOf(%q) = %q; want %q", c.id, got, c.want)
		}
	}
}
