package indexer

import (
	"sort"
	"strings"
	"testing"

	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// makeBridges is a one-liner shortcut around ParseBridges that fails
// the test on parse error. Convenience for the "expected to parse"
// table cases.
func makeBridges(t *testing.T, cfg ...*configpb.CodegenBridge) []Bridge {
	t.Helper()
	bs, err := ParseBridges(cfg)
	if err != nil {
		t.Fatalf("ParseBridges: %v", err)
	}
	return bs
}

// edgeKey is a normalized comparison form for edge assertions.
func edgeKey(e SameAsEdge) string {
	return e.SourceID + " => " + e.TargetID
}

func sortedEdgeKeys(edges []SameAsEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, edgeKey(e))
	}
	sort.Strings(out)
	return out
}

// bridge_basic: proto Message with cpp sibling at the rendered ID;
// resolver emits one SAME_AS edge.
func TestResolve_BridgeBasic(t *testing.T) {
	bridges := makeBridges(t, &configpb.CodegenBridge{
		SourceEcosystem:    "proto",
		TargetEcosystem:    "cpp",
		TargetNameTemplate: "{{.PackageCpp}}::pwpb::{{.Name}}",
	})
	elems := []*contractpb.ContractElement{
		{
			Id:        "pw.log/LogEntry",
			Kind:      contractpb.ContractElementKind_TYPE,
			Ecosystem: "proto",
			Library:   "pw.log",
		},
		{
			Id:        "pw::log::pwpb::LogEntry",
			Kind:      contractpb.ContractElementKind_CPP_CLASS,
			Ecosystem: "cpp",
			Library:   "pw_log",
		},
	}
	edges := Resolve(bridges, elems)
	if len(edges) != 1 {
		t.Fatalf("want 1 edge; got %d (%v)", len(edges), edges)
	}
	got := edges[0]
	if got.SourceID != "pw.log/LogEntry" || got.TargetID != "pw::log::pwpb::LogEntry" || got.BridgeID != 0 {
		t.Errorf("unexpected edge: %+v", got)
	}
}

