package scanner

import (
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"unicode"

	"github.com/sheaf-data/sheaf/internal/versionscheme"
)

// ReportData is everything render.go needs. Computed once from a
// Snapshot. All maps and slices use stable orderings so the rendered
// HTML is deterministic for fixed inputs (see report-requirements §8).
type ReportData struct {
	// Nav, when non-nil, renders the run-switcher + home link in the
	// sticky strip (monorepo fan-out). nil for a standalone report.
	Nav *NavContext
	// Lag carries the per-library doc-staleness distribution computed
	// from git commit timestamps over (element source, doc reference)
	// pairs. Zero (Pairs == 0) when no docs or repoRoot is not a git
	// working tree. Pure mechanical; every value decomposes to a commit.
	Lag          LagResult
	Library      string
	NounSingular string
	NounPlural   string
	// TotalNounSingular / TotalNounPlural — umbrella nouns for the
	// .Total element count (sticky strip, hero strip, "Show all N",
	// footer, deprecation banner). Differs from NounSingular/Plural
	// for multi-tier surfaces like cobra ("commands & flags" total
	// vs. "commands" primary-detail). Populated from view.TotalNoun();
	// falls back to Noun when the view's umbrella matches its
	// primary-detail (e.g. FIDL).
	TotalNounSingular string
	TotalNounPlural   string
	Ecosystem         string
	Total             int
	Bridged           int
	SubstantivePct    int

	// ConceptDocsHref, when non-empty, renders a one-line entry-point link
	// from the masthead to the sibling Concept Docs report (the doc-centric
	// clear/ambiguous/silent map). Empty omits the link, so a report without
	// a generated sibling carries no dead link.
	ConceptDocsHref string

	// SurfacesRequired is the project's declared surface set (from
	// sheaf.textproto's surfaces_required field). When non-empty,
	// the bridged predicate computes against exactly these surfaces
	// and the masthead suppresses per-surface tiles for surfaces
	// not in the set. Empty falls back to the v0 three-surface
	// bridged definition (concept + tests + examples).
	SurfacesRequired []string
	// ShowConceptTile / ShowTestTile / ShowExampleTile /
	// ShowImplementationsTile drive the masthead's per-surface tile
	// visibility. A tile renders iff (a) the surface is declared in
	// SurfacesRequired (or SurfacesRequired is empty — fallback
	// mode), AND (b) at least one element kind in the library admits
	// that surface per kindSurfaces. So a FIDL library shows the
	// Implementations tile and hides Tests; a CLI library does the
	// inverse.
	ShowConceptTile         bool
	ShowTestTile            bool
	ShowExampleTile         bool
	ShowImplementationsTile bool
	// TiersSideBySide renders the tier sections as columns instead of
	// stacked, when there are 2-3 tiers and each shows exactly one chip
	// (e.g. a FIDL library reduced to just the Reference-docs chip on its
	// Protocols + Methods tiers).
	TiersSideBySide bool

	// SuppressPrimaryTier is true when the Primary tier collapses
	// to a single root-binary element (PrimaryTotal == 1 and the
	// element's id equals the library name). For single-binary
	// CLIs the Primary tier is structurally a tautology — one
	// element whose surface IS its flags — so we drop its tiles
	// and roll the binary's identity into the Modifier section
	// heading via BinaryRoot. Multi-subcommand CLIs (kubectl, gh)
	// keep both tiers as before.
	SuppressPrimaryTier bool
	// BinaryRoot is the binary name displayed as the identifier in
	// the Modifier section heading when SuppressPrimaryTier is
	// true. Renders as "fd · 44 Flags" (interpunct conjunction),
	// generalizable to any single-root CLI. Empty when not
	// suppressing.
	BinaryRoot string

	// Library-level deprecation (extracted from a LIBRARY-kind element
	// if the FIDL adapter emitted one). Nil = not deprecated.
	LibraryDeprecation    *Deprecation
	DeprecatedCount       int // number of methods marked deprecated
	RemovedCount          int // number of methods already removed at TargetAPILevel
	TotalIncludingRemoved int // raw method count before excluding removed ones

	// Tiers carries the per-ecosystem header tiles in display order.
	// Each tier sums the elements of one or more kinds (PROTOCOL,
	// METHOD, SUBCOMMAND, …). The template ranges over HeaderTiers
	// (below) for the header — that's Tiers filtered to those with
	// ShowInHeader=true. Tiers in full also includes invisible-in-header
	// kinds like TYPE so the back-compat scalars below can still derive
	// their counts.
	//
	// See docs/scanner/multi-ecosystem-report-architecture.md.
	Tiers []Tier

	// HeaderTiers is Tiers filtered to ShowInHeader=true, preserving
	// order. The template uses this for the section header so its
	// "first tile vs subsequent tile" CSS logic doesn't need to count
	// past invisible tiers like TYPE (FIDL) — the iteration index $i
	// directly corresponds to position in the visible header.
	HeaderTiers []Tier

	// GapCount is the number of live methods that aren't fully bridged
	// — the number that show up somewhere in the worklist.
	GapCount       int
	TargetAPILevel string // the API level the report was computed at

	ConceptPct   int
	ConceptCount int
	// ConceptDocPct / ConceptDocCount roll up the NEW additive
	// docs.concepts (narrative concept-doc) surface — the count of live
	// elements with >=1 anchored concept-doc claim, and that as a percent
	// of Total. PARALLEL to ConceptPct / ConceptCount (the `///`-fed
	// reference-doc surface), never a replacement: existing reports that
	// read ConceptCount are unaffected. Zero when no concept-doc source ran
	// for this library.
	ConceptDocPct   int
	ConceptDocCount int
	// Concept-doc REACH partition (the conceptdoc engine's clear/ambiguous/
	// silent classification, rolled up over Total). These feed the report's
	// honest one-line reach NOTE — never a chip, never a percentage:
	//   ConceptDocConfigured — a concept-doc source was configured + scanned
	//     for this scan (snapshot.ConceptDocSource). The reach line renders
	//     ONLY when true, so a repo with no narrative source wired shows
	//     nothing rather than a misleading "0 of N".
	//   ConceptDocClear      — elements the docs reference UNAMBIGUOUSLY
	//     (>=1 degree-1 anchored mention).
	//   ConceptDocAmbiguous  — elements referenced but never singled out
	//     (every mention is a shared, degree>=2 site).
	// Silent (not discussed) is Total - Clear - Ambiguous; it is shown as
	// "the rest", never divided into a percentage.
	ConceptDocConfigured bool
	ConceptDocClear      int
	ConceptDocAmbiguous  int
	// ImplementationsCount counts elements whose CoverageProfile has
	// at least one implementations entry. Non-zero only for libraries
	// containing interface kinds (METHOD / TYPE / PROTOCOL / SYSCALL).
	// Drives the Implementations masthead tile.
	ImplementationsPct   int
	ImplementationsCount int
	// HasImplementsSignal is true when at least one element in the
	// scan carries an IMPLEMENTS edge (i.e., the impl tree was
	// partially or fully in scope). False means the implementations
	// surface has no signal *anywhere* — the tile should read
	// "N/A · no impl tree in scope" rather than a misleading 0%
	// against an unmeasured denominator.
	HasImplementsSignal bool
	TestPct             int
	TestCount           int
	ExamplePct          int
	ExampleCount        int
	// UsagePct / UsageCount roll examples and workflows into one
	// presentation surface — the trichotomy the report headlines now
	// adopt (docs say · tests verify · usage shows). UsageCount is the
	// union (m.Example > 0 OR m.Workflow > 0), counted once per element,
	// so 100% is reachable honestly. The underlying ExampleCount and
	// WorkflowsTotal stay populated so the deeper structural collapse
	// can land later without re-deriving the union from the parts.
	UsagePct   int
	UsageCount int

	// Per-tier coverage breakdown — exposes the "100% concept docs"
	// lie that hides when commands and flags are pooled into one
	// denominator. The primary-detail tier (commands for cobra,
	// methods for FIDL) and the modifier tier (flags, fields) each
	// get their own row. Renders as the 2×3 grid in the redesigned
	// masthead. Counts are computed in the per-element loop below;
	// percentages are derived in the finalize block.
	PrimaryTotal  int    // count of primary-detail elements (commands)
	ModifierTotal int    // count of modifier elements (flags/switches)
	PrimaryNoun   string // "Commands" / "Methods" / "Knobs"
	ModifierNoun  string // "Flags" / "Fields" / ""
	// Container tier — the parent surface above the primary detail
	// tier. For FIDL: PROTOCOL elements above METHOD elements. For
	// proto: SERVICE-shaped PROTOCOL elements above their METHODs.
	// Ecosystems without a container tier (cobra; SUBCOMMAND IS the
	// container for its FLAGs) leave ContainerTotal=0 and the
	// container section does not render.
	//
	// Containers carry the same surface set as their children for
	// interface kinds: kindSurfaces[PROTOCOL] = {docs.reference,
	// docs.concepts, examples, implementations} — same as METHOD.
	// So the per-tile grid mirrors the primary tier's grid.
	ContainerTotal              int
	ContainerNoun               string
	ContainerConceptN           int
	ContainerConceptPct         int
	ContainerTestN              int
	ContainerTestPct            int
	ContainerExampleN           int
	ContainerExamplePct         int
	ContainerImplementationsN   int
	ContainerImplementationsPct int
	ContainerBridgedN           int
	ContainerSubhead            string
	PrimaryConceptPct           int
	PrimaryConceptN             int
	PrimaryTestPct              int
	PrimaryTestN                int
	PrimaryExamplePct           int
	PrimaryExampleN             int
	PrimaryImplementationsPct   int
	PrimaryImplementationsN     int
	PrimaryBridgedN             int
	ModifierConceptPct          int
	ModifierConceptN            int
	ModifierTestPct             int
	ModifierTestN               int
	ModifierExamplePct          int
	ModifierExampleN            int
	ModifierImplementationsPct  int
	ModifierImplementationsN    int
	ModifierBridgedN            int
	// Cross-module test floor. The pooled Test%s above count an element
	// as tested if ANY attributed test references it — including tests in
	// OTHER modules that merely share a type name (a name-token collision).
	// These *InModule* counts admit only tests whose path is inside this
	// library, giving the trustworthy lower bound. A large gap between the
	// pooled % and the in-module % is the collision-inflation signal the
	// validation chip surfaces as "Needs tuning".
	ContainerTestInModuleN   int
	ContainerTestInModulePct int
	PrimaryTestInModuleN     int
	PrimaryTestInModulePct   int
	ModifierTestInModuleN    int
	ModifierTestInModulePct  int
	// MastheadInsight is the italic subtitle re-derived from the
	// 2×3 grid above. It points at the weakest cell(s) and names
	// what an agent reading the surface would miss. Replaces the
	// older free-text masthead lede whose copy was a lie when the
	// pooled "X% concept docs" rolled over a tier of mostly-empty
	// flag claims.
	MastheadInsight string
	// WorkflowConfigured signals whether a workflow adapter ran.
	// When false, the workflow block renders the explicit
	// empty-state ("no workflow corpus configured") rather than a
	// silent 0%.
	WorkflowConfigured bool
	// Workflows lists every documented workflow (a doc sequencing >=2
	// elements) with its staleness, sorted stalest-first. Each is scored
	// by the max lag over the elements it sequences — its
	// most-recently-changed dependency. This is the per-workflow "go
	// review this sequence" signal; workflows are deliberately kept out
	// of the run-level Lag distribution (see walkDocPaths) so a single
	// many-element workflow doc can't over-weight it.
	Workflows []WorkflowRow
	// WorkflowsStaleN is how many workflows are Aging or Stale — a
	// referenced element moved >30d past the workflow doc. Headlined in
	// the Workflows section as the "needs review" count.
	WorkflowsStaleN int
	// Guides lists every authored guide (a workflows-surface doc that
	// sequences commands) with its CROSS-REPO staleness — guide commit
	// time from the docs repo (e.g. github/docs) vs each command's code
	// commit time from the scanned code repo (e.g. cli/cli). Populated
	// only when the snapshot recorded a workflows docs_dir
	// (DocSurfaceDirs), i.e. an authored-doc surface that lives in a
	// different repo from the code. Empty for single-repo scans, where
	// the run-level Lag distribution already covers doc drift. Sorted
	// stalest-first.
	Guides []GuideRow
	// GuidesStaleN is how many guides are Aging or Stale — a command they
	// teach moved >30d past the guide. The guide "needs review" count.
	GuidesStaleN int
	// Workflow-coverage roll-ups. Computed by walking every
	// element's profile.docs.reference.byAdapter.workflows.refs[]
	// and aggregating across the unique source-paths that show up
	// there. Distinct workflow = distinct .md source path. Length
	// of a workflow = number of distinct elements that path
	// references. The histogram is grouped into the buckets the
	// masthead renders.
	WorkflowsTotal        int
	WorkflowsAvgCommands  float64
	PrimaryInWorkflowN    int
	PrimaryInWorkflowPct  int
	ModifierInWorkflowN   int
	ModifierInWorkflowPct int
	WorkflowLengthHist    []WorkflowLengthBucket
	WorkflowSubhead       string

	CoverageSentence string

	// Insight sentences rendered as italic ledes per the brand rule
	// "italic = a sentence the reader couldn't write themselves from
	// glancing at the section." Each is data-derived; if it ever
	// degrades to pure description, drop the italic styling at the
	// call site rather than weakening the sentence.
	CategorizationInsight string // categorization (UpSet) view lede
	FixPriorityInsight    string // fix-priority (worklist) view lede
	AnomaliesInsight      string // anomalies section lede

	// Masthead numbers — the dynamic 3rd/4th heroes. Defaults to
	// domain-drift + time-drift; the picker can fall back to top
	// gap kinds when no LLM ran (see error-metrics doc §6).
	HeroDriftA Hero // 3rd hero
	HeroDriftB Hero // 4th hero

	Overlap []OverlapRow

	SubstanceCounts map[string]int // SUBSTANTIVE/PARTIAL/SIGNATURE_ONLY/ABSENT → count
	SubstanceTotal  int

	FixGroups []FixGroup
	// VisibleFixGroupCount is the number of FixGroups the Evidence rail
	// actually renders — excludes the "removed" and "deprecated" buckets
	// (status, not work) and any empty groups. Used in the rail header
	// so the count matches what the reader sees below it.
	VisibleFixGroupCount int
	// RailFixGroups is FixGroups filtered to what the Evidence rail
	// renders (no removed / no deprecated / no empty) and resorted
	// smallest-to-largest by member count. The retired Worklist
	// section's severity order lives on in FixGroups for other
	// consumers (JSON, MCP, prbot); the rail uses ascending-size
	// because the reader scans top-down and small groups are easier
	// to triage first.
	RailFixGroups []FixGroup

	// UnassignedMembers are the methods that don't land in any
	// rendered FixGroup — bridged + deprecated + removed + anything
	// the worklist doesn't claim. Rendered as the trailing block at
	// the bottom of the Evidence rail so Theo can browse the whole
	// surface; the worklist on top stays the queue, the bucket below
	// is the table of contents.
	UnassignedMembers []MethodRow
	// UnassignedLabel reads "Bridged" when every member is fully
	// bridged; otherwise "Out of scope" (the mixed case where the
	// bucket contains bridged + deprecated + removed). Picked from
	// the population, not declared up front, so the bucket name
	// matches what's actually inside it.
	UnassignedLabel string
	// UnassignedCap is the max rows the rail renders before showing a
	// "+ N more" overflow footer. Kubectl's 967 bridged commands need
	// this cap or the rail blows the page.
	UnassignedCap int
	// UnassignedOverflow is len(UnassignedMembers) - UnassignedCap
	// when positive, otherwise zero. Pre-computed because Go
	// templates can't subtract; the renderer just reads it.
	UnassignedOverflow int
	// RailRank maps each rail-eligible element ID to its alphabetical
	// rank across the union of RailFixGroups members and
	// UnassignedMembers. Rendered as `style="order:N"` on each
	// .ev-member; the rail flips to a flat flex layout when a
	// surface/search filter is active, at which point those orders
	// produce a single A→Z list across (formerly-grouped) members.
	RailRank map[string]int
	// WorklistCaption is the one-line accounting note rendered above the
	// Fix-priority worklist for buckets the worklist intentionally does
	// not surface as rows: removed (gone at the target API level) and
	// deprecated (expected gaps). Empty when both buckets are empty.
	WorklistCaption string
	Methods         []MethodRow
	Anomalies       []AnomalyRow
	// AnomalyGroups is Anomalies clustered by Cause, sorted by group
	// size desc. The Findings section renders this rather than the
	// flat Anomalies list.
	AnomalyGroups []AnomalyGroup
	GeneratedAt   string

	// SourceURLTemplate, when set, turns path:line labels in the report
	// into clickable links. Placeholders: {path}, {abs_path}, {line}.
	// Examples:
	//   "https://github.com/grpc/grpc/blob/master/{path}#L{line}"
	//   "vscode://file/{abs_path}:{line}"
	//   "cursor://file/{abs_path}:{line}"
	//   "file://{abs_path}"
	// {abs_path} requires AbsRepoRoot to be set on the ReportData.
	SourceURLTemplate string

	// AbsRepoRoot is the absolute path of the scanned repo root.
	// Used to expand {abs_path} in SourceURLTemplate. Empty leaves
	// {abs_path} substitutions blank.
	AbsRepoRoot string

	// HeaderStyle selects the masthead/numtable layout. Recognized
	// values: "full" (default, v0.1: 5-KPI numtable), "hero"
	// (single "% bridged" tile), "minimal" (one-line identification
	// strip, no stat tiles). Lets us iterate on the top-section
	// design without rebuilds.
	HeaderStyle string

	// Commit, when set, is appended to the minimal/hero strip as a
	// short git hash. Caller responsibility to provide a useful form
	// (full SHA or short SHA). Empty omits the segment.
	Commit string

	// Analyzers lists the names of analyzers the server had
	// configured at scan time (from sheaf.textproto's analyzer
	// blocks). The Findings section uses this to disambiguate the
	// empty-findings state: empty list → no analyzers configured,
	// non-empty list → analyzers ran and the corpus is clean.
	// Older servers / test harnesses without WithReview return nil
	// here; the template falls back to the legacy vague message.
	Analyzers []string

	// EvidencePanels carries the per-element evidence-split-pane data
	// (one row per element in r.Methods, same order). Computed from
	// the snapshot's element + profile data plus on-disk source reads.
	// Nil when absRepoRoot is empty — the enrichment pass needs a
	// repo root to read source from.
	EvidencePanels []EvidencePanel

	// PanelByID indexes EvidencePanels by ElementID so the worklist's
	// member rows in the report template can reach the matching panel
	// without a slice scan. Populated alongside EvidencePanels.
	PanelByID map[string]*EvidencePanel
}

