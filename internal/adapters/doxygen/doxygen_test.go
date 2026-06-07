package doxygen

import (
	"context"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// TestParse_Fixtures drives the adapter against the synthetic Doxygen XML in
// testdata/. It pins the idioms that matter: documented compound + member are
// emitted; undocumented members are dropped (correctness over coverage);
// namespace/file compounds emit no compound-level claim; the same member
// listed under two compounds is emitted once (dedupe by id).
func TestParse_Fixtures(t *testing.T) {
	a := New(Config{XMLDir: "testdata", URLBase: "https://ex.dev/api/"})
	claims, err := a.Parse(context.Background(), ".", adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byRef := map[string]*docclaimpb.DocClaim{}
	for _, c := range claims {
		if len(c.GetContractRefs()) != 1 {
			t.Fatalf("claim %q: want exactly 1 contract ref, got %v", c.GetRawText(), c.GetContractRefs())
		}
		ref := c.GetContractRefs()[0]
		if _, dup := byRef[ref]; dup {
			t.Errorf("duplicate claim for %q (dedupe failed)", ref)
		}
		byRef[ref] = c
		if c.GetKind() != docclaimpb.DocClaimKind_REFERENCE {
			t.Errorf("%s: kind = %v, want REFERENCE", ref, c.GetKind())
		}
		if c.GetAdapter() != Name {
			t.Errorf("%s: adapter = %q, want %q", ref, c.GetAdapter(), Name)
		}
	}

	want := []string{
		"pw::Widget",        // class compound, documented
		"pw::Widget::Frob",  // documented member
		"pw::Widget::Reset", // member with brief only
		"pw::util::Encode",  // namespace free function, documented (deduped vs file)
	}
	for _, w := range want {
		if _, ok := byRef[w]; !ok {
			t.Errorf("missing expected claim for %q", w)
		}
	}
	if len(claims) != len(want) {
		t.Errorf("got %d claims, want %d: %v", len(claims), len(want), keys(byRef))
	}

	// Undocumented symbols must NOT appear.
	for _, bad := range []string{"pw::Widget::count_", "pw::util::Decode"} {
		if _, ok := byRef[bad]; ok {
			t.Errorf("undocumented %q should have been skipped", bad)
		}
	}

	// Substance grading + location + URL on a known member.
	if frob := byRef["pw::Widget::Frob"]; frob != nil {
		if frob.GetSubstance() != commonpb.Substance_SUBSTANTIVE {
			t.Errorf("Frob substance = %v, want SUBSTANTIVE", frob.GetSubstance())
		}
		if got := frob.GetLocation().GetLine(); got != 42 {
			t.Errorf("Frob line = %d, want 42", got)
		}
		if got := frob.GetLocation().GetPath(); got != "pw_widget/public/pw_widget/widget.h" {
			t.Errorf("Frob path = %q", got)
		}
		if got := frob.GetUrl(); got != "https://ex.dev/api/classpw_1_1_widget.html#aFROB" {
			t.Errorf("Frob url = %q", got)
		}
	}
	// A brief-only member grades thin, not substantive.
	if reset := byRef["pw::Widget::Reset"]; reset != nil {
		if reset.GetSubstance() == commonpb.Substance_SUBSTANTIVE {
			t.Errorf("Reset (brief only) should not be SUBSTANTIVE")
		}
	}
}

func keys(m map[string]*docclaimpb.DocClaim) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
