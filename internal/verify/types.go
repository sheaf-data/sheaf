// Package verify adversarially re-checks a sheaf scan's top-line numbers
// against the snapshot it was rendered from and the repository on disk —
// BEFORE the report is shown to anyone.
//
// It exists because a sheaf number is a join artifact, not a measurement:
// discovery → attribution → a fraction, and each stage has a
// characteristic way of lying:
//
//   - the denominator effect — a blended "11% documented" that is really
//     "100% of commands, 0% of flags" pooled into one denominator;
//   - a doc/test format the adapter can't parse rendering a surface as a
//     confident 0% when the docs plainly exist;
//   - a missing source map silently zeroing every docs.* surface;
//   - file-level test refs smeared across every sibling element, inflating
//     a per-element count 10–30×;
//   - name-token over-matching attributing unrelated tests to an element
//     whose local name is a common word.
//
// The first report a team sees is the whole trust game: a reviewer who
// spot-checks ten rows and finds two wrong discards the scan — and the
// methodology with it. verify is the rail that makes that first contact a
// confirmation, not a discovery. Every number it passes is reconciled to
// its formula and inputs; every number it cannot stand behind is surfaced
// as an explicit, honest unknown rather than rendered as fact.
package verify

// Severity ranks a Finding. Errors mean a shown number is wrong or could
// not be verified at all; warnings mean a number is suspicious and was
// flagged for validation; info is context (e.g. a benign denominator
// split worth explaining).
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warning"
	SeverityError Severity = "error"
)

// Category tags what kind of problem a Finding represents. These map 1:1
// to the failure modes mined from past sheaf scan reviews.
type Category string

const (
	CatReconcile     Category = "reconcile"          // a headline number does not reproduce from its inputs
	CatDenominator   Category = "denominator"        // a blended % hides a per-tier split that changes its meaning
	CatLowCoverage   Category = "low_coverage"       // a surface/tier is at or below the suspicion threshold
	CatZeroSurface   Category = "zero_surface"       // a surface reads exactly 0% (almost always format/wiring, not real absence)
	CatNoSourceMap   Category = "no_source_map"      // categorization/source map missing → docs.* silently read 0
	CatSmearing      Category = "test_smearing"      // file-level refs inflate an element's per-element test count
	CatNameCollision Category = "name_collision"     // attributions on a common single-word element name
	CatContamination Category = "glob_contamination" // refs sourced from vendored/worktree/build dirs
	CatGroundTruth   Category = "ground_truth"       // element count disagrees with an authoritative parser
	CatDocURL        Category = "doc_url"            // a generated doc URL does not resolve
	CatFalsePositive Category = "false_positive"     // an attributed claim is not supported on disk
	CatFalseNegative Category = "false_negative"     // disk has evidence the scan missed
	CatUnverifiable  Category = "unverifiable"       // verify could not check a claim; honest unknown
)