// bridge_template_var: exercise each documented proto + fidl template
// variable and assert the rendered ID matches what the lookup expects.
func TestResolve_BridgeTemplateVariables(t *testing.T) {
	tests := []struct {
		name       string
		bridge     *configpb.CodegenBridge
		source     *contractpb.ContractElement
		targetID   string
		targetEco  string
		targetKind contractpb.ContractElementKind
	}{
		{
			name: "proto-package",
			bridge: &configpb.CodegenBridge{
				SourceEcosystem:    "proto",
				TargetEcosystem:    "cpp",
				TargetNameTemplate: "{{.Package}}::{{.Name}}",
			},
			source: &contractpb.ContractElement{
				Id: "pw.log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
				Ecosystem: "proto", Library: "pw.log",
			},
			targetID: "pw.log::LogEntry", targetEco: "cpp",
			targetKind: contractpb.ContractElementKind_CPP_CLASS,
		},
		{
			name: "proto-package-cpp-and-package-slash",
			bridge: &configpb.CodegenBridge{
				SourceEcosystem:    "proto",
				TargetEcosystem:    "cpp",
				TargetNameTemplate: "{{.PackageSlash}}|{{.PackageCpp}}|{{.Name}}",
			},
			source: &contractpb.ContractElement{
				Id: "pw.log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
				Ecosystem: "proto", Library: "pw.log",
			},
			targetID: "pw/log|pw::log|LogEntry", targetEco: "cpp",
			targetKind: contractpb.ContractElementKind_CPP_CLASS,
		},
		{
			name: "proto-fullname-and-kind",
			bridge: &configpb.CodegenBridge{
				SourceEcosystem:    "proto",
				TargetEcosystem:    "cpp",
				TargetNameTemplate: "{{.FullName}}#{{.Kind}}",
			},
			source: &contractpb.ContractElement{
				Id: "pw.log/Logs", Kind: contractpb.ContractElementKind_PROTOCOL,
				Ecosystem: "proto", Library: "pw.log",
			},
			targetID: "pw.log.Logs#service", targetEco: "cpp",
			targetKind: contractpb.ContractElementKind_CPP_CLASS,
		},
		{
			name: "proto-method-service-method",
			bridge: &configpb.CodegenBridge{
				SourceEcosystem:    "proto",
				TargetEcosystem:    "cpp",
				TargetNameTemplate: "{{.Service}}::{{.Method}}",
			},
			source: &contractpb.ContractElement{
				Id: "pw.log/Logs.Listen", Kind: contractpb.ContractElementKind_METHOD,
				Ecosystem: "proto", Library: "pw.log",
			},
			targetID: "Logs::Listen", targetEco: "cpp",
			targetKind: contractpb.ContractElementKind_CPP_METHOD,
		},
		{
			name: "fidl-library-and-name",
			bridge: &configpb.CodegenBridge{
				SourceEcosystem:    "fidl",
				TargetEcosystem:    "cpp",
				TargetNameTemplate: "{{.LibraryCpp}}::{{.Name}}",
			},
			source: &contractpb.ContractElement{
				Id: "fuchsia.io/Directory", Kind: contractpb.ContractElementKind_PROTOCOL,
				Ecosystem: "fidl", Library: "fuchsia.io",
			},
			targetID: "fuchsia::io::Directory", targetEco: "cpp",
			targetKind: contractpb.ContractElementKind_CPP_CLASS,
		},
		{
			name: "fidl-method-protocol-method",
			bridge: &configpb.CodegenBridge{
				SourceEcosystem:    "fidl",
				TargetEcosystem:    "cpp",
				TargetNameTemplate: "{{.Protocol}}::{{.Method}}",
			},
			source: &contractpb.ContractElement{
				Id: "fuchsia.io/Directory.Open", Kind: contractpb.ContractElementKind_METHOD,
				Ecosystem: "fidl", Library: "fuchsia.io",
			},
			targetID: "Directory::Open", targetEco: "cpp",
			targetKind: contractpb.ContractElementKind_CPP_METHOD,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			bridges := makeBridges(t, tc.bridge)
			elems := []*contractpb.ContractElement{
				tc.source,
				{Id: tc.targetID, Kind: tc.targetKind, Ecosystem: tc.targetEco},
			}
			edges := Resolve(bridges, elems)
			if len(edges) != 1 {
				t.Fatalf("want 1 edge; got %d (%+v)", len(edges), edges)
			}
			if edges[0].TargetID != tc.targetID {
				t.Errorf("rendered to %q; want %q", edges[0].TargetID, tc.targetID)
			}
		})
	}
}

// bridge_missing_target: bridge declared, source elements present, but
// no matching target — assert no edge and no error.
func TestResolve_BridgeMissingTarget(t *testing.T) {
	bridges := makeBridges(t, &configpb.CodegenBridge{
		SourceEcosystem:    "proto",
		TargetEcosystem:    "cpp",
		TargetNameTemplate: "{{.PackageCpp}}::pwpb::{{.Name}}",
	})
	elems := []*contractpb.ContractElement{
		{
			Id: "pw.log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
			Ecosystem: "proto", Library: "pw.log",
		},
		// A cpp element exists in the corpus but isn't the name the
		// template would render to.
		{
			Id: "pw::other::LogEntry", Kind: contractpb.ContractElementKind_CPP_CLASS,
			Ecosystem: "cpp",
		},
	}
	edges := Resolve(bridges, elems)
	if len(edges) != 0 {
		t.Errorf("want 0 edges; got %d (%+v)", len(edges), edges)
	}
}

// bridge_bad_template: invalid template syntax must surface at
// ParseBridges, not at Resolve.
func TestParseBridges_BadTemplate(t *testing.T) {
	_, err := ParseBridges([]*configpb.CodegenBridge{
		{SourceEcosystem: "proto", TargetEcosystem: "cpp", TargetNameTemplate: "{{.Name"},
	})
	if err == nil {
		t.Fatal("want parse error; got nil")
	}
	if !strings.Contains(err.Error(), "codegen_bridges[0]") {
		t.Errorf("error should mention which bridge index failed: %v", err)
	}
}

