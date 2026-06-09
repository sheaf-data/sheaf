// Package mcp implements the Sheaf MCP server.
//
// MCP here is a thin JSON-RPC-over-HTTP wrapper around the corpus +
// findings produced by the orchestrator. The server is constructed
// against an already-built corpus + findings list; rebuilds happen
// out-of-band (caller stops the server, rebuilds, starts a new one).
//
// Auth per D5: bind defaults to 127.0.0.1; non-localhost bind requires
// BEARER tokens read from the configured env var.

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/librarysnapshot"
	"github.com/sheaf-data/sheaf/internal/llm"
	"github.com/sheaf-data/sheaf/internal/llm/embedcache"
	"github.com/sheaf-data/sheaf/internal/prbot"
	"github.com/sheaf-data/sheaf/internal/review"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Hardening defaults for the HTTP server. These bound how long a
// single connection can tie up a goroutine and how large a request
// body the server will read, so a slow or oversized client cannot
// wedge or OOM a long-lived `sheaf serve` process (audit #2).
const (
	// defaultReadTimeout caps the time to read the entire request
	// (headers + body). Generous enough for a large library_snapshot
	// POST, tight enough that a slow-body client cannot pin a
	// connection open indefinitely.
	defaultReadTimeout = 30 * time.Second
	// defaultWriteTimeout caps the time spent writing the response.
	// review_pr can be heavy; this is the per-response ceiling, not a
	// whole-review deadline (that is derived from the request ctx).
	defaultWriteTimeout = 60 * time.Second
	// defaultIdleTimeout caps how long a keep-alive connection may sit
	// idle between requests before the server closes it, reclaiming the
	// goroutine. Without it, idle keep-alive conns accumulate.
	defaultIdleTimeout = 120 * time.Second
	// maxRequestBodyBytes bounds a single JSON-RPC request body. 8 MiB
	// is far above any legitimate request (the largest is a review_pr
	// with two paths) and well below a memory-pressure threshold.
	maxRequestBodyBytes = 8 << 20 // 8 MiB
	// defaultReviewConcurrency caps concurrent review_pr operations.
	// Each review scans two checked-out workspaces (CPU + FD heavy),
	// so a small bound prevents a burst of reviews from wedging the
	// server (audit #5).
	defaultReviewConcurrency = 2
)

// Server is the MCP server.
type Server struct {
	corpus   *corpus.Corpus
	findings []*findingpb.Finding
	cfg      *configpb.MCPServerConfig

	// Structured logger for per-call log lines. Never nil after
	// construction: New installs a stderr text logger by default;
	// WithLogger overrides it. Writes go to stderr only — stdout is
	// reserved for the human "MCP server listening on …" banner and is
	// the reserved channel should a stdio transport ever be added.
	logger *slog.Logger

	// reqSeq generates a monotonic per-call correlation id used when a
	// request carries no JSON-RPC id (e.g. notifications) so panic logs
	// and their client-facing data.correlation_id can still be matched.
	reqSeq atomic.Uint64

	// Optional embedder for semantic find_examples. If nil, falls
	// back to token-overlap scoring.
	embedder llm.Embedder
	cache    embedcache.Cache

	// Lazy: element-id → embedding vector. Populated on first
	// find_examples call when embedder is set.
	embedMu      sync.Mutex
	elementEmbed map[string][]float32

	// Optional review surface — enables `review_pr` MCP operation.
	// Configured via WithReview; the server runs the full scan
	// pipeline per request using the same sheafConfig + rules.
	sheafConfig   *configpb.Config
	rules         *categorizationpb.Rules
	reviewAdapter review.Adapter

	// reviewSem bounds concurrent review_pr operations (audit #5).
	// Buffered to defaultReviewConcurrency; nil until WithReview.
	reviewSem chan struct{}

	// testHooks is a test-only dispatch seam: when non-nil and a request
	// names a method present in the map, dispatch invokes the hook
	// instead of a real op. It exists solely so tests can exercise the
	// panic→-32603 recovery path (dispatchSafe) deterministically
	// without depending on a fragile adversarial input that a later
	// hardening pass might fix. Always nil in production (only
	// installed from *_test.go); guarded by a single nil check below.
	testHooks map[string]func(json.RawMessage) (any, *rpcError)

	httpSrv *http.Server
}

