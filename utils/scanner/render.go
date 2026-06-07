package scanner

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"sort"
	"strings"
)

//go:embed templates/report.html.tmpl templates/logo.png
var assets embed.FS

// RenderHTML writes the self-contained HTML report for r into w.
func RenderHTML(w io.Writer, r *ReportData) error {
	tmpl, err := loadTemplate()
	if err != nil {
		return err
	}
	view := buildView(r)
	return tmpl.ExecuteTemplate(w, "report", view)
}

func loadTemplate() (*template.Template, error) {
	funcs := template.FuncMap{
		"json": jsonInline,
		"on": func(b bool) string {
			if b {
				return "on"
			}
			return ""
		},
		"plus": func(a, b int) int { return a + b },
		"pct": func(n, total int) int {
			if total <= 0 {
				return 0
			}
			return n * 100 / total
		},
		// pcts rounds a set of counts to whole percentages of total
		// that sum to exactly 100, using the largest-remainder method.
		// Plain per-cell pct floors each value, so a four-band row of
		// 90/0/0/9 visibly sums to 99; pcts hands the leftover point(s)
		// to the bands with the largest fractional remainder. Returns a
		// slice aligned to counts; all-zero when total <= 0. Callers use
		// the same returned value for both the label and its bar width
		// so the two never disagree.
		"pcts": func(total int, counts ...int) []int {
			out := make([]int, len(counts))
			if total <= 0 {
				return out
			}
			type frac struct {
				idx, rem int
			}
			fr := make([]frac, len(counts))
			sum := 0
			for i, c := range counts {
				out[i] = c * 100 / total
				sum += out[i]
				fr[i] = frac{i, (c * 100) % total}
			}
			short := 100 - sum
			if short <= 0 {
				return out
			}
			// Largest remainder first; ties keep input order (stable) so
			// the rounding is deterministic across runs / golden renders.
			sort.SliceStable(fr, func(a, b int) bool { return fr[a].rem > fr[b].rem })
			for k := 0; k < short && k < len(fr); k++ {
				out[fr[k].idx]++
			}
			return out
		},
		"pctClass": func(pct int) string {
			switch {
			case pct >= 60:
				return "ok"
			case pct >= 25:
				return "mid"
			}
			return "gap"
		},
		// zeroOr renders headline counts as the spelled-out word "Zero"
		// when n == 0, otherwise as the bare digits. The italic
		// subheadlines below each headline route the same substitution
		// through compute.go's numWord helper — both layers share the
		// rule "Zero in headlines reads as a deliberate finding; 0
		// reads as a missing number."
		"zeroOr": func(n int) string {
			if n == 0 {
				return "Zero"
			}
			return fmt.Sprintf("%d", n)
		},
		// lower routes through strings.ToLower — used by the
		// per-tier masthead's apex line to render ecosystem-aware
		// tier nouns ("Commands" → "commands", "Methods" →
		// "methods") inline without forcing compute.go to ship two
		// copies of the same noun.
		"lower": strings.ToLower,
	}
	t := template.New("report").Funcs(funcs)
	return t.ParseFS(assets, "templates/report.html.tmpl")
}

// jsonInline serializes v as compact JSON for inline use inside a
// <script> block. It escapes "</script" sequences so user-controlled
// strings can't break out of the tag.
func jsonInline(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("null")
	}
	s := strings.ReplaceAll(string(b), "</", `<\/`)
	return template.JS(s)
}

// view is the flattened render struct that goes into the template.
type view struct {
	*ReportData
	IconDataURI    template.URL
	Donut          donutGeom
	MethodsJSON    template.JS
	AnomaliesJSON  template.JS
	FixGroupsJSON  template.JS
	SubstanceShare map[string]int // % share per substance bucket
}

// LogoDataURI returns the embedded sheaf logo (docs/logo.png) as a base64
// data: URI, or "" if it can't be read. Shared by the report header
// (buildView) and the coverage-index sticky header.
func LogoDataURI() template.URL {
	icon, err := assets.ReadFile("templates/logo.png")
	if err != nil {
		return ""
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(icon))
}

func buildView(r *ReportData) *view {
	v := &view{ReportData: r}
	v.IconDataURI = LogoDataURI()
	v.Donut = computeDonut(r.SubstanceCounts, r.SubstanceTotal)
	v.MethodsJSON = jsonInline(r.Methods)
	v.AnomaliesJSON = jsonInline(r.Anomalies)
	v.FixGroupsJSON = jsonInline(r.FixGroups)
	v.SubstanceShare = map[string]int{}
	for k, n := range r.SubstanceCounts {
		v.SubstanceShare[k] = pct(n, r.SubstanceTotal)
	}
	return v
}

// donutGeom holds the SVG arc commands + label layout for the
// substance donut. Arcs are in ink-density order (darkest = most
// substantive). An absent arc is omitted entirely.
type donutGeom struct {
	Arcs     []donutArc
	BigPct   int
	BigLabel string // lowercase label of the largest bucket
	Labels   []donutLabel
}

type donutArc struct {
	Path  string
	Color string
	Label string
	Pct   int
}

// donutLabel is one leader-line + text annotation rendered next to
// the donut. Labels are split into left/right columns based on which
// half of the donut their arc lives in (so leaders never cross the
// donut), and Y positions are post-adjusted to enforce a minimum
// vertical gap so labels never overlap.
type donutLabel struct {
	DotX, DotY   float64 // anchor on the arc edge
	TextX, TextY float64 // baseline of the text label
	Anchor       string  // "start" (right column) | "end" (left column)
	Leader       string  // pre-formatted polyline "x1,y1 x2,y2 x3,y3"
	Pct          int
	Label        string
	Color        string
}

