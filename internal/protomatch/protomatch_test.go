package protomatch

import (
	"reflect"
	"sort"
	"testing"
)

func TestExtract_ProtoBlockWithPackageAndService(t *testing.T) {
	body := `
syntax = "proto3";
package grpc.health.v1;

service Health {
    rpc Check(HealthCheckRequest) returns (HealthCheckResponse);
    rpc Watch(HealthCheckRequest) returns (stream HealthCheckResponse);
}
`
	got := Extract(body)
	wantHas := []string{
		"grpc.health.v1/Health",
		"grpc.health.v1.Health",
		"grpc.health.v1/Health.Check",
		"grpc.health.v1.Health.Check",
		"grpc.health.v1/Health.Watch",
		"grpc.health.v1.Health.Watch",
	}
	for _, w := range wantHas {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

func TestExtract_ProtoBlockNoPackage(t *testing.T) {
	body := `
service Foo {
    rpc Bar(X) returns (Y);
}
`
	got := Extract(body)
	// With no package, we emit fuzzy-suffix candidates that the
	// indexer's "fuzzy ends-with" pass can pick up.
	wantHas := []string{"Foo", "Foo.Bar"}
	for _, w := range wantHas {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

func TestExtract_BareFQDN(t *testing.T) {
	body := `The grpc.health.v1.Health.Check rpc is documented elsewhere.`
	got := Extract(body)
	wantHas := []string{
		"grpc.health.v1/Health.Check",
		"grpc.health.v1.Health.Check",
	}
	for _, w := range wantHas {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

func TestExtract_GrpcurlInvocation(t *testing.T) {
	body := `$ grpcurl -d '{}' localhost:50051 grpc.health.v1.Health/Check`
	got := Extract(body)
	wantHas := []string{
		"grpc.health.v1/Health.Check",
		"grpc.health.v1.Health.Check",
	}
	for _, w := range wantHas {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

func TestExtract_GoogleProtobufNoisefilter(t *testing.T) {
	body := `The field uses google.protobuf.Duration for elapsed time.`
	got := Extract(body)
	// google.protobuf.* is filtered as noise.
	for _, ref := range got {
		if ref == "google.protobuf/Duration" || ref == "google.protobuf.Duration" {
			t.Errorf("google.protobuf.* should be filtered, but got %q", ref)
		}
	}
}

func TestExtract_Dedupe(t *testing.T) {
	body := `
package grpc.health.v1;
service Health {
    rpc Check(X) returns (Y);
}
And later: grpc.health.v1.Health.Check is mentioned again.
And again: grpc.health.v1.Health.Check.
`
	got := Extract(body)
	seen := map[string]int{}
	for _, r := range got {
		seen[r]++
	}
	for r, n := range seen {
		if n > 1 {
			t.Errorf("ref %q emitted %d times; should be deduped", r, n)
		}
	}
}

func TestExtract_Empty(t *testing.T) {
	if got := Extract(""); got != nil {
		t.Errorf("Extract(\"\") = %v, want nil", got)
	}
}

func TestExtract_SortedOutput(t *testing.T) {
	body := `
package grpc.health.v1;
service Health {
    rpc Watch(X) returns (Y);
    rpc Check(X) returns (Y);
}
`
	got := Extract(body)
	want := append([]string(nil), got...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("output not sorted: %v", got)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
