// scanner — Sheaf MCP client + report generator.
//
// This file holds the JSON-RPC over HTTP client used to talk to a
// running `sheaf serve` instance. The shape mirrors internal/mcp's
// dispatch table; types here are decoded into the same generic
// shapes the server emits (protojson round-tripped through map).
package scanner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func getEnv(key string) string {
	return os.Getenv(key)
}

// MCPClient is a minimal JSON-RPC 2.0 client.
type MCPClient struct {
	URL        string // e.g. http://127.0.0.1:7700
	BearerEnv  string // env var holding the bearer token (empty = no auth)
	HTTPClient *http.Client
	id         int
}

// NewClient constructs a client with sensible defaults.
func NewClient(url, bearerEnv string) *MCPClient {
	if url == "" {
		url = "http://127.0.0.1:7700"
	}
	url = strings.TrimRight(url, "/")
	return &MCPClient{
		URL:        url,
		BearerEnv:  bearerEnv,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Health does a GET /healthz and returns the decoded JSON body.
func (c *MCPClient) Health() (map[string]any, error) {
	req, err := http.NewRequest("GET", c.URL+"/healthz", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting %s: %w", c.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("healthz: status=%d body=%s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("healthz: decode: %w body=%s", err, body)
	}
	return out, nil
}

// Call posts a JSON-RPC request and decodes the result field into out.
// JSON-RPC errors come back as a Go error (not unmarshaled into out).
func (c *MCPClient) Call(method string, params any, out any) error {
	c.id++
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}
	req, err := http.NewRequest("POST", c.URL+"/mcp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("rpc %s: status=%d body=%s", method, resp.StatusCode, raw)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("rpc %s: decode envelope: %w body=%s", method, err, raw)
	}
	if env.Error != nil {
		return fmt.Errorf("rpc %s: %d %s", method, env.Error.Code, env.Error.Message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("rpc %s: decode result: %w", method, err)
	}
	return nil
}

func (c *MCPClient) setAuth(req *http.Request) {
	if c.BearerEnv == "" {
		return
	}
	if tok := getEnv(c.BearerEnv); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// LibraryEntry mirrors one item in list_libraries.
type LibraryEntry struct {
	Library  string `json:"library"`
	Elements int    `json:"elements"`
	Profiles int    `json:"profiles"`
	Findings int    `json:"findings"`
}

// ListLibraries fetches the library inventory.
func (c *MCPClient) ListLibraries() ([]LibraryEntry, error) {
	var out struct {
		Libraries []LibraryEntry `json:"libraries"`
		Total     int            `json:"total"`
	}
	if err := c.Call("list_libraries", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Libraries, nil
}

// Snapshot is the bulk shape returned by library_snapshot. Elements,
// profiles and findings stay as generic maps because we go through
// protojson server-side — keeps the client free of proto deps.
//
// Analyzers carries the names of analyzers configured in the
// server's sheaf.textproto. It lets the report disambiguate the
// "no findings" state — empty list means no analyzers were
// configured (expected emptiness); non-empty list means analyzers
// ran and found nothing (genuinely clean corpus). Older servers
// that predate this field, and test harnesses that build the
// server without WithReview, return nil.
type Snapshot struct {
	// SchemaVersion is the librarysnapshot.SchemaVersion the producer
	// stamped. 0 means a legacy snapshot written before versioning existed
	// (rendered best-effort). A non-zero value that doesn't match the
	// current schema is rejected by the offline reader — see cmd/scanner.
	SchemaVersion int              `json:"schema_version"`
	Library       string           `json:"library"`
	Elements      []map[string]any `json:"elements"`
	Profiles      []map[string]any `json:"profiles"`
	Findings      []map[string]any `json:"findings"`
	Analyzers     []string         `json:"analyzers,omitempty"`
	// SurfacesRequired is the project's declared surface set, used
	// to drive the masthead's bridged math + per-surface slot
	// rendering. Empty when the server's sheaf.textproto does not
	// declare surfaces_required (engine falls back to the v0
	// three-surface bridged definition: concept + tests + examples).
	SurfacesRequired []string `json:"surfaces_required,omitempty"`

	// ConceptDocSource records whether a concept-doc (narrative) source was
	// configured AND scanned for this snapshot — i.e. the anchored-mention
	// engine ran over a non-empty narrative-doc corpus. It GATES the report's
	// concept-doc reach line: a repo with no concept-doc source wired leaves
	// this false and the line does not render (no misleading "0 of N"). True
	// even when the engine attributed nothing, so a configured-but-silent
	// corpus still gets the honest "clearly reference 0 of N" map.
	ConceptDocSource bool `json:"concept_doc_source,omitempty"`

	// DocSurfaceDirs maps a rendered_reference adapter name (e.g.
	// "workflows", "markdowncli") to the absolute docs_dir it was
	// scanned from. Recorded at snapshot time so the render-from-snapshot
	// path can resolve a doc's git timestamp in the repo that actually
	// tracks it — which enables cross-repo doc-lag when the authored docs
	// (e.g. github/docs guides) live in a different repo from the code
	// (e.g. cli/cli). Empty for single-repo scans and legacy snapshots.
	DocSurfaceDirs map[string]string `json:"doc_surface_dirs,omitempty"`
}

// LibrarySnapshot bulk-fetches a library.
func (c *MCPClient) LibrarySnapshot(library string) (*Snapshot, error) {
	var s Snapshot
	if err := c.Call("library_snapshot", map[string]any{"library": library}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
