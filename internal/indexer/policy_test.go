package indexer

import (
	"strings"
	"testing"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// TestKindStrategiesExhaustive asserts that every defined
// ContractElementKind (except KIND_UNSPECIFIED) has an entry in
// kindStrategies. A missing kind is a programming error: it means
// the matcher's behavior for that kind silently falls back to the
// fail-closed default in strategiesFor (direct_ref only), which is
// rarely what the kind's authors intended.
func TestKindStrategiesExhaustive(t *testing.T) {
	for k, name := range contractpb.ContractElementKind_name {
		kind := contractpb.ContractElementKind(k)
		if kind == contractpb.ContractElementKind_KIND_UNSPECIFIED {
			continue
		}
		strategies, ok := kindStrategies[kind]
		if !ok {
			t.Errorf("kind %s (%d) missing from kindStrategies", name, k)
			continue
		}
		if len(strategies) == 0 {
			t.Errorf("kind %s has empty strategy list", name)
		}
		seen := map[Strategy]bool{}
		for _, s := range strategies {
			if seen[s] {
				t.Errorf("kind %s has duplicate strategy %s", name, s)
			}
			seen[s] = true
		}
	}
}

// TestKindStrategiesDefaults locks the post-validation admission
// policy: fine-grained kinds admit Strategy 1 only; coarse kinds admit
// Strategy 1 + 3. No kind admits Strategy 2 (implements-map) under
// default policy — it was demoted to a relationship-only data source.
// The test failing on a future change is the canary that someone widened (or
// narrowed) policy without updating the admission rules.
func TestKindStrategiesDefaults(t *testing.T) {
	cases := []struct {
		kind contractpb.ContractElementKind
		want []Strategy
	}{
		// Fine-grained.
		{contractpb.ContractElementKind_FLAG, []Strategy{StrategyDirectRef}},
		{contractpb.ContractElementKind_SWITCH, []Strategy{StrategyDirectRef}},
		{contractpb.ContractElementKind_CONFIG_KNOB, []Strategy{StrategyDirectRef}},
		{contractpb.ContractElementKind_METHOD, []Strategy{StrategyDirectRef}},
		{contractpb.ContractElementKind_TYPE, []Strategy{StrategyDirectRef}},
		{contractpb.ContractElementKind_PROTOCOL, []Strategy{StrategyDirectRef}},
		{contractpb.ContractElementKind_POSITIONAL, []Strategy{StrategyDirectRef}},
		{contractpb.ContractElementKind_CPP_CLASS, []Strategy{StrategyDirectRef}},
		{contractpb.ContractElementKind_RUST_TYPE, []Strategy{StrategyDirectRef}},
		// Coarse.
		{contractpb.ContractElementKind_SUBCOMMAND, []Strategy{StrategyDirectRef, StrategyNameTokens}},
		{contractpb.ContractElementKind_LIBRARY, []Strategy{StrategyDirectRef, StrategyNameTokens}},
		{contractpb.ContractElementKind_CONFIG_FACET, []Strategy{StrategyDirectRef, StrategyNameTokens}},
	}
	for _, c := range cases {
		got := strategiesFor(c.kind)
		if !equalStrategies(got, c.want) {
			t.Errorf("strategiesFor(%s) = %v, want %v", c.kind, got, c.want)
		}
	}
}

// TestAdmitsStrategy verifies the boolean-form policy helper used by
// the matcher.
func TestAdmitsStrategy(t *testing.T) {
	if !admitsStrategy(contractpb.ContractElementKind_FLAG, StrategyDirectRef) {
		t.Error("FLAG should admit direct_ref")
	}
	if admitsStrategy(contractpb.ContractElementKind_FLAG, StrategyNameTokens) {
		t.Error("FLAG must not admit name_tokens — Strategy 3 over-attributes for flags")
	}
	if !admitsStrategy(contractpb.ContractElementKind_SUBCOMMAND, StrategyNameTokens) {
		t.Error("SUBCOMMAND should admit name_tokens — cliShapeMatch keeps it precise")
	}
	if admitsStrategy(contractpb.ContractElementKind_METHOD, StrategyImplementsMap) {
		t.Error("METHOD must not admit implements_map — demoted to relationship-only")
	}
	if admitsStrategy(contractpb.ContractElementKind_FLAG, StrategyImplementsMap) {
		t.Error("FLAG must not admit implements_map — no kind admits implements_map under default policy")
	}
	if admitsStrategy(contractpb.ContractElementKind_METHOD, StrategyNameTokens) {
		t.Error("METHOD must not admit name_tokens — sibling-method bleed (grpc channelz)")
	}
}

// TestKindSurfacesExhaustive asserts that every defined
// ContractElementKind (except KIND_UNSPECIFIED) has an entry in
// kindSurfaces. Mirrors TestKindStrategiesExhaustive — adding a new
// kind must be a deliberate per-surface decision, not a silent
// fallback to surfacesFor's fail-closed default.
func TestKindSurfacesExhaustive(t *testing.T) {
	for k, name := range contractpb.ContractElementKind_name {
		kind := contractpb.ContractElementKind(k)
		if kind == contractpb.ContractElementKind_KIND_UNSPECIFIED {
			continue
		}
		surfaces, ok := kindSurfaces[kind]
		if !ok {
			t.Errorf("kind %s (%d) missing from kindSurfaces", name, k)
			continue
		}
		if len(surfaces) == 0 {
			t.Errorf("kind %s has empty surface list", name)
		}
		seen := map[string]bool{}
		for _, s := range surfaces {
			if seen[s] {
				t.Errorf("kind %s has duplicate surface %s", name, s)
			}
			seen[s] = true
		}
	}
}

// TestKindSurfacesDefaults locks the v1 per-kind surface set.
func TestKindSurfacesDefaults(t *testing.T) {
	cases := []struct {
		kind contractpb.ContractElementKind
		want []string
	}{
		// Interface kinds: implementations replaces tests.
		{contractpb.ContractElementKind_METHOD, []string{"docs.reference", "docs.concepts", "examples", "implementations"}},
		{contractpb.ContractElementKind_TYPE, []string{"docs.reference", "docs.concepts", "examples", "implementations"}},
		{contractpb.ContractElementKind_PROTOCOL, []string{"docs.reference", "docs.concepts", "examples", "implementations"}},
		{contractpb.ContractElementKind_SYSCALL, []string{"docs.reference", "docs.concepts", "examples", "implementations"}},
		// CLI kinds: tests are real.
		{contractpb.ContractElementKind_FLAG, []string{"docs.reference", "docs.concepts", "examples", "tests"}},
		{contractpb.ContractElementKind_SWITCH, []string{"docs.reference", "docs.concepts", "examples", "tests"}},
		{contractpb.ContractElementKind_CONFIG_KNOB, []string{"docs.reference", "docs.concepts", "examples", "tests"}},
		{contractpb.ContractElementKind_SUBCOMMAND, []string{"docs.reference", "docs.concepts", "examples", "tests"}},
		{contractpb.ContractElementKind_POSITIONAL, []string{"docs.reference", "docs.concepts", "examples", "tests"}},
		{contractpb.ContractElementKind_CONFIG_FACET, []string{"docs.reference", "docs.concepts", "examples", "tests"}},
		// Implementation kinds: where real test coverage lives.
		{contractpb.ContractElementKind_CPP_CLASS, []string{"docs.reference", "tests"}},
		{contractpb.ContractElementKind_RUST_TYPE, []string{"docs.reference", "tests"}},
		// Synthetic grouping element.
		{contractpb.ContractElementKind_LIBRARY, []string{"docs.reference", "docs.concepts", "examples"}},
	}
	for _, c := range cases {
		got := surfacesFor(c.kind)
		if !equalStringSlices(got, c.want) {
			t.Errorf("surfacesFor(%s) = %v, want %v", c.kind, got, c.want)
		}
	}
}

// TestKindSurfacesInterfaceKindsHaveNoTests is the load-bearing
// invariant for the interface-surfaces redesign: FIDL / proto interface
// kinds NEVER declare a tests surface under default policy. If this
// test fails, the implements-map regression that motivated the
// redesign is back.
func TestKindSurfacesInterfaceKindsHaveNoTests(t *testing.T) {
	interfaceKinds := []contractpb.ContractElementKind{
		contractpb.ContractElementKind_METHOD,
		contractpb.ContractElementKind_TYPE,
		contractpb.ContractElementKind_PROTOCOL,
		contractpb.ContractElementKind_SYSCALL,
	}
	for _, k := range interfaceKinds {
		surfaces := surfacesFor(k)
		for _, s := range surfaces {
			if s == SurfaceTests {
				t.Errorf("interface kind %s must not declare tests surface", k)
			}
		}
		if !containsString(surfaces, SurfaceImplementations) {
			t.Errorf("interface kind %s must declare implementations surface", k)
		}
	}
}

// TestIsInterfaceKind verifies the helper used by indexer.Build to
// decide which elements get implementations populated.
func TestIsInterfaceKind(t *testing.T) {
	cases := []struct {
		kind contractpb.ContractElementKind
		want bool
	}{
		{contractpb.ContractElementKind_METHOD, true},
		{contractpb.ContractElementKind_TYPE, true},
		{contractpb.ContractElementKind_PROTOCOL, true},
		{contractpb.ContractElementKind_SYSCALL, true},
		{contractpb.ContractElementKind_FLAG, false},
		{contractpb.ContractElementKind_SUBCOMMAND, false},
		{contractpb.ContractElementKind_CPP_CLASS, false},
		{contractpb.ContractElementKind_RUST_TYPE, false},
		{contractpb.ContractElementKind_LIBRARY, false},
	}
	for _, c := range cases {
		if got := isInterfaceKind(c.kind); got != c.want {
			t.Errorf("isInterfaceKind(%s) = %v, want %v", c.kind, got, c.want)
		}
	}
}

// TestStrategyString ensures the String() method covers all defined
// strategy values plus the unspecified zero.
func TestStrategyString(t *testing.T) {
	cases := []struct {
		s    Strategy
		want string
	}{
		{StrategyUnspecified, "unspecified"},
		{StrategyDirectRef, "direct_ref"},
		{StrategyImplementsMap, "implements_map"},
		{StrategyNameTokens, "name_tokens"},
	}
	for _, c := range cases {
		got := c.s.String()
		if got != c.want {
			t.Errorf("Strategy(%d).String() = %q, want %q", c.s, got, c.want)
		}
	}
}

// cliShapeInputs builds the (testTokens, pathTokens) maps the way the
// production call site in Build does: pathTokens = tokensFromPath(path),
// testTokens = name tokens ∪ path tokens. nameTokens are the extra
// tokens a suite/test name would contribute (often empty for the Rust
// crate-per-command layouts, whose suites are named after the feature,
// not the command).
func cliShapeInputs(path string, nameTokens ...string) (testTokens, pathTokens map[string]bool) {
	pathTokens = make(map[string]bool, 16)
	for _, t := range tokensFromPath(path) {
		pathTokens[strings.ToLower(t)] = true
	}
	testTokens = make(map[string]bool, len(pathTokens)+len(nameTokens))
	for k := range pathTokens {
		testTokens[k] = true
	}
	for _, t := range nameTokens {
		testTokens[strings.ToLower(t)] = true
	}
	return testTokens, pathTokens
}

// TestCliShapeMatch locks cliShapeMatch's behavior, including the
// directory-anchor fallback that lets Rust crate-per-command layouts
// (ffx plugins + subtools) match their co-located generic-module tests
// (lib.rs / args.rs), while leaving kubectl's basename-carried naming
// (history_test.go) untouched so a parent element cannot double-claim a
// child's test.
func TestCliShapeMatch(t *testing.T) {
	cases := []struct {
		name    string
		elemID  string
		library string
		path    string
		// names contributes extra (non-path) name tokens, mirroring a
		// suite/test name. Empty for the generic-module ffx cases.
		names []string
		want  bool
	}{
		// --- Directory anchor: ffx crate-per-command (must now pass) ---
		{
			name:    "ffx plugins nested target/list via lib.rs",
			elemID:  "ffx target list",
			library: "ffx",
			path:    "src/developer/ffx/plugins/target/list/src/lib.rs",
			want:    true,
		},
		{
			name:    "ffx subtools nested wm/list via lib.rs",
			elemID:  "ffx wm list",
			library: "ffx",
			path:    "src/developer/ffx/tools/wm/list/src/lib.rs",
			want:    true,
		},
		{
			name:    "ffx kebab leaf wm/set-order via lib.rs",
			elemID:  "ffx wm set-order",
			library: "ffx",
			path:    "src/developer/ffx/tools/wm/set-order/src/lib.rs",
			want:    true,
		},
		{
			name:    "ffx single-token doctor via lib.rs",
			elemID:  "ffx doctor",
			library: "ffx",
			path:    "src/developer/ffx/plugins/doctor/src/lib.rs",
			want:    true,
		},
		{
			name:    "ffx generic args.rs basename still anchors on dirs",
			elemID:  "ffx target list",
			library: "ffx",
			path:    "src/developer/ffx/plugins/target/list/src/args.rs",
			want:    true,
		},
		// --- Negatives ---
		{
			// PARENT no-double-claim: "ffx target" (required ["target"])
			// accumulates "list" then "targetlist" off the chain and
			// never equals "target", so the parent does not claim the
			// child's lib.rs.
			name:    "parent ffx target does not double-claim child list dir",
			elemID:  "ffx target",
			library: "ffx",
			path:    "src/developer/ffx/plugins/target/list/src/lib.rs",
			want:    false,
		},
		{
			// KUBECTL regression: basename "history" is not generic, so
			// the directory anchor is disabled and the parent "kubectl
			// rollout" cannot claim the history child's test.
			name:    "kubectl parent rollout does not claim history child",
			elemID:  "kubectl rollout",
			library: "kubectl",
			path:    "pkg/cmd/rollout/history_test.go",
			want:    false,
		},
		{
			// WRONG PARENT (Rule 1): "target" is absent from the path
			// (foo/list, not target/list) and from the name tokens, so
			// the all-required-tokens check fails before the dir anchor.
			name:    "ffx target list fails when target dir absent",
			elemID:  "ffx target list",
			library: "ffx",
			path:    "src/developer/ffx/plugins/foo/list/src/lib.rs",
			want:    false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testTokens, pathTokens := cliShapeInputs(c.path, c.names...)
			got := cliShapeMatch(c.elemID, c.library, testTokens, pathTokens, c.path)
			if got != c.want {
				t.Errorf("cliShapeMatch(%q, %q, path=%q) = %v, want %v",
					c.elemID, c.library, c.path, got, c.want)
			}
		})
	}
}

func equalStrategies(a, b []Strategy) bool {
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

func equalStringSlices(a, b []string) bool {
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

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
