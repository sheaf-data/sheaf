package versionscheme

import "testing"

func TestFIDL_IsRemovedAt(t *testing.T) {
	s := FIDL()
	cases := []struct {
		name, removed, target string
		want                  bool
	}{
		{"no removed marker", "", "HEAD", false},
		{"no removed numeric target", "", "27", false},

		// HEAD target: anything numeric or HEAD-removed is gone, NEXT is ahead.
		{"HEAD vs num29", "29", "HEAD", true},
		{"HEAD vs HEAD", "HEAD", "HEAD", true},
		{"HEAD vs NEXT", "NEXT", "HEAD", false},
		{"empty target defaults to HEAD", "29", "", true},

		// Numeric target.
		{"num27 vs num29 (29>27 → not yet)", "29", "27", false},
		{"num27 vs num27 (removed AT level)", "27", "27", true},
		{"num30 vs num27 (long past)", "27", "30", true},
		{"num27 vs HEAD removed (after any number)", "HEAD", "27", false},
		{"num27 vs NEXT removed", "NEXT", "27", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := s.IsRemovedAt(c.removed, c.target); got != c.want {
				t.Errorf("FIDL.IsRemovedAt(%q, %q) = %v; want %v",
					c.removed, c.target, got, c.want)
			}
		})
	}
}

func TestFor_FallbackToFIDL(t *testing.T) {
	if got := For("").Name(); got != "fidl" {
		t.Errorf("For(\"\").Name() = %q; want fidl", got)
	}
	if got := For("fidl").Name(); got != "fidl" {
		t.Errorf("For(fidl).Name() = %q; want fidl", got)
	}
	if got := For("unknown-ecosystem").Name(); got != "fidl" {
		t.Errorf("For(unknown).Name() = %q; want fidl (fallback)", got)
	}
}
