// Package implementsmap detects C++ / Rust classes that implement a
// FIDL protocol, and emits ContractElements with IMPLEMENTS
// relationships pointing at the FIDL element they serve. The indexer
// uses these edges to attribute tests of the implementation class
// back to the FIDL methods.
//
// Without this adapter, a test like PseudoFileTest.ReadAtEnd that
// exercises the C++ class never gets attributed to fuchsia.io/File.Read.
// Phase 0 measured structural test-recall at ~9% on fuchsia.io without
// implements-map; activating it is the principal path to >=50% recall.
//
// v1 patterns supported:
//   C++: class Foo : ... public fidl::WireServer<NS::Proto>
//   C++: class Foo : ... public fidl::Server<NS::Proto>
//   Rust: impl NS::ProtoRequest for Foo
//   Rust: impl Marker for Foo where Marker: NS::*RequestStream
//
// v2 Rust patterns (Fuchsia async-server idiom — added to bridge
// component_manager and other Rust FIDL servers that don't use the
// `impl ProtoRequest for X` trait shape):
//
//	use fidl_fuchsia_sys2 as fsys;
//	async fn serve(..., mut stream: fsys::RealmQueryRequestStream) {
//	    while let Some(req) = stream.next().await {
//	        match req {
//	            fsys::RealmQueryRequest::GetInstance { .. } => { ... }
//	        }
//	    }
//	}
//
// Two signals are matched, both QUALIFIED to keep false positives low:
//   - `<ns>::<Proto>RequestStream`        (the server-side stream type)
//   - `<ns>::<Proto>Request::<Variant>`   (request-dispatch match arm)
// `<ns>` must resolve to a `fidl_fuchsia_*` crate — either it IS the
// crate path, or it is a local alias declared via
// `use fidl_fuchsia_... as <ns>;` in the same file. A qualifier that
// does not map to a FIDL crate is dropped, so a stray `foo::BarRequest`
// never bridges.

package implementsmap

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

const Name = "cpp-fidl-wireserver"
const Version = "0.1.0"

type Adapter struct {
	include []string
	exclude []string
	// cppClassHeader matches a class declaration that has a base list and
	// captures (class name, base-clause region). cppServerBase enumerates
	// every `fidl::(Wire)?Server<Proto>` base within that region. A class
	// that serves several protocols via multiple inheritance therefore
	// yields one IMPLEMENTS edge per distinct protocol, not just the first
	// — the multi-base undercount fix. See discoverCPP.
	cppClassHeader *regexp.Regexp
	cppServerBase  *regexp.Regexp
	// cppExtraPatterns are project-supplied CPPPatterns. Each produces two
	// capture groups, (class_name, ns_proto), matched in one shot.
	cppExtraPatterns []*regexp.Regexp
	rustPatterns     []rustPattern
	// rustAlias matches `use fidl_fuchsia_... as <ident>;` lines so the
	// short qualifier in `fsys::FooRequestStream` can be resolved to the
	// real FIDL crate path (group 1 = crate, group 2 = alias ident).
	rustAlias *regexp.Regexp
}

// rustPattern pairs a compiled regex with the capture-group indices for
// the protocol-qualifier and the implementor name. Different Rust
// idioms put these groups in different positions, so we record them
// explicitly rather than assuming a fixed order.
//
// protoGroup is the submatch index (1-based) of the qualified protocol
// reference, e.g. "fsys::RealmQueryRequest" or
// "fidl_fuchsia_io::DirectoryRequest".
//
// nameGroup is the submatch index of the implementor's name. When it is
// 0, the idiom has no explicit implementor (Fuchsia servers are usually
// free functions, not `impl X`); a synthetic name is derived from the
// protocol so the IMPLEMENTS edge still has a stable, unique source.
//
// requireFidl gates the match on the protocol qualifier resolving to a
// `fidl_*` crate (after alias resolution). The v2 server patterns key
// off a bare `<ns>::<Proto>Request[Stream]` shape that would otherwise
// also match unrelated local enums like `internal::JobRequest::Run`;
// requiring a fidl_ crate head removes that whole class of false
// positives. The v1 trait-impl pattern leaves this false because it is
// already anchored by the `impl ... for` keywords.
type rustPattern struct {
	rx          *regexp.Regexp
	protoGroup  int
	nameGroup   int
	requireFidl bool
}

