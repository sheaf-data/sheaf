package indexer

import (
	"reflect"
	"testing"

	"github.com/sheaf-data/sheaf/internal/categorize"
	"github.com/sheaf-data/sheaf/internal/corpus"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// mkRules and mkCat are short helpers for test fixtures.
func mkCategorizer(t *testing.T, cats ...*categorizationpb.Category) *categorize.Categorizer {
	t.Helper()
	c, err := categorize.New(&categorizationpb.Rules{Version: 1, Category: cats})
	if err != nil {
		t.Fatalf("New categorizer: %v", err)
	}
	return c
}

// --- name token helpers ---

func TestTokensFromElementID(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"fuchsia.io/Directory.Open", []string{"fuchsia", "io", "directory", "open"}},
		{"fuchsia.io/Directory", []string{"fuchsia", "io", "directory"}},
		{"fuchsia.driver.framework/CompositeNodeManager", []string{"fuchsia", "driver", "framework", "composite", "node", "manager"}},
	}
	for _, c := range cases {
		got := tokensFromElementID(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokensFromElementID(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSplitCamelCase(t *testing.T) {
	cases := map[string][]string{
		"FidlReadTest":         {"fidl", "read", "test"},
		"Open":                 {"open"},
		"PERM_READ_BYTES":      {"perm_read_bytes"}, // underscores not split here
		"":                     nil,
		"CompositeNodeManager": {"composite", "node", "manager"},
	}
	for in, want := range cases {
		got := splitCamelCase(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("splitCamelCase(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSplitMethodID(t *testing.T) {
	proto, method := splitMethodID("fuchsia.io/Directory.Open")
	if proto != "Directory" || method != "Open" {
		t.Errorf("got (%q, %q), want (Directory, Open)", proto, method)
	}
	proto, method = splitMethodID("not-a-method-id")
	if proto != "" || method != "" {
		t.Errorf("got (%q, %q), want empty", proto, method)
	}
}

// --- basic build ---

func TestBuild_DocClaimAttachedToMethod(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:        "fuchsia.io/Directory.Open",
		Kind:      contractpb.ContractElementKind_METHOD,
		Ecosystem: "fidl",
		Library:   "fuchsia.io",
	})
	c.AddDocClaim(&docclaimpb.DocClaim{
		SourcePath:   "x.fidl",
		Location:     &commonpb.SourceLocation{Path: "x.fidl", Line: 1},
		ContractRefs: []string{"fuchsia.io/Directory.Open"},
		Substance:    commonpb.Substance_SUBSTANTIVE,
		Kind:         docclaimpb.DocClaimKind_REFERENCE,
		Adapter:      "fidl",
	})
	idx := New(c, nil)
	idx.Build()

	p := c.Profile("fuchsia.io/Directory.Open")
	if p == nil {
		t.Fatal("no profile for method")
	}
	if p.Docs.Reference == nil || len(p.Docs.Reference.Fidldoc) != 1 {
		t.Errorf("expected 1 fidldoc ref; got %+v", p.Docs)
	}
}

// --- inheritance materialization ---

func TestBuild_ComposeInheritsMethods(t *testing.T) {
	c := corpus.New()
	// Parent protocol Openable with method Open.
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Openable", Kind: contractpb.ContractElementKind_PROTOCOL, Library: "fuchsia.io",
	})
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Openable.Open", Kind: contractpb.ContractElementKind_METHOD, Library: "fuchsia.io",
	})
	// Child protocol Directory composes Openable.
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Directory", Kind: contractpb.ContractElementKind_PROTOCOL, Library: "fuchsia.io",
		Relationships: []*contractpb.Relationship{{
			Kind:            contractpb.RelationshipKind_COMPOSED_FROM,
			TargetElementId: "fuchsia.io/Openable",
		}},
	})

	idx := New(c, nil)
	idx.Build()

	// The indexer should have synthesized fuchsia.io/Directory.Open
	if c.Element("fuchsia.io/Directory.Open") == nil {
		t.Errorf("expected synthesized inherited method fuchsia.io/Directory.Open; elements=%v", c.ElementIDs())
	}
	// And its profile should exist
	if c.Profile("fuchsia.io/Directory.Open") == nil {
		t.Errorf("no profile for synthesized inherited method")
	}
	// INHERITED_FROM relationship.
	dirOpen := c.Element("fuchsia.io/Directory.Open")
	var hasInherited bool
	for _, r := range dirOpen.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_INHERITED_FROM &&
			r.GetTargetElementId() == "fuchsia.io/Openable.Open" {
			hasInherited = true
		}
	}
	if !hasInherited {
		t.Errorf("Directory.Open should have INHERITED_FROM Openable.Open; got %+v", dirOpen.GetRelationships())
	}
}

// --- impl-class IMPLEMENTS → interface element renders as implementations surface ---