// New constructs a Server from an already-built corpus + findings
// list and an optional MCPServerConfig (nil defaults to localhost:7700,
// no auth, 1h cache TTL).
func New(c *corpus.Corpus, findings []*findingpb.Finding, cfg *configpb.MCPServerConfig) *Server {
	if cfg == nil {
		cfg = &configpb.MCPServerConfig{Bind: "127.0.0.1", Port: 7700, CacheTtlSeconds: 3600}
	}
	if cfg.GetBind() == "" {
		cfg.Bind = "127.0.0.1"
	}
	if cfg.GetPort() == 0 {
		cfg.Port = 7700
	}
	return &Server{
		corpus:   c,
		findings: findings,
		cfg:      cfg,
		// Default logger: text to stderr. Replaceable via WithLogger.
		logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// WithLogger sets the structured logger used for per-call log lines.
// Passing nil resets to the default (text handler on os.Stderr) rather
// than disabling logging — callers that want to silence logs should
// pass a logger backed by io.Discard. Returns the server for chaining.
//
// Logs always go to stderr by convention even when the caller supplies
// the handler: stdout carries the human-facing banner and is reserved
// for a possible future stdio transport.
func (s *Server) WithLogger(l *slog.Logger) *Server {
	if l == nil {
		l = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	s.logger = l
	return s
}

// WithEmbedder enables semantic find_examples backed by the given
// embedder + cache. Pass nil cache to skip on-disk caching.
// Returns the server for chaining.
func (s *Server) WithEmbedder(e llm.Embedder, cache embedcache.Cache) *Server {
	if e == nil {
		return s
	}
	if _, ok := e.(llm.NoopEmbedder); ok {
		return s
	}
	s.embedder = e
	s.cache = cache
	return s
}

// WithReview enables the `review_pr` operation. The server runs the
// scan pipeline against the base + head paths supplied in each
// request, using the provided sheaf config + rules. The adapter
// (nil → noop) is used when callers set post: true.
func (s *Server) WithReview(cfg *configpb.Config, rules *categorizationpb.Rules, adapter review.Adapter) *Server {
	s.sheafConfig = cfg
	s.rules = rules
	if adapter == nil {
		adapter = review.Noop{}
	}
	s.reviewAdapter = adapter
	// Bound concurrent reviews (audit #5). Buffered semaphore acquired
	// at the top of opReviewPR with a select on ctx.Done() so a waiting
	// request stays cancelable.
	s.reviewSem = make(chan struct{}, defaultReviewConcurrency)
	return s
}

// Addr returns the canonical bind address (host:port).
func (s *Server) Addr() string {
	return fmt.Sprintf("%s:%d", s.cfg.GetBind(), s.cfg.GetPort())
}

// Start begins serving on the configured address. Blocks until
// ListenAndServe returns. Use Shutdown to stop.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/mcp", s.handleMCP)

	// Auth wrap.
	handler := s.authMiddleware(mux)

	s.httpSrv = &http.Server{
		Addr:              s.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Bound per-connection lifetime so a slow client cannot pin a
		// goroutine open for the life of the process (audit #2).
		ReadTimeout:  defaultReadTimeout,
		WriteTimeout: defaultWriteTimeout,
		IdleTimeout:  defaultIdleTimeout,
	}
	ln, err := net.Listen("tcp", s.Addr())
	if err != nil {
		return err
	}
	// Graceful shutdown: when ctx is canceled (SIGINT/SIGTERM wired by
	// runServe), drain in-flight requests with a bounded deadline. The
	// goroutine exits after Shutdown returns, so it no longer blocks for
	// the whole process lifetime (audit #6). A separate done channel
	// lets us wait for that drain before Serve's caller returns.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), defaultWriteTimeout)
		defer cancel()
		if err := s.Shutdown(shCtx); err != nil {
			s.logger.Error("mcp shutdown error", "err", err)
		}
	}()
	err = s.httpSrv.Serve(ln)
	// On graceful shutdown Serve returns ErrServerClosed; wait for the
	// drain goroutine to finish so callers see a fully-stopped server,
	// then report a clean stop (nil) to distinguish it from a real bind
	// or accept failure.
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	return err
}

// Shutdown stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// authMiddleware enforces D5: bearer-token check when configured.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := s.cfg.GetAuth()
		if auth == nil || auth.GetMode() == configpb.AuthConfig_NONE || auth.GetMode() == configpb.AuthConfig_MODE_UNSPECIFIED {
			next.ServeHTTP(w, r)
			return
		}
		if auth.GetMode() == configpb.AuthConfig_BEARER {
			env := auth.GetBearerTokenEnv()
			want := os.Getenv(env)
			if want == "" {
				http.Error(w, "server bearer auth misconfigured: env "+env+" not set", http.StatusInternalServerError)
				return
			}
			h := r.Header.Get("Authorization")
			if h != "Bearer "+want {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	stats := s.corpus.Stats()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"elements": stats.Elements,
		"profiles": stats.Profiles,
		"findings": len(s.findings),
		"server":   "sheaf-mcp",
		"version":  "0.1.0",
	})
}

