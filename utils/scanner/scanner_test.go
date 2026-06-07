package scanner

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/mcp"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// startTestServer boots an in-process MCP server populated with two
// elements: one fully bridged, one with only a concept doc + a stale
// finding (so the report exercises both bridged and drift paths).
func startTestServer(t *testing.T) string {
	t.Helper()
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "demo.lib/A.bridged", Kind: contractpb.ContractElementKind_METHOD,
		Library:  "demo.lib",
		Location: &commonpb.SourceLocation{Path: "src/a.go", Line: 10},
	})
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "demo.lib/A.bridged",
		Docs: &coveragepb.DocCoverage{Concept: []*commonpb.DocRef{
			{Path: "docs/a.md", Line: 1, Substance: commonpb.Substance_SUBSTANTIVE, Words: 200},
		}},
		// Interface METHODs are bridged via the implementations
		// surface (populated from IMPLEMENTS relationships) — not via
		// the tests surface, which interface kinds no longer carry.
		// The fixture's tests entry is retained to exercise the
		// renderer's Tests-panel suppression for interface kinds.
		Tests:    &coveragepb.TestCoverage{Unit: []*commonpb.TestRef{{TestName: "ATest", Path: "test/a_test.go"}}},
		Examples: &coveragepb.ExampleCoverage{InTree: []*commonpb.CodeRef{{Path: "examples/a.go"}}},
		Implementations: &coveragepb.ImplementationCoverage{Impls: []*coveragepb.ImplementationRef{
			{ImplElementId: "cpp:src/a_impl.h#AImpl", ImplKind: "CPP_CLASS", Path: "src/a_impl.h", Line: 5},
		}},
	})

	c.AddElement(&contractpb.ContractElement{
		Id: "demo.lib/B.stale", Kind: contractpb.ContractElementKind_METHOD,
		Library:  "demo.lib",
		Location: &commonpb.SourceLocation{Path: "src/b.go", Line: 22},
	})
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "demo.lib/B.stale",
		Docs: &coveragepb.DocCoverage{Concept: []*commonpb.DocRef{
			{Path: "docs/b.md", Line: 3, Substance: commonpb.Substance_PARTIAL, Words: 40},
		}},
	})

	findings := []*findingpb.Finding{
		{
			Id: "f1", Kind: findingpb.FindingKind_STALE_DOC,
			Subject: "demo.lib/B.stale", Severity: commonpb.Severity_WARNING,
			Analyzer: "stale-doc",
			Message:  "doc references removed parameter `force`",
			Evidence: []*findingpb.EvidencePointer{{
				Description: "doc reference",
				Location:    &commonpb.SourceLocation{Path: "docs/b.md", Line: 3},
			}},
		},
	}

	srv := mcp.New(c, findings, &configpb.MCPServerConfig{
		Bind: "127.0.0.1", Port: 27801, CacheTtlSeconds: 60,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Start(ctx) }()
	addr := "http://" + srv.Addr()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(addr + "/healthz"); err == nil {
			resp.Body.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server didn't come up")
	return ""
}

func TestScanner_ListThenRender(t *testing.T) {
	addr := startTestServer(t)
	c := NewClient(addr, "")

	libs, err := c.ListLibraries()
	if err != nil {
		t.Fatalf("ListLibraries: %v", err)
	}
	if len(libs) != 1 || libs[0].Library != "demo.lib" {
		t.Fatalf("libs = %+v", libs)
	}
	if libs[0].Elements != 2 || libs[0].Profiles != 2 || libs[0].Findings != 1 {
		t.Errorf("library counts wrong: %+v", libs[0])
	}

	snap, err := c.LibrarySnapshot("demo.lib")
	if err != nil {
		t.Fatalf("LibrarySnapshot: %v", err)
	}
	if len(snap.Elements) != 2 || len(snap.Profiles) != 2 || len(snap.Findings) != 1 {
		t.Fatalf("snapshot counts wrong: %d/%d/%d", len(snap.Elements), len(snap.Profiles), len(snap.Findings))
	}

	r := BuildReport(snap, "fidl", "2026-05-23 12:00 UTC", "HEAD")
	if r.Total != 2 {
		t.Errorf("Total = %d; want 2", r.Total)
	}
	if r.Bridged != 1 {
		t.Errorf("Bridged = %d; want 1", r.Bridged)
	}
	// B has STALE_DOC → time drift expected on its method row.
	driftSeen := false
	for _, m := range r.Methods {
		if m.Name == "demo.lib/B.stale" && m.Drift == "time" {
			driftSeen = true
		}
	}
	if !driftSeen {
		t.Errorf("expected B.stale to have time drift; methods = %+v", r.Methods)
	}

	var buf bytes.Buffer
	if err := RenderHTML(&buf, r); err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	html := buf.String()
	// The "What to fix" worklist is merged into the UpSet rows; each
	// combo cell now carries its own Action verb. Anchor instead on
	// the merged Methods section header and the new u2r-action class.
	// Header redesign (default branch): the big "Fragmentation scan: <lib>"
	// H1 is removed in favor of the sticky report-strip; the library
	// identity now lives in the strip's .strip-lib chip.
	for _, want := range []string{
		`class="report-strip"`,
		`class="strip-lib">demo.lib`,
		"demo.lib",
		"sec-coverage",
		// "sec-anomalies" was removed when the Findings section was
		// folded into the worklist as part of the multi-ecosystem
		// redesign (commit 27f4738). Worklist + UpSet rows now carry
		// the same content; the dedicated anchor is gone.
		"sec-methods",
		"u2r-action",
		// Header labels still present after the Phase 1 view refactor.
		// These come from the fidlView's Tiers() (Protocols + Methods)
		// via the polymorphic .HeaderTiers slice the template ranges
		// over for the section header.
		"Protocols",
		"Methods",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}
