package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for `sheaf init`.

func TestRunInit_MinimalTemplate(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runInit(&out, &errOut, dir, "minimal")
	if rc != 0 {
		t.Fatalf("runInit returned %d; stderr=%s", rc, errOut.String())
	}
	for _, name := range []string{"sheaf.textproto", "categorization-rules.textproto"} {
		full := filepath.Join(dir, name)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("init didn't write %s: %v", full, err)
		}
	}
	if !strings.Contains(out.String(), "Bootstrapped Sheaf config") {
		t.Errorf("expected bootstrap line; got %s", out.String())
	}
}

func TestRunInit_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sheaf.textproto"), []byte("preexisting"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var out, errOut bytes.Buffer
	rc := runInit(&out, &errOut, dir, "minimal")
	if rc == 0 {
		t.Errorf("expected non-zero exit when sheaf.textproto exists; got 0, stderr=%s", errOut.String())
	}
}

func TestRunInit_UnknownTemplate(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runInit(&out, &errOut, dir, "definitely-not-a-template")
	if rc == 0 {
		t.Errorf("expected non-zero exit on unknown template; got 0")
	}
	if !strings.Contains(errOut.String(), "unknown template") {
		t.Errorf("expected unknown-template error; got %s", errOut.String())
	}
}

func TestRunInit_RepoFlag(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runInit(&out, &errOut, dir, "minimal")
	if rc != 0 {
		t.Fatalf("init --repo returned %d", rc)
	}
	for _, name := range []string{"sheaf.textproto", "categorization-rules.textproto"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("init didn't write %s", name)
		}
	}
}

func TestRunInit_ArghCLITemplate(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	rc := runInit(&out, &errOut, dir, "argh-cli")
	if rc != 0 {
		t.Fatalf("runInit(argh-cli) returned %d; stderr=%s", rc, errOut.String())
	}
	body, err := os.ReadFile(filepath.Join(dir, "sheaf.textproto"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "argh") {
		t.Errorf("argh-cli template body missing 'argh' marker:\n%s", body)
	}
}

// When a docs/ tree is present, init auto-adds a markdown doc_parser to the
// minimal template (which scaffolds none) so the concept-doc surface isn't a
// false "silent" on first scan.
func TestRunInit_AutoDetectsDocsTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("# Guide\n"), 0o644); err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	var out, errOut bytes.Buffer
	if rc := runInit(&out, &errOut, dir, "minimal"); rc != 0 {
		t.Fatalf("runInit returned %d; stderr=%s", rc, errOut.String())
	}
	body, err := os.ReadFile(filepath.Join(dir, "sheaf.textproto"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "doc_parser") || !strings.Contains(string(body), "docs/**/*.md") {
		t.Errorf("expected an auto-added markdown doc_parser; got:\n%s", body)
	}
	if !strings.Contains(out.String(), "Detected a docs/ tree") {
		t.Errorf("expected docs-detection note on stdout; got %s", out.String())
	}
}

// Without a docs/ tree, the minimal template is written unchanged (no
// doc_parser) — auto-detection must not invent a doc corpus that isn't there.
func TestRunInit_NoDocsTree_NoDocParser(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if rc := runInit(&out, &errOut, dir, "minimal"); rc != 0 {
		t.Fatalf("runInit returned %d; stderr=%s", rc, errOut.String())
	}
	body, err := os.ReadFile(filepath.Join(dir, "sheaf.textproto"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), "doc_parser") {
		t.Errorf("did not expect a doc_parser without a docs/ tree; got:\n%s", body)
	}
}

func TestInit_ArgvFlagParsing(t *testing.T) {
	// Drive the public argv entry so --repo and --template are parsed
	// from the command line. The runInit tests pass the template
	// positionally, which never attributes the --template flag.
	dir := t.TempDir()
	if rc := Init([]string{"--repo", dir, "--template", "minimal"}); rc != 0 {
		t.Fatalf("Init(--repo,--template) returned %d; want 0", rc)
	}
	if _, err := os.Stat(filepath.Join(dir, "sheaf.textproto")); err != nil {
		t.Errorf("init via argv didn't write sheaf.textproto: %v", err)
	}
}