type Config struct {
	Include []string
	Exclude []string
	// CPPPatterns / RustPatterns let projects extend the default set.
	// Patterns must produce two capture groups: (class_name, ns_proto).
	CPPPatterns  []string
	RustPatterns []string
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"src/**/*.cc", "src/**/*.h", "src/**/*.cpp", "src/**/*.rs"}
	}
	a := &Adapter{include: include, exclude: cfg.Exclude}
	// Built-in C++ detection runs in two stages (see discoverCPP): first
	// isolate each class's base-clause region, then enumerate EVERY
	// `fidl::(Wire)?Server<Proto>` base inside it. cppClassHeader captures
	// the class name (group 1) and the whole base region (group 2, from the
	// `:` to the opening `{` — `[^{;]*` spans multi-line base lists).
	// cppServerBase then extracts each server base's template argument
	// (group 1) within that region. Project-supplied CPPPatterns keep the
	// legacy single-shot (class, proto) convention and run alongside.
	a.cppClassHeader = regexp.MustCompile(`(?s)class\s+(\w+)\s*(?:final\s+)?:([^{;]*)\{`)
	a.cppServerBase = regexp.MustCompile(`fidl::(?:Wire)?Server<([\w:]+)>`)
	for _, p := range cfg.CPPPatterns {
		a.cppExtraPatterns = append(a.cppExtraPatterns, regexp.MustCompile(p))
	}

	// `use fidl_fuchsia_component_runner as fcrunner;`
	a.rustAlias = regexp.MustCompile(
		`(?m)^\s*use\s+(fidl_[a-z][a-z0-9_]*)\s+as\s+([a-z][a-zA-Z0-9_]*)\s*;`)

	// Built-in Rust patterns. protoGroup/nameGroup record where the
	// qualified protocol ref and the implementor name sit in each regex.
	a.rustPatterns = []rustPattern{
		// v1: `impl <name>::Request for X` (trait-impl shape). Implementor
		// name is the struct after `for`.
		{
			rx:         regexp.MustCompile(`(?m)impl\s+([\w:]+Request)\s+for\s+(\w+)`),
			protoGroup: 1,
			nameGroup:  2,
		},
		// v2a: request-dispatch match arm — the strongest "this code
		// serves the protocol" signal. Clients never write
		// `<ns>::<Proto>Request::<Variant>`. No explicit implementor.
		//   fsys::RealmQueryRequest::GetInstance { .. } => ...
		{
			rx:          regexp.MustCompile(`(?m)\b([a-z][a-zA-Z0-9_]*::[A-Z][A-Za-z0-9_]*Request)::[A-Z]`),
			protoGroup:  1,
			nameGroup:   0,
			requireFidl: true,
		},
		// v2b: server-side stream type. Qualified so a bare identifier
		// can't trigger; the qualifier is alias-resolved below.
		//   mut stream: fsys::RealmQueryRequestStream
		{
			rx:          regexp.MustCompile(`\b([a-z][a-zA-Z0-9_]*::[A-Z][A-Za-z0-9_]*RequestStream)\b`),
			protoGroup:  1,
			nameGroup:   0,
			requireFidl: true,
		},
	}
	// Project-supplied extension patterns keep the legacy
	// (proto=group1, name=group2) convention.
	for _, p := range cfg.RustPatterns {
		a.rustPatterns = append(a.rustPatterns, rustPattern{
			rx:         regexp.MustCompile(p),
			protoGroup: 1,
			nameGroup:  2,
		})
	}
	return a
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	var out []*contractpb.ContractElement
	err := adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("implementsmap: read %s: %w", rel, err)
		}
		if strings.HasSuffix(rel, ".rs") {
			out = append(out, a.discoverRust(rel, body, scope)...)
		} else {
			out = append(out, a.discoverCPP(rel, body, scope)...)
		}
		return nil
	})
	return out, err
}