// Under the interface-surfaces redesign, tests of impl
// classes attribute to the impl element directly via Strategy 1; the
// FIDL/proto interface element renders its IMPLEMENTS edges via the
// `implementations` surface, NOT via test attribution.
//
// Replaces the older TestBuild_TestAttributedViaImplementsMap, which
// encoded the implements-map attribution path that produced 57–100%
// FP across three Fuchsia FIDL example reports.
func TestBuild_ImplementsSurfacePopulated(t *testing.T) {
	c := corpus.New()
	// FIDL protocol + method.
	c.AddElement(&contractpb.ContractElement{
		Id:      "fuchsia.io/File",
		Kind:    contractpb.ContractElementKind_PROTOCOL,
		Library: "fuchsia.io",
		Aliases: []string{"fuchsia.io.File"},
	})
	c.AddElement(&contractpb.ContractElement{
		Id:      "fuchsia.io/File.Read",
		Kind:    contractpb.ContractElementKind_METHOD,
		Library: "fuchsia.io",
		Aliases: []string{"fuchsia.io.File.Read"},
	})
	// C++ class implementing fuchsia.io/File.
	c.AddElement(&contractpb.ContractElement{
		Id:        "cpp:src/storage/file.h#FileConnection",
		Kind:      contractpb.ContractElementKind_CPP_CLASS,
		Ecosystem: "cpp",
		Location:  &commonpb.SourceLocation{Path: "src/storage/file.h", Line: 10},
		Relationships: []*contractpb.Relationship{{
			Kind:            contractpb.RelationshipKind_IMPLEMENTS,
			TargetElementId: "fuchsia.io/File",
		}},
	})
	// Test exercising the C++ class. Under the pre-redesign model this
	// would have attributed to fuchsia.io/File via the implements-map
	// + name-mention bridge. Under the new model it does NOT — interface
	// elements get no tests, only an implementations surface.
	c.AddTest(&testcasepb.TestCase{
		Id:           "FileConnectionTest.ReadWorks",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/storage/file_tests.cc", Line: 1},
		NameTokens:   []string{"file", "connection", "test", "read", "works"},
		ContractRefs: []string{"cpp:src/storage/file.h#FileConnection"},
	})
	// A test that exercises File.Read explicitly via a ContractRef
	// (Strategy 1). Interface METHOD elements still attribute via
	// direct refs; only the implements-map bridge was removed.
	c.AddTest(&testcasepb.TestCase{
		Id:           "FileReadTest.Smoke",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/storage/file_read_test.cc", Line: 1},
		NameTokens:   []string{"file", "read", "test", "smoke"},
		ContractRefs: []string{"fuchsia.io.File.Read"},
	})

	idx := New(c, nil)
	idx.Build()

	// File protocol: zero tests (no implements-map bridge), exactly
	// one implementations entry (the C++ FileConnection class).
	p := c.Profile("fuchsia.io/File")
	if p == nil {
		t.Fatal("File profile missing")
	}
	if countTestRefs(p) != 0 {
		t.Errorf("fuchsia.io/File should have zero test attributions under new model; got %d (tests = %+v)", countTestRefs(p), p.GetTests())
	}
	if p.GetImplementations() == nil || len(p.GetImplementations().GetImpls()) != 1 {
		t.Errorf("fuchsia.io/File should have exactly one implementations entry; got %+v", p.GetImplementations())
	} else {
		impl := p.GetImplementations().GetImpls()[0]
		if impl.GetImplElementId() != "cpp:src/storage/file.h#FileConnection" {
			t.Errorf("implementations entry has wrong impl_element_id: %q", impl.GetImplElementId())
		}
		if impl.GetImplKind() != "CPP_CLASS" {
			t.Errorf("implementations entry has wrong impl_kind: %q", impl.GetImplKind())
		}
		if impl.GetPath() != "src/storage/file.h" {
			t.Errorf("implementations entry has wrong path: %q", impl.GetPath())
		}
	}

	// File.Read attribution: the direct-ref test still attributes
	// (Strategy 1 unchanged). No implementations entry because no
	// IMPLEMENTS edge targets it.
	p2 := c.Profile("fuchsia.io/File.Read")
	if p2 == nil || countTestRefs(p2) != 1 {
		t.Errorf("File.Read should attribute exactly via direct ref (1); got %+v", p2)
	}
	if p2.GetImplementations() != nil && len(p2.GetImplementations().GetImpls()) != 0 {
		t.Errorf("File.Read should have no implementations entries (no IMPLEMENTS edge); got %+v", p2.GetImplementations())
	}

	// Impl-class element: the FileConnectionTest attributes here
	// directly via Strategy 1. This is where the real test coverage
	// of the interface lives, per the redesign.
	p3 := c.Profile("cpp:src/storage/file.h#FileConnection")
	if p3 == nil || countTestRefs(p3) != 1 {
		t.Errorf("FileConnection impl should attribute exactly via direct ref (1); got %+v", p3)
	}
}

// --- test attribution: METHOD elements require direct ref ---

// Under the direct-evidence guard, a METHOD element no longer attributes
// via name-token overlap alone — a ContractRef on the TestCase is now
// the only path. Replaces the older "name-token attribution works"
// expectation, which encoded the looser policy.
func TestBuild_NameTokenAttribution(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "fuchsia.io/Directory.Open",
		Kind:    contractpb.ContractElementKind_METHOD,
		Library: "fuchsia.io",
		Aliases: []string{"fuchsia.io.Directory.Open"},
	})
	// Name-token-only test: REFUSED under direct-evidence guard.
	c.AddTest(&testcasepb.TestCase{
		Id:         "DirectoryTest.OpenSucceeds",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "src/foo.cc", Line: 1},
		NameTokens: []string{"directory", "test", "open", "succeeds"},
	})
	// Direct-ref test: attributes via Strategy 1.
	c.AddTest(&testcasepb.TestCase{
		Id:           "DirectoryOpenIntegration.Smoke",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/bar.cc", Line: 1},
		NameTokens:   []string{"directory", "open", "integration", "smoke"},
		ContractRefs: []string{"fuchsia.io.Directory.Open"},
	})
	idx := New(c, nil)
	idx.Build()

	p := c.Profile("fuchsia.io/Directory.Open")
	if p == nil || countTestRefs(p) != 1 {
		t.Errorf("expected exactly 1 test attribution via direct ref; got %+v", p)
	}
}

// TestBuild_KebabSubcommandJoinsConcatenatedDir verifies that a
// hyphenated cobra subcommand ID ("kubectl api-resources") joins
// the test that lives in the conventional concatenated-name source
// directory ("pkg/cmd/apiresources/apiresources_test.go"). Real
// regression — without joinNonLib fallback, the path-token check
// fails because tokensFromPath sees "apiresources" as a single
// token while the element splits into ["api","resources"].
// Under the SUBCOMMAND direct-evidence guard, "kubectl api-resources"
// no longer attributes via name-token + path overlap alone. Tests must
// carry a direct ref to the subcommand — the gotest adapter emits one
// when the test invokes the cobra plumbing (NewCmdX, exec.Command,
// SetArgs, struct args). This regression-tests that path.
func TestBuild_KebabSubcommandJoinsConcatenatedDir(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "kubectl api-resources",
		Kind:    contractpb.ContractElementKind_SUBCOMMAND,
		Library: "kubectl",
	})
	c.AddTest(&testcasepb.TestCase{
		Id:           "TestAPIResourcesRun",
		Framework:    "gotest",
		Location:     &commonpb.SourceLocation{Path: "staging/src/k8s.io/kubectl/pkg/cmd/apiresources/apiresources_test.go", Line: 1},
		NameTokens:   []string{"test", "api", "resources", "run"},
		ContractRefs: []string{"kubectl api-resources"},
	})
	idx := New(c, nil)
	idx.Build()
	p := c.Profile("kubectl api-resources")
	if p == nil || countTestRefs(p) == 0 {
		t.Errorf("expected test attribution for kubectl api-resources; got %+v", p)
	}
}

