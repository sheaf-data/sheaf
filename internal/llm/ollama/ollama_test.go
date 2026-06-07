package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// ---- Embedder ----

func TestEmbedder_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s, want /api/embed", r.URL.Path)
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Model != "nomic-embed-text" {
			t.Errorf("model = %s", req.Model)
		}
		// Return one vector per input.
		out := make([][]float32, len(req.Input))
		for i := range req.Input {
			out[i] = []float32{float32(i), float32(i) + 0.1}
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Model: req.Model, Embeddings: out})
	}))
	defer srv.Close()
	host, port := splitURL(t, srv.URL)
	e := NewEmbedder(Config{Host: host, Port: port})
	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
	if vecs[0][0] != 0 || vecs[1][0] != 1 {
		t.Errorf("vecs = %v", vecs)
	}
}

func TestEmbedder_EmptyInputReturnsNil(t *testing.T) {
	e := NewEmbedder(Config{Host: "127.0.0.1", Port: 1})
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Errorf("got (%v, %v), want (nil, nil)", vecs, err)
	}
}

func TestEmbedder_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal model error"))
	}))
	defer srv.Close()
	host, port := splitURL(t, srv.URL)
	e := NewEmbedder(Config{Host: host, Port: port})
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Errorf("expected status-500 error, got %v", err)
	}
}

func TestEmbedder_MismatchedCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{1, 2}}})
	}))
	defer srv.Close()
	host, port := splitURL(t, srv.URL)
	e := NewEmbedder(Config{Host: host, Port: port})
	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "got 1 embeddings for 2 inputs") {
		t.Errorf("expected count-mismatch error, got %v", err)
	}
}

func TestEmbedder_NameIncludesModel(t *testing.T) {
	e := NewEmbedder(Config{Model: "nomic-embed-text"})
	if e.Name() != "ollama-embed:nomic-embed-text" {
		t.Errorf("name = %s", e.Name())
	}
}

// ---- Client (generation) ----

func TestClient_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req generateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			t.Errorf("stream should be false")
		}
		_ = json.NewEncoder(w).Encode(generateResponse{Model: req.Model, Response: "hello there", Done: true})
	}))
	defer srv.Close()
	host, port := splitURL(t, srv.URL)
	c := NewClient(Config{Host: host, Port: port})
	out, err := c.Generate(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if out != "hello there" {
		t.Errorf("out = %q", out)
	}
}

func TestClient_NameIncludesModel(t *testing.T) {
	c := NewClient(Config{Model: "qwen2.5:7b-instruct"})
	if c.Name() != "ollama:qwen2.5:7b-instruct" {
		t.Errorf("name = %s", c.Name())
	}
}

// ---- Ping ----

func TestPing_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			_, _ = w.Write([]byte(`{"version":"0.0.0"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	host, port := splitURL(t, srv.URL)
	if err := Ping(context.Background(), host, port); err != nil {
		t.Errorf("Ping err = %v", err)
	}
}

func TestPing_Unreachable(t *testing.T) {
	if err := Ping(context.Background(), "127.0.0.1", 1); err == nil {
		t.Errorf("expected error for unreachable")
	}
}

// helpers

func splitURL(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	return host, port
}
