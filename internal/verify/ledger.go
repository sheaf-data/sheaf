package verify

import (
	"fmt"
	"sort"
	"strings"
)

// RenderLedger renders the human-readable "trust ledger" for a verify
// Report. It leads with the verdict and the single next action (BLUF),
// then the reconciled headline numbers, then findings grouped by
// severity, then an explicit list of what was NOT verified. The ledger is
// the artifact a skeptic reads before forwarding the report: every number
// shows its formula and inputs, and every unknown is named rather than
// hidden.
func RenderLedger(r *Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Sheaf scan — trust ledger\n\n")
	fmt.Fprintf(&b, "**Library:** %s  ·  **Ecosystem:** %s  ·  **Elements:** %d  ·  **Low-coverage line:** ≤%d%%\n\n",
		dash(r.Library), dash(r.Ecosystem), r.ElementCount, int(r.Threshold*100))

	// BLUF: verdict + the one next action.
	switch r.Verdict {
	case VerdictTrustworthy:
		fmt.Fprintf(&b, "> ✅ **TRUSTWORTHY** — every shown figure reconciled to its numerator and denominator, no surface flagged. %s\n\n", diskNote(r))
	case VerdictReview:
		fmt.Fprintf(&b, "> ⚠️ **REVIEW** — %d number(s) flagged for validation before this report is shared. Work the warnings below; each names the disk check that confirms or refutes it. %s\n\n", r.Warnings, diskNote(r))
	case VerdictBroken:
		fmt.Fprintf(&b, "> ⛔ **BROKEN** — %d number(s) are wrong or unverifiable. Do NOT show this report until they are resolved; a reviewer who spots one on first contact discards the tool. %s\n\n", r.Errors, diskNote(r))
	}

	// Reconciled headline numbers.
	b.WriteString("## Headline numbers (reconciled)\n\n")
	if len(r.Metrics) == 0 {
		b.WriteString("_No coverage surfaces were rendered for this library._\n\n")
	} else {
		b.WriteString("| Surface | Tier | Shown | n ÷ N | Reproduced | Flag |\n")
		b.WriteString("|---|---|---:|---:|:---:|:---|\n")
		for _, m := range r.Metrics {
			repro := "✓"
			if !m.Reproduced {
				repro = "✗ mismatch"
			}
			flag := ""
			switch {
			case m.Percent == 0 && m.Flagged:
				flag = "⛔ 0%"
			case m.Flagged:
				flag = "⚠ ≤line"
			}
			tier := m.Tier
			if m.TierNoun != "" {
				tier = fmt.Sprintf("%s (%s)", m.Tier, m.TierNoun)
			}
			fmt.Fprintf(&b, "| %s | %s | %d%% | %d ÷ %d | %s | %s |\n",
				m.Name, tier, m.Percent, m.Numerator, m.Denominator, repro, flag)
		}
		b.WriteString("\n")
	}

	// Findings grouped by severity.
	errs := findingsOf(r, SeverityError)
	warns := findingsOf(r, SeverityWarn)
	infos := findingsOf(r, SeverityInfo)

	fmt.Fprintf(&b, "## Findings (%d)\n\n", len(r.Findings))
	if len(r.Findings) == 0 {
		b.WriteString("_None._\n\n")
	}
	writeFindingGroup(&b, "Errors — a shown number is wrong or unverifiable", errs)
	writeFindingGroup(&b, "Warnings — flagged for validation against disk", warns)
	writeFindingGroup(&b, "Notes", infos)

	// Honest unknowns.
	if len(r.Caveats) > 0 {
		b.WriteString("## What was NOT verified\n\n")
		for _, c := range r.Caveats {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	}

	return b.String()
}

func writeFindingGroup(b *strings.Builder, heading string, fs []Finding) {
	if len(fs) == 0 {
		return
	}
	fmt.Fprintf(b, "### %s\n\n", heading)
	for _, f := range fs {
		fmt.Fprintf(b, "- **%s**  `[%s]`\n", f.Title, f.Category)
		if f.Detail != "" {
			fmt.Fprintf(b, "  - %s\n", f.Detail)
		}
		if f.Element != "" {
			fmt.Fprintf(b, "  - element: `%s`\n", f.Element)
		}
		if f.Expected != "" || f.Actual != "" {
			fmt.Fprintf(b, "  - shown: **%s**, expected: **%s**\n", dash(f.Actual), dash(f.Expected))
		}
		for _, e := range f.Evidence {
			fmt.Fprintf(b, "  - evidence: `%s`\n", e)
		}
		if f.Fix != "" {
			fmt.Fprintf(b, "  - → %s\n", f.Fix)
		}
	}
	b.WriteString("\n")
}

func findingsOf(r *Report, sev Severity) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == sev {
			out = append(out, f)
		}
	}
	// Stable order: by category then title, so the ledger is deterministic.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Title < out[j].Title
	})
	return out
}

func diskNote(r *Report) string {
	if r.DiskVerified {
		return "Disk oracle: ON (claims checked against the source tree)."
	}
	return "Disk oracle: OFF (reconciled to the snapshot only — see caveats)."
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
