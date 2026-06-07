package html

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/corpus"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

func mkSampleCorpus() (*corpus.Corpus, []*findingpb.Finding) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Directory.Open", Kind: contractpb.ContractElementKind_METHOD,
		Library:           "fuchsia.io",
		Location:          &commonpb.SourceLocation{Path: "sdk/fidl/fuchsia.io/directory.fidl", Line: 395},
		DocCommentExcerpt: "Open (or create) a node relative to this directory.",
	})
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId: "fuchsia.io/Directory.Open",
		Docs: &coveragepb.DocCoverage{Reference: &coveragepb.DocCoverage_Reference{
			Fidldoc: []*commonpb.DocRef{{
				Path:      "sdk/fidl/fuchsia.io/directory.fidl",
				Line:      395,
				Url:       "https://fuchsia.dev/reference/fidl/fuchsia.io#Directory.Open",
				Substance: commonpb.Substance_SUBSTANTIVE,
				Words:     176,
				Adapter:   "fidl",
			}},
		}},
		Tests: &coveragepb.TestCoverage{
			Unit: []*commonpb.TestRef{{
				TestName:  "Service.OpenAsDirectoryShouldFail",
				Path:      "src/storage/lib/vfs/cpp/tests/service_tests.cc",
				Line:      113,
				Framework: "gtest",
			}},
		},
		GapsSummary: &coveragepb.GapsSummary{Missing: []string{"examples"}},
	})
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/File.Read", Kind: contractpb.ContractElementKind_METHOD,
		Library: "fuchsia.io",
	})
	c.SetProfile(&coveragepb.CoverageProfile{
		ElementId:   "fuchsia.io/File.Read",
		GapsSummary: &coveragepb.GapsSummary{Missing: []string{"docs.reference", "tests", "examples"}},
	})
	findings := []*findingpb.Finding{
		{
			Id:       "x:doc-no-test:fuchsia.io/File.Read",
			Kind:     findingpb.FindingKind_DOCUMENTED_UNTESTED,
			Subject:  "fuchsia.io/File.Read",
			Severity: commonpb.Severity_WARNING,
			Analyzer: "documented-untested",
			Message:  "element is documented but has no test references",
		},
		{
			Id:       "y:missing:fuchsia.io/Directory.Open:examples",
			Kind:     findingpb.FindingKind_MISSING_IN_CATEGORY,
			Subject:  "fuchsia.io/Directory.Open",
			Severity: commonpb.Severity_INFO,
			Analyzer: "missing-in-category",
			Message:  "no references in category \"examples.in_tree\"",
		},
	}
	return c, findings
}

func TestWriter_ProducesExpectedFiles(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Project: "demo", OutDir: dir}
	c, fs := mkSampleCorpus()
	pages, err := w.Write(c, fs)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// index + findings + 2 elements = 4 pages
	if pages != 4 {
		t.Errorf("pages = %d, want 4", pages)
	}
	for _, want := range []string{
		filepath.Join(dir, "index.html"),
		filepath.Join(dir, "findings.html"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("missing file %s: %v", want, err)
		}
	}
	// Spot-check that some element page exists.
	matches, _ := filepath.Glob(filepath.Join(dir, "elements", "*.html"))
	if len(matches) != 2 {
		t.Errorf("element pages = %d, want 2", len(matches))
	}
}

func TestWriter_IndexHTMLContent(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Project: "demo", OutDir: dir}
	c, fs := mkSampleCorpus()
	if _, err := w.Write(c, fs); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "index.html"))
	s := string(body)
	for _, want := range []string{
		"demo",
		"Elements",
		"fuchsia.io/Directory.Open",
		"fuchsia.io/File.Read",
		"METHOD",
		"<table",
		`class="sortable"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

func TestWriter_FindingsHTMLContent(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Project: "demo", OutDir: dir}
	c, fs := mkSampleCorpus()
	if _, err := w.Write(c, fs); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "findings.html"))
	s := string(body)
	for _, want := range []string{
		"DOCUMENTED_UNTESTED",
		"MISSING_IN_CATEGORY",
		"fuchsia.io/File.Read",
		"WARNING",
		"INFO",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("findings.html missing %q", want)
		}
	}
}

func TestWriter_ElementHTMLContent(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Project: "demo", OutDir: dir}
	c, fs := mkSampleCorpus()
	if _, err := w.Write(c, fs); err != nil {
		t.Fatalf("Write: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "elements", "*.html"))
	for _, m := range matches {
		body, _ := os.ReadFile(m)
		if strings.Contains(string(body), "Directory.Open") {
			// Spot-check the rich element page.
			s := string(body)
			for _, want := range []string{
				"Open (or create) a node",
				"SUBSTANTIVE",
				"176 words",
				"Service.OpenAsDirectoryShouldFail",
				"fuchsia.dev/reference/fidl/fuchsia.io#Directory.Open",
				"Missing",
				"examples",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("element page missing %q", want)
				}
			}
			return
		}
	}
	t.Errorf("no element page contained Directory.Open content; files = %v", matches)
}

func TestWriter_EmptyCorpusStillProducesPages(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{Project: "empty", OutDir: dir}
	c := corpus.New()
	pages, err := w.Write(c, nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if pages < 2 {
		t.Errorf("pages = %d, want at least index + findings", pages)
	}
}

func TestWriter_RejectsEmptyOutDir(t *testing.T) {
	w := &Writer{Project: "x"}
	c, _ := mkSampleCorpus()
	_, err := w.Write(c, nil)
	if err == nil {
		t.Errorf("expected error for empty OutDir")
	}
}

func TestCoverPill(t *testing.T) {
	if !strings.Contains(string(coverPill(0)), "pill-red") {
		t.Errorf("0 should be red")
	}
	if !strings.Contains(string(coverPill(1)), "pill-yellow") {
		t.Errorf("1 should be yellow")
	}
	if !strings.Contains(string(coverPill(50)), "pill-green") {
		t.Errorf("50 should be green")
	}
}

func TestSeverityPill(t *testing.T) {
	if !strings.Contains(string(severityPill("ERROR")), "pill-red") {
		t.Errorf("ERROR not red")
	}
	if !strings.Contains(string(severityPill("WARNING")), "pill-yellow") {
		t.Errorf("WARNING not yellow")
	}
}
