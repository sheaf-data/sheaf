package clidoc

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// buildFixtureTarball writes a synthetic clidoc tarball and returns its path.
func buildFixtureTarball(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "clidoc.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tw.Write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz.Close: %v", err)
	}
	f.Close()
	return path
}

const sampleFfx = "# ffx\n\n" +
	"## component {#ffx_component}\n\n" +
	"Manage Fuchsia components. Subcommands let you list, show, run, and start.\n\n" +
	"### show {#ffx_component_show}\n\n" +
	"Show component details including monikers, capabilities, and exposed services.\n\n" +
	"### list {#ffx_component_list}\n\n" +
	"List.\n\n" +
	"## doctor {#ffx_doctor}\n\n" +
	"Diagnose connectivity, environment, and tool versions.\n"

func TestParse_HappyPath(t *testing.T) {
	path := buildFixtureTarball(t, map[string]string{
		"clidoc/ffx.md": sampleFfx,
	})
	a := New(Config{BundlePath: path, SectionPath: "clidoc/ffx.md"})
	claims, err := a.Parse(context.Background())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 4 anchors: ffx_component, ffx_component_show, ffx_component_list, ffx_doctor
	if len(claims) != 4 {
		t.Errorf("got %d claims, want 4", len(claims))
	}
}

func TestParse_ContractRefsUseSpacePaths(t *testing.T) {
	path := buildFixtureTarball(t, map[string]string{
		"clidoc/ffx.md": sampleFfx,
	})
	a := New(Config{BundlePath: path, SectionPath: "clidoc/ffx.md"})
	claims, _ := a.Parse(context.Background())
	refs := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			refs[r] = true
		}
	}
	for _, want := range []string{
		"ffx component", "ffx component show", "ffx component list", "ffx doctor",
	} {
		if !refs[want] {
			t.Errorf("missing ref %q in %v", want, refs)
		}
	}
}

func TestParse_URLConstruction(t *testing.T) {
	path := buildFixtureTarball(t, map[string]string{
		"clidoc/ffx.md": sampleFfx,
	})
	a := New(Config{
		BundlePath:  path,
		SectionPath: "clidoc/ffx.md",
		URLBase:     "https://fuchsia.dev/reference/tools/sdk/",
	})
	claims, _ := a.Parse(context.Background())
	for _, c := range claims {
		if !strings.HasPrefix(c.GetUrl(), "https://fuchsia.dev/reference/tools/sdk/ffx#") {
			t.Errorf("URL = %q", c.GetUrl())
		}
	}
}

func TestParse_SubstanceGrading(t *testing.T) {
	path := buildFixtureTarball(t, map[string]string{
		"clidoc/ffx.md": sampleFfx,
	})
	a := New(Config{BundlePath: path, SectionPath: "clidoc/ffx.md"})
	claims, _ := a.Parse(context.Background())
	gradeByRef := make(map[string]commonpb.Substance)
	for _, c := range claims {
		gradeByRef[c.GetContractRefs()[0]] = c.GetSubstance()
	}
	// "show" has a longer description → should be SUBSTANTIVE
	if gradeByRef["ffx component show"] != commonpb.Substance_SUBSTANTIVE {
		t.Errorf("ffx component show grade = %v, want SUBSTANTIVE", gradeByRef["ffx component show"])
	}
	// "list" is one word → SIGNATURE_ONLY
	if gradeByRef["ffx component list"] != commonpb.Substance_SIGNATURE_ONLY {
		t.Errorf("ffx component list grade = %v, want SIGNATURE_ONLY", gradeByRef["ffx component list"])
	}
}

func TestParse_MissingTarball(t *testing.T) {
	a := New(Config{BundlePath: "/no/such/file.tar.gz", SectionPath: "x.md"})
	if _, err := a.Parse(context.Background()); err == nil {
		t.Errorf("expected error for missing tarball")
	}
}

