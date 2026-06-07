package cml

import (
	"context"
	"os"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// Integration: scan a real slice of the Fuchsia checkout if it's mounted,
// and confirm the adapter extracts at least a few CONFIG_KNOBs without
// returning an error. Skipped when T7 isn't mounted (CI / non-dev hosts).
func TestDiscover_Fuchsia_T7(t *testing.T) {
	root := "/Volumes/T7/fuchsia/examples/components"
	if _, err := os.Stat(root); err != nil {
		t.Skipf("T7 fuchsia checkout not present at %s; skipping integration test", root)
	}
	a := New(Config{})
	elems, err := a.Discover(context.Background(), root, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(elems) == 0 {
		t.Fatalf("want >= 1 CONFIG_KNOB across examples/components, got 0")
	}
	for _, e := range elems {
		if e.GetKind() != contractpb.ContractElementKind_CONFIG_KNOB {
			t.Errorf("%s: want CONFIG_KNOB, got %v", e.GetId(), e.GetKind())
		}
	}
	t.Logf("integration: extracted %d CONFIG_KNOBs from %s", len(elems), root)
}
