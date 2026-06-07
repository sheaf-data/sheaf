package verify

import (
	"fmt"
	"strconv"
)

// checkGroundTruth cross-checks the scan's element count against an
// authoritative count — a protoc descriptor, `fidlc --json`, or the `--help`
// command tree — passed in via --expected-elements. The element count is the
// denominator of every percentage in the report: if sheaf finds 12 elements
// on a 200-method API, every downstream figure is meaningless.
//
// This is a provable check (two integers), so it is allowed to escalate to
// `error` when the gap is large and unambiguous — that is the one
// ground-truth case the honesty rail lets call a number wrong. A gap within a
// tiny rounding tolerance (e.g. one synthetic root element) is clean; a
// moderate gap is a warning. When no authoritative count is supplied it is an
// honest caveat, never a finding — keeping the tool-running on the agent and
// the compare in Go.
func checkGroundTruth(rep *Report, opts Options) {
	if opts.ExpectedElements == nil {
		rep.Caveats = append(rep.Caveats,
			"Element count not independently verified — pass --expected-elements from the authoritative parser (protoc descriptor, fidlc --json, or the --help command tree) to cross-check the denominator.")
		return
	}
	expected := *opts.ExpectedElements
	actual := rep.ElementCount
	diff := actual - expected
	if diff < 0 {
		diff = -diff
	}

	// A tiny tolerance (~1%, floor 1) absorbs a single synthetic/root
	// element; within it the denominator is sound and there is no finding.
	tiny := expected / 100
	if tiny < 1 {
		tiny = 1
	}
	if diff <= tiny {
		return
	}
	// A large gap (~10%, floor 5) is unambiguous — provably the wrong
	// denominator — and escalates to error; in between is a warning.
	large := expected / 10
	if large < 5 {
		large = 5
	}

	sev := SeverityWarn
	title := fmt.Sprintf("Element count %d diverges from the authoritative %d (off by %d)", actual, expected, diff)
	detail := "The scan's element count is the denominator of every coverage percentage. It diverges from the authoritative parser's count by more than a rounding tolerance — reconcile the contract globs so the denominator is provably right before trusting any percentage."
	if diff > large {
		sev = SeverityError
		title = fmt.Sprintf("Element count %d disagrees with the authoritative %d (off by %d) — every percentage is against the wrong denominator", actual, expected, diff)
		detail = "The scan found a very different number of elements than the authoritative parser. This is provable (two counts) and the gap is large and unambiguous: the contract globs miss or over-reach, so every coverage percentage is computed against the wrong denominator and is meaningless until the counts reconcile."
	}
	rep.add(Finding{
		Category: CatGroundTruth, Severity: sev,
		Title:    title,
		Detail:   detail,
		Expected: strconv.Itoa(expected) + " (authoritative)",
		Actual:   strconv.Itoa(actual) + " (scan)",
		Fix:      "Reconcile the contract globs against the authoritative parser (protoc descriptor / fidlc --json / the --help command tree) until the element counts match.",
	})
}
