package cli

import (
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// RunMeta carries run-level provenance rendered into the index masthead
// and footer. BaseURL is empty in v1 (relative links); Stage 4 threads
// an absolute base for published runs.
type RunMeta struct {
	Repo         string
	Commit       string
	SheafVersion string
	BaseURL      string
	GeneratedAt  time.Time
	// ContentsPrefix is prepended to every entry href so the index (which
	// sits beside the contents dir as <root>.html) links into it, e.g.
	// "<root>-contents/". Empty when the index lives among the reports.
	ContentsPrefix string
}

// surfaceBullet is one of the four per-surface bullets on a domain row:
// the domain's coverage for the surface (Pct). Cond marks the conditional
// workflow surface, which is shown but never gates the bridge. Worst is
// set on the single bullet with the lowest Pct among the non-conditional
// surfaces of this domain — used to typographically elevate the per-card
// finding so each card has a "figure" the eye lands on at scroll-scale.
// Worst is only set when there is actual variance (min < max); if all the
// non-conditional surfaces are tied, no bullet is elevated.
type surfaceBullet struct {
	Key   string
	Label string
	Pct   int
	Cond  bool
	Worst bool
}

// libRow is a sub-library line under a domain.
type libRow struct {
	Name     string
	Names    []string // Name split on commas — rendered as individual chips
	Href     string
	Elements int
	DotDocs  bool
	DotTests bool
	DotUsage bool
	Failed   bool
	Err      string
}

type domainView struct {
	Name          string
	Href          string // report link when this card is a single domain
	LibCount      int    // total libraries (chips) across this card's entries
	Elements      int
	Orphans       int // elements with no doc/test/example (Completeness[0])
	OrphanPct     int // Orphans as a percentage of Elements
	Libraries     int
	LagP50        int    // domain median doc lag in days (across libs in this domain)
	LagPairs      int    // cross-file (element, doc) pairs counted in this domain
	Inline        int    // same-file (in-source) doc-comment references in this domain
	HasDocs       bool   // any docs at all on this domain (cross-file or inline)
	DriftText     string // gated drift annotation, e.g. "drift 489d · 3 pairs" or "drift —"
	DriftHasPairs bool   // concept-doc pairs exist → show provenance dot + full weight
	ShowDrift     bool   // render the drift annotation line at all
	MinSurfacePct int    // lowest Pct across non-conditional surfaces; drives sort
	Bullets       []surfaceBullet
	Libs          []libRow
}

type indexView struct {
	GeneratedStr  string
	Commit        string
	SheafVersion  string
	Repo          string
	Home          string
	Icon          template.URL
	TotalElements int
	TotalLibs     int
	TotalDomains  int
	Bridged3      int
	BridgedPct    int
	Orphans       int
	OrphanPct     int
	Medians       [3]int
	ShowTests     bool
	ShowUsage     bool
	Domains       []domainView
	OkCount       int
	FailCount     int
}

// surfaceDefs fixes the order + labels of the three surfaces the index
// renders. Cond is preserved as a field for future expansion (per-domain
// conditional surfaces) but no current surface uses it — examples +
// workflows are merged into "usage" so the conditional-workflow split
// no longer surfaces at the index level. The split still lives in the
// schema and in the per-report renderer; this is a presentation choice.
var surfaceDefs = []struct {
	Key, Label string
	Cond       bool
}{
	{"d", "reference", false},
	{"t", "tests", false},
	{"u", "usage", false},
}

func ipct(n, total int) int {
	if total <= 0 {
		return 0
	}
	return int(float64(n)*100.0/float64(total) + 0.5)
}

// quantile returns the q-quantile (0..1) of an already-sorted slice,
// rounded to the nearest int. Linear interpolation between ranks.
func quantile(sorted []float64, q float64) int {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return int(sorted[0] + 0.5)
	}
	pos := q * float64(n-1)
	lo := int(pos)
	frac := pos - float64(lo)
	if lo+1 < n {
		return int(sorted[lo]*(1-frac) + sorted[lo+1]*frac + 0.5)
	}
	return int(sorted[lo] + 0.5)
}

// surfaceCovered returns the covered-element count for surface key k on
// one library's stats. "u" (usage) is the union of examples and
// workflows, computed once at LibraryStats time so it never
// double-counts elements covered by both.
func surfaceCovered(s anyStats, k string) int {
	switch k {
	case "d":
		return s.Docs
	case "t":
		return s.Tests
	case "u":
		return s.Usage
	}
	return 0
}

