package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A minimal cobra YAML for one tool. Produces one SUBCOMMAND plus one
// FLAG element, library == binary name. Hermetic — no external tools.
const toolYAML = `command: %[1]s greet
short: Print a greeting
options:
  - option: name
    value_type: string
    description: Who to greet
`

const toolConfig = `version: 1
project { name: "%[1]s" }
scope { library: "%[1]s" }
contract_anchor {
  name: "cobra"
  cobra {
    yaml_dir: "%[2]s"
    binary_name: "%[1]s"
    include: "**/*.yaml"
  }
}
`

const toolRules = `version: 1
category {
  dotted_path: "cli.commands"
  paths: "**/*.yaml"
}
`

// fanoutFixture builds a synthetic repo with two cobra-backed tools and
// a manifest referencing both. Returns (manifestPath, repoRoot). The
// configs live under manifestDir/<tool>/ so config_path resolution is
// exercised relative to the manifest; the cobra yaml_dir is relative to
// repoRoot. cfgA carries a sibling categorization-rules.textproto to
// exercise the rules-staging path.
func fanoutFixture(t *testing.T) (manifestPath, repoRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	manifestDir := t.TempDir()

	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	// Tool A — cobra yaml under repoRoot/cmds-a, config + sibling rules
	// under manifestDir/cfgA.
	write(filepath.Join(repoRoot, "cmds-a", "toola.yaml"), fmt.Sprintf(toolYAML, "toola"))
	write(filepath.Join(manifestDir, "cfgA", "sheaf.textproto"), fmt.Sprintf(toolConfig, "toola", "cmds-a"))
	write(filepath.Join(manifestDir, "cfgA", "categorization-rules.textproto"), toolRules)

	// Tool B — no sibling rules (exercises the uncategorized path).
	write(filepath.Join(repoRoot, "cmds-b", "toolb.yaml"), fmt.Sprintf(toolYAML, "toolb"))
	write(filepath.Join(manifestDir, "cfgB", "sheaf.textproto"), fmt.Sprintf(toolConfig, "toolb", "cmds-b"))

	manifestPath = filepath.Join(manifestDir, "manifest.textproto")
	write(manifestPath, `
entries { config_path: "cfgA/sheaf.textproto" library: "toola" ecosystem: "cli" library_label: "Tool A CLI" output: "toola.html" }
entries { config_path: "cfgB/sheaf.textproto" library: "toolb" ecosystem: "cli" library_label: "Tool B CLI" output: "toolb.html" }
`)
	return manifestPath, repoRoot
}

// splitOut maps a fan-out --output-dir to the split layout it now produces:
// the index file <root>.html and the <root>-contents/ dir, both beside outDir.
func splitOut(outDir string) (indexPath, contentsDir string) {
	root := filepath.Base(outDir)
	parent := filepath.Dir(outDir)
	return filepath.Join(parent, root+".html"), filepath.Join(parent, root+"-contents")
}