// discoverCPP detects C++ FIDL servers declared via a `public
// fidl::WireServer<NS::Proto>` / `fidl::Server<NS::Proto>` base clause.
//
// A class can serve several protocols at once through multiple
// inheritance, and the base list often spans multiple lines:
//
//	class DriverRunner : public fidl::WireServer<fuchsia_driver_framework::CompositeNodeManager>,
//	                     public fidl::WireServer<fuchsia_driver_index::DriverNotifier>,
//	                     public fidl::Server<fuchsia_driver_token::NodeBusTopology>,
//	                     public NodeManager { ... };
//
// The earlier implementation anchored each pattern on `class <name> ...
// fidl::Server<...>` and so only ever recorded the FIRST server base per
// class, silently dropping the 2nd+ protocols (a recall bug; precision
// was already perfect). We now isolate the base-clause region once per
// class, then enumerate EVERY server base inside it, emitting one
// IMPLEMENTS edge per distinct protocol — all on a single element for the
// class, since one C++ class is one implementor. Precision safeguards are
// unchanged: the template argument is still parsed with the same
// `[\w:]+` shape and run through resolveFIDLProtocol + libraryInScope, so
// non-server bases (plain mixins) and out-of-scope protocols are dropped
// exactly as before.
func (a *Adapter) discoverCPP(rel string, body []byte, scope adapters.ScopeConfig) []*contractpb.ContractElement {
	var out []*contractpb.ContractElement
	for _, m := range a.cppClassHeader.FindAllSubmatchIndex(body, -1) {
		if len(m) < 6 {
			continue
		}
		className := string(body[m[2]:m[3]])
		region := body[m[4]:m[5]]
		regionStart := m[4]
		// Enumerate every fidl::(Wire)?Server<...> base in this class's
		// base list, deduping repeated protocols. Each surviving protocol
		// becomes one IMPLEMENTS relationship on the class element, with a
		// DeclarationSite pointing at the base's own line.
		var rels []*contractpb.Relationship
		seenTarget := map[string]bool{}
		for _, bm := range a.cppServerBase.FindAllSubmatchIndex(region, -1) {
			if len(bm) < 4 {
				continue
			}
			nsProto := string(region[bm[2]:bm[3]])
			targetID, lib := resolveFIDLProtocol(nsProto)
			if targetID == "" || !libraryInScope(lib, scope) {
				continue
			}
			if seenTarget[targetID] {
				continue
			}
			seenTarget[targetID] = true
			baseLine := lineFromOffset(body, regionStart+bm[0])
			rels = append(rels, &contractpb.Relationship{
				Kind:            contractpb.RelationshipKind_IMPLEMENTS,
				TargetElementId: targetID,
				Note:            fmt.Sprintf("%s implementation of %s", className, targetID),
				DeclarationSite: &commonpb.SourceLocation{Path: rel, Line: uint32(baseLine)},
			})
		}
		if len(rels) == 0 {
			continue
		}
		classLine := lineFromOffset(body, m[0])
		out = append(out, &contractpb.ContractElement{
			Id:        "cpp:" + rel + "#" + className,
			Kind:      contractpb.ContractElementKind_CPP_CLASS,
			Ecosystem: "cpp",
			Library:   "", // implementation classes don't belong to a FIDL library
			Location: &commonpb.SourceLocation{
				Path: rel,
				Line: uint32(classLine),
			},
			Relationships: rels,
		})
	}
	// Project-supplied extension patterns keep the legacy one-shot
	// (class, proto) shape — one edge per match, no base-region scan.
	for _, rx := range a.cppExtraPatterns {
		for _, m := range rx.FindAllSubmatchIndex(body, -1) {
			if len(m) < 6 {
				continue
			}
			className := string(body[m[2]:m[3]])
			nsProto := string(body[m[4]:m[5]])
			targetID, lib := resolveFIDLProtocol(nsProto)
			if targetID == "" || !libraryInScope(lib, scope) {
				continue
			}
			line := lineFromOffset(body, m[0])
			out = append(out, newImplElement("cpp", contractpb.ContractElementKind_CPP_CLASS, rel, className, targetID, line))
		}
	}
	return out
}