// TestBuild_MultiWordMethodTokenMatch verifies that methods whose
// names contain multiple CamelCase tokens (e.g. TakeScreenshot,
// CreateSession, RegisterBufferCollection) link to tests when the
// test's own tokens cover every method-name token — even though the
// concatenated lowercase form ("takescreenshot") never appears in
// the test's tokenized name. This regression-tests the
// testCaseRefsElement fix from issue #14.
// TestBuild_MultiWordMethodTokenMatch: under direct-evidence guard,
// METHOD elements with multi-word names still require ContractRef
// attribution. Replaces the older "multi-word name-token match" path
// which is no longer the operative rule.
func TestBuild_MultiWordMethodTokenMatch(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "fuchsia.ui.scenic/Scenic.TakeScreenshot",
		Kind:    contractpb.ContractElementKind_METHOD,
		Library: "fuchsia.ui.scenic",
		Aliases: []string{"fuchsia.ui.scenic.Scenic.TakeScreenshot"},
	})
	// Name-token-only test (no ContractRef): REFUSED.
	c.AddTest(&testcasepb.TestCase{
		Id:         "ScreenshotTest.TakeScreenshotReturnsImage",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "src/ui/scenic/tests/screenshot_test.cc", Line: 1},
		NameTokens: []string{"screenshot", "test", "take", "screenshot", "returns", "image"},
	})
	// Direct-ref test: attributes.
	c.AddTest(&testcasepb.TestCase{
		Id:           "ScenicScreenshotIntegration.Roundtrip",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/ui/scenic/tests/scenic_screenshot_test.cc", Line: 1},
		NameTokens:   []string{"scenic", "screenshot", "integration", "roundtrip"},
		ContractRefs: []string{"fuchsia.ui.scenic.Scenic.TakeScreenshot"},
	})
	idx := New(c, nil)
	idx.Build()

	p := c.Profile("fuchsia.ui.scenic/Scenic.TakeScreenshot")
	if p == nil || countTestRefs(p) != 1 {
		t.Errorf("expected exactly 1 test attribution via direct ref; got %+v", p)
	}
}

// TestBuild_MultiWordMethodTokenMissNoMatch verifies the negative
// case: when the test doesn't carry all method-name tokens it should
// NOT match. Without this guard, the fix would over-match.
func TestBuild_MultiWordMethodTokenMissNoMatch(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "fuchsia.ui.scenic/Scenic.TakeScreenshot",
		Kind:    contractpb.ContractElementKind_METHOD,
		Library: "fuchsia.ui.scenic",
	})
	// Test mentions "screenshot" + path has "scenic" but never "take"
	// → should not count.
	c.AddTest(&testcasepb.TestCase{
		Id:         "ScreenshotTest.ReturnsImage",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "src/ui/scenic/tests/screenshot_test.cc", Line: 1},
		NameTokens: []string{"screenshot", "test", "returns", "image"},
	})
	idx := New(c, nil)
	idx.Build()

	p := c.Profile("fuchsia.ui.scenic/Scenic.TakeScreenshot")
	if p != nil && countTestRefs(p) > 0 {
		t.Errorf("test missing the 'take' token should not match; got %d refs", countTestRefs(p))
	}
}

// TestBuild_NoisyWordsExtensible: under the broader direct-evidence
// guard for TYPE elements, the noisy-words sub-rule is no longer
// load-bearing — TYPE attribution always requires a direct ref. The
// Options.NoisyWords config is kept in the API (it still guards
// SUBCOMMAND / SERVICE / LIBRARY paths below) but doesn't affect
// TYPE attribution either way.
func TestBuild_NoisyWordsExtensible(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "acme.gear/Widget",
		Kind:    contractpb.ContractElementKind_TYPE,
		Library: "acme.gear",
		Aliases: []string{"acme.gear.Widget"},
	})
	// Name-token-only test: refused regardless of NoisyWords config.
	c.AddTest(&testcasepb.TestCase{
		Id:         "UnrelatedTest.UsesWidget",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "src/random/widget_helper_test.cc", Line: 1},
		NameTokens: []string{"unrelated", "test", "uses", "widget"},
	})

	for _, optName := range []string{"with-noisy-widget", "default"} {
		var idx *Indexer
		if optName == "with-noisy-widget" {
			idx = NewWithOptions(c, nil, Options{NoisyWords: []string{"widget"}})
		} else {
			idx = New(c, nil)
		}
		idx.Build()
		if got := countTestRefs(c.Profile("acme.gear/Widget")); got != 0 {
			t.Errorf("%s: TYPE element must not attribute via name-token; got %d refs", optName, got)
		}
	}

	// Direct-ref test attributes regardless.
	c2 := corpus.New()
	c2.AddElement(&contractpb.ContractElement{
		Id:      "acme.gear/Widget",
		Kind:    contractpb.ContractElementKind_TYPE,
		Library: "acme.gear",
		Aliases: []string{"acme.gear.Widget"},
	})
	c2.AddTest(&testcasepb.TestCase{
		Id:           "WidgetUserTest.Roundtrip",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/acme/gear/widget_test.cc", Line: 1},
		NameTokens:   []string{"widget", "user", "test", "roundtrip"},
		ContractRefs: []string{"acme.gear.Widget"},
	})
	idx2 := New(c2, nil)
	idx2.Build()
	if got := countTestRefs(c2.Profile("acme.gear/Widget")); got != 1 {
		t.Errorf("direct-ref test should attribute (1); got %d", got)
	}
}