// --- JSON-RPC handling ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Bound the body read (audit #2). MaxBytesReader makes Decode fail
	// once the limit is exceeded; that surfaces as a -32700 parse error
	// below, which is the existing contract for an unreadable body.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error: "+err.Error(), nil)
		return
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		s.writeError(w, req.ID, -32600, "invalid request: jsonrpc version", nil)
		return
	}
	// Thread the request context so client disconnect / server timeout
	// cancels in-flight work (audit #4), and time the call for the log
	// line. dispatchSafe converts any panic into a -32603 (audit #1).
	start := time.Now()
	result, rerr := s.dispatchSafe(r.Context(), req.Method, req.ID, req.Params)
	s.logCall(r.Context(), req.Method, req.ID, time.Since(start), rerr)
	if rerr != nil {
		s.writeError(w, req.ID, rerr.Code, rerr.Message, rerr.Data)
		return
	}
	s.writeResult(w, req.ID, result)
}

// logCall emits exactly one structured record per dispatched call:
// method, request id, duration, and outcome. ok/bad-params/not-found
// log at info/warn; an internal -32603 logs at error. The id is logged
// as a string (it is raw JSON on the wire — a number, string, or null).
func (s *Server) logCall(ctx context.Context, method string, id json.RawMessage, dur time.Duration, rerr *rpcError) {
	attrs := []slog.Attr{
		slog.String("method", method),
		slog.String("id", rpcIDString(id)),
		slog.Duration("dur", dur),
	}
	if rerr == nil {
		attrs = append(attrs, slog.String("outcome", "ok"))
		s.logger.LogAttrs(ctx, slog.LevelInfo, "rpc call", attrs...)
		return
	}
	attrs = append(attrs,
		slog.String("outcome", "error"),
		slog.Int("code", rerr.Code),
		slog.String("err", rerr.Message),
	)
	level := slog.LevelWarn
	if rerr.Code == codeInternalError {
		level = slog.LevelError
	}
	s.logger.LogAttrs(ctx, level, "rpc call", attrs...)
}

// rpcIDString renders a JSON-RPC id (raw JSON: number, string, or null)
// as a plain string for logging. Empty/absent id → "null".
func rpcIDString(id json.RawMessage) string {
	if len(id) == 0 {
		return "null"
	}
	return string(id)
}

func (s *Server) writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0", ID: id,
		Error: &rpcError{Code: code, Message: msg, Data: data},
	})
}

func (s *Server) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0", ID: id, Result: result,
	})
}

// --- stdio transport ---

// ServeStdio runs the MCP server over a newline-delimited JSON-RPC stream
// on in/out — the transport MCP clients (Claude Desktop, Cursor, Cline)
// use when they spawn `sheaf serve` as a subprocess. Each line of `in` is
// one JSON-RPC request; each response is written as one compact JSON line
// to `out`. Requests without an id are notifications and receive no
// response, per JSON-RPC 2.0. Diagnostics go to the server logger
// (stderr) — `out` carries protocol messages only, so anything else
// written there corrupts the stream. Returns nil when `in` reaches EOF
// (the client closed the pipe) or ctx is canceled (shutdown signal); a
// read error otherwise.
//
// Requests are handled serially: one response is fully written before the
// next request is read, so writes to `out` never interleave.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	// A dedicated reader goroutine lets ctx cancellation (SIGINT) return
	// promptly even while the main loop would otherwise block on stdin.
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReader(in)
		for {
			line, err := readStdioLine(r)
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case line := <-lines:
			s.handleStdioLine(ctx, line, out)
		}
	}
}

// readStdioLine reads one '\n'-delimited line (newline included on a full
// line; absent on a final unterminated line, which arrives with io.EOF).
// A line longer than the HTTP body cap is rejected to bound memory.
func readStdioLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if len(line) > maxRequestBodyBytes {
		return nil, fmt.Errorf("request line exceeds %d bytes", maxRequestBodyBytes)
	}
	return line, err
}