type Hero struct {
	Label string
	Count int
	Say   string
}

// WorkflowLengthBucket is one row of the masthead's workflow-length
// histogram. Label is "2 cmd", "3 cmd", …, "6+ cmd". Count is the
// number of workflows of that length. BarPct is the bar width
// relative to the largest bucket (0-100).
type WorkflowLengthBucket struct {
	Label  string
	Count  int
	BarPct int
}

// Deprecation captures a non-empty @available(...) record. Any of
// the version strings may be empty; "DeprecatedIn" being non-empty
// is the load-bearing signal for "this is deprecated."
//
// Ecosystem identifies which versionscheme.Scheme governs comparison
// of these version strings ("fidl" → HEAD/NEXT/numeric semantics).
// Empty ecosystem falls back to the FIDL scheme for back-compat.
type Deprecation struct {
	AddedIn      string
	DeprecatedIn string
	RemovedIn    string
	Note         string
	Ecosystem    string
	// Inferred is true when this record was synthesized from a high
	// per-element deprecation ratio rather than parsed from an explicit
	// LIBRARY-kind @available marker. Banner copy adjusts so the
	// reader knows the signal is derived, not declared.
	Inferred bool
}

// IsDeprecated reports whether this record indicates deprecation —
// the DeprecatedIn version must be set, RemovedIn alone counts
// (removed > deprecated), or the record is inferred from a high
// per-element ratio.
func (d *Deprecation) IsDeprecated() bool {
	if d == nil {
		return false
	}
	return d.DeprecatedIn != "" || d.RemovedIn != "" || d.Inferred
}

// IsRemovedAt reports whether this element no longer exists at the
// given target level, using the ecosystem-appropriate version
// scheme (FIDL by default — HEAD/NEXT/numeric). Delegates to
// internal/versionscheme; see that package for per-scheme rules.
func (d *Deprecation) IsRemovedAt(targetLevel string) bool {
	if d == nil {
		return false
	}
	return versionscheme.For(d.Ecosystem).IsRemovedAt(d.RemovedIn, targetLevel)
}

type OverlapRow struct {
	Concept bool
	Test    bool
	Example bool
	Count   int
	Pct     int
	// Term is the single past-participle category name ("Unclaimed",
	// "Asserted", …) defined in sheaf-categorization-vocabulary.md.
	// The template renders it bold + uppercase, followed by an arrow
	// and the Tagline.
	Term string
	// Tagline is the short clause that follows the arrow — a
	// compressed restatement of what the term implies.
	Tagline string
	// Label is the legacy combined form ("Term — tagline") kept for
	// backward compat with CLI consumers; the HTML template prefers
	// Term + Tagline rendered side by side.
	Label       string
	Explanation string
	Highlight   bool   // the fully-bridged row gets the green accent
	LineClass   string // CSS line class for the upset visual ("", "ln-1-2", "ln-1-3", "ln-2-3")
	// ComboClass packs which surfaces are on into a CSS-safe token
	// the redesigned upset2 layout uses for bar color coding: one of
	// "combo-none", "combo-d", "combo-t", "combo-e", "combo-dt",
	// "combo-de", "combo-te", "combo-dte". d=docs, t=tests, e=examples.
	ComboClass string
	// Members are the short-names of the methods that fall into this
	// row's combination. Used by the upset2 row-expansion view so a
	// click on a row shows which specific methods are in the group.
	Members []string
	// Action / Severity / FixKey carry the merged "what to fix" content
	// for this combo. The standalone worklist section is gone; each
	// UpSet row now reads as both the categorization AND the fix.
	Action   string // imperative verb sentence: what to do for methods in this combo
	Severity string // one-line trust-damage rationale (italic, secondary)
	FixKey   string // taxonomy key: "time", "domain", "orphan", "untested",
	//                 "noexample", "tested-undoc", "no-doc", "bridged"
	// BarPct is the percentage width of this row's micro-bar relative
	// to the largest count in the OverlapRow slice. Populated after
	// the count-desc sort. The Categorization view renders these as
	// small inline bars beside each row's count.
	BarPct int
}

type FixGroup struct {
	Key      string
	Label    string
	Severity string
	Tone     string
	Action   string
	// DotDocs / DotTests / DotExamples flag which of the three surface
	// indicators next to the group's count should render filled. The
	// dot strip replaces the old micro-bar under the count number; one
	// dot per surface (docs / tests / examples), filled when the
	// fix-group's action targets that surface. "noworkflow" is the
	// exception: all three are filled because the elements already
	// have docs+tests+examples — the gap is at the recipe tier.
	DotDocs     bool
	DotTests    bool
	DotExamples bool
	// ShortAction is the imperative-verb label rendered as the row's
	// always-visible title in the Fix-priority view (e.g. "ADD A TEST",
	// "REPLACE THE STUB"). Drops the longer Action sentence's framing
	// in favor of one short phrase the user can act on at a glance.
	// ALL CAPS — distinguishes the worklist group label from the
	// surrounding sentence-case subtitle / severity prose at a glance,
	// and reads as a chip/tag rather than a fragment of the body copy.
	ShortAction string
	// BarPct is the percentage width of this row's micro-bar relative
	// to the largest group in the worklist. Populated after grouping.
	BarPct int
	Items  []MethodRow
}

type MethodRow struct {
	Name      string
	ShortName string
	// Kind is the ContractElementKind string ("METHOD", "FLAG",
	// "PROTOCOL", "CPP_CLASS", …). Populated from the snapshot
	// element's kind field. Drives the per-kind bridged math and
	// the per-row tile/chip rendering: interface kinds hide the
	// Tests chip and show Implementations; CLI kinds do the inverse.
	Kind    string
	Concept int
	// ConceptDoc counts NARRATIVE concept-doc claims attributed to this
	// element on the additive docs.concepts surface (the conceptdoc
	// engine's anchored-mention attribution). It is PARALLEL to Concept and
	// strictly additive: Concept stays the `///`-fed reference-doc count and
	// is never repurposed. Populated from the snapshot profile's
	// docs.concepts bucket; 0 when no concept-doc source ran.
	ConceptDoc int
	// ConceptDocVerdict is the element's clear/ambiguous classification on the
	// concept-doc surface ("clear" | "ambiguous" | ""). "" means silent (no
	// anchored mention). Feeds the report-level reach partition; never
	// rendered per-row (there is no concept-doc chip).
	ConceptDocVerdict string
	Test              int
	Example           int
	// Implementations counts IMPLEMENTS-edge entries on the element's
	// CoverageProfile.implementations surface. Non-zero only for
	// interface kinds (METHOD / TYPE / PROTOCOL / SYSCALL).
	Implementations int
	// ImplementationsList carries the per-impl details for rendering
	// as cross-links on the row. Empty when Implementations == 0.
	ImplementationsList []ImplementationRow
	Drift               string // "none"|"domain"|"time"
	Bridged             bool
	NeedsTest           bool
	NeedsExample        bool
	Fix                 string
	Refs                []MethodRef
	Deprecation         *Deprecation // nil if not deprecated
	Removed             bool         // true if this method is already gone at the target API level
	// Substance grade — highest substance level across this element's
	// doc refs. One of "SUBSTANTIVE" / "PARTIAL" / "SIGNATURE_ONLY" /
	// "ABSENT". Used by the report template's clickable substance bar
	// to filter the method list. Empty for non-primary-detail elements.
	Substance string
	// LagBucket is this element's worst (stalest) doc-lag bucket —
	// "FRESH" / "RECENT" / "AGING" / "STALE" — from
	// LagResult.ElementBuckets. Empty when the element has no countable
	// cross-file doc pair. Rendered as the .ev-member data-lag flag the
	// Lag quartile bands filter the Evidence rail on, exactly the way the
	// Depth bands filter on Substance.
	LagBucket string
	// Tier classifies the row for per-tier filters in the report's
	// JS layer. "primary" for ecosystem detail elements (SUBCOMMAND
	// for cobra, METHOD for FIDL), "modifier" for the rest
	// (FLAG/SWITCH/POSITIONAL for cobra, fields/types elsewhere).
	// Empty for single-tier libraries. Used by showSurface() in the
	// template to filter the "show the N →" reveals to the calling
	// section's tier.
	Tier string
	// Workflow — count of distinct WORKFLOW DocClaims (each from a
	// distinct source file) that reference this element. Zero means
	// the element appears in no documented workflow. When the
	// workflows adapter is configured, the bridged definition tightens
	// to require Workflow > 0 alongside docs + tests + examples — an
	// element that has the three in isolation but no documented use
	// is no longer counted as fully bridged.
	Workflow int
}

// ElementRow is the post-refactor name for MethodRow — the per-element
// row used by every ecosystem (methods, subcommands, config knobs, …),
// not just FIDL methods. Defined as a Go type alias so the FIDL-era
// name `MethodRow` keeps working in render.go and the template
// without forcing a single big rename. A future cleanup can switch
// the primary name to ElementRow and the field name (r.Methods →
// r.Items) together; the alias is the steady state in the meantime.
type ElementRow = MethodRow

// ImplementationRow is a per-impl entry on an interface element's
// row. Renders as a cross-link to the impl's source location;
// CoveragePage is populated when the impl element is in the same
// report (currently always empty in v1 — impl elements live in
// different libraries than the FIDL interface they implement, so the
// scanner has no in-report anchor to link to).
type ImplementationRow struct {
	ImplElementID string
	ImplKind      string // "CPP_CLASS" / "RUST_TYPE" / …
	Path          string
	Line          uint32
	SourceURL     template.URL // expanded from SourceURLTemplate when set
	CoveragePage  string       // intra-report anchor when impl is in-scope
}

type MethodRef struct {
	Kind  string // "doc"|"contract"|"absent"
	Label string
	// Url is rendered as <a href> when non-empty. Typed as
	// template.URL so html/template won't sanitize editor-deep-link
	// schemes like vscode://, cursor://, file://, etc. The expander
	// (expandSourceURL) is the only writer; user input goes through
	// only the well-defined placeholder substitution, so the
	// trust-bypass is bounded.
	Url template.URL
}

type AnomalyRow struct {
	Name       string
	Cause      string // CSS class suffix: removed|version|polarity|gap|naming
	CauseLabel string
	Confidence float64
	Desc       string
	Links      []AnomalyLink
}

type AnomalyLink struct {
	Label      string
	IsContract bool
	// Url is the clickable target, expanded from
	// ReportData.SourceURLTemplate. Empty when no template is set or
	// when the link is description-only (no location). See MethodRef.Url
	// for the trust-bypass rationale.
	Url template.URL
	// Snippet is a short verbatim quote of the prose at this location
	// (where applicable) so the Findings section's per-row evidence
	// can show *what the doc says* without the reader clicking through.
	// Empty unless the adapter populated it.
	Snippet string
}

// AnomalyGroup clusters AnomalyRow entries by their Cause for the
// grouped-by-cause Findings layout. One AnomalyGroup renders as a
// section header (cause name + confidence range) followed by its
// member rows; each member row carries per-link evidence inline.
type AnomalyGroup struct {
	Cause      string  // "naming", "version", …
	CauseLabel string  // display name ("Naming gaps", "Version", …)
	ConfMin    float64 // smallest confidence in the group
	ConfMax    float64 // largest confidence in the group
	Items      []AnomalyRow
}

// BuildReport turns a server snapshot into the data we render.
// targetAPILevel scopes "removed in the past" detection — pass "HEAD"
// (or "") to treat any element with @available(removed=…) numeric or
// HEAD as already gone; pass a number like "27" to compare against a
// frozen API level. Pass empty to default to HEAD.
func BuildReport(snap *Snapshot, ecosystem, generatedAt, targetAPILevel string) *ReportData {
	return BuildReportWithOptions(snap, ecosystem, generatedAt, targetAPILevel, "", "", "", "")
}