// TestBuild_SingleWordTypeRequiresLibraryToken verifies that a
// type whose local name is a common English word ("Snapshot") only
// links to tests whose path/name also includes a distinctive token
// from the library (e.g. "scenic"). Without this, every test in the
// tree that says "snapshot" would attribute to fuchsia.ui.scenic.
// TestBuild_SingleWordTypeRequiresLibraryToken: under the broader
// direct-evidence guard, single-word TYPE leaves (Snapshot, Channel,
// Value, …) reject ALL name-token attribution — not just the cross-
// library noise the older guard targeted. Only direct refs attribute.
func TestBuild_SingleWordTypeRequiresLibraryToken(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "fuchsia.ui.scenic/Snapshot",
		Kind:    contractpb.ContractElementKind_TYPE,
		Library: "fuchsia.ui.scenic",
		Aliases: []string{"fuchsia.ui.scenic.Snapshot"},
	})
	// Unrelated test in diagnostics — REFUSED (no direct ref).
	c.AddTest(&testcasepb.TestCase{
		Id:         "ArchiveTest.TakeSnapshot",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "src/diagnostics/archivist/tests/archive_test.cc", Line: 1},
		NameTokens: []string{"archive", "test", "take", "snapshot"},
	})
	// Scenic-dir test by name-token alone — also REFUSED. The older
	// guard would have admitted this; the broader policy does not.
	c.AddTest(&testcasepb.TestCase{
		Id:         "ScenicSnapshotTest.Roundtrip",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "src/ui/scenic/tests/snapshot_test.cc", Line: 1},
		NameTokens: []string{"scenic", "snapshot", "test", "roundtrip"},
	})
	// Direct-ref test: attributes.
	c.AddTest(&testcasepb.TestCase{
		Id:           "ScenicSnapshotIntegration.RoundtripDirect",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/ui/scenic/tests/snapshot_integration_test.cc", Line: 1},
		NameTokens:   []string{"scenic", "snapshot", "integration", "roundtrip", "direct"},
		ContractRefs: []string{"fuchsia.ui.scenic.Snapshot"},
	})
	idx := New(c, nil)
	idx.Build()

	p := c.Profile("fuchsia.ui.scenic/Snapshot")
	if p == nil {
		t.Fatal("no profile")
	}
	if got := countTestRefs(p); got != 1 {
		t.Errorf("expected exactly 1 ref (the direct-ref test); got %d", got)
	}
}

// TestBuild_ComposedMethodInheritsDocClaim verifies fix #2: a method
// surfaced via FIDL `compose` inheritance picks up the original
// method's DocClaim. Without this, the inherited surface element
// shows "TESTED_UNDOCUMENTED" even when the doc clearly exists on
// the parent protocol — confirmed in the fuchsia.ui.composition audit
// for Flatland.ReleaseImageImmediately (composed from TrustedFlatland).
func TestBuild_ComposedMethodInheritsDocClaim(t *testing.T) {
	c := corpus.New()
	// Parent protocol with a documented method.
	c.AddElement(&contractpb.ContractElement{
		Id:                "demo.lib/Parent",
		Kind:              contractpb.ContractElementKind_PROTOCOL,
		Library:           "demo.lib",
		DocCommentExcerpt: "The parent protocol.",
	})
	c.AddElement(&contractpb.ContractElement{
		Id:                "demo.lib/Parent.Heavy",
		Kind:              contractpb.ContractElementKind_METHOD,
		Library:           "demo.lib",
		DocCommentExcerpt: "Heavy method with real prose: explains errors, rights, edge cases.",
		Location:          &commonpb.SourceLocation{Path: "demo.fidl", Line: 10},
	})
	// Child protocol composing the parent — the indexer synthesizes
	// Child.Heavy as an inherited surface method.
	c.AddElement(&contractpb.ContractElement{
		Id:      "demo.lib/Child",
		Kind:    contractpb.ContractElementKind_PROTOCOL,
		Library: "demo.lib",
		Relationships: []*contractpb.Relationship{{
			Kind:            contractpb.RelationshipKind_COMPOSED_FROM,
			TargetElementId: "demo.lib/Parent",
		}},
	})

	// DocClaim targeting the original (Parent.Heavy), as the FIDL
	// adapter emits at scan time.
	c.AddDocClaim(&docclaimpb.DocClaim{
		SourcePath:   "demo.fidl",
		Location:     &commonpb.SourceLocation{Path: "demo.fidl", Line: 10},
		RawText:      "Heavy method with real prose: explains errors, rights, edge cases.",
		Kind:         docclaimpb.DocClaimKind_REFERENCE,
		Adapter:      "fidl",
		ContractRefs: []string{"demo.lib/Parent.Heavy"},
	})

	idx := New(c, nil)
	idx.Build()

	// The synthesized Child.Heavy should now carry a doc ref too.
	prof := c.Profile("demo.lib/Child.Heavy")
	if prof == nil {
		t.Fatal("no profile for synthesized Child.Heavy")
	}
	refs := prof.GetDocs().GetReference()
	n := len(refs.GetFidldoc()) + len(refs.GetClidoc()) + len(refs.GetDockerdoc())
	for _, list := range refs.GetByAdapter() {
		n += len(list.GetRefs())
	}
	if n == 0 {
		t.Errorf("expected Child.Heavy to inherit doc claim from Parent.Heavy; got 0 refs")
	}
}

// TestBuild_WildcardRefFansOutToProtocol verifies fix #3: a test's
// ContractRef in the form "lib/*.Method" attributes to any element
// in that library whose ID ends in ".Method". Critical for catching
// the "var name doesn't equal protocol name" pattern in integration
// tests.
func TestBuild_WildcardRefFansOutToProtocol(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "demo.lib/Flatland", Kind: contractpb.ContractElementKind_PROTOCOL, Library: "demo.lib",
	})
	c.AddElement(&contractpb.ContractElement{
		Id:   "demo.lib/Flatland.SetInfiniteHitRegion",
		Kind: contractpb.ContractElementKind_METHOD, Library: "demo.lib",
	})
	c.AddTest(&testcasepb.TestCase{
		Id:         "TouchTest.Foo",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "src/touch_test.cc", Line: 1},
		NameTokens: []string{"touch", "test", "foo"},
		// The wildcard form — what fidlmatch now emits when the var
		// name (parent_session_) doesn't equal a real protocol.
		ContractRefs: []string{"demo.lib/*.SetInfiniteHitRegion"},
	})
	idx := New(c, nil)
	idx.Build()

	if got := countTestRefs(c.Profile("demo.lib/Flatland.SetInfiniteHitRegion")); got != 1 {
		t.Errorf("expected wildcard ref to attribute to Flatland.SetInfiniteHitRegion; got %d refs", got)
	}
}

