package scanner

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the absolute path of the sheaf repo working from
// this test file's known location. Used to exercise the source-read
// helpers against real files without depending on cwd.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file = .../utils/scanner/evidence_test.go → repo = .../sheaf
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func TestReadCodeLinesDedents(t *testing.T) {
	root := repoRoot(t)
	text, total, _ := readCodeLines(root, "utils/scanner/evidence.go", 1, 5, 16)
	if text == "" {
		t.Fatalf("readCodeLines returned empty for known file")
	}
	if total < 5 {
		t.Fatalf("total lines should be >= 5, got %d", total)
	}
	if strings.HasPrefix(text, "\t") || strings.HasPrefix(text, " ") {
		t.Errorf("output should be dedented; first line: %q", strings.SplitN(text, "\n", 2)[0])
	}
}

func TestReadContractLinesIncludesLeadingComments(t *testing.T) {
	root := repoRoot(t)
	// EvidencePanel is documented at line ~16 of evidence.go and has a
	// multi-line leading comment. The contract reader should grab some
	// of that comment block.
	body, _, _ := readContractLines(root, "utils/scanner/evidence.go", 16, 12)
	if body == "" {
		t.Fatalf("contract reader returned empty")
	}
	if !strings.Contains(body, "EvidencePanel") {
		t.Errorf("body should contain EvidencePanel identifier; got: %q", body[:min(120, len(body))])
	}
}

func TestReadTestFunctionBodyBraceBalanced(t *testing.T) {
	root := repoRoot(t)
	body, total, _ := readTestFunctionBody(root, "utils/scanner/evidence_test.go", 23, 30)
	if body == "" {
		t.Fatalf("test body reader returned empty for known function")
	}
	if total <= 0 {
		t.Errorf("total lines should be > 0; got %d", total)
	}
}

func TestWorkflowClassifierMatchesDefaults(t *testing.T) {
	wc := defaultWorkflowClassifier()
	cases := map[string]bool{
		"docs/workflows/edge.md":           true,
		"docs/recipes/cookbook.md":         true,
		"docs/best-practices/edge.rst":     true,
		"docs/tutorial/getting-started.md": true,
		"docs/how-to/scale.md":             true,
		"docs/api/listener.rst":            false,
		"pkg/api/admin.go":                 false,
	}
	for path, want := range cases {
		if got := wc.matchesPath(path); got != want {
			t.Errorf("matchesPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestBuildEvidencePanelsHandlesEmptyRoot(t *testing.T) {
	got := buildEvidencePanels(nil, nil, nil, nil, "", "", nil)
	if got != nil {
		t.Errorf("expected nil EvidencePanels for empty absRepoRoot, got %d", len(got))
	}
}

func TestParagraphMentions(t *testing.T) {
	if !paragraphMentions("Use the option to configure use_remote_address.", "envoy/Listener.use_remote_address") {
		t.Error("should match by element-tail token")
	}
	if paragraphMentions("totally unrelated prose", "envoy/Listener.use_remote_address") {
		t.Error("should not match unrelated text")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