// BuildReportWithOptions is BuildReport plus optional source-link
// settings: a URL template (see ReportData.SourceURLTemplate) and
// the absolute repo root used to expand {abs_path} placeholders.
// headerStyle selects the masthead layout: "full" (default), "hero",
// or "minimal". commit is the optional short git hash appended to
// the minimal/hero strip. Kept as a separate entry point to preserve
// the v1 signature.
func BuildReportWithOptions(snap *Snapshot, ecosystem, generatedAt, targetAPILevel, sourceURLTemplate, absRepoRoot, headerStyle, commit string) *ReportData {
	if targetAPILevel == "" {
		targetAPILevel = "HEAD"
	}
	if headerStyle == "" {
		headerStyle = "full"
	}
	// Resolve the ecosystem view. Unknown ecosystems fall back to the
	// FIDL view (which produces FIDL-shaped tiers — same behavior the
	// scanner had before this refactor for unrecognized values). The
	// view is what tells us which kinds aggregate into which header
	// tier and what counts as the "primary detail" surface for
	// substance grading and the worklist.
	view := EcosystemFor(ecosystem)

	r := &ReportData{
		Library:           snap.Library,
		Ecosystem:         ecosystem,
		GeneratedAt:       generatedAt,
		SubstanceCounts:   map[string]int{},
		TargetAPILevel:    targetAPILevel,
		SourceURLTemplate: sourceURLTemplate,
		AbsRepoRoot:       absRepoRoot,
		HeaderStyle:       headerStyle,
		Commit:            commit,
		Analyzers:         append([]string(nil), snap.Analyzers...),
		SurfacesRequired:  append([]string(nil), snap.SurfacesRequired...),
		// Gate the concept-doc reach line on whether a concept-doc source was
		// configured + scanned for this snapshot (set by BuildSnapshot). The
		// clear/ambiguous counts are tallied per-element below.
		ConceptDocConfigured: snap.ConceptDocSource,
	}
	// Tile visibility — surfaces not in surfaces_required are not
	// rendered as masthead slots. Empty surfaces_required falls
	// back to the v0 three-surface view (all tiles visible) so
	// pre-existing reports look unchanged.
	// Compute the union of kindSurfaces for elements present in the
	// library — this is the set of surfaces ANY element in the
	// library admits. A tile shows iff (a) the surface is declared
	// (or fallback mode), AND (b) the union contains it.
	hasInterfaceKind, hasCLIKind, hasImplKind := false, false, false
	for _, e := range snap.Elements {
		switch firstString(e["kind"]) {
		case "METHOD", "TYPE", "PROTOCOL", "SYSCALL":
			hasInterfaceKind = true
		case "FLAG", "SWITCH", "CONFIG_KNOB", "SUBCOMMAND", "POSITIONAL", "CONFIG_FACET":
			hasCLIKind = true
		case "CPP_CLASS", "RUST_TYPE":
			hasImplKind = true
		}
	}
	conceptAdmitted := hasInterfaceKind || hasCLIKind
	exampleAdmitted := hasInterfaceKind || hasCLIKind
	// Tests admission is kind-heuristic OR view-declared: the CLI/impl
	// kinds are the usual carriers of a test corpus, but a view may
	// explicitly declare "tests" in its EvidenceSurfaces() to admit the
	// surface for kinds the heuristic reads as a pure interface. openapi
	// (PROTOCOL/METHOD endpoints with endpoint tests) is the motivating
	// case; viewDeclaresSurface is strict, so fidl/cobra/proto and the
	// {contract,docs}-only views (manifest/crd/helm) are unaffected.
	testsAdmitted := hasCLIKind || hasImplKind || viewDeclaresSurface(view, "tests")
	implsAdmitted := hasInterfaceKind

	if len(snap.SurfacesRequired) == 0 {
		// Fallback mode: tiles visible when admitted by any kind, and
		// (for views that scope their surfaces) permitted by the view.
		r.ShowConceptTile = conceptAdmitted && viewAllowsSurface(view, "docs")
		r.ShowTestTile = testsAdmitted && viewAllowsSurface(view, "tests")
		r.ShowExampleTile = exampleAdmitted && viewAllowsSurface(view, "examples")
		r.ShowImplementationsTile = implsAdmitted && viewAllowsSurface(view, "implementations")
	} else {
		r.ShowConceptTile = conceptAdmitted && requiresSurface(snap.SurfacesRequired, "concept") && viewAllowsSurface(view, "docs")
		r.ShowTestTile = testsAdmitted && requiresSurface(snap.SurfacesRequired, "tests") && viewAllowsSurface(view, "tests")
		r.ShowExampleTile = exampleAdmitted && requiresSurface(snap.SurfacesRequired, "examples") && viewAllowsSurface(view, "examples")
		r.ShowImplementationsTile = implsAdmitted && requiresImplementationsSurface(snap.SurfacesRequired) && viewAllowsSurface(view, "implementations")
	}
	// Noun lookup: every supported ecosystem is now a registered
	// EcosystemView (cli, fidl, proto, + default fallback), so the
	// view's Noun() and TotalNoun() are always the right answer.
	// Unknown ecosystem ids hit defaultEcosystemView via EcosystemFor
	// and render with "element/elements" — a defensible default the
	// reader can recognize as "this scan used an ecosystem sheaf
	// doesn't have a tailored shape for."
	r.NounSingular, r.NounPlural = view.Noun()
	r.TotalNounSingular, r.TotalNounPlural = view.TotalNoun()

	// Initialize the header tiers at zero — they get incremented in
	// the element walk below. We deep-copy Kinds into the Tier so the
	// TierSpec slices returned by the view stay free of per-report state.
	tierSpecs := view.Tiers()
	r.Tiers = make([]Tier, len(tierSpecs))
	for i, t := range tierSpecs {
		r.Tiers[i] = Tier{
			ID:           t.ID,
			Label:        t.Label,
			Kinds:        append([]string(nil), t.Kinds...),
			ShowInHeader: t.ShowInHeader,
		}
	}

	// Pull out the synthetic LIBRARY-kind element (if any) before
	// computing totals — it carries library-scoped metadata
	// (@available) but is not itself a counted method.
	var libElem map[string]any
	var realElements []map[string]any
	for _, e := range snap.Elements {
		if firstString(e["kind"]) == "LIBRARY" {
			libElem = e
			continue
		}
		realElements = append(realElements, e)
	}
	if libElem != nil {
		r.LibraryDeprecation = deprecationFromConstraints(libElem["versionConstraints"])
	}
	r.TotalIncludingRemoved = len(realElements)

	// Index profiles by element id.
	profByID := map[string]map[string]any{}
	for _, p := range snap.Profiles {
		id, _ := p["elementId"].(string)
		if id != "" {
			profByID[id] = p
		}
	}

	// Index findings by subject for drift assignment.
	findingsBySubj := map[string][]map[string]any{}
	for _, f := range snap.Findings {
		subj, _ := f["subject"].(string)
		findingsBySubj[subj] = append(findingsBySubj[subj], f)
	}

	// Walk elements → MethodRow.
	// singlePrimaryID tracks the id of the most recently-seen
	// primary-detail element. When PrimaryTotal lands at 1, this
	// is the id of that lone element; the post-walk single-binary
	// detection compares it to snap.Library to decide whether the
	// Primary tier collapses (fd-shaped) or is a real one-command
	// project that deserves its own tier (rare; left visible).
	var singlePrimaryID string
	// Per-tier substance staging. Resolved post-walk into
	// r.SubstanceCounts / r.SubstanceTotal based on whether the
	// Primary tier was suppressed.
	primarySubstanceCounts := map[string]int{}
	modifierSubstanceCounts := map[string]int{}
	var primarySubstanceTotal, modifierSubstanceTotal int
	for _, e := range realElements {
		id, _ := e["id"].(string)
		prof := profByID[id]
		m := buildMethodRow(id, e, prof, findingsBySubj[id], sourceURLTemplate, absRepoRoot, snap.SurfacesRequired)
		// Inherit library-level deprecation onto methods that have no
		// per-element annotation. Per-element annotations take precedence.
		if m.Deprecation == nil && r.LibraryDeprecation.IsDeprecated() {
			m.Deprecation = r.LibraryDeprecation
		}
		m.Removed = m.Deprecation.IsRemovedAt(targetAPILevel)
		if m.Removed {
			r.RemovedCount++
			r.Methods = append(r.Methods, m)
			// Removed methods don't count toward Total, surface %s, or
			// substance distribution — they're not part of the live API.
			// We still keep them in r.Methods so the fix-group renderer
			// can show a "Removed (n)" group at the bottom for visibility.
			continue
		}
		if m.Deprecation.IsDeprecated() {
			r.DeprecatedCount++
		}
		r.Total++
		// Per-tier tallies for the section header. The view's TierSpec
		// list determines which kinds aggregate into which header tile;
		// for FIDL that's PROTOCOL → "Protocols", METHOD → "Methods",
		// TYPE → countable-but-no-tile, with the substance + worklist
		// scoped to METHOD (the "primary detail" kind). Other ecosystems
		// answer this differently — cobra has SUBCOMMAND as the primary
		// detail and FLAG/SWITCH as modifiers, CML has CONFIG_KNOB as
		// the only primary tier, etc.
		kind := firstString(e["kind"])
		// Does this element have at least one test inside its own module?
		// (vs. only cross-module name-collision attributions.)
		inModTest := m.Test > 0 && hasInModuleTest(prof, snap.Library)
		for i := range r.Tiers {
			if kindIn(kind, r.Tiers[i].Kinds) {
				r.Tiers[i].Count++
			}
		}
		// Substance bucketing — scoped to the view's primary-detail
		// kinds. The donut + "X of Y <noun> have docs that explain
		// behavior" synopsis both consume these counts; grading a
		// protocol or a type as "explains its behavior" doesn't make
		// sense, and keeping the denominator equal to the primary-detail
		// count (the number shown as the primary header tile) avoids
		// the 52-vs-98-methods inconsistency that the older
		// all-elements roll-up produced.
		// Per-tier substance bookkeeping. We always grade every element
		// and stage the result into per-tier buckets; the final
		// SubstanceCounts / SubstanceTotal pick the right tier
		// post-walk (when SuppressPrimaryTier is known). For
		// single-binary CLIs the lone primary element has no doc
		// claims because the binary name doesn't appear as a
		// qualified mention — measuring substance over it always
		// reads zero. The Modifier tier (flags) is where the
		// substance signal actually lives for that shape.
		if isPrimaryDetail(view, kind) {
			bucket := elementSubstance(prof)
			primarySubstanceCounts[bucket]++
			primarySubstanceTotal++
			m.Substance = bucket
		} else if isModifier(view, kind) {
			bucket := elementSubstance(prof)
			modifierSubstanceCounts[bucket]++
			modifierSubstanceTotal++
			m.Substance = bucket
		}
		// Surface presence aggregates (pooled — kept for back-compat
		// with the older single-percentage masthead and for the
		// coverage-shape sentence).
		if m.Concept > 0 {
			r.ConceptCount++
		}
		// Additive docs.concepts (narrative) rollup — parallel to
		// ConceptCount, never repurposing it. An element counts once if it
		// has any anchored concept-doc claim.
		if m.ConceptDoc > 0 {
			r.ConceptDocCount++
		}
		// Concept-doc reach partition rollup: clear / ambiguous from the
		// per-element verdict. Silent is the remainder (Total - Clear -
		// Ambiguous), computed in finalize.
		switch m.ConceptDocVerdict {
		case "clear":
			r.ConceptDocClear++
		case "ambiguous":
			r.ConceptDocAmbiguous++
		}
		if m.Test > 0 {
			r.TestCount++
		}
		if m.Example > 0 {
			r.ExampleCount++
		}
		if m.Implementations > 0 {
			r.ImplementationsCount++
		}
		if m.Bridged {
			r.Bridged++
		}
		// Per-tier presence aggregates. Primary = the ecosystem's
		// detail kind (SUBCOMMAND for cobra, METHOD for FIDL).
		// Modifier = the rest (FLAG/SWITCH/POSITIONAL for cobra,
		// fields/types/etc. elsewhere). The 2×3 masthead grid reads
		// directly off these.
		if isContainer(view, kind) {
			m.Tier = "container"
			r.ContainerTotal++
			if m.Concept > 0 {
				r.ContainerConceptN++
			}
			if m.Test > 0 {
				r.ContainerTestN++
			}
			if inModTest {
				r.ContainerTestInModuleN++
			}
			if m.Example > 0 {
				r.ContainerExampleN++
			}
			if m.Implementations > 0 {
				r.ContainerImplementationsN++
			}
			if m.Bridged {
				r.ContainerBridgedN++
			}
		} else if isPrimaryDetail(view, kind) {
			m.Tier = "primary"
			r.PrimaryTotal++
			// Track the first primary element's id so the
			// post-walk single-binary detection can compare it to
			// snap.Library. Only the n==1 case cares; we still
			// overwrite freely because we only consult the value
			// when PrimaryTotal turns out to be 1.
			singlePrimaryID = id
			if m.Concept > 0 {
				r.PrimaryConceptN++
			}
			if m.Test > 0 {
				r.PrimaryTestN++
			}
			if inModTest {
				r.PrimaryTestInModuleN++
			}
			if m.Example > 0 {
				r.PrimaryExampleN++
			}
			if m.Implementations > 0 {
				r.PrimaryImplementationsN++
			}
			if m.Bridged {
				r.PrimaryBridgedN++
			}
		} else if isModifier(view, kind) {
			m.Tier = "modifier"
			r.ModifierTotal++
			if m.Concept > 0 {
				r.ModifierConceptN++
			}
			if m.Test > 0 {
				r.ModifierTestN++
			}
			if inModTest {
				r.ModifierTestInModuleN++
			}
			if m.Example > 0 {
				r.ModifierExampleN++
			}
			if m.Implementations > 0 {
				r.ModifierImplementationsN++
			}
			if m.Bridged {
				r.ModifierBridgedN++
			}
		}
		r.Methods = append(r.Methods, m)
	}

	// HeaderTiers: visible-in-header subset of Tiers, in order. The
	// template iterates this for the header.
	r.HeaderTiers = nil
	for _, t := range r.Tiers {
		if t.ShowInHeader {
			r.HeaderTiers = append(r.HeaderTiers, t)
		}
	}

	r.ConceptPct = pct(r.ConceptCount, r.Total)
	r.ConceptDocPct = pct(r.ConceptDocCount, r.Total)
	r.TestPct = pct(r.TestCount, r.Total)
	r.ExamplePct = pct(r.ExampleCount, r.Total)
	r.ImplementationsPct = pct(r.ImplementationsCount, r.Total)
	r.HasImplementsSignal = r.ImplementationsCount > 0

	// Per-tier percentages for the 2×3 masthead grid. Computed off
	// per-tier denominators (PrimaryTotal, ModifierTotal) so the
	// "100% docs" lie that hides when commands and flags share a
	// denominator can't reappear here.
	r.ContainerConceptPct = pct(r.ContainerConceptN, r.ContainerTotal)
	r.ContainerTestPct = pct(r.ContainerTestN, r.ContainerTotal)
	r.ContainerExamplePct = pct(r.ContainerExampleN, r.ContainerTotal)
	r.ContainerImplementationsPct = pct(r.ContainerImplementationsN, r.ContainerTotal)
	r.PrimaryConceptPct = pct(r.PrimaryConceptN, r.PrimaryTotal)
	r.PrimaryTestPct = pct(r.PrimaryTestN, r.PrimaryTotal)
	r.PrimaryExamplePct = pct(r.PrimaryExampleN, r.PrimaryTotal)
	r.PrimaryImplementationsPct = pct(r.PrimaryImplementationsN, r.PrimaryTotal)
	r.ModifierConceptPct = pct(r.ModifierConceptN, r.ModifierTotal)
	r.ModifierTestPct = pct(r.ModifierTestN, r.ModifierTotal)
	r.ModifierExamplePct = pct(r.ModifierExampleN, r.ModifierTotal)
	r.ModifierImplementationsPct = pct(r.ModifierImplementationsN, r.ModifierTotal)
	r.ContainerTestInModulePct = pct(r.ContainerTestInModuleN, r.ContainerTotal)
	r.PrimaryTestInModulePct = pct(r.PrimaryTestInModuleN, r.PrimaryTotal)
	r.ModifierTestInModulePct = pct(r.ModifierTestInModuleN, r.ModifierTotal)
	// Tier nouns: pull from the ecosystem view's HeaderTiers. The
	// primary tier's label is always the first ShowInHeader tier
	// whose Kinds overlap PrimaryDetailKinds; the modifier tier is
	// the next ShowInHeader tier. Falls back to plain "Commands" /
	// "Flags" when the view doesn't expose modifier tiers (rare —
	// e.g. CML).
	r.PrimaryNoun = "Elements"
	for _, t := range r.HeaderTiers {
		isPrimaryTier := false
		for _, k := range t.Kinds {
			if isPrimaryDetail(view, k) {
				isPrimaryTier = true
				break
			}
		}
		if isPrimaryTier {
			r.PrimaryNoun = t.Label
			break
		}
	}
	// Container tier label — picked from the ecosystem view's
	// container-tier spec (FIDL: "Protocols"). Empty for ecosystems
	// without a container tier (cobra).
	if cspec := containerTierSpec(view); cspec != nil {
		r.ContainerNoun = cspec.Label
	}
	for _, t := range r.HeaderTiers {
		isPrimaryTier := false
		for _, k := range t.Kinds {
			if isPrimaryDetail(view, k) {
				isPrimaryTier = true
				break
			}
		}
		if !isPrimaryTier && t.Label != r.PrimaryNoun {
			r.ModifierNoun = t.Label
			break
		}
	}

	// Single-binary CLI detection. When the Primary tier has
	// exactly one element AND that element's id is the library
	// root itself (e.g. snap.Library == "fd" and the lone
	// SUBCOMMAND id is "fd"), the Primary tier is structurally a
	// tautology — the binary IS its flags. Suppress the Primary
	// tier and surface the binary's name in the Modifier section
	// heading as an identifier ("fd · 44 Flags"). Real
	// one-subcommand projects (rare) keep their tier because
	// their lone primary element does not equal the library name.
	if r.PrimaryTotal == 1 && singlePrimaryID == snap.Library && r.ModifierTotal > 0 {
		r.SuppressPrimaryTier = true
		r.BinaryRoot = snap.Library
		// Switch the canonical noun to the modifier tier so every
		// downstream sentence ("4 flags", "No flags match.", row
		// expl popups) reads against the surface the report
		// actually measures. The original primary noun ("command")
		// is now carried by BinaryRoot as an identifier, not a
		// counted population.
		if r.ModifierNoun != "" {
			r.NounPlural = strings.ToLower(r.ModifierNoun)
			r.NounSingular = singular(r.NounPlural)
		}
	}

	// Side-by-side tiers: when the report is reduced to a single chip per
	// tier (e.g. a FIDL library showing only the Reference-docs chip) and
	// there are 2-3 tiers, render them as columns instead of stacked.
	{
		chips := 0
		for _, on := range []bool{r.ShowConceptTile, r.ShowTestTile, r.ShowExampleTile, r.ShowImplementationsTile} {
			if on {
				chips++
			}
		}
		tiers := 0
		if r.ContainerTotal > 0 {
			tiers++
		}
		if !r.SuppressPrimaryTier && r.PrimaryTotal > 0 {
			tiers++
		}
		if r.ModifierTotal > 0 {
			tiers++
		}
		r.TiersSideBySide = chips == 1 && tiers >= 2 && tiers <= 3
	}

	// Resolve the staged substance counts into the final
	// SubstanceCounts / SubstanceTotal that drive the substance
	// bar and the synopsis. Default: primary-tier substance,
	// preserving v0 behavior. Single-binary CLIs (SuppressPrimaryTier)
	// switch to modifier-tier substance because the lone primary
	// element is the binary itself, which the qualified-mention
	// matcher never attributes to — its "0% substantive" reading
	// is a property of the matcher, not the project.
	if r.SuppressPrimaryTier {
		r.SubstanceCounts = modifierSubstanceCounts
		r.SubstanceTotal = modifierSubstanceTotal
	} else {
		r.SubstanceCounts = primarySubstanceCounts
		r.SubstanceTotal = primarySubstanceTotal
	}

	// The italic masthead subtitle. Re-derived every render from the
	// grid; never carried forward as static copy. Brand rule: italic
	// only if it states a finding the reader couldn't form by
	// glancing at the section above.
	r.MastheadInsight = mastheadInsight(r)
	// Workflow coverage roll-up. The workflows adapter places one
	// DocClaim per (workflow file × element-it-references) into
	// profile.docs.reference.byAdapter.workflows.refs[]. We walk
	// every element's bucket, build an edge set keyed on source
	// path, and aggregate from there: total workflows = distinct
	// source paths; workflow length = distinct elements that path
	// touches; per-element "in N workflows" = distinct source paths
	// in that element's bucket; per-tier coverage = elements with
	// at least one such edge, divided by tier total.
	workflowEdgesByPath := map[string]map[string]bool{} // path → element-ids
	workflowURLByPath := map[string]string{}            // path → canonical published URL (workflows adapter's url_base + slug)
	elementWorkflowCounts := map[string]int{}           // element-id → count
	for _, prof := range snap.Profiles {
		elemID := firstString(prof["elementId"])
		if elemID == "" {
			continue
		}
		docs, _ := prof["docs"].(map[string]any)
		ref, _ := docs["reference"].(map[string]any)
		ba, _ := ref["byAdapter"].(map[string]any)
		wf, _ := ba["workflows"].(map[string]any)
		refsList, _ := wf["refs"].([]any)
		seenPaths := map[string]bool{}
		for _, item := range refsList {
			r, ok := item.(map[string]any)
			if !ok {
				continue
			}
			path := firstString(r["path"])
			if path == "" {
				continue
			}
			seenPaths[path] = true
			if workflowEdgesByPath[path] == nil {
				workflowEdgesByPath[path] = map[string]bool{}
			}
			workflowEdgesByPath[path][elemID] = true
			// The workflows adapter stamps a canonical published URL on each
			// ref (url_base + slug) when url_base is configured; capture it so
			// the Freshness rows can link the doc name. First non-empty wins.
			if workflowURLByPath[path] == "" {
				if u := firstString(r["url"]); u != "" {
					workflowURLByPath[path] = u
				}
			}
		}
		elementWorkflowCounts[elemID] = len(seenPaths)
	}
	r.WorkflowConfigured = len(workflowEdgesByPath) > 0
	r.WorkflowsTotal = len(workflowEdgesByPath)
	if r.WorkflowsTotal > 0 {
		var sumLen int
		lengths := make([]int, 0, len(workflowEdgesByPath))
		for _, elems := range workflowEdgesByPath {
			sumLen += len(elems)
			lengths = append(lengths, len(elems))
		}
		r.WorkflowsAvgCommands = float64(sumLen) / float64(r.WorkflowsTotal)
		r.WorkflowLengthHist = buildWorkflowLengthHist(lengths)
		r.Workflows = computeWorkflowLag(snap, absRepoRoot, workflowEdgesByPath, workflowURLByPath)
		for _, w := range r.Workflows {
			if w.LagBucket == "AGING" || w.LagBucket == "STALE" {
				r.WorkflowsStaleN++
			}
		}
	}
	// Cross-repo guide-lag: authored guides (docs.github.com pages that
	// sequence commands) scored against the commands they teach, with the
	// guide's commit time read from the docs repo and each command's from
	// the scanned code repo. No-op unless the snapshot recorded a
	// workflows docs_dir (DocSurfaceDirs) — i.e. single-repo scans skip it
	// and rely on the run-level Lag distribution instead.
	r.Guides = computeGuideLag(snap, absRepoRoot)
	for _, g := range r.Guides {
		if g.LagBucket == "AGING" || g.LagBucket == "STALE" {
			r.GuidesStaleN++
		}
	}
	// Per-tier "in ≥1 workflow" coverage. Walk r.Methods so the
	// Tier field (set above) tells us which bucket the element
	// counts into. Also annotate every MethodRow with its
	// per-element workflow count (zero or more) so the bridged
	// recomputation below can fold workflow presence into the
	// 4-surface definition.
	for i := range r.Methods {
		m := &r.Methods[i]
		m.Workflow = elementWorkflowCounts[m.Name]
		// Usage = union of example and workflow, counted once per element.
		// Computed here because m.Workflow is only populated in this loop;
		// the earlier per-element pass that tallies ExampleCount runs
		// before the workflow roll-up. The count is the union, never the
		// sum, so 100% is reachable honestly without double-counting an
		// element covered by both.
		if m.Example > 0 || m.Workflow > 0 {
			r.UsageCount++
		}
		if m.Workflow == 0 {
			continue
		}
		switch m.Tier {
		case "primary":
			r.PrimaryInWorkflowN++
		case "modifier":
			r.ModifierInWorkflowN++
		}
	}
	r.PrimaryInWorkflowPct = pct(r.PrimaryInWorkflowN, r.PrimaryTotal)
	r.ModifierInWorkflowPct = pct(r.ModifierInWorkflowN, r.ModifierTotal)
	r.UsagePct = pct(r.UsageCount, r.Total)
	r.WorkflowSubhead = workflowSubhead(r)

	// Workflows are a CONDITIONAL surface: reported (PrimaryInWorkflowN
	// etc., and the per-element m.Workflow count set above) but NOT
	// folded into the bridged definition. An element that's documented,
	// tested, and demonstrated is fully bridged even when no recipe
	// names it — a thin or absent workflow signal must never demote an
	// otherwise-connected element (the "99% gap while three of four
	// surfaces are strong" failure mode). This keeps the per-report
	// bridged count consistent with the monorepo index's triple-based
	// completeness. The block still re-tallies off bridgedFromSurfaces
	// so the count reflects exactly the required surface set; only the
	// per-element workflow/insight annotations below depend on workflows.
	if r.WorkflowConfigured {
		r.Bridged = 0
		r.PrimaryBridgedN = 0
		r.ModifierBridgedN = 0
		for i := range r.Methods {
			m := &r.Methods[i]
			m.Bridged = bridgedFromSurfaces(*m, snap.SurfacesRequired)
			if m.Bridged {
				r.Bridged++
				switch m.Tier {
				case "primary":
					r.PrimaryBridgedN++
				case "modifier":
					r.ModifierBridgedN++
				}
			}
		}
		// Recompute the masthead insight off the new bridged counts.
		r.MastheadInsight = mastheadInsight(r)
	}

	r.SubstantivePct = substantivePct(r.SubstanceCounts)

	// Gap count = live methods minus the fully bridged ones (the
	// worklist size). Computed off Total so it includes TYPE/PROTOCOL
	// elements that don't satisfy the bridged definition (consistent
	// with how the existing fix groups partition).
	r.GapCount = r.Total - r.Bridged

	r.CoverageSentence = coverageShape(r.ConceptPct, r.TestPct, r.UsagePct, r.NounPlural, snap.SurfacesRequired)

	// Library-deprecation inference. If the FIDL/proto/etc. file doesn't
	// formally mark the LIBRARY element @available(deprecated=…) but a
	// strong majority of its elements carry per-element deprecation
	// markers, treat the whole library as effectively deprecated and
	// synthesize a LibraryDeprecation so the banner template fires. The
	// fuchsia.ui.gfx case: 132 of 141 elements deprecated, library
	// element itself unmarked.
	if !r.LibraryDeprecation.IsDeprecated() && r.Total > 0 {
		const threshold = 50 // percent
		if pct(r.DeprecatedCount, r.Total) >= threshold {
			r.LibraryDeprecation = &Deprecation{
				Inferred: true,
				Note: fmt.Sprintf(
					"%d of %d %s carry @available(deprecated=…) individually; no LIBRARY-level marker on this surface.",
					r.DeprecatedCount, r.Total, r.NounPlural,
				),
			}
		}
	}

	// Per-element doc-lag, computed here — after the method walk, before
	// groupFixes / the unassigned bucket copy MethodRows — so each
	// element's worst lag bucket rides along into the rendered .ev-member
	// rail. That lets the Lag quartile bands filter the Evidence rail the
	// same way the Depth bands filter on Substance. r.Lag (run-level) is
	// set from the same pass.
	r.Lag = computeLag(snap, absRepoRoot)
	// A shallow clone can't be differenced — skip the concept-doc fallback
	// (it reads the same truncated git history) so it can't substitute a
	// second fake distribution for the suppressed one.
	// Fallback for generated-contract scans (e.g. ffx): when no run-level
	// (element-location → doc) pair resolved a git timestamp — typically
	// because the contract is synthesized and carries no history — date each
	// element by its git-tracked test-evidence dir and difference the concept
	// docs against it instead. This keeps the misleading "no documentation
	// surface" empty-state from firing when narrative concept docs do exist.
	// It only engages when the run-level pass is empty, so scans with real
	// per-element lag (e.g. the self-scan golden) are left byte-identical.
	if r.Lag.Pairs == 0 && !r.Lag.Unavailable {
		if cd := computeConceptDocLag(snap, absRepoRoot); cd.Pairs > 0 {
			cd.Inline = r.Lag.Inline // keep the run-level inline-comment disclosure
			r.Lag = cd
		}
	}
	for i := range r.Methods {
		if b := r.Lag.ElementBuckets[r.Methods[i].Name]; b != "" {
			r.Methods[i].LagBucket = b
		}
	}
	r.Overlap = buildOverlap(r.Methods, r.Total, snap.SurfacesRequired)
	r.FixGroups = groupFixes(r.Methods)
	for _, g := range r.FixGroups {
		if g.Key == "removed" || g.Key == "deprecated" {
			continue
		}
		if len(g.Items) > 0 {
			r.VisibleFixGroupCount++
			r.RailFixGroups = append(r.RailFixGroups, g)
		}
	}
	// Rail order: smallest groups first (easiest triage first). Keep
	// the slice stable on ties so deterministic regen still produces
	// byte-identical output.
	sort.SliceStable(r.RailFixGroups, func(i, j int) bool {
		return len(r.RailFixGroups[i].Items) < len(r.RailFixGroups[j].Items)
	})
	// Trailing "Bridged / Out of scope" bucket — every method not
	// claimed by a rendered fix group. Walks r.Methods so the bucket
	// order matches the natural element order, not the worklist's
	// severity order.
	{
		claimed := make(map[string]bool, len(r.Methods))
		for _, g := range r.FixGroups {
			if g.Key == "removed" || g.Key == "deprecated" {
				continue
			}
			for _, it := range g.Items {
				claimed[it.Name] = true
			}
		}
		allBridged := true
		for _, m := range r.Methods {
			if claimed[m.Name] {
				continue
			}
			r.UnassignedMembers = append(r.UnassignedMembers, m)
			if !m.Bridged {
				allBridged = false
			}
		}
		if len(r.UnassignedMembers) > 0 {
			if allBridged {
				r.UnassignedLabel = "Bridged"
			} else {
				r.UnassignedLabel = "Out of scope"
			}
		}
		r.UnassignedCap = 30
		// Only truncate when the overflow is genuinely large; hiding a small
		// handful behind "+ N more" costs a click and saves little, so render
		// small overflows in full and reserve the overflow line for big groups.
		if extra := len(r.UnassignedMembers) - r.UnassignedCap; extra > 25 {
			r.UnassignedOverflow = extra
		} else {
			r.UnassignedCap = len(r.UnassignedMembers)
		}
	}
	// Build RailRank — alphabetical order across the union of rail
	// fix-group members + unassigned members. The filtered-rail layout
	// reads this as CSS `order:N` to sort matching members A→Z while
	// the unfiltered grouped layout ignores it (order only applies to
	// flex children, which we switch on via the .ev-list-filtered class).
	{
		type alphaEntry struct{ name, short string }
		var rs []alphaEntry
		seen := map[string]bool{}
		for _, g := range r.RailFixGroups {
			if g.Key == "removed" || g.Key == "deprecated" {
				continue
			}
			for _, it := range g.Items {
				if seen[it.Name] {
					continue
				}
				seen[it.Name] = true
				rs = append(rs, alphaEntry{it.Name, it.ShortName})
			}
		}
		for _, m := range r.UnassignedMembers {
			if seen[m.Name] {
				continue
			}
			seen[m.Name] = true
			rs = append(rs, alphaEntry{m.Name, m.ShortName})
		}
		sort.SliceStable(rs, func(i, j int) bool {
			return strings.ToLower(rs[i].short) < strings.ToLower(rs[j].short)
		})
		r.RailRank = make(map[string]int, len(rs))
		for i, e := range rs {
			r.RailRank[e.name] = i
		}
	}
	r.EvidencePanels = buildEvidencePanels(r.Methods, snap.Elements, snap.Profiles, r.FixGroups, sourceURLTemplate, absRepoRoot, view)
	if len(r.EvidencePanels) > 0 {
		r.PanelByID = make(map[string]*EvidencePanel, len(r.EvidencePanels))
		for i := range r.EvidencePanels {
			p := &r.EvidencePanels[i]
			r.PanelByID[p.ElementID] = p
		}
	}
	r.Anomalies = buildAnomalies(snap.Findings, sourceURLTemplate, absRepoRoot)
	r.AnomalyGroups = buildAnomalyGroups(r.Anomalies)

	// Dynamic heroes — defaults to domain/time drift counts. Falls
	// back to top non-bridged fix-group counts when no drift exists.
	r.HeroDriftA, r.HeroDriftB = pickHeroes(r)

	// Italic insight sentences (one per view) — derived from the
	// data so each makes a claim the reader couldn't form by glancing
	// at the section. See brand doc §7 "Italic = a finding, not a
	// description."
	r.CategorizationInsight = categorizationInsight(r)
	r.FixPriorityInsight = fixPriorityInsight(r)
	r.WorklistCaption = worklistCaption(r)
	r.AnomaliesInsight = anomaliesInsight(r)

	return r
}

