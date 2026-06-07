package conceptdoc

import (
	"testing"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// This file pins the precision fixes from the Phase 3 precision audit.
// Each false-positive CLASS the
// audit found is locked as a "must NOT attribute" test; the genuine signals
// are locked as "must attribute". The fixes live in
// internal/grounding/anchored.go (the concept-doc attribution gate) and are
// exercised end-to-end here through Detect.
//
//   Fix 1 — a BARE-PROSE anchor attributes only on the element's FULL
//           multi-word name written out; a single-word name (any length) and
//           a bare common-noun tail never attribute from prose.
//   Fix 2 — a BACKTICKED/QUALIFIED anchor attributes only when the token IS
//           the element's own identifier (whole, or final dotted/slash
//           segment, in PascalCase) AND, for a library-prefixed FQN, the
//           library matches.

// covered reports whether the element with the given ID is covered in res.
func covered(res *Result, id string) (ElementCoverage, bool) {
	for _, e := range res.Elements {
		if e.ElementID == id {
			return e, e.Covered
		}
	}
	return ElementCoverage{}, false
}

// mustNotAttribute fails if any element is covered (the doc should produce no
// attribution at all for the supplied prose).
func mustNotAttribute(t *testing.T, res *Result, why string) {
	t.Helper()
	for _, e := range res.Elements {
		if e.Covered {
			t.Fatalf("%s: element %q wrongly attributed (claims=%d, docs=%v); excerpt(s)=%s",
				why, e.ElementID, e.ClaimCount, e.DocPaths, firstExcerpt(e))
		}
	}
}

func firstExcerpt(e ElementCoverage) string {
	if len(e.Claims) > 0 {
		return e.Claims[0].GetRawText()
	}
	return "(none)"
}

// detectWith runs Detect over one in-memory doc against the given elements,
// with library="" (the wired multi-FIDL-library default the fuchsia domain
// configs use).
func detectWith(elems []*contractpb.ContractElement, path, body string) *Result {
	return Detect(Options{Library: "", Elements: elems, Docs: []Doc{doc(path, body)}})
}

func elem(id, kind, library string) *contractpb.ContractElement {
	var k contractpb.ContractElementKind
	switch kind {
	case "PROTOCOL":
		k = contractpb.ContractElementKind_PROTOCOL
	case "TYPE":
		k = contractpb.ContractElementKind_TYPE
	default:
		k = contractpb.ContractElementKind_PROTOCOL
	}
	return &contractpb.ContractElement{Id: id, Kind: k, Library: library}
}

// ---------------------------------------------------------------------------
// FP Class A — common-English exact name in lowercase prose (single-word).
// ---------------------------------------------------------------------------

func TestFP_CommonEnglishExactName_LowercaseProse(t *testing.T) {
	const dl = "fuchsia.component.decl"
	const cl = "fuchsia.component"
	const dt = "fuchsia.driver.test"
	const dfw = "fuchsia.driver.framework"
	elems := []*contractpb.ContractElement{
		elem(dl+"/Use", "TYPE", dl),
		elem(cl+"/Error", "TYPE", cl),
		elem(dfw+"/Condition", "TYPE", dfw),
		elem(dt+"/Expose", "TYPE", dt),
	}
	cases := []struct {
		name, body string
	}{
		{"use-verb", "# Tests\n\nWe can use the test suite described in US NIST SP800-90B.\n"},
		{"error-noun", "# Errors\n\nExample: CRC or Parity error.\n"},
		{"conditions", "# FAQ\n\nThis helps you identify special conditions or edge cases.\n"},
		{"expose-keyword", "# Comm\n\nAnd the expose specifies the source of the capability.\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/concepts/x.md", c.body)
			mustNotAttribute(t, res, "single-word common-English exact name in lowercase prose")
		})
	}
}

// ---------------------------------------------------------------------------
// FP Class A' — single-word PascalCase in bare prose, incl. long dictionary
// words (Configuration, Presentation, Flatland). Length/stoplist must NOT
// rescue these — Fix 1 rejects every single-word bare-prose mention.
// ---------------------------------------------------------------------------

