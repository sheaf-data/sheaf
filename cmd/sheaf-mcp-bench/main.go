// Command sheaf-mcp-bench measures per-tool latency and response size for
// the sheaf MCP server against a real scanned corpus. It builds the corpus
// once (the same ingest→index pipeline `sheaf serve` runs), then drives
// each MCP operation N times over an in-process stdio transport and
// reports p50/p95 latency plus response size.
//
// Notes on what's measured:
//   - Latency is the operation's in-process compute cost (the stdio pipe
//     handoff is negligible); the MCP `tools/call` envelope adds only a
//     small constant wrapper over these bare-method calls.
//   - "Response size" is bytes. Token cost tracks bytes closely (~4
//     bytes/token for JSON) and is provider-independent, so bytes are the
//     stable proxy; the lean module deliberately avoids a tokenizer dep.
//   - review_pr is excluded (it scans two external workspaces per call).
//
// Usage:
//
//	sheaf-mcp-bench --config sheaf.textproto --repo . [--n 50] [--out bench.md]
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/mcp"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
)

func main() {
	var configPath, repoPath, outPath string
	var n int
	flag.StringVar(&configPath, "config", "", "Path to sheaf.textproto (default <repo>/sheaf.textproto)")
	flag.StringVar(&repoPath, "repo", ".", "Project repo root")
	flag.IntVar(&n, "n", 50, "Iterations per tool")
	flag.StringVar(&outPath, "out", "", "Optional: also write the results table to this markdown file")
	flag.Parse()

	if err := run(configPath, repoPath, n, outPath); err != nil {
		fmt.Fprintln(os.Stderr, "sheaf-mcp-bench:", err)
		os.Exit(1)
	}
}

type toolResult struct {
	tool  string
	p50   time.Duration
	p95   time.Duration
	bytes int
}

func run(configPath, repoPath string, n int, outPath string) error {
	if n < 1 {
		n = 1
	}
	if configPath == "" {
		configPath = filepath.Join(repoPath, "sheaf.textproto")
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Source map is optional; resolve it next to the config (the example
	// configs' convention).
	var rules *categorizationpb.Rules
	rulesPath := filepath.Join(filepath.Dir(configPath), "categorization-rules.textproto")
	if _, statErr := os.Stat(rulesPath); statErr == nil {
		rules, _ = config.LoadRules(rulesPath)
	}

	o, err := orchestrator.New(cfg, rules, repoPath)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	fmt.Fprintln(os.Stderr, "scanning corpus (one-time, same as sheaf serve startup)…")
	res, err := o.Run(context.Background())
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	// Pick a representative element + its library from the corpus so the
	// per-element ops exercise a real lookup rather than a miss.
	var elemID, lib string
	for _, e := range res.Corpus.Elements() {
		if e.GetLibrary() != "" {
			elemID, lib = e.GetId(), e.GetLibrary()
			break
		}
	}
	if elemID == "" && len(res.Corpus.Elements()) > 0 {
		elemID = res.Corpus.Elements()[0].GetId()
	}

	srv := mcp.New(res.Corpus, res.Findings, cfg.GetMcpServer())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.ServeStdio(ctx, inR, outW) }()
	defer inW.Close()
	br := bufio.NewReader(outR)

	type call struct{ tool, params string }
	calls := []call{
		{"tools/list", `{}`},
		{"list_libraries", `{}`},
		{"find_coverage_gaps", `{}`},
		{"find_examples", `{"query":"open a directory"}`},
	}
	if lib != "" {
		calls = append(calls, call{"library_snapshot", fmt.Sprintf(`{"library":%q}`, lib)})
	}
	if elemID != "" {
		calls = append(calls,
			call{"query_contract", fmt.Sprintf(`{"element_id":%q}`, elemID)},
			call{"coverage", fmt.Sprintf(`{"element_id":%q}`, elemID)},
		)
	}

	var results []toolResult
	id := 0
	for _, c := range calls {
		durs := make([]time.Duration, 0, n)
		var lastBytes int
		for i := 0; i < n; i++ {
			id++
			req := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, c.tool, c.params)
			t0 := time.Now()
			if _, werr := io.WriteString(inW, req+"\n"); werr != nil {
				return fmt.Errorf("%s: write: %w", c.tool, werr)
			}
			line, rerr := br.ReadBytes('\n')
			d := time.Since(t0)
			if rerr != nil {
				return fmt.Errorf("%s: read response: %w", c.tool, rerr)
			}
			if i == 0 && strings.Contains(string(line), `"error"`) {
				fmt.Fprintf(os.Stderr, "warning: %s returned a JSON-RPC error (still timed): %s\n", c.tool, trim(line))
			}
			durs = append(durs, d)
			lastBytes = len(line)
		}
		sort.Slice(durs, func(a, b int) bool { return durs[a] < durs[b] })
		results = append(results, toolResult{tool: c.tool, p50: pctile(durs, 50), p95: pctile(durs, 95), bytes: lastBytes})
	}

	table := renderTable(res, lib, elemID, n, results)
	fmt.Print(table)
	if outPath != "" {
		if werr := os.WriteFile(outPath, []byte(table), 0o644); werr != nil {
			return fmt.Errorf("write %s: %w", outPath, werr)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", outPath)
	}
	return nil
}

// pctile returns the p-th percentile of an ascending-sorted slice using
// the nearest-rank method.
func pctile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p * (len(sorted) - 1)) / 100
	return sorted[idx]
}

func renderTable(res *orchestrator.Result, lib, elemID string, n int, results []toolResult) string {
	var b strings.Builder
	st := res.Corpus.Stats()
	fmt.Fprintf(&b, "# sheaf MCP per-tool benchmark\n\n")
	fmt.Fprintf(&b, "Corpus: %d elements, %d tests, %d doc claims, %d coverage profiles. ",
		st.Elements, st.Tests, st.DocClaims, st.Profiles)
	fmt.Fprintf(&b, "%d iterations per tool. Per-element ops use `%s` (library `%s`).\n\n", n, elemID, lib)
	fmt.Fprintf(&b, "| Tool | p50 | p95 | response bytes |\n")
	fmt.Fprintf(&b, "|---|--:|--:|--:|\n")
	for _, r := range results {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %d |\n", r.tool, ms(r.p50), ms(r.p95), r.bytes)
	}
	fmt.Fprintf(&b, "\n_Latency is in-process op compute (stdio handoff negligible); response bytes are the token proxy (~4 bytes/token). Generated by `cmd/sheaf-mcp-bench`; absolute numbers are machine-specific._\n")
	return b.String()
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.3f ms", float64(d.Microseconds())/1000.0)
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
