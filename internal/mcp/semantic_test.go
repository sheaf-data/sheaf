package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// fakeEmbedder maps strings to deterministic vectors so we can
// hand-craft scenarios where one element is the obvious "match" for
// a given query.
type fakeEmbedder struct {
	// table: text → vector. Unknown text gets a zero vector (won't
	// match anything).
	table map[string][]float32
	calls int
}

func (f *fakeEmbedder) Name() string { return "fake" }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := f.table[t]; ok {
			out[i] = v
		} else {
			out[i] = []float32{0, 0, 0}
		}
	}
	return out, nil
}

func newSemanticTestServer(t *testing.T, embedder *fakeEmbedder) *Server {
	t.Helper()
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:                "fuchsia.io/Directory.Open",
		Kind:              contractpb.ContractElementKind_METHOD,
		DocCommentExcerpt: "Open (or create) a node relative to this directory.",
	})
	c.AddElement(&contractpb.ContractElement{
		Id:                "fuchsia.io/Directory.Close",
		Kind:              contractpb.ContractElementKind_METHOD,
		DocCommentExcerpt: "Closes the directory.",
	})
	c.AddElement(&contractpb.ContractElement{
		Id:                "fuchsia.io/File.Read",
		Kind:              contractpb.ContractElementKind_METHOD,
		DocCommentExcerpt: "Reads bytes from a file.",
	})

	srv := New(c, nil, &configpb.MCPServerConfig{
		Bind: "127.0.0.1", Port: 17900,
	}).WithEmbedder(embedder, nil)
	return srv
}

func TestSemanticFindExamples_TopHitMatches(t *testing.T) {
	embedder := &fakeEmbedder{
		table: map[string][]float32{
			// Wire the query and "Directory.Open"'s searchable text to
			// the same vector — perfect cosine similarity.
			"open a directory": {1, 0, 0},
			"fuchsia.io/Directory.Open\n\nOpen (or create) a node relative to this directory.": {1, 0, 0},
			// Other elements get orthogonal vectors.
			"fuchsia.io/Directory.Close\n\nCloses the directory.": {0, 1, 0},
			"fuchsia.io/File.Read\n\nReads bytes from a file.":    {0, 0, 1},
		},
	}
	srv := newSemanticTestServer(t, embedder)
	result, err := srv.semanticFindExamples(context.Background(), "open a directory", 5)
	if err != nil {
		t.Fatalf("semanticFindExamples: %v", err)
	}
	m := result.(map[string]any)
	matches := m["matches"].([]map[string]any)
	if len(matches) == 0 {
		t.Fatalf("no matches; got %+v", m)
	}
	if matches[0]["element_id"] != "fuchsia.io/Directory.Open" {
		t.Errorf("top match = %v, want fuchsia.io/Directory.Open", matches[0]["element_id"])
	}
	if !strings.HasPrefix(m["scoringMethod"].(string), "semantic:") {
		t.Errorf("scoringMethod = %v, want semantic:*", m["scoringMethod"])
	}
}

func TestSemanticFindExamples_IncludesDocExcerpt(t *testing.T) {
	embedder := &fakeEmbedder{
		table: map[string][]float32{
			"x": {1},
			"fuchsia.io/Directory.Open\n\nOpen (or create) a node relative to this directory.": {1},
		},
	}
	srv := newSemanticTestServer(t, embedder)
	result, _ := srv.semanticFindExamples(context.Background(), "x", 5)
	m := result.(map[string]any)
	matches := m["matches"].([]map[string]any)
	if len(matches) == 0 {
		t.Fatalf("no matches")
	}
	if doc, _ := matches[0]["doc_excerpt"].(string); !strings.Contains(doc, "Open") {
		t.Errorf("doc_excerpt missing: %v", matches[0])
	}
}

func TestSemanticFindExamples_ElementEmbedsCachedAcrossQueries(t *testing.T) {
	embedder := &fakeEmbedder{
		table: map[string][]float32{
			"q1": {1, 0, 0},
			"q2": {1, 0, 0},
			"fuchsia.io/Directory.Open\n\nOpen (or create) a node relative to this directory.": {1, 0, 0},
		},
	}
	srv := newSemanticTestServer(t, embedder)
	_, err := srv.semanticFindExamples(context.Background(), "q1", 5)
	if err != nil {
		t.Fatalf("q1: %v", err)
	}
	callsAfterQ1 := embedder.calls
	if callsAfterQ1 == 0 {
		t.Fatalf("expected at least one embed call after q1")
	}
	_, err = srv.semanticFindExamples(context.Background(), "q2", 5)
	if err != nil {
		t.Fatalf("q2: %v", err)
	}
	// q2 should only have embedded the query itself — element
	// embeddings should already be cached in s.elementEmbed.
	if embedder.calls != callsAfterQ1+1 {
		t.Errorf("element embeddings not cached: %d calls total, expected %d (one new query embed)",
			embedder.calls, callsAfterQ1+1)
	}
}

func TestFindExamples_TokenOverlapFallback_NoEmbedder(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Directory.Open", Kind: contractpb.ContractElementKind_METHOD,
		Location: &commonpb.SourceLocation{Path: "x.fidl", Line: 1},
	})
	srv := New(c, nil, &configpb.MCPServerConfig{Bind: "127.0.0.1", Port: 17901})
	result := srv.tokenOverlapFindExamples("directory open", 5, "")
	m := result.(map[string]any)
	if m["scoringMethod"] != "token-overlap" {
		t.Errorf("scoringMethod = %v", m["scoringMethod"])
	}
	matches := m["matches"].([]map[string]any)
	if len(matches) == 0 || matches[0]["element_id"] != "fuchsia.io/Directory.Open" {
		t.Errorf("matches = %v", matches)
	}
}

func TestFindExamples_EmbedderErrorFallsBackToTokens(t *testing.T) {
	embedder := &failingEmbedder{}
	srv := newSemanticTestServer(t, nil).WithEmbedder(embedder, nil)
	// Use the public dispatch path so we exercise the fallback branch.
	result, rpcErr := srv.opFindExamples(context.Background(), []byte(`{"query":"directory open","max_items":5}`))
	if rpcErr != nil {
		t.Fatalf("opFindExamples err = %v", rpcErr)
	}
	m := result.(map[string]any)
	if m["scoringMethod"] != "token-overlap" {
		t.Errorf("scoringMethod = %v, want token-overlap fallback", m["scoringMethod"])
	}
	if m["fallbackReason"] == nil {
		t.Errorf("expected fallbackReason in result; got %+v", m)
	}
}

// failingEmbedder always errors.
type failingEmbedder struct{}

func (failingEmbedder) Name() string { return "failing" }
func (failingEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, errEmbedderDown
}

var errEmbedderDown = &mockErr{"embedder unreachable"}

type mockErr struct{ msg string }

func (e *mockErr) Error() string { return e.msg }