// Verifies wildcard does NOT cross library boundaries.
func TestBuild_WildcardRefStaysInLibrary(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "other.lib/Other.Method", Kind: contractpb.ContractElementKind_METHOD, Library: "other.lib",
	})
	c.AddTest(&testcasepb.TestCase{
		Id: "T.X", Framework: "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/x_test.cc", Line: 1},
		NameTokens:   []string{"t", "x"},
		ContractRefs: []string{"demo.lib/*.Method"}, // demo.lib, not other.lib
	})
	idx := New(c, nil)
	idx.Build()
	if got := countTestRefs(c.Profile("other.lib/Other.Method")); got != 0 {
		t.Errorf("wildcard should not cross libraries; got %d refs on other.lib element", got)
	}
}

// --- gaps summary ---

func TestBuild_GapsSummaryMissingTests(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:   "fuchsia.io/Directory.Open",
		Kind: contractpb.ContractElementKind_METHOD,
	})
	idx := New(c, nil)
	idx.Build()

	p := c.Profile("fuchsia.io/Directory.Open")
	if p == nil {
		t.Fatal("no profile")
	}
	found := make(map[string]bool)
	for _, m := range p.GetGapsSummary().GetMissing() {
		found[m] = true
	}
	if !found["tests"] || !found["docs.reference"] || !found["examples"] {
		t.Errorf("expected missing tests + docs.reference + examples; got %v", found)
	}
}

// --- categorizer integration ---

func TestBuild_CategorizerRoutesTestsByPath(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "fuchsia.io/File.Read",
		Kind:    contractpb.ContractElementKind_METHOD,
		Library: "fuchsia.io",
		Aliases: []string{"fuchsia.io.File.Read"},
	})
	// Integration test — attributes via direct ref.
	c.AddTest(&testcasepb.TestCase{
		Id:           "FileTest.ReadEndToEnd",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/integration/file_tests.cc", Line: 1},
		NameTokens:   []string{"file", "test", "read", "end", "to", "end"},
		ContractRefs: []string{"fuchsia.io.File.Read"},
	})
	// Unit test — attributes via direct ref.
	c.AddTest(&testcasepb.TestCase{
		Id:           "FileTest.ReadBasic",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "src/unit/file_tests.cc", Line: 1},
		NameTokens:   []string{"file", "test", "read", "basic"},
		ContractRefs: []string{"fuchsia.io.File.Read"},
	})

	cat := mkCategorizer(t,
		&categorizationpb.Category{DottedPath: "tests.unit_tests", Paths: []string{"src/unit/**"}},
		&categorizationpb.Category{DottedPath: "tests.integration_tests", Paths: []string{"src/integration/**"}},
	)
	idx := New(c, cat)
	idx.Build()

	p := c.Profile("fuchsia.io/File.Read")
	if len(p.Tests.Unit) != 1 || p.Tests.Unit[0].GetTestName() != "FileTest.ReadBasic" {
		t.Errorf("unit bucket wrong: %+v", p.Tests.Unit)
	}
	if len(p.Tests.Integration) != 1 || p.Tests.Integration[0].GetTestName() != "FileTest.ReadEndToEnd" {
		t.Errorf("integration bucket wrong: %+v", p.Tests.Integration)
	}
}

// --- end-to-end small corpus ---

func TestBuildWithStats_SmallCorpus(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Directory", Kind: contractpb.ContractElementKind_PROTOCOL,
		Library: "fuchsia.io", Aliases: []string{"fuchsia.io.Directory"},
	})
	c.AddElement(&contractpb.ContractElement{
		Id: "fuchsia.io/Directory.Open", Kind: contractpb.ContractElementKind_METHOD,
		Library: "fuchsia.io", Aliases: []string{"fuchsia.io.Directory.Open"},
	})
	c.AddDocClaim(&docclaimpb.DocClaim{
		SourcePath: "x.fidl", Location: &commonpb.SourceLocation{Path: "x.fidl", Line: 1},
		ContractRefs: []string{"fuchsia.io/Directory.Open"},
		Kind:         docclaimpb.DocClaimKind_REFERENCE, Adapter: "fidl",
		Substance: commonpb.Substance_SUBSTANTIVE, WordCount: 50,
	})
	c.AddTest(&testcasepb.TestCase{
		Id: "DirectoryTest.OpenWorks", Framework: "gtest",
		Location:     &commonpb.SourceLocation{Path: "test.cc", Line: 1},
		NameTokens:   []string{"directory", "test", "open", "works"},
		ContractRefs: []string{"fuchsia.io.Directory.Open"},
	})
	idx := New(c, nil)
	stats := idx.BuildWithStats()

	if stats.Elements < 2 || stats.ProfilesBuilt < 2 {
		t.Errorf("stats elements/profiles = %d/%d", stats.Elements, stats.ProfilesBuilt)
	}
	if stats.TestRefsByElement == 0 {
		t.Errorf("expected at least one test ref attribution")
	}
	if stats.DocRefsByElement == 0 {
		t.Errorf("expected at least one doc ref attribution")
	}
}

