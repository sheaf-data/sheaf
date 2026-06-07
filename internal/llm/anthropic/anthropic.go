// Package anthropic implements llm.Client against the Anthropic Messages
// API (https://api.anthropic.com/v1/messages). Pure HTTP, no SDK
// dependency — it activates when ANTHROPIC_API_KEY is set.
//
// This is the Stage-2 frontier backend: a user-selectable alternative to
// the local ollama client. A frontier model raises the LLM tier's
// quality (extraction recall, attribution precision) well above the
// local-model floor the prototype measured. It does NOT change the
// trust architecture — every LLM-emitted element/edge is still citation-
// gated and lives in the flagged LLM tier, never the deterministic
// number — it only makes that tier better.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	apiURL     = "https://api.anthropic.com/v1/messages"
	apiVersion = "2023-06-01"
	// DefaultModel is the frontier model used when ANTHROPIC_MODEL is
	// unset — current Sonnet, the speed/intelligence balance appropriate
	// for bulk per-file extraction/attraction. Callers who want maximum
	// quality set ANTHROPIC_MODEL=claude-opus-4-7 (or the config model).
	DefaultModel = "claude-sonnet-4-6"
	// maxTokens caps a single completion. Extraction/attribution
	// responses are small JSON arrays, so this is generous.
	maxTokens = 4096
)

// Config tunes the client. APIKey/Model default from the environment
// (ANTHROPIC_API_KEY, ANTHROPIC_MODEL) when left empty.
type Config struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
	// OnUsage, if set, is called after each successful response with the
	// request's token counts. Lets callers accumulate cost without changing
	// the llm.Client interface.
	OnUsage func(inputTokens, outputTokens, cacheCreate, cacheRead int)
}

// Client implements llm.Client (Name + Generate) via the Messages API.
type Client struct {
	apiKey  string
	model   string
	http    *http.Client
	onUsage func(inputTokens, outputTokens, cacheCreate, cacheRead int)
}

// New builds a client. It returns an error only when no API key is
// available (neither in cfg nor in ANTHROPIC_API_KEY), so callers can
// fall back to a local backend.
func New(cfg Config) (*Client, error) {
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("anthropic: ANTHROPIC_API_KEY not set")
	}
	model := cfg.Model
	if model == "" {
		if env := os.Getenv("ANTHROPIC_MODEL"); env != "" {
			model = env
		} else {
			model = DefaultModel
		}
	}
	httpc := cfg.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{apiKey: key, model: model, http: httpc, onUsage: cfg.OnUsage}, nil
}

func (c *Client) Name() string { return "anthropic:" + c.model }

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    any       `json:"system,omitempty"` // string, or []systemBlock for prompt caching
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string    `json:"role"`
	Content []content `json:"content"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// systemBlock is a system content block that can carry a cache breakpoint.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type response struct {
	Content    []content `json:"content"`
	StopReason string    `json:"stop_reason"`
	Usage      struct {
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Generate sends a single-turn prompt and returns the concatenated text
// of the model's response. No tools, no multi-turn loop — sheaf's
// extraction/attraction prompts are one-shot JSON producers.
func (c *Client) Generate(ctx context.Context, prompt string) (string, error) {
	return c.do(ctx, request{
		Model:     c.model,
		MaxTokens: maxTokens,
		Messages: []message{
			{Role: "user", Content: []content{{Type: "text", Text: prompt}}},
		},
	})
}

// GenerateCached sends `system` as a cached prefix (prompt caching) and
// `user` as the variable body. When the same `system` recurs across many
// calls (e.g. the attribution pass's instructions + candidate-element
// list, identical for every file), the prefix is written to the cache
// once and read cheaply thereafter — verified via the response's
// cache_read_input_tokens. Prompt caching is GA: no beta header. The
// system prefix must clear the model's minimum cacheable size (~2K tokens
// for Sonnet) or it silently won't cache.
//
// Implements llm.CachedGenerator so callers can prefer it when present
// and fall back to Generate(system+"\n\n"+user) otherwise.
func (c *Client) GenerateCached(ctx context.Context, system, user string) (string, error) {
	return c.do(ctx, request{
		Model:     c.model,
		MaxTokens: maxTokens,
		System: []systemBlock{{
			Type:         "text",
			Text:         system,
			CacheControl: &cacheControl{Type: "ephemeral"},
		}},
		Messages: []message{
			{Role: "user", Content: []content{{Type: "text", Text: user}}},
		},
	})
}

// do issues one Messages API request and returns the concatenated text.
func (c *Client) do(ctx context.Context, payload request) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic generate: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("anthropic generate: status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("anthropic generate: decode: %w (body: %s)", err, truncate(string(raw), 300))
	}
	if r.Error != nil {
		return "", fmt.Errorf("anthropic generate: %s: %s", r.Error.Type, r.Error.Message)
	}
	if c.onUsage != nil {
		c.onUsage(r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheCreationInputTokens, r.Usage.CacheReadInputTokens)
	}
	var b strings.Builder
	for _, blk := range r.Content {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
