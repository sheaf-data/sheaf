package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// Run executes the deterministic verification battery against a snapshot
// and returns the assembled Report. Disk-oracle checks (TP/FP/FN sampling,
// doc-URL resolution, authoritative element count) run only when
// opts.DiskChecks is set and a RepoRoot is available; otherwise the report
// carries an explicit caveat that those checks were skipped.
func Run(opts Options) (*Report, error) {
	snap, err := loadOrBuildSnapshot(opts)
	if err != nil {
		return nil, err
	}
	return Analyze(snap, opts), nil
}

// loadOrBuildSnapshot resolves the snapshot to verify: a pre-built JSON
// (--from-snapshot), or — for the one-shot path — an in-process scan from a
// config (--config + --repo + --library), so verify can build and check in a
// single step without a separate `sheaf snapshot`.
func loadOrBuildSnapshot(opts Options) (*scanner.Snapshot, error) {
	if opts.SnapshotPath != "" {
		return loadSnapshot(opts.SnapshotPath)
	}
	if opts.ConfigPath != "" {
		if opts.Library == "" {
			return nil, fmt.Errorf("verify: the --config one-shot requires --library")
		}
		// Auto-locate the source map next to the config (the convention the
		// example configs follow); an empty result lets BuildSnapshot fall
		// back to the repo root or run without categorization.
		rules := siblingRules(opts.ConfigPath)
		snap, err := scanner.BuildSnapshot(context.Background(), opts.ConfigPath, opts.RepoRoot, opts.Library, "", rules)
		if err != nil {
			return nil, fmt.Errorf("verify: build snapshot from config: %w", err)
		}
		return snap, nil
	}
	return nil, fmt.Errorf("verify: no snapshot source — pass --from-snapshot or --config")
}

// siblingRules returns categorization-rules.textproto next to the config when
// it exists, else "" (BuildSnapshot then resolves against the repo root or
// runs uncategorized). Mirrors validate.py's rules auto-location.
func siblingRules(configPath string) string {
	cand := filepath.Join(filepath.Dir(configPath), "categorization-rules.textproto")
	if _, err := os.Stat(cand); err == nil {
		return cand
	}
	return ""
}

// Analyze runs the verification battery against an already-loaded snapshot
// and returns the assembled Report. Exposed separately from Run so callers
// (and tests) can verify an in-memory snapshot without a file round-trip.
func Analyze(snap *scanner.Snapshot, opts Options) *Report {
	if opts.Threshold <= 0 {
		opts.Threshold = 0.15
	}

	// BuildReportWithOptions produces the exact numbers the HTML report
	// shows. Verifying against it (rather than re-deriving a parallel set)
	// guarantees there is no gap between "what verify checked" and "what
	// the team will see".
	rd := scanner.BuildReportWithOptions(snap, opts.Ecosystem, "", "HEAD", "", opts.RepoRoot, "", "")

	rep := &Report{
		Library:      snap.Library,
		Ecosystem:    opts.Ecosystem,
		SnapshotPath: opts.SnapshotPath,
		RepoRoot:     opts.RepoRoot,
		Threshold:    opts.Threshold,
		ElementCount: rd.Total,
	}

	profByID := indexProfiles(snap)

	reconcileTests(rep, rd, profByID)
	decomposeTiers(rep, rd, opts.Threshold)
	detectSmearing(rep, rd, profByID)
	detectContamination(rep, rd, profByID)
	detectNameCollision(rep, rd, profByID)
	detectNoSourceMap(rep, rd, snap)

	if !opts.DiskChecks || opts.RepoRoot == "" {
		rep.add(Finding{
			Category: CatUnverifiable,
			Severity: SeverityWarn,
			Title:    "Repo-grep oracle not run — claims reconciled to the snapshot, not validated on disk",
			Detail:   "The structural checks (reconciliation, per-tier decomposition, smearing, contamination, name collisions) ran against the snapshot. The repo-grep oracle did NOT run: attribution true/false-positive sampling, the false-negative search, doc-URL resolution, and the authoritative element-count cross-check. Whether each attributed claim is actually supported on disk — and whether real evidence was missed — is unverified.",
			Fix:      "Re-run with disk checks enabled and a --repo path to validate the claims against the source tree.",
		})
		rep.Caveats = append(rep.Caveats,
			"Disk oracle disabled: coverage figures are reconciled to the snapshot's join data only, not validated against the source tree.")
	} else {
		rep.DiskVerified = true
		diskOracle(rep, rd, profByID, opts)
		// Doc-URL resolution is a separate, opt-in network check: gated on
		// --check-urls and run independently of diskOracle so a missing git
		// (which skips the grep oracle) does not suppress it, and vice versa.
		if opts.CheckURLs {
			checkDocURLs(rep, rd, profByID, opts, defaultDocURLClient())
		}
		// Ground-truth element-count cross-check (compare-in-Go; the
		// authoritative count is the agent's to compute and pass in).
		checkGroundTruth(rep, opts)
	}

	// Emit the attribution-precision sample (snapshot-derived; needs no disk
	// or network). It surfaces only in --json output for an agent to verdict;
	// the human ledger ignores it.
	sampleAssertions(rep, snap.Library, rd, profByID, opts)

	rep.finalize()
	return rep
}