// computeDonut lays out a 360° donut around (200, 165) radius 92px,
// plus a right-side label column with leader lines. The center label
// reflects the largest bucket — not always "substantive".
func computeDonut(counts map[string]int, total int) donutGeom {
	if total == 0 {
		return donutGeom{}
	}
	const (
		cx, cy, r  = 200.0, 165.0, 92.0
		leaderDotR = 115.0 // dot just outside the arc's outer edge
		// Right column.
		labelRightTextX = 332.0
		labelRightBendX = 318.0
		// Left column (mirror).
		labelLeftTextX     = 68.0
		labelLeftBendX     = 82.0
		minLabelGap        = 36.0 // min vertical px between adjacent labels in a column
		leaderHGapFromText = 6.0
	)
	order := []struct {
		key   string
		color string
		label string
	}{
		{"SUBSTANTIVE", "#16201c", "FULL DOCUMENTATION"},
		{"PARTIAL", "#5a635c", "Brief"},
		{"SIGNATURE_ONLY", "#9aa49d", "Stub"},
		{"ABSENT", "#d8d1c1", "None"},
	}

	// Pick the largest bucket for the center label.
	bigN := 0
	bigLabel := ""
	for _, b := range order {
		if counts[b.key] > bigN {
			bigN = counts[b.key]
			bigLabel = b.label
		}
	}
	g := donutGeom{
		BigPct:   pct(bigN, total),
		BigLabel: strings.ToLower(bigLabel),
	}

	start := -math.Pi / 2 // 12 o'clock
	var leftLabels, rightLabels []donutLabel
	for _, b := range order {
		n := counts[b.key]
		if n == 0 {
			continue
		}
		share := float64(n) / float64(total)
		end := start + share*2*math.Pi
		large := 0
		if share > 0.5 {
			large = 1
		}
		x1 := cx + r*math.Cos(start)
		y1 := cy + r*math.Sin(start)
		x2 := cx + r*math.Cos(end)
		y2 := cy + r*math.Sin(end)
		var path string
		if share >= 0.999 {
			// Full-circle case: an SVG arc from a point back to itself
			// is degenerate (renders nothing). Split into two 180°
			// arcs joined at the bottom of the circle so the ring
			// actually draws.
			mx := cx + r*math.Cos(start+math.Pi)
			my := cy + r*math.Sin(start+math.Pi)
			path = fmt.Sprintf("M %.2f %.2f A %.0f %.0f 0 0 1 %.2f %.2f A %.0f %.0f 0 0 1 %.2f %.2f",
				x1, y1, r, r, mx, my, r, r, x2, y2)
		} else {
			path = fmt.Sprintf("M %.2f %.2f A %.0f %.0f 0 %d 1 %.2f %.2f",
				x1, y1, r, r, large, x2, y2)
		}
		g.Arcs = append(g.Arcs, donutArc{
			Path:  path,
			Color: b.color,
			Label: b.label,
			Pct:   pct(n, total),
		})
		mid := start + share*math.Pi // arc midpoint angle
		dotX := cx + leaderDotR*math.Cos(mid)
		dotY := cy + leaderDotR*math.Sin(mid)
		lbl := donutLabel{
			DotX: dotX, DotY: dotY,
			TextY: dotY, // initial; column-stacked below
			Pct:   pct(n, total),
			Label: b.label,
			Color: b.color,
		}
		if math.Cos(mid) >= 0 {
			lbl.TextX = labelRightTextX
			lbl.Anchor = "start"
			rightLabels = append(rightLabels, lbl)
		} else {
			lbl.TextX = labelLeftTextX
			lbl.Anchor = "end"
			leftLabels = append(leftLabels, lbl)
		}
		start = end
	}

	// Stack each column top-to-bottom and enforce min vertical gap.
	stack := func(col []donutLabel) []donutLabel {
		sort.SliceStable(col, func(i, j int) bool {
			return col[i].DotY < col[j].DotY
		})
		prev := -1e9
		for i := range col {
			if col[i].TextY < prev+minLabelGap {
				col[i].TextY = prev + minLabelGap
			}
			prev = col[i].TextY
		}
		return col
	}
	rightLabels = stack(rightLabels)
	leftLabels = stack(leftLabels)

	// Pre-format the leader polyline so the template doesn't need
	// float arithmetic. Three points: arc dot → bend at column edge →
	// near-text (offset on whichever side the text lives).
	for i := range rightLabels {
		l := &rightLabels[i]
		l.Leader = fmt.Sprintf("%.1f,%.1f %.1f,%.1f %.1f,%.1f",
			l.DotX, l.DotY,
			labelRightBendX, l.TextY,
			l.TextX-leaderHGapFromText, l.TextY)
	}
	for i := range leftLabels {
		l := &leftLabels[i]
		l.Leader = fmt.Sprintf("%.1f,%.1f %.1f,%.1f %.1f,%.1f",
			l.DotX, l.DotY,
			labelLeftBendX, l.TextY,
			l.TextX+leaderHGapFromText, l.TextY)
	}
	g.Labels = append(leftLabels, rightLabels...)
	return g
}
