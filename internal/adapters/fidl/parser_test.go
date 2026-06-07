package fidl

import (
	"strings"
	"testing"
)

func TestParse_LibraryAndImports(t *testing.T) {
	src := `library fuchsia.io;
using zx;
using foo.bar.baz;`
	f, err := Parse(src, "test.fidl")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Library != "fuchsia.io" {
		t.Errorf("Library = %q, want fuchsia.io", f.Library)
	}
	if len(f.Imports) != 2 {
		t.Errorf("Imports = %v, want 2", f.Imports)
	}
}

func TestParse_ProtocolWithMethods(t *testing.T) {
	src := `library demo;

/// A directory of nodes.
protocol Directory {
    /// Opens a child node.
    Open(struct { name string; flags uint32; }) -> (struct { node Node; });

    /// Closes the directory.
    Close() -> () error int32;

    /// One-way ping.
    Ping();
};`
	f, _ := Parse(src, "demo.fidl")
	if len(f.Protocols) != 1 {
		t.Fatalf("Protocols = %d, want 1", len(f.Protocols))
	}
	p := f.Protocols[0]
	if p.Name != "Directory" {
		t.Errorf("Name = %q, want Directory", p.Name)
	}
	if !strings.Contains(p.Doc, "directory of nodes") {
		t.Errorf("Doc = %q", p.Doc)
	}
	if len(p.Methods) != 3 {
		t.Fatalf("Methods = %d, want 3", len(p.Methods))
	}
	open := p.Methods[0]
	if open.Name != "Open" || !open.HasResponse || open.HasError {
		t.Errorf("Open = %+v", open)
	}
	close := p.Methods[1]
	if close.Name != "Close" || !close.HasResponse || !close.HasError {
		t.Errorf("Close = %+v", close)
	}
	ping := p.Methods[2]
	if ping.Name != "Ping" || ping.HasResponse {
		t.Errorf("Ping = %+v", ping)
	}
}

func TestParse_ProtocolWithCompose(t *testing.T) {
	src := `library demo;
protocol Directory {
    compose Openable;
    compose Node;
    compose Queryable;
    Reread();
};`
	f, _ := Parse(src, "demo.fidl")
	p := f.Protocols[0]
	if len(p.Composes) != 3 {
		t.Fatalf("Composes = %d, want 3", len(p.Composes))
	}
	for _, want := range []string{"Openable", "Node", "Queryable"} {
		found := false
		for _, c := range p.Composes {
			if c.Target == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing compose %s in %+v", want, p.Composes)
		}
	}
}

func TestParse_AvailableAnnotation(t *testing.T) {
	src := `library demo;
@available(added=7, deprecated=18, removed=HEAD, note="use Foo instead")
protocol OldProto {};`
	f, _ := Parse(src, "demo.fidl")
	p := f.Protocols[0]
	if p.Available == nil {
		t.Fatalf("Available is nil")
	}
	if p.Available.Added != "7" {
		t.Errorf("Added = %q", p.Available.Added)
	}
	if p.Available.Deprecated != "18" {
		t.Errorf("Deprecated = %q", p.Available.Deprecated)
	}
	if p.Available.Removed != "HEAD" {
		t.Errorf("Removed = %q", p.Available.Removed)
	}
	if !strings.Contains(p.Available.Note, "use Foo") {
		t.Errorf("Note = %q", p.Available.Note)
	}
}

func TestParse_LibraryAvailableAnnotation(t *testing.T) {
	// Library-level @available is how Fuchsia marks a whole FIDL
	// library deprecated, e.g. fuchsia.ui.scenic. The parser must
	// capture it onto File.LibraryAvailable so downstream emits a
	// LIBRARY-kind ContractElement.
	src := `@available(added=7, deprecated=13, removed=17)
library legacy.lib;
protocol Thing {};`
	f, _ := Parse(src, "legacy.fidl")
	if f.LibraryAvailable == nil {
		t.Fatal("LibraryAvailable is nil")
	}
	if f.LibraryAvailable.Deprecated != "13" {
		t.Errorf("Deprecated = %q; want 13", f.LibraryAvailable.Deprecated)
	}
	if f.LibraryAvailable.Removed != "17" {
		t.Errorf("Removed = %q; want 17", f.LibraryAvailable.Removed)
	}
	// The library-level annotation must NOT bleed into the next
	// protocol declaration.
	if len(f.Protocols) != 1 {
		t.Fatalf("expected 1 protocol; got %d", len(f.Protocols))
	}
	if f.Protocols[0].Available != nil {
		t.Errorf("library @available leaked into protocol: %+v", f.Protocols[0].Available)
	}
}

