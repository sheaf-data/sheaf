// Package pwfacade is a build-graph recognizer that parses Pigweed
// pw_facade()/pw_source_set()/pw_static_library() declarations out of
// BUILD.gn files and builds a facade→backend map by the Pigweed name-
// convention heuristic: a pw_facade("X") in module pw_Y is backed by
// every sibling pw_Y_* module that declares pw_source_set("X") (or
// pw_static_library("X")) with the same name.
//
// The full BUILDCONFIG.gn backend-variable resolution is out of scope
// for v1; the name-convention heuristic catches the structural
// relationships that matter for the report.
//
// The recognizer is plugged into the framework via the Recognizer
// interface (Name + Walk). Its output is exposed as adapters.BuildHints,
// specifically the FacadeOf method. IsPublic always returns
// (false, false) in v1 — no opinion. Adapters that consult IsPublic
// must treat (false, false) as "treat as public."
package pwfacade

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

const Name = "pwfacade"
const Version = "0.1.0"

// Config tunes which BUILD.gn files the recognizer considers.
type Config struct {
	Include []string // defaults to ["**/BUILD.gn"]
	Exclude []string
}

// Recognizer walks BUILD.gn files and produces facade-relationship
// hints. Implements buildgraph.Recognizer; its output is an
// adapters.BuildHints implementation.
type Recognizer struct {
	include []string
	exclude []string
}

func New(cfg Config) *Recognizer {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/BUILD.gn"}
	}
	return &Recognizer{include: include, exclude: cfg.Exclude}
}

func (r *Recognizer) Name() string { return Name }

