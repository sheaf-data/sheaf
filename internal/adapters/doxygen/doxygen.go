// Package doxygen reads a Doxygen XML output directory (GENERATE_XML=YES)
// and emits one DocClaim per *documented* C++ API entity — the formal API
// reference surface that lives in `///` / `/** */` header comments rather
// than in narrative prose.
//
// It is the structured-reference sibling of the rst/markdown prose doc
// parsers. Where those scan hand-written guides and catch the elements an
// author happened to name, this reads the generator's own per-symbol output
// and so reflects the *reference* surface directly: a class or function is
// "documented" iff Doxygen recorded a non-empty brief/detailed description
// for it.
//
// One DocClaim is emitted per:
//   - class / struct / union compound that carries a description
//     (ContractRef = the qualified compound name, e.g. `pw::StringBuilder`);
//   - member (function, variable, enum, typedef, define, …) that carries a
//     description (ContractRef = its <qualifiedname>, e.g.
//     `pw::StringBuilder::append` or `pw::string::Format`).
//
// Correctness over coverage (the adapter-authoring non-negotiable):
//   - a member/compound with an EMPTY description is skipped — Doxygen lists
//     undocumented symbols too (private fields, EXTRACT_ALL=YES), and counting
//     them as "documented" would over-attribute and poison the docs surface;
//   - members are deduped by Doxygen id, because the same free function is
//     listed under both its namespace compound and its file compound;
//   - namespace / file / dir compounds emit no compound-level claim (they are
//     not contract elements) — only their members count.
package doxygen

