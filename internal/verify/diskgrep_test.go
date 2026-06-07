package verify

import "testing"

func TestFnSearchTerm(t *testing.T) {
	cases := []struct{ id, kind, want string }{
		{"sheaf scan manifest", "FLAG", "--manifest"},
		{"sheaf scan quiet", "FLAG", ""},       // common shared flag → skipped
		{"sheaf scan --verbose", "SWITCH", ""}, // common shared flag → skipped
		{"lib/Service.Method", "METHOD", "Service.Method"},
		{"lib/Foo", "TYPE", ""},             // single token → not distinctive
		{"sheaf scan", "SUBCOMMAND", ""},    // subcommands are skipped
		{"x", "FLAG", ""},                   // too short
		{"sheaf coverage repo", "FLAG", ""}, // common shared flag → skipped
		{"sheaf gaps severity", "FLAG", "--severity"},
	}
	for _, c := range cases {
		if got := fnSearchTerm(c.id, c.kind); got != c.want {
			t.Errorf("fnSearchTerm(%q,%q)=%q want %q", c.id, c.kind, got, c.want)
		}
	}
}

func TestLooksLikeTest(t *testing.T) {
	yes := []string{"internal/cli/foo_test.go", "tests/bar.py", "x/test_baz.py", "a/foo.bats", "pkg/m_spec.rb"}
	no := []string{"internal/cli/foo.go", "docs/readme.md", "main.go", "internal/latest.go"}
	for _, p := range yes {
		if !looksLikeTest(p) {
			t.Errorf("looksLikeTest(%q)=false, want true", p)
		}
	}
	for _, p := range no {
		if looksLikeTest(p) {
			t.Errorf("looksLikeTest(%q)=true, want false", p)
		}
	}
}

func TestLastSegment(t *testing.T) {
	cases := map[string]string{
		"sheaf scan quiet":   "quiet",
		"ns::C::run":         "run",
		"lib/Service.Method": "Method",
		"plain":              "plain",
	}
	for in, want := range cases {
		if got := lastSegment(in); got != want {
			t.Errorf("lastSegment(%q)=%q want %q", in, got, want)
		}
	}
}
