// Package slug normalizes strings for use as filenames, cache keys,
// and URL fragments. Sheaf's element IDs aren't always slug-safe
// (they contain dots, slashes, angle brackets, parentheses), so we
// route them through Slugify before disk use.
package slug

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// Slugify produces a slug-safe, lowercased version of s. The mapping
// is NOT injective — multiple inputs can collide (e.g. "Foo.Bar" and
// "foo-bar" both -> "foo-bar"). Use SlugifyUnique when uniqueness
// matters for disk paths.
func Slugify(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastDash := true // suppress leading dashes
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastDash = false
		case lastDash:
			// skip; collapse runs of separator chars
		default:
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "_"
	}
	return out
}

// SlugifyUnique returns Slugify(s) with a short hash suffix derived
// from the original string. Guarantees disambiguation when the same
// slug would otherwise be produced from different inputs.
func SlugifyUnique(s string) string {
	base := Slugify(s)
	h := sha256.Sum256([]byte(s))
	return base + "-" + hex.EncodeToString(h[:4])
}

// Hash returns a hex-encoded SHA-256 of s. Used as a content key
// when the input isn't suitable for direct slugging.
func Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ShortHash returns the first 12 hex characters of SHA-256(s).
// Suitable for compact identifiers (cache key fragments, IDs).
func ShortHash(s string) string {
	return Hash(s)[:12]
}