// Walk scans every matching BUILD.gn under repoRoot, parses the
// pw_facade / pw_source_set / pw_static_library declarations it
// contains, and returns a BuildHints reporting facade→backend
// relationships.
func (r *Recognizer) Walk(ctx context.Context, repoRoot string) (adapters.BuildHints, error) {
	// module -> set of facade names
	facades := map[string]map[string]bool{}
	// module -> source-set name -> list of public-header rel paths
	sources := map[string]map[string][]string{}

	err := adapters.WalkMatching(repoRoot, r.include, r.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return nil
		}
		module := moduleFromBuildGN(rel)
		if module == "" {
			return nil
		}
		decls, perr := parseBuildGN(string(body))
		if perr != nil {
			log.Printf("pwfacade: %s: %v", rel, perr)
			return nil
		}
		for _, d := range decls {
			switch d.kind {
			case "pw_facade":
				if facades[module] == nil {
					facades[module] = map[string]bool{}
				}
				facades[module][d.name] = true
			case "pw_source_set", "pw_static_library":
				if sources[module] == nil {
					sources[module] = map[string][]string{}
				}
				// Public headers are repo-relative: prefix module dir.
				moduleDir := path.Dir(rel)
				abs := make([]string, 0, len(d.public))
				for _, h := range d.public {
					if h == "" {
						continue
					}
					abs = append(abs, path.Join(moduleDir, h))
				}
				sources[module][d.name] = append(sources[module][d.name], abs...)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	h := newHints(facades, sources)
	return h, nil
}

// --- Hints implementation ---

// hints is the BuildHints implementation produced by Walk. It maps a
// header path to (facade module, backend module) when the header
// belongs to a backend that backs a facade.
type hints struct {
	// fileBacks: header relpath -> (facade module, backend module)
	fileBacks map[string][2]string
	// moduleBacks: backend module -> facade module, for symbol-level
	// convention matching (FacadeSymbol).
	moduleBacks map[string]string
}

func newHints(facades map[string]map[string]bool, sources map[string]map[string][]string) *hints {
	out := &hints{fileBacks: map[string][2]string{}, moduleBacks: map[string]string{}}

	// For each facade module, a backend is a sibling module whose name
	// starts with facadeModule + "_". A backend's implementation
	// source-set is identified by name, trying two conventions:
	//
	//   1. a source-set named after the facade declaration (e.g. a
	//      pw_facade("X") backed by a sibling's pw_source_set("X")), and
	//   2. a source-set named after the backend module itself — the
	//      canonical Pigweed convention, where module pw_X_Y provides
	//      target :pw_X_Y that backs facade pw_X. The recognizer's
	//      contract is "pw_facade in module pw_Y is backed by pw_Y_*
	//      modules", and Pigweed backends (pw_log_basic, pw_log_tokenized,
	//      …) name their primary source-set after the module, not the
	//      facade — so convention (1) alone never matches them.
	//
	// Each matched backend header → (facade, backend).
	facadeModules := make([]string, 0, len(facades))
	for m := range facades {
		facadeModules = append(facadeModules, m)
	}
	sort.Strings(facadeModules)

	backendModules := make([]string, 0, len(sources))
	for b := range sources {
		backendModules = append(backendModules, b)
	}
	sort.Strings(backendModules)

	for _, facadeModule := range facadeModules {
		prefix := facadeModule + "_"
		facadeNames := make([]string, 0, len(facades[facadeModule]))
		for n := range facades[facadeModule] {
			facadeNames = append(facadeNames, n)
		}
		sort.Strings(facadeNames)

		for _, backendModule := range backendModules {
			if !strings.HasPrefix(backendModule, prefix) {
				continue
			}
			sets := sources[backendModule]
			// Candidate source-set names: the facade names (convention 1)
			// then the backend module name (convention 2).
			candidates := make([]string, 0, len(facadeNames)+1)
			candidates = append(candidates, facadeNames...)
			candidates = append(candidates, backendModule)
			for _, setName := range candidates {
				headers, ok := sets[setName]
				if !ok {
					continue
				}
				for _, hpath := range headers {
					// First registration wins. Different facades naming
					// the same header would be a config bug; don't
					// silently overwrite.
					if _, exists := out.fileBacks[hpath]; exists {
						continue
					}
					out.fileBacks[hpath] = [2]string{facadeModule, backendModule}
					if _, exists := out.moduleBacks[backendModule]; !exists {
						out.moduleBacks[backendModule] = facadeModule
					}
				}
			}
		}
	}
	return out
}

func (h *hints) IsPublic(string) (bool, bool) { return false, false }

func (h *hints) FacadeOf(relPath string) (facade string, backend string, ok bool) {
	if v, ok := h.fileBacks[relPath]; ok {
		return v[0], v[1], true
	}
	return "", "", false
}

// FacadeModule reports the facade a backend module backs, independent of
// any specific header path — the post-pass needs this because a Pigweed
// backend defines its facade symbols in public/<module>/*.h headers that
// the build graph does not list directly (it lists the public_overrides/
// glue header instead).
func (h *hints) FacadeModule(module string) (string, bool) {
	f, ok := h.moduleBacks[module]
	return f, ok
}

// FacadeSymbol encodes Pigweed's backend-interface-macro convention: a
// facade module pw_<x> exposes the public macro PW_<X> (X = upper(x)),
// and a backend satisfies it by defining the handler macro PW_HANDLE_<X>.
// The two names are deliberately different, so the generic same-name
// match never connects them — this maps PW_HANDLE_<X> back to PW_<X>.
// Returns ("", false) when the backend module is not a recognized
// backend or the name is not the handler macro (same-name match applies).
func (h *hints) FacadeSymbol(backendModule, backendLocalName string) (string, bool) {
	facadeModule, ok := h.moduleBacks[backendModule]
	if !ok {
		return "", false
	}
	stem := strings.TrimPrefix(facadeModule, "pw_")
	if stem == "" || stem == facadeModule {
		return "", false
	}
	x := strings.ToUpper(stem)
	if backendLocalName == "PW_HANDLE_"+x {
		return "PW_" + x, true
	}
	return "", false
}

// --- BUILD.gn parsing ---

// declaration is one pw_facade / pw_source_set / pw_static_library
// block extracted from a BUILD.gn file.
type declaration struct {
	kind   string   // "pw_facade", "pw_source_set", "pw_static_library"
	name   string   // the string literal arg (e.g. "system_clock")
	public []string // entries from the `public = [ ... ]` list
}

var (
	// targetLine matches the opening of a target declaration:
	//   pw_facade("system_clock") {
	//   pw_source_set("X") {
	// Trailing whitespace before `{` is allowed; the line may have
	// content after, but we look for `{` to enter the block.
	targetLine = regexp.MustCompile(`^\s*(pw_facade|pw_source_set|pw_static_library)\s*\(\s*"([^"]+)"\s*\)\s*\{`)

	// publicLine matches the start of a `public = [ ... ]` assignment.
	// We accept the same-line list-open `[` and grab content until the
	// closing `]`.
	publicAssign = regexp.MustCompile(`^\s*public\s*=\s*\[`)

	// stringLiteral grabs every "..."-quoted token on a line. Used to
	// extract header paths from a public list body.
	stringLiteral = regexp.MustCompile(`"([^"]+)"`)
)

// parseBuildGN extracts pw_facade / pw_source_set / pw_static_library
// declarations out of GN source. The parser tracks brace depth so it
// correctly delimits the block scope without being a full GN parser;
// nested braces in conditionals or inner blocks inside the target
// don't fool the closing-brace finder.
//
// Returns a non-nil error only on truly malformed input — e.g. an
// unterminated target block at EOF. Per-target syntax issues are
// silently skipped so one bad target doesn't drop the others.
func parseBuildGN(body string) ([]declaration, error) {
	lines := strings.Split(body, "\n")
	var out []declaration
	for i := 0; i < len(lines); i++ {
		m := targetLine.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		d := declaration{kind: m[1], name: m[2]}
		// Walk the block tracking brace depth from this line's open `{`.
		depth := 1
		// Allow the opening line itself to contain content past `{` —
		// rare in Pigweed BUILD.gn but cheap to handle.
		startCol := strings.Index(lines[i], "{") + 1
		// Process the remainder of the opening line, then subsequent.
		if startCol < len(lines[i]) {
			depth += braceDelta(lines[i][startCol:])
		}
		j := i + 1
		for ; j < len(lines) && depth > 0; j++ {
			line := lines[j]
			// public = [ ... ] block: detect, then collect headers
			// across as many lines as needed until the matching `]`.
			if publicAssign.MatchString(line) {
				// Strip everything up to and including the opening `[`.
				idx := strings.Index(line, "[")
				rem := line[idx+1:]
				headers, consumed := parsePublicList(rem, lines[j+1:])
				d.public = append(d.public, headers...)
				// Account for the brace delta across all the public-list
				// lines we just consumed (depth-wise, brackets don't
				// matter, but braces inside string literals don't occur
				// in normal public lists; depth stays the same).
				j += consumed
				continue
			}
			depth += braceDelta(line)
		}
		if depth > 0 {
			return out, fmt.Errorf("unterminated %s(%q) block starting at line %d", d.kind, d.name, i+1)
		}
		out = append(out, d)
		i = j - 1
	}
	return out, nil
}

// parsePublicList parses the body of a `public = [ ... ]` list. `first`
// is the remainder of the line after the opening `[`; `rest` is the
// subsequent lines. Returns the list of string literals and how many
// extra lines were consumed.
func parsePublicList(first string, rest []string) ([]string, int) {
	var headers []string
	scan := func(s string) (done bool) {
		// Strip a trailing comment (`#` outside strings); naive but
		// safe for Pigweed's BUILD.gn style.
		if hash := strings.Index(s, "#"); hash >= 0 {
			// Only strip if hash isn't inside a string literal. Simple
			// heuristic: count quotes before the hash; if even, strip.
			if strings.Count(s[:hash], `"`)%2 == 0 {
				s = s[:hash]
			}
		}
		for _, m := range stringLiteral.FindAllStringSubmatch(s, -1) {
			headers = append(headers, m[1])
		}
		return strings.Contains(s, "]")
	}
	if scan(first) {
		return headers, 0
	}
	for k, line := range rest {
		if scan(line) {
			return headers, k + 1
		}
	}
	// Unterminated list — return what we have. The outer brace-depth
	// tracker will surface the malformed-block error separately if the
	// whole target is unterminated.
	return headers, len(rest)
}

// braceDelta returns (open braces - close braces) on a line, ignoring
// braces inside double-quoted string literals. Comments after `#` are
// ignored.
func braceDelta(line string) int {
	if hash := strings.Index(line, "#"); hash >= 0 {
		if strings.Count(line[:hash], `"`)%2 == 0 {
			line = line[:hash]
		}
	}
	inStr := false
	delta := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case '\\':
			if inStr && i+1 < len(line) {
				i++
			}
		case '"':
			inStr = !inStr
		case '{':
			if !inStr {
				delta++
			}
		case '}':
			if !inStr {
				delta--
			}
		}
	}
	return delta
}

// moduleFromBuildGN extracts the GN module name from a BUILD.gn path.
// It returns the basename of the file's parent directory:
//
//	"pw_chrono/BUILD.gn" -> "pw_chrono"
//	"pw_chrono/backend/BUILD.gn" -> "backend"
//
// Returns "" if the path has no parent directory.
func moduleFromBuildGN(rel string) string {
	dir := path.Dir(rel)
	if dir == "" || dir == "." || dir == "/" {
		return ""
	}
	// Use filepath.Base after normalizing separators.
	return filepath.Base(dir)
}
