// Package cml implements a contract-anchor adapter for Fuchsia
// Component Manifest Language (.cml) files. It extracts CONFIG_KNOB
// elements from `config: { ... }` blocks.
//
// CML is a JSON5 superset: comments, trailing commas, unquoted keys,
// nested objects. This adapter parses the config block deterministically
// via a small brace/string-aware scanner — no LLM, no general JSON5
// parser dependency, and resilient to the rest of the manifest's
// declarations (program, capabilities, use, expose, offer, etc.).
//
// Each emitted ContractElement has:
//   - Id = "cml:<package_dir>/<knob_name>"
//   - Kind = CONFIG_KNOB
//   - Ecosystem = "cml"
//   - Library = package_dir (the conventional parent of meta/)
//   - Location pointing at the line of the knob declaration
//   - EcosystemMeta carrying the declared type, default value, and
//     constraints (max_size, max_count, element type)
package cml

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	"google.golang.org/protobuf/types/known/structpb"
)

const Name = "cml"
const Version = "0.1.0"

type Adapter struct {
	include []string
	exclude []string
}

type Config struct {
	Include []string // defaults to ["**/*.cml"]
	Exclude []string // defaults to []
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.cml"}
	}
	return &Adapter{
		include: include,
		exclude: cfg.Exclude,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Discover walks the repo for .cml files and emits one ContractElement
// per knob declared in a top-level `config: { ... }` block.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []*contractpb.ContractElement
	err := adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return nil // skip unreadable file; not a hard error
		}
		pkg := packageDir(rel)
		if !libraryInScope(pkg, scope) {
			return nil
		}
		out = append(out, parseCMLFile(rel, string(body), pkg)...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cml: walk repo: %w", err)
	}
	return out, nil
}

// parseCMLFile finds the first top-level `config: { ... }` block and
// emits a ContractElement per declared knob.
func parseCMLFile(relPath, body, pkg string) []*contractpb.ContractElement {
	startIdx, endIdx, openLine := locateConfigBlock(body)
	if startIdx < 0 {
		return nil
	}
	inner := body[startIdx:endIdx]
	return parseConfigEntries(inner, relPath, openLine, pkg)
}

// locateConfigBlock returns (inner-start, inner-end, line-of-opening-brace)
// for a top-level `config: { ... }` block, or (-1,-1,0) if absent. It
// honors string literals and `//` / `/* */` comments when balancing braces.
func locateConfigBlock(text string) (int, int, int) {
	re := regexp.MustCompile(`(?m)^[ \t]*config\s*:\s*\{`)
	loc := re.FindStringIndex(text)
	if loc == nil {
		return -1, -1, 0
	}
	openLine := 1 + strings.Count(text[:loc[1]-1], "\n")
	pos := loc[1]
	depth := 1
	inStr := false
	for pos < len(text) {
		c := text[pos]
		if inStr {
			if c == '\\' && pos+1 < len(text) {
				pos += 2
				continue
			}
			if c == '"' {
				inStr = false
			}
			pos++
			continue
		}
		if c == '/' && pos+1 < len(text) {
			if text[pos+1] == '/' {
				if nl := strings.IndexByte(text[pos:], '\n'); nl >= 0 {
					pos += nl
				} else {
					pos = len(text)
				}
				continue
			}
			if text[pos+1] == '*' {
				if end := strings.Index(text[pos+2:], "*/"); end >= 0 {
					pos += 2 + end + 2
					continue
				}
				return -1, -1, 0
			}
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return loc[1], pos, openLine
			}
		}
		pos++
	}
	return -1, -1, 0
}

// parseConfigEntries walks the inner text of a config block and emits
// one ContractElement per top-level entry.
func parseConfigEntries(inner, relPath string, openLine int, pkg string) []*contractpb.ContractElement {
	var out []*contractpb.ContractElement
	clean := stripCommentsKeepLines(inner)

	pos := 0
	for pos < len(clean) {
		for pos < len(clean) && (isSpace(clean[pos]) || clean[pos] == ',') {
			pos++
		}
		if pos >= len(clean) {
			break
		}
		keyStart := pos
		var key string
		if clean[pos] == '"' {
			end := pos + 1
			for end < len(clean) && clean[end] != '"' {
				if clean[end] == '\\' && end+1 < len(clean) {
					end += 2
					continue
				}
				end++
			}
			if end >= len(clean) {
				break
			}
			key = clean[pos+1 : end]
			pos = end + 1
		} else if isIdentStart(clean[pos]) {
			end := pos
			for end < len(clean) && isIdentCont(clean[end]) {
				end++
			}
			key = clean[pos:end]
			pos = end
		} else {
			pos++
			continue
		}
		lineOfKey := openLine + strings.Count(clean[:keyStart], "\n")

		for pos < len(clean) && isSpace(clean[pos]) {
			pos++
		}
		if pos >= len(clean) || clean[pos] != ':' {
			continue
		}
		pos++
		for pos < len(clean) && isSpace(clean[pos]) {
			pos++
		}
		if pos >= len(clean) {
			break
		}
		var valStr string
		if clean[pos] == '{' {
			end := matchBrace(clean, pos)
			if end < 0 {
				break
			}
			valStr = clean[pos : end+1]
			pos = end + 1
		} else {
			end := pos
			depth := 0
			inStr := false
			for end < len(clean) {
				c := clean[end]
				if inStr {
					if c == '\\' && end+1 < len(clean) {
						end += 2
						continue
					}
					if c == '"' {
						inStr = false
					}
					end++
					continue
				}
				if c == '"' {
					inStr = true
				} else if c == '{' || c == '[' {
					depth++
				} else if c == '}' || c == ']' {
					if depth == 0 {
						break
					}
					depth--
				} else if c == ',' && depth == 0 {
					break
				}
				end++
			}
			valStr = strings.TrimSpace(clean[pos:end])
			pos = end
		}

		if e := buildElement(key, valStr, relPath, lineOfKey, pkg); e != nil {
			out = append(out, e)
		}
	}
	return out
}

