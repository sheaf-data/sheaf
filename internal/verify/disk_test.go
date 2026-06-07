package verify

import (
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

func TestDetectContamination_FlagsWorktreePath(t *testing.T) {
	rd := &scanner.ReportData{Methods: []scanner.MethodRow{{Name: "a"}, {Name: "b"}}}
	profByID := map[string]map[string]any{
		"a": testProfile(1, ".claude/worktrees/x/a_test.go"),
		"b": testProfile(1, "internal/cli/b_test.go"),
	}
	rep := &Report{}
	detectContamination(rep, rd, profByID)
	if !hasCategory(rep, CatContamination) {
		t.Fatalf("expected contamination finding for a worktree path, got %+v", rep.Findings)
	}
}

func TestDetectContamination_CleanIsClean(t *testing.T) {
	rd := &scanner.ReportData{Methods: []scanner.MethodRow{{Name: "b"}}}
	profByID := map[string]map[string]any{
		"b": testProfile(2, "internal/cli/b_test.go", "internal/review/b2_test.go"),
	}
	rep := &Report{}
	detectContamination(rep, rd, profByID)
	if hasCategory(rep, CatContamination) {
		t.Fatalf("did not expect contamination on clean paths, got %+v", rep.Findings)
	}
}

func TestDetectNameCollision(t *testing.T) {
	rd := &scanner.ReportData{Methods: []scanner.MethodRow{
		{Name: "tool run", Test: 3},
		{Name: "tool frobnicate", Test: 3}, // not a common word
		{Name: "lib/Service.Get", Test: 1},
		{Name: "tool list", Test: 0}, // untested → not flagged
	}}
	rep := &Report{}
	detectNameCollision(rep, rd, nil)
	if !hasCategory(rep, CatNameCollision) {
		t.Fatalf("expected a name-collision finding, got %+v", rep.Findings)
	}
	var ev []string
	for _, f := range rep.Findings {
		if f.Category == CatNameCollision {
			ev = f.Evidence
		}
	}
	joined := strings.Join(ev, " ")
	if !strings.Contains(joined, "tool run") || !strings.Contains(joined, "lib/Service.Get") {
		t.Fatalf("expected 'tool run' and 'lib/Service.Get' in evidence, got %v", ev)
	}
	if strings.Contains(joined, "frobnicate") {
		t.Fatalf("did not expect 'frobnicate' (not a common word), got %v", ev)
	}
	if strings.Contains(joined, "tool list") {
		t.Fatalf("did not expect untested 'tool list', got %v", ev)
	}
}

func TestLocalName(t *testing.T) {
	cases := map[string]string{
		"lib/Service.Method":  "method",
		"tool sub":            "sub",
		"ns::Class::run":      "run",
		"Get":                 "get",
		"docker container ls": "ls",
	}
	for in, want := range cases {
		if got := localName(in); got != want {
			t.Errorf("localName(%q)=%q want %q", in, got, want)
		}
	}
}
