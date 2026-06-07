// Package clidoc reads a `clidoc_out.tar.gz` rendered-reference bundle
// for CLI tools and emits DocClaims for each anchored section.
//
// clidoc's output is a tarball with one markdown file per CLI binary
// (e.g., `clidoc/ffx.md`, `clidoc/triage.md`). Each binary's file
// contains recursive subcommand sections with anchored headers like:
//
//   ### show {#ffx_component_show}
//
//   Shows component details.
//
//   --json   emit JSON
//   --tree   show full tree
//
// Sheaf reads the file selected by `section_path` and emits one
// DocClaim per anchor. ContractRefs translate `ffx_component_show`
// back to `ffx component show` so the indexer can cross-reference
// against argh-discovered subcommands.

package clidoc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "clidoc"
const Version = "0.1.0"

type Adapter struct {
	bundlePath  string
	sectionPath string
	urlBase     string
}

type Config struct {
	BundlePath  string
	SectionPath string // path within the tarball to the binary's .md file
	URLBase     string
}

func New(cfg Config) *Adapter {
	urlBase := cfg.URLBase
	if urlBase == "" {
		urlBase = "https://fuchsia.dev/reference/tools/"
	}
	return &Adapter{
		bundlePath:  cfg.BundlePath,
		sectionPath: cfg.SectionPath,
		urlBase:     urlBase,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Parse opens the tarball, finds the configured section file, and
// emits DocClaims for each anchored subcommand inside it.
func (a *Adapter) Parse(_ context.Context) ([]*docclaimpb.DocClaim, error) {
	if a.bundlePath == "" {
		return nil, fmt.Errorf("clidoc: bundle_path is empty")
	}
	if a.sectionPath == "" {
		return nil, fmt.Errorf("clidoc: section_path is empty")
	}
	body, err := readTarballEntry(a.bundlePath, a.sectionPath)
	if err != nil {
		return nil, err
	}
	binary := binaryNameFromSectionPath(a.sectionPath)
	return parseSections(body, a.sectionPath, binary, a.urlBase), nil
}

// readTarballEntry opens a .tar.gz and returns the bytes for the named entry.
func readTarballEntry(tarballPath, entryName string) ([]byte, error) {
	f, err := os.Open(tarballPath)
	if err != nil {
		return nil, fmt.Errorf("clidoc: open %s: %w", tarballPath, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("clidoc: gunzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("clidoc: tar read: %w", err)
		}
		if hdr.Name == entryName || strings.TrimPrefix(hdr.Name, "./") == entryName {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("clidoc: entry %q not found in %s", entryName, tarballPath)
}

// binaryNameFromSectionPath returns "ffx" for "clidoc/ffx.md" or "ffx.md".
func binaryNameFromSectionPath(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return strings.TrimSuffix(p, ".md")
}

// Anchor headers follow the convention `### name {#binary_path_name}`.
var anchorRx = regexp.MustCompile(`(?m)^(#{2,4})\s+([^\n{]+?)\s+\{#([^\}]+)\}\s*$`)

// flagListRx matches a flag-listing line inside a command section. The
// long flag name is captured before any `, -s` shorthand, `<value>`
// placeholder, or trailing description. Leading whitespace is optional.
// The remainder of the line (group 2) becomes the claim body.
//
//	`--config, -c <config>  set config values …`  → name "config"
//	`--machine <machine>  machine output format …` → name "machine"
//	`--schema  output JSON schema …`               → name "schema"
var flagListRx = regexp.MustCompile(`^\s*--([a-z][a-z0-9-]*)\b(.*)$`)

func parseSections(body []byte, sourcePath, binary, urlBase string) []*docclaimpb.DocClaim {
	matches := anchorRx.FindAllSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return nil
	}
	offsets := computeLineOffsets(body)
	var out []*docclaimpb.DocClaim
	for i, m := range matches {
		anchor := string(body[m[6]:m[7]])
		// Translate underscore-joined anchor → space-joined command path.
		// `ffx_component_show` → `ffx component show`.
		commandPath := strings.ReplaceAll(anchor, "_", " ")
		// Section body is from end-of-header to start of next header.
		bodyStart := m[1]
		bodyEnd := len(body)
		if i+1 < len(matches) {
			bodyEnd = matches[i+1][0]
		}
		section := body[bodyStart:bodyEnd]
		prose := proseOnly(section)
		words := countWords(prose)
		out = append(out, &docclaimpb.DocClaim{
			SourcePath:   sourcePath,
			Location:     &commonpb.SourceLocation{Path: sourcePath, Line: uint32(lineFromOffset(offsets, m[0]))},
			RawText:      truncate(strings.TrimSpace(string(prose)), 300),
			ContractRefs: []string{commandPath},
			Url:          urlBase + binary + "#" + anchor,
			Substance:    gradeSubstance(words),
			WordCount:    uint32(words),
			Kind:         docclaimpb.DocClaimKind_REFERENCE,
			Adapter:      Name,
		})

		// Additive per-flag claims: each flag-listing line in this
		// section credits its own FLAG/SWITCH element via the ref
		// `<commandPath> --<flagname>`. Command-level claims above are
		// unchanged.
		out = append(out, flagClaims(section, bodyStart, offsets, commandPath, anchor, sourcePath, urlBase, binary)...)
	}
	return out
}

// flagClaims emits one REFERENCE DocClaim per flag-listing line found in a
// command's section body. section is the raw bytes from end-of-header to
// the next header; sectionStart is its absolute offset in the file (for
// line numbers). Each claim's ContractRef is `<commandPath> --<flagname>`
// and its body is the flag's trailing description, graded with
// gradeSubstance. A flag seen twice in one section is emitted once.
func flagClaims(section []byte, sectionStart int, offsets []int, commandPath, anchor, sourcePath, urlBase, binary string) []*docclaimpb.DocClaim {
	var out []*docclaimpb.DocClaim
	seen := map[string]bool{}
	off := 0
	for _, raw := range bytes.SplitAfter(section, []byte("\n")) {
		lineStart := off
		off += len(raw)
		line := strings.TrimRight(string(raw), "\r\n")
		m := flagListRx.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		desc := flagDescription(m[2])
		words := countWords([]byte(desc))
		ref := commandPath + " --" + name
		out = append(out, &docclaimpb.DocClaim{
			SourcePath:   sourcePath,
			Location:     &commonpb.SourceLocation{Path: sourcePath, Line: uint32(lineFromOffset(offsets, sectionStart+lineStart))},
			RawText:      truncate(desc, 300),
			ContractRefs: []string{ref},
			Url:          urlBase + binary + "#" + anchor,
			Substance:    gradeSubstance(words),
			WordCount:    uint32(words),
			Kind:         docclaimpb.DocClaimKind_REFERENCE,
			Adapter:      Name,
		})
	}
	return out
}

// flagDescription turns the post-name remainder of a flag-listing line
// into the human description: it drops a leading `, -s` shorthand and any
// `<value>` placeholder, then trims surrounding whitespace. For
// `, -c <config>  set config values` it returns `set config values`.
func flagDescription(rest string) string {
	rest = strings.TrimLeft(rest, " \t")
	// Drop a leading shorthand alias like `, -c`.
	if strings.HasPrefix(rest, ",") {
		rest = strings.TrimLeft(rest, ", \t")
		// Skip the `-s` token itself.
		if strings.HasPrefix(rest, "-") {
			if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
				rest = rest[sp:]
			} else {
				rest = ""
			}
		}
		rest = strings.TrimLeft(rest, " \t")
	}
	// Drop a leading `<value>` placeholder.
	if strings.HasPrefix(rest, "<") {
		if gt := strings.IndexByte(rest, '>'); gt >= 0 {
			rest = rest[gt+1:]
		}
	}
	return strings.TrimSpace(rest)
}

func proseOnly(s []byte) []byte {
	var out bytes.Buffer
	lines := bytes.Split(s, []byte("\n"))
	inCode := false
	for _, line := range lines {
		trimmed := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trimmed, []byte("```")) {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if bytes.HasPrefix(trimmed, []byte("|")) {
			continue
		}
		// CLI option listings typically look like `--name  description`
		// — keep the description part by leaving the line in but
		// stripping the flag prefix.
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func gradeSubstance(words int) commonpb.Substance {
	switch {
	case words == 0:
		return commonpb.Substance_ABSENT
	case words <= 1:
		return commonpb.Substance_SIGNATURE_ONLY
	case words <= 7:
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