func loadSnapshot(path string) (*scanner.Snapshot, error) {
	if path == "" {
		return nil, fmt.Errorf("verify: no snapshot path given")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("verify: read snapshot: %w", err)
	}
	var snap scanner.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("verify: parse snapshot %s: %w", path, err)
	}
	return &snap, nil
}

func indexProfiles(snap *scanner.Snapshot) map[string]map[string]any {
	out := make(map[string]map[string]any, len(snap.Profiles))
	for _, p := range snap.Profiles {
		id, _ := p["elementId"].(string)
		if id == "" {
			id, _ = p["element_id"].(string)
		}
		if id != "" {
			out[id] = p
		}
	}
	return out
}

// detTestBuckets are the deterministic test buckets — everything except
// llm_inferred, which the trusted coverage count deliberately excludes
// (the "moat"). Mirrors scanner.countTests so any divergence verify
// reports is a real inconsistency, not a counting difference.
var detTestBuckets = []string{"unit", "integration", "e2e", "ctf", "performance", "fuzz", "golden"}

func testRefs(prof map[string]any) []map[string]any {
	var out []map[string]any
	if prof == nil {
		return out
	}
	t, _ := prof["tests"].(map[string]any)
	if t == nil {
		return out
	}
	for _, k := range detTestBuckets {
		if arr, ok := t[k].([]any); ok {
			for _, it := range arr {
				if m, ok := it.(map[string]any); ok {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// reconcileTests independently recomputes "elements with ≥1 deterministic
// test ref" from the raw profiles and cross-checks it against the report's
// pooled TestCount. It reuses the renderer's per-element Removed flag
// (structural classification, not the figure under test) but verifies the
// tested boolean itself from scratch. A mismatch means a headline that
// cannot be reproduced from the join data — the bug, by definition.
func reconcileTests(rep *Report, rd *scanner.ReportData, profByID map[string]map[string]any) {
	rawTested, mismatches := 0, 0
	var sample string
	for i := range rd.Methods {
		m := rd.Methods[i]
		if m.Removed {
			continue
		}
		raw := len(testRefs(profByID[m.Name]))
		if (raw > 0) != (m.Test > 0) {
			mismatches++
			if sample == "" {
				sample = fmt.Sprintf("%s — report: %d test refs, snapshot join: %d", m.Name, m.Test, raw)
			}
		}
		if raw > 0 {
			rawTested++
		}
	}
	if rawTested != rd.TestCount || mismatches > 0 {
		rep.add(Finding{
			Category: CatReconcile,
			Severity: SeverityError,
			Surface:  "tests",
			Title:    "Pooled tested count does not reproduce from the join data",
			Detail: fmt.Sprintf("Report shows %d tested of %d; an independent recompute from the snapshot's deterministic test refs found %d tested, with %d per-element mismatch(es).",
				rd.TestCount, rd.Total, rawTested, mismatches),
			Expected: strconv.Itoa(rawTested) + " tested",
			Actual:   strconv.Itoa(rd.TestCount) + " tested",
			Evidence: nonEmpty(sample),
			Fix:      "A headline that can't be reproduced from the join data is the bug — inspect the renderer's count path or the snapshot projection.",
		})
	}
}

// surf is one coverage surface's shown numerator/percent plus whether the
// report renders a tile for it.
type surf struct {
	name string
	show bool
	n    int
	pct  int
}

func decomposeTiers(rep *Report, rd *scanner.ReportData, threshold float64) {
	thr := int(threshold * 100)

	// Pooled surfaces — the headline numbers most prone to the
	// denominator effect. Always surfaced with their formula.
	pooled := []surf{
		{"docs.reference", rd.ShowConceptTile, rd.ConceptCount, rd.ConceptPct},
		{"tests", rd.ShowTestTile, rd.TestCount, rd.TestPct},
		{"examples", rd.ShowExampleTile, rd.ExampleCount, rd.ExamplePct},
		{"implementations", rd.ShowImplementationsTile && rd.HasImplementsSignal, rd.ImplementationsCount, rd.ImplementationsPct},
	}
	for _, s := range pooled {
		if s.show {
			emitMetric(rep, "pooled", "", s, rd.Total, thr)
		}
	}

	type tier struct {
		id, noun string
		total    int
		surfaces []surf
	}
	tiers := []tier{
		{"container", rd.ContainerNoun, rd.ContainerTotal, []surf{
			{"docs.reference", rd.ShowConceptTile, rd.ContainerConceptN, rd.ContainerConceptPct},
			{"tests", rd.ShowTestTile, rd.ContainerTestN, rd.ContainerTestPct},
			{"examples", rd.ShowExampleTile, rd.ContainerExampleN, rd.ContainerExamplePct},
			{"implementations", rd.ShowImplementationsTile && rd.HasImplementsSignal, rd.ContainerImplementationsN, rd.ContainerImplementationsPct},
		}},
		{"primary", rd.PrimaryNoun, rd.PrimaryTotal, []surf{
			{"docs.reference", rd.ShowConceptTile, rd.PrimaryConceptN, rd.PrimaryConceptPct},
			{"tests", rd.ShowTestTile, rd.PrimaryTestN, rd.PrimaryTestPct},
			{"examples", rd.ShowExampleTile, rd.PrimaryExampleN, rd.PrimaryExamplePct},
			{"implementations", rd.ShowImplementationsTile && rd.HasImplementsSignal, rd.PrimaryImplementationsN, rd.PrimaryImplementationsPct},
		}},
		{"modifier", rd.ModifierNoun, rd.ModifierTotal, []surf{
			{"docs.reference", rd.ShowConceptTile, rd.ModifierConceptN, rd.ModifierConceptPct},
			{"tests", rd.ShowTestTile, rd.ModifierTestN, rd.ModifierTestPct},
			{"examples", rd.ShowExampleTile, rd.ModifierExampleN, rd.ModifierExamplePct},
			{"implementations", rd.ShowImplementationsTile && rd.HasImplementsSignal, rd.ModifierImplementationsN, rd.ModifierImplementationsPct},
		}},
	}
	for _, t := range tiers {
		if t.total == 0 {
			continue
		}
		for _, s := range t.surfaces {
			if s.show {
				emitMetric(rep, t.id, t.noun, s, t.total, thr)
			}
		}
	}

	explainDenominator(rep, rd, thr)
}

// emitMetric records one reconciled MetricCheck and raises the zero-surface
// / low-coverage / pct-mismatch findings that attach to it.
func emitMetric(rep *Report, tierID, tierNoun string, s surf, total, thr int) {
	if total == 0 {
		return
	}
	expected := pctRound(s.n, total)
	reproduced := abs(expected-s.pct) <= 1 // tolerate integer-rounding of ±1
	mc := MetricCheck{
		Name:        s.name,
		Tier:        tierID,
		TierNoun:    tierNoun,
		Numerator:   s.n,
		Denominator: total,
		Percent:     s.pct,
		Formula:     strconv.Itoa(s.n) + " ÷ " + strconv.Itoa(total),
		Reproduced:  reproduced,
		Flagged:     s.pct <= thr,
	}
	rep.Metrics = append(rep.Metrics, mc)

	if !reproduced {
		rep.add(Finding{
			Category: CatReconcile, Severity: SeverityError,
			Surface: s.name, Tier: tierID,
			Title:    fmt.Sprintf("%s shows %d%% but its numerator/denominator round to %d%%", label(tierID, s.name), s.pct, expected),
			Detail:   fmt.Sprintf("%d ÷ %d = %d%%, but the report displays %d%%. A percentage that disagrees with its own inputs cannot be trusted.", s.n, total, expected, s.pct),
			Expected: strconv.Itoa(expected) + "%",
			Actual:   strconv.Itoa(s.pct) + "%",
		})
		return
	}
	if s.pct == 0 {
		// A 0% surface is the highest-risk claim in a report, but pre-disk
		// the engine cannot yet know whether it is a real gap or a
		// format/wiring bug. Asserting "wrong" on an unverified suspicion
		// is itself the confident-false-negative failure mode, so this is a
		// high-priority WARNING. The disk oracle upgrades it to an error
		// only if it finds the evidence exists but was not attributed.
		rep.add(Finding{
			Category: CatZeroSurface, Severity: SeverityWarn,
			Surface: s.name, Tier: tierID,
			Title:  fmt.Sprintf("%s reads exactly 0%% — verify before showing", label(tierID, s.name)),
			Detail: "A surface at exactly 0% is, in past sheaf scans, far more often a format or wiring gap (the adapter can't parse the doc/test format, or the source map is missing) than real absence — and it is the claim a reviewer who knows the evidence exists discards the tool over. Confirm it against disk: if the evidence is there, this is a wrong number (the highest-risk kind); if not, it is an honest gap.",
			Actual: "0%",
			Fix:    "Open one source file for this surface on disk and confirm whether the evidence exists in a format the adapter doesn't read.",
		})
	} else if mc.Flagged {
		rep.add(Finding{
			Category: CatLowCoverage, Severity: SeverityWarn,
			Surface: s.name, Tier: tierID,
			Title:  fmt.Sprintf("%s is %d%% (≤ %d%% suspicion line)", label(tierID, s.name), s.pct, thr),
			Detail: "In past sheaf scans a suspiciously-low coverage figure was far more often a denominator effect or an unparsed format than a real gap. Validate against disk before trusting it.",
			Actual: strconv.Itoa(s.pct) + "%",
			Fix:    "Decompose the denominator per tier and open a disk sample of the 'uncovered' elements to confirm the absence is real.",
		})
	}
}

// explainDenominator raises an info finding when a pooled surface is
// flagged low but a constituent tier is healthy — the blended figure is a
// denominator artifact, not a coverage statement.
func explainDenominator(rep *Report, rd *scanner.ReportData, thr int) {
	type pair struct {
		surface                string
		show                   bool
		pooledPct              int
		primaryPct, primaryN   int
		modifierPct, modifierN int
	}
	pairs := []pair{
		{"tests", rd.ShowTestTile, rd.TestPct, rd.PrimaryTestPct, rd.PrimaryTestN, rd.ModifierTestPct, rd.ModifierTestN},
		{"docs.reference", rd.ShowConceptTile, rd.ConceptPct, rd.PrimaryConceptPct, rd.PrimaryConceptN, rd.ModifierConceptPct, rd.ModifierConceptN},
	}
	for _, p := range pairs {
		if !p.show || rd.ModifierTotal == 0 || rd.PrimaryTotal == 0 {
			continue
		}
		if p.pooledPct <= thr && p.primaryPct >= 2*thr && p.primaryPct-p.pooledPct >= thr {
			rep.add(Finding{
				Category: CatDenominator, Severity: SeverityInfo,
				Surface: p.surface,
				Title:   fmt.Sprintf("%s: pooled %d%% is a denominator effect, not a coverage gap", p.surface, p.pooledPct),
				Detail: fmt.Sprintf("The blended %d%% pools the %s tier (%d/%d = %d%%) with the %s tier (%d/%d = %d%%). The headline is dragged down by the larger, sparser modifier tier — read it per tier, not pooled.",
					p.pooledPct, rd.PrimaryNoun, p.primaryN, rd.PrimaryTotal, p.primaryPct,
					rd.ModifierNoun, p.modifierN, rd.ModifierTotal, p.modifierPct),
				Fix: "Show the per-tier figures; the pooled percentage misrepresents both tiers.",
			})
		}
	}
}

// detectSmearing flags an element that carries many test refs sourced from
// very few files — the signature of file-level attribution, where every
// element named in a file inherits all of that file's tests and per-element
// counts inflate 10–30×.
func detectSmearing(rep *Report, rd *scanner.ReportData, profByID map[string]map[string]any) {
	const (
		minRefs     = 8
		maxDistinct = 2
	)
	for i := range rd.Methods {
		m := rd.Methods[i]
		if m.Removed {
			continue
		}
		refs := testRefs(profByID[m.Name])
		if len(refs) < minRefs {
			continue
		}
		files := map[string]int{}
		for _, r := range refs {
			if p, _ := r["path"].(string); p != "" {
				files[p]++
			}
		}
		if len(files) == 0 || len(files) > maxDistinct {
			continue
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		ev := make([]string, 0, len(paths))
		for _, p := range paths {
			ev = append(ev, fmt.Sprintf("%s (%d refs)", p, files[p]))
		}
		rep.add(Finding{
			Category: CatSmearing, Severity: SeverityWarn,
			Element:  m.Name,
			Title:    fmt.Sprintf("%s shows %d tests from only %d file(s) — likely file-level smearing", m.Name, len(refs), len(files)),
			Detail:   "When tests are attributed at file granularity, every element named in a file inherits all of that file's tests, inflating per-element counts 10–30×. The true per-element count is probably far lower.",
			Evidence: ev,
			Fix:      "Confirm by opening a cited file and counting the call sites that actually exercise this element; if inflated, scope each test's contract refs to its own test-function byte range, not the whole file.",
		})
	}
}

func label(tier, name string) string {
	if tier == "" || tier == "pooled" {
		return name + " (pooled)"
	}
	return name + " (" + tier + " tier)"
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
