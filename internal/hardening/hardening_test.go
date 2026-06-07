package hardening

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/autodetect"
	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

func det(source string) *commonpb.RowProvenance {
	return &commonpb.RowProvenance{Tier: commonpb.RowProvenance_DETERMINISTIC, Source: source}
}
func llm() *commonpb.RowProvenance {
	return &commonpb.RowProvenance{Tier: commonpb.RowProvenance_LLM, Source: "llm"}
}

// TestGenerate_RealPwLogEntries is the proof the design demands: the two
// confirmed pw_log hardening items must FALL OUT of the scan signals,
// not be hand-written. (1) a #define alias graph (DBG -> PW_LOG_DEBUG),
// (2) cppheader missing an `inline constexpr` form the LLM caught.
func TestGenerate_RealPwLogEntries(t *testing.T) {
	root := t.TempDir()
	// A header with a macro-alias chain and an inline-constexpr constant.
	hdr := "lib/log.h"
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := strings.Join([]string{
		"#define PW_LOG_DEBUG(...) PW_HANDLE_LOG(__VA_ARGS__)",
		"#define PW_HANDLE_LOG(...) impl(__VA_ARGS__)",
		"#define DBG(...) PW_LOG_DEBUG(__VA_ARGS__)", // alias edge: DBG -> PW_LOG_DEBUG
		"inline constexpr int kDefaultToken = 0;",    // line 4
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, hdr), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	c := corpus.New()
	// Deterministic cppheader-style elements (the #defines it does emit).
	c.AddElement(&contractpb.ContractElement{
		Id: "lib/PW_LOG_DEBUG", Library: "lib", Provenance: det("cppheader"),
		Location: &commonpb.SourceLocation{Path: hdr, Line: 1},
	})
	c.AddElement(&contractpb.ContractElement{
		Id: "lib/DBG", Library: "lib", Provenance: det("cppheader"),
		Location: &commonpb.SourceLocation{Path: hdr, Line: 3},
	})
	// An LLM-only element on the inline-constexpr form cppheader misses.
	c.AddElement(&contractpb.ContractElement{
		Id: "lib/kDefaultToken", Library: "lib", Provenance: llm(),
		Location: &commonpb.SourceLocation{Path: hdr, Line: 4},
	})

	md := Generate(Input{
		RepoRoot:  root,
		ProjectID: "demo",
		Detection: &autodetect.Result{SchemalessHeaders: true},
		Corpus:    c,
	})

	if !strings.Contains(md, "#define alias graph") {
		t.Errorf("missing #define-graph hardening entry:\n%s", md)
	}
	if !strings.Contains(md, "DBG → PW_LOG_DEBUG") {
		t.Errorf("expected DBG -> PW_LOG_DEBUG alias edge:\n%s", md)
	}
	if !strings.Contains(md, "cppheader misses") {
		t.Errorf("missing cppheader-extension hardening entry:\n%s", md)
	}
	if !strings.Contains(md, "inline-constexpr") {
		t.Errorf("expected inline-constexpr form classification:\n%s", md)
	}
	// Tier accounting must report 2 deterministic, 1 llm.
	if !strings.Contains(md, "Deterministic elements: **2**") {
		t.Errorf("expected 2 deterministic elements:\n%s", md)
	}
	if !strings.Contains(md, "LLM-tier elements: **1**") {
		t.Errorf("expected 1 LLM element:\n%s", md)
	}
}

// TestGenerate_SchemaEntry confirms a detected schema ecosystem yields a
// rung-1 "you have a schema" entry.
func TestGenerate_SchemaEntry(t *testing.T) {
	md := Generate(Input{
		ProjectID: "demo",
		Detection: &autodetect.Result{Detections: []autodetect.Detection{
			{Adapter: "proto", Role: autodetect.RoleContract, Tier: autodetect.TierDeterministic, FileCount: 4},
		}},
		Corpus: corpus.New(),
	})
	if !strings.Contains(md, "You have a schema (proto)") {
		t.Errorf("expected proto schema hardening entry:\n%s", md)
	}
}

// TestGenerate_NoItems confirms a fully-deterministic corpus reports no
// backlog rather than inventing items.
func TestGenerate_NoItems(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{Id: "lib/A", Library: "lib", Provenance: det("proto")})
	md := Generate(Input{ProjectID: "demo", Detection: &autodetect.Result{}, Corpus: c})
	if !strings.Contains(md, "No hardening items detected") {
		t.Errorf("expected empty backlog message:\n%s", md)
	}
}
