package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	configpb "github.com/sheaf-data/sheaf/proto/config"
	"github.com/sheaf-data/sheaf/utils/scanner"
)

func TestRenderIndex(t *testing.T) {
	mk := func(lib, group, out string, st scanner.LibraryStats) EntryResult {
		return EntryResult{
			Entry:        &configpb.MonorepoManifest_Entry{Library: lib, Group: group, Output: out},
			OutputRel:    out,
			Group:        group,
			ElementCount: st.Total,
			BridgedCount: st.Bridged,
			Stats:        st,
		}
	}
	// Usage = max(Examples, Workflows) — synthetic lower bound (the test
	// data doesn't carry per-element overlap; max is correct when
	// workflows-set ⊇ examples-set, which is the common real shape).
	results := []EntryResult{
		mk("fuchsia.io", "Storage & IO", "fuchsia.io.html",
			scanner.LibraryStats{Total: 100, Docs: 70, Tests: 40, Examples: 40, Workflows: 20, Usage: 40, Completeness: [4]int{10, 30, 40, 20}}),
		mk("fuchsia.fs", "Storage & IO", "fuchsia.fs.html",
			scanner.LibraryStats{Total: 50, Docs: 40, Tests: 30, Examples: 20, Workflows: 5, Usage: 20, Completeness: [4]int{5, 15, 20, 10}}),
		mk("zx", "Kernel & Zircon", "zx.html",
			scanner.LibraryStats{Total: 80, Docs: 60, Tests: 70, Examples: 10, Workflows: 30, Usage: 30, Completeness: [4]int{20, 30, 20, 10}}),
		{
			Entry: &configpb.MonorepoManifest_Entry{Library: "broken", Group: "Kernel & Zircon"},
			Group: "Kernel & Zircon",
			Err:   fmt.Errorf("boom"),
		},
	}

	dir := t.TempDir()
	meta := RunMeta{Repo: "testrepo", Commit: "abc1234", SheafVersion: "test", GeneratedAt: time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)}
	if err := RenderIndex(filepath.Join(dir, "index.html"), results, meta); err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)

	// ZgotmplZ is html/template's marker for a value it refused to place
	// in a CSS/URL context — its presence means a bullet/strip width
	// didn't render. Must not appear.
	if strings.Contains(html, "ZgotmplZ") {
		t.Fatalf("template emitted ZgotmplZ — a dynamic CSS value was rejected")
	}
	wants := []string{
		"Storage &amp; IO", // domain name, html-escaped &
		"Kernel &amp; Zircon",
		`href="fuchsia.io.html"`,
		"broken — failed",  // failed entry rendered inline
		"abc1234",          // commit provenance
		"blt-fill fill-d",  // a docs bullet rendered
		"reports rendered", // moved to the masthead meta line
	}
	for _, w := range wants {
		if !strings.Contains(html, w) {
			t.Errorf("index.html missing %q", w)
		}
	}
}

// TestGenerateIndexPreview writes a full 8-domain index from the
// approved-mockup dataset to SHEAF_INDEX_PREVIEW_DIR (skipped unless
// set), so the renderer's output can be eyeballed against the
// approved index mockup.
func TestGenerateIndexPreview(t *testing.T) {
	outDir := os.Getenv("SHEAF_INDEX_PREVIEW_DIR")
	if outDir == "" {
		t.Skip("set SHEAF_INDEX_PREVIEW_DIR to generate a preview")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// domain, [Total, Docs, Tests, Examples, Workflows], Completeness[0..3]=orphan,1/3,2/3,3/3
	type d struct {
		group string
		lib   string
		st    scanner.LibraryStats
	}
	pc := func(total, doc, test, ex, wf int, comp [4]int) scanner.LibraryStats {
		// Usage is the union of examples + workflows. Synthetic test data
		// doesn't carry the per-element overlap, so we approximate with the
		// max of the two — the lower bound, valid when workflows are a
		// superset of examples (a common real-world pattern).
		usage := ex
		if wf > usage {
			usage = wf
		}
		return scanner.LibraryStats{Total: total, Docs: doc, Tests: test, Examples: ex, Workflows: wf, Usage: usage, Completeness: comp, Bridged: comp[3]}
	}
	rows := []d{
		{"Kernel & Zircon", "zx,fuchsia.kernel,fuchsia.boot,fuchsia.process,fuchsia.scheduler,fuchsia.ldsvc,fuchsia.sysinfo", pc(196, 137, 161, 43, 69, [4]int{12, 68, 78, 38})},
		{"Storage & IO", "fuchsia.io,fuchsia.fs,fuchsia.fshost,fuchsia.pkg,fuchsia.paver,fuchsia.storage.blobfs,fuchsia.storage.partitions,fuchsia.update", pc(188, 135, 79, 75, 38, [4]int{10, 44, 70, 64})},
		{"Component Framework", "fuchsia.component,fuchsia.component.decl,fuchsia.component.runner,fuchsia.component.sandbox,fuchsia.sys2,fuchsia.data", pc(164, 90, 79, 49, 41, [4]int{16, 50, 58, 40})},
		{"Drivers", "fuchsia.hardware.network,fuchsia.hardware.block.driver,fuchsia.hardware.usb,fuchsia.hardware.gpio,fuchsia.hardware.i2c,fuchsia.hardware.pci,fuchsia.driver.framework", pc(210, 80, 63, 38, 25, [4]int{38, 92, 52, 28})},
		{"UI & Graphics", "fuchsia.ui.composition,fuchsia.ui.views,fuchsia.ui.input,fuchsia.ui.scenic,fuchsia.images2,fuchsia.sysmem", pc(172, 112, 86, 100, 48, [4]int{8, 30, 56, 78})},
		{"Networking", "fuchsia.net,fuchsia.net.interfaces,fuchsia.net.routes,fuchsia.net.dhcp,fuchsia.net.name,fuchsia.posix.socket", pc(156, 94, 86, 55, 47, [4]int{10, 38, 58, 50})},
		{"Media", "fuchsia.media,fuchsia.media.audio,fuchsia.audio,fuchsia.camera3,fuchsia.mediacodec", pc(118, 57, 45, 30, 18, [4]int{18, 40, 36, 24})},
		{"Diagnostics", "fuchsia.diagnostics,fuchsia.logger,fuchsia.inspect,fuchsia.tracing,fuchsia.feedback", pc(80, 68, 48, 40, 32, [4]int{2, 12, 26, 40})},
	}
	var results []EntryResult
	for _, r := range rows {
		out := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(r.group), " & ", "-"), " ", "-") + ".html"
		results = append(results, EntryResult{
			Entry:     &configpb.MonorepoManifest_Entry{Library: r.lib, Group: r.group, Output: out},
			OutputRel: out, Group: r.group, ElementCount: r.st.Total, BridgedCount: r.st.Bridged, Stats: r.st,
		})
	}
	meta := RunMeta{Repo: "fuchsia.git", Commit: "9f3c2a1", SheafVersion: "dev", GeneratedAt: time.Date(2026, 5, 29, 14, 2, 0, 0, time.UTC)}
	if err := RenderIndex(filepath.Join(outDir, "index.html"), results, meta); err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	t.Logf("wrote %s/index.html", outDir)
}
