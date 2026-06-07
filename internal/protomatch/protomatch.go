// Package protomatch extracts likely proto/gRPC contract references
// from text — typically fenced code blocks inside markdown docs, or
// raw doc bodies. It returns candidate ContractElement IDs that the
// indexer can verify against the corpus. The matcher is deliberately
// liberal — it emits any ID it can plausibly construct; the indexer
// drops ones that don't resolve.
//
// Patterns recognized inside a block:
//
//   - `package pkg.path;`             — sets in-block package scope.
//   - `service Name { ... }`          — emits "pkg.path/Name" and
//     sets in-block service scope
//     to Name.
//   - `rpc Name(...)`                 — when a service is in scope,
//     emits "pkg.path/Service.Name".
//     When no service is in scope,
//     emits the bare "Name" as a
//     fuzzy candidate.
//   - `pkg.path.Service`              — bare FQDN; emits both slash-
//     form "pkg.path/Service" and
//     dotted "pkg.path.Service" so
//     the indexer's alias matcher
//     catches whichever form the
//     element uses.
//   - `pkg.path.Service.Method`       — bare FQDN method; emits both
//     slash and dotted forms.
//   - `host:port/pkg.path.Service/Method` — grpcurl-style invocation;
//     emits the methodref forms.
//
// Refs are de-duplicated before return.
package protomatch

import (
	"regexp"
	"sort"
	"strings"
)

var (
	// `package foo.bar.v1;` — captures the package path.
	packageRx = regexp.MustCompile(`(?m)^\s*package\s+([a-z][a-z0-9_]*(?:\.[a-z0-9_][a-z0-9_]*)*)\s*;`)

	// `service Foo {`        — captures the service name.
	serviceRx = regexp.MustCompile(`(?m)^\s*service\s+([A-Z][A-Za-z0-9_]*)\s*\{`)

	// `rpc Foo(`             — captures the rpc method name.
	rpcRx = regexp.MustCompile(`(?m)^\s*rpc\s+([A-Z][A-Za-z0-9_]*)\s*\(`)

	// `pkg.path.Service` or `pkg.path.Service.Method` — bare FQDN.
	// Must begin with a lowercase segment (the package) and contain
	// at least one capitalized segment (the type/service). Anchored
	// with non-word boundaries to avoid matching identifiers like
	// foo.barBaz embedded in larger tokens.
	fqdnRx = regexp.MustCompile(`\b([a-z][a-z0-9_]*(?:\.[a-z0-9_][a-z0-9_]*)*)\.([A-Z][A-Za-z0-9_]*)(?:\.([A-Z][A-Za-z0-9_]*))?\b`)

	// grpcurl-style invocation: `<anything>/pkg.path.Service/Method`.
	// The leading `/` and trailing `/Method` distinguish from a bare
	// FQDN. Picks up `grpc_cli call host:port /grpc.health.v1.Health/Check`
	// and `grpcurl -d ... host:50051 grpc.health.v1.Health/Check`.
	grpcurlRx = regexp.MustCompile(`\b([a-z][a-z0-9_]*(?:\.[a-z0-9_][a-z0-9_]*)*)\.([A-Z][A-Za-z0-9_]*)/([A-Z][A-Za-z0-9_]*)\b`)
)

// Extract scans body for proto-flavored references and returns
// candidate ContractElement IDs (sorted, deduplicated). The body
// is processed top-down so `package` declarations earlier in the
// block establish scope for `service` / `rpc` declarations that
// follow.
func Extract(body string) []string {
	if body == "" {
		return nil
	}
	refs := make(map[string]struct{}, 16)
	add := func(s string) {
		if s != "" {
			refs[s] = struct{}{}
		}
	}

	// Pass 1: package + service + rpc, scope-tracked.
	scanScoped(body, add)

	// Pass 2: bare FQDNs anywhere in the block.
	for _, m := range fqdnRx.FindAllStringSubmatch(body, -1) {
		pkg, svc, method := m[1], m[2], m[3]
		// Skip very common false positives.
		if isWellKnownNoise(pkg) {
			continue
		}
		if method == "" {
			// pkg.Service
			add(pkg + "/" + svc)
			add(pkg + "." + svc)
		} else {
			// pkg.Service.Method
			add(pkg + "/" + svc + "." + method)
			add(pkg + "." + svc + "." + method)
		}
	}

	// Pass 3: grpcurl `/Method` invocations.
	for _, m := range grpcurlRx.FindAllStringSubmatch(body, -1) {
		pkg, svc, method := m[1], m[2], m[3]
		if isWellKnownNoise(pkg) {
			continue
		}
		add(pkg + "/" + svc + "." + method)
		add(pkg + "." + svc + "." + method)
	}

	out := make([]string, 0, len(refs))
	for r := range refs {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// scanScoped walks body in source order, tracking the current
// `package` and `service` scope so that an unqualified `rpc Method`
// inside `service Foo { ... }` inside `package x.y;` emits "x.y/Foo.Method".
func scanScoped(body string, add func(string)) {
	type tok struct {
		pos  int
		kind string // "pkg" | "svc" | "rpc"
		name string
	}
	var toks []tok
	for _, idx := range packageRx.FindAllStringSubmatchIndex(body, -1) {
		toks = append(toks, tok{pos: idx[0], kind: "pkg", name: body[idx[2]:idx[3]]})
	}
	for _, idx := range serviceRx.FindAllStringSubmatchIndex(body, -1) {
		toks = append(toks, tok{pos: idx[0], kind: "svc", name: body[idx[2]:idx[3]]})
	}
	for _, idx := range rpcRx.FindAllStringSubmatchIndex(body, -1) {
		toks = append(toks, tok{pos: idx[0], kind: "rpc", name: body[idx[2]:idx[3]]})
	}
	sort.SliceStable(toks, func(i, j int) bool { return toks[i].pos < toks[j].pos })

	var pkg, svc string
	for _, t := range toks {
		switch t.kind {
		case "pkg":
			pkg = t.name
		case "svc":
			svc = t.name
			if pkg != "" {
				add(pkg + "/" + svc)
				add(pkg + "." + svc)
			} else {
				add(svc)
			}
		case "rpc":
			if pkg != "" && svc != "" {
				add(pkg + "/" + svc + "." + t.name)
				add(pkg + "." + svc + "." + t.name)
			} else if svc != "" {
				add(svc + "." + t.name)
			} else {
				// Bare rpc with no service or package in scope —
				// emit as a weak suffix candidate. The indexer's
				// suffix-fuzzy matcher will pick it up if any
				// element ends with ".Method".
				add(t.name)
			}
		}
	}
}

// isWellKnownNoise filters out common dotted-token false positives:
// hostnames, file paths, version strings, and stdlib namespaces that
// happen to share the lowercase.lowercase.Type shape we're looking
// for but never refer to a contract element.
func isWellKnownNoise(pkg string) bool {
	switch pkg {
	case "www", "http", "https", "io", "com", "net", "org":
		return true
	}
	// "google.protobuf.*" — the well-knowns we deliberately skip so
	// they don't pollute every join. If a project legitimately maps
	// them as contract elements, the indexer will still find them
	// via the alias path; this filter just stops emitting candidates
	// for them.
	if strings.HasPrefix(pkg, "google.protobuf") {
		return true
	}
	return false
}