// handleStdioLine parses one line as a JSON-RPC request, dispatches it
// through the same core the HTTP handler uses, and writes a response
// unless the request was a notification.
func (s *Server) handleStdioLine(ctx context.Context, line []byte, out io.Writer) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		// Can't recover an id from an unparseable line; reply with null id.
		s.writeStdioResponse(out, rpcResponse{
			JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		if !isNotification(req.ID) {
			s.writeStdioResponse(out, rpcResponse{
				JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: -32600, Message: "invalid request: jsonrpc version"},
			})
		}
		return
	}
	start := time.Now()
	result, rerr := s.dispatchSafe(ctx, req.Method, req.ID, req.Params)
	s.logCall(ctx, req.Method, req.ID, time.Since(start), rerr)
	if isNotification(req.ID) {
		return // JSON-RPC notifications get no response.
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	s.writeStdioResponse(out, resp)
}

// writeStdioResponse marshals resp to compact JSON and writes it as one
// line. json.Marshal escapes any newline inside string values, so the
// framing invariant — exactly one message per line — always holds.
func (s *Server) writeStdioResponse(out io.Writer, resp rpcResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "marshal stdio response", slog.Any("err", err))
		return
	}
	b = append(b, '\n')
	if _, err := out.Write(b); err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "write stdio response", slog.Any("err", err))
	}
}

// isNotification reports whether a JSON-RPC id is absent — the marker of a
// notification, which gets no response. An explicit null id counts as a
// request (answered with a null id).
func isNotification(id json.RawMessage) bool {
	return len(id) == 0
}

// --- Operation dispatch ---

// codeInternalError is the JSON-RPC 2.0 "internal error" code, returned
// by dispatchSafe when an op panics. It is the only code that logs at
// error level.
const codeInternalError = -32603

// dispatchSafe wraps dispatch in a recover() so a panic in any op (a bad
// type assertion or nil-map index on client-controlled input) becomes a
// well-formed JSON-RPC -32603 instead of crashing the request goroutine
// or dropping the connection (audit #1). The panic value + stack are
// logged at error level; the client sees only a generic "internal error"
// plus a correlation id in data so a report can be tied back to the log.
func (s *Server) dispatchSafe(ctx context.Context, method string, id, params json.RawMessage) (result any, rerr *rpcError) {
	defer func() {
		if rec := recover(); rec != nil {
			corr := fmt.Sprintf("mcp-%d", s.reqSeq.Add(1))
			s.logger.LogAttrs(ctx, slog.LevelError, "panic in op",
				slog.String("method", method),
				slog.String("id", rpcIDString(id)),
				slog.String("correlation_id", corr),
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())),
			)
			// Do not leak the panic message to the client (info leak);
			// return a generic message + a correlation id that also
			// appears in the error log above.
			result = nil
			rerr = &rpcError{
				Code:    codeInternalError,
				Message: "internal error",
				Data:    map[string]any{"correlation_id": corr},
			}
		}
	}()
	return s.dispatch(ctx, method, params)
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	// Test-only seam (always nil in production): lets a test register a
	// panicking op to exercise dispatchSafe's recovery path.
	if s.testHooks != nil {
		if hook, ok := s.testHooks[method]; ok {
			return hook(params)
		}
	}
	switch method {
	case "query_contract":
		return s.opQueryContract(params)
	case "coverage":
		return s.opCoverage(params)
	case "find_coverage_gaps":
		return s.opFindCoverageGaps(params)
	case "find_examples":
		return s.opFindExamples(ctx, params)
	case "verify_invocation":
		return s.opVerifyInvocation(params)
	case "review_pr":
		return s.opReviewPR(ctx, params)
	case "list_libraries":
		return s.opListLibraries(params)
	case "library_snapshot":
		return s.opLibrarySnapshot(params)
	case "tools/list":
		return s.opToolsList()
	case "initialize":
		return s.opInitialize(params)
	case "tools/call":
		return s.opToolsCall(ctx, params)
	case "notifications/initialized":
		// MCP lifecycle notification: acknowledged with no result/error.
		// The transport suppresses the response (notifications carry no id).
		return nil, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + method}
}

