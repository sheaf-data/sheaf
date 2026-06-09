package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
)

// buildGotestPlugin compiles the reference runtime adapter into a temp
// path for the duration of the test.
func buildGotestPlugin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; skipping external-adapter e2e")
	}
	bin := filepath.Join(t.TempDir(), "sheaf-adapter-gotest")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/sheaf-data/sheaf/cmd/sheaf-adapter-gotest")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build gotest plugin: %v", err)
	}
	return bin
}

// TestRun_ExternalTestParser runs a full scan whose only test parser is
// an out-of-process plugin, and asserts the runtime adapter is
// indistinguishable from an in-process one: the tests land in the corpus
// and carry the configured provenance (deterministic tier, source
// "gotest").
func TestRun_ExternalTestParser(t *testing.T) {
	bin := buildGotestPlugin(t)
	repo := setupRepo(t, map[string]string{
		"pkg/widget/widget_test.go": `package widget

import "testing"

func TestWidgetSpins(t *testing.T) { _ = t }
func TestWidgetStops(t *testing.T) { _ = t }
`,
	})
	cfg := &configpb.Config{
		Version: 1,
		Project: &configpb.Project{Name: "test"},
		TestParser: []*configpb.TestParserConfig{
			{
				Name: "external",
				PerAdapter: &configpb.TestParserConfig_External{
					External: &configpb.ExternalAdapterConfig{
						Command: bin,
						Include: []string{"**/*_test.go"},
						Name:    "gotest",
					},
				},
			},
		},
	}

	o, err := New(cfg, nil, repo)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := o.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.AdapterErrors) > 0 {
		t.Fatalf("AdapterErrors = %+v, want none", res.AdapterErrors)
	}
	tests := res.Corpus.Tests()
	if len(tests) != 2 {
		t.Fatalf("Tests = %d, want 2", len(tests))
	}
	for _, tc := range tests {
		p := tc.GetProvenance()
		if p.GetTier() != commonpb.RowProvenance_DETERMINISTIC || p.GetSource() != "gotest" {
			t.Errorf("test %q provenance = {%v, %q}, want {DETERMINISTIC, gotest}",
				tc.GetId(), p.GetTier(), p.GetSource())
		}
	}

	// The runtime adapter shows up in the summary under its configured name.
	if s := o.Summary(); len(s.TestParsers) != 1 || s.TestParsers[0] != "gotest" {
		t.Errorf("Summary().TestParsers = %v, want [gotest]", s.TestParsers)
	}
}

// TestCheckHealth_External probes a healthy external adapter and a
// missing one, asserting `sheaf doctor`'s health check distinguishes them.
func TestCheckHealth_External(t *testing.T) {
	bin := buildGotestPlugin(t)

	ext := func(command string) *configpb.Config {
		return &configpb.Config{
			Version: 1,
			Project: &configpb.Project{Name: "x"},
			TestParser: []*configpb.TestParserConfig{
				{
					Name: "external",
					PerAdapter: &configpb.TestParserConfig_External{
						External: &configpb.ExternalAdapterConfig{Command: command, Name: "gotest"},
					},
				},
			},
		}
	}

	// Healthy: the info probe succeeds and reports a compatible version.
	oOK, err := New(ext(bin), nil, "/tmp")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	health := oOK.CheckHealth(context.Background())
	if len(health) != 1 || health[0].Err != nil {
		t.Fatalf("CheckHealth = %+v, want one healthy entry", health)
	}

	// Missing binary: health probe must surface the failure.
	oBad, err := New(ext(filepath.Join(t.TempDir(), "nope")), nil, "/tmp")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	health = oBad.CheckHealth(context.Background())
	if len(health) != 1 || health[0].Err == nil {
		t.Fatalf("CheckHealth = %+v, want one failing entry for a missing plugin", health)
	}
}

// TestNew_ExternalMissingCommand verifies construction fails fast when an
// external block omits its command (a config error, not a runtime one).
func TestNew_ExternalMissingCommand(t *testing.T) {
	cfg := &configpb.Config{
		Version: 1,
		Project: &configpb.Project{Name: "x"},
		TestParser: []*configpb.TestParserConfig{
			{
				Name: "external",
				PerAdapter: &configpb.TestParserConfig_External{
					External: &configpb.ExternalAdapterConfig{Include: []string{"**/*_test.go"}},
				},
			},
		},
	}
	if _, err := New(cfg, nil, "/tmp"); err == nil {
		t.Fatal("want an error when external.command is empty")
	}
}
