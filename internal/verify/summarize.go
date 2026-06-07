package verify

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

// This file is the `summarize` half of the attribution precision workflow:
// it reads the agent-verdicted assertions back and computes per-library
// precision + the confirmed-false-positive table, mirroring
// `validate.py summarize`. Go does only the arithmetic; the tp/fp call was
// the agent's.

// LoadAssertions reads verdicted assertions from path. It is tolerant of the
// three shapes an agent might hand back: JSONL (one Assertion per line — the
// canonical form), a bare JSON array, or a {"assertions": [...]} object (the
// shape `sheaf verify --json` emits, in case the agent edited it in place).
func LoadAssertions(path string) ([]Assertion, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read assertions: %w", err)
	}

	// JSONL first — the canonical hand-back form.
	var rows []Assertion
	jsonl := true
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var a Assertion
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			jsonl = false
			break
		}
		rows = append(rows, a)
	}
	if jsonl && len(rows) > 0 {
		return rows, nil
	}

	// A bare JSON array.
	var arr []Assertion
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}
	// The {"assertions": [...]} wrapper (verify.json itself).
	var wrap struct {
		Assertions []Assertion `json:"assertions"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && wrap.Assertions != nil {
		return wrap.Assertions, nil
	}
	return nil, fmt.Errorf("could not parse %s as JSONL, a JSON array, or a verify.json with assertions", path)
}

// verdictOf normalizes the agent's verdict to a lowercase token, "" when
// unset/blank (counted as unverified, never as a pass).
func verdictOf(a Assertion) string {
	if a.Verdict == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*a.Verdict))
}

func isAttribution(kind string) bool { return kind == "tested_by" || kind == "documented_by" }

type libPrecision struct {
	lib                             string
	sampled, tp, fp, ambig, unverif int
}

// precision is tp / (tp+fp); NaN (rendered "—") when no decided attribution.
func (p libPrecision) precision() float64 {
	d := p.tp + p.fp
	if d == 0 {
		return math.NaN()
	}
	return float64(p.tp) / float64(d)
}

// RenderPrecisionLedger computes per-library precision and the confirmed-FP
// table from verdicted assertions and renders the markdown summary.
func RenderPrecisionLedger(rows []Assertion) string {
	byLib := map[string]*libPrecision{}
	var fps []Assertion
	var ambig []Assertion
	verified, unverified := 0, 0

	for _, a := range rows {
		if !isAttribution(a.Kind) {
			continue // fn_candidate etc. belong to the --disk FN half
		}
		lib := a.Library
		if lib == "" {
			lib = "(no library)"
		}
		p := byLib[lib]
		if p == nil {
			p = &libPrecision{lib: lib}
			byLib[lib] = p
		}
		p.sampled++
		switch verdictOf(a) {
		case "tp":
			p.tp++
			verified++
		case "fp":
			p.fp++
			verified++
			fps = append(fps, a)
		case "ambiguous":
			p.ambig++
			verified++
			ambig = append(ambig, a)
		default:
			p.unverif++
			unverified++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Sheaf attribution precision (sampled)\n\n")
	total := verified + unverified
	fmt.Fprintf(&b, "- Attribution assertions: **%d**  ·  verified: **%d**  ·  unverified: **%d**\n", total, verified, unverified)
	fmt.Fprintf(&b, "- Confirmed false positives: **%d**  ·  ambiguous (needs human): **%d**\n\n", len(fps), len(ambig))

	if total == 0 {
		b.WriteString("_No `tested_by` / `documented_by` assertions found. Emit them with `sheaf verify --json` (the `assertions` array), fill each verdict, then re-run summarize._\n")
		return b.String()
	}

	b.WriteString("## Per-library precision (sampling-aware)\n\n")
	b.WriteString("| Library | Sampled | TP | FP | Ambiguous | Unverified | Precision |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
	libs := make([]string, 0, len(byLib))
	for l := range byLib {
		libs = append(libs, l)
	}
	sort.Strings(libs)
	for _, l := range libs {
		p := byLib[l]
		fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d | %d | %s |\n",
			p.lib, p.sampled, p.tp, p.fp, p.ambig, p.unverif, fmtPct(p.precision()))
	}
	b.WriteString("\n")

	if len(fps) > 0 {
		sortAssertions(fps)
		b.WriteString("## Confirmed false positives\n\n")
		b.WriteString("| Library | Element | Kind | Reference | Reason |\n")
		b.WriteString("|---|---|---|---|---|\n")
		for _, a := range fps {
			fmt.Fprintf(&b, "| `%s` | `%s` | %s | `%s` | %s |\n",
				dash(a.Library), dash(a.Element), a.Kind, assertionRef(a), escapePipe(reasonOf(a)))
		}
		b.WriteString("\n")
	}

	if len(ambig) > 0 {
		sortAssertions(ambig)
		b.WriteString("## Ambiguous (needs human review)\n\n")
		shown := ambig
		if len(shown) > 50 {
			shown = shown[:50]
		}
		for _, a := range shown {
			fmt.Fprintf(&b, "- `%s` ← `%s`: %s\n", dash(a.Element), assertionRef(a), dash(reasonOf(a)))
		}
		if len(ambig) > 50 {
			fmt.Fprintf(&b, "- _…and %d more._\n", len(ambig)-50)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Caveats\n\n")
	b.WriteString("- **Precision** is computed only over sampled, agent-verified attributions; the true precision over the whole population is approximately this value, within sampling error.\n")
	b.WriteString("- A null or blank verdict is counted as **unverified**, never as a pass.\n")
	b.WriteString("- False negatives (missed attributions) are a separate question — run `sheaf verify --disk` for the false-negative search.\n")
	return b.String()
}

func reasonOf(a Assertion) string {
	if a.Reason == nil {
		return ""
	}
	return strings.TrimSpace(*a.Reason)
}

// assertionRef renders the source the claim points at, for a table cell.
func assertionRef(a Assertion) string {
	if a.Kind == "documented_by" {
		if a.DocLine > 0 {
			return fmt.Sprintf("%s:%d", a.DocPath, a.DocLine)
		}
		return a.DocPath
	}
	loc := a.TestPath
	if a.TestLine > 0 {
		loc = fmt.Sprintf("%s:%d", a.TestPath, a.TestLine)
	}
	if a.TestName != "" {
		return fmt.Sprintf("%s (%s)", a.TestName, loc)
	}
	return loc
}

func sortAssertions(as []Assertion) {
	sort.SliceStable(as, func(i, j int) bool {
		if as[i].Library != as[j].Library {
			return as[i].Library < as[j].Library
		}
		if as[i].Element != as[j].Element {
			return as[i].Element < as[j].Element
		}
		return assertionRef(as[i]) < assertionRef(as[j])
	})
}

func escapePipe(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func fmtPct(x float64) string {
	if math.IsNaN(x) {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", x*100)
}