// query_contract — return the ContractElement + summary of its CoverageProfile.
func (s *Server) opQueryContract(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ElementID string `json:"element_id"`
		Subtree   string `json:"subtree"` // optional: tests|docs|examples|usage|gaps
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	e := s.corpus.Element(p.ElementID)
	if e == nil {
		return nil, &rpcError{Code: -32004, Message: "element not found: " + p.ElementID}
	}
	prof := s.corpus.Profile(p.ElementID)
	out := map[string]any{
		"element": pbToMap(e),
	}
	if prof != nil {
		switch p.Subtree {
		case "tests":
			out["tests"] = pbToMap(prof.GetTests())
		case "docs":
			out["docs"] = pbToMap(prof.GetDocs())
		case "examples":
			out["examples"] = pbToMap(prof.GetExamples())
		case "usage":
			out["usage"] = pbToMap(prof.GetUsage())
		case "gaps":
			out["gaps"] = pbToMap(prof.GetGapsSummary())
		default:
			out["coverage"] = pbToMap(prof)
		}
	}
	return out, nil
}

// coverage — return the CoverageProfile JSON.
func (s *Server) opCoverage(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ElementID string `json:"element_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	prof := s.corpus.Profile(p.ElementID)
	if prof == nil {
		return nil, &rpcError{Code: -32004, Message: "no profile for element: " + p.ElementID}
	}
	return pbToMap(prof), nil
}

// find_coverage_gaps — return findings, optionally filtered.
func (s *Server) opFindCoverageGaps(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Library  string   `json:"library"`
		Kinds    []string `json:"kinds"`
		MaxItems int      `json:"max_items"`
	}
	_ = json.Unmarshal(params, &p)
	kindSet := make(map[string]bool, len(p.Kinds))
	for _, k := range p.Kinds {
		kindSet[strings.ToUpper(k)] = true
	}
	var out []map[string]any
	for _, f := range s.findings {
		if p.Library != "" {
			subj := f.GetSubject()
			if !strings.HasPrefix(subj, p.Library+"/") && !strings.HasPrefix(subj, p.Library+".") {
				continue
			}
		}
		if len(kindSet) > 0 && !kindSet[strings.TrimPrefix(f.GetKind().String(), "FINDING_KIND_")] {
			continue
		}
		m := pbToMap(f)
		if m != nil {
			out = append(out, m)
		}
		if p.MaxItems > 0 && len(out) >= p.MaxItems {
			break
		}
	}
	return map[string]any{
		"findings": out,
		"total":    len(out),
	}, nil
}

// find_examples — return ContractElements matching the description.
// When an embedder is configured, ranks by cosine similarity on text
// embeddings. Otherwise falls back to token-overlap scoring.
func (s *Server) opFindExamples(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Query    string `json:"query"`
		MaxItems int    `json:"max_items"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	if p.MaxItems == 0 {
		p.MaxItems = 10
	}
	if s.embedder != nil {
		out, err := s.semanticFindExamples(ctx, p.Query, p.MaxItems)
		if err == nil {
			return out, nil
		}
		// Fall through to token overlap on embedder failure; include
		// the error in the response so callers know.
		return s.tokenOverlapFindExamples(p.Query, p.MaxItems, err.Error()), nil
	}
	return s.tokenOverlapFindExamples(p.Query, p.MaxItems, ""), nil
}

// semanticFindExamples does an embedding-based search. On first call
// (or after a cache miss), it embeds every element's "searchable
// text" — id + doc-comment + name-tokens. Subsequent queries are
// just one embed (for the query) + cosine compare.
func (s *Server) semanticFindExamples(ctx context.Context, query string, maxItems int) (any, error) {
	if err := s.ensureElementEmbeddings(ctx); err != nil {
		return nil, err
	}
	queryVec, err := s.embedOne(ctx, query)
	if err != nil {
		return nil, err
	}
	type scored struct {
		id    string
		score float32
	}
	var scoredList []scored
	s.embedMu.Lock()
	for id, vec := range s.elementEmbed {
		score := llm.Cosine(queryVec, vec)
		if score > 0 {
			scoredList = append(scoredList, scored{id, score})
		}
	}
	s.embedMu.Unlock()
	sort.SliceStable(scoredList, func(i, j int) bool {
		return scoredList[i].score > scoredList[j].score
	})
	var matches []map[string]any
	for i, sc := range scoredList {
		if i >= maxItems {
			break
		}
		e := s.corpus.Element(sc.id)
		matches = append(matches, map[string]any{
			"element_id":  sc.id,
			"score":       sc.score,
			"kind":        e.GetKind().String(),
			"location":    pbToMap(e.GetLocation()),
			"doc_excerpt": shortExcerpt(e.GetDocCommentExcerpt(), 200),
		})
	}
	return map[string]any{
		"matches":       matches,
		"scoringMethod": "semantic:" + s.embedder.Name(),
	}, nil
}