func TestFP_SingleWordPascalCase_BareProse(t *testing.T) {
	const cl = "fuchsia.component"
	const ui = "fuchsia.ui.composition"
	elems := []*contractpb.ContractElement{
		elem(cl+"/Configuration", "TYPE", cl),
		elem(ui+"/Presentation", "TYPE", ui),
		elem(ui+"/Flatland", "PROTOCOL", ui),
	}
	cases := []struct{ name, body string }{
		// Capitalized mid-sentence (the author wrote the word, not the type).
		{"configuration", "# Setup\n\nThe Configuration of the system is stored on disk.\n"},
		{"presentation", "# UI\n\nThe Presentation layer renders the scene each frame.\n"},
		{"flatland", "# Graphics\n\nFlatland is the name of the 2D composition API.\n"},
		// Sentence-initial capitalization of a long dictionary word.
		{"configuration-initial", "# Setup\n\nConfiguration files live under /config.\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/concepts/x.md", c.body)
			mustNotAttribute(t, res, "single-word PascalCase element in bare prose")
		})
	}
}

// ---------------------------------------------------------------------------
// FP Class B — bare common-noun tail must not light up several same-tailed
// multi-word elements. "Socket Proxy ... sockets" must attribute NONE of the
// six Socket types.
// ---------------------------------------------------------------------------

func TestFP_BareTail_CrossElement(t *testing.T) {
	const ps = "fuchsia.posix.socket"
	const psp = "fuchsia.posix.socket.packet"
	const psr = "fuchsia.posix.socket.raw"
	elems := []*contractpb.ContractElement{
		elem(psp+"/Socket", "PROTOCOL", psp),
		elem(ps+"/DatagramSocket", "PROTOCOL", ps),
		elem(ps+"/StreamSocket", "PROTOCOL", ps),
		elem(ps+"/BaseSocket", "PROTOCOL", ps),
		elem(psr+"/Socket", "PROTOCOL", psr),
	}
	body := "# Policy\n\nSocket Proxy: A component for tracking flows and assigning an appropriate mark to sockets.\n"
	res := detectWith(elems, "docs/development/networking/policy/overview.md", body)
	mustNotAttribute(t, res, "bare tail 'sockets' must not attribute any Socket-tailed element")
}

// A bare multi-word *tail phrase* that is NOT the element's full name must not
// attribute either: "node manager" must not attribute CompositeNodeManager
// (its full name is "composite node manager").
func TestFP_PartialMultiWordTail_BareProse(t *testing.T) {
	res := detect(doc("docs/concepts/drivers/x.md",
		"# Drivers\n\nThe node manager keeps the topology consistent.\n"))
	if _, ok := covered(res, lib+"/CompositeNodeManager"); ok {
		t.Fatalf("partial tail phrase 'node manager' must NOT attribute CompositeNodeManager")
	}
}

// A bold/emphasized single common word must attribute ONLY the element whose
// whole name is that word — never every same-tailed multi-word element. The
// audit's Class B leaking through the defined-term anchor: **Driver** must not
// light up Manager.DisableDriver / NodeController.WaitForDriver / DriverHost.
func TestFP_DefinedTermBareTail_CrossElement(t *testing.T) {
	const dh = "fuchsia.driver.host"
	const dd = "fuchsia.driver.development"
	const dfw = "fuchsia.driver.framework"
	elems := []*contractpb.ContractElement{
		elem(dh+"/Driver", "PROTOCOL", dh),                     // whole name == "Driver"
		elem(dd+"/Manager.DisableDriver", "TYPE", dd),          // tail "Driver"
		elem(dfw+"/NodeController.WaitForDriver", "TYPE", dfw), // tail "Driver"
		elem(dh+"/DriverHost", "PROTOCOL", dh),                 // tail "Driver"? no — head; not "Driver"
	}
	body := "# Glossary\n\n**Driver**: the unit of execution bound to a node.\n"
	res := detectWith(elems, "docs/glossary/README.md", body)
	// Only fuchsia.driver.host/Driver may be covered.
	for _, e := range res.Elements {
		if e.Covered && e.ElementID != dh+"/Driver" {
			t.Fatalf("bold **Driver** wrongly attributed same-tailed element %q (claims=%d)", e.ElementID, e.ClaimCount)
		}
	}
	if _, ok := covered(res, dh+"/Driver"); !ok {
		t.Fatalf("bold **Driver** should attribute the element whose whole name is Driver")
	}
}

