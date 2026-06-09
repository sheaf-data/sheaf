package external

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/adapters/gotest"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// fixtureRepo writes a small Go test corpus that exercises the gotest
// parser's three output shapes: top-level test funcs, t.Run subtests, and
// cobra-invocation ContractRefs (driven by binary_name).
func fixtureRepo(t *testing.T) string {
	t.Helper()
	files := map[string]string{
		"cli/command/container/run_test.go": `package container

import (
	"os/exec"
	"testing"
)

func TestRunWithRm(t *testing.T) {
	exec.Command("docker", "run", "--rm", "alpine")
	t.Run("with platform", func(t *testing.T) {
		_ = t
	})
}

func TestRunDetached(t *testing.T) {
	_ = t
}
`,
		"cli/command/image/build_test.go": `package image

import "testing"

func TestBuildContext(t *testing.T) {
	t.Run("from dockerfile", func(t *testing.T) { _ = t })
}
`,
	}
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestEquivalence_InProcessVsPlugin is the core guarantee of the
// runtime-adapter feature: the stock gotest parser run in-process and the
// same parser run as an out-of-process plugin emit identical TestCases on
// identical input. If this drifts, the wire protocol is lossy.
func TestEquivalence_InProcessVsPlugin(t *testing.T) {
	repo := fixtureRepo(t)
	scope := adapters.ScopeConfig{}
	include := []string{"**/*_test.go"}
	const binaryName = "docker"

	// In-process.
	want, err := gotest.New(gotest.Config{Include: include, BinaryName: binaryName}).
		Discover(context.Background(), repo, scope)
	if err != nil {
		t.Fatalf("in-process Discover: %v", err)
	}

	// Out-of-process via the reference plugin.
	tp, err := NewTestParser(Config{
		Command: gotestPluginBin,
		Include: include,
		Option:  map[string]string{"binary_name": binaryName},
		Name:    gotest.Name,
	})
	if err != nil {
		t.Fatalf("NewTestParser: %v", err)
	}
	got, err := tp.Discover(context.Background(), repo, scope)
	if err != nil {
		t.Fatalf("plugin Discover: %v", err)
	}

	if len(want) == 0 {
		t.Fatal("fixture produced no tests; the equivalence check would be vacuous")
	}
	sortTests(want)
	sortTests(got)
	if len(got) != len(want) {
		t.Fatalf("count mismatch: plugin=%d in-process=%d", len(got), len(want))
	}
	for i := range want {
		if !proto.Equal(want[i], got[i]) {
			t.Errorf("test[%d] differs:\n in-process = %v\n     plugin = %v", i, want[i], got[i])
		}
	}

	// Name must match the stock adapter so provenance reads identically.
	if tp.Name() != gotest.Name {
		t.Errorf("Name() = %q, want %q", tp.Name(), gotest.Name)
	}
}

func sortTests(ts []*testcasepb.TestCase) {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].GetId() != ts[j].GetId() {
			return ts[i].GetId() < ts[j].GetId()
		}
		li, lj := ts[i].GetLocation(), ts[j].GetLocation()
		if li.GetPath() != lj.GetPath() {
			return li.GetPath() < lj.GetPath()
		}
		return li.GetLine() < lj.GetLine()
	})
}