// ensureElementEmbeddings populates s.elementEmbed lazily on first
// semantic query. Uses the embed cache when configured so warm
// restarts don't re-pay for embeddings.
func (s *Server) ensureElementEmbeddings(ctx context.Context) error {
	s.embedMu.Lock()
	have := s.elementEmbed != nil
	s.embedMu.Unlock()
	if have {
		return nil
	}
	elements := s.corpus.Elements()
	texts := make([]string, len(elements))
	for i, e := range elements {
		texts[i] = searchableText(e)
	}
	var (
		vecs [][]float32
		err  error
	)
	if s.cache != nil {
		vecs, err = embedcache.EmbedWithCache(ctx, s.embedder, s.cache, texts)
	} else {
		vecs, err = s.embedder.Embed(ctx, texts)
	}
	if err != nil {
		return err
	}
	out := make(map[string][]float32, len(elements))
	for i, e := range elements {
		out[e.GetId()] = vecs[i]
	}
	s.embedMu.Lock()
	s.elementEmbed = out
	s.embedMu.Unlock()
	return nil
}

// embedOne is a thin wrapper around Embed for a single string.
func (s *Server) embedOne(ctx context.Context, text string) ([]float32, error) {
	vs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vs) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors for 1 input", len(vs))
	}
	return vs[0], nil
}

// searchableText composes the text used to embed an element. The
// element ID + doc excerpt is usually enough to capture both the
// structural identity and the semantics.
func searchableText(e *contractpb.ContractElement) string {
	var b strings.Builder
	b.WriteString(e.GetId())
	if doc := e.GetDocCommentExcerpt(); doc != "" {
		b.WriteString("\n\n")
		b.WriteString(doc)
	}
	return b.String()
}

// tokenOverlapFindExamples is the deterministic fallback. Same shape
// as the semantic path so callers can interpret scores uniformly.
func (s *Server) tokenOverlapFindExamples(query string, maxItems int, fallbackReason string) any {
	queryToks := tokenize(query)
	type scored struct {
		element *contractpb.ContractElement
		score   int
	}
	var scoredList []scored
	for _, e := range s.corpus.Elements() {
		idToks := tokenize(strings.ReplaceAll(e.GetId(), ".", " "))
		score := overlap(queryToks, idToks)
		if score > 0 {
			scoredList = append(scoredList, scored{e, score})
		}
	}
	sort.SliceStable(scoredList, func(i, j int) bool {
		return scoredList[i].score > scoredList[j].score
	})
	var matches []map[string]any
	for i, sc := range scoredList {
		if i >= maxItems {
			break
		}
		matches = append(matches, map[string]any{
			"element_id":  sc.element.GetId(),
			"score":       sc.score,
			"kind":        sc.element.GetKind().String(),
			"location":    pbToMap(sc.element.GetLocation()),
			"doc_excerpt": shortExcerpt(sc.element.GetDocCommentExcerpt(), 200),
		})
	}
	result := map[string]any{
		"matches":       matches,
		"scoringMethod": "token-overlap",
	}
	if fallbackReason != "" {
		result["fallbackReason"] = fallbackReason
	}
	return result
}