func TestParse_TypeDeclarations(t *testing.T) {
	src := `library demo;

/// A struct.
type Point = struct {
    x int32;
    y int32;
};

/// A table.
type Attributes = table {
    1: name string;
};

type Result = strict union {
    1: ok Point;
    2: err int32;
};

type Color = enum : uint8 {
    RED = 1;
    GREEN = 2;
    BLUE = 3;
};

type Flags = flexible bits : uint32 {
    A = 0x1;
    B = 0x2;
};`
	f, _ := Parse(src, "demo.fidl")
	if len(f.Types) != 5 {
		t.Fatalf("Types = %d, want 5", len(f.Types))
	}
	kinds := make(map[string]string)
	for _, t := range f.Types {
		kinds[t.Name] = t.Kind
	}
	for k, v := range map[string]string{
		"Point": "struct", "Attributes": "table", "Result": "union",
		"Color": "enum", "Flags": "bits",
	} {
		if got := kinds[k]; got != v {
			t.Errorf("type %s kind = %q, want %q", k, got, v)
		}
	}
}

func TestParse_Consts(t *testing.T) {
	src := `library demo;
const MAX_NAME uint64 = 255;
const PROTOCOL_NAME string = "fuchsia.io/Directory";`
	f, _ := Parse(src, "demo.fidl")
	if len(f.Constants) != 2 {
		t.Errorf("Constants = %d, want 2", len(f.Constants))
	}
}

func TestParse_Alias(t *testing.T) {
	src := `library demo;
/// Node name.
alias Name = string:255;`
	f, _ := Parse(src, "demo.fidl")
	if len(f.Aliases) != 1 || f.Aliases[0].Name != "Name" {
		t.Errorf("Aliases = %+v", f.Aliases)
	}
	if !strings.Contains(f.Aliases[0].Doc, "Node name") {
		t.Errorf("Doc = %q", f.Aliases[0].Doc)
	}
}

func TestParse_OpenClosedProtocol(t *testing.T) {
	src := `library demo;
open protocol A { Foo(); };
closed protocol B { Bar(); };`
	f, _ := Parse(src, "demo.fidl")
	if len(f.Protocols) != 2 {
		t.Fatalf("Protocols = %d", len(f.Protocols))
	}
	if f.Protocols[0].Openness != "open" || f.Protocols[1].Openness != "closed" {
		t.Errorf("Openness wrong: %s / %s", f.Protocols[0].Openness, f.Protocols[1].Openness)
	}
}

func TestParse_DocCommentAttachesToNextDecl(t *testing.T) {
	src := `library demo;

/// Doc for type Foo.
type Foo = struct {};

/// Doc for protocol Bar.
protocol Bar { Baz(); };`
	f, _ := Parse(src, "demo.fidl")
	if !strings.Contains(f.Types[0].Doc, "Doc for type Foo") {
		t.Errorf("type Foo doc = %q", f.Types[0].Doc)
	}
	if !strings.Contains(f.Protocols[0].Doc, "Doc for protocol Bar") {
		t.Errorf("protocol Bar doc = %q", f.Protocols[0].Doc)
	}
}

func TestParse_MalformedRecovery(t *testing.T) {
	// Garbage in the middle shouldn't kill parsing of subsequent decls.
	src := `library demo;
protocol Good { Hi(); };
/// garbage follows
@@@ this is not valid
type Recovered = struct {};`
	f, err := Parse(src, "demo.fidl")
	if err != nil {
		t.Fatalf("Parse should not fail: %v", err)
	}
	// `protocol Good` should still parse.
	found := false
	for _, p := range f.Protocols {
		if p.Name == "Good" {
			found = true
		}
	}
	if !found {
		t.Errorf("protocol Good lost; got protocols=%+v", f.Protocols)
	}
}

func TestParse_MethodParamTypeRefs(t *testing.T) {
	src := `library demo;
protocol P {
    Op(struct { node Node; flags Rights; }) -> (struct { result Result; });
};`
	f, _ := Parse(src, "demo.fidl")
	if len(f.Protocols) != 1 || len(f.Protocols[0].Methods) != 1 {
		t.Fatalf("parse failed")
	}
	m := f.Protocols[0].Methods[0]
	for _, want := range []string{"Node", "Rights"} {
		if !contains(m.ParamTypeRefs, want) {
			t.Errorf("ParamTypeRefs missing %q: %v", want, m.ParamTypeRefs)
		}
	}
	if !contains(m.ResultTypeRefs, "Result") {
		t.Errorf("ResultTypeRefs missing 'Result': %v", m.ResultTypeRefs)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