// categorizationInsight names the bottleneck surface — the rarest
// of Concept / Test / Example among the live methods. Every Completed
// method needs all three, so the rarest one bounds how many can ever
// move into Completed without new work on that surface.
// mastheadInsight derives the italic subtitle from the per-tier
// grid. The shape of the sentence is dictated by where the gap
// lives, not by static copy:
//
//   - Both tiers strong everywhere → a one-line bridged sentence.
//   - Primary tier strong, modifier tier weak → "in isolation the
//     primary is X, in detail the modifier is Y" — the agent-
//     usability lede.
//   - Primary tier weak → name the primary gap; modifier is moot.
//   - No modifier tier (CML, FIDL without flag-tier) → fall back
//     to a single-tier framing.
//
// Brand rule: italic = a sentence the reader couldn't write from
// glancing at the grid. Each branch states a finding the cells
// alone don't connect.
func mastheadInsight(r *ReportData) string {
	if r.PrimaryTotal == 0 {
		return ""
	}
	primary := strings.ToLower(r.PrimaryNoun)
	if r.ModifierTotal == 0 {
		// Single-tier framing — FIDL-without-fields, CML, etc.
		// Pick the weakest of the three tier columns and call it.
		weakName, weakPct := "concept-documented", r.PrimaryConceptPct
		if r.PrimaryTestPct < weakPct {
			weakName, weakPct = "tested", r.PrimaryTestPct
		}
		if r.PrimaryExamplePct < weakPct {
			weakName, weakPct = "demonstrated by a worked example", r.PrimaryExamplePct
		}
		return fmt.Sprintf(
			"Only %d%% of %s are %s; %d of %d are fully bridged.",
			weakPct, primary, weakName, r.PrimaryBridgedN, r.PrimaryTotal,
		)
	}
	modifier := strings.ToLower(r.ModifierNoun)
	// Find the weakest modifier-tier cell — that's almost always
	// the agent-usability story.
	weakName, weakPct := "documented", r.ModifierConceptPct
	if r.ModifierTestPct < weakPct {
		weakName, weakPct = "tested", r.ModifierTestPct
	}
	if r.ModifierExamplePct < weakPct {
		weakName, weakPct = "demonstrated in a worked example", r.ModifierExamplePct
	}
	// Primary-tier shape — what an agent would conclude looking only
	// at the command row.
	primaryShape := "well documented and tested"
	switch {
	case r.PrimaryConceptPct < 60:
		primaryShape = "thinly documented"
	case r.PrimaryTestPct < 30:
		primaryShape = "documented but lightly tested"
	}
	return fmt.Sprintf(
		"In isolation, %s are %s. In detail, %s aren't — only %d%% are %s. "+
			"%d of %d %s are fully bridged; %d of %d %s are.",
		primary, primaryShape, modifier, weakPct, weakName,
		r.PrimaryBridgedN, r.PrimaryTotal, primary,
		r.ModifierBridgedN, r.ModifierTotal, modifier,
	)
}

// buildWorkflowLengthHist groups the per-workflow length values into
// fixed buckets ("2 cmd", "3 cmd", "4 cmd", "5 cmd", "6+ cmd") and
// computes the bar width as a percentage of the largest bucket. Used
// by the workflow-coverage block to render an ASCII-style histogram
// strip without depending on a chart library.
func buildWorkflowLengthHist(lengths []int) []WorkflowLengthBucket {
	if len(lengths) == 0 {
		return nil
	}
	labels := []string{"2 cmd", "3 cmd", "4 cmd", "5 cmd", "6+ cmd"}
	counts := make([]int, 5)
	for _, n := range lengths {
		switch {
		case n <= 2:
			counts[0]++
		case n == 3:
			counts[1]++
		case n == 4:
			counts[2]++
		case n == 5:
			counts[3]++
		default:
			counts[4]++
		}
	}
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	out := make([]WorkflowLengthBucket, 5)
	for i := range out {
		bp := 0
		if max > 0 {
			bp = counts[i] * 100 / max
		}
		out[i] = WorkflowLengthBucket{Label: labels[i], Count: counts[i], BarPct: bp}
	}
	return out
}

// workflowSubhead derives the italic finding rendered beneath the
// Workflows section headline. When the modifier-tier (flags) is
// the gap and falls ≤ lowCoveragePct, the sentence leads with the
// spelled-out figure ("Seven percent of flags appear in any
// documented sequence…") — promoting the row into the headline
// rather than burying it in the row stack below. That number is
// the single most damning fact in the whole report.
//
// Brand rule: italic must state a finding the reader couldn't form
// from the rows below. Spelled-out numerals are the editorial
// signature for headline findings (same convention as numWord in
// the per-section subheads).
//
// Returns "" when no workflows exist so the template can suppress
// the line.
func workflowSubhead(r *ReportData) string {
	if r.WorkflowsTotal == 0 {
		return ""
	}
	primary := strings.ToLower(r.PrimaryNoun)
	// Single-tier libraries (CML, FIDL-without-fields) — no
	// command-vs-flag interpretation to draw.
	if r.ModifierTotal == 0 {
		return fmt.Sprintf(
			"%d documented workflows touch %d%% of the %s surface; the rest are present only as reference entries.",
			r.WorkflowsTotal, r.PrimaryInWorkflowPct, primary,
		)
	}
	modifier := strings.ToLower(r.ModifierNoun)
	primaryPct, modifierPct := r.PrimaryInWorkflowPct, r.ModifierInWorkflowPct
	// Headline case: modifier-tier workflow exposure is below the
	// shared low-coverage threshold (same number the per-tile
	// warn-chip fires on). Promote the figure to a spelled-out
	// lede — this is the agent-failure surface and deserves the
	// editorial register the rest of the rows can't carry.
	if modifierPct <= lowCoveragePct {
		return fmt.Sprintf(
			"%s percent of %s appear in any documented sequence — recipes name the %s, not the %s. An agent that knows a %s exists in isolation hasn't been shown when to use it.",
			pctWord(modifierPct), modifier,
			singular(primary), singular(modifier),
			singular(modifier),
		)
	}
	// Asymmetric but above the headline threshold — recipes name
	// the verbs but elide the options.
	if primaryPct-modifierPct >= 20 {
		return fmt.Sprintf(
			"Recipes name the %s, not the %s — %d%% of %s appear in at least one workflow, only %d%% of %s do.",
			singular(primary), singular(modifier),
			primaryPct, primary,
			modifierPct, modifier,
		)
	}
	// Both tiers low — the corpus barely touches the surface.
	if primaryPct < 30 {
		return fmt.Sprintf(
			"Only %d%% of %s and %d%% of %s appear in any documented sequence — the corpus is narrow.",
			primaryPct, primary, modifierPct, modifier,
		)
	}
	// Symmetric coverage — the rare healthy case.
	return fmt.Sprintf(
		"Recipes touch %d%% of %s and %d%% of %s — both tiers are well-represented across %d workflows.",
		primaryPct, primary, modifierPct, modifier, r.WorkflowsTotal,
	)
}

// lowCoveragePct is the single shared threshold for "this number is
// alarmingly low" across the report. Anything at or below this
// fires the per-tile warn-chip in the masthead and, in the
// Workflows subhead, promotes the figure to a spelled-out lede.
//
// Picked by deliberation, not measurement: 20% reads as "one in
// five" — the folk-defensible floor at which a reasonable engineer
// agrees the surface is too thin to be discoverable. Higher (33%)
// reads as "a third" and dilutes the signal; lower (10%) is too
// punitive for libraries with active but incomplete coverage.
// TODO: once we have ≥100 cross-project scans, replace this
// absolute threshold with a per-tier percentile (bottom decile
// fires the chip). The page's methodology footnote already
// promises this migration.
const lowCoveragePct = 20

