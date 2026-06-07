package cli

import (
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// bundleItem is one report embedded in a single-file bundle. HTML is the
// full self-contained report, embedded via an <iframe srcdoc> so each
// report renders in its own document (no CSS/JS id collisions). Err is
// set when the entry failed to render.
type bundleItem struct {
	Slug  string
	Name  string
	Group string
	HTML  string
	Err   string
	Stats scanner.LibraryStats
}

type bundleGroup struct {
	Name  string
	Items []*bundleItem
}

type bundleView struct {
	Commit       string
	SheafVersion string
	Repo         string
	GeneratedStr string
	Total        int
	FirstSlug    string
	Groups       []bundleGroup
	Items        []*bundleItem
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// slugify derives a stable hash-anchor id from the entry's output path
// (preferred) or library name.
func slugify(out, lib string) string {
	base := out
	if base == "" {
		base = lib
	}
	base = strings.TrimSuffix(base, ".html")
	s := strings.Trim(slugRe.ReplaceAllString(base, "-"), "-")
	if s == "" {
		s = "report"
	}
	return s
}

func groupOr(g string) string {
	if g == "" {
		return "Ungrouped"
	}
	return g
}

// RenderBundle writes a single self-contained index.html into outputDir
// that embeds every report as a hash-routed iframe — one portable file
// for the whole run (small/medium runs; the file weighs the sum of its
// reports).
func RenderBundle(outputDir string, items []bundleItem, meta RunMeta) error {
	view := bundleView{
		Commit:       meta.Commit,
		SheafVersion: meta.SheafVersion,
		Repo:         meta.Repo,
		GeneratedStr: meta.GeneratedAt.Format("2006-01-02 15:04") + " UTC",
	}
	gi := map[string]int{}
	for i := range items {
		it := &items[i]
		view.Items = append(view.Items, it)
		if it.Err == "" {
			view.Total++
			if view.FirstSlug == "" {
				view.FirstSlug = it.Slug
			}
		}
		g := groupOr(it.Group)
		idx, ok := gi[g]
		if !ok {
			idx = len(view.Groups)
			gi[g] = idx
			view.Groups = append(view.Groups, bundleGroup{Name: g})
		}
		view.Groups[idx].Items = append(view.Groups[idx].Items, it)
	}
	f, err := os.Create(filepath.Join(outputDir, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	return bundleTemplate.Execute(f, view)
}

var bundleTemplate = template.Must(template.New("bundle").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sheaf — single-file run</title>
<style>
:root{--paper:#faf6ee;--card:#fffdf8;--ink:#16201c;--ink-2:#4a554f;--ink-3:#7d877f;--line:#ece5d6;--green-700:#0a6b3f;--green-600:#0f9d58;--gap-700:#b32d1a;
--f-ui:'IBM Plex Sans',-apple-system,system-ui,sans-serif;--f-mono:ui-monospace,Menlo,monospace;}
*{margin:0;padding:0;box-sizing:border-box;}
body{background:var(--paper);color:var(--ink);font-family:var(--f-ui);height:100vh;display:flex;flex-direction:column;}
header{padding:10px 18px;border-bottom:1px solid var(--line);display:flex;align-items:center;gap:12px;flex:0 0 auto;}
header .mark{width:22px;height:22px;border-radius:5px;background:var(--green-600);}
header .brand{font-weight:700;font-size:12px;letter-spacing:.14em;text-transform:uppercase;color:var(--green-700);}
header .meta{font-family:var(--f-mono);font-size:11px;color:var(--ink-3);}
.layout{flex:1 1 auto;display:flex;min-height:0;}
.bnav{flex:0 0 240px;border-right:1px solid var(--line);overflow:auto;padding:12px 0;background:var(--card);}
.bnav .bgrp{font-size:10px;font-weight:700;letter-spacing:.1em;text-transform:uppercase;color:var(--ink-3);padding:12px 18px 4px;}
.bnav a{display:block;padding:5px 18px;font-size:13px;color:var(--ink-2);text-decoration:none;font-family:var(--f-mono);}
.bnav a:hover{background:var(--paper);color:var(--ink);}
.bnav a.err{color:var(--gap-700);}
main{flex:1 1 auto;min-width:0;position:relative;}
.rpt{display:none;width:100%;height:100%;border:0;}
.rpt:target{display:block;}
section.rpt{padding:40px;font-family:var(--f-mono);color:var(--gap-700);}
.empty{position:absolute;inset:0;display:flex;align-items:center;justify-content:center;color:var(--ink-3);font-size:13px;}
</style>
</head>
<body>
<header>
  <span class="mark"></span><span class="brand">Sheaf</span>
  <span class="meta">{{.Repo}}{{if .Commit}} @ {{.Commit}}{{end}} · {{.GeneratedStr}} · sheaf {{.SheafVersion}} · {{.Total}} reports (single file)</span>
</header>
<div class="layout">
  <nav class="bnav">
{{range .Groups}}    <div class="bgrp">{{.Name}}</div>
{{range .Items}}    <a href="#{{.Slug}}"{{if .Err}} class="err"{{end}}>{{.Name}}{{if .Err}} — failed{{end}}</a>
{{end}}{{end}}  </nav>
  <main>
    <div class="empty">Select a report from the left.</div>
{{range .Items}}{{if .Err}}    <section class="rpt" id="{{.Slug}}">{{.Name}} — failed: {{.Err}}</section>
{{else}}    <iframe class="rpt" id="{{.Slug}}" srcdoc="{{.HTML}}" loading="lazy" title="{{.Name}}"></iframe>
{{end}}{{end}}  </main>
</div>
<script>
(function(){var f={{.FirstSlug}};if(!location.hash&&f){location.replace("#"+f);}})();
</script>
</body>
</html>
`))