// ---------------------------------------------------------------------------
// FP Class C — backtick on a path / build target / manifest keyword.
// ---------------------------------------------------------------------------

func TestFP_BacktickOnPath(t *testing.T) {
	const dd = "fuchsia.driver.development"
	const dh = "fuchsia.driver.host"
	elems := []*contractpb.ContractElement{
		elem(dd+"/Manager", "PROTOCOL", dd),
		elem(dh+"/Driver", "PROTOCOL", dh),
	}
	cases := []struct{ name, body string }{
		{"build-target", "# Build\n\nList drivers in `//build/drivers/all_drivers_lists_arm64.txt` before building.\n"},
		{"header-path", "# Migrate\n\nInclude `//src/lib/ddk/include/lib/ddk/driver.h` in your target.\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/development/drivers/build.md", c.body)
			mustNotAttribute(t, res, "backticked path component is not the element identifier")
		})
	}
}

func TestFP_BacktickOnKeyword(t *testing.T) {
	const ci = "fuchsia.component.internal"
	elems := []*contractpb.ContractElement{
		elem(ci+"/InjectedUseProtocol", "PROTOCOL", ci),
	}
	body := "# Capabilities\n\nA `protocol` with the key `fuchsia.examples.Echo` is offered to the child.\n"
	res := detectWith(elems, "docs/concepts/components/v2/capabilities/dictionary.md", body)
	mustNotAttribute(t, res, "backticked manifest keyword `protocol` is not InjectedUseProtocol")
}

// ---------------------------------------------------------------------------
// FP Class D — cross-library FQN tail. A qualified mention of ANOTHER
// library's same-tailed type must not attribute this library's element.
// ---------------------------------------------------------------------------

func TestFP_CrossLibraryFQNTail(t *testing.T) {
	const act = "fuchsia.ui.activity"
	elems := []*contractpb.ContractElement{
		elem(act+"/Provider", "PROTOCOL", act),
	}
	// A backticked FQN from the *tracing* library — tail "Provider".
	body := "# Vulkan\n\nRegister with `fuchsia.tracing.provider.Registry` to emit trace records.\n"
	res := detectWith(elems, "docs/development/graphics/magma/concepts/vulkan.md", body)
	if _, ok := covered(res, act+"/Provider"); ok {
		t.Fatalf("cross-library FQN tail (fuchsia.tracing.provider.Registry) must NOT attribute ui.activity/Provider")
	}
}

// Same-library FQN of a same-tailed element IS allowed: a backticked
// `fuchsia.ui.activity/Provider` attributes ui.activity/Provider.
func TestTP_SameLibraryFQN(t *testing.T) {
	const act = "fuchsia.ui.activity"
	elems := []*contractpb.ContractElement{elem(act+"/Provider", "PROTOCOL", act)}
	body := "# Activity\n\nThe `fuchsia.ui.activity/Provider` protocol reports idle state.\n"
	res := detectWith(elems, "docs/concepts/ui/activity.md", body)
	if _, ok := covered(res, act+"/Provider"); !ok {
		t.Fatalf("same-library FQN fuchsia.ui.activity/Provider should attribute Provider")
	}
}

// ---------------------------------------------------------------------------
// FP Class C' — link whose target tail contains the bare word only as a
// hyphen/dot-delimited slug component, not as the whole segment. A link to a
// heading "#…-in-a-driver" or the glossary "#board-driver" must NOT attribute
// the single-word Driver/Node/Device element (the audit's #board-driver case).
// ---------------------------------------------------------------------------

func TestFP_LinkSlugTail(t *testing.T) {
	const dh = "fuchsia.driver.host"
	const dfw = "fuchsia.driver.framework"
	elems := []*contractpb.ContractElement{
		elem(dh+"/Driver", "PROTOCOL", dh),
		elem(dfw+"/Node", "PROTOCOL", dfw),
		elem(dfw+"/Device", "PROTOCOL", dfw),
	}
	cases := []struct{ name, body string }{
		{"glossary-board-driver", "# Bind\n\nA [driver](/docs/glossary/README.md#board-driver) binds to hardware.\n"},
		{"heading-slug", "# VMO\n\nSee [driver](#mapping-a-vmo-to-a-vmar-in-a-driver) for details.\n"},
		{"device-vs-node", "# Model\n\nThe [node](#device-vs-node) and the [device](#device-vs-node) differ.\n"},
		{"filename-slug", "# Bind\n\nRead [driver](driver-binding.md) docs.\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/development/drivers/x.md", c.body)
			mustNotAttribute(t, res, "link slug tail is not the whole element name")
		})
	}
}