// pctWord spells out small percentages for editorial emphasis
// ("Seven percent of flags…"). Falls back to the bare numeral
// once the figure reads more naturally as a number than as prose.
// The spelled-out form is the brand's signal that the figure is
// the headline finding rather than background data.
func pctWord(pct int) string {
	words := []string{
		"Zero", "One", "Two", "Three", "Four", "Five",
		"Six", "Seven", "Eight", "Nine", "Ten",
		"Eleven", "Twelve", "Thirteen", "Fourteen", "Fifteen",
		"Sixteen", "Seventeen", "Eighteen", "Nineteen", "Twenty",
	}
	if pct >= 0 && pct < len(words) {
		return words[pct]
	}
	return fmt.Sprintf("%d", pct)
}

// singular trims a trailing "s" from a plural noun. Crude but it
// covers the ecosystem nouns the report carries (commands → command,
// flags → flag, methods → method, fields → field).
func singular(plural string) string {
	if strings.HasSuffix(plural, "s") {
		return strings.TrimSuffix(plural, "s")
	}
	return plural
}

func categorizationInsight(r *ReportData) string {
	if r.Total == 0 {
		return ""
	}
	// r.NounPlural / NounSingular have already been switched to
	// the modifier noun in the SuppressPrimaryTier branch above,
	// so the insight reads against whatever surface this project
	// actually measures (e.g. "flags" for fd).
	noun := r.NounPlural
	nounSingular := r.NounSingular
	examplesRequired := len(r.SurfacesRequired) == 0 || requiresSurface(r.SurfacesRequired, "examples")
	if !examplesRequired {
		// 2-surface lede: drop the examples clause and the
		// "needs all three" assertion. The bottleneck is whichever
		// of concept/tests has the smaller count.
		rarestName, rarestCount := "concept docs", r.ConceptCount
		if r.TestCount < rarestCount {
			rarestName, rarestCount = "tests", r.TestCount
		}
		_ = rarestCount
		return fmt.Sprintf(
			"Concept docs cover %s %s; tests cover %s. %s is the bottleneck — every Completed %s needs both.",
			numWord(r.ConceptCount), noun,
			numWord(r.TestCount),
			//nolint:staticcheck // SA1019: rarestName is a fixed lowercase ASCII label, so Title's Unicode word-boundary caveat doesn't apply; avoids a golang.org/x/text dependency.
			strings.Title(rarestName), nounSingular,
		)
	}
	rarestName, rarestCount := "docs", r.ConceptCount
	if r.TestCount < rarestCount {
		rarestName, rarestCount = "tests", r.TestCount
	}
	if r.UsageCount < rarestCount {
		rarestName, rarestCount = "usage", r.UsageCount
	}
	_ = rarestCount
	// numWord turns a zero count into the spelled-out word "Zero" so
	// the italic finding doesn't read as a missing number ("Docs cover
	// 0 methods…" → "Docs cover Zero methods…"). House style for
	// headlines + italic subheadlines.
	// Trichotomy: docs say · tests verify · usage shows. UsageCount is
	// the union of ExampleCount and Workflow-bearing elements (counted
	// once each); the underlying split is preserved for the deeper
	// structural refactor and the Workflows section below.
	return fmt.Sprintf(
		"Docs cover %s %s; tests cover %s; usage covers %s. %s is the bottleneck — every Completed %s needs all three.",
		numWord(r.ConceptCount), noun,
		numWord(r.TestCount), numWord(r.UsageCount),
		//nolint:staticcheck // SA1019: rarestName is a fixed lowercase ASCII label, so Title's Unicode word-boundary caveat doesn't apply; avoids a golang.org/x/text dependency.
		strings.Title(rarestName), nounSingular,
	)
}

// fixPriorityInsight names where the biggest single-group leverage
// sits in the worklist. Picks the FixGroup with the largest member
// count (not the highest severity — that's the row order's job) and
// reports what fraction of the non-Completed surface it covers.
func fixPriorityInsight(r *ReportData) string {
	if len(r.FixGroups) == 0 {
		return ""
	}
	nonBridged := r.Total - r.Bridged
	if nonBridged <= 0 {
		return ""
	}
	// "removed" and "deprecated" are status, not work — the worklist
	// demotes both to a one-line caption. Exclude them here too so the
	// "largest group" insight doesn't pick a non-actionable bucket.
	var largest FixGroup
	haveLargest := false
	for _, g := range r.FixGroups {
		if g.Key == "removed" || g.Key == "deprecated" {
			continue
		}
		if !haveLargest || len(g.Items) > len(largest.Items) {
			largest = g
			haveLargest = true
		}
	}
	if !haveLargest {
		return ""
	}
	share := pct(len(largest.Items), nonBridged)
	_ = largest.ShortAction
	return fmt.Sprintf(
		"Closing the largest group would move %s%% of the debt — the biggest single shift available.",
		numWord(share),
	)
}

// worklistCaption builds the one-line accounting note above the
// Fix-priority worklist. Combines the removed and deprecated buckets
// into a single sentence since neither renders as a row — they are
// status, not action. Returns "" when both buckets are empty.
func worklistCaption(r *ReportData) string {
	var removed, deprecated int
	for _, g := range r.FixGroups {
		switch g.Key {
		case "removed":
			removed = len(g.Items)
		case "deprecated":
			deprecated = len(g.Items)
		}
	}
	if removed == 0 && deprecated == 0 {
		return ""
	}
	noun := func(n int) string {
		if n == 1 {
			return r.NounSingular
		}
		return r.NounPlural
	}
	verb := func(n int) string {
		if n == 1 {
			return "is"
		}
		return "are"
	}
	tail := " — shown in the States view, not as work."
	switch {
	case removed > 0 && deprecated > 0:
		return fmt.Sprintf(
			"%d %s %s removed at this API level and %d %s deprecated upstream%s",
			removed, noun(removed), verb(removed),
			deprecated, verb(deprecated),
			tail,
		)
	case removed > 0:
		return fmt.Sprintf(
			"%d %s %s removed at this API level%s",
			removed, noun(removed), verb(removed), tail,
		)
	default:
		return fmt.Sprintf(
			"%d %s %s deprecated upstream%s",
			deprecated, noun(deprecated), verb(deprecated), tail,
		)
	}
}

// anomaliesInsight names the most-suspect element if any anomaly
// clusters by name, otherwise reports the single highest-confidence
// finding. Falls back to an empty string when nothing meaningful
// emerges; the caller should render nothing italic in that case.
func anomaliesInsight(r *ReportData) string {
	if len(r.Anomalies) == 0 {
		return ""
	}
	// Cluster by Name; if one name dominates (>= 2 hits), call it out.
	byName := map[string]int{}
	for _, a := range r.Anomalies {
		byName[a.Name]++
	}
	var topName string
	var topHits int
	for n, h := range byName {
		if h > topHits {
			topName, topHits = n, h
		}
	}
	if topHits >= 2 {
		return fmt.Sprintf("%d of %d anomalies cluster on %s — likely the same root cause; review that first.",
			topHits, len(r.Anomalies), topName)
	}
	// Otherwise: name the single highest-confidence finding.
	top := r.Anomalies[0]
	for _, a := range r.Anomalies[1:] {
		if a.Confidence > top.Confidence {
			top = a
		}
	}
	return fmt.Sprintf("Highest-confidence finding: %s (%.2f) — %s.", top.Name, top.Confidence, top.CauseLabel)
}

// requiresSurface reports whether the given surface name is in the
// project's declared surfaces_required set. Identifiers are
// normalized to lowercase before comparison; common aliases
// ("docs.concepts" matches "concepts", "concept", "docs.concept")
// are accepted so config can be written naturally.
func requiresSurface(surfacesRequired []string, name string) bool {
	if len(surfacesRequired) == 0 {
		return false
	}
	for _, s := range surfacesRequired {
		ls := strings.ToLower(s)
		switch name {
		case "concept", "docs.concepts":
			if ls == "concept" || ls == "concepts" || ls == "docs.concept" || ls == "docs.concepts" {
				return true
			}
		case "test", "tests":
			if ls == "test" || ls == "tests" || strings.HasPrefix(ls, "tests.") {
				return true
			}
		case "example", "examples":
			if ls == "example" || ls == "examples" || strings.HasPrefix(ls, "examples.") {
				return true
			}
		case "docs.reference":
			if ls == "docs.reference" || ls == "reference" {
				return true
			}
		case "docs.tutorial":
			if ls == "docs.tutorial" || ls == "tutorial" || ls == "tutorials" {
				return true
			}
		case "implementations", "implementation":
			if ls == "implementations" || ls == "implementation" {
				return true
			}
		}
	}
	return false
}

// bridgedFromSurfaces evaluates the per-element bridged predicate
// from the interface-surfaces design: an element is fully bridged
// iff every surface in kindSurfaces[element.kind] ∩ surfacesRequired
// has at least one substantive claim. When surfacesRequired is
// empty the kindSurfaces set alone is used.
//
// Concretely:
//   - Interface kinds (METHOD / TYPE / PROTOCOL / SYSCALL) need
//     Implementations > 0 instead of Tests > 0. They do NOT carry
//     a tests surface; per-row test counts are ignored in the
//     bridged predicate.
//   - CLI kinds (FLAG / SWITCH / CONFIG_KNOB / SUBCOMMAND /
//     POSITIONAL / CONFIG_FACET) need Tests > 0 as before.
//   - Implementation kinds (CPP_CLASS / RUST_TYPE) need
//     docs.reference + tests; they have no concept/examples
//     requirement.
//   - LIBRARY elements never enter the bridged tally — they are
//     filtered out before this is called.
//
// surfacesRequired entries that don't apply to the element's kind
// are ignored. A project declaring surfaces_required: "tests" with
// only interface elements computes bridged against the kindSurfaces
// set with "tests" dropped (interface elements have no tests
// surface), so the requirement effectively becomes a no-op rather
// than a permanent failure.
//
// docs.reference and docs.tutorial are not tracked in MethodRow's
// per-row counts — they're considered satisfied when the surface is
// declared but no row-local signal disproves it.
func bridgedFromSurfaces(m MethodRow, surfacesRequired []string) bool {
	kindSet := kindSurfaceSetForRow(m.Kind)
	// Determine the effective per-element surface set.
	//
	// Three cases:
	//   1. Kind is known and surfacesRequired is set → intersect.
	//   2. Kind is known and surfacesRequired is empty → use kindSet.
	//   3. Kind is unknown (legacy snapshots, fixture rows without
	//      kind populated) → fall back to surfacesRequired alone
	//      (or the v0 three-surface rule in fallback mode).
	var effective []string
	switch {
	case len(kindSet) > 0 && len(surfacesRequired) > 0:
		for _, s := range kindSet {
			if requiresSurface(surfacesRequired, s) {
				effective = append(effective, s)
			}
		}
	case len(kindSet) > 0:
		effective = kindSet
	case len(surfacesRequired) > 0:
		// Unknown kind, explicit surfaces. Treat each declared
		// surface as required against the per-row count.
		for _, s := range surfacesRequired {
			effective = append(effective, strings.ToLower(s))
		}
	default:
		// Unknown kind, no surfaces declared. v0 three-surface rule.
		return m.Concept > 0 && m.Test > 0 && m.Example > 0
	}
	if len(effective) == 0 {
		// Intersection was empty (e.g. interface kind with only
		// "tests" required). Nothing checkable; report as bridged so
		// the requirement is treated as a no-op rather than a
		// permanent failure.
		return true
	}
	for _, s := range effective {
		switch s {
		case "docs.concepts", "concept", "concepts":
			if m.Concept == 0 {
				return false
			}
		case "examples", "example":
			if m.Example == 0 {
				return false
			}
		case "tests", "test":
			if m.Test == 0 {
				return false
			}
		case "implementations", "implementation":
			if m.Implementations == 0 {
				return false
			}
		case "docs.reference", "reference":
			// Not tracked per-row; treated as satisfied when declared.
		}
	}
	return true
}

// kindSurfaceSetForRow returns the kindSurfaces set for the given
// kind string. Mirrors internal/indexer/policy.go::kindSurfaces.
// Keep in sync.
func kindSurfaceSetForRow(kind string) []string {
	switch kind {
	case "METHOD", "TYPE", "PROTOCOL", "SYSCALL":
		return []string{"docs.reference", "docs.concepts", "examples", "implementations"}
	case "FLAG", "SWITCH", "CONFIG_KNOB", "SUBCOMMAND", "POSITIONAL", "CONFIG_FACET":
		return []string{"docs.reference", "docs.concepts", "examples", "tests"}
	case "CPP_CLASS", "RUST_TYPE":
		return []string{"docs.reference", "tests"}
	case "LIBRARY":
		return []string{"docs.reference", "docs.concepts", "examples"}
	}
	return nil
}

// requiresSurface check used by bridgedFromSurfaces. Extends the
// existing requiresSurface with the implementations surface (new in
// the interface-surfaces redesign) and the docs.reference surface
// (also previously unhandled).
//
// This is a thin wrapper that adds the new surface names; the
// pre-existing requiresSurface continues to handle the old set.
func requiresImplementationsSurface(surfacesRequired []string) bool {
	for _, s := range surfacesRequired {
		ls := strings.ToLower(s)
		if ls == "implementations" || ls == "implementation" {
			return true
		}
	}
	return false
}

// buildMethodRow derives surface presence, drift class, fix text and
// — when urlTemplate is non-empty — clickable URLs on the receipts.
// urlTemplate is the report-level pattern; see ReportData.SourceURLTemplate.
// receipts for a single element.
func buildMethodRow(id string, e map[string]any, prof map[string]any, findings []map[string]any, urlTemplate, absRepoRoot string, surfacesRequired []string) MethodRow {
	kind := firstString(e["kind"])
	m := MethodRow{Name: id, ShortName: shortName(id), Drift: "none", Kind: kind}
	m.Deprecation = deprecationFromConstraints(e["versionConstraints"])
	if prof != nil {
		m.Concept = countConcept(prof)
		m.ConceptDoc = countConceptDoc(prof)
		m.ConceptDocVerdict = conceptDocVerdict(prof)
		m.Test = countTests(prof)
		m.Example = countExamples(prof)
		m.Implementations = countImplementations(prof)
		if isInterfaceKindString(kind) && m.Implementations > 0 {
			m.ImplementationsList = extractImplementations(prof, urlTemplate, absRepoRoot)
		}
	}
	m.Bridged = bridgedFromSurfaces(m, surfacesRequired)
	// NeedsTest is meaningful only for kinds whose surface set
	// includes tests. Interface kinds have no tests surface — they
	// can't "need a test" in the bridged sense.
	if isInterfaceKindString(kind) {
		m.NeedsTest = false
	} else {
		m.NeedsTest = m.Test == 0
	}
	// NeedsExample only contributes to the fix sentence when the
	// project actually requires the examples surface. Without this,
	// projects that don't ship examples (single-binary CLIs like fd)
	// would see every fully-bridged-for-their-shape row demoted to
	// "Explained and tested, but no runnable example to copy."
	examplesRequired := len(surfacesRequired) == 0 || requiresSurface(surfacesRequired, "examples")
	m.NeedsExample = examplesRequired && m.Example == 0

	// Drift classification from finding kinds.
	for _, f := range findings {
		k, _ := f["kind"].(string)
		switch k {
		case "STALE_DOC":
			m.Drift = "time"
		case "TESTED_UNDOCUMENTED", "EXTERNAL_MENTION_ONLY":
			if m.Drift == "none" {
				m.Drift = "domain"
			}
		}
	}

	// Fix sentence — mirrors the reference HTML's per-class text.
	switch {
	case m.Bridged && m.Drift == "none":
		m.Fix = "Fully bridged — nothing to fix."
	case m.Drift == "time":
		m.Fix = "Docs reference a signature that has since changed — likely stale."
	case m.Drift == "domain":
		m.Fix = "Concept docs describe the behavior but never name this call."
	case m.Concept == 0 && m.Test == 0 && m.Example == 0:
		m.Fix = "No doc, test or example — exists only in the contract."
	case m.NeedsTest && m.NeedsExample:
		m.Fix = "Documented, but neither tested nor exampled."
	case m.NeedsTest:
		m.Fix = "Documented but no test exercises it."
	case m.NeedsExample:
		m.Fix = "Explained and tested, but no runnable example to copy."
	default:
		m.Fix = "Fully bridged — nothing to fix."
	}

	// Receipts (source references). Each is a doc/contract/absent token.
	m.Refs = buildReceipts(m, e, prof, findings, urlTemplate, absRepoRoot)
	return m
}

func buildReceipts(m MethodRow, e map[string]any, prof map[string]any, findings []map[string]any, urlTemplate, absRepoRoot string) []MethodRef {
	var out []MethodRef
	// Doc receipts. Prefer the adapter-supplied url (e.g. markdowncli's
	// url_base + filename, which encodes per-page slugging the URL
	// template can't reproduce) and fall back to template expansion.
	for _, r := range docRefsWithLoc(prof) {
		out = append(out, MethodRef{
			Kind:  "doc",
			Label: r.label,
			Url:   pickURL(r.url, urlTemplate, absRepoRoot, r.path, r.line),
		})
	}
	// Contract location (always present).
	if path, line := locPathLine(e["location"]); path != "" {
		label := formatLoc(path, line)
		if m.Drift == "time" {
			label += " — signature changed"
		}
		if m.Drift == "domain" {
			label = m.Name + " — never named in prose"
		}
		out = append(out, MethodRef{
			Kind:  "contract",
			Label: label,
			Url:   pickURL(locURL(e["location"]), urlTemplate, absRepoRoot, path, line),
		})
	}
	// Test receipts — show up to maxTestRefsPerMethod individual tests
	// with file:line URLs; if there are more, append an "+N more"
	// summary chip so the receipt strip doesn't blow out the page on
	// elements with hundreds of tests (e.g. grpc.channelz.v1/Channelz).
	const maxTestRefsPerMethod = 5
	tests := testRefsWithLoc(prof)
	shown := tests
	if len(shown) > maxTestRefsPerMethod {
		shown = shown[:maxTestRefsPerMethod]
	}
	for _, t := range shown {
		out = append(out, MethodRef{
			Kind:  "test",
			Label: t.label,
			Url:   pickURL(t.url, urlTemplate, absRepoRoot, t.path, t.line),
		})
	}
	if extra := len(tests) - len(shown); extra > 0 {
		out = append(out, MethodRef{
			Kind:  "test",
			Label: fmt.Sprintf("+%d more tests", extra),
		})
	}
	// Named absences.
	if m.NeedsTest {
		out = append(out, MethodRef{Kind: "absent", Label: "no test found"})
	}
	if m.NeedsExample {
		out = append(out, MethodRef{Kind: "absent", Label: "no example found"})
	}
	return out
}

