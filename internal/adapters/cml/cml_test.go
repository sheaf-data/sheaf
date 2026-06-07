package cml

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// Happy path: a .cml with two knobs (string with max_size + uint64) emits
// two CONFIG_KNOB ContractElements with correct type/constraints carried
// in EcosystemMeta.
func TestDiscover_HappyPath(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "my_pkg", "meta")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := `// header comment
{
    program: { runner: "elf", binary: "bin/x" },
    config: {
        greeting: {
            type: "string",
            max_size: 512,
        },
        delay_ms: { type: "uint64" },
    },
}
`
	if err := os.WriteFile(filepath.Join(pkg, "app.cml"), []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := New(Config{})
	elems, err := a.Discover(context.Background(), dir, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(elems) != 2 {
		t.Fatalf("want 2 knobs, got %d", len(elems))
	}
	wantIDs := map[string]bool{
		"cml:my_pkg/app/greeting": false,
		"cml:my_pkg/app/delay_ms": false,
	}
	for _, e := range elems {
		if _, ok := wantIDs[e.GetId()]; ok {
			wantIDs[e.GetId()] = true
		}
		if e.GetKind() != contractpb.ContractElementKind_CONFIG_KNOB {
			t.Errorf("%s: want kind CONFIG_KNOB, got %v", e.GetId(), e.GetKind())
		}
		if e.GetEcosystem() != "cml" {
			t.Errorf("%s: want ecosystem cml, got %q", e.GetId(), e.GetEcosystem())
		}
		if e.GetLocation().GetPath() == "" {
			t.Errorf("%s: missing location.path", e.GetId())
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("missing expected id %q", id)
		}
	}

	// Spot-check EcosystemMeta on the greeting (the one with max_size).
	for _, e := range elems {
		if e.GetId() != "cml:my_pkg/app/greeting" {
			continue
		}
		fields := e.GetEcosystemMeta().GetFields()
		if got := fields["type"].GetStringValue(); got != "string" {
			t.Errorf("greeting.type: want string, got %q", got)
		}
		if got := fields["max_size"].GetStringValue(); got != "512" {
			t.Errorf("greeting.max_size: want 512, got %q", got)
		}
	}
}

// A .cml without a config block emits no elements.
func TestDiscover_NoConfigBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "p", "meta"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := `{
    program: { runner: "elf", binary: "bin/x" },
    use: [ { protocol: "fuchsia.logger.LogSink" } ],
}
`
	if err := os.WriteFile(filepath.Join(dir, "p", "meta", "app.cml"), []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a := New(Config{})
	elems, err := a.Discover(context.Background(), dir, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(elems) != 0 {
		t.Fatalf("want 0 knobs, got %d", len(elems))
	}
}
