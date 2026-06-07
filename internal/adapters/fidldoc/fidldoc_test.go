package fidldoc

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// buildFixtureZip writes a synthetic fidldoc-shaped zip and returns
// its on-disk path.
func buildFixtureZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fidldoc.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip Create: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip Write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	f.Close()
	return path
}

const sampleFuchsiaIO = "# fuchsia.io\n\n" +
	"## Directory {#Directory}\n\n" +
	"A directory of nodes. Directories hold child entries that can be looked up by name.\n\n" +
	"### Open {#Directory.Open}\n\n" +
	"Open (or create) a node relative to this directory. Any errors are communicated via an\n" +
	"epitaph sent on the `object` channel.\n\n" +
	"Errors:\n\n" +
	"```\n" +
	"ZX_ERR_BAD_PATH if path is invalid\n" +
	"```\n\n" +
	"| Param | Type |\n" +
	"|---|---|\n" +
	"| path | string |\n\n" +
	"### Close {#Directory.Close}\n\n" +
	"Closes.\n\n" +
	"### Query {#Directory.Query}\n\n" +
	"x\n"

func TestParse_HappyPath(t *testing.T) {
	path := buildFixtureZip(t, map[string]string{
		"fidldoc/fuchsia.io/README.md": sampleFuchsiaIO,
	})
	a := New(Config{BundlePath: path})
	claims, err := a.Parse(context.Background())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// One per anchor: Directory, Directory.Open, Directory.Close, Directory.Query
	if len(claims) != 4 {
		t.Errorf("got %d claims, want 4 (got %+v)", len(claims), claimIDs(claims))
	}
}

func TestParse_URLAndContractRefs(t *testing.T) {
	path := buildFixtureZip(t, map[string]string{
		"fuchsia.io/README.md": sampleFuchsiaIO,
	})
	a := New(Config{BundlePath: path})
	claims, _ := a.Parse(context.Background())
	for _, c := range claims {
		if len(c.GetContractRefs()) != 1 {
			t.Errorf("expected 1 ref per claim; got %v", c.GetContractRefs())
		}
		if !strings.HasPrefix(c.GetUrl(), "https://fuchsia.dev/reference/fidl/fuchsia.io#") {
			t.Errorf("URL = %q, want fuchsia.dev prefix", c.GetUrl())
		}
		if !strings.HasPrefix(c.GetContractRefs()[0], "fuchsia.io/") {
			t.Errorf("ContractRef = %q, want fuchsia.io/ prefix", c.GetContractRefs()[0])
		}
	}
}

func TestParse_SubstanceGrading(t *testing.T) {
	path := buildFixtureZip(t, map[string]string{
		"fuchsia.io/README.md": sampleFuchsiaIO,
	})
	a := New(Config{BundlePath: path})
	claims, _ := a.Parse(context.Background())
	gradeByAnchor := make(map[string]commonpb.Substance)
	wordsByAnchor := make(map[string]uint32)
	for _, c := range claims {
		// ContractRef looks like "fuchsia.io/Directory" or "fuchsia.io/Directory.Open"
		ref := c.GetContractRefs()[0]
		short := strings.TrimPrefix(ref, "fuchsia.io/")
		gradeByAnchor[short] = c.GetSubstance()
		wordsByAnchor[short] = c.GetWordCount()
	}
	// Directory.Open has long prose body → SUBSTANTIVE
	if gradeByAnchor["Directory.Open"] != commonpb.Substance_SUBSTANTIVE {
		t.Errorf("Directory.Open grade = %v (words=%d), want SUBSTANTIVE",
			gradeByAnchor["Directory.Open"], wordsByAnchor["Directory.Open"])
	}
	// Directory.Close has "Closes." → SIGNATURE_ONLY
	if gradeByAnchor["Directory.Close"] != commonpb.Substance_SIGNATURE_ONLY {
		t.Errorf("Directory.Close grade = %v (words=%d), want SIGNATURE_ONLY",
			gradeByAnchor["Directory.Close"], wordsByAnchor["Directory.Close"])
	}
	// Directory.Query has just "x" → SIGNATURE_ONLY
	if gradeByAnchor["Directory.Query"] != commonpb.Substance_SIGNATURE_ONLY {
		t.Errorf("Directory.Query grade = %v, want SIGNATURE_ONLY",
			gradeByAnchor["Directory.Query"])
	}
}

func TestParse_ExcludesCodeAndTablesFromSubstance(t *testing.T) {
	// All the words are inside a code block or a table; prose word
	// count should be effectively zero → SIGNATURE_ONLY or ABSENT.
	md := "## X {#X}\n\n```\nthese are not words\nthis is not prose\n```\n\n" +
		"| a | b |\n|---|---|\n| 1 | 2 |\n"
	path := buildFixtureZip(t, map[string]string{
		"fuchsia.io/README.md": "# Header\n" + md,
	})
	a := New(Config{BundlePath: path})
	claims, _ := a.Parse(context.Background())
	if len(claims) != 1 {
		t.Fatalf("claims = %d", len(claims))
	}
	if claims[0].GetSubstance() >= commonpb.Substance_PARTIAL {
		t.Errorf("got %v; code-only/table-only content should grade below PARTIAL",
			claims[0].GetSubstance())
	}
}

func TestParse_LibraryFilter(t *testing.T) {
	path := buildFixtureZip(t, map[string]string{
		"fidldoc/fuchsia.io/README.md":    "## X {#X}\nx\n",
		"fidldoc/fuchsia.other/README.md": "## Y {#Y}\ny\n",
	})
	a := New(Config{BundlePath: path, Library: "fuchsia.io"})
	claims, _ := a.Parse(context.Background())
	for _, c := range claims {
		if !strings.HasPrefix(c.GetContractRefs()[0], "fuchsia.io/") {
			t.Errorf("library filter failed; got %s", c.GetContractRefs()[0])
		}
	}
}

func TestParse_MissingBundle(t *testing.T) {
	a := New(Config{BundlePath: "/no/such/file.zip"})
	if _, err := a.Parse(context.Background()); err == nil {
		t.Errorf("expected error for missing bundle")
	}
}

func TestLibraryFromZipPath(t *testing.T) {
	cases := []struct {
		in, wantLib string
		wantOK      bool
	}{
		{"fidldoc/fuchsia.io/README.md", "fuchsia.io", true},
		{"fuchsia.io/README.md", "fuchsia.io", true},
		{"fidldoc/fuchsia.io/sub/README.md", "", false}, // nested
		{"index.md", "", false},
		{"fidldoc/README.md", "", false}, // top-level
	}
	for _, c := range cases {
		gotLib, gotOK := libraryFromZipPath(c.in)
		if gotLib != c.wantLib || gotOK != c.wantOK {
			t.Errorf("libraryFromZipPath(%q) = (%q, %v), want (%q, %v)",
				c.in, gotLib, gotOK, c.wantLib, c.wantOK)
		}
	}
}

func claimIDs(claims []*docclaimpb.DocClaim) []string {
	out := make([]string, len(claims))
	for i, c := range claims {
		if len(c.GetContractRefs()) > 0 {
			out[i] = c.GetContractRefs()[0]
		}
	}
	return out
}