import (
	"context"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "doxygen"
const Version = "0.1.0"

// Config configures the doxygen doc parser.
type Config struct {
	// XMLDir is the Doxygen XML output directory (the dir containing
	// index.xml and the per-compound *.xml files). Absolute, or relative
	// to the scan repo root.
	XMLDir string
	// URLBase, when set, builds a clickable link per claim following the
	// Doxygen HTML convention: <URLBase>/<compoundRefid>.html#<anchor>.
	// Empty omits URLs.
	URLBase string
	// Include / Exclude are optional globs over the *.xml filenames
	// (basename match). Empty include = all compound XML files.
	Include []string
	Exclude []string
}

// Adapter implements adapters.DocParser over Doxygen XML.
type Adapter struct {
	xmlDir  string
	urlBase string
	include []string
	exclude []string
}

func New(cfg Config) *Adapter {
	return &Adapter{
		xmlDir:  cfg.XMLDir,
		urlBase: strings.TrimRight(cfg.URLBase, "/"),
		include: cfg.Include,
		exclude: cfg.Exclude,
	}
}

func (a *Adapter) Name() string               { return Name }
func (a *Adapter) Version() string            { return Version }
func (a *Adapter) SupportedFormats() []string { return []string{"doxygen-xml"} }

// --- Doxygen XML schema (the narrow subset we read) ---

type doxygenRoot struct {
	Compound compoundDef `xml:"compounddef"`
}

type compoundDef struct {
	ID       string      `xml:"id,attr"`
	Kind     string      `xml:"kind,attr"`
	Name     string      `xml:"compoundname"`
	Sections []sectptr   `xml:"sectiondef"`
	Brief    description `xml:"briefdescription"`
	Detailed description `xml:"detaileddescription"`
	Location location    `xml:"location"`
}

type sectptr struct {
	Members []memberDef `xml:"memberdef"`
}

type memberDef struct {
	ID            string      `xml:"id,attr"`
	Kind          string      `xml:"kind,attr"`
	Name          string      `xml:"name"`
	QualifiedName string      `xml:"qualifiedname"`
	Brief         description `xml:"briefdescription"`
	Detailed      description `xml:"detaileddescription"`
	Location      location    `xml:"location"`
}

// description captures the mixed-content inner XML of a brief/detailed
// description block so we can strip tags and grade the prose.
type description struct {
	Inner string `xml:",innerxml"`
}

type location struct {
	File string `xml:"file,attr"`
	Line uint32 `xml:"line,attr"`
}

// Parse walks the configured Doxygen XML directory and emits one DocClaim
// per documented compound/member.
func (a *Adapter) Parse(ctx context.Context, repoRoot string, _ adapters.ScopeConfig) ([]*docclaimpb.DocClaim, error) {
	if a.xmlDir == "" {
		return nil, fmt.Errorf("doxygen: xml_dir is empty")
	}
	dir := a.xmlDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repoRoot, dir)
	}

	var out []*docclaimpb.DocClaim
	seenMember := make(map[string]bool)
	seenCompound := make(map[string]bool)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir {
				return fs.SkipDir // Doxygen XML is flat; don't descend.
			}
			return nil
		}
		base := d.Name()
		if !strings.HasSuffix(base, ".xml") {
			return nil
		}
		// Non-compound bookkeeping files Doxygen drops in the same dir.
		if base == "index.xml" || base == "Doxyfile.xml" || strings.HasPrefix(base, "dir_") {
			return nil
		}
		if !a.fileAllowed(base) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("doxygen: read %s: %w", base, rerr)
		}
		var root doxygenRoot
		if uerr := xml.Unmarshal(data, &root); uerr != nil {
			// Not a compound XML (xsd/xslt or malformed) — skip quietly.
			return nil
		}
		c := root.Compound
		if c.Name == "" {
			return nil
		}
		out = append(out, claimsFromCompound(c, base, a.urlBase, seenCompound, seenMember)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (a *Adapter) fileAllowed(base string) bool {
	for _, ex := range a.exclude {
		if ok, _ := filepath.Match(ex, base); ok {
			return false
		}
	}
	if len(a.include) == 0 {
		return true
	}
	for _, in := range a.include {
		if ok, _ := filepath.Match(in, base); ok {
			return true
		}
	}
	return false
}

// emitsCompoundClaim reports whether a compound of this kind is itself a
// contract element worth a doc claim. Classes/structs/unions are; namespaces,
// files and dirs are containers, not elements.
func emitsCompoundClaim(kind string) bool {
	switch kind {
	case "class", "struct", "union", "interface":
		return true
	default:
		return false
	}
}

func claimsFromCompound(c compoundDef, sourcePath, urlBase string, seenCompound, seenMember map[string]bool) []*docclaimpb.DocClaim {
	if c.Kind == "dir" {
		return nil
	}
	var out []*docclaimpb.DocClaim

	// Compound-level claim (the class/struct/union itself).
	if emitsCompoundClaim(c.Kind) && !seenCompound[c.ID] {
		if prose := descProse(c.Brief, c.Detailed); prose != "" {
			seenCompound[c.ID] = true
			out = append(out, mkClaim(c.Name, prose, c.Location, sourcePath,
				url(urlBase, c.ID, c.ID)))
		}
	}

	// Member claims.
	for _, sec := range c.Sections {
		for _, m := range sec.Members {
			if m.ID != "" && seenMember[m.ID] {
				continue
			}
			ref := m.QualifiedName
			if ref == "" {
				// Fall back to <compoundname>::<name> for older Doxygen.
				ref = joinQualified(c.Name, m.Name)
			}
			if ref == "" {
				continue
			}
			prose := descProse(m.Brief, m.Detailed)
			if prose == "" {
				continue // undocumented — correctness over coverage.
			}
			if m.ID != "" {
				seenMember[m.ID] = true
			}
			out = append(out, mkClaim(ref, prose, m.Location, sourcePath,
				url(urlBase, c.ID, m.ID)))
		}
	}
	return out
}

func mkClaim(ref, prose string, loc location, sourcePath, claimURL string) *docclaimpb.DocClaim {
	words := countWords(prose)
	return &docclaimpb.DocClaim{
		SourcePath:   sourcePath,
		Location:     &commonpb.SourceLocation{Path: loc.File, Line: loc.Line},
		RawText:      truncate(prose, 300),
		WordCount:    uint32(words),
		Substance:    gradeSubstance(words),
		Kind:         docclaimpb.DocClaimKind_REFERENCE,
		ContractRefs: []string{ref},
		Url:          claimURL,
		Adapter:      Name,
	}
}

// joinQualified builds `pw::Foo::bar` from a compound name and a member name,
// avoiding a double `::` if the member name already carries qualification.
func joinQualified(compound, member string) string {
	compound = strings.TrimSpace(compound)
	member = strings.TrimSpace(member)
	if member == "" {
		return ""
	}
	if compound == "" || strings.Contains(member, "::") {
		return member
	}
	return compound + "::" + member
}

// url builds the Doxygen-HTML link for a member: <base>/<compoundRefid>.html#<anchor>.
// Doxygen member ids are `<compoundRefid>_1<anchor>`; for a compound-level claim
// memberID == compoundRefid (no anchor). Empty base disables links.
func url(base, compoundRefid, memberID string) string {
	if base == "" || compoundRefid == "" {
		return ""
	}
	u := base + "/" + compoundRefid + ".html"
	if memberID != "" && memberID != compoundRefid {
		anchor := strings.TrimPrefix(memberID, compoundRefid+"_1")
		if anchor != memberID && anchor != "" {
			u += "#" + anchor
		}
	}
	return u
}

var tagRx = regexp.MustCompile(`<[^>]*>`)
var wsRx = regexp.MustCompile(`\s+`)

// descProse flattens the brief + detailed description inner XML into plain
// prose: strip tags, collapse whitespace. Returns "" when there is no real
// description (the gate that keeps undocumented symbols out of the surface).
func descProse(brief, detailed description) string {
	parts := make([]string, 0, 2)
	if p := flatten(brief.Inner); p != "" {
		parts = append(parts, p)
	}
	if p := flatten(detailed.Inner); p != "" {
		parts = append(parts, p)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func flatten(inner string) string {
	if strings.TrimSpace(inner) == "" {
		return ""
	}
	s := tagRx.ReplaceAllString(inner, " ")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = wsRx.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func gradeSubstance(words int) commonpb.Substance {
	switch {
	case words == 0:
		return commonpb.Substance_ABSENT
	case words <= 4:
		return commonpb.Substance_SIGNATURE_ONLY
	case words <= 19:
		return commonpb.Substance_PARTIAL
	default:
		return commonpb.Substance_SUBSTANTIVE
	}
}

func countWords(s string) int { return len(strings.Fields(s)) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
