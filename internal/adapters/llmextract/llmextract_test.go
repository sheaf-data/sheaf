package llmextract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

// fakeClient fails Generate for any prompt whose embedded source contains
// failMarker, and otherwise returns a fixed citation-valid element. It
// simulates a flaky local model that times out on one file.
type fakeClient struct{ failMarker string }

func (fakeClient) Name() string { return "fake" }

func (c fakeClient) Generate(_ context.Context, prompt string) (string, error) {
	if strings.Contains(prompt, c.failMarker) {
		return "", context.DeadlineExceeded
	}
	// Cite line 1, which in good.h declares Foo — survives verification.
	return `[{"name":"Foo","kind":"free_function","line":1}]`, nil
}

// TestDiscover_PerFileFailureIsNonFatal is the regression guard for the
// integration bug found in the pw_log --auto run: a single per-file model
// timeout used to abort the whole walk (and the orchestrator then dropped
// the entire LLM tier). One bad file must degrade the result by exactly
// that file — the other files' elements survive.
func TestDiscover_PerFileFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	// WalkMatching visits files in lexical order, so "bad.h" is reached
	// before "good.h": the old all-or-nothing walk would abort before
	// ever extracting Foo.
	if err := os.WriteFile(filepath.Join(dir, "bad.h"), []byte("void Boom();\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.h"), []byte("void Foo();\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := New(Config{Include: []string{"**/*.h"}}, fakeClient{failMarker: "Boom"})
	out, st, err := a.DiscoverWithStats(context.Background(), dir, adapters.ScopeConfig{})

	if err == nil {
		t.Fatal("expected a non-nil (soft) error surfacing the failed file")
	}
	if st.FilesFailed != 1 {
		t.Errorf("FilesFailed = %d, want 1", st.FilesFailed)
	}
	if st.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2", st.FilesScanned)
	}
	if len(out) != 1 {
		t.Fatalf("got %d elements, want 1 (good.h's Foo despite bad.h failing)", len(out))
	}
	if !strings.HasSuffix(out[0].GetId(), "Foo") {
		t.Errorf("surviving element id = %q, want it to end in Foo", out[0].GetId())
	}
}
