// Package glob implements doublestar (** recursive) glob matching for
// repo-relative paths. We don't use stdlib filepath.Match because it
// doesn't support ** and we need that for our config globs.
//
// Semantics:
//   - matches any sequence of non-separator characters
//     ** matches any sequence of characters including separators
//     ?  matches any single non-separator character
//     [abc] matches one of the characters listed
//
// Paths are always forward-slash separated; on Windows this means
// callers must normalize before matching.
package glob

import (
	"errors"
	"strings"
)

// Match reports whether path matches the given pattern.
// path must be forward-slash separated and not have a leading slash.
func Match(pattern, path string) (bool, error) {
	// Normalize: strip leading "./" if present.
	path = strings.TrimPrefix(path, "./")
	pattern = strings.TrimPrefix(pattern, "./")
	return matchSegments(splitSegments(pattern), splitSegments(path))
}

// MatchAny returns true if path matches any of the patterns.
func MatchAny(patterns []string, path string) (bool, error) {
	for _, p := range patterns {
		ok, err := Match(p, path)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// MatchAnyIncludeExclude returns true if path matches any of `include`
// AND does not match any of `exclude`. Empty `include` is treated as
// "match everything" so an exclude-only ruleset works as expected.
func MatchAnyIncludeExclude(include, exclude []string, path string) (bool, error) {
	if len(include) > 0 {
		ok, err := MatchAny(include, path)
		if err != nil || !ok {
			return false, err
		}
	}
	if len(exclude) > 0 {
		ok, err := MatchAny(exclude, path)
		if err != nil {
			return false, err
		}
		if ok {
			return false, nil
		}
	}
	return true, nil
}

func splitSegments(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// matchSegments walks pattern segments against path segments,
// handling ** specially: it can match zero-or-more path segments.
func matchSegments(pat, path []string) (bool, error) {
	switch {
	case len(pat) == 0:
		return len(path) == 0, nil
	case pat[0] == "**":
		// ** matches zero or more segments; recurse with each split.
		rest := pat[1:]
		// First try matching ** against zero segments.
		if ok, err := matchSegments(rest, path); err != nil || ok {
			return ok, err
		}
		// Then match ** against 1, 2, … remaining segments.
		for i := 1; i <= len(path); i++ {
			if ok, err := matchSegments(rest, path[i:]); err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	case len(path) == 0:
		return false, nil
	default:
		ok, err := matchSegment(pat[0], path[0])
		if err != nil || !ok {
			return false, err
		}
		return matchSegments(pat[1:], path[1:])
	}
}

// matchSegment matches a single path segment against a single pattern
// segment that does NOT contain **.
func matchSegment(pat, seg string) (bool, error) {
	pi, si := 0, 0
	starPi, starSi := -1, 0
	for si < len(seg) {
		if pi < len(pat) {
			switch pat[pi] {
			case '?':
				pi++
				si++
				continue
			case '*':
				starPi = pi
				starSi = si
				pi++
				continue
			case '[':
				end := strings.IndexByte(pat[pi:], ']')
				if end < 0 {
					return false, errors.New("glob: unterminated character class")
				}
				class := pat[pi+1 : pi+end]
				if !classMatch(class, seg[si]) {
					if starPi < 0 {
						return false, nil
					}
					pi = starPi + 1
					starSi++
					si = starSi
					continue
				}
				pi += end + 1
				si++
				continue
			default:
				if pat[pi] == seg[si] {
					pi++
					si++
					continue
				}
			}
		}
		if starPi >= 0 {
			pi = starPi + 1
			starSi++
			si = starSi
			continue
		}
		return false, nil
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat), nil
}

func classMatch(class string, b byte) bool {
	negate := false
	if len(class) > 0 && class[0] == '!' {
		negate = true
		class = class[1:]
	}
	matched := false
	for i := 0; i < len(class); i++ {
		// Range a-z.
		if i+2 < len(class) && class[i+1] == '-' {
			if b >= class[i] && b <= class[i+2] {
				matched = true
			}
			i += 2
			continue
		}
		if class[i] == b {
			matched = true
		}
	}
	if negate {
		return !matched
	}
	return matched
}
