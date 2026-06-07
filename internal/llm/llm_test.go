package llm

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestCosine_KnownVectors(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1},
		{"parallel", []float32{2, 0, 0}, []float32{5, 0, 0}, 1},
		{"45deg", []float32{1, 0, 0}, []float32{1, 1, 0}, float32(1.0 / math.Sqrt2)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Cosine(c.a, c.b)
			if math.Abs(float64(got-c.want)) > 1e-5 {
				t.Errorf("Cosine(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestCosine_EdgeCases(t *testing.T) {
	if got := Cosine(nil, nil); got != 0 {
		t.Errorf("nil/nil → %v, want 0", got)
	}
	if got := Cosine([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Errorf("length-mismatch → %v, want 0", got)
	}
	if got := Cosine([]float32{0, 0, 0}, []float32{1, 1, 1}); got != 0 {
		t.Errorf("zero-magnitude → %v, want 0", got)
	}
}

func TestNoopEmbedder(t *testing.T) {
	e := NoopEmbedder{}
	if e.Name() != "noop" {
		t.Errorf("name = %q", e.Name())
	}
	_, err := e.Embed(context.Background(), []string{"hello"})
	if !errors.Is(err, ErrNoEmbedder) {
		t.Errorf("err = %v, want ErrNoEmbedder", err)
	}
}

func TestNoopClient(t *testing.T) {
	c := NoopClient{}
	if c.Name() != "noop" {
		t.Errorf("name = %q", c.Name())
	}
	_, err := c.Generate(context.Background(), "anything")
	if !errors.Is(err, ErrNoClient) {
		t.Errorf("err = %v, want ErrNoClient", err)
	}
}