// discoverRust handles both the v1 trait-impl shape and the v2 Fuchsia
// async-server idiom. The v2 patterns reference the protocol through a
// crate alias (`use fidl_fuchsia_sys2 as fsys;`), so we first build the
// per-file alias map, then alias-resolve each qualifier before handing
// it to resolveFIDLProtocol. A qualifier that does not map to a FIDL
// crate is dropped, which is the principal false-positive guard.
//
// A single server file typically produces many hits for one protocol
// (one per match arm plus the stream type). We dedup per
// (synthetic-implementor, target) so each file emits one element per
// protocol it serves, anchored at the first occurrence.
func (a *Adapter) discoverRust(rel string, body []byte, scope adapters.ScopeConfig) []*contractpb.ContractElement {
	aliases := a.rustAliasMap(body)
	var out []*contractpb.ContractElement
	seen := map[string]bool{} // element ID -> already emitted
	for _, p := range a.rustPatterns {
		for _, m := range p.rx.FindAllSubmatchIndex(body, -1) {
			lo := 2 * p.protoGroup
			if lo+1 >= len(m) || m[lo] < 0 {
				continue
			}
			nsProto := resolveRustAlias(string(body[m[lo]:m[lo+1]]), aliases)
			// FP guard for the bare server idioms: the qualifier must
			// resolve to a FIDL crate. `internal::JobRequest::Run` (a
			// local enum) has no fidl_ head and is dropped here.
			if p.requireFidl && !strings.HasPrefix(nsProto, "fidl_") {
				continue
			}
			targetID, lib := resolveFIDLProtocol(nsProto)
			if targetID == "" || !libraryInScope(lib, scope) {
				continue
			}
			// Implementor name: explicit (group present) or synthesized
			// from the protocol when the idiom is a free function.
			var className string
			if p.nameGroup > 0 {
				ni := 2 * p.nameGroup
				if ni+1 < len(m) && m[ni] >= 0 {
					className = string(body[m[ni]:m[ni+1]])
				}
			}
			if className == "" {
				// targetID is "lib/Proto"; take the protocol leaf.
				proto := targetID
				if i := strings.LastIndex(proto, "/"); i >= 0 {
					proto = proto[i+1:]
				}
				className = proto + "Server"
			}
			id := "rust:" + rel + "#" + className
			if seen[id] {
				continue
			}
			seen[id] = true
			line := lineFromOffset(body, m[0])
			out = append(out, newImplElement("rust", contractpb.ContractElementKind_RUST_TYPE, rel, className, targetID, line))
		}
	}
	return out
}

// rustAliasMap returns the `use fidl_... as <alias>` mapping for one
// file, e.g. {"fsys": "fidl_fuchsia_sys2"}.
func (a *Adapter) rustAliasMap(body []byte) map[string]string {
	out := map[string]string{}
	for _, m := range a.rustAlias.FindAllSubmatch(body, -1) {
		if len(m) >= 3 {
			out[string(m[2])] = string(m[1])
		}
	}
	return out
}

// resolveRustAlias rewrites the leading qualifier of a `<ns>::<Rest>`
// reference using the file's alias map. `fsys::RealmQueryRequest`
// becomes `fidl_fuchsia_sys2::RealmQueryRequest` when `fsys` is aliased;
// references whose head is already a full `fidl_...` path, or has no
// alias, pass through unchanged (and a non-FIDL head is dropped later by
// resolveFIDLProtocol).
func resolveRustAlias(nsProto string, aliases map[string]string) string {
	i := strings.Index(nsProto, "::")
	if i < 0 {
		return nsProto
	}
	head := nsProto[:i]
	if crate, ok := aliases[head]; ok {
		return crate + nsProto[i:]
	}
	return nsProto
}

