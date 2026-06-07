package embedcache

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFSCache_RoundTrip(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	vec := []float32{1.0, 2.5, -3.5, 0}
	if err := c.Put("hello", "p1", vec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := c.Get("hello", "p1")
	if !ok {
		t.Fatalf("Get miss after Put")
	}
	if !reflect.DeepEqual(got, vec) {
		t.Errorf("got %v, want %v", got, vec)
	}
}

func TestFSCache_KeyIncludesProvider(t *testing.T) {
	// Same text under two providers must be distinct entries.
	c, _ := New(t.TempDir())
	if err := c.Put("hello", "p1", []float32{1, 2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Put("hello", "p2", []float32{9, 8}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a, _ := c.Get("hello", "p1")
	b, _ := c.Get("hello", "p2")
	if reflect.DeepEqual(a, b) {
		t.Errorf("provider should differentiate keys; both = %v", a)
	}
}

func TestFSCache_DifferentTextDifferentKeys(t *testing.T) {
	c, _ := New(t.TempDir())
	if err := c.Put("text A", "p1", []float32{1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Put("text B", "p1", []float32{2}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a, _ := c.Get("text A", "p1")
	b, _ := c.Get("text B", "p1")
	if a[0] == b[0] {
		t.Errorf("different texts produced same vector: %v / %v", a, b)
	}
}

func TestFSCache_MissReturnsFalse(t *testing.T) {
	c, _ := New(t.TempDir())
	_, ok := c.Get("never put", "p")
	if ok {
		t.Errorf("expected miss")
	}
}

func TestFSCache_EmptyVector(t *testing.T) {
	c, _ := New(t.TempDir())
	if err := c.Put("k", "p", []float32{}); err != nil {
		t.Fatalf("Put empty: %v", err)
	}
	got, ok := c.Get("k", "p")
	if !ok || len(got) != 0 {
		t.Errorf("got (%v, %v), want ([], true)", got, ok)
	}
}

// ----- EmbedWithCache -----

type countingEmbedder struct {
	calls    int
	lastArgs []string
	err      error
}

func (e *countingEmbedder) Name() string { return "counter" }
func (e *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	e.lastArgs = texts
	if e.err != nil {
		return nil, e.err
	}
	// Return one vector per input, with text-length as the first dim.
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{float32(len(t))}
	}
	return out, nil
}

func TestEmbedWithCache_MissesEmbedCacheHitsSkip(t *testing.T) {
	c, _ := New(t.TempDir())
	e := &countingEmbedder{}

	// First call: 2 misses → embed 2.
	v1, err := EmbedWithCache(context.Background(), e, c, []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if e.calls != 1 || len(e.lastArgs) != 2 {
		t.Errorf("first call expected 1 call with 2 args; got calls=%d args=%v", e.calls, e.lastArgs)
	}
	if !reflect.DeepEqual(v1[0], []float32{5}) || !reflect.DeepEqual(v1[1], []float32{4}) {
		t.Errorf("first call vectors = %v", v1)
	}

	// Second call: all hits → no Embed.
	e.calls, e.lastArgs = 0, nil
	v2, _ := EmbedWithCache(context.Background(), e, c, []string{"alpha", "beta"})
	if e.calls != 0 {
		t.Errorf("second call should be all cache hits; got %d calls", e.calls)
	}
	if !reflect.DeepEqual(v2, v1) {
		t.Errorf("v2 = %v, want %v", v2, v1)
	}

	// Mixed: one hit, one miss → embed 1.
	e.calls, e.lastArgs = 0, nil
	mixed, _ := EmbedWithCache(context.Background(), e, c, []string{"alpha", "gamma"})
	if e.calls != 1 || len(e.lastArgs) != 1 || e.lastArgs[0] != "gamma" {
		t.Errorf("mixed expected 1 call for 'gamma'; got calls=%d args=%v", e.calls, e.lastArgs)
	}
	if mixed[0][0] != 5 || mixed[1][0] != 5 {
		t.Errorf("mixed vectors wrong: %v", mixed)
	}
}

func TestEmbedWithCache_PropagatesEmbedderError(t *testing.T) {
	c, _ := New(t.TempDir())
	e := &countingEmbedder{err: errors.New("boom")}
	_, err := EmbedWithCache(context.Background(), e, c, []string{"x"})
	if err == nil || err.Error() != "boom" {
		t.Errorf("err = %v, want 'boom'", err)
	}
}

// Verify the type assertion: FSCache implements Cache; countingEmbedder satisfies Embedder.
var _ Cache = (*FSCache)(nil)
var _ Embedder = (*countingEmbedder)(nil)
