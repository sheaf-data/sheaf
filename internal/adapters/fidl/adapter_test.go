package fidl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
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

func TestAdapter_BasicProtocolDiscovery(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"sdk/fidl/demo/foo.fidl": `library demo;
/// A directory protocol.
protocol Directory {
    compose Openable;
    /// Open opens a file.
    /// Returns ZX_ERR_NOT_FOUND if the path doesn't exist.
    Open(struct { name string; }) -> (struct { node Node; }) error int32;
};
type Node = struct { id uint32; };`,
	})
	a := New(Config{})
	elems, claims, err := a.DiscoverWithDocs(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(elems) != 3 { // Directory protocol + Open method + Node type
		t.Errorf("elems = %d, want 3; got %v", len(elems), elemIDs(elems))
	}

	dir := findElem(elems, "demo/Directory")
	if dir == nil || dir.GetKind() != contractpb.ContractElementKind_PROTOCOL {
		t.Fatalf("Directory protocol missing: %v", elems)
	}
	open := findElem(elems, "demo/Directory.Open")
	if open == nil || open.GetKind() != contractpb.ContractElementKind_METHOD {
		t.Fatalf("Open method missing")
	}
	if !strings.Contains(open.GetDocCommentExcerpt(), "Open opens a file") {
		t.Errorf("Open doc = %q", open.GetDocCommentExcerpt())
	}

	// COMPOSED_FROM relationship
	if len(dir.GetRelationships()) != 1 ||
		dir.GetRelationships()[0].GetKind() != contractpb.RelationshipKind_COMPOSED_FROM {
		t.Errorf("Directory composes not extracted: %+v", dir.GetRelationships())
	}

	// Doc claims for each documented element
	if len(claims) < 2 {
		t.Errorf("expected doc claims for documented protocol and method; got %d", len(claims))
	}
	// One claim should reference the canonical fuchsia.dev URL.
	for _, c := range claims {
		if strings.Contains(c.GetUrl(), "demo") {
			return
		}
	}
	t.Errorf("no claim had a URL containing 'demo'")
}

func TestAdapter_MultiFileLibrary(t *testing.T) {
	// A library spread across multiple files (like fuchsia.io).
	repo := setupRepo(t, map[string]string{
		"sdk/fidl/demo/a.fidl": `library demo;
protocol A { Hi(); };`,
		"sdk/fidl/demo/b.fidl": `library demo;
protocol B { Bye(); };`,
	})
	a := New(Config{})
	elems, _, err := a.DiscoverWithDocs(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if findElem(elems, "demo/A") == nil || findElem(elems, "demo/B") == nil {
		t.Errorf("missing protocols across files: %v", elemIDs(elems))
	}
}

func TestAdapter_ScopeFilter(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"a.fidl": `library wanted; protocol A { Foo(); };`,
		"b.fidl": `library skipped; protocol B { Bar(); };`,
	})
	a := New(Config{})
	elems, _, _ := a.DiscoverWithDocs(context.Background(), repo, adapters.ScopeConfig{
		Libraries: []string{"wanted"},
	})
	if findElem(elems, "wanted/A") == nil {
		t.Errorf("wanted/A missing")
	}
	if findElem(elems, "skipped/B") != nil {
		t.Errorf("skipped library should be filtered")
	}
}

func TestAdapter_ScopeWildcardExclude(t *testing.T) {
	repo := setupRepo(t, map[string]string{
		"a.fidl": `library fuchsia.io; protocol A { Foo(); };`,
		"b.fidl": `library fuchsia.test.thing; protocol B { Bar(); };`,
	})
	a := New(Config{})
	elems, _, _ := a.DiscoverWithDocs(context.Background(), repo, adapters.ScopeConfig{
		Libraries: []string{"fuchsia.io"},
		Exclude:   []string{"fuchsia.test.*"},
	})
	if findElem(elems, "fuchsia.io/A") == nil {
		t.Errorf("fuchsia.io/A missing")
	}
	if findElem(elems, "fuchsia.test.thing/B") != nil {
		t.Errorf("fuchsia.test.* should be excluded")
	}
}

func TestResolveComposeTarget(t *testing.T) {
	cases := []struct {
		curLib, target, want string
	}{
		{"fuchsia.io", "Openable", "fuchsia.io/Openable"},
		{"fuchsia.io", "fuchsia.unknown/Cloneable", "fuchsia.unknown/Cloneable"},
		{"fuchsia.io", "fuchsia.unknown.Cloneable", "fuchsia.unknown/Cloneable"},
	}
	for _, c := range cases {
		got := resolveComposeTarget(c.curLib, c.target)
		if got != c.want {
			t.Errorf("resolveComposeTarget(%q, %q) = %q, want %q", c.curLib, c.target, got, c.want)
		}
	}
}

func TestMakeInlineDocClaim_SubstanceGrades(t *testing.T) {
	cases := []struct {
		doc  string
		want commonpb.Substance
	}{
		{"", commonpb.Substance_ABSENT},
		{"Opens it.", commonpb.Substance_SIGNATURE_ONLY},
		{"Opens the given path and returns a handle.", commonpb.Substance_PARTIAL},
		{strings.Repeat("word ", 30), commonpb.Substance_SUBSTANTIVE},
	}
	for _, c := range cases {
		got := makeInlineDocClaim("demo/X", c.doc, "x.fidl", 1, "https://x/", "demo").GetSubstance()
		if got != c.want {
			t.Errorf("doc %q -> %v, want %v", c.doc, got, c.want)
		}
	}
}

func findElem(elems []*contractpb.ContractElement, id string) *contractpb.ContractElement {
	for _, e := range elems {
		if e.GetId() == id {
			return e
		}
	}
	return nil
}

func elemIDs(elems []*contractpb.ContractElement) []string {
	out := make([]string, len(elems))
	for i, e := range elems {
		out[i] = e.GetId()
	}
	return out
}