// pickURL returns the strongest URL available for a ref. The
// adapter-supplied url wins because adapters know about per-page
// slugging the URL template can't reproduce (e.g. markdowncli's
// kubectl_get/_index.md → kubernetes.io/.../kubectl_get/). Falls back
// to template expansion (path:line into the scanner's
// --source-url-template), then empty.
func pickURL(adapterURL, tmpl, absRepoRoot, path string, line int) template.URL {
	if adapterURL != "" {
		return template.URL(adapterURL)
	}
	return expandSourceURL(tmpl, absRepoRoot, path, line)
}

// expandSourceURL substitutes {path}, {abs_path}, and {line} into the
// template. Empty template → empty URL (no link rendered). Empty path
// → empty URL (we don't generate links to "nowhere"). Line ≤ 0 produces
// an empty {line} substitution but still emits a path-only URL.
//
// {abs_path} expands to absRepoRoot + "/" + path, suitable for editor
// deep-link schemes like vscode://file/{abs_path}:{line} or
// cursor://file/{abs_path}:{line}. Empty absRepoRoot leaves it blank.
func expandSourceURL(tmpl, absRepoRoot, path string, line int) template.URL {
	if tmpl == "" || path == "" {
		return ""
	}
	out := strings.ReplaceAll(tmpl, "{path}", path)
	abs := ""
	if absRepoRoot != "" {
		abs = absRepoRoot + "/" + path
	}
	out = strings.ReplaceAll(out, "{abs_path}", abs)
	if line > 0 {
		out = strings.ReplaceAll(out, "{line}", fmt.Sprintf("%d", line))
	} else {
		out = strings.ReplaceAll(out, "{line}", "")
	}
	return template.URL(out)
}

func formatLoc(path string, line int) string {
	if line > 0 {
		return fmt.Sprintf("%s:%d", path, line)
	}
	return path
}

// ============================================================
// Surface presence counters — walk the CoverageProfile shape.
// ============================================================

func countConcept(p map[string]any) int {
	d, _ := p["docs"].(map[string]any)
	if d == nil {
		return 0
	}
	n := 0
	for _, key := range []string{"concept", "tutorial", "releaseNotes", "faq"} {
		if arr, ok := d[key].([]any); ok {
			n += len(arr)
		}
	}
	if g, ok := d["guide"].(map[string]any); ok {
		for _, key := range []string{"migration", "troubleshooting", "cookbook"} {
			if arr, ok := g[key].([]any); ok {
				n += len(arr)
			}
		}
	}
	// Treat reference docs as concept-bearing too — the reference
	// surface is where most prose lives in IDL-style ecosystems.
	if ref, ok := d["reference"].(map[string]any); ok {
		n += countReferenceRefs(ref)
	}
	return n
}

// countConceptDoc counts claims on the NEW additive docs.concepts surface —
// narrative concept-doc attributions produced by the conceptdoc engine's
// anchored-mention pass. It reads ONLY the docs.concepts bucket and is
// strictly parallel to countConcept: it never touches docs.concept /
// docs.reference (the `///`-fed surface), so existing Concept counts and
// every shipped report are unaffected. Returns 0 when no concept-doc source
// ran (the bucket is absent), which is the correct not-covered signal.
//
// The bucket is a list of DocClaims under docs.concepts (protojson decodes
// a repeated message to []any); its length is the per-element claim count.
func countConceptDoc(p map[string]any) int {
	d, _ := p["docs"].(map[string]any)
	if d == nil {
		return 0
	}
	if arr, ok := d["concepts"].([]any); ok {
		return len(arr)
	}
	return 0
}

// conceptDocVerdict reads the per-element clear/ambiguous verdict the
// conceptdoc engine stamped onto the profile (docs.conceptsVerdict). It is the
// per-element half of the partition: "clear" / "ambiguous" for a referenced
// element, "" for a silent one (no verdict stamped). The reach-line rollup
// tallies clear + ambiguous; silent falls out as Total - referenced.
func conceptDocVerdict(p map[string]any) string {
	d, _ := p["docs"].(map[string]any)
	if d == nil {
		return ""
	}
	if v, ok := d["conceptsVerdict"].(string); ok {
		return v
	}
	return ""
}

// countReferenceRefs sums refs across the typed buckets and the
// by_adapter map. Keep this in sync with internal/coverage/refs;
// the two layers exist because Sheaf's MCP wire format is protojson
// (maps decode to map[string]any), while in-process Go code uses
// the typed proto.
func countReferenceRefs(ref map[string]any) int {
	if ref == nil {
		return 0
	}
	n := 0
	for _, key := range []string{"fidldoc", "clidoc", "dockerdoc"} {
		if arr, ok := ref[key].([]any); ok {
			n += len(arr)
		}
	}
	if ba, ok := ref["byAdapter"].(map[string]any); ok {
		for _, v := range ba {
			if list, ok := v.(map[string]any); ok {
				if arr, ok := list["refs"].([]any); ok {
					n += len(arr)
				}
			}
		}
	}
	return n
}

// walkReferenceRefs invokes fn for every DocRef across the typed
// buckets and the by_adapter map. Used by elementSubstance and
// docRefs so they handle markdowncli (and any future by_adapter
// entries) without needing per-adapter hardcoding.
func walkReferenceRefs(ref map[string]any, fn func(map[string]any)) {
	if ref == nil {
		return
	}
	visit := func(arr []any) {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				fn(m)
			}
		}
	}
	for _, key := range []string{"fidldoc", "clidoc", "dockerdoc"} {
		if arr, ok := ref[key].([]any); ok {
			visit(arr)
		}
	}
	if ba, ok := ref["byAdapter"].(map[string]any); ok {
		keys := make([]string, 0, len(ba))
		for k := range ba {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			list, ok := ba[k].(map[string]any)
			if !ok {
				continue
			}
			if arr, ok := list["refs"].([]any); ok {
				visit(arr)
			}
		}
	}
}

func countTests(p map[string]any) int {
	t, _ := p["tests"].(map[string]any)
	if t == nil {
		return 0
	}
	n := 0
	for _, key := range []string{"unit", "integration", "e2e", "ctf", "performance", "fuzz", "golden"} {
		if arr, ok := t[key].([]any); ok {
			n += len(arr)
		}
	}
	return n
}

func countExamples(p map[string]any) int {
	x, _ := p["examples"].(map[string]any)
	if x == nil {
		return 0
	}
	n := 0
	for _, key := range []string{"inTree", "inDocs", "external"} {
		if arr, ok := x[key].([]any); ok {
			n += len(arr)
		}
	}
	return n
}

// countImplementations counts entries on the implementations surface.
// Populated for interface kinds (METHOD / TYPE / PROTOCOL / SYSCALL)
// from IMPLEMENTS relationships in the corpus.
func countImplementations(p map[string]any) int {
	im, _ := p["implementations"].(map[string]any)
	if im == nil {
		return 0
	}
	impls, _ := im["impls"].([]any)
	return len(impls)
}

// extractImplementations renders the per-impl rows on an interface
// element's MethodRow. Returns nil when the surface is empty.
func extractImplementations(p map[string]any, urlTemplate, absRepoRoot string) []ImplementationRow {
	im, _ := p["implementations"].(map[string]any)
	if im == nil {
		return nil
	}
	impls, _ := im["impls"].([]any)
	if len(impls) == 0 {
		return nil
	}
	out := make([]ImplementationRow, 0, len(impls))
	for _, raw := range impls {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		row := ImplementationRow{
			ImplElementID: firstString(entry["implElementId"]),
			ImplKind:      firstString(entry["implKind"]),
			Path:          firstString(entry["path"]),
			CoveragePage:  firstString(entry["coveragePage"]),
		}
		if l, ok := entry["line"].(float64); ok {
			row.Line = uint32(l)
		}
		if row.Path != "" && urlTemplate != "" {
			row.SourceURL = expandSourceURL(urlTemplate, absRepoRoot, row.Path, int(row.Line))
		}
		out = append(out, row)
	}
	return out
}

// isInterfaceKindString reports whether the given snapshot kind string
// names an interface kind (METHOD / TYPE / PROTOCOL / SYSCALL). The
// scanner consumes kinds as strings from the JSON snapshot rather than
// the ContractElementKind enum directly; this helper centralizes the
// per-kind routing the renderer needs.
//
// Mirrors internal/indexer/policy.go::isInterfaceKind. Keep in sync.
func isInterfaceKindString(kind string) bool {
	switch kind {
	case "METHOD", "TYPE", "PROTOCOL", "SYSCALL":
		return true
	}
	return false
}

// elementSubstance returns the highest substance level across all
// of an element's doc refs. ABSENT when there are no doc refs.
func elementSubstance(p map[string]any) string {
	if p == nil {
		return "ABSENT"
	}
	d, _ := p["docs"].(map[string]any)
	if d == nil {
		return "ABSENT"
	}
	best := 0
	rank := map[string]int{
		"ABSENT": 1, "SIGNATURE_ONLY": 2, "PARTIAL": 3, "SUBSTANTIVE": 4,
	}
	walk := func(arr []any) {
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			if s, _ := m["substance"].(string); s != "" {
				if r := rank[s]; r > best {
					best = r
				}
			} else {
				// proto default: SUBSTANCE_UNSPECIFIED. Treat as
				// SIGNATURE_ONLY — the doc ref exists but carries
				// no substance signal.
				if rank["SIGNATURE_ONLY"] > best {
					best = rank["SIGNATURE_ONLY"]
				}
			}
		}
	}
	for _, key := range []string{"concept", "tutorial", "releaseNotes", "faq"} {
		if arr, ok := d[key].([]any); ok {
			walk(arr)
		}
	}
	if g, ok := d["guide"].(map[string]any); ok {
		for _, key := range []string{"migration", "troubleshooting", "cookbook"} {
			if arr, ok := g[key].([]any); ok {
				walk(arr)
			}
		}
	}
	if ref, ok := d["reference"].(map[string]any); ok {
		walkReferenceRefs(ref, func(m map[string]any) { walk([]any{m}) })
	}
	switch best {
	case 4:
		return "SUBSTANTIVE"
	case 3:
		return "PARTIAL"
	case 2:
		return "SIGNATURE_ONLY"
	}
	return "ABSENT"
}

// substantivePct — share of documented elements that grade as
// SUBSTANTIVE. Per requirements §4.1 hero 5.
func substantivePct(counts map[string]int) int {
	documented := counts["SUBSTANTIVE"] + counts["PARTIAL"] + counts["SIGNATURE_ONLY"]
	if documented == 0 {
		return 0
	}
	return pct(counts["SUBSTANTIVE"], documented)
}

// docRefLoc carries a doc reference's display label plus its raw
// path/line, so the URL-template expander can stamp clickable links.
// url is the adapter-supplied pre-computed URL (e.g. markdowncli's
// url_base + filename); it wins over template expansion when present.
type docRefLoc struct {
	label string
	path  string
	line  int
	url   string
}

// docRefsWithLoc walks the same doc surfaces docRefs used to and
// returns one docRefLoc per reference. The label is the same
// "<path>:<line>" form the v1 docRefs produced.
func docRefsWithLoc(p map[string]any) []docRefLoc {
	if p == nil {
		return nil
	}
	d, _ := p["docs"].(map[string]any)
	if d == nil {
		return nil
	}
	var out []docRefLoc
	emit := func(arr []any) {
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			path, _ := m["path"].(string)
			if path == "" {
				continue
			}
			line := numAsInt(m["line"])
			url, _ := m["url"].(string)
			out = append(out, docRefLoc{
				label: formatLoc(path, line),
				path:  path,
				line:  line,
				url:   url,
			})
		}
	}
	for _, key := range []string{"concept", "tutorial", "releaseNotes", "faq"} {
		if arr, ok := d[key].([]any); ok {
			emit(arr)
		}
	}
	if g, ok := d["guide"].(map[string]any); ok {
		for _, key := range []string{"migration", "troubleshooting", "cookbook"} {
			if arr, ok := g[key].([]any); ok {
				emit(arr)
			}
		}
	}
	if ref, ok := d["reference"].(map[string]any); ok {
		walkReferenceRefs(ref, func(m map[string]any) { emit([]any{m}) })
	}
	return out
}

// testRefLoc carries a test reference's display label plus its raw
// (test source path, line). Same pattern as docRefLoc. url is the
// adapter-supplied pre-computed URL when present.
type testRefLoc struct {
	label string
	path  string
	line  int
	url   string
}

// testRefsWithLoc walks every test bucket in the coverage profile
// (unit, integration, e2e, ctf, performance, fuzz, golden) and
// returns one testRefLoc per test reference. Label is
// "<testName> (<path>:<line>)" when testName is present, otherwise
// just "<path>:<line>". Tests are returned in profile-walk order;
// callers can truncate as needed.
func testRefsWithLoc(p map[string]any) []testRefLoc {
	if p == nil {
		return nil
	}
	t, _ := p["tests"].(map[string]any)
	if t == nil {
		return nil
	}
	var out []testRefLoc
	for _, key := range []string{"unit", "integration", "e2e", "ctf", "performance", "fuzz", "golden"} {
		arr, ok := t[key].([]any)
		if !ok {
			continue
		}
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			path, _ := m["path"].(string)
			if path == "" {
				continue
			}
			line := numAsInt(m["line"])
			name := firstString(m["testName"])
			label := formatLoc(path, line)
			if name != "" {
				label = name + " — " + label
			}
			url, _ := m["url"].(string)
			out = append(out, testRefLoc{label: label, path: path, line: line, url: url})
		}
	}
	return out
}

// hasInModuleTest reports whether the element's profile carries at least
// one test whose path is inside `library` (e.g. "pw_status/..."), as
// opposed to a cross-module name-collision attribution. Used to compute
// the trustworthy in-module test floor for the validation chips.
func hasInModuleTest(prof map[string]any, library string) bool {
	if library == "" {
		return false
	}
	pref := library + "/"
	for _, ref := range testRefsWithLoc(prof) {
		if strings.HasPrefix(ref.path, pref) {
			return true
		}
	}
	return false
}

// ChipData is the validation chip + popover for one (tier, surface) cell of
// the coverage grid. The chip is the at-a-glance verdict; the popover (opened
// on click) explains the number in plain English. Emitted by Chip().
type ChipData struct {
	Show        bool   // false => render no chip (e.g. structurally-N/A implementations)
	Verdict     string // "Verified" | "Lower bound" | "Needs tuning" | "Not measured"
	Glyph       string // ✓ ▲ ◐ ○
	Class       string // verdict CSS suffix: ok|lb|nt|nm
	Alert       bool   // card carries the low-coverage warn "!" -> chip goes white-on-black
	Surface     string // surface name for the popover header
	Number      string // plain-English "the number" line
	Provenance  string
	Issues      string
	Remediation string
}

func (c *ChipData) set(verdict, glyph, class string) {
	c.Verdict, c.Glyph, c.Class = verdict, glyph, class
}