// anyStats decouples this file from the scanner package's concrete type
// name; it matches scanner.LibraryStats field-for-field via EntryResult.
type anyStats struct {
	Total, Docs, Tests, Examples, Workflows, Usage int
	Completeness                                   [4]int
}

func statsOf(r EntryResult) anyStats {
	return anyStats{
		Total:        r.Stats.Total,
		Docs:         r.Stats.Docs,
		Tests:        r.Stats.Tests,
		Examples:     r.Stats.Examples,
		Workflows:    r.Stats.Workflows,
		Usage:        r.Stats.Usage,
		Completeness: r.Stats.Completeness,
	}
}

// splitLibs splits a manifest entry's comma-separated library field into
// trimmed individual library names, each rendered as its own chip in the
// index. Empty segments are dropped.
func splitLibs(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func buildIndexView(results []EntryResult, meta RunMeta) indexView {
	// Which per-surface bullets to render is config-driven: a surface shows
	// only when some entry's surfaces_required declares it (docs.reference /
	// docs.concepts -> docs, tests -> tests, examples / workflows -> usage).
	// When no entry declares any (older configs), all three show (v0).
	shown := surfaceDefs
	showTests, showUsage := true, true
	{
		want := map[string]bool{}
		declared := false
		for _, r := range results {
			for _, s := range r.SurfacesRequired {
				declared = true
				switch strings.ToLower(s) {
				case "docs.reference", "reference", "docs.concepts", "docs.concept", "concept", "concepts":
					want["d"] = true
				case "tests", "test":
					want["t"] = true
				case "examples", "example", "workflows", "workflow":
					want["u"] = true
				}
			}
		}
		if declared {
			filtered := surfaceDefs[:0:0]
			for _, sd := range surfaceDefs {
				if want[sd.Key] {
					filtered = append(filtered, sd)
				}
			}
			if len(filtered) > 0 {
				shown = filtered
				showTests, showUsage = want["t"], want["u"]
			}
		}
	}

	// Per-surface per-library coverage %, for run-wide median + IQR.
	perSurface := map[string][]float64{}
	ok := 0
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		ok++
		s := statsOf(r)
		for _, sd := range shown {
			perSurface[sd.Key] = append(perSurface[sd.Key], float64(ipct(surfaceCovered(s, sd.Key), s.Total)))
		}
	}
	median := map[string]int{}
	for _, sd := range shown {
		v := perSurface[sd.Key]
		sort.Float64s(v)
		median[sd.Key] = quantile(v, 0.5)
	}

	// Group by domain, first-appearance order so the manifest author
	// controls ordering.
	var order []string
	byDomain := map[string][]EntryResult{}
	for _, r := range results {
		g := r.Group
		if g == "" {
			g = "Ungrouped"
		}
		if _, seen := byDomain[g]; !seen {
			order = append(order, g)
		}
		byDomain[g] = append(byDomain[g], r)
	}

	view := indexView{
		GeneratedStr: meta.GeneratedAt.Format("2006-01-02 15:04") + " UTC",
		Commit:       meta.Commit,
		SheafVersion: meta.SheafVersion,
		Repo:         meta.Repo,
		Home:         "#",
		Icon:         scanner.LogoDataURI(),
		Medians:      [3]int{median["d"], median["t"], median["u"]},
		ShowTests:    showTests,
		ShowUsage:    showUsage,
	}
	if meta.BaseURL != "" {
		view.Home = meta.BaseURL
	}

	for _, g := range order {
		entries := byDomain[g]
		dv := domainView{Name: g, Libraries: 0}
		var comp [4]int
		sumCovered := map[string]int{}
		domTotal := 0
		var domLags []int
		for _, r := range entries {
			lr := libRow{Name: r.Entry.GetLibrary(), Names: splitLibs(r.Entry.GetLibrary()), Href: joinBase(meta.BaseURL, meta.ContentsPrefix+r.OutputRel)}
			if r.Err != nil {
				lr.Failed = true
				lr.Err = r.Err.Error()
				dv.Libs = append(dv.Libs, lr)
				continue
			}
			dv.Libraries++
			s := statsOf(r)
			lr.Elements = s.Total
			lr.DotDocs = s.Docs*2 >= s.Total && s.Total > 0
			lr.DotTests = s.Tests*2 >= s.Total && s.Total > 0
			lr.DotUsage = s.Usage*2 >= s.Total && s.Total > 0
			dv.Libs = append(dv.Libs, lr)
			domTotal += s.Total
			for i := range comp {
				comp[i] += s.Completeness[i]
			}
			for _, sd := range surfaceDefs {
				sumCovered[sd.Key] += surfaceCovered(s, sd.Key)
			}
			domLags = append(domLags, r.Stats.Lag.Sorted...)
			dv.Inline += r.Stats.Lag.Inline
		}
		dv.HasDocs = len(domLags) > 0 || dv.Inline > 0
		dv.Elements = domTotal
		dv.Orphans = comp[0]
		dv.OrphanPct = ipct(comp[0], domTotal)
		sort.Ints(domLags)
		dv.LagP50 = sortedIntPercentile(domLags, 50)
		dv.LagPairs = len(domLags)
		// Drift annotation (gated, annotation-weight): median days the
		// cross-file concept docs trail the code. Inline reference comments
		// are colocated and excluded, so a domain with reference docs but no
		// cross-file pairs honestly shows an em-dash, not a zero; 0d (same-day)
		// is a real, good result shown distinctly. The median is mechanical
		// (git commit timestamps); no LLM is involved.
		dv.ShowDrift = dv.Elements > 0
		if dv.LagPairs > 0 {
			unit := "pairs"
			if dv.LagPairs == 1 {
				unit = "pair"
			}
			dv.DriftText = fmt.Sprintf("drift %dd · %d %s", dv.LagP50, dv.LagPairs, unit)
			dv.DriftHasPairs = true
		} else {
			dv.DriftText = "drift —"
		}
		for _, sd := range shown {
			dv.Bullets = append(dv.Bullets, surfaceBullet{
				Key:   sd.Key,
				Label: sd.Label,
				Pct:   ipct(sumCovered[sd.Key], domTotal),
				Cond:  sd.Cond,
			})
		}
		// Pick the lowest Pct among non-conditional surfaces to elevate as
		// the per-card "figure," and remember it as MinSurfacePct so we can
		// sort domains worst-first. We only set Worst when there is real
		// variance — if every non-conditional surface ties, no bullet wins
		// (a tied row is its own honest signal; we don't manufacture a
		// distinction by typesetting the first column louder than the rest).
		minPct, maxPct, worstKey := 101, -1, ""
		for _, b := range dv.Bullets {
			if b.Cond {
				continue
			}
			if b.Pct < minPct {
				minPct = b.Pct
				worstKey = b.Key
			}
			if b.Pct > maxPct {
				maxPct = b.Pct
			}
		}
		if minPct < maxPct {
			for i := range dv.Bullets {
				if dv.Bullets[i].Key == worstKey {
					dv.Bullets[i].Worst = true
				}
			}
		}
		// minPct stays at 101 only if every surface is conditional, which
		// surfaceDefs precludes — but guard anyway so a misconfigured
		// surfaceDefs sorts those domains last instead of crashing.
		if minPct > 100 {
			minPct = 100
		}
		dv.MinSurfacePct = minPct
		for _, lr := range dv.Libs {
			dv.LibCount += len(lr.Names)
		}
		// When a card is a single domain (one entry), the domain name links
		// straight to that domain's report. Multi-entry (coarse-group) cards
		// keep a plain title — the per-library chips carry the links.
		if len(entries) == 1 && entries[0].Err == nil {
			dv.Href = joinBase(meta.BaseURL, meta.ContentsPrefix+entries[0].OutputRel)
		}
		view.TotalElements += domTotal
		view.Bridged3 += comp[3]
		view.Orphans += comp[0]
		view.TotalLibs += dv.Libraries
		view.Domains = append(view.Domains, dv)
	}
	// Sort domains most-covered-first: the mean coverage across surfaces,
	// descending, so the best-covered domains lead. Stable so ties preserve
	// the manifest author's order (deterministic, reviewable).
	meanCover := func(d domainView) int {
		if len(d.Bullets) == 0 {
			return 0
		}
		sum := 0
		for _, b := range d.Bullets {
			sum += b.Pct
		}
		return sum / len(d.Bullets)
	}
	sort.SliceStable(view.Domains, func(i, j int) bool {
		return meanCover(view.Domains[i]) > meanCover(view.Domains[j])
	})
	view.TotalDomains = len(view.Domains)
	view.OkCount = ok
	view.FailCount = len(results) - ok
	view.BridgedPct = ipct(view.Bridged3, view.TotalElements)
	view.OrphanPct = ipct(view.Orphans, view.TotalElements)
	return view
}

