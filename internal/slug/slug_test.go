package slug

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"fuchsia.io/Directory.Open", "fuchsia-io-directory-open"},
		{"ffx component show --json", "ffx-component-show-json"},
		{"Hello, World!", "hello-world"},
		{"already-slug", "already-slug"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"___underscores___", "underscores"},
		{"Mixed.CASE.value", "mixed-case-value"},
		{"123-numeric-start", "123-numeric-start"},
		{"!@#$%", "_"},
		{"", "_"},
	}
	for _, tc := range cases {
		got := Slugify(tc.in)
		if got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlugifyUnique_Disambiguates(t *testing.T) {
	a := SlugifyUnique("Foo.Bar")
	b := SlugifyUnique("foo-bar")
	// Both slugify to "foo-bar" but should produce distinct unique slugs.
	if a == b {
		t.Errorf("expected distinct slugs, both got %q", a)
	}
	if !strings.HasPrefix(a, "foo-bar-") {
		t.Errorf("expected prefix foo-bar-, got %q", a)
	}
}

func TestHashStability(t *testing.T) {
	h1 := Hash("fuchsia.io/Directory.Open")
	h2 := Hash("fuchsia.io/Directory.Open")
	if h1 != h2 {
		t.Errorf("Hash is not stable: %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("Hash length = %d, want 64", len(h1))
	}
}

func TestShortHash(t *testing.T) {
	h := ShortHash("anything")
	if len(h) != 12 {
		t.Errorf("ShortHash length = %d, want 12", len(h))
	}
}