func buildElement(key, val, relPath string, line int, pkg string) *contractpb.ContractElement {
	id := fmt.Sprintf("cml:%s/%s", pkg, key)
	meta := map[string]interface{}{}
	if strings.HasPrefix(val, "{") {
		body := val[1 : len(val)-1]
		if v := extractAttr(body, "type"); v != "" {
			meta["type"] = v
		}
		if v := extractAttr(body, "max_size"); v != "" {
			meta["max_size"] = v
		}
		if v := extractAttr(body, "max_count"); v != "" {
			meta["max_count"] = v
		}
		if v := extractAttr(body, "element"); v != "" {
			meta["element"] = v
		}
		if v := extractAttr(body, "default"); v != "" {
			meta["default"] = strings.Trim(v, `"`)
		}
	} else {
		// Shorthand scalar form.
		meta["type"] = "unknown"
		meta["default"] = strings.Trim(val, `"`)
	}
	ecoMeta, _ := structpb.NewStruct(meta)
	return &contractpb.ContractElement{
		Id:        id,
		Kind:      contractpb.ContractElementKind_CONFIG_KNOB,
		Ecosystem: "cml",
		Library:   pkg,
		Location: &commonpb.SourceLocation{
			Path: relPath,
			Line: uint32(line),
		},
		EcosystemMeta: ecoMeta,
	}
}

// ----- attribute extraction (regex-style for a flat object) -----

var reAttr = regexp.MustCompile(`\b(type|max_size|max_count|element|default)\s*:\s*("([^"\\]|\\.)*"|[^,}\s]+)`)

func extractAttr(body, attr string) string {
	for _, m := range reAttr.FindAllStringSubmatch(body, -1) {
		if m[1] == attr {
			v := m[2]
			if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
				return v[1 : len(v)-1]
			}
			return v
		}
	}
	return ""
}

// ----- helpers -----

// packageDir derives a stable per-package name from a .cml's repo-relative
// path. CML files conventionally live under <package>/meta/<name>.cml; we
// take the parent of "meta" (or the immediate dir if "meta" isn't there)
// and join it with the file basename, producing a name like
// "config_example/cpp" or "bt-host/bt-host".
func packageDir(relPath string) string {
	base := strings.TrimSuffix(filepath.Base(relPath), ".cml")
	dir := filepath.Dir(relPath)
	parent := filepath.Base(filepath.Dir(dir)) // typically skips "meta"
	if parent != "" && parent != "." && parent != string(filepath.Separator) {
		return parent + "/" + base
	}
	return base
}

func libraryInScope(lib string, scope adapters.ScopeConfig) bool {
	if len(scope.Libraries) == 0 {
		return true
	}
	for _, pat := range scope.Libraries {
		if pat == lib || (strings.HasSuffix(pat, "/*") && strings.HasPrefix(lib, strings.TrimSuffix(pat, "*"))) {
			return true
		}
	}
	return false
}

func matchBrace(s string, openIdx int) int {
	if openIdx >= len(s) || s[openIdx] != '{' {
		return -1
	}
	depth := 1
	inStr := false
	i := openIdx + 1
	for i < len(s) {
		c := s[i]
		if inStr {
			if c == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}

func stripCommentsKeepLines(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	inStr := false
	for i < len(s) {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
				continue
			}
			if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '/' && i+1 < len(s) {
			if s[i+1] == '/' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
				continue
			}
			if s[i+1] == '*' {
				end := strings.Index(s[i+2:], "*/")
				if end < 0 {
					return b.String()
				}
				for _, ch := range s[i : i+2+end+2] {
					if ch == '\n' {
						b.WriteByte('\n')
					}
				}
				i = i + 2 + end + 2
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func isSpace(c byte) bool      { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
func isIdentStart(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' }
func isIdentCont(c byte) bool  { return isIdentStart(c) || (c >= '0' && c <= '9') }
