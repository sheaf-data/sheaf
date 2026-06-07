// Integration test against the real fuchsia.io FIDL source.
//
// Skipped unless `/Volumes/T7/fuchsia/sdk/fidl/fuchsia.io/io.fidl`
// exists. To run elsewhere, set SHEAF_FUCHSIA_REPO to point at a
// Fuchsia checkout root.
//
// This is a soft test — it asserts shape rather than exact counts,
// since Fuchsia FIDL evolves. The counts asserted are conservative
// floors derived from the structural minimum (Directory has at least
// 3 composes; fuchsia.io has at least 5 protocols, etc.).

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/adapters/fidl"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

func resolveFuchsiaRepo(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("SHEAF_FUCHSIA_REPO"); env != "" {
		if _, err := os.Stat(filepath.Join(env, "sdk/fidl/fuchsia.io/io.fidl")); err == nil {
			return env
		}
		t.Fatalf("SHEAF_FUCHSIA_REPO=%q has no sdk/fidl/fuchsia.io/io.fidl", env)
	}
	def := "/Volumes/T7/fuchsia"
	if _, err := os.Stat(filepath.Join(def, "sdk/fidl/fuchsia.io/io.fidl")); err == nil {
		return def
	}
	t.Skip("no Fuchsia checkout located (looked at /Volumes/T7/fuchsia and $SHEAF_FUCHSIA_REPO)")
	return ""
}

func TestFIDL_FuchsiaIO_RealSource(t *testing.T) {
	repo := resolveFuchsiaRepo(t)
	a := fidl.New(fidl.Config{Include: []string{"sdk/fidl/**/*.fidl"}})
	elems, claims, err := a.DiscoverWithDocs(
		context.Background(),
		repo,
		adapters.ScopeConfig{Libraries: []string{"fuchsia.io"}},
	)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// --- Counts: shape, not exact numbers ---
	if len(elems) < 100 {
		t.Errorf("elements = %d, want >=100 (fuchsia.io has ~120)", len(elems))
	}
	if len(claims) < 50 {
		t.Errorf("doc claims = %d, want >=50", len(claims))
	}

	// --- Per-kind floors ---
	byKind := make(map[contractpb.ContractElementKind]int)
	for _, e := range elems {
		byKind[e.GetKind()]++
	}
	if byKind[contractpb.ContractElementKind_PROTOCOL] < 5 {
		t.Errorf("PROTOCOL count = %d, want >=5", byKind[contractpb.ContractElementKind_PROTOCOL])
	}
	if byKind[contractpb.ContractElementKind_METHOD] < 30 {
		t.Errorf("METHOD count = %d, want >=30", byKind[contractpb.ContractElementKind_METHOD])
	}
	if byKind[contractpb.ContractElementKind_TYPE] < 30 {
		t.Errorf("TYPE count = %d, want >=30", byKind[contractpb.ContractElementKind_TYPE])
	}

	// --- Specific elements that have existed in fuchsia.io for years ---
	want := []string{
		"fuchsia.io/Directory",
		"fuchsia.io/Directory.Open",
		"fuchsia.io/File",
		"fuchsia.io/Node",
		"fuchsia.io/Openable",
	}
	for _, id := range want {
		if findElemByID(elems, id) == nil {
			t.Errorf("missing well-known element %q", id)
		}
	}

	// --- Directory has the canonical composes ---
	dir := findElemByID(elems, "fuchsia.io/Directory")
	if dir == nil {
		t.Fatal("Directory protocol missing")
	}
	composeTargets := make(map[string]bool)
	for _, r := range dir.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_COMPOSED_FROM {
			composeTargets[r.GetTargetElementId()] = true
		}
	}
	for _, want := range []string{"fuchsia.io/Openable", "fuchsia.io/Node"} {
		if !composeTargets[want] {
			t.Errorf("Directory missing compose %q; got %v", want, composeTargets)
		}
	}

	// --- Node composes from fuchsia.unknown — cross-library resolution ---
	node := findElemByID(elems, "fuchsia.io/Node")
	if node == nil {
		t.Fatal("Node protocol missing")
	}
	hasExternalCompose := false
	for _, r := range node.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_COMPOSED_FROM &&
			strings.HasPrefix(r.GetTargetElementId(), "fuchsia.unknown/") {
			hasExternalCompose = true
		}
	}
	if !hasExternalCompose {
		t.Errorf("Node should compose fuchsia.unknown.* protocols; got %+v", node.GetRelationships())
	}

	// --- Doc-comment substance: most fuchsia.io claims are >=PARTIAL ---
	var substantive int
	for _, c := range claims {
		if c.GetSubstance() >= 3 {
			substantive++
		}
	}
	frac := float64(substantive) / float64(len(claims))
	if frac < 0.5 {
		t.Errorf("PARTIAL+ doc fraction = %.2f, want >=0.5", frac)
	}
}

func TestFIDL_DriverFramework_RealSource(t *testing.T) {
	repo := resolveFuchsiaRepo(t)
	a := fidl.New(fidl.Config{Include: []string{"sdk/fidl/**/*.fidl"}})
	elems, _, err := a.DiscoverWithDocs(
		context.Background(),
		repo,
		adapters.ScopeConfig{Libraries: []string{"fuchsia.driver.framework"}},
	)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) < 20 {
		t.Errorf("fuchsia.driver.framework elements = %d, want >=20", len(elems))
	}
	if findElemByID(elems, "fuchsia.driver.framework/Driver") == nil {
		t.Errorf("Driver protocol missing")
	}
}

func TestFIDL_DriverFramework_WildcardScope(t *testing.T) {
	repo := resolveFuchsiaRepo(t)
	a := fidl.New(fidl.Config{Include: []string{"sdk/fidl/**/*.fidl"}})
	elems, _, err := a.DiscoverWithDocs(
		context.Background(),
		repo,
		adapters.ScopeConfig{Libraries: []string{"fuchsia.driver.*"}},
	)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	libs := make(map[string]bool)
	for _, e := range elems {
		libs[e.GetLibrary()] = true
	}
	if len(libs) < 5 {
		t.Errorf("fuchsia.driver.* wildcard matched %d libraries, want >=5: %v", len(libs), libs)
	}
}

func findElemByID(elems []*contractpb.ContractElement, id string) *contractpb.ContractElement {
	for _, e := range elems {
		if e.GetId() == id {
			return e
		}
	}
	return nil
}
