package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		// Literal matches
		{"exact match", "foo/bar.go", "foo/bar.go", true},
		{"mismatch", "foo/bar.go", "foo/baz.go", false},
		{"prefix mismatch", "foo/bar.go", "qux/bar.go", false},

		// * within a segment
		{"star prefix", "*.go", "main.go", true},
		{"star middle", "foo*.go", "foobar.go", true},
		{"star no cross-segment", "*.go", "a/b.go", false},
		{"star empty", "*.go", ".go", true},
		{"star at end", "foo/*", "foo/bar", true},
		{"star matches no dir", "foo/*", "foo/bar/baz", false},

		// ** recursive
		{"doublestar leading", "**/foo.go", "foo.go", true},
		{"doublestar leading nested", "**/foo.go", "a/b/c/foo.go", true},
		{"doublestar middle", "src/**/test.go", "src/test.go", true},
		{"doublestar middle nested", "src/**/test.go", "src/a/b/test.go", true},
		{"doublestar trailing", "src/**", "src/a/b/c.go", true},
		{"doublestar zero segments", "src/**/test.go", "src/test.go", true},

		// ? single char
		{"single char", "f?o.go", "foo.go", true},
		{"single char miss", "f?o.go", "fo.go", false},

		// [class]
		{"class match", "[fb]oo", "foo", true},
		{"class second match", "[fb]oo", "boo", true},
		{"class no match", "[fb]oo", "zoo", false},
		{"class range", "src/[a-c]/x", "src/b/x", true},
		{"class range out", "src/[a-c]/x", "src/d/x", false},
		{"class negation", "[!fb]oo", "zoo", true},
		{"class negation hits", "[!fb]oo", "foo", false},

		// Combinations
		{"fidl test path", "sdk/fidl/**/*.fidl", "sdk/fidl/fuchsia.io/io.fidl", true},
		{"fidl excludes test", "sdk/fidl/**/test/**", "sdk/fidl/fuchsia.io/test/x.fidl", true},
		{"normalized leading dotslash", "./foo/bar", "foo/bar", true},
		{"both have leading dotslash", "./foo/bar", "./foo/bar", true},
		{"trailing star matches dir", "src/**/test/**", "src/a/test/b.cc", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Match(tc.pattern, tc.path)
			if err != nil {
				t.Fatalf("Match(%q, %q) error: %v", tc.pattern, tc.path, err)
			}
			if got != tc.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

func TestMatchUnterminatedClass(t *testing.T) {
	if _, err := Match("[abc", "anything"); err == nil {
		t.Errorf("expected error for unterminated class")
	}
}

func TestMatchAny(t *testing.T) {
	pats := []string{"foo/*.go", "bar/**/test.go"}
	cases := []struct {
		path string
		want bool
	}{
		{"foo/x.go", true},
		{"foo/y.txt", false},
		{"bar/a/b/test.go", true},
		{"bar/test.go", true},
		{"baz/test.go", false},
	}
	for _, tc := range cases {
		got, err := MatchAny(pats, tc.path)
		if err != nil {
			t.Fatalf("MatchAny error: %v", err)
		}
		if got != tc.want {
			t.Errorf("MatchAny(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestMatchAnyIncludeExclude(t *testing.T) {
	cases := []struct {
		name    string
		include []string
		exclude []string
		path    string
		want    bool
	}{
		{"include only hit", []string{"src/**/*.go"}, nil, "src/a/b.go", true},
		{"include only miss", []string{"src/**/*.go"}, nil, "docs/a.md", false},
		{"include hit, exclude miss", []string{"src/**/*.go"}, []string{"src/vendor/**"}, "src/a/b.go", true},
		{"include hit, exclude hit", []string{"src/**/*.go"}, []string{"src/vendor/**"}, "src/vendor/x.go", false},
		{"empty include matches", nil, []string{"src/vendor/**"}, "any/path.go", true},
		{"empty include + exclude hit", nil, []string{"src/vendor/**"}, "src/vendor/x.go", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchAnyIncludeExclude(tc.include, tc.exclude, tc.path)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
