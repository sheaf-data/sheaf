// Package ollama implements LLM and Embedder against a local ollama
// HTTP daemon (default 127.0.0.1:11434).
//
// API references:
//   - Embeddings: POST /api/embed   {"model": "...", "input": "txt"|["txt1","txt2"]}
//                  → {"embeddings": [[float, …]], "model": "..."}
//   - Generation: POST /api/generate {"model": "...", "prompt": "...", "stream": false}
//                  → {"response": "...", "done": true, "model": "..."}
//
// Both paths are non-streaming for v1 — Sheaf's use cases don't need
// token streaming (semantic search waits for the full vector;
// generation in v2+ HITL is short enough that streaming isn't needed).

package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config configures an ollama client. Defaults: host=127.0.0.1, port=11434.
type Config struct {
	Host  string
	Port  int
	Model string
	// HTTPClient is optional; defaults to a 60s-timeout client.
	HTTPClient *http.Client
	// OnUsage, if set, is called after each successful generation with the
	// request's token counts (cacheCreate/cacheRead are always 0 — ollama
	// has no prompt cache). Lets callers accumulate usage without changing
	// the llm.Client interface.
	OnUsage func(inputTokens, outputTokens, cacheCreate, cacheRead int)
}

func defaultHost(h string) string {
	if h == "" {
		return "127.0.0.1"
	}
	return h
}

func defaultPort(p int) int {
	if p == 0 {
		return 11434
	}
	return p
}

func defaultHTTPClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// ===========================================================
// Embedder
// ===========================================================

// Embedder implements llm.Embedder against ollama's /api/embed.
type Embedder struct {
	baseURL string
	model   string
	client  *http.Client
}

func NewEmbedder(cfg Config) *Embedder {
	if cfg.Model == "" {
		cfg.Model = "nomic-embed-text"
	}
	return &Embedder{
		baseURL: fmt.Sprintf("http://%s:%d", defaultHost(cfg.Host), defaultPort(cfg.Port)),
		model:   cfg.Model,
		client:  defaultHTTPClient(cfg.HTTPClient),
	}
}

func (e *Embedder) Name() string { return "ollama-embed:" + e.model }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed sends one batched request per call. ollama supports arrays in
// the `input` field, so we keep this single-call.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama embed: status %d: %s", resp.StatusCode, truncate(string(buf), 200))
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("ollama embed: decode: %w", err)
	}
	if len(er.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: got %d embeddings for %d inputs", len(er.Embeddings), len(texts))
	}
	return er.Embeddings, nil
}

// ===========================================================
// Client (generation)
// ===========================================================

// Client implements llm.Client against ollama's /api/generate.
type Client struct {
	baseURL string
	model   string
	client  *http.Client
	onUsage func(inputTokens, outputTokens, cacheCreate, cacheRead int)
}

func NewClient(cfg Config) *Client {
	if cfg.Model == "" {
		cfg.Model = "qwen2.5:7b-instruct"
	}
	return &Client{
		baseURL: fmt.Sprintf("http://%s:%d", defaultHost(cfg.Host), defaultPort(cfg.Port)),
		model:   cfg.Model,
		client:  defaultHTTPClient(cfg.HTTPClient),
		onUsage: cfg.OnUsage,
	}
}

func (c *Client) Name() string { return "ollama:" + c.model }

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type generateResponse struct {
	Model           string `json:"model"`
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	PromptEvalCount int    `json:"prompt_eval_count"` // input (prompt) tokens
	EvalCount       int    `json:"eval_count"`        // generated (output) tokens
}

// Generate returns the model's full response. Non-streaming.
func (c *Client) Generate(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(generateRequest{Model: c.model, Prompt: prompt, Stream: false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama generate: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama generate: status %d: %s", resp.StatusCode, truncate(string(buf), 200))
	}
	var gr generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return "", fmt.Errorf("ollama generate: decode: %w", err)
	}
	if c.onUsage != nil {
		c.onUsage(gr.PromptEvalCount, gr.EvalCount, 0, 0)
	}
	return gr.Response, nil
}

// ===========================================================
// Liveness probe
// ===========================================================

// Ping checks whether the ollama daemon is reachable. Used by callers
// to decide whether to wire ollama in or fall back.
func Ping(ctx context.Context, host string, port int) error {
	url := fmt.Sprintf("http://%s:%d/api/version", defaultHost(host), defaultPort(port))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ollama ping %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("ollama: non-200 from /api/version")
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