func TestParse_MissingSection(t *testing.T) {
	path := buildFixtureTarball(t, map[string]string{
		"clidoc/ffx.md": sampleFfx,
	})
	a := New(Config{BundlePath: path, SectionPath: "clidoc/triage.md"})
	if _, err := a.Parse(context.Background()); err == nil {
		t.Errorf("expected error for missing section")
	}
}

func TestParse_DotSlashTarballEntries(t *testing.T) {
	// Some tar tools prefix paths with `./`; the reader should still find them.
	path := buildFixtureTarball(t, map[string]string{
		"./clidoc/ffx.md": sampleFfx,
	})
	a := New(Config{BundlePath: path, SectionPath: "clidoc/ffx.md"})
	claims, err := a.Parse(context.Background())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(claims) == 0 {
		t.Errorf("expected claims; got none")
	}
}

// sampleFfxFlags is a synthetic clidoc section with a command header
// followed by an Options list of three flags (matching the real ffx
// clidoc line shapes: `--name, -s <value>  desc`, `--name <value>  desc`,
// and a bare switch `--name  desc`).
const sampleFfxFlags = "# ffx\n\n" +
	"### gen {#ffx_audio_gen}\n\n" +
	"Generate an audio signal and write it to stdout.\n\n" +
	"--duration, -d <duration>  how long to generate the signal\n" +
	"--frequency <frequency>  the frequency of the generated signal in hz\n" +
	"--format  emit the raw sample format header\n"

func TestParse_PerFlagClaims(t *testing.T) {
	path := buildFixtureTarball(t, map[string]string{
		"clidoc/ffx.md": sampleFfxFlags,
	})
	a := New(Config{BundlePath: path, SectionPath: "clidoc/ffx.md"})
	claims, err := a.Parse(context.Background())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := make(map[string]bool)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			refs[r] = true
		}
	}

	// The existing command-level claim must still be present.
	if !refs["ffx audio gen"] {
		t.Errorf("missing command-level ref %q in %v", "ffx audio gen", refs)
	}
	// One additive REFERENCE claim per flag, ref = `<command> --<flag>`.
	for _, want := range []string{
		"ffx audio gen --duration",
		"ffx audio gen --frequency",
		"ffx audio gen --format",
	} {
		if !refs[want] {
			t.Errorf("missing per-flag ref %q in %v", want, refs)
		}
	}

	// All per-flag claims are REFERENCE kind, and the shorthand/placeholder
	// noise is stripped out of the body.
	bodyByRef := make(map[string]string)
	for _, c := range claims {
		for _, r := range c.GetContractRefs() {
			if strings.Contains(r, " --") {
				if c.GetKind() != docclaimpb.DocClaimKind_REFERENCE {
					t.Errorf("flag claim %q kind = %v, want REFERENCE", r, c.GetKind())
				}
				bodyByRef[r] = c.GetRawText()
			}
		}
	}
	if got := bodyByRef["ffx audio gen --duration"]; got != "how long to generate the signal" {
		t.Errorf("duration body = %q, want %q", got, "how long to generate the signal")
	}
	if got := bodyByRef["ffx audio gen --frequency"]; got != "the frequency of the generated signal in hz" {
		t.Errorf("frequency body = %q, want %q", got, "the frequency of the generated signal in hz")
	}
	if got := bodyByRef["ffx audio gen --format"]; got != "emit the raw sample format header" {
		t.Errorf("format body = %q, want %q", got, "emit the raw sample format header")
	}
}

func TestBinaryNameFromSectionPath(t *testing.T) {
	cases := map[string]string{
		"clidoc/ffx.md":    "ffx",
		"clidoc/triage.md": "triage",
		"ffx.md":           "ffx",
	}
	for in, want := range cases {
		got := binaryNameFromSectionPath(in)
		if got != want {
			t.Errorf("binaryNameFromSectionPath(%q) = %q, want %q", in, got, want)
		}
	}
}
