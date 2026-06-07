package gotest

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

func setupRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return dir
}

func TestDiscover_TopLevelTestFunc(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"cli/command/container/run_test.go": `
package container

import "testing"

func TestRunValidateFlags(t *testing.T) {
	_ = t
}
`,
	})
	p := New(Config{})
	tcs, err := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tcs) != 1 {
		t.Fatalf("got %d test cases, want 1", len(tcs))
	}
	tc := tcs[0]
	if tc.GetName() != "TestRunValidateFlags" {
		t.Errorf("name = %q", tc.GetName())
	}
	if tc.GetFramework() != "gotest" {
		t.Errorf("framework = %q", tc.GetFramework())
	}
}

func TestDiscover_TokenizeStripsPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"TestContainerRun", []string{"container", "run"}},
		{"TestRunValidateFlags", []string{"run", "validate", "flags"}},
		{"BenchmarkRunWithVolume", []string{"run", "with", "volume"}},
		{"ExampleClient_Do", []string{"client", "do"}},
		{"Test_run_with_volume", []string{"run", "with", "volume"}},
	}
	for _, c := range cases {
		got := tokenizeGoTestName(c.in)
		if !sliceEq(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDiscover_Subtests(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"cli/command/container/run_test.go": "package container\n" +
			"import \"testing\"\n" +
			"func TestRun(t *testing.T) {\n" +
			"  t.Run(\"with rm\", func(t *testing.T) {})\n" +
			"  t.Run(\"without rm\", func(t *testing.T) {})\n" +
			"  t.Run(\"container start\", func(t *testing.T) {})\n" +
			"}\n",
	})
	p := New(Config{})
	tcs, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	names := make(map[string]bool)
	for _, tc := range tcs {
		names[tc.GetName()] = true
	}
	for _, want := range []string{"TestRun", "with rm", "without rm", "container start"} {
		if !names[want] {
			t.Errorf("missing test %q; got %v", want, mapKeys(names))
		}
	}
}

func TestDiscover_BackticksInTRun(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"x_test.go": "package x\n" +
			"import \"testing\"\n" +
			"func TestX(t *testing.T) {\n" +
			"  t.Run(`with backticks`, func(t *testing.T) {})\n" +
			"}\n",
	})
	p := New(Config{})
	tcs, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	found := false
	for _, tc := range tcs {
		if tc.GetName() == "with backticks" {
			found = true
		}
	}
	if !found {
		t.Errorf("backtick t.Run not picked up")
	}
}

func TestDiscover_VendorExcludedByDefault(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"vendor/foo/bar_test.go": "package bar\nimport \"testing\"\nfunc TestSkipMe(t *testing.T){}\n",
		"foo_test.go":            "package foo\nimport \"testing\"\nfunc TestKeepMe(t *testing.T){}\n",
	})
	p := New(Config{})
	tcs, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	names := map[string]bool{}
	for _, tc := range tcs {
		names[tc.GetName()] = true
	}
	if !names["TestKeepMe"] || names["TestSkipMe"] {
		t.Errorf("expected only TestKeepMe; got %v", mapKeys(names))
	}
}

func TestDiscover_LocationCarriesFilePath(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"cli/command/container/run_test.go": "package container\nimport \"testing\"\nfunc TestRun(t *testing.T){}\n",
	})
	p := New(Config{})
	tcs, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if len(tcs) == 0 {
		t.Fatal("no tests")
	}
	if tcs[0].GetLocation().GetPath() != "cli/command/container/run_test.go" {
		t.Errorf("location.path = %q", tcs[0].GetLocation().GetPath())
	}
	if tcs[0].GetLocation().GetLine() == 0 {
		t.Errorf("location.line = 0")
	}
}

func TestDiscover_BenchAndExampleAndFuzz(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"x_test.go": "package x\n" +
			"import \"testing\"\n" +
			"func BenchmarkX(b *testing.B){}\n" +
			"func FuzzX(f *testing.F){}\n" +
			"func ExampleX(){}\n",
	})
	p := New(Config{})
	tcs, _ := p.Discover(context.Background(), repo, adapters.ScopeConfig{})
	names := map[string]bool{}
	for _, tc := range tcs {
		names[tc.GetName()] = true
	}
	for _, want := range []string{"BenchmarkX", "FuzzX", "ExampleX"} {
		if !names[want] {
			t.Errorf("missing %s; got %v", want, mapKeys(names))
		}
	}
}

// ---- Cobra invocation extractor (Fix A) ----

func TestExtract_ExecCommandSubprocess(t *testing.T) {
	body := []byte(`package x
import ("os/exec"; "testing")
func TestRunInit(t *testing.T) {
	cmd := exec.Command("docker", "run", "-i", "-t", "--init", "--name", "x", "image", "sleep", "30")
	_ = cmd
}
`)
	refs := extractCobraInvocations(body, "e2e/container/proxy_signal_test.go", "docker")
	wantContains(t, refs, "docker run --init")
	wantContains(t, refs, "docker run --name")
	// Short flags also extracted.
	wantContains(t, refs, "docker run -i")
	wantContains(t, refs, "docker run -t")
}

