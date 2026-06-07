package conceptdocs

import (
	_ "embed"
	"encoding/base64"
	"html/template"
	"io"
	"strings"
)

//go:embed templates/concept-docs-report.html.tmpl
var tmplSrc string

//go:embed templates/logo.png
var logoPNG []byte

// logoDataURI returns the embedded sheaf logo as a base64 data URI, mirroring
// utils/scanner's LogoDataURI so the concept-doc header shows the real logo
// (not a CSS placeholder). Self-contained — the asset lives beside this package
// so go:embed can reach it without dragging in utils/scanner.
func logoDataURI() template.URL {
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(logoPNG))
}

var funcMap = template.FuncMap{
	"lower": strings.ToLower,
	"sub":   func(a, b int) int { return a - b },
	"shortsha": func(s string) string {
		if len(s) > 7 {
			return s[:7]
		}
		return s
	},
}

var tmpl = template.Must(template.New("concept-docs-report").Funcs(funcMap).Parse(tmplSrc))

// Render writes the Concept Docs report HTML for v to w.
func Render(w io.Writer, v *View) error {
	return tmpl.Execute(w, v)
}

// RenderString renders the report to a string.
func RenderString(v *View) (string, error) {
	var b strings.Builder
	if err := Render(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}