// sortedIntPercentile returns the q-th percentile (0..100) of an
// already-sorted []int, rounded to the nearest int. Linear interpolation
// between adjacent ranks. Returns 0 for an empty slice.
func sortedIntPercentile(sorted []int, q int) int {
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

// gitShortCommit best-effort resolves the short HEAD of repo; "" on any
// failure (not a git repo, git missing, …).
func gitShortCommit(repo string) string {
	if repo == "" {
		repo = "."
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RenderIndex writes the per-domain coverage index (completeness strips +
// per-surface bullets + sub-library rows) to indexPath. meta carries run
// provenance and meta.ContentsPrefix, the relative path from indexPath to the
// dir holding the per-entry reports.
func RenderIndex(indexPath string, results []EntryResult, meta RunMeta) error {
	if meta.GeneratedAt.IsZero() {
		meta.GeneratedAt = time.Now().UTC()
	}
	view := buildIndexView(results, meta)
	f, err := os.Create(indexPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return indexTemplate.Execute(f, view)
}

var indexTemplate = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sheaf — Coverage Index</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Junge&family=Fraunces:ital,opt,wght@0,500;0,600;0,700;1,400;1,500&family=IBM+Plex+Sans:ital,wght@0,400;0,500;0,600;0,700;1,400&family=JetBrains+Mono:wght@400;500&display=swap">
<style>
:root{
  --paper:#faf6ee; --card:#fffdf8; --ink:#16201c; --ink-2:#4a554f; --ink-3:#7d877f;
  --line:#ece5d6; --line-2:#f3eee2;
  --green-700:#0a6b3f; --green-600:#0f9d58; --gap-700:#b32d1a;
  --surf-docs:#11b3c6; --surf-tests:#2a4a78; --surf-examples:#c79a3a; --surf-workflows:#0a7a85;
  --f-display:'Junge',Georgia,serif; --f-frame:'Fraunces',Georgia,serif; --f-score:'Fraunces',Georgia,serif;
  --f-body:'IBM Plex Sans',-apple-system,system-ui,sans-serif; --f-ui:'IBM Plex Sans',-apple-system,system-ui,sans-serif;
  --f-mono:'JetBrains Mono',ui-monospace,Menlo,monospace;
}
*{margin:0;padding:0;box-sizing:border-box;}
body{background:var(--paper);color:var(--ink);font-family:var(--f-body);line-height:1.5;min-height:100vh;}
.topnav{position:sticky;top:0;z-index:50;background:rgba(250,246,238,.93);border-bottom:1px solid var(--line);backdrop-filter:saturate(1.2) blur(6px);}
.topnav-in{max-width:1080px;margin:0 auto;padding:10px 32px;display:flex;align-items:center;gap:10px;flex-wrap:wrap;}
.topnav-logo .mark{width:24px;height:24px;border-radius:6px;display:block;background:var(--green-600);}
.topnav-brand{font-family:var(--f-ui);font-weight:700;font-size:12px;letter-spacing:.14em;text-transform:uppercase;color:var(--green-700);}
.topnav-logo-img{width:24px;height:24px;border-radius:6px;display:block;flex:0 0 24px;}
.topnav-in .mark{width:24px;height:24px;border-radius:6px;display:block;background:var(--green-600);flex:0 0 24px;}
.topnav-sha,.topnav-sep,.topnav-prov{font-family:var(--f-mono);font-size:11.5px;color:var(--ink-3);}
.wrap{max-width:1080px;margin:0 auto;padding:0 32px 100px;}
.masthead{padding:46px 0 8px;}
.index-rule{border-top:1px solid var(--line);margin:14px 0 22px;}
.meta{font-family:var(--f-mono);font-size:11px;color:var(--ink-3);margin-bottom:12px;}
.meta b{color:var(--green-700);font-weight:500;}
.title{font-family:var(--f-display);font-weight:400;font-size:40px;letter-spacing:-.01em;line-height:1.08;margin-bottom:14px;}
.title .lib{font-family:var(--f-mono);font-weight:500;font-size:0.45em;color:var(--ink-2);background:#edeae1;padding:3px 11px;border-radius:8px;vertical-align:middle;}
.masthead-insight{font-family:var(--f-frame);font-style:italic;font-size:17px;color:var(--ink-2);line-height:1.5;margin-bottom:14px;max-width:76ch;}
.sh{display:flex;align-items:baseline;gap:12px;border-top:1px solid var(--line);padding-top:38px;margin-top:30px;margin-bottom:6px;}
.sh h2{font-family:var(--f-display);font-weight:400;font-size:26px;margin:0 0 16px 0;}
.dom{border:1px solid var(--line);border-radius:12px;background:var(--card);padding:17px 20px;margin-bottom:12px;}
.dom-head{display:grid;grid-template-columns:minmax(290px,1fr) 600px;gap:30px;align-items:start;}
.dom-name{font-family:var(--f-display);font-weight:400;font-size:21px;line-height:1.15;}
.dom-link{color:inherit;text-decoration:none;}
.dom-link:hover{text-decoration:underline;}
.dom-sub{font-family:var(--f-ui);font-size:12px;color:var(--ink-3);margin-top:4px;font-feature-settings:"tnum" 1;}
.bullets{display:grid;grid-template-columns:repeat(3,1fr);gap:18px;}
/* A lone bullet (e.g. a docs-only index) is right-aligned: placed in the
   rightmost of the three columns instead of the left. */
.bullets-right .blt{grid-column:3;}
/* min-height locks the row to the worst bullet's line-box, so when the
   .blt.worst variant bumps the percentage to 19px the bar below it stays
   at the same Y position as the other three bars on the card. Without
   this, the elevated row grows taller and the bar slides down. */
.blt-top{display:flex;align-items:baseline;justify-content:space-between;gap:6px;margin-bottom:6px;min-height:22px;}
.blt-val{line-height:1;}
.blt.worst .blt-val{line-height:1;}
.blt-key{font-family:var(--f-ui);font-size:9.5px;font-weight:700;letter-spacing:.09em;text-transform:uppercase;display:inline-flex;align-items:center;gap:5px;}
.blt-key .kd{width:7px;height:7px;border-radius:50%;display:inline-block;}
/* Usage reuses the examples mustard — examples is the more recognizable
   of the two underlying surfaces at index scale, and a single color is
   cleaner than blending the workflows teal in. */
.kd.d{background:var(--surf-docs);}.kd.t{background:var(--surf-tests);}.kd.u{background:var(--surf-examples);}
.kx.d{color:var(--surf-docs);}.kx.t{color:var(--surf-tests);}.kx.u{color:var(--surf-examples);}
.blt-val{font-family:var(--f-ui);font-weight:600;font-size:9.5px;color:var(--ink);}
.blt-val .u{font-family:var(--f-ui);font-size:9.5px;color:var(--ink-3);font-weight:600;}
.blt-track{position:relative;height:10px;background:var(--line-2);border-radius:2px;}
.blt-fill{position:absolute;left:0;top:0;bottom:0;border-radius:2px;}
.fill-d{background:var(--surf-docs);}.fill-t{background:var(--surf-tests);}.fill-u{background:var(--surf-examples);}
/* Worst — the lowest-coverage non-conditional surface on this card. The
   typographic step is the only "figure" we add: same family, one weight
   heavier, one step larger. This is the single elevation the design crew
   recommended; it must not read as a verdict. */
.blt.worst .blt-val{font-size:19px;font-weight:700;}
.sublibs{margin-top:14px;display:flex;flex-direction:column;}
/* Each row is one domain (one report); its member libraries render as
   individual chips that all link to that domain's report. A faint rule
   separates adjacent domains, since a chip set can wrap over several lines. */
.sub{padding:9px 0;border-top:1px solid var(--line-2);}
.sub:first-child{border-top:none;}
.sub-name{font-family:var(--f-mono);font-size:12px;color:var(--ink);}
.sub.failed .sub-name{color:var(--gap-700);}
.chips{display:flex;flex-wrap:wrap;gap:5px;}
.chip{font-family:var(--f-mono);font-size:11px;color:var(--ink-2);background:#edeae1;border:1px solid var(--line);padding:2px 8px;border-radius:2px;text-decoration:none;transition:background .12s ease,color .12s ease,border-color .12s ease;}
.chip:hover{background:var(--ink-2);color:var(--paper);border-color:var(--ink-2);}
.foot{margin-top:46px;padding-top:18px;border-top:1px solid var(--line);font-family:var(--f-mono);font-size:11px;color:var(--ink-3);line-height:1.7;}
.foot a{color:var(--ink-2);} .foot b{color:var(--ink-2);font-weight:500;}
.dom-lag{font-weight:600;color:var(--ink-2);}
.dom-lag-inline{font-family:var(--f-frame);font-style:italic;font-weight:500;cursor:help;}
/* Drift — gated, annotation-weight: its own restrained line under the
   REFERENCE bar (elevated out of the sub-line, but quiet — no bar, no
   chart). Same 9.5px as the bar's label + value so the right rail reads
   as one uniform block. The em-dash (.none) is the honest "no concept-doc
   pairs" cell, distinct from a 0d that means same-day. */
.dom-drift{text-align:right;margin-top:9px;font-family:var(--f-ui);font-size:9.5px;font-weight:400;letter-spacing:.01em;color:var(--ink-3);font-feature-settings:"tnum" 1;}
.dom-drift.none{opacity:.55;}
</style>
</head>
<body>
<div class="topnav"><div class="topnav-in">{{if .Icon}}<img class="topnav-logo-img" src="{{.Icon}}" alt="">{{else}}<span class="mark"></span>{{end}}<span class="topnav-brand">sheaf</span>{{if .Commit}}<span class="topnav-sha">sha:{{.Commit}}</span>{{end}}<span class="topnav-sep">·</span><span class="topnav-prov">scanned {{.GeneratedStr}}</span>{{if .Repo}}<span class="topnav-sep">·</span><span class="topnav-prov">{{.Repo}}</span>{{end}}<span class="topnav-sep">·</span><span class="topnav-prov">{{.OkCount}} reports rendered{{if .FailCount}}, {{.FailCount}} failed{{end}}</span></div></div>
<div class="wrap">
  <div class="masthead">
    <div class="title">Coverage Index{{if .Repo}} <span class="lib">{{.Repo}}</span>{{end}}</div>
  </div>
  <div class="index-rule"></div>
{{range .Domains}}
  <div class="dom">
    <div class="dom-head">
      <div>
        <div class="dom-name">{{if .Href}}<a class="dom-link" href="{{.Href}}">{{.Name}}</a>{{else}}{{.Name}}{{end}}</div>
        <div class="dom-sub">{{.LibCount}} libraries&nbsp;·&nbsp;{{.Elements}} elements</div>
      </div>
      <div class="dom-right">
        <div class="bullets{{if eq (len .Bullets) 1}} bullets-right{{end}}">
{{range .Bullets}}        <div class="blt{{if .Cond}} cond{{end}}{{if .Worst}} worst{{end}}"><div class="blt-top"><span class="blt-key">{{.Label}}{{if .Cond}} (cond.){{end}}</span><span class="blt-val">{{.Pct}}<span class="u">%</span></span></div><div class="blt-track"><div class="blt-fill fill-{{.Key}}" style="width:{{.Pct}}%"></div></div></div>
{{end}}        </div>
{{if .ShowDrift}}        <div class="dom-drift{{if not .DriftHasPairs}} none{{end}}">{{.DriftText}}</div>
{{end}}      </div>
    </div>
    <div class="sublibs">
{{range .Libs}}      <div class="sub{{if .Failed}} failed{{end}}">{{if .Failed}}<span class="sub-name" title="{{.Err}}">{{.Name}} — failed</span>{{else}}<div class="chips">{{$href := .Href}}{{range .Names}}<a class="chip" href="{{$href}}">{{.}}</a>{{end}}</div>{{end}}</div>
{{end}}    </div>
  </div>
{{end}}
  <div class="foot">
    run medians — reference {{index .Medians 0}}%{{if .ShowTests}} · tests {{index .Medians 1}}%{{end}}{{if .ShowUsage}} · usage {{index .Medians 2}}%{{end}}
  </div>
</div>
</body>
</html>
`))