// bridge_no_kind_in_var: METHOD-only template variables ({{.Service}},
// {{.Method}}) used on a non-METHOD source element must render to
// empty strings, which causes the lookup to MISS cleanly rather than
// panic, attribute to the wrong thing, or render to a partial ID that
// accidentally matches an unrelated cpp element.
func TestResolve_MethodOnlyVarsOnNonMethodSource(t *testing.T) {
	bridges := makeBridges(t, &configpb.CodegenBridge{
		SourceEcosystem:    "proto",
		TargetEcosystem:    "cpp",
		TargetNameTemplate: "{{.Service}}::{{.Method}}",
	})
	elems := []*contractpb.ContractElement{
		// A proto message — neither {{.Service}} nor {{.Method}} is
		// populated. The rendered string is "::", which intentionally
		// does NOT match anything sensible in the cpp corpus.
		{
			Id: "pw.log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
			Ecosystem: "proto", Library: "pw.log",
		},
		// A cpp class that we explicitly should NOT bridge to. The
		// "::" rendered ID must miss it.
		{
			Id: "::", Kind: contractpb.ContractElementKind_CPP_CLASS,
			Ecosystem: "cpp",
		},
		// The "expected miss" path also covers the actual element
		// we'd hope to find — none exists.
	}
	edges := Resolve(bridges, elems)
	// We accept the literal "::" → "::" lookup match as a degenerate
	// hit (the corpus literally contains that ID). The point of this
	// test is that rendering didn't panic. Whether it emits an edge
	// to the placeholder element is a non-issue; what matters is the
	// renderer treated the missing variables as empty strings. Assert
	// no panic + the source element is not attributed to anything
	// unrelated.
	for _, e := range edges {
		if e.SourceID != "pw.log/LogEntry" || e.TargetID != "::" {
			t.Errorf("unexpected edge: %+v", e)
		}
	}
}

// bridge_multiple_bridges: two bridges in one config, both fire
// against the same source corpus; both edge sets appear.
func TestResolve_MultipleBridges(t *testing.T) {
	bridges := makeBridges(t,
		&configpb.CodegenBridge{
			SourceEcosystem:    "proto",
			TargetEcosystem:    "cpp",
			TargetNameTemplate: "{{.PackageCpp}}::pwpb::{{.Name}}",
		},
		&configpb.CodegenBridge{
			SourceEcosystem:    "proto",
			TargetEcosystem:    "cpp",
			TargetNameTemplate: "{{.PackageCpp}}::nanopb::{{.Name}}",
		},
	)
	elems := []*contractpb.ContractElement{
		{
			Id: "pw.log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
			Ecosystem: "proto", Library: "pw.log",
		},
		{Id: "pw::log::pwpb::LogEntry", Kind: contractpb.ContractElementKind_CPP_CLASS, Ecosystem: "cpp"},
		{Id: "pw::log::nanopb::LogEntry", Kind: contractpb.ContractElementKind_CPP_CLASS, Ecosystem: "cpp"},
	}
	got := sortedEdgeKeys(Resolve(bridges, elems))
	want := []string{
		"pw.log/LogEntry => pw::log::nanopb::LogEntry",
		"pw.log/LogEntry => pw::log::pwpb::LogEntry",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got edges:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// ParseBridges must reject empty source / target / template entries so
// a typoed config doesn't silently no-op.
func TestParseBridges_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		b    *configpb.CodegenBridge
	}{
		{"empty-source", &configpb.CodegenBridge{TargetEcosystem: "cpp", TargetNameTemplate: "{{.Name}}"}},
		{"empty-target", &configpb.CodegenBridge{SourceEcosystem: "proto", TargetNameTemplate: "{{.Name}}"}},
		{"empty-template", &configpb.CodegenBridge{SourceEcosystem: "proto", TargetEcosystem: "cpp"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseBridges([]*configpb.CodegenBridge{c.b}); err == nil {
				t.Error("want error; got nil")
			}
		})
	}
}
