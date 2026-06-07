package scanner

import "testing"

// Hermetic unit tests for the pure helpers behind cross-repo guide-lag.
// The git-dependent end-to-end path (two real repos) is exercised by the
// live gh scan, not here, so these stay fixture-free and CI-safe.

func TestCommandOf(t *testing.T) {
	for in, want := range map[string]string{
		"gh issue create --assignee": "gh issue create",
		"gh pr list":                 "gh pr list",
		"gh":                         "gh",
		"gh repo clone --depth":      "gh repo clone",
	} {
		if got := commandOf(in); got != want {
			t.Errorf("commandOf(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestPathDir(t *testing.T) {
	for in, want := range map[string]string{
		"pkg/cmd/pr/list/list_test.go": "pkg/cmd/pr/list",
		"list_test.go":                 ".",
		"a/b":                          "a",
	} {
		if got := pathDir(in); got != want {
			t.Errorf("pathDir(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestFirstTestDir(t *testing.T) {
	tests := map[string]any{
		"unit": []any{
			map[string]any{"path": "pkg/cmd/pr/list/list_test.go", "line": float64(77)},
		},
	}
	if got := firstTestDir(tests); got != "pkg/cmd/pr/list" {
		t.Errorf("firstTestDir = %q; want pkg/cmd/pr/list", got)
	}
	if got := firstTestDir(map[string]any{}); got != "" {
		t.Errorf("firstTestDir(empty) = %q; want empty", got)
	}
}

func TestWorkflowRefs(t *testing.T) {
	prof := map[string]any{
		"docs": map[string]any{
			"reference": map[string]any{
				"byAdapter": map[string]any{
					"workflows": map[string]any{
						"refs": []any{
							map[string]any{
								"path": "issues/creating-an-issue.md",
								"url":  "https://docs.github.com/en/issues/creating-an-issue",
							},
						},
					},
					"markdowncli": map[string]any{
						"refs": []any{map[string]any{"path": "gh_issue.md"}},
					},
				},
			},
		},
	}
	refs := workflowRefs(prof)
	if len(refs) != 1 || refs[0].path != "issues/creating-an-issue.md" {
		t.Fatalf("workflowRefs = %+v; want exactly the one workflows ref", refs)
	}
	if got := workflowRefs(map[string]any{}); got != nil {
		t.Errorf("workflowRefs(empty profile) = %+v; want nil", got)
	}
}

func TestComputeGuideLag_NoDocsDir(t *testing.T) {
	// Without a recorded workflows docs_dir, guide-lag is a no-op
	// (single-repo scans keep using the run-level Lag distribution).
	if rows := computeGuideLag(&Snapshot{}, "/tmp"); rows != nil {
		t.Errorf("computeGuideLag with no DocSurfaceDirs = %+v; want nil", rows)
	}
}