// A link whose fragment IS exactly the element name still attributes, and the
// library-prefix rule still allows the element's own library.
func TestTP_LinkExactFragment(t *testing.T) {
	const dfw = "fuchsia.driver.framework"
	elems := []*contractpb.ContractElement{elem(dfw+"/Node", "PROTOCOL", dfw)}
	body := "# Topology\n\nEach [node](/reference/fidl/fuchsia.driver.framework#Node) has a parent.\n"
	res := detectWith(elems, "docs/concepts/drivers/topo.md", body)
	if _, ok := covered(res, dfw+"/Node"); !ok {
		t.Fatalf("link with fragment #Node should attribute Node")
	}
}

// A link whose fragment is the element name but carries a FOREIGN library
// prefix must NOT attribute (Class D for links).
func TestFP_LinkCrossLibraryFragment(t *testing.T) {
	const act = "fuchsia.ui.activity"
	elems := []*contractpb.ContractElement{elem(act+"/Provider", "PROTOCOL", act)}
	body := "# Trace\n\nRegister the [provider](/reference/fidl/fuchsia.tracing.provider#Provider) now.\n"
	res := detectWith(elems, "docs/x.md", body)
	if _, ok := covered(res, act+"/Provider"); ok {
		t.Fatalf("cross-library link fragment (fuchsia.tracing.provider#Provider) must NOT attribute ui.activity/Provider")
	}
}

// ---------------------------------------------------------------------------
// POSITIVES — the genuine signals Fix 1/2 must keep.
// ---------------------------------------------------------------------------

// Full multi-word name written out in BARE prose attributes (Fix 1's keep).
func TestTP_FullMultiWordName_BareProse(t *testing.T) {
	const dh = "fuchsia.driver.host"
	const dfw = "fuchsia.driver.framework"
	elems := []*contractpb.ContractElement{
		elem(dh+"/DriverHost", "PROTOCOL", dh),
		elem(dfw+"/NodeController", "PROTOCOL", dfw),
	}
	cases := []struct {
		name, body, wantID string
	}{
		{"driver-host-spaced", "# Hosts\n\nEach driver host runs drivers in its own process.\n", dh + "/DriverHost"},
		{"node-controller-spaced", "# Topology\n\nThe parent uses the node controller to manage one child.\n", dfw + "/NodeController"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/concepts/drivers/x.md", c.body)
			if _, ok := covered(res, c.wantID); !ok {
				t.Fatalf("full multi-word name written out should attribute %q; res=%+v", c.wantID, res.Elements)
			}
		})
	}
}

// Backticked whole identifier attributes (Fix 2's keep), including the single-
// word case that bare prose forbids: `Driver` backticked DOES attribute.
func TestTP_BacktickedWholeIdentifier(t *testing.T) {
	const dfw = "fuchsia.driver.framework"
	const dh = "fuchsia.driver.host"
	elems := []*contractpb.ContractElement{
		elem(dfw+"/NodeController", "PROTOCOL", dfw),
		elem(dh+"/Driver", "PROTOCOL", dh),
	}
	cases := []struct {
		name, body, wantID string
	}{
		{"multiword-backtick", "# Drivers\n\nThe `NodeController` protocol manages one child.\n", dfw + "/NodeController"},
		{"singleword-backtick", "# Drivers\n\nThe `Driver` protocol is bound by the framework.\n", dh + "/Driver"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/concepts/drivers/x.md", c.body)
			if _, ok := covered(res, c.wantID); !ok {
				t.Fatalf("backticked whole identifier should attribute %q; res=%+v", c.wantID, res.Elements)
			}
		})
	}
}