// Chip computes the validation chip + popover for a (tier, surface) cell.
// tier is "container"|"primary"|"modifier"; surface is
// "concept"|"test"|"example"|"implementations". The verdicts derive from the
// per-tier coverage and the in-module test floor; the narratives are plain
// English. Called once per card from the report template.
func (r *ReportData) Chip(tier, surface string) ChipData {
	// Validation chips are tuned for the C++/cppheader coverage surfaces
	// (the in-module test floor, the Doxygen-reference narratives), so they
	// gate to the cpp ecosystem; every other ecosystem's report is unchanged.
	if r.Ecosystem != "cpp" {
		return ChipData{}
	}
	var pctv, n, total, inModPct, inModN, pooled int
	switch tier {
	case "container":
		total = r.ContainerTotal
		switch surface {
		case "concept":
			pctv, n, pooled = r.ContainerConceptPct, r.ContainerConceptN, r.ConceptPct
		case "test":
			pctv, n, inModPct, inModN, pooled = r.ContainerTestPct, r.ContainerTestN, r.ContainerTestInModulePct, r.ContainerTestInModuleN, r.TestPct
		case "example":
			pctv, n, pooled = r.ContainerExamplePct, r.ContainerExampleN, r.ExamplePct
		case "implementations":
			pctv, n = r.ContainerImplementationsPct, r.ContainerImplementationsN
		}
	case "primary":
		total = r.PrimaryTotal
		switch surface {
		case "concept":
			pctv, n, pooled = r.PrimaryConceptPct, r.PrimaryConceptN, r.ConceptPct
		case "test":
			pctv, n, inModPct, inModN, pooled = r.PrimaryTestPct, r.PrimaryTestN, r.PrimaryTestInModulePct, r.PrimaryTestInModuleN, r.TestPct
		case "example":
			pctv, n, pooled = r.PrimaryExamplePct, r.PrimaryExampleN, r.ExamplePct
		case "implementations":
			pctv, n = r.PrimaryImplementationsPct, r.PrimaryImplementationsN
		}
	case "modifier":
		total = r.ModifierTotal
		switch surface {
		case "concept":
			pctv, n, pooled = r.ModifierConceptPct, r.ModifierConceptN, r.ConceptPct
		case "test":
			pctv, n, inModPct, inModN, pooled = r.ModifierTestPct, r.ModifierTestN, r.ModifierTestInModulePct, r.ModifierTestInModuleN, r.TestPct
		case "example":
			pctv, n, pooled = r.ModifierExamplePct, r.ModifierExampleN, r.ExamplePct
		case "implementations":
			pctv, n = r.ModifierImplementationsPct, r.ModifierImplementationsN
		}
	}
	c := ChipData{Show: true, Alert: pctv <= 20}
	switch surface {
	case "concept":
		c.Surface = "Documentation"
		c.Provenance = "Read from Doxygen XML generated off the headers' /// comments. Deterministic — no LLM."
		switch {
		case total == 0:
			c.Show = false
		case pctv >= 100:
			c.set("Verified", "✓", "ok")
		case pctv == 0:
			c.set("Needs tuning", "◐", "nt")
		default:
			c.set("Lower bound", "▲", "lb")
		}
		c.Number = fmt.Sprintf("%d of %d elements carry a Doxygen API doc (%d%%); the library overall is %d%%. A per-symbol floor — class-level docs read higher.", n, total, pctv, pooled)
		if pctv < 100 {
			c.Issues = "The contract is enumerated per symbol while many methods are documented at the class level, so the per-method figure reads as a floor."
			c.Remediation = "Document the remaining symbols; read the Doxygen macro-definition entries to lift documented macros off 0%."
		}
	case "test":
		c.Surface = "Tests"
		c.Provenance = "Tests are matched to API elements by shared name token. Deterministic."
		crossN := n - inModN
		switch {
		case total == 0:
			c.Show = false
		case crossN > 0 && pctv > inModPct:
			c.set("Needs tuning", "◐", "nt")
			c.Number = fmt.Sprintf("The trustworthy floor is the in-module figure: %d%% (%d of %d). The pooled %d%% also counts tests in other modules that share a type name.", inModPct, inModN, total, pctv)
			c.Issues = fmt.Sprintf("%d of %d attributed elements are tested only by a cross-module name collision (a test in another module that merely uses the type), inflating the pooled %% above the %d%% floor.", crossN, n, inModPct)
			c.Remediation = "Gate cross-module attribution by include-edge / fully-qualified-name; auto-derive noisy_words for common type names."
		case inModPct >= 80:
			c.set("Verified", "✓", "ok")
			c.Number = fmt.Sprintf("%d of %d elements have a unit test inside this module (%d%%).", inModN, total, inModPct)
		default:
			c.set("Lower bound", "▲", "lb")
			c.Number = fmt.Sprintf("%d of %d elements have an in-module unit test (%d%%); methods exercised only through class instances aren't attributed.", inModN, total, inModPct)
			c.Remediation = "Add unit tests for the uncovered elements; name-token matching can't see instance-method calls."
		}
	case "example":
		c.Surface = "Usage"
		switch {
		case r.ExampleCount == 0:
			c.set("Not measured", "○", "nm")
			c.Number = "No usage/example source produced any evidence for this library, so this is unmeasured — not a real zero."
			c.Provenance = "No usage/example source is configured for this scan, so there is nothing to count."
			c.Issues = "A 0% here would read like a finding when it is really a wiring gap."
			c.Remediation = "Wire a usage/example source (an rst code-block language, or an examples adapter) to turn this into a real number."
		case total == 0:
			c.Show = false
		case pctv >= 80:
			c.set("Verified", "✓", "ok")
			c.Number = fmt.Sprintf("%d of %d elements appear in usage/examples (%d%%).", n, total, pctv)
		default:
			c.set("Lower bound", "▲", "lb")
			c.Number = fmt.Sprintf("%d of %d elements appear in usage/examples (%d%%).", n, total, pctv)
		}
	case "implementations":
		c.Surface = "Implementations"
		switch {
		case !r.HasImplementsSignal:
			c.Show = false // structurally N/A — no implementation tree in scope
		case pctv >= 80:
			c.set("Verified", "✓", "ok")
			c.Number = fmt.Sprintf("%d of %d interface elements have an implementation edge (%d%%).", n, total, pctv)
		default:
			c.set("Lower bound", "▲", "lb")
			c.Number = fmt.Sprintf("%d of %d interface elements have an implementation edge (%d%%).", n, total, pctv)
		}
	}
	return c
}

// ============================================================
// Overlap (UpSet rows).
//
// Rows are sorted by Count descending so the reader can see the
// fragmentation pattern at a glance (largest combinations first).
// Ties are broken by the catalog order below — see
// sheaf-report-generator-requirements.md §4.2 (UpSet sort rule).
// ============================================================

func buildOverlap(methods []MethodRow, total int, surfacesRequired []string) []OverlapRow {
	examplesRequired := len(surfacesRequired) == 0 || requiresSurface(surfacesRequired, "examples")
	counts := map[[3]bool]int{}
	members := map[[3]bool][]string{}
	for _, m := range methods {
		if m.Removed {
			continue // removed methods aren't part of the live surface
		}
		c := m.Concept > 0
		t := m.Test > 0
		e := m.Example > 0
		// When examples isn't a tracked surface for this project,
		// fold the e-axis out of the combo space: every element
		// reads as e=true so c-and-t combos collapse onto the
		// "Completed" row and the example-shaped rows become
		// empty (suppressed by the Count>0 template guard). The
		// row dots respect this projection too — the EXAMPLES
		// chip will read as not-applicable across the rendered
		// rows for that project.
		if !examplesRequired {
			e = true
		}
		key := [3]bool{c, t, e}
		counts[key]++
		name := m.ShortName
		if name == "" {
			name = m.Name
		}
		members[key] = append(members[key], name)
	}
	// Catalog of all 2³ = 8 surface combinations. Past-participle
	// names — each describes what the team did (or didn't) to the
	// call. See docs/scanner/sheaf-categorization-vocabulary.md for
	// the canonical reference and the naming rationale. When
	// examples isn't required, the 2-surface variant uses
	// rewritten labels that don't reference examples; the e=false
	// rows are still emitted but will have Count=0 and stay
	// hidden by the template's row guard.
	var rows []OverlapRow
	if examplesRequired {
		rows = []OverlapRow{
			{Concept: false, Test: false, Example: false,
				Term: "Unclaimed", Tagline: "only the contract, nothing else",
				Explanation: "The team never picked it up. Only the contract exists — no concept doc, no test, no example.",
				LineClass:   ""},
			{Concept: true, Test: false, Example: false,
				Term: "Asserted", Tagline: "claim in docs, nothing verifies it",
				Explanation: "The team made a claim in docs, but no test verifies the behavior and no example shows the call.",
				LineClass:   ""},
			{Concept: false, Test: true, Example: false,
				Term: "Exercised", Tagline: "tested, but no prose names it",
				Explanation: "The test suite works it, but no concept doc names the behavior and no example shows the call.",
				LineClass:   ""},
			{Concept: false, Test: false, Example: true,
				Term: "Sketched", Tagline: "copyable sample, no theory or proof",
				Explanation: "A copyable sample exists, but no concept doc explains it and no test verifies it. Readers can paste but can't reason.",
				LineClass:   ""},
			{Concept: true, Test: true, Example: false,
				Term: "Established", Tagline: "explained and verified, no example",
				Explanation: "The claim was backed by tests — explained and verified, but no example to copy.",
				LineClass:   "ln-1-2"},
			{Concept: true, Test: false, Example: true,
				Term: "Shown", Tagline: "explained and demoed, but not verified",
				Explanation: "The team explained it and showed an example, but no test verifies the behavior.",
				LineClass:   "ln-1-3"},
			{Concept: false, Test: true, Example: true,
				Term: "Practiced", Tagline: "works and is demoed, no prose",
				Explanation: "The team made it work in tests and demoed it, but never theorized the behavior in prose.",
				LineClass:   "ln-2-3"},
			{Concept: true, Test: true, Example: true,
				Term: "Completed", Tagline: "all three surfaces aligned",
				Explanation: "All three surfaces — concept doc, test, and example — line up on the same call.",
				LineClass:   "ln-1-3",
				Highlight:   true},
		}
	} else {
		// 2-surface variant: examples not required. The e-axis is
		// projected to true (see the count walk above) so the
		// e=false rows never have members. We still keep them in
		// the catalog with Count=0 so the template's
		// {{if $r.Count}} guard suppresses them cleanly.
		rows = []OverlapRow{
			{Concept: false, Test: false, Example: false,
				Term: "Unclaimed", Tagline: "only the contract, nothing else",
				Explanation: "(unreachable: 2-surface project; example axis is projected to true)",
				LineClass:   ""},
			{Concept: true, Test: false, Example: false,
				Term: "Asserted (no examples surface)", Tagline: "",
				Explanation: "(unreachable: 2-surface project)",
				LineClass:   ""},
			{Concept: false, Test: true, Example: false,
				Term: "Exercised (no examples surface)", Tagline: "",
				Explanation: "(unreachable: 2-surface project)",
				LineClass:   ""},
			{Concept: false, Test: false, Example: true,
				Term: "Unclaimed", Tagline: "only the contract, nothing else",
				Explanation: "The team never picked it up. Only the contract exists — no concept doc and no test.",
				LineClass:   ""},
			{Concept: true, Test: true, Example: false,
				Term: "(legacy slot)", Tagline: "",
				Explanation: "(unreachable: 2-surface project)",
				LineClass:   ""},
			{Concept: true, Test: false, Example: true,
				Term: "Asserted", Tagline: "claim in docs, nothing verifies it",
				Explanation: "The team made a claim in docs, but no test verifies the behavior.",
				LineClass:   ""},
			{Concept: false, Test: true, Example: true,
				Term: "Exercised", Tagline: "tested, but no prose names it",
				Explanation: "The test suite works it, but no concept doc names the behavior.",
				LineClass:   "ln-2-3"},
			{Concept: true, Test: true, Example: true,
				Term: "Completed", Tagline: "concept docs and tests aligned",
				Explanation: "Both configured surfaces — concept doc and test — line up on the same call.",
				LineClass:   "ln-1-2",
				Highlight:   true},
		}
	}
	// Populate the legacy Label field for any CLI consumer that
	// still reads the combined form. Format: "Term — tagline".
	for i := range rows {
		if rows[i].Tagline != "" {
			rows[i].Label = rows[i].Term + " — " + rows[i].Tagline
		} else {
			rows[i].Label = rows[i].Term
		}
	}
	for i := range rows {
		key := [3]bool{rows[i].Concept, rows[i].Test, rows[i].Example}
		rows[i].Count = counts[key]
		rows[i].Pct = pct(rows[i].Count, total)
		rows[i].ComboClass = comboClassFor(rows[i].Concept, rows[i].Test, rows[i].Example)
		rows[i].FixKey, rows[i].Action, rows[i].Severity = comboFixFor(rows[i].Concept, rows[i].Test, rows[i].Example, surfacesRequired)
		rows[i].Members = members[key]
		sort.Strings(rows[i].Members)
	}
	// Sort by Count desc; SliceStable preserves the catalog order on
	// ties so the output is deterministic and the no-data rows
	// (Count == 0) cluster at the end in their narrative order.
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Count > rows[j].Count
	})
	// BarPct on each row — share of the largest row's count so the
	// Categorization view can render tiny micro-bars beside each
	// count for at-a-glance distribution.
	maxC := 0
	for _, r := range rows {
		if r.Count > maxC {
			maxC = r.Count
		}
	}
	if maxC > 0 {
		for i := range rows {
			rows[i].BarPct = rows[i].Count * 100 / maxC
		}
	}
	return rows
}

// comboFixFor maps a (concept, test, example) tuple to the merged
// "fix" content the UpSet row carries. Returns (key, action, severity).
// The standalone worklist section is gone; each combo cell of the
// UpSet is now both the categorization and the action. Drift-driven
// findings (STALE_DOC, TESTED_UNDOCUMENTED) overlay on top of these
// per-row defaults as small chip annotations rendered by the template.
//
// surfacesRequired narrows the combo space. When examples isn't in
// the required set, the 3-surface combo collapses to a 2-surface
// model: "noexample" is not a real fix class (the project never
// committed to ship examples), and the c-and-t combo becomes
// "bridged" outright. Other combos drop their example-shaped
// language. Empty surfacesRequired preserves v0 three-surface
// behavior so unconfigured projects are unaffected.
func comboFixFor(c, t, e bool, surfacesRequired []string) (key, action, severity string) {
	examplesRequired := len(surfacesRequired) == 0 || requiresSurface(surfacesRequired, "examples")
	if !examplesRequired {
		// 2-surface model: examples is not a tracked surface for
		// this project, so the only axes are concept and test.
		// The third bit (e) is effectively masked out.
		switch {
		case c && t:
			return "bridged",
				"Fully bridged — keep them this way as the contract evolves.",
				"Both configured surfaces line up on the same call. No action required."
		case c && !t:
			return "untested",
				"Add a test that exercises each element against its documented behavior.",
				"Behavioral prose exists, but nothing verifies it still holds."
		case !c && t:
			return "tested-undoc",
				"Add the element name to the prose so search and grounding resolve it.",
				"Tests exercise these but no concept-doc names them. Vocabulary mismatch."
		default: // !c && !t
			return "orphan",
				"Replace the inherited stub with real behavioral prose, or accept it as boilerplate.",
				"A reference page exists but explains nothing. Present, but empty."
		}
	}
	switch {
	case c && t && e:
		return "bridged",
			"Fully bridged — keep them this way as the contract evolves.",
			"All three surfaces line up on the same call. No action required."
	case c && t && !e:
		return "noexample",
			"Add a runnable example, or promote an existing test snippet into one.",
			"Explained and proven, but nothing readers can copy. Lowest urgency."
	case c && !t && e:
		return "untested",
			"Add a test that exercises each method against its documented behavior.",
			"Usage is documented and demonstrated, but nothing verifies it still holds."
	case !c && t && e:
		return "no-doc",
			"Write the concept doc; the test and example show what to describe.",
			"Behavior is exercised and demonstrated, but no prose names the call."
	case c && !t && !e:
		return "untested",
			"Add a test that exercises each method against its documented behavior.",
			"Behavioral prose exists, but nothing verifies it still holds."
	case !c && t && !e:
		return "tested-undoc",
			"Add the method name to the prose so search and grounding resolve it.",
			"Tests exercise these but no concept-doc names them. Vocabulary mismatch."
	case !c && !t && e:
		return "no-doc",
			"Write the concept doc and add a test; the example shows what to copy.",
			"Only a copyable snippet exists. Readers can paste but can't reason."
	default: // !c && !t && !e
		return "orphan",
			"Replace the inherited stub with real behavioral prose, or accept it as boilerplate.",
			"A reference page exists but explains nothing. Present, but empty."
	}
}

// comboClassFor maps the (concept, test, example) tuple to a CSS
// class token used by the redesigned upset2 layout to color-code
// each row's horizontal bar by which surfaces are present.
func comboClassFor(c, t, e bool) string {
	var s string
	if c {
		s += "d"
	}
	if t {
		s += "t"
	}
	if e {
		s += "e"
	}
	if s == "" {
		return "combo-none"
	}
	return "combo-" + s
}