// TestBuild_MethodNameTokenAttributionRequiresDirectRef verifies the
// METHOD-element direct-evidence guard: name-token overlap alone — even
// with a perfectly matching plural-form rpc name in the test — never
// attributes. Only an explicit ContractRef does. Replaces the older
// "ambiguous last token stem suppression" test, which encoded the
// finer-grained stemming sub-rule that the broader direct-evidence
// policy now supersedes.
func TestBuild_MethodNameTokenAttributionRequiresDirectRef(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "grpc.channelz.v1/Channelz", Kind: contractpb.ContractElementKind_PROTOCOL,
		Library: "grpc.channelz.v1", Ecosystem: "proto",
	})
	c.AddElement(&contractpb.ContractElement{
		Id: "grpc.channelz.v1/Channelz.GetChannel", Kind: contractpb.ContractElementKind_METHOD,
		Library: "grpc.channelz.v1", Ecosystem: "proto",
	})
	c.AddElement(&contractpb.ContractElement{
		Id:      "grpc.channelz.v1/Channelz.GetTopChannels",
		Kind:    contractpb.ContractElementKind_METHOD,
		Library: "grpc.channelz.v1", Ecosystem: "proto",
		Aliases: []string{"grpc.channelz.v1.Channelz.GetTopChannels"},
	})
	// Name-token-only test: tokens overlap with BOTH method names
	// (Channelz, GetTopChannels, GetChannel via stemming), but no
	// ContractRef. Direct-evidence guard refuses both.
	c.AddTest(&testcasepb.TestCase{
		Id:         "ChannelzRegistryBasedTest.GetTopChannelsPagination",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "test/core/channelz/channelz_test.cc", Line: 537},
		NameTokens: []string{"channelz", "registry", "based", "test", "get", "top", "channels", "pagination"},
	})
	// Direct-ref test: explicit ContractRef matches GetTopChannels'
	// alias → Strategy 1 attributes only that element.
	c.AddTest(&testcasepb.TestCase{
		Id:           "ChannelzServiceTest.PaginationViaStub",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "test/cpp/end2end/channelz_service_test.cc", Line: 200},
		NameTokens:   []string{"channelz", "service", "test", "pagination", "via", "stub"},
		ContractRefs: []string{"grpc.channelz.v1.Channelz.GetTopChannels"},
	})

	idx := New(c, nil)
	idx.Build()

	if got := countTestRefs(c.Profile("grpc.channelz.v1/Channelz.GetTopChannels")); got != 1 {
		t.Errorf("plural-form element should attribute only via direct ref (1), got %d", got)
	}
	if got := countTestRefs(c.Profile("grpc.channelz.v1/Channelz.GetChannel")); got != 0 {
		t.Errorf("singular-form element must not name-token-bleed, got %d", got)
	}
}

// TestBuild_MethodAttributionAcrossEcosystems verifies that the
// direct-evidence guard applies to METHOD elements regardless of
// ecosystem — proto AND fidl methods both refuse name-token-only
// attribution. Replaces the older proto-specific leaf-exact-match
// test, which is no longer the operative rule: the broader guard
// supersedes it for both ecosystems.
func TestBuild_MethodAttributionAcrossEcosystems(t *testing.T) {
	for _, eco := range []string{"proto", "fidl"} {
		t.Run(eco, func(t *testing.T) {
			c := corpus.New()
			c.AddElement(&contractpb.ContractElement{
				Id: "grpc.health.v1/Health", Kind: contractpb.ContractElementKind_PROTOCOL,
				Library: "grpc.health.v1", Ecosystem: eco,
			})
			c.AddElement(&contractpb.ContractElement{
				Id:      "grpc.health.v1/Health.Check",
				Kind:    contractpb.ContractElementKind_METHOD,
				Library: "grpc.health.v1", Ecosystem: eco,
				Aliases: []string{"grpc.health.v1.Health.Check"},
			})
			// Name-token-only test (no ContractRef): must NOT attribute,
			// regardless of stemming or any sub-rule that used to fire.
			c.AddTest(&testcasepb.TestCase{
				Id:         "HealthChecksFixture.RunsClean",
				Framework:  "gtest",
				Location:   &commonpb.SourceLocation{Path: "test/health_checks_test.cc", Line: 10},
				NameTokens: []string{"health", "checks", "fixture", "runs", "clean"},
			})
			// Direct-ref test: explicit alias hit → attributes.
			c.AddTest(&testcasepb.TestCase{
				Id:           "HealthClientIntegration.Probe",
				Framework:    "gtest",
				Location:     &commonpb.SourceLocation{Path: "test/cpp/health_probe_test.cc", Line: 42},
				NameTokens:   []string{"health", "client", "integration", "probe"},
				ContractRefs: []string{"grpc.health.v1.Health.Check"},
			})

			idx := New(c, nil)
			idx.Build()

			if got := countTestRefs(c.Profile("grpc.health.v1/Health.Check")); got != 1 {
				t.Errorf("%s METHOD should attribute only via direct ref (1), got %d", eco, got)
			}
		})
	}
}

// TestBuild_ProtocolLeafEqualsLibraryDistinctive covers Fix 2: a
// PROTOCOL element whose only local-name token matches the library's
// distinctive segment ("Channelz" service in library "grpc.channelz.v1")
// cannot be attributed via name-token overlap. Tests that DO reference
// it via ContractRefs (Strategy 1) still attribute.
func TestBuild_ProtocolLeafEqualsLibraryDistinctive(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "grpc.channelz.v1/Channelz", Kind: contractpb.ContractElementKind_PROTOCOL,
		Library: "grpc.channelz.v1", Ecosystem: "proto",
		Aliases: []string{"grpc.channelz.v1.Channelz", "Channelz"},
	})
	// Token-only match: name has "channelz" but no body refs. Under
	// Fix 2 this must NOT attribute, even though "channelz" is in
	// the test path.
	c.AddTest(&testcasepb.TestCase{
		Id:         "ChannelTracerTest.BasicTest",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "test/core/channelz/channel_trace_test.cc", Line: 158},
		NameTokens: []string{"channel", "tracer", "test", "basic", "test"},
	})
	// Body-ref match: explicit "Channelz" alias in ContractRefs.
	// This should attribute via Strategy 1.
	c.AddTest(&testcasepb.TestCase{
		Id:           "ChannelzServerTest.SuccessfulRequestTest",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "test/cpp/end2end/channelz_service_test.cc", Line: 391},
		NameTokens:   []string{"channelz", "server", "test", "successful", "request", "test"},
		ContractRefs: []string{"Channelz"},
	})

	idx := New(c, nil)
	idx.Build()

	refs := countTestRefs(c.Profile("grpc.channelz.v1/Channelz"))
	if refs != 1 {
		t.Errorf("Channelz SERVICE should attribute only via body-ref (1), got %d", refs)
	}
}