// Finding is one issue verify surfaces. Expected/Actual and Evidence make
// it auditable: a reader can reproduce the check without rerunning sheaf.
type Finding struct {
	Category Category `json:"category"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Element  string   `json:"element,omitempty"`
	Surface  string   `json:"surface,omitempty"`
	Tier     string   `json:"tier,omitempty"`
	Expected string   `json:"expected,omitempty"` // what verify computed / expected
	Actual   string   `json:"actual,omitempty"`   // what the report shows
	Evidence []string `json:"evidence,omitempty"` // path:line citations, URLs, commands
	Fix      string   `json:"fix,omitempty"`      // the concrete next step
}

// MetricCheck is one headline number reconciled to its numerator,
// denominator, and formula. Reproduced is true when verify independently
// recomputed the figure and it matched what the report shows. Flagged is
// true when the figure is at or below the low-coverage suspicion
// threshold and therefore must be validated against disk before trust.
type MetricCheck struct {
	Name        string `json:"name"`           // e.g. "tests", "docs.reference", "docs.concepts"
	Tier        string `json:"tier,omitempty"` // "pooled" | "container" | "primary" | "modifier"
	TierNoun    string `json:"tier_noun,omitempty"`
	Numerator   int    `json:"numerator"`
	Denominator int    `json:"denominator"`
	Percent     int    `json:"percent"`
	Formula     string `json:"formula"`
	Reproduced  bool   `json:"reproduced"`
	Flagged     bool   `json:"flagged"`
	Note        string `json:"note,omitempty"`
}

// Verdict is the one-word roll-up a caller (or a human skimming the
// ledger) reads first.
type Verdict string

const (
	VerdictTrustworthy Verdict = "trustworthy" // no errors, no warnings
	VerdictReview      Verdict = "review"      // warnings only — numbers flagged for validation
	VerdictBroken      Verdict = "broken"      // at least one number is wrong or unverifiable
)

// Report is the full verification result: the reconciled metrics, the
// findings, the honest caveats about what could not be checked, and the
// roll-up verdict. Serialized as verify.json; rendered as the trust ledger.
type Report struct {
	Library      string        `json:"library"`
	Ecosystem    string        `json:"ecosystem"`
	SnapshotPath string        `json:"snapshot_path,omitempty"`
	RepoRoot     string        `json:"repo_root,omitempty"`
	Threshold    float64       `json:"low_coverage_threshold"`
	ElementCount int           `json:"element_count"`
	DiskVerified bool          `json:"disk_verified"`
	Metrics      []MetricCheck `json:"metrics"`
	Findings     []Finding     `json:"findings"`
	Caveats      []string      `json:"caveats"`
	Verdict      Verdict       `json:"verdict"`
	Errors       int           `json:"errors"`
	Warnings     int           `json:"warnings"`
	// Assertions is a deterministic, bounded sample of attributed claims
	// (tested_by / documented_by) with verdict=null, for an agent to
	// adjudicate. Consumed by `sheaf verify summarize` to compute precision.
	Assertions []Assertion `json:"assertions,omitempty"`
}

// Options configure a verify run.
type Options struct {
	// SnapshotPath is a snapshot JSON produced by `sheaf snapshot`.
	// Exactly one of SnapshotPath or (ConfigPath+RepoRoot) is the source.
	SnapshotPath string
	// ConfigPath + RepoRoot drive an in-process scan+snapshot when no
	// pre-built snapshot is given.
	ConfigPath string
	RepoRoot   string
	Library    string
	// Ecosystem selects the masthead/tier shape. Empty lets the scanner
	// fall back to its default view.
	Ecosystem string
	// Threshold is the low-coverage suspicion line in [0,1]; any per-tier
	// or pooled coverage at or below it is flagged for validation.
	// Defaults to 0.15.
	Threshold float64
	// DiskChecks enables the on-disk oracle (TP/FP/FN sampling, doc-URL
	// resolution, ground-truth element count). Requires RepoRoot.
	DiskChecks bool
	// MaxDiskElements caps how many elements the disk oracle samples per
	// run so a huge corpus stays bounded. 0 uses a built-in default.
	MaxDiskElements int
	// CheckURLs enables doc-URL resolution: a bounded HTTP HEAD/GET sample
	// of published doc URLs to catch dead links (a slug/underscore
	// convention mismatch). Network is a side effect, so it is gated behind
	// both DiskChecks and this explicit opt-in; default off.
	CheckURLs bool
	// MaxAssertionElements caps how many elements are sampled into the
	// attribution-precision `assertions` array (0 uses a built-in default).
	// Snapshot-derived; needs no disk or network.
	MaxAssertionElements int
	// ExpectedElements, when non-nil, is the authoritative element count
	// (from a protoc descriptor, `fidlc --json`, or the `--help` tree,
	// computed by the agent) to cross-check Report.ElementCount against.
	// nil = not provided → an honest caveat rather than a finding.
	ExpectedElements *int
}

// add appends a finding.
func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

// finalize tallies severities and resolves the roll-up verdict. Call once
// after all checks have run.
func (r *Report) finalize() {
	r.Errors, r.Warnings = 0, 0
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityError:
			r.Errors++
		case SeverityWarn:
			r.Warnings++
		}
	}
	switch {
	case r.Errors > 0:
		r.Verdict = VerdictBroken
	case r.Warnings > 0:
		r.Verdict = VerdictReview
	default:
		r.Verdict = VerdictTrustworthy
	}
}

// pctRound returns round(100*n/total), matching the report's integer
// percentages. total==0 yields 0.
func pctRound(n, total int) int {
	if total <= 0 {
		return 0
	}
	return (100*n + total/2) / total
}
