// Package fidldoc reads a `fidldoc.zip` rendered-reference bundle and
// emits DocClaims for each anchored section.
//
// fidldoc.zip is the published-form of FIDL reference docs: one
// `<library>/README.md` per FIDL library, with each protocol/method/
// type as an anchored markdown section like:
//
//   ### Open {#Directory.Open}
//
//   Open (or create) a node relative to this directory…
//
//   * `ZX_ERR_BAD_PATH` if `path` is invalid
//   …
//
// The adapter splits on those anchor headers, extracts the body,
// grades substance by word count of the prose (excluding tables and
// fenced code), and emits one DocClaim per anchor.

package fidldoc

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "fidldoc"
const Version = "0.1.0"

// Adapter implements adapters.RenderedReferenceParser for FIDL bundles.
type Adapter struct {
	bundlePath string
	urlBase    string
	libraries  []string // optional: limit to these libraries; empty = all
}

type Config struct {
	BundlePath string
	URLBase    string
	Library    string   // optional single library
	Libraries  []string // optional list (used if Library is empty)
}

func New(cfg Config) *Adapter {
	libs := cfg.Libraries
	if cfg.Library != "" {
		libs = append([]string{cfg.Library}, libs...)
	}
	urlBase := cfg.URLBase
	if urlBase == "" {
		urlBase = "https://fuchsia.dev/reference/fidl/"
	}
	return &Adapter{
		bundlePath: cfg.BundlePath,
		urlBase:    urlBase,
		libraries:  libs,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Parse opens the zip bundle and walks all `<library>/README.md` files
// inside it. Each anchored section becomes one DocClaim.
func (a *Adapter) Parse(_ context.Context) ([]*docclaimpb.DocClaim, error) {
	if a.bundlePath == "" {
		return nil, fmt.Errorf("fidldoc: bundle_path is empty")
	}
	r, err := zip.OpenReader(a.bundlePath)
	if err != nil {
		return nil, fmt.Errorf("fidldoc: open %s: %w", a.bundlePath, err)
	}
	defer r.Close()

	var out []*docclaimpb.DocClaim
	for _, f := range r.File {
		library, ok := libraryFromZipPath(f.Name)
		if !ok {
			continue
		}
		if !a.libraryAllowed(library) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("fidldoc: open %s in bundle: %w", f.Name, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("fidldoc: read %s: %w", f.Name, err)
		}
		out = append(out, parseSections(body, f.Name, library, a.urlBase)...)
	}
	return out, nil
}

func (a *Adapter) libraryAllowed(lib string) bool {
	if len(a.libraries) == 0 {
		return true
	}
	for _, l := range a.libraries {
		if l == lib {
			return true
		}
	}
	return false
}

// libraryFromZipPath returns ("fuchsia.io", true) for inputs like:
//
//	fidldoc/fuchsia.io/README.md
//	fuchsia.io/README.md
//
// Returns ("", false) for anything that doesn't fit the convention
// (table of contents, top-level index, etc.).
func libraryFromZipPath(p string) (string, bool) {
	// Normalize separators (zip uses /).
	p = strings.TrimPrefix(p, "fidldoc/")
	if !strings.HasSuffix(p, "/README.md") {
		return "", false
	}
	lib := strings.TrimSuffix(p, "/README.md")
	// Skip nested READMEs (we only want the per-library top-level).
	if strings.Contains(lib, "/") {
		return "", false
	}
	if lib == "" {
		return "", false
	}
	return lib, true
}

// Anchored header pattern: `### <name> {#<id>}`. The id portion is
// usually `Protocol.Method` for methods, `Protocol` for protocols,
// `TypeName` for types.
var anchorRx = regexp.MustCompile(`(?m)^(#{2,4})\s+([^\n{]+?)\s+\{#([^\}]+)\}\s*$`)

func parseSections(body []byte, sourcePath, library, urlBase string) []*docclaimpb.DocClaim {
	matches := anchorRx.FindAllSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return nil
	}
	offsets := computeLineOffsets(body)
	var out []*docclaimpb.DocClaim
	for i, m := range matches {
		anchor := string(body[m[6]:m[7]])
		// Section body is from end-of-header to start of next header (or EOF).
		bodyStart := m[1]
		bodyEnd := len(body)
		if i+1 < len(matches) {
			bodyEnd = matches[i+1][0]
		}
		section := body[bodyStart:bodyEnd]
		prose := proseOnly(section)
		words := countWords(prose)
		substance := gradeSubstance(words)
		url := urlBase + library + "#" + anchor
		out = append(out, &docclaimpb.DocClaim{
			SourcePath:   sourcePath,
			Location:     &commonpb.SourceLocation{Path: sourcePath, Line: uint32(lineFromOffset(offsets, m[0]))},
			RawText:      truncate(strings.TrimSpace(string(prose)), 300),
			ContractRefs: []string{library + "/" + anchor},
			Url:          url,
			Substance:    substance,
			WordCount:    uint32(words),
			Kind:         docclaimpb.DocClaimKind_REFERENCE,
			Adapter:      Name,
		})
	}
	return out
}

// proseOnly strips elements that don't count toward "behavioral
// description" word count: fenced code blocks, markdown tables,
// horizontal rules. Heuristics, but they handle the common cases
// in fidldoc output.
func proseOnly(s []byte) []byte {
	var out bytes.Buffer
	lines := bytes.Split(s, []byte("\n"))
	inCode := false
	for _, line := range lines {
		trimmed := bytes.TrimLeft(line, " \t")
		// Fenced code block.
		if bytes.HasPrefix(trimmed, []byte("```")) {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		// Markdown table separator/row.
		if bytes.HasPrefix(trimmed, []byte("|")) {
			continue
		}
		// Horizontal rule.
		if bytes.Equal(trimmed, []byte("---")) || bytes.Equal(trimmed, []byte("***")) {
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
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

func countWords(b []byte) int {
	return len(strings.Fields(string(b)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func computeLineOffsets(body []byte) []int {
	offsets := []int{0}
	for i, b := range body {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

func lineFromOffset(offsets []int, off int) int {
	lo, hi := 0, len(offsets)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if offsets[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}
