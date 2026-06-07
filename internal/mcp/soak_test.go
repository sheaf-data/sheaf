//go:build soak

// Package mcp soak test. Build-tagged so it is invisible to the default
// `go test ./...` / `make check` suite (audit/plan 9139F6E8): it runs a
// sustained tool-call loop and asserts the server does not leak
// goroutines or grow the heap unboundedly.
//
// Run it explicitly:
//
//	go test -tags soak ./internal/mcp -run Soak -v
//
// Iteration count is configurable via SHEAF_SOAK_ITERS (default 5000).
package mcp

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"testing"
	"time"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// soakEmbedder is a deterministic in-process embedder so the warmup
// find_examples populates the element-embedding map (audit #7's
// one-time growth) without any network dependency. Every text maps to a
// small fixed-dimension vector derived from its length, which is enough
// for the soak loop's find_examples to do real cosine work each call.
type soakEmbedder struct{}

func (soakEmbedder) Name() string { return "soak" }
func (soakEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		// 3-dim vector seeded from the text so different elements get
		// different (non-zero) vectors; cheap and allocation-bounded.
		n := float32(len(t)%7 + 1)
		out[i] = []float32{n, n / 2, 1}
	}
	return out, nil
}

// mkSoakServer builds a non-trivial corpus (so the read ops do real
// work) and stands up a live HTTP server with the in-process embedder.
func mkSoakServer(t *testing.T, elements int) (string, func()) {
	t.Helper()
	c := corpus.New()
	var findings []*findingpb.Finding
	for i := 0; i < elements; i++ {
		id := fmt.Sprintf("soak.lib/Type%d.Method%d", i, i)
		c.AddElement(&contractpb.ContractElement{
			Id:                id,
			Kind:              contractpb.ContractElementKind_METHOD,
			Library:           "soak.lib",
			DocCommentExcerpt: fmt.Sprintf("Method %d does a thing relative to type %d", i, i),
			Location:          &commonpb.SourceLocation{Path: "soak.fidl", Line: uint32(i + 1)},
		})
		if i%2 == 0 {
			c.SetProfile(&coveragepb.CoverageProfile{
				ElementId: id,
				Tests:     &coveragepb.TestCoverage{Unit: []*commonpb.TestRef{{TestName: fmt.Sprintf("Test%d", i)}}},
			})
		}
		findings = append(findings, &findingpb.Finding{
			Id:       fmt.Sprintf("x:missing:%s:examples", id),
			Kind:     findingpb.FindingKind_MISSING_IN_CATEGORY,
			Subject:  id,
			Severity: commonpb.Severity_WARNING,
			Analyzer: "missing-in-category",
		})
	}
	srv := New(c, findings, &configpb.MCPServerConfig{
		Bind: "127.0.0.1", Port: uint32(freePort(t)), CacheTtlSeconds: 60,
	}).WithEmbedder(soakEmbedder{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Start(ctx) }()
	addr := "http://" + srv.Addr()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			stop := func() {
				cancel()
				// Give the graceful-shutdown goroutine a moment to drain
				// so the next test (or the goroutine assertion) sees a
				// fully-stopped server.
				time.Sleep(100 * time.Millisecond)
			}
			return addr, stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatalf("soak server did not become reachable")
	return "", func() {}
}

func TestSoak_NoLeaks(t *testing.T) {
	iters := 5000
	if v := os.Getenv("SHEAF_SOAK_ITERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("SHEAF_SOAK_ITERS=%q: %v", v, err)
		}
		iters = n
	}

	addr, stop := mkSoakServer(t, 200)
	defer stop()

	// Warmup: one find_examples populates the element-embedding map.
	// This is one-time growth (audit #7) and MUST precede the baseline,
	// or the soak will misread it as a per-request leak.
	if r := rpcCall(t, addr, "find_examples", map[string]any{"query": "method thing"}); r["error"] != nil {
		t.Fatalf("warmup find_examples: %v", r["error"])
	}
	// Let the keep-alive pool and any warmup transients settle.
	for i := 0; i < 50; i++ {
		rpcCall(t, addr, "list_libraries", map[string]any{})
	}

	// Baseline AFTER warmup.
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	g0 := runtime.NumGoroutine()

	// Mixed read workload. Each is deterministic and allocation-bounded.
	ops := []func(){
		func() { rpcCall(t, addr, "query_contract", map[string]any{"element_id": "soak.lib/Type1.Method1"}) },
		func() { rpcCall(t, addr, "coverage", map[string]any{"element_id": "soak.lib/Type0.Method0"}) },
		func() { rpcCall(t, addr, "find_coverage_gaps", map[string]any{"max_items": 10}) },
		func() { rpcCall(t, addr, "list_libraries", map[string]any{}) },
		func() { rpcCall(t, addr, "library_snapshot", map[string]any{"library": "soak.lib"}) },
		func() { rpcCall(t, addr, "verify_invocation", map[string]any{"invocation": "soak.lib/Type2.Method2"}) },
		func() { rpcCall(t, addr, "find_examples", map[string]any{"query": "method thing", "max_items": 5}) },
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < iters; i++ {
		ops[rng.Intn(len(ops))]()
	}

	// End measurement.
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	g1 := runtime.NumGoroutine()

	t.Logf("soak: iters=%d goroutines %d→%d heapInuse %d→%d heapAlloc %d→%d",
		iters, g0, g1, m0.HeapInuse, m1.HeapInuse, m0.HeapAlloc, m1.HeapAlloc)

	// Goroutine assertion: tolerate a small constant for http keep-alive
	// pool jitter. A genuine per-request leak grows with iters and blows
	// past this immediately.
	if g1 > g0+4 {
		dumpGoroutines(t)
		t.Errorf("goroutine leak: baseline %d, after %d iters %d (allowed +4)", g0, iters, g1)
	}

	// Heap assertion: bounded, not flat. GC noise means exact equality
	// would flake; the leak signal is monotonic growth proportional to
	// iters. Factor 1.5x over a post-warmup baseline is comfortably
	// above GC jitter for this workload and well below what a real
	// per-request retention would produce. Tune via one clean run if the
	// fixture/workload changes; the chosen factor + reasoning live here.
	const factor = 1.5
	if m1.HeapInuse > uint64(float64(m0.HeapInuse)*factor) {
		t.Errorf("heap growth: HeapInuse baseline %d, after %d iters %d (> %.1fx)",
			m0.HeapInuse, iters, m1.HeapInuse, factor)
	}
}

// dumpGoroutines writes all goroutine stacks to stderr so a leak failure
// is diagnosable (which goroutine, what it is blocked on).
func dumpGoroutines(t *testing.T) {
	t.Helper()
	_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 1)
}