// TestBuild_TypeAttributionRequiresDirectRef verifies that TYPE
// elements (e.g. proto messages, FIDL types) refuse name-token-only
// attribution even when the test name perfectly tiles the type's
// leaf tokens. Replaces the older multi-token-noisy-word-guard test,
// which encoded a finer-grained guard the direct-evidence policy
// now supersedes for TYPE.
func TestBuild_TypeAttributionRequiresDirectRef(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "grpc.channelz.v1/ServerData",
		Kind:    contractpb.ContractElementKind_TYPE,
		Library: "grpc.channelz.v1", Ecosystem: "proto",
		Aliases: []string{"grpc.channelz.v1.ServerData"},
	})
	// Name-token-only test (channelz + server + data all present in
	// tokens, no ContractRef): direct-evidence guard refuses.
	c.AddTest(&testcasepb.TestCase{
		Id:         "ChannelzServerDataTest.Basic",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "test/core/channelz/server_data_test.cc", Line: 1},
		NameTokens: []string{"channelz", "server", "data", "test", "basic"},
	})
	// Direct-ref test: explicit alias → attributes.
	c.AddTest(&testcasepb.TestCase{
		Id:           "ServerDataParseTest.FromWire",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "test/cpp/parse_test.cc", Line: 5},
		NameTokens:   []string{"server", "data", "parse", "test", "from", "wire"},
		ContractRefs: []string{"grpc.channelz.v1.ServerData"},
	})

	idx := New(c, nil)
	idx.Build()

	if refs := countTestRefs(c.Profile("grpc.channelz.v1/ServerData")); refs != 1 {
		t.Errorf("TYPE should attribute only via direct ref (1), got %d", refs)
	}
}

// TestBuild_PrefixCollisionDirectEvidence verifies that the
// Channel-vs-ChannelTrace prefix-collision class — once handled by a
// dedicated tightening — is now subsumed by the direct-evidence guard.
// Neither element attributes via name-token overlap; only direct refs
// route to their specific element. Replaces three older sibling-
// collision tests (intra-library prefer-longer, intra-library
// short-leaf-survives, cross-library no-suppress) — under the broader
// policy all three collapse to the same expectation.
func TestBuild_PrefixCollisionDirectEvidence(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "grpc.channelz.v1/Channel",
		Kind:    contractpb.ContractElementKind_TYPE,
		Library: "grpc.channelz.v1", Ecosystem: "proto",
		Aliases: []string{"grpc.channelz.v1.Channel"},
	})
	c.AddElement(&contractpb.ContractElement{
		Id:      "grpc.channelz.v1/ChannelTrace",
		Kind:    contractpb.ContractElementKind_TYPE,
		Library: "grpc.channelz.v1", Ecosystem: "proto",
		Aliases: []string{"grpc.channelz.v1.ChannelTrace"},
	})
	// Name-token-only test: tokens contain "channel" and "trace".
	// Under the old prefix-collision sub-rule the longer sibling
	// would have attributed. Under the direct-evidence guard,
	// neither does.
	c.AddTest(&testcasepb.TestCase{
		Id:         "ChannelTracerTest.BasicTest",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "test/core/channelz/channel_trace_test.cc", Line: 158},
		NameTokens: []string{"channel", "tracer", "test", "basic", "test"},
	})
	// Direct-ref test for the short leaf: only Channel attributes.
	c.AddTest(&testcasepb.TestCase{
		Id:           "ChannelzChannelTest.BasicChannelMessage",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "test/core/channelz/channel_message_test.cc", Line: 1},
		NameTokens:   []string{"channelz", "channel", "test", "basic", "message"},
		ContractRefs: []string{"grpc.channelz.v1.Channel"},
	})
	// Direct-ref test for the long leaf: only ChannelTrace attributes.
	c.AddTest(&testcasepb.TestCase{
		Id:           "ChannelTraceMessageTest.Serializes",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "test/core/channelz/channel_trace_test.cc", Line: 200},
		NameTokens:   []string{"channel", "trace", "message", "test", "serializes"},
		ContractRefs: []string{"grpc.channelz.v1.ChannelTrace"},
	})

	idx := New(c, nil)
	idx.Build()

	if refs := countTestRefs(c.Profile("grpc.channelz.v1/Channel")); refs != 1 {
		t.Errorf("Channel should attribute only via direct ref (1), got %d", refs)
	}
	if refs := countTestRefs(c.Profile("grpc.channelz.v1/ChannelTrace")); refs != 1 {
		t.Errorf("ChannelTrace should attribute only via direct ref (1), got %d", refs)
	}
}

// TestBuild_TypeAttributionCrossLibraryIsolated verifies that TYPE
// attribution is fully library-scoped: a test that name-tokens
// libB's FrobNicate does NOT bleed onto libA's Frob, and vice versa
// — the direct-evidence guard refuses both because neither test
// carries a ContractRef. With direct refs each test attributes only
// to its own library's element.
func TestBuild_TypeAttributionCrossLibraryIsolated(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id:      "libA/Frob",
		Kind:    contractpb.ContractElementKind_TYPE,
		Library: "libA", Ecosystem: "proto",
		Aliases: []string{"libA.Frob"},
	})
	c.AddElement(&contractpb.ContractElement{
		Id:      "libB/FrobNicate",
		Kind:    contractpb.ContractElementKind_TYPE,
		Library: "libB", Ecosystem: "proto",
		Aliases: []string{"libB.FrobNicate"},
	})
	// Name-token-only test: refuses both.
	c.AddTest(&testcasepb.TestCase{
		Id:         "FrobNicateTest.Basic",
		Framework:  "gtest",
		Location:   &commonpb.SourceLocation{Path: "test/frob_nicate_test.cc", Line: 1},
		NameTokens: []string{"frob", "nicate", "test", "basic"},
	})
	// Direct-ref tests, one per library.
	c.AddTest(&testcasepb.TestCase{
		Id:           "FrobUserTest.Basic",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "test/libA/frob_user_test.cc", Line: 1},
		NameTokens:   []string{"frob", "user", "test", "basic"},
		ContractRefs: []string{"libA.Frob"},
	})
	c.AddTest(&testcasepb.TestCase{
		Id:           "FrobNicateUserTest.Basic",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "test/libB/frob_nicate_user_test.cc", Line: 1},
		NameTokens:   []string{"frob", "nicate", "user", "test", "basic"},
		ContractRefs: []string{"libB.FrobNicate"},
	})

	idx := New(c, nil)
	idx.Build()

	if refs := countTestRefs(c.Profile("libA/Frob")); refs != 1 {
		t.Errorf("libA/Frob should attribute only via direct ref (1), got %d", refs)
	}
	if refs := countTestRefs(c.Profile("libB/FrobNicate")); refs != 1 {
		t.Errorf("libB/FrobNicate should attribute only via direct ref (1), got %d", refs)
	}
}

