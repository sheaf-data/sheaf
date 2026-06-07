package proto

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func requireProtoc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not on PATH; skipping (install protobuf): %v", err)
	}
}

func TestAdapter_BasicServiceDiscovery(t *testing.T) {
	requireProtoc(t)
	repo := setupRepo(t, map[string]string{
		"proto/grpc/example/v1/echo.proto": `
syntax = "proto3";
package grpc.example.v1;

// Echo service: trivial demo for the proto adapter test.
service Echo {
    // SayHello returns a greeting.
    rpc SayHello(EchoRequest) returns (EchoResponse);
    // SayStream streams greetings.
    rpc SayStream(EchoRequest) returns (stream EchoResponse);
}

// EchoRequest carries the name to greet.
message EchoRequest {
    string name = 1;
}
message EchoResponse {
    string greeting = 1;
}

enum EchoMood {
    NEUTRAL = 0;
    FRIENDLY = 1;
}
`,
	})

	a := New(Config{
		Include:   []string{"proto/**/*.proto"},
		ProtoPath: []string{"proto"},
	})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	ids := elemIDs(elems)
	wantIDs := []string{
		"grpc.example.v1/Echo",
		"grpc.example.v1/Echo.SayHello",
		"grpc.example.v1/Echo.SayStream",
		"grpc.example.v1/EchoRequest",
		"grpc.example.v1/EchoResponse",
		"grpc.example.v1/EchoMood",
	}
	for _, w := range wantIDs {
		if !contains(ids, w) {
			t.Errorf("missing element %q in %v", w, ids)
		}
	}

	svc := findElem(elems, "grpc.example.v1/Echo")
	if svc == nil || svc.GetKind() != contractpb.ContractElementKind_PROTOCOL {
		t.Fatalf("Echo service missing or wrong kind")
	}
	if svc.GetEcosystem() != "proto" {
		t.Errorf("ecosystem = %q, want proto", svc.GetEcosystem())
	}

	hello := findElem(elems, "grpc.example.v1/Echo.SayHello")
	if hello == nil || hello.GetKind() != contractpb.ContractElementKind_METHOD {
		t.Fatalf("SayHello method missing or wrong kind")
	}
	if !strings.Contains(hello.GetDocCommentExcerpt(), "returns a greeting") {
		t.Errorf("SayHello doc = %q", hello.GetDocCommentExcerpt())
	}
	// Relationships: ACCEPTS_TYPE / RETURNS_TYPE.
	var accepts, returns bool
	for _, r := range hello.GetRelationships() {
		switch r.GetKind() {
		case contractpb.RelationshipKind_ACCEPTS_TYPE:
			if r.GetTargetElementId() == "grpc.example.v1/EchoRequest" {
				accepts = true
			}
		case contractpb.RelationshipKind_RETURNS_TYPE:
			if r.GetTargetElementId() == "grpc.example.v1/EchoResponse" {
				returns = true
			}
		}
	}
	if !accepts {
		t.Errorf("SayHello missing ACCEPTS_TYPE → EchoRequest; got %+v", hello.GetRelationships())
	}
	if !returns {
		t.Errorf("SayHello missing RETURNS_TYPE → EchoResponse")
	}

	// Streaming note.
	stream := findElem(elems, "grpc.example.v1/Echo.SayStream")
	if stream == nil {
		t.Fatal("SayStream method missing")
	}
	streamNoted := false
	for _, r := range stream.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_RETURNS_TYPE &&
			strings.Contains(r.GetNote(), "stream") {
			streamNoted = true
		}
	}
	if !streamNoted {
		t.Errorf("SayStream RETURNS_TYPE note should say 'stream'")
	}

	// Source location points at the repo-relative path
	// (SourceLocation.path is "repo-relative; forward-slash separated"
	// per common.proto, regardless of -I path normalization).
	if got := svc.GetLocation().GetPath(); got != "proto/grpc/example/v1/echo.proto" {
		t.Errorf("Echo location path = %q, want proto/grpc/example/v1/echo.proto", got)
	}
	if svc.GetLocation().GetLine() == 0 {
		t.Errorf("Echo location line is zero")
	}
}

func TestAdapter_ScopeFilter(t *testing.T) {
	requireProtoc(t)
	repo := setupRepo(t, map[string]string{
		"proto/keep/v1/keep.proto": `syntax="proto3"; package keep.v1; service K { rpc Ping(P) returns (P); } message P {}`,
		"proto/drop/v1/drop.proto": `syntax="proto3"; package drop.v1; service D { rpc Ping(P) returns (P); } message P {}`,
	})
	a := New(Config{
		Include:   []string{"proto/**/*.proto"},
		ProtoPath: []string{"proto"},
	})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{
		Libraries: []string{"keep.v1"},
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, e := range elems {
		if !strings.HasPrefix(e.GetId(), "keep.v1/") {
			t.Errorf("scope filter leaked element from %s: %s", e.GetLibrary(), e.GetId())
		}
	}
	if len(elems) == 0 {
		t.Errorf("expected some keep.v1 elements, got none")
	}
}

func TestAdapter_TransitiveImportsNotEmitted(t *testing.T) {
	requireProtoc(t)
	repo := setupRepo(t, map[string]string{
		"proto/dep/dep.proto": `syntax="proto3"; package dep; message Helper { string s = 1; }`,
		"proto/top/top.proto": `syntax="proto3"; package top; import "dep/dep.proto"; message Use { dep.Helper h = 1; }`,
	})
	a := New(Config{
		// Only include top.proto. dep.proto is pulled in by --include_imports
		// for type resolution but must NOT appear as elements.
		Include:   []string{"proto/top/*.proto"},
		ProtoPath: []string{"proto"},
	})
	elems, err := a.Discover(context.Background(), repo, adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, e := range elems {
		if e.GetLibrary() == "dep" {
			t.Errorf("transitive import leaked: %s", e.GetId())
		}
	}
	if findElem(elems, "top/Use") == nil {
		t.Errorf("expected top/Use, got %v", elemIDs(elems))
	}
}

func elemIDs(es []*contractpb.ContractElement) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.GetId()
	}
	return out
}

func findElem(es []*contractpb.ContractElement, id string) *contractpb.ContractElement {
	for _, e := range es {
		if e.GetId() == id {
			return e
		}
	}
	return nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