// MockOverlap returns a richer fictional OverlapRow set suitable
// for mocking up the upset2 visualization on libraries whose real
// data sits mostly in one bucket. Caller (scanner CLI) gates this
// behind a --mock-overlap flag.
func MockOverlap() []OverlapRow {
	// Fake but plausible Channelz-style method names per combo so the
	// row-expansion view has something interesting to show.
	rows := []OverlapRow{
		{Concept: true, Test: false, Example: false, Count: 27,
			Label:       "Asserted — claim in docs, nothing verifies it",
			Explanation: "The team made a claim in docs, but no test verifies the behavior and no example shows the call.",
			Members: []string{
				"Channelz.GetChannel", "Channelz.GetSubchannel", "Channelz.GetSocket",
				"Channelz.GetServer", "Channelz.GetServers", "Channelz.GetTopChannels",
				"Channelz.GetServerSockets", "ChannelData", "SubchannelData",
				"SocketData", "ServerData", "ChannelRef", "SubchannelRef",
				"SocketRef", "ServerRef", "SocketOption", "SocketOptionLinger",
				"SocketOptionTimeout", "SocketOptionTcpInfo", "Security",
				"Address", "Address.TcpIpAddress", "Address.UdsAddress",
				"Address.OtherAddress", "ChannelTrace", "ChannelTraceEvent",
				"ChannelConnectivityState",
			}},
		{Concept: false, Test: true, Example: false, Count: 19,
			Label:       "Exercised — tested, but no prose names it",
			Explanation: "The test suite works it, but no concept doc names the behavior and no example shows the call.",
			Members: []string{
				"GetChannelRequest", "GetChannelResponse", "GetServerRequest",
				"GetServerResponse", "GetServersRequest", "GetServersResponse",
				"GetSocketRequest", "GetSocketResponse", "GetSubchannelRequest",
				"GetSubchannelResponse", "GetTopChannelsRequest", "GetTopChannelsResponse",
				"GetServerSocketsRequest", "GetServerSocketsResponse",
				"Channel", "Subchannel", "Socket", "Server", "ChannelTraceEventSeverity",
			}},
		{Concept: true, Test: true, Example: false, Count: 14,
			Label:       "Established — explained and verified, no example",
			Explanation: "The claim was backed by tests — explained and verified, but no example to copy.",
			Members: []string{
				"Health.Check", "Health.Watch", "Health", "HealthCheckRequest",
				"HealthCheckResponse", "HealthCheckResponse.ServingStatus",
				"ServerReflection.ServerReflectionInfo", "ServerReflection",
				"ServerReflectionRequest", "ServerReflectionResponse",
				"FileDescriptorResponse", "ServiceResponse",
				"ListServiceResponse", "ErrorResponse",
			}},
		{Concept: false, Test: false, Example: false, Count: 11,
			Label:       "Unclaimed — only the contract, nothing else",
			Explanation: "The team never picked it up. Only the contract exists — no concept doc, no test, no example.",
			Members: []string{
				"LoadBalancer", "LoadBalancer.BalanceLoad", "LoadBalanceRequest",
				"LoadBalanceResponse", "ServerList", "Server.LoadBalanceRequest",
				"InitialLoadBalanceResponse", "InitialLoadBalanceRequest",
				"ClientStats", "ClientStatsPerToken", "LoadBalanceTokenSet",
			}},
		{Concept: true, Test: true, Example: true, Count: 8,
			Label:       "Completed — all three surfaces aligned",
			Explanation: "All three surfaces — concept doc, test, and example — line up on the same call.",
			Highlight:   true,
			Members: []string{
				"Echo.Say", "Echo.SayAgain", "Echo", "EchoRequest", "EchoResponse",
				"Greeter.SayHello", "Greeter", "HelloReply",
			}},
		{Concept: true, Test: false, Example: true, Count: 5,
			Label:       "Shown — explained and demoed, but not verified",
			Explanation: "The team explained it and showed an example, but no test verifies the behavior.",
			Members: []string{
				"RouteLookupService", "RouteLookupRequest", "RouteLookupResponse",
				"RouteLookupConfig", "GrpcKeyBuilder",
			}},
		{Concept: false, Test: true, Example: true, Count: 3,
			Label:       "Practiced — works and is demoed, no prose",
			Explanation: "The team made it work in tests and demoed it, but never theorized the behavior in prose.",
			Members:     []string{"BenchmarkService.UnaryCall", "BenchmarkService.StreamingCall", "SimpleRequest"},
		},
		{Concept: false, Test: false, Example: true, Count: 2,
			Label:       "Sketched — copyable sample, no theory or proof",
			Explanation: "A copyable sample exists, but no concept doc explains it and no test verifies it. Readers can paste but can't reason.",
			Members:     []string{"WorkerService.RunServer", "ServerArgs"},
		},
	}
	total := 0
	for _, r := range rows {
		total += r.Count
	}
	for i := range rows {
		rows[i].Pct = pct(rows[i].Count, total)
		rows[i].ComboClass = comboClassFor(rows[i].Concept, rows[i].Test, rows[i].Example)
		rows[i].FixKey, rows[i].Action, rows[i].Severity = comboFixFor(rows[i].Concept, rows[i].Test, rows[i].Example, nil)
		// Split the legacy combined Label into Term + Tagline so the
		// HTML template can style the term distinctly. Format is
		// "Term — tagline".
		if rows[i].Term == "" && rows[i].Label != "" {
			if parts := strings.SplitN(rows[i].Label, " — ", 2); len(parts) == 2 {
				rows[i].Term = parts[0]
				rows[i].Tagline = parts[1]
			} else {
				rows[i].Term = rows[i].Label
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Count > rows[j].Count })
	return rows
}

// ============================================================
// Fix groups (the "What to fix" worklist).
// ============================================================

func groupFixes(methods []MethodRow) []FixGroup {
	groups := map[string]*FixGroup{
		"time": {Key: "time", Label: "Stale docs — signature changed",
			Severity:    "Docs teach a call that no longer matches the contract. Actively misleading.",
			Tone:        "time",
			ShortAction: "UPDATE STALE DOCS",
			Action:      "Rewrite each doc to match the current signature."},
		"domain": {Key: "domain", Label: "Vocabulary mismatch",
			Severity:    "Docs describe the behavior but never name the actual call, so search and grounding miss it.",
			Tone:        "domain",
			ShortAction: "NAME THE CALL",
			Action:      "Add the identifier to the prose so grep and embeddings can resolve the name."},
		"orphan": {Key: "orphan", Label: "Signature-only — documented in name only",
			Severity:    "A reference page exists but explains nothing. Present, but empty.",
			Tone:        "gap",
			ShortAction: "REPLACE THE STUB",
			Action:      "Replace the inherited boilerplate with prose that explains the actual behavior."},
		"untested": {Key: "untested", Label: "Not named in any test",
			Severity:    "No test in the scanned source textually names this element. The behavior may still be exercised — through table-driven loops, integration scripts, or generic suites that don't cite the element by name — but the bridge from test to contract is missing the word.",
			Tone:        "test",
			ShortAction: "CITE IT IN A TEST",
			Action:      "Name this element in an existing test (a t.Run label, a table-driven case name, a comment) or add a test that does."},
		"noexample": {Key: "noexample", Label: "No runnable example",
			Severity:    "Explained and often tested, but nothing to copy. Lowest urgency.",
			Tone:        "example",
			ShortAction: "ADD AN EXAMPLE",
			Action:      "Add a runnable snippet, or promote an existing test into an example."},
		"noworkflow": {Key: "noworkflow", Label: "Not in any documented workflow",
			Severity:    "Documented, tested, and demonstrated in isolation — but no recipe shows it composed with anything. An agent that reads the docs may use the flag wrong because no sequence teaches when.",
			Tone:        "example",
			ShortAction: "WIRE INTO A RECIPE",
			Action:      "Name this element in a tutorial alongside the commands it composes with."},
		"deprecated": {Key: "deprecated", Label: "Deprecated — won't fix",
			Severity:    "Marked deprecated upstream. Coverage gaps here are expected; the replacement is where new tests should land.",
			Tone:        "deprecated",
			ShortAction: "MIGRATE TO THE REPLACEMENT",
			Action:      "Route new tests to the replacement and stop adding coverage to this surface."},
		"removed": {Key: "removed", Label: "Removed — no longer in API",
			Severity:    "Marked @available(removed=…) at or before the target API level. These don't exist in the live surface; shown here only so you can confirm the report scoped them out.",
			Tone:        "removed",
			ShortAction: "(Removed)",
			Action:      "No action — these methods are gone. If your tests still call them you'll see compile errors at the target level."},
	}
	for _, m := range methods {
		// Removed methods short-circuit everything else — they don't
		// show up as bridged, undocumented, etc. because the question
		// doesn't apply.
		if m.Removed {
			groups["removed"].Items = append(groups["removed"].Items, m)
			continue
		}
		if m.Bridged && m.Drift == "none" {
			continue
		}
		// Deprecated methods always go into the dedicated bucket so
		// they don't inflate the actionable worklist. Anything reading
		// the report should treat "no test" on a deprecated method as
		// expected, not as work to do.
		if m.Deprecation.IsDeprecated() {
			groups["deprecated"].Items = append(groups["deprecated"].Items, m)
			continue
		}
		switch {
		case m.Drift == "time":
			groups["time"].Items = append(groups["time"].Items, m)
		case m.Drift == "domain":
			groups["domain"].Items = append(groups["domain"].Items, m)
		case m.Concept == 0 && m.Test == 0 && m.Example == 0:
			groups["orphan"].Items = append(groups["orphan"].Items, m)
		case m.NeedsTest:
			groups["untested"].Items = append(groups["untested"].Items, m)
		case m.NeedsExample:
			groups["noexample"].Items = append(groups["noexample"].Items, m)
		case m.Concept > 0 && m.Test > 0 && m.Example > 0 && m.Workflow == 0:
			// All three per-element surfaces are present, but no
			// workflow names this element. The 4-surface bridged
			// definition demoted this case from "fully bridged"
			// to a gap that's only visible at the workflow tier.
			groups["noworkflow"].Items = append(groups["noworkflow"].Items, m)
		}
	}
	// Severity order. Deprecated then removed last — both are
	// "expected gaps" the reader can collapse mentally. "noworkflow"
	// sits between the missing-surface buckets and the deprecated
	// roll-up because it's a softer signal than missing tests or
	// examples but still a real coverage gap.
	order := []string{"time", "domain", "orphan", "untested", "noexample", "noworkflow", "deprecated", "removed"}
	// Surface dots per fix-group key. Filled = "this is the surface the
	// action targets" (i.e., the thing to add). noworkflow is the
	// exception — the elements already have docs+tests+examples, so
	// all three light up; the recipe is the 4th-tier gap not shown.
	dots := map[string]struct{ d, t, e bool }{
		"time":       {d: true},
		"domain":     {d: true},
		"orphan":     {d: true},
		"untested":   {t: true},
		"noexample":  {e: true},
		"noworkflow": {d: true, t: true, e: true},
	}
	var out []FixGroup
	for _, k := range order {
		if len(groups[k].Items) > 0 {
			g := *groups[k]
			if d, ok := dots[k]; ok {
				g.DotDocs = d.d
				g.DotTests = d.t
				g.DotExamples = d.e
			}
			out = append(out, g)
		}
	}
	// Compute BarPct on each group: percentage of items relative to
	// the largest group in the slice. The Fix-priority view renders
	// these as inline micro-bars beside each row's count.
	maxCount := 0
	for _, g := range out {
		if len(g.Items) > maxCount {
			maxCount = len(g.Items)
		}
	}
	if maxCount > 0 {
		for i := range out {
			out[i].BarPct = len(out[i].Items) * 100 / maxCount
		}
	}
	return out
}

// ============================================================
// Anomalies — LLM-derived findings; for now we synthesize the
// AnomalyStream from the existing Findings list, mapping kinds
// to cause classes the report uses.
// ============================================================

func buildAnomalies(findings []map[string]any, urlTemplate, absRepoRoot string) []AnomalyRow {
	var out []AnomalyRow
	for _, f := range findings {
		kind, _ := f["kind"].(string)
		cause, label := causeFromKind(kind)
		if cause == "" {
			continue
		}
		out = append(out, AnomalyRow{
			Name:       firstString(f["subject"]),
			Cause:      cause,
			CauseLabel: label,
			Confidence: confidenceFromSeverity(firstString(f["severity"])),
			Desc:       firstString(f["message"]),
			Links:      linksFromEvidence(f, urlTemplate, absRepoRoot),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func causeFromKind(k string) (cause, label string) {
	switch k {
	case "STALE_DOC":
		return "version", "Stale references"
	case "TESTED_UNDOCUMENTED":
		return "naming", "Naming gaps"
	case "EXTERNAL_MENTION_ONLY":
		return "gap", "Spec gaps"
	case "DOCUMENTED_UNTESTED":
		return "removed", "Likely removed"
	case "THIN_REFERENCE":
		return "polarity", "Polarity"
	}
	return "", ""
}

// buildAnomalyGroups clusters anomalies by their Cause and orders
// groups largest-first. Returns an empty slice when there are no
// anomalies, so the template can short-circuit safely.
func buildAnomalyGroups(rows []AnomalyRow) []AnomalyGroup {
	if len(rows) == 0 {
		return nil
	}
	byCause := map[string]*AnomalyGroup{}
	order := []string{} // preserve first-seen order for stable tie-breaks
	for _, r := range rows {
		g, ok := byCause[r.Cause]
		if !ok {
			g = &AnomalyGroup{
				Cause:      r.Cause,
				CauseLabel: r.CauseLabel,
				ConfMin:    r.Confidence,
				ConfMax:    r.Confidence,
			}
			byCause[r.Cause] = g
			order = append(order, r.Cause)
		}
		if r.Confidence < g.ConfMin {
			g.ConfMin = r.Confidence
		}
		if r.Confidence > g.ConfMax {
			g.ConfMax = r.Confidence
		}
		g.Items = append(g.Items, r)
	}
	out := make([]AnomalyGroup, 0, len(byCause))
	for _, k := range order {
		out = append(out, *byCause[k])
	}
	// Largest groups first; ties broken by first-seen order.
	sort.SliceStable(out, func(i, j int) bool {
		return len(out[i].Items) > len(out[j].Items)
	})
	return out
}

func confidenceFromSeverity(sev string) float64 {
	switch sev {
	case "ERROR":
		return 0.9
	case "WARNING":
		return 0.7
	case "INFO":
		return 0.55
	}
	return 0.5
}

func linksFromEvidence(f map[string]any, urlTemplate, absRepoRoot string) []AnomalyLink {
	var out []AnomalyLink
	ev, _ := f["evidence"].([]any)
	for _, item := range ev {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		loc, _ := m["location"].(map[string]any)
		path, line := locPathLine(loc)
		label := locString(loc)
		if label == "" {
			label = firstString(m["description"])
		}
		if label == "" {
			continue
		}
		isContract := strings.Contains(strings.ToLower(label), ".fidl") ||
			strings.Contains(strings.ToLower(label), ".proto")
		out = append(out, AnomalyLink{
			Label:      label,
			IsContract: isContract,
			Url:        expandSourceURL(urlTemplate, absRepoRoot, path, line),
		})
	}
	return out
}

// ============================================================
// Coverage-shape sentence — the section's framing italic.
// Mirrors the small classifier in coverage-narrative doc §3.
// ============================================================

func coverageShape(concept, test, usage int, noun string, surfacesRequired []string) string {
	// Surface-set-aware variant: when the project declares
	// surfaces_required and neither examples nor workflows is in it,
	// fall back to a two-surface narrative that doesn't claim "usage
	// is thin" on a project that never committed to ship it.
	// usage is the trichotomy's third leg — the union of examples
	// and workflows. If the underlying project declared examples
	// required, that's enough to gate the third-surface narrative.
	usageRequired := len(surfacesRequired) == 0 ||
		requiresSurface(surfacesRequired, "examples") ||
		requiresSurface(surfacesRequired, "workflows")
	if !usageRequired {
		highC := concept >= 60
		highT := test >= 60
		lowC := concept < 25
		lowT := test < 25
		switch {
		case highC && highT:
			return "Docs and tests both land — the contract is well-bridged on the surfaces this project ships."
		case highC && lowT:
			return "Docs are nearly complete, but few tests verify what the prose claims."
		case lowC && highT:
			return "Tests cover the surface, but the docs rarely name what they verify — readers can't get from the description to the API."
		case lowC && lowT:
			return fmt.Sprintf("Both docs and tests are thin — most %s are present only as contract entries.", noun)
		default:
			return fmt.Sprintf("Coverage is uneven across the configured surfaces; see the breakdown below for where each %s gap clusters.", noun)
		}
	}
	highC := concept >= 60
	highT := test >= 60
	highU := usage >= 60
	lowT := test < 25
	lowU := usage < 25
	switch {
	case highC && lowT && lowU:
		return "Docs are nearly complete, but tests and usage barely exist — the behavior is written down, never made executable or runnable."
	case highC && lowT && !lowU:
		return "Docs and usage are present, but few tests verify what the docs claim."
	case highC && !lowT && lowU:
		return "Docs and tests carry the surface; usage is scarce — readers can learn and trust but not easily copy a working call."
	case highC && highT && highU:
		return "Docs, tests, and usage all land together — the contract is well-bridged."
	case !highC && !highT && !highU:
		return fmt.Sprintf("All three surfaces are thin — most %s are present only as contract entries.", noun)
	default:
		return fmt.Sprintf("Coverage is uneven across surfaces; see the breakdown below for where each %s gap clusters.", noun)
	}
}

// ============================================================
// Masthead hero picker — defaults to drift counts, otherwise top
// non-bridged fix-group sizes.
// ============================================================

func pickHeroes(r *ReportData) (Hero, Hero) {
	var domain, time int
	for _, m := range r.Methods {
		if m.Drift == "domain" {
			domain++
		}
		if m.Drift == "time" {
			time++
		}
	}
	candidates := []Hero{
		{Label: fmt.Sprintf("%d", domain), Count: domain,
			Say: fmt.Sprintf("%s whose docs describe the behavior in words that never name the actual call — a reader can't get from the concept to the API.", capitalize(r.NounPlural))},
		{Label: fmt.Sprintf("%d", time), Count: time,
			Say: fmt.Sprintf("%s whose docs or tests reference a signature that has since changed — the explanation no longer matches the code.", capitalize(r.NounPlural))},
	}
	// Fallbacks if no LLM/drift findings exist.
	if domain == 0 && time == 0 {
		untested, noexample := 0, 0
		for _, m := range r.Methods {
			if m.Drift != "none" || m.Bridged {
				continue
			}
			if m.NeedsTest {
				untested++
			} else if m.NeedsExample {
				noexample++
			}
		}
		candidates[0] = Hero{Label: fmt.Sprintf("%d", untested), Count: untested,
			Say: fmt.Sprintf("Documented %s with no test — the prose isn't verified by anything that runs.", r.NounPlural)}
		candidates[1] = Hero{Label: fmt.Sprintf("%d", noexample), Count: noexample,
			Say: fmt.Sprintf("Documented and tested %s with no runnable example — readers can't copy a working call.", r.NounPlural)}
	}
	return candidates[0], candidates[1]
}

// ============================================================
// Helpers.
// ============================================================

// nounsFor was the legacy noun fallback table for ecosystems that
// hadn't yet been registered as full EcosystemViews. It's been
// retired now that cli + fidl + proto cover the shapes we render, and
// defaultEcosystemView gives unknown ids a defensible
// "element/elements" rendering instead of guessing.

func pct(n, total int) int {
	if total == 0 {
		return 0
	}
	v := (n*100 + total/2) / total
	if v > 100 {
		v = 100
	}
	return v
}

// numWord formats a count as the word "Zero" when n == 0, otherwise
// as the bare digits. Used in headlines and italic subheadline
// strings per the house style: a "0" in those positions reads as a
// missing number, whereas "Zero" reads as a deliberate finding.
// Sentence-mid uses can call strings.ToLower on the result if a
// lowercased "zero" reads more naturally.
func numWord(n int) string {
	if n == 0 {
		return "Zero"
	}
	return fmt.Sprintf("%d", n)
}

func shortName(id string) string {
	if i := strings.IndexAny(id, "/"); i > 0 {
		return id[i+1:]
	}
	return id
}

func firstString(v any) string {
	s, _ := v.(string)
	return s
}

func numAsInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// deprecationFromConstraints reads a versionConstraints slice (as
// returned by protojson) and returns a Deprecation, or nil if no
// deprecation/removal signal is present.
func deprecationFromConstraints(v any) *Deprecation {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	for _, item := range arr {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		d := &Deprecation{
			AddedIn:      firstString(m["added"]),
			DeprecatedIn: firstString(m["deprecated"]),
			RemovedIn:    firstString(m["removed"]),
			Note:         firstString(m["note"]),
			Ecosystem:    firstString(m["ecosystem"]),
		}
		if d.IsDeprecated() || d.AddedIn != "" || d.Note != "" {
			return d
		}
	}
	return nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func locString(v any) string {
	path, line := locPathLine(v)
	if path == "" {
		return ""
	}
	return formatLoc(path, line)
}

// locPathLine returns the raw (path, line) for a SourceLocation-shaped
// map. Used by callers that need to build URLs from the same data
// locString formats for display.
func locPathLine(v any) (string, int) {
	m, _ := v.(map[string]any)
	if m == nil {
		return "", 0
	}
	path := firstString(m["path"])
	if path == "" {
		return "", 0
	}
	return path, numAsInt(m["line"])
}

// locURL pulls an adapter-supplied canonical URL off a SourceLocation
// map (set when the contract adapter knows the user-facing URL is
// different from the on-disk path — e.g. cobra's url_base). Empty
// when the location doesn't carry one; callers fall back to
// expandSourceURL.
func locURL(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	return firstString(m["url"])
}
