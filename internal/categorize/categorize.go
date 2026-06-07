// Package categorize assigns repo-relative file paths to one or more
// taxonomy buckets, per the project's source map (on disk:
// categorization-rules.textproto).
//
// The source map declares which paths in a project's source tree
// produce which kind of evidence (docs.reference, docs.concepts,
// tests.integration_tests, examples, ...). The taxonomy itself is
// fixed by the CoverageProfile proto schema; this package doesn't
// validate that dotted paths land on real fields — that's the
// indexer's job. Categorize is path-pattern matching.
package categorize

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/glob"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
)

// Categorizer is the compiled form of a Rules message.
// Construction validates patterns once; Categorize calls then need no
// further validation.
type Categorizer struct {
	rules []compiledCategory
}

type compiledCategory struct {
	DottedPath      string
	Paths           []string
	ExcludePaths    []string
	SectionExcludes []string // lowercased substrings; matched against any heading in sectionPath
	SectionIncludes []string // lowercased substrings; at least one must match if non-empty
}

// New compiles a Rules message into a Categorizer.
func New(r *categorizationpb.Rules) (*Categorizer, error) {
	if r == nil {
		return nil, errors.New("categorize: rules is nil")
	}
	cats := make([]compiledCategory, 0, len(r.GetCategory()))
	for i, c := range r.GetCategory() {
		if c.GetDottedPath() == "" {
			return nil, fmt.Errorf("categorize: rule[%d].dotted_path is empty", i)
		}
		cats = append(cats, compiledCategory{
			DottedPath:      c.GetDottedPath(),
			Paths:           append([]string(nil), c.GetPaths()...),
			ExcludePaths:    append([]string(nil), c.GetExcludePaths()...),
			SectionExcludes: lowerAll(c.GetSectionExcludes()),
			SectionIncludes: lowerAll(c.GetSectionIncludes()),
		})
	}
	return &Categorizer{rules: cats}, nil
}

// Categorize returns the sorted, de-duplicated set of category
// dotted-paths that path matches. A category with no `paths`
// (e.g. docs.reference) is never produced here — those buckets are
// populated by their adapters, not by path matching.
//
// sectionPath is the heading stack the claim sits under (see
// DocClaim.section_path). When non-empty, per-category
// section_excludes / section_includes filters apply. When empty
// (non-markdown source or preamble), section filters are not
// consulted. Callers with no section context pass nil.
func (c *Categorizer) Categorize(path string, sectionPath []string) ([]string, error) {
	cats, _, err := c.CategorizeWithDecision(path, sectionPath)
	return cats, err
}

// CategorizeWithDecision is Categorize plus a sectionExcluded
// signal. sectionExcluded is true when at least one category's
// `paths` glob matched but the claim was rejected by that
// category's section filter. It lets the caller distinguish
// "no rule matched this path" (cats empty, sectionExcluded false →
// safe to fall back to a default bucket) from "a rule matched
// but the section filter said no" (cats empty, sectionExcluded
// true → respect the project's intent and don't fall back).
//
// When at least one rule fully matched (path + section), cats is
// non-empty and sectionExcluded reflects whether other matching
// rules were also section-filtered. The indexer cares only about
// the "cats empty + sectionExcluded true" case.
func (c *Categorizer) CategorizeWithDecision(path string, sectionPath []string) ([]string, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	hits := make(map[string]struct{})
	sectionExcluded := false
	for _, cat := range c.rules {
		if len(cat.Paths) == 0 {
			continue
		}
		ok, err := glob.MatchAnyIncludeExclude(cat.Paths, cat.ExcludePaths, path)
		if err != nil {
			return nil, false, fmt.Errorf("categorize: category %q: %w", cat.DottedPath, err)
		}
		if !ok {
			continue
		}
		if !sectionFilterApplies(cat, sectionPath) {
			sectionExcluded = true
			continue
		}
		hits[cat.DottedPath] = struct{}{}
	}
	if len(hits) == 0 {
		return nil, sectionExcluded, nil
	}
	out := make([]string, 0, len(hits))
	for k := range hits {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, sectionExcluded, nil
}

// sectionFilterApplies returns true if cat's section_excludes /
// section_includes allow the given heading stack. An empty
// sectionPath skips both filters (so preamble and non-markdown
// claims aren't filtered out by section rules).
func sectionFilterApplies(cat compiledCategory, sectionPath []string) bool {
	if len(sectionPath) == 0 {
		return true
	}
	if len(cat.SectionExcludes) > 0 {
		for _, h := range sectionPath {
			lh := strings.ToLower(h)
			for _, pat := range cat.SectionExcludes {
				if strings.Contains(lh, pat) {
					return false
				}
			}
		}
	}
	if len(cat.SectionIncludes) > 0 {
		matched := false
		for _, h := range sectionPath {
			lh := strings.ToLower(h)
			for _, pat := range cat.SectionIncludes {
				if strings.Contains(lh, pat) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func lowerAll(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToLower(s)
	}
	return out
}

// AdapterPopulated returns the dotted paths declared in the rules
// but without any `paths` patterns — these are the buckets that an
// adapter is expected to populate directly (docs.reference being the
// canonical example).
func (c *Categorizer) AdapterPopulated() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, cat := range c.rules {
		if len(cat.Paths) == 0 {
			out = append(out, cat.DottedPath)
		}
	}
	sort.Strings(out)
	return out
}

// AllDeclared returns every dotted path declared in the rules,
// regardless of whether it's path-matched or adapter-populated.
func (c *Categorizer) AllDeclared() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.rules))
	for _, cat := range c.rules {
		out = append(out, cat.DottedPath)
	}
	sort.Strings(out)
	return out
}