func shortExcerpt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// verify_invocation — check whether a call-site mention plausibly
// matches a real ContractElement. Returns the matched element + a
// confidence score, or null with reasons.
func (s *Server) opVerifyInvocation(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Invocation string `json:"invocation"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	// Exact match first.
	if e := s.corpus.Element(p.Invocation); e != nil {
		return map[string]any{
			"matched":    true,
			"element":    pbToMap(e),
			"confidence": 1.0,
		}, nil
	}
	// Fuzzy: token overlap against element IDs.
	queryToks := tokenize(strings.ReplaceAll(p.Invocation, ".", " "))
	best := struct {
		id    string
		score int
	}{}
	for _, e := range s.corpus.Elements() {
		score := overlap(queryToks, tokenize(strings.ReplaceAll(e.GetId(), ".", " ")))
		if score > best.score {
			best.score = score
			best.id = e.GetId()
		}
	}
	if best.score == 0 {
		return map[string]any{"matched": false, "reason": "no token overlap with any element"}, nil
	}
	return map[string]any{
		"matched":    false,
		"candidate":  best.id,
		"confidence": float64(best.score) / float64(len(queryToks)),
		"reason":     "fuzzy match only — no exact element id",
	}, nil
}

// tools/list — MCP convention: describe what we offer.
func (s *Server) opToolsList() (any, *rpcError) {
	tools := []map[string]any{
		{"name": "query_contract", "description": "Return ContractElement and its CoverageProfile (or a subtree)"},
		{"name": "coverage", "description": "Return the CoverageProfile for an element"},
		{"name": "find_coverage_gaps", "description": "List findings, filterable by library and kind"},
		{"name": "find_examples", "description": "Search for elements matching a query (semantic if embedder configured, else token-overlap)"},
		{"name": "verify_invocation", "description": "Check whether an invocation string matches a real element"},
		{"name": "list_libraries", "description": "List libraries in the corpus with element/profile/finding counts"},
		{"name": "library_snapshot", "description": "Return all ContractElements, CoverageProfiles and Findings for one library — the bulk read used by the report generator"},
	}
	if s.sheafConfig != nil {
		tools = append(tools, map[string]any{
			"name":        "review_pr",
			"description": "Run a PR review: scan base + head paths, render comment, optionally post via configured review adapter",
		})
	}
	return map[string]any{"tools": tools}, nil
}

// mcpProtocolVersion is the MCP protocol version advertised in the
// initialize handshake. The envelope sheaf implements (initialize,
// tools/list, tools/call) is stable across recent MCP revisions, so the
// handshake echoes the client's requested version when present and falls
// back to this otherwise.
const mcpProtocolVersion = "2024-11-05"

// opInitialize answers the MCP `initialize` handshake: it advertises the
// tools capability and identifies the server. A spec-compliant client
// (Claude Desktop, Cursor, Cline) sends this first and will not issue
// tools/list or tools/call until it succeeds.
func (s *Server) opInitialize(params json.RawMessage) (any, *rpcError) {
	version := mcpProtocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
			version = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "sheaf", "version": "0.1.0"},
	}, nil
}

// opToolsCall implements the MCP `tools/call` envelope. It unwraps
// {name, arguments}, routes name to the same op the bare JSON-RPC method
// reaches, and wraps the result in MCP content (text JSON +
// structuredContent). An unknown tool is a protocol error (-32602); an
// op-level failure is returned as an isError result so the calling model
// reads the message instead of seeing a transport fault.
func (s *Server) opToolsCall(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
		}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: -32602, Message: "invalid params: tool name required"}
	}
	// The envelope methods are not themselves callable tools.
	switch p.Name {
	case "initialize", "tools/list", "tools/call", "notifications/initialized":
		return nil, &rpcError{Code: -32602, Message: "not a callable tool: " + p.Name}
	}
	result, rerr := s.dispatch(ctx, p.Name, p.Arguments)
	if rerr != nil {
		if rerr.Code == -32601 { // unknown method → unknown tool
			return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
		}
		// Tool ran and failed: surface as an MCP isError result.
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": rerr.Message}},
			"isError": true,
		}, nil
	}
	text, err := json.Marshal(result)
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "marshal tool result"}
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(text)}},
		"structuredContent": result,
	}, nil
}

// list_libraries — enumerate the libraries present in the corpus.
// Returns one entry per library with element / profile / finding
// counts so a picker UI can show useful metadata before drill-down.
func (s *Server) opListLibraries(_ json.RawMessage) (any, *rpcError) {
	type libAgg struct {
		Library  string `json:"library"`
		Elements int    `json:"elements"`
		Profiles int    `json:"profiles"`
		Findings int    `json:"findings"`
	}
	aggs := make(map[string]*libAgg)
	for _, e := range s.corpus.Elements() {
		lib := e.GetLibrary()
		if lib == "" {
			lib = "(unspecified)"
		}
		a, ok := aggs[lib]
		if !ok {
			a = &libAgg{Library: lib}
			aggs[lib] = a
		}
		a.Elements++
		if s.corpus.Profile(e.GetId()) != nil {
			a.Profiles++
		}
	}
	// Findings are keyed by subject ("library/element_id" or
	// "library.element_id"). Library names themselves contain dots
	// (e.g. fuchsia.io), so match against the known library set
	// rather than splitting on the first separator.
	for _, f := range s.findings {
		subj := f.GetSubject()
		for lib, a := range aggs {
			if strings.HasPrefix(subj, lib+"/") || strings.HasPrefix(subj, lib+".") {
				a.Findings++
				break
			}
		}
	}
	out := make([]*libAgg, 0, len(aggs))
	for _, a := range aggs {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Library < out[j].Library })
	return map[string]any{"libraries": out, "total": len(out)}, nil
}

// library_snapshot — bulk dump for one library: elements + their
// CoverageProfiles + findings scoped to it. The report generator
// consumes this in a single round trip.
func (s *Server) opLibrarySnapshot(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Library string `json:"library"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	if p.Library == "" {
		return nil, &rpcError{Code: -32602, Message: "library is required"}
	}
	// The projection (element/profile loop, finding subject-matching,
	// analyzer + surface extraction) is shared with the in-process render
	// path via internal/librarysnapshot so the two cannot drift. This op
	// keeps assembling its own response map to preserve the exact wire
	// shape (nil slices marshal to null; all keys always present).
	proj := librarysnapshot.Project(s.corpus, s.findings, s.sheafConfig, p.Library)
	return map[string]any{
		"schema_version":    librarysnapshot.SchemaVersion,
		"library":           p.Library,
		"elements":          proj.Elements,
		"profiles":          proj.Profiles,
		"findings":          proj.Findings,
		"analyzers":         proj.Analyzers,
		"surfaces_required": proj.SurfacesRequired,
	}, nil
}

