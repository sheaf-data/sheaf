package implementsmap

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
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

func TestResolveFIDLProtocol(t *testing.T) {
	cases := []struct {
		in              string
		wantID, wantLib string
	}{
		{"fuchsia_io::Directory", "fuchsia.io/Directory", "fuchsia.io"},
		{"fuchsia_driver_framework::Driver", "fuchsia.driver.framework/Driver", "fuchsia.driver.framework"},
		{"fidl_fuchsia_io::DirectoryRequest", "fuchsia.io/Directory", "fuchsia.io"},
		{"fuchsia_io_admin::Admin", "fuchsia.io.admin/Admin", "fuchsia.io.admin"},
		{"NoColons", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		gotID, gotLib := resolveFIDLProtocol(c.in)
		if gotID != c.wantID || gotLib != c.wantLib {
			t.Errorf("resolveFIDLProtocol(%q) = (%q, %q), want (%q, %q)",
				c.in, gotID, gotLib, c.wantID, c.wantLib)
		}
	}
}

func TestDiscover_SingleLineCPP(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/foo.h": `class Foo : public fidl::WireServer<fuchsia_io::File> {};`,
	})
	a := New(Config{Include: []string{"src/**/*.h"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 1 {
		t.Fatalf("got %d elems, want 1", len(elems))
	}
	if elems[0].GetKind() != contractpb.ContractElementKind_CPP_CLASS {
		t.Errorf("kind = %v, want CPP_CLASS", elems[0].GetKind())
	}
	rels := elems[0].GetRelationships()
	if len(rels) != 1 || rels[0].GetTargetElementId() != "fuchsia.io/File" {
		t.Errorf("rel = %+v", rels)
	}
}

func TestDiscover_MultiLineDeclaration(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/conn.h": `class DirectoryConnection final : public Connection,
                                  public fidl::WireServer<fuchsia_io::Directory> {
 public:
  DirectoryConnection();
};

class FileConnection : public Connection, public fidl::WireServer<fuchsia_io::File> {
 public:
  FileConnection();
};

class NodeConnection final : public Connection, public fidl::WireServer<fuchsia_io::Node> {};`,
	})
	a := New(Config{Include: []string{"src/**/*.h"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 3 {
		t.Fatalf("got %d elems, want 3", len(elems))
	}
	got := make(map[string]string)
	for _, e := range elems {
		for _, r := range e.GetRelationships() {
			// Element ID looks like "cpp:src/conn.h#DirectoryConnection"
			cls := e.GetId()
			if i := lastHash(cls); i >= 0 {
				cls = cls[i+1:]
			}
			got[cls] = r.GetTargetElementId()
		}
	}
	for cls, want := range map[string]string{
		"DirectoryConnection": "fuchsia.io/Directory",
		"FileConnection":      "fuchsia.io/File",
		"NodeConnection":      "fuchsia.io/Node",
	} {
		if got[cls] != want {
			t.Errorf("class %s -> %q, want %q", cls, got[cls], want)
		}
	}
}

func lastHash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '#' {
			return i
		}
	}
	return -1
}

// relTargets returns the set of IMPLEMENTS target IDs on one element.
func relTargets(e *contractpb.ContractElement) map[string]bool {
	out := map[string]bool{}
	for _, r := range e.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_IMPLEMENTS {
			out[r.GetTargetElementId()] = true
		}
	}
	return out
}

// TestDiscover_CPPMultipleServerBases is the core regression test for the
// multi-base undercount: one class serving several protocols via multiple
// inheritance, with the base list spanning multiple lines and mixing
// fidl::WireServer<> and fidl::Server<>. Every server base must yield an
// IMPLEMENTS edge — all on the single class element — not just the first.
func TestDiscover_CPPMultipleServerBases(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/driver_runner.h": `namespace driver_manager {

class DriverRunner : public fidl::WireServer<fuchsia_driver_framework::CompositeNodeManager>,
                     public fidl::WireServer<fuchsia_driver_index::DriverNotifier>,
                     public fidl::WireServer<fuchsia_driver_crash::CrashIntrospect>,
                     public fidl::Server<fuchsia_driver_token::NodeBusTopology>,
                     public fidl::WireServer<fuchsia_driver_token::Debug>,
                     public BindManagerBridge,
                     public std::enable_shared_from_this<DriverRunner> {
 public:
  DriverRunner();
};

}  // namespace driver_manager`,
	})
	a := New(Config{Include: []string{"src/**/*.h"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// One element for the class, carrying one edge per distinct protocol.
	if len(elems) != 1 {
		t.Fatalf("got %d elems, want 1 (single class element); got %+v", len(elems), elems)
	}
	e := elems[0]
	if e.GetId() != "cpp:src/driver_runner.h#DriverRunner" {
		t.Errorf("id = %q, want cpp:src/driver_runner.h#DriverRunner", e.GetId())
	}
	if e.GetKind() != contractpb.ContractElementKind_CPP_CLASS {
		t.Errorf("kind = %v, want CPP_CLASS", e.GetKind())
	}
	got := relTargets(e)
	want := []string{
		"fuchsia.driver.framework/CompositeNodeManager",
		"fuchsia.driver.index/DriverNotifier",
		"fuchsia.driver.crash/CrashIntrospect",
		"fuchsia.driver.token/NodeBusTopology",
		"fuchsia.driver.token/Debug",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d: %v", len(got), len(want), got)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing IMPLEMENTS edge to %q; got %v", w, got)
		}
	}
	// Each relationship's DeclarationSite should point at the base's own
	// line, not all collapse to the class line. The five bases sit on
	// distinct lines, so we expect five distinct declaration lines.
	lines := map[uint32]bool{}
	for _, r := range e.GetRelationships() {
		lines[r.GetDeclarationSite().GetLine()] = true
	}
	if len(lines) != 5 {
		t.Errorf("expected 5 distinct base-decl lines, got %d: %v", len(lines), lines)
	}
}

// TestDiscover_CPPDedupRepeatedProtocol verifies a protocol named twice in
// the same base list collapses to a single edge.
func TestDiscover_CPPDedupRepeatedProtocol(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/dup.h": `class Dup : public fidl::WireServer<fuchsia_io::Node>,
                    public fidl::Server<fuchsia_io::Node>,
                    public Mixin {};`,
	})
	a := New(Config{Include: []string{"src/**/*.h"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 1 {
		t.Fatalf("got %d elems, want 1; got %+v", len(elems), elems)
	}
	if n := len(elems[0].GetRelationships()); n != 1 {
		t.Fatalf("got %d edges, want 1 (deduped); got %+v", n, elems[0].GetRelationships())
	}
	if !containsTarget(elems, "fuchsia.io/Node") {
		t.Errorf("missing fuchsia.io/Node: %+v", elems)
	}
}

// TestDiscover_CPPNonServerBaseIgnored confirms a non-server base in the
// list (a plain mixin / interface that is not fidl::(Wire)?Server<>) does
// not produce an edge, while the one real server base still does.
func TestDiscover_CPPNonServerBaseIgnored(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/mix.h": `class Mixed : public SomeBase,
                      public fidl::WireClient<fuchsia_io::Directory>,
                      public fidl::WireServer<fuchsia_io::File>,
                      public std::enable_shared_from_this<Mixed> {};`,
	})
	a := New(Config{Include: []string{"src/**/*.h"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 1 {
		t.Fatalf("got %d elems, want 1; got %+v", len(elems), elems)
	}
	got := relTargets(elems[0])
	// Only the WireServer<File> base bridges. WireClient<> (consumer side)
	// and the plain bases must not.
	if len(got) != 1 || !got["fuchsia.io/File"] {
		t.Errorf("edges = %v, want exactly {fuchsia.io/File}", got)
	}
}

// TestDiscover_CPPMultiBaseScopeFilter verifies out-of-scope protocols in a
// multi-base class are dropped while in-scope ones survive — the precision
// safeguard must keep working per-base, not just per-class.
func TestDiscover_CPPMultiBaseScopeFilter(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/node.h": `class Node : public fidl::WireServer<fuchsia_driver_framework::NodeController>,
             public fidl::WireServer<fuchsia_driver_framework::Node>,
             public fidl::WireServer<fuchsia_component_runner::ComponentController>,
             public fidl::WireServer<fuchsia_device::Controller>,
             public NodeShutdownBridge {};`,
	})
	a := New(Config{Include: []string{"src/**/*.h"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{
		Libraries: []string{"fuchsia.driver.framework"},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 1 {
		t.Fatalf("got %d elems, want 1; got %+v", len(elems), elems)
	}
	got := relTargets(elems[0])
	want := []string{
		"fuchsia.driver.framework/NodeController",
		"fuchsia.driver.framework/Node",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d (only in-scope libs): %v", len(got), len(want), got)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing %q; got %v", w, got)
		}
	}
	// ComponentController (fuchsia.component.runner) and Controller
	// (fuchsia.device) are out of scope and must be absent.
	for _, bad := range []string{"fuchsia.component.runner/ComponentController", "fuchsia.device/Controller"} {
		if got[bad] {
			t.Errorf("out-of-scope edge leaked: %q", bad)
		}
	}
}

func TestDiscover_ScopeFilter(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/a.h": `class A : public fidl::WireServer<fuchsia_io::File> {};`,
		"src/b.h": `class B : public fidl::WireServer<fuchsia_other::Thing> {};`,
	})
	a := New(Config{Include: []string{"src/**/*.h"}})
	elems, _ := a.Discover(context.Background(), repo, adapters.ScopeConfig{
		Libraries: []string{"fuchsia.io"},
	})
	// Only A should survive; B targets fuchsia.other which is out of scope.
	if len(elems) != 1 {
		t.Fatalf("got %d elems, want 1 (scope filter dropped B); got %+v", len(elems), elems)
	}
	if !containsTarget(elems, "fuchsia.io/File") {
		t.Errorf("missing fuchsia.io/File: %+v", elems)
	}
}

func TestDiscover_FidlServer(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/x.h": `class Server : public fidl::Server<fuchsia_io::Directory> {};`,
	})
	a := New(Config{Include: []string{"src/**/*.h"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 1 || elems[0].GetRelationships()[0].GetTargetElementId() != "fuchsia.io/Directory" {
		t.Errorf("got %+v", elems)
	}
}

func TestDiscover_RustImplPattern(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/x.rs": `
impl fidl_fuchsia_io::DirectoryRequest for MyServer {
    fn open(&mut self, args: OpenArgs) {}
}
`,
	})
	a := New(Config{Include: []string{"src/**/*.rs"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) == 0 {
		t.Fatalf("no rust impls detected; got %+v", elems)
	}
	if elems[0].GetKind() != contractpb.ContractElementKind_RUST_TYPE {
		t.Errorf("rust kind wrong: %v", elems[0].GetKind())
	}
	if elems[0].GetRelationships()[0].GetTargetElementId() != "fuchsia.io/Directory" {
		t.Errorf("rust target wrong: %+v", elems[0].GetRelationships())
	}
}

// TestDiscover_RustFuchsiaServerIdiom covers the v2 patterns: a Fuchsia
// async FIDL server that aliases the crate (`use fidl_fuchsia_sys2 as
// fsys;`) and serves via a RequestStream param + Request:: match arms.
// This is the component_manager idiom that the v1 `impl ...Request for
// X` pattern missed entirely.
func TestDiscover_RustFuchsiaServerIdiom(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/realm_query.rs": `
use fidl_fuchsia_sys2 as fsys;

async fn serve_inner(scope: Scope, mut stream: fsys::RealmQueryRequestStream) {
    while let Some(request) = stream.next().await {
        match request {
            fsys::RealmQueryRequest::GetInstance { moniker, responder } => {}
            fsys::RealmQueryRequest::GetAllInstances { responder } => {}
        }
    }
}
`,
	})
	a := New(Config{Include: []string{"src/**/*.rs"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Despite the stream type + two match arms (three raw hits), the
	// file must collapse to exactly one element for RealmQuery.
	if len(elems) != 1 {
		t.Fatalf("got %d elems, want 1 (deduped per protocol); got %+v", len(elems), elems)
	}
	e := elems[0]
	if e.GetKind() != contractpb.ContractElementKind_RUST_TYPE {
		t.Errorf("kind = %v, want RUST_TYPE", e.GetKind())
	}
	// Free-function server → synthetic implementor name derived from proto.
	if got := e.GetId(); got != "rust:src/realm_query.rs#RealmQueryServer" {
		t.Errorf("element id = %q, want rust:src/realm_query.rs#RealmQueryServer", got)
	}
	rels := e.GetRelationships()
	if len(rels) != 1 || rels[0].GetTargetElementId() != "fuchsia.sys2/RealmQuery" {
		t.Errorf("rel = %+v, want IMPLEMENTS fuchsia.sys2/RealmQuery", rels)
	}
}

// TestDiscover_RustServerStreamOnly verifies the RequestStream-type
// signal alone (no match arm) still bridges, and that alias resolution
// works for a multi-segment library (fuchsia.component.runner).
func TestDiscover_RustServerStreamOnly(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/runner.rs": `
use fidl_fuchsia_component_runner as fcrunner;

fn publish(stream: fcrunner::ComponentRunnerRequestStream) {
    spawn(serve(stream));
}
`,
	})
	a := New(Config{Include: []string{"src/**/*.rs"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !containsTarget(elems, "fuchsia.component.runner/ComponentRunner") {
		t.Fatalf("missing fuchsia.component.runner/ComponentRunner: %+v", elems)
	}
}

// TestDiscover_RustNonFidlQualifierIgnored is the false-positive guard:
// a `<ns>::FooRequest::Bar` whose qualifier is NOT a FIDL crate alias
// must not bridge. Here `internal` is a plain module, not `use fidl_...
// as internal`, so the dispatch arm resolves to nothing.
func TestDiscover_RustNonFidlQualifierIgnored(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"src/handler.rs": `
use crate::internal;

fn handle(req: internal::JobRequest) {
    match req {
        internal::JobRequest::Run { id } => {}
    }
}
`,
	})
	a := New(Config{Include: []string{"src/**/*.rs"}})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 0 {
		t.Fatalf("got %d elems, want 0 (non-FIDL qualifier must not bridge); got %+v", len(elems), elems)
	}
}

// TestResolveRustAlias exercises the alias rewrite directly.
func TestResolveRustAlias(t *testing.T) {
	aliases := map[string]string{
		"fsys":     "fidl_fuchsia_sys2",
		"fcrunner": "fidl_fuchsia_component_runner",
	}
	cases := []struct{ in, want string }{
		{"fsys::RealmQueryRequest", "fidl_fuchsia_sys2::RealmQueryRequest"},
		{"fcrunner::ComponentRunnerRequestStream", "fidl_fuchsia_component_runner::ComponentRunnerRequestStream"},
		// already-qualified full path: unchanged.
		{"fidl_fuchsia_io::DirectoryRequest", "fidl_fuchsia_io::DirectoryRequest"},
		// unknown qualifier: unchanged (dropped later by resolveFIDLProtocol).
		{"internal::JobRequest", "internal::JobRequest"},
		// no "::": unchanged.
		{"NoColons", "NoColons"},
	}
	for _, c := range cases {
		if got := resolveRustAlias(c.in, aliases); got != c.want {
			t.Errorf("resolveRustAlias(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func containsTarget(elems []*contractpb.ContractElement, want string) bool {
	for _, e := range elems {
		for _, r := range e.GetRelationships() {
			if r.GetTargetElementId() == want {
				return true
			}
		}
	}
	return false
}

// Sort helper used in some other tests.
var _ = sort.Strings