func TestRunManifest_Basic(t *testing.T) {
	manifestPath, repoRoot := fanoutFixture(t)
	outDir := t.TempDir()
	var log bytes.Buffer
	err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, "", "", false, false, 1, "", false, &log)
	if err != nil {
		t.Fatalf("RunManifest: %v\nlog:\n%s", err, log.String())
	}
	indexPath, contentsDir := splitOut(outDir)
	for _, name := range []string{"toola.html", "toolb.html"} {
		if _, err := os.Stat(filepath.Join(contentsDir, name)); err != nil {
			t.Errorf("expected %s in contents dir: %v", name, err)
		}
	}
	if _, err := os.Stat(indexPath); err != nil {
		t.Errorf("expected index %s: %v", indexPath, err)
	}
	index, _ := os.ReadFile(indexPath)
	// The redesigned index lists each report by library name (linked to
	// its report) and buckets ungrouped entries under "Ungrouped".
	for _, want := range []string{"toola.html", "toolb.html", ">toola</a>", ">toolb</a>", "Ungrouped"} {
		if !strings.Contains(string(index), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	// The fan-out no longer stages rules into repoRoot — each entry's
	// sibling rules are threaded straight into the render call — so the
	// repo root must stay clean (no categorization-rules.textproto written).
	if _, err := os.Stat(filepath.Join(repoRoot, "categorization-rules.textproto")); err == nil {
		t.Errorf("fan-out wrote categorization-rules.textproto into repoRoot; it must never touch the repo root")
	}
}

func TestRunManifest_OneFails(t *testing.T) {
	manifestPath, repoRoot := fanoutFixture(t)
	// Corrupt entry B's config_path so it fails; A should still render.
	bad := `
entries { config_path: "cfgA/sheaf.textproto" library: "toola" ecosystem: "cli" library_label: "Tool A CLI" output: "toola.html" }
entries { config_path: "does-not-exist/sheaf.textproto" library: "toolb" ecosystem: "cli" library_label: "Tool B CLI" output: "toolb.html" }
`
	if err := os.WriteFile(manifestPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	var log bytes.Buffer
	// Default (continue-on-failure): RunManifest returns nil.
	if err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, "", "", false, false, 1, "", false, &log); err != nil {
		t.Fatalf("RunManifest should not error in continue-on-failure mode: %v", err)
	}
	indexPath, contentsDir := splitOut(outDir)
	if _, err := os.Stat(filepath.Join(contentsDir, "toola.html")); err != nil {
		t.Errorf("toola.html should still render despite B failing: %v", err)
	}
	index, _ := os.ReadFile(indexPath)
	if !strings.Contains(string(index), "failed") {
		t.Errorf("index.html should mark the failed entry inline; got:\n%s", index)
	}
	// With --fail-on-error, the same manifest yields a non-nil error.
	var log2 bytes.Buffer
	if err := RunManifest(context.Background(), manifestPath, t.TempDir(), repoRoot, "", "", false, true, 1, "", false, &log2); err == nil {
		t.Errorf("expected non-nil error with failOnError=true and a failing entry")
	}
}

func TestRunManifest_BadTextproto(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.textproto")
	if err := os.WriteFile(manifestPath, []byte("this is { not : valid textproto ]["), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	err := RunManifest(context.Background(), manifestPath, t.TempDir(), ".", "", "", false, false, 1, "", false, &log)
	if err == nil {
		t.Fatalf("expected a parse error for a malformed manifest")
	}
	if !strings.Contains(err.Error(), "parse manifest") {
		t.Errorf("error should name the parse failure; got %v", err)
	}
	// No entry should have been attempted — no index written.
	if log.Len() != 0 {
		t.Errorf("expected no per-entry log output on parse failure; got %q", log.String())
	}
}

// configRootFixture builds a repo whose sheaf config lives at
// repoRoot/cfg/sheaf.textproto (cobra yaml under repoRoot/cmds), with a
// manifest in a SEPARATE directory that references the config by a
// REPO-RELATIVE path ("cfg/sheaf.textproto"). Resolving that relative to
// the manifest dir would fail; resolving it against --config-root=repoRoot
// succeeds. Returns (manifestPath, repoRoot).
func configRootFixture(t *testing.T, configPath string) (manifestPath, repoRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	manifestDir := t.TempDir()
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(repoRoot, "cmds", "tool.yaml"), fmt.Sprintf(toolYAML, "tool"))
	write(filepath.Join(repoRoot, "cfg", "sheaf.textproto"), fmt.Sprintf(toolConfig, "tool", "cmds"))

	manifestPath = filepath.Join(manifestDir, "manifest.textproto")
	write(manifestPath, fmt.Sprintf(
		`entries { config_path: %q library: "tool" ecosystem: "cli" output: "tool.html" }`+"\n",
		configPath))
	return manifestPath, repoRoot
}

func TestRunManifest_ConfigRoot_ResolvesRepoRelative(t *testing.T) {
	manifestPath, repoRoot := configRootFixture(t, "cfg/sheaf.textproto")
	outDir := t.TempDir()
	var log bytes.Buffer
	// With --config-root pointing at the repo, the repo-relative
	// config_path resolves and the entry renders.
	if err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, repoRoot, "", false, true, 1, "", false, &log); err != nil {
		t.Fatalf("RunManifest with --config-root should succeed: %v\nlog:\n%s", err, log.String())
	}
	_, contentsDir := splitOut(outDir)
	if _, err := os.Stat(filepath.Join(contentsDir, "tool.html")); err != nil {
		t.Errorf("tool.html should render with --config-root: %v", err)
	}
}

func TestRunManifest_NoConfigRoot_FallsBackToManifestDir(t *testing.T) {
	manifestPath, repoRoot := configRootFixture(t, "cfg/sheaf.textproto")
	outDir := t.TempDir()
	var log bytes.Buffer
	// Without --config-root, the repo-relative path resolves against the
	// manifest dir (a different temp dir), where cfg/ does not exist, so
	// the entry fails — preserving back-compat behavior.
	if err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, "", "", false, true, 1, "", false, &log); err == nil {
		t.Errorf("expected failure resolving repo-relative config against manifest dir; log:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "FAILED") {
		t.Errorf("log should record the entry failure; got:\n%s", log.String())
	}
}

func TestRunManifest_AbsoluteConfigUnaffectedByConfigRoot(t *testing.T) {
	// Absolute config_path wins regardless of --config-root. Point
	// config-root at a bogus directory to prove it is ignored.
	manifestPath, repoRoot := configRootFixture(t, "PLACEHOLDER")
	absConfig := filepath.Join(repoRoot, "cfg", "sheaf.textproto")
	// Rewrite the manifest with the absolute path now that we know it.
	body := fmt.Sprintf(
		`entries { config_path: %q library: "tool" ecosystem: "cli" output: "tool.html" }`+"\n",
		absConfig)
	if err := os.WriteFile(manifestPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	var log bytes.Buffer
	if err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, filepath.Join(t.TempDir(), "bogus"), "", false, true, 1, "", false, &log); err != nil {
		t.Fatalf("absolute config_path should resolve despite a bogus --config-root: %v\nlog:\n%s", err, log.String())
	}
	_, contentsDir := splitOut(outDir)
	if _, err := os.Stat(filepath.Join(contentsDir, "tool.html")); err != nil {
		t.Errorf("tool.html should render from the absolute config_path: %v", err)
	}
}

func TestRunManifest_MissingOutputDir(t *testing.T) {
	manifestPath, repoRoot := fanoutFixture(t)
	// Point at a nested directory that does not yet exist.
	outDir := filepath.Join(t.TempDir(), "nested", "out")
	if _, err := os.Stat(outDir); err == nil {
		t.Fatal("precondition: outDir should not exist yet")
	}
	var log bytes.Buffer
	if err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, "", "", false, false, 1, "", false, &log); err != nil {
		t.Fatalf("RunManifest: %v", err)
	}
	indexPath, _ := splitOut(outDir)
	if _, err := os.Stat(indexPath); err != nil {
		t.Errorf("runner should have created the parent dir and written the index: %v", err)
	}
}