// review_pr — run the PR-bot flow against two pre-checked-out paths.
// Sheaf is git-agnostic; the caller hands it base_path + head_path
// already-checked-out workspaces.
func (s *Server) opReviewPR(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	if s.sheafConfig == nil {
		return nil, &rpcError{Code: -32601, Message: "review_pr is not enabled on this server (configure with WithReview)"}
	}
	var p struct {
		PRRef    string `json:"pr_ref"`
		BasePath string `json:"base_path"`
		HeadPath string `json:"head_path"`
		Post     bool   `json:"post"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	if p.BasePath == "" || p.HeadPath == "" {
		return nil, &rpcError{Code: -32602, Message: "base_path and head_path are required"}
	}
	if p.PRRef == "" {
		p.PRRef = "PR#unknown"
	}
	// Concurrency guard (audit #5): each review scans two workspaces and
	// is CPU/FD heavy. Acquire a slot, but stay cancelable — if the
	// client disconnects or the request times out while we are queued
	// behind other reviews, bail with -32000 rather than block a
	// goroutine indefinitely.
	if s.reviewSem != nil {
		select {
		case s.reviewSem <- struct{}{}:
			defer func() { <-s.reviewSem }()
		case <-ctx.Done():
			return nil, &rpcError{Code: -32000, Message: "review_pr: canceled while waiting for a review slot: " + ctx.Err().Error()}
		}
	}
	res, err := prbot.RunReview(ctx, prbot.RunOptions{
		Config:   s.sheafConfig,
		Rules:    s.rules,
		BaseRoot: p.BasePath,
		HeadRoot: p.HeadPath,
		PRRef:    p.PRRef,
		Post:     p.Post,
		Adapter:  s.reviewAdapter,
	})
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "review_pr: " + err.Error()}
	}
	out := map[string]any{
		"pr_ref":              p.PRRef,
		"comment_md":          res.Comment.Body,
		"affected_elements":   res.Comment.AffectedElements,
		"suggested_reviewers": res.Comment.SuggestedReviewers,
		"subscribers":         res.Comment.Subscribers,
		"posted":              res.Posted,
	}
	if res.Posted {
		out["posted_to"] = res.PostedTo
		out["adapter"] = res.Adapter
	}
	return out, nil
}

// --- helpers ---

// pbToMap marshals a protobuf message through protojson and decodes
// it into a generic map[string]any. Returns nil for nil input.
func pbToMap(msg any) map[string]any {
	if msg == nil {
		return nil
	}
	pm, ok := msg.(protoreflect.ProtoMessage)
	if !ok {
		// Not a proto — best-effort json round-trip.
		b, err := json.Marshal(msg)
		if err != nil {
			return map[string]any{"_raw": fmt.Sprintf("%v", msg)}
		}
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return out
	}
	b, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(pm)
	if err != nil {
		return map[string]any{"_error": err.Error()}
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func tokenize(s string) []string {
	var toks []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-' || r == '.' || r == '/' || r == ',' || r == ':' || r == '!' || r == '?'
	}) {
		toks = append(toks, strings.ToLower(p))
	}
	return toks
}

func overlap(a, b []string) int {
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[t] = true
	}
	n := 0
	for _, t := range b {
		if set[t] {
			n++
		}
	}
	return n
}

// Error sentinel used by tests.
var ErrShutdown = errors.New("mcp: server shutdown")

// Ensure CoverageProfile interface is referenced for typecheck.
var _ = (*coveragepb.CoverageProfile)(nil)
