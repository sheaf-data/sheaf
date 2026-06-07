// Package versionscheme defines the per-ecosystem ordering rules for
// the version strings carried on VersionConstraint records.
//
// Different IDLs version their elements differently:
//
//   - FIDL uses integer API levels with symbolic anchors: numeric
//     (7, 13, 17), plus HEAD (the dev tip) and NEXT (the next stable
//     after HEAD) and LEGACY.
//   - Most other ecosystems use semver (1.2.3), date stamps
//     (2025-04-01), or git tags.
//
// Scheme captures the comparison and "is removed at this target"
// question without baking any ecosystem's syntax into the scanner.
//
// Callers retrieve a Scheme via For(ecosystem); unknown ecosystems
// fall back to FIDL so existing scans behave the way they always did.
package versionscheme

import "strings"

// Scheme is the per-ecosystem rule set for interpreting version
// strings. Implementations should be safe to share across goroutines
// (and are typically stateless).
type Scheme interface {
	// Name identifies the scheme (used for diagnostics and tests).
	Name() string

	// IsRemovedAt reports whether an element marked
	// @available(removed=removedIn) is no longer present at the
	// given target level. removedIn == "" → never removed.
	//
	// Implementations should be permissive on input: "" target means
	// "dev tip / latest"; garbage strings default to "not removed"
	// rather than panicking.
	IsRemovedAt(removedIn, target string) bool
}

// For returns the Scheme keyed by ecosystem name. Unknown ecosystems
// fall back to FIDL — that keeps existing scans (which don't always
// stamp ecosystem on their VersionConstraint records) working.
//
// Add new schemes by registering them here.
func For(ecosystem string) Scheme {
	switch strings.ToLower(ecosystem) {
	case "fidl", "":
		return fidlScheme{}
	}
	return fidlScheme{}
}

// FIDL returns the FIDL versioning scheme.
func FIDL() Scheme { return fidlScheme{} }

// fidlScheme implements the @available semantics: numeric levels
// (7, 13, 17) plus the symbolic anchors HEAD (dev tip), NEXT (the
// next stable after HEAD), and LEGACY (the very first level).
type fidlScheme struct{}

func (fidlScheme) Name() string { return "fidl" }

func (fidlScheme) IsRemovedAt(removedIn, target string) bool {
	if removedIn == "" {
		return false
	}
	rmNum, rmSym := parseFIDLLevel(removedIn)
	tgtNum, tgtSym := parseFIDLLevel(target)
	switch {
	case tgtSym == "HEAD" || target == "":
		// At the dev tip, anything numeric or HEAD-removed is gone;
		// NEXT is one step further out, still ahead.
		return rmSym != "NEXT"
	case tgtSym != "":
		// Both symbolic — call it removed only when the target IS the
		// removal marker.
		return rmSym == tgtSym
	default:
		if rmSym != "" {
			// Symbolic removed against a concrete numeric target →
			// HEAD and NEXT are after any concrete shippable level.
			return false
		}
		return rmNum <= tgtNum
	}
}

// parseFIDLLevel returns (numeric, "") for digit strings, (0, SYMBOL)
// for HEAD/NEXT/LEGACY, or (0, "") for anything else.
func parseFIDLLevel(v string) (int, string) {
	switch strings.ToUpper(v) {
	case "HEAD":
		return 0, "HEAD"
	case "NEXT":
		return 0, "NEXT"
	case "LEGACY":
		return 0, "LEGACY"
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, ""
		}
		n = n*10 + int(r-'0')
	}
	return n, ""
}