// --- SAME_AS / codegen_bridge end-to-end ---

// codegen_bridge wired into the indexer must (a) materialize
// bidirectional SAME_AS edges between matched cross-ecosystem
// elements and (b) union evidence across siblings so a test
// attributed to one side shows up on the other side's CoverageProfile.
//
// proto↔fidl stands in for proto↔cpp here because both adapters
// already exist; the bridge mechanism is ecosystem-agnostic.
func TestBuild_SameAsBridgeUnionsEvidence(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "pw.log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
		Ecosystem: "proto", Library: "pw.log",
	})
	// Sibling on the "fidl" side standing in for cpp. The bridge
	// template renders to "pw/log/LogEntry" via PackageSlash.
	c.AddElement(&contractpb.ContractElement{
		Id: "pw/log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
		Ecosystem: "fidl", Library: "pw.log",
	})

	// One test per side, each via direct ContractRef (Strategy 1).
	c.AddTest(&testcasepb.TestCase{
		Id:           "ProtoLogEntryRoundTrip",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "proto/log_entry_test.cc", Line: 1},
		NameTokens:   []string{"proto", "log", "entry", "round", "trip"},
		ContractRefs: []string{"pw.log/LogEntry"},
	})
	c.AddTest(&testcasepb.TestCase{
		Id:           "FidlLogEntrySerializes",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "fidl/log_entry_test.cc", Line: 1},
		NameTokens:   []string{"fidl", "log", "entry", "serializes"},
		ContractRefs: []string{"pw/log/LogEntry"},
	})

	bridges := makeBridges(t, &configpb.CodegenBridge{
		SourceEcosystem:    "proto",
		TargetEcosystem:    "fidl",
		TargetNameTemplate: "{{.PackageSlash}}/{{.Name}}",
	})
	idx := NewWithOptions(c, nil, Options{CodegenBridges: bridges})
	idx.Build()

	pProto := c.Profile("pw.log/LogEntry")
	pFidl := c.Profile("pw/log/LogEntry")
	if pProto == nil || pFidl == nil {
		t.Fatalf("missing profile (proto=%v fidl=%v)", pProto, pFidl)
	}
	if got := countTestRefs(pProto); got != 2 {
		t.Errorf("proto profile: want 2 test refs after SAME_AS union; got %d", got)
	}
	if got := countTestRefs(pFidl); got != 2 {
		t.Errorf("fidl profile: want 2 test refs after SAME_AS union; got %d", got)
	}

	protoE := c.Element("pw.log/LogEntry")
	fidlE := c.Element("pw/log/LogEntry")
	hasSameAs := func(e *contractpb.ContractElement, targetID string) bool {
		for _, r := range e.GetRelationships() {
			if r.GetKind() == contractpb.RelationshipKind_SAME_AS && r.GetTargetElementId() == targetID {
				return true
			}
		}
		return false
	}
	if !hasSameAs(protoE, "pw/log/LogEntry") {
		t.Errorf("proto element missing SAME_AS->fidl; rels=%+v", protoE.GetRelationships())
	}
	if !hasSameAs(fidlE, "pw.log/LogEntry") {
		t.Errorf("fidl element missing SAME_AS->proto; rels=%+v", fidlE.GetRelationships())
	}
	// Gaps must reflect the aggregated view. Both have tests via
	// their sibling; neither should still report 'tests' missing.
	for _, m := range pProto.GetGapsSummary().GetMissing() {
		if m == "tests" {
			t.Errorf("proto: gaps still reports 'tests' missing after sibling aggregation")
		}
	}
	for _, m := range pFidl.GetGapsSummary().GetMissing() {
		if m == "tests" {
			t.Errorf("fidl: gaps still reports 'tests' missing after sibling aggregation")
		}
	}
}

// Same fixture without any codegen_bridge configured: no SAME_AS edges
// emerge and no cross-aggregation happens. Catches the regression of
// "bridge logic always runs even when unconfigured."
func TestBuild_NoBridgeNoSameAsNoUnion(t *testing.T) {
	c := corpus.New()
	c.AddElement(&contractpb.ContractElement{
		Id: "pw.log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
		Ecosystem: "proto", Library: "pw.log",
	})
	c.AddElement(&contractpb.ContractElement{
		Id: "pw/log/LogEntry", Kind: contractpb.ContractElementKind_TYPE,
		Ecosystem: "fidl", Library: "pw.log",
	})
	c.AddTest(&testcasepb.TestCase{
		Id:           "ProtoLogEntryRoundTrip",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "proto/log_entry_test.cc", Line: 1},
		NameTokens:   []string{"proto", "log", "entry", "round", "trip"},
		ContractRefs: []string{"pw.log/LogEntry"},
	})
	c.AddTest(&testcasepb.TestCase{
		Id:           "FidlLogEntrySerializes",
		Framework:    "gtest",
		Location:     &commonpb.SourceLocation{Path: "fidl/log_entry_test.cc", Line: 1},
		NameTokens:   []string{"fidl", "log", "entry", "serializes"},
		ContractRefs: []string{"pw/log/LogEntry"},
	})

	idx := New(c, nil) // no bridges
	idx.Build()

	pProto := c.Profile("pw.log/LogEntry")
	pFidl := c.Profile("pw/log/LogEntry")
	if countTestRefs(pProto) != 1 {
		t.Errorf("proto profile: want 1 test ref (no union); got %d", countTestRefs(pProto))
	}
	if countTestRefs(pFidl) != 1 {
		t.Errorf("fidl profile: want 1 test ref (no union); got %d", countTestRefs(pFidl))
	}
	for _, e := range []*contractpb.ContractElement{c.Element("pw.log/LogEntry"), c.Element("pw/log/LogEntry")} {
		for _, r := range e.GetRelationships() {
			if r.GetKind() == contractpb.RelationshipKind_SAME_AS {
				t.Errorf("%s: unexpected SAME_AS edge with no bridge configured: %+v", e.GetId(), r)
			}
		}
	}
}