func TestExtract_IcmdCommandSubprocess(t *testing.T) {
	body := []byte(`package x
func TestX(t *testing.T) {
	icmd.Command("docker", "container", "ls", "--all", "--filter", "name=foo")
}
`)
	refs := extractCobraInvocations(body, "e2e/container/list_test.go", "docker")
	wantContains(t, refs, "docker container ls --all")
	wantContains(t, refs, "docker container ls --filter")
}

func TestExtract_StructLiteralArgs(t *testing.T) {
	body := []byte(`package container
func TestPullAllTags(t *testing.T) {
	testCases := []struct{ args []string }{
		{args: []string{"--all-tags", "image:tag"}},
		{args: []string{"--platform", "linux/amd64", "image"}},
	}
	_ = testCases
}
`)
	refs := extractCobraInvocations(body, "cli/command/image/pull_test.go", "docker")
	wantContains(t, refs, "docker image pull --all-tags")
	wantContains(t, refs, "docker image pull --platform")
	// Bare form also emitted for alias matching.
	wantContains(t, refs, "docker pull --all-tags")
}

func TestExtract_SetArgs(t *testing.T) {
	body := []byte(`package container
func TestRun(t *testing.T) {
	cmd.SetArgs([]string{"--detach", "image"})
}
`)
	refs := extractCobraInvocations(body, "cli/command/container/run_test.go", "docker")
	wantContains(t, refs, "docker container run --detach")
}

func TestExtract_UtilityFileSkipped(t *testing.T) {
	body := []byte(`package container
func TestParseRunFlags(t *testing.T) {
	cases := []struct{ flags []string }{
		{flags: []string{"--mac-address", "..."}},
	}
	_ = cases
}
`)
	// opts_test.go is a utility file — no file-based attribution.
	refs := extractCobraInvocations(body, "cli/command/container/opts_test.go", "docker")
	for _, r := range refs {
		if strings.Contains(r, "opts") {
			t.Errorf("opts_test should not produce opts-prefixed refs: %q", r)
		}
	}
}

func TestExtract_NonDockerSubprocessIgnored(t *testing.T) {
	body := []byte(`package x
func TestX(t *testing.T) {
	exec.Command("git", "status")
	exec.Command("kubectl", "get", "pods")
}
`)
	refs := extractCobraInvocations(body, "e2e/x_test.go", "docker")
	for _, r := range refs {
		if strings.HasPrefix(r, "git ") || strings.HasPrefix(r, "kubectl ") {
			t.Errorf("non-docker subprocess produced ref: %q", r)
		}
	}
}

func TestExtract_FlagWithEquals(t *testing.T) {
	body := []byte(`package x
func TestX(t *testing.T) {
	exec.Command("docker", "run", "--platform=linux/amd64", "image")
}
`)
	refs := extractCobraInvocations(body, "e2e/x_test.go", "docker")
	wantContains(t, refs, "docker run --platform")
	// Should NOT include the value part.
	for _, r := range refs {
		if strings.Contains(r, "linux") {
			t.Errorf("flag=value form leaked value: %q", r)
		}
	}
}

func TestExtract_EmptyWhenBinaryNotConfigured(t *testing.T) {
	body := []byte(`package x
func TestX(t *testing.T) { exec.Command("docker", "run", "--rm") }
`)
	refs := extractCobraInvocations(body, "x_test.go", "")
	if len(refs) != 0 {
		t.Errorf("expected no refs when binary_name unset; got %v", refs)
	}
}

func TestCandidateCommandsFromPath(t *testing.T) {
	cases := []struct {
		file string
		want []string
	}{
		{"cli/command/container/run_test.go", []string{"docker container run", "docker run"}},
		{"cli/command/registry/login_test.go", []string{"docker registry login", "docker login"}},
		{"e2e/container/run_test.go", []string{"docker container run", "docker run"}},
		{"cli/command/container/opts_test.go", nil},
		{"cli/command/container/client_test.go", nil},
		// opts/ is in utilityFileNames as a family — only the bare
		// form is emitted; "docker opts parse" isn't a real command.
		{"opts/parse_test.go", []string{"docker parse"}},
	}
	for _, c := range cases {
		got := candidateCommandsFromPath(c.file, "docker")
		if !sliceEq(got, c.want) {
			t.Errorf("candidateCommandsFromPath(%q) = %v, want %v", c.file, got, c.want)
		}
	}
}

func wantContains(t *testing.T, refs []string, want string) {
	t.Helper()
	for _, r := range refs {
		if r == want {
			return
		}
	}
	t.Errorf("missing ref %q; got %v", want, refs)
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