// The two canonical positives from the brief: `NodeController` (backtick) and
// fuchsia.driver.framework/NodeController (qualified) both attribute.
func TestTP_NodeController_BacktickAndQualified(t *testing.T) {
	const dfw = "fuchsia.driver.framework"
	elems := []*contractpb.ContractElement{elem(dfw+"/NodeController", "PROTOCOL", dfw)}

	resBT := detectWith(elems, "docs/a.md", "# A\n\nUse the `NodeController` to manage children.\n")
	if _, ok := covered(resBT, dfw+"/NodeController"); !ok {
		t.Fatalf("backticked `NodeController` should attribute")
	}
	resFQ := detectWith(elems, "docs/b.md", "# B\n\nSee fuchsia.driver.framework/NodeController for details.\n")
	if _, ok := covered(resFQ, dfw+"/NodeController"); !ok {
		t.Fatalf("qualified fuchsia.driver.framework/NodeController should attribute")
	}
}

// Type::member / Protocol/Method credits the HEAD element when the head
// identifier + library match.
func TestTP_TypeMemberHead(t *testing.T) {
	const dfw = "fuchsia.driver.framework"
	elems := []*contractpb.ContractElement{elem(dfw+"/NodeController", "PROTOCOL", dfw)}
	// `NodeController/Remove` — slash-method form; head is NodeController.
	res := detectWith(elems, "docs/c.md", "# C\n\nCall `NodeController/Remove` to detach the node.\n")
	if _, ok := covered(res, dfw+"/NodeController"); !ok {
		t.Fatalf("NodeController/Remove should credit head NodeController")
	}
}

// A qualified FQN that is the element's OWN library still attributes even
// though the tail is a common noun: `fuchsia.driver.host/Driver`.
func TestTP_SameLibraryFQN_CommonNounTail(t *testing.T) {
	const dh = "fuchsia.driver.host"
	elems := []*contractpb.ContractElement{elem(dh+"/Driver", "PROTOCOL", dh)}
	res := detectWith(elems, "docs/d.md", "# D\n\nThe `fuchsia.driver.host/Driver` protocol starts the host.\n")
	if _, ok := covered(res, dh+"/Driver"); !ok {
		t.Fatalf("same-library FQN fuchsia.driver.host/Driver should attribute Driver")
	}
}

// ===========================================================================
// REGRESSION re-audit (post-Phase-3): two residual leak classes the Phase-3
// fixes did NOT close. These inflated the live "clear" count by ~20% with
// degree-1 false positives, pushing it from clear-conservative (~281) toward
// clear-inclusive (~338). The fixes tighten the link + defined_term anchors in
// internal/grounding/anchored.go ONLY (the shared detector is untouched).
// ===========================================================================

// LINK leak: a link target whose resolved tail matches the element only AFTER
// lowercasing, stripping a file extension, or tolerating a plural. The
// post-fix rule is a CASE-SENSITIVE, whole-segment, no-plural, no-ext-strip
// identifier match, so a lowercase path FILENAME (host.fidl, debug.h,
// session.fidl, process.md) and a PLURAL path segment (/src/devices,
// #resolvers, #logs) and a hyphen slug (#board-driver) must NOT attribute.
func TestFP_LinkPathFilenameAndPlural(t *testing.T) {
	const bth = "fuchsia.bluetooth.host"
	const ddk = "fuchsia.device"
	const cr = "fuchsia.component.resolution"
	const dfw = "fuchsia.driver.framework"
	const diag = "fuchsia.diagnostics"
	elems := []*contractpb.ContractElement{
		elem(bth+"/Host", "PROTOCOL", bth),   // host.fidl filename
		elem(ddk+"/Device", "PROTOCOL", ddk), // /src/devices plural dir
		elem(cr+"/Resolver", "PROTOCOL", cr), // #resolvers plural fragment
		elem(dfw+"/Driver", "PROTOCOL", dfw), // /src/devices/usb/drivers plural
		elem(diag+"/Log", "PROTOCOL", diag),  // #logs plural fragment
	}
	cases := []struct{ name, body string }{
		{"fidl-filename", "# BT\n\nbt-host devices implement the [host.fidl](/sdk/fidl/fuchsia.bluetooth.host/host.fidl) protocol.\n"},
		{"header-filename", "# DDK\n\nInclude [debug](/src/lib/ddk/include/lib/ddk/debug.h) for logging.\n"},
		{"plural-devices-dir", "# Tree\n\nDrivers live under [devices](/src/devices) in the tree.\n"},
		{"plural-drivers-dir", "# Tree\n\nUSB [drivers](/src/devices/usb/drivers) are built here.\n"},
		{"plural-resolvers-frag", "# Resolvers\n\nSee the [resolvers](#resolvers) section.\n"},
		{"plural-logs-frag", "# Diag\n\nRead the [logs](#logs) channel.\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/x.md", c.body)
			mustNotAttribute(t, res, "link path filename / plural segment is not the element identifier")
		})
	}
}