// newImplElement builds the ContractElement carrying the IMPLEMENTS edge
// to the FIDL protocol. Shared by the C++ and Rust paths.
func newImplElement(ecosystem string, kind contractpb.ContractElementKind, rel, className, targetID string, line int) *contractpb.ContractElement {
	return &contractpb.ContractElement{
		Id:        ecosystem + ":" + rel + "#" + className,
		Kind:      kind,
		Ecosystem: ecosystem,
		Library:   "", // implementation classes don't belong to a FIDL library
		Location: &commonpb.SourceLocation{
			Path: rel,
			Line: uint32(line),
		},
		Relationships: []*contractpb.Relationship{{
			Kind:            contractpb.RelationshipKind_IMPLEMENTS,
			TargetElementId: targetID,
			Note:            fmt.Sprintf("%s implementation of %s", className, targetID),
			DeclarationSite: &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
		}},
	}
}

// resolveFIDLProtocol turns source notation into a sheaf element ID.
// Examples:
//
//	"fuchsia_io::Directory"            -> ("fuchsia.io/Directory", "fuchsia.io")
//	"fuchsia_driver_framework::Driver" -> ("fuchsia.driver.framework/Driver", "fuchsia.driver.framework")
//	"fidl_fuchsia_io::DirectoryRequest" (Rust form) -> ("fuchsia.io/Directory", "fuchsia.io")
//	"fuchsia_io_admin::Admin"          -> ("fuchsia.io.admin/Admin", "fuchsia.io.admin")
//
// Returns ("", "") if the input doesn't look like a FIDL protocol ref.
func resolveFIDLProtocol(nsProto string) (string, string) {
	// Split on ::; last segment is protocol, others are library parts.
	parts := strings.Split(nsProto, "::")
	if len(parts) < 2 {
		return "", ""
	}
	proto := parts[len(parts)-1]
	libParts := parts[:len(parts)-1]
	// Strip optional Rust prefix "fidl_" on the first segment.
	if len(libParts) > 0 && strings.HasPrefix(libParts[0], "fidl_") {
		libParts[0] = strings.TrimPrefix(libParts[0], "fidl_")
	}
	// Rust often emits TypeNameRequest / TypeNameMarker; strip those
	// suffixes from the protocol name so we land on the FIDL protocol.
	for _, suf := range []string{"Request", "Marker", "RequestStream"} {
		if strings.HasSuffix(proto, suf) {
			proto = strings.TrimSuffix(proto, suf)
			break
		}
	}
	// The C/C++ binding mangles dots into underscores: fuchsia_io = fuchsia.io
	libParts = strings.Split(strings.Join(libParts, "."), "_")
	lib := strings.Join(libParts, ".")
	if lib == "" || proto == "" {
		return "", ""
	}
	return lib + "/" + proto, lib
}

// libraryInScope mirrors the helper in the fidl adapter.
func libraryInScope(lib string, scope adapters.ScopeConfig) bool {
	for _, ex := range scope.Exclude {
		if matchLibrary(ex, lib) {
			return false
		}
	}
	if len(scope.Libraries) == 0 && len(scope.AlsoInclude) == 0 {
		return true
	}
	for _, l := range scope.Libraries {
		if matchLibrary(l, lib) {
			return true
		}
	}
	for _, l := range scope.AlsoInclude {
		if matchLibrary(l, lib) {
			return true
		}
	}
	return false
}

func matchLibrary(pattern, lib string) bool {
	if pattern == lib {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(lib, prefix)
	}
	return false
}

func lineFromOffset(body []byte, off int) int {
	line := 1
	for i := 0; i < off && i < len(body); i++ {
		if body[i] == '\n' {
			line++
		}
	}
	return line
}