// LINK positive that MUST survive: a fragment written in the element's own
// PascalCase identifier is a real anchor. `[`Foo`](#Foo)` attributes Foo.
func TestTP_LinkExactCaseFragment(t *testing.T) {
	const dfw = "fuchsia.driver.framework"
	const med = "fuchsia.media"
	elems := []*contractpb.ContractElement{
		elem(dfw+"/NodeController", "PROTOCOL", dfw),
		elem(med+"/Usage", "TYPE", med),
	}
	cases := []struct{ name, body, wantID string }{
		// Backticked link text spelled in the element's case, fragment in case.
		{"backtick-frag", "# Topo\n\nEach [`NodeController`](#NodeController) has a parent.\n", dfw + "/NodeController"},
		// Same-library FQN fragment on a fuchsia.dev reference URL.
		{"fidl-ref-frag", "# Audio\n\n[Usage](https://fuchsia.dev/reference/fidl/fuchsia.media#Usage) is a hint.\n", med + "/Usage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/x.md", c.body)
			if _, ok := covered(res, c.wantID); !ok {
				t.Fatalf("exact-case link fragment should attribute %q; res=%+v", c.wantID, res.Elements)
			}
		})
	}
}

// DEFINED-TERM leak: a bold/italic English PHRASE that merely BEGINS with the
// element name. detect.go's definedTermBinds only checks the opening marker,
// so "**Device administrators**" fired "Device", "**Block Map**" fired
// "Block", "**Condition statements**" fired "Condition". The post-fix
// emphasisTightlyWraps requires the emphasized SPAN to be the element name
// alone, so these must NOT attribute.
func TestFP_DefinedTermPhraseNotTerm(t *testing.T) {
	const cl = "fuchsia.component"
	const dfw = "fuchsia.driver.framework"
	const fs = "fuchsia.fs"
	elems := []*contractpb.ContractElement{
		elem(cl+"/Device", "TYPE", cl),
		elem(fs+"/Block", "TYPE", fs),
		elem(dfw+"/Condition", "TYPE", dfw),
	}
	cases := []struct{ name, body string }{
		{"device-administrators", "# Audience\n\n- **Device administrators**.\n"},
		{"block-map", "# Layout\n\n*   The **Block Map**, a bitmap of free blocks.\n"},
		{"condition-statements", "# Bind\n\n- **Condition statements** are equality expressions.\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/x.md", c.body)
			mustNotAttribute(t, res, "bold English phrase that merely contains the name is not a defined term")
		})
	}
}

// DEFINED-TERM positive that MUST survive: an emphasized span that is EXACTLY
// the element name (the markdown defined-term convention). **Driver**,
// *Environments* (plural of the single-word name), `_Node_`.
func TestTP_DefinedTermExactSpan(t *testing.T) {
	const dh = "fuchsia.driver.host"
	const cl = "fuchsia.component"
	const dfw = "fuchsia.driver.framework"
	elems := []*contractpb.ContractElement{
		elem(dh+"/Driver", "PROTOCOL", dh),
		elem(cl+"/Environment", "TYPE", cl),
		elem(dfw+"/Node", "PROTOCOL", dfw),
	}
	cases := []struct{ name, body, wantID string }{
		{"bold-driver", "# Glossary\n\n**Driver**: the unit bound to a node.\n", dh + "/Driver"},
		{"italic-environments", "# Realms\n\n*Environments* configure framework choices.\n", cl + "/Environment"},
		{"bold-node", "# Topology\n\nThe **Node** is the unit of the driver tree.\n", dfw + "/Node"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := detectWith(elems, "docs/x.md", c.body)
			if _, ok := covered(res, c.wantID); !ok {
				t.Fatalf("exact-span defined term should attribute %q; res=%+v", c.wantID, res.Elements)
			}
		})
	}
}
