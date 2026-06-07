// Package indexer: facadepass.go
//
// Facade post-pass. After all contract adapters have produced their
// elements, walks each element through BuildHints.FacadeOf using its
// Location.Path, and when that path identifies a backend module
// backing a facade module, emits IMPLEMENTS relationships from the
// backend element to every matching element in the facade module.
//
// "Matching element": the facade-side local name comes from the
// recognizer's BuildHints.FacadeSymbol convention when it has an opinion
// (e.g. Pigweed maps a backend's PW_HANDLE_<X> macro to the facade's
// PW_<X> macro), otherwise it falls back to same-local-name matching
// (the part of the ID after the final "/"). Keeping the convention in
// the recognizer lets each ecosystem carry its own implements rule
// without the generic indexer special-casing any one of them.
//
// The post-pass mutates the input elements in place — each backend
// element's Relationships slice gains an IMPLEMENTS edge for every
// matched facade element. Mirrors how INHERITED_FROM and SAME_AS
// edges are injected by other indexer passes. Idempotent: running
// it twice does not duplicate edges.

package indexer

import (
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// ImplementsEdge is one resolved backend→facade relationship.
type ImplementsEdge struct {
	BackendElementID string
	FacadeElementID  string
}

// RunFacadePass implements the facade post-pass described above.
// Returns the list of edges it emitted (for stats / diagnostics) and
// also mutates each backend element's Relationships in place with the
// corresponding IMPLEMENTS entries.
func RunFacadePass(hints adapters.BuildHints, elements []*contractpb.ContractElement) []ImplementsEdge {
	if hints == nil {
		return nil
	}
	if _, isNop := hints.(adapters.NopHints); isNop {
		return nil
	}

	// Index facade-module elements by (library, localName) so we can
	// look up matches efficiently. The facade module appears as an
	// element's Library (e.g. cppheader stamps Library=pw_chrono on
	// every element discovered under pw_chrono/public/...).
	byLibLocal := make(map[string]map[string][]*contractpb.ContractElement)
	for _, e := range elements {
		lib := e.GetLibrary()
		if lib == "" {
			continue
		}
		local := localName(e.GetId())
		if local == "" {
			continue
		}
		if byLibLocal[lib] == nil {
			byLibLocal[lib] = map[string][]*contractpb.ContractElement{}
		}
		byLibLocal[lib][local] = append(byLibLocal[lib][local], e)
	}

	var edges []ImplementsEdge
	for _, backend := range elements {
		// Identify the backend module + its facade. Prefer the precise
		// path mapping; fall back to the module-level backing relation
		// for elements whose own header the build graph does not list
		// directly (Pigweed routes facade glue through public_overrides/,
		// so the symbol-defining header is not the named backend header).
		facadeModule, backendModule, ok := hints.FacadeOf(backend.GetLocation().GetPath())
		if ok {
			// Guard: the backend element's library should equal the
			// reported backend module. Otherwise something is off — e.g.
			// an element from a different ecosystem happens to live at a
			// path the build-graph claims as a backend; don't link.
			if backend.GetLibrary() != backendModule {
				continue
			}
		} else if lib := backend.GetLibrary(); lib != "" {
			if fm, mok := hints.FacadeModule(lib); mok {
				facadeModule, backendModule, ok = fm, lib, true
			}
		}
		if !ok {
			continue
		}
		local := localName(backend.GetId())
		if local == "" {
			continue
		}
		// Resolve the facade-side local name. The recognizer's
		// convention (e.g. Pigweed's PW_HANDLE_<X> -> PW_<X>) takes
		// precedence; absent an opinion, fall back to same-local-name.
		facadeLocal := local
		if mapped, ok := hints.FacadeSymbol(backendModule, local); ok {
			facadeLocal = mapped
		}
		matches := byLibLocal[facadeModule][facadeLocal]
		// Deterministic order.
		sorted := append([]*contractpb.ContractElement(nil), matches...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetId() < sorted[j].GetId() })
		for _, facadeElem := range sorted {
			if facadeElem.GetId() == backend.GetId() {
				continue
			}
			if hasImplementsEdge(backend, facadeElem.GetId()) {
				continue
			}
			backend.Relationships = append(backend.Relationships, &contractpb.Relationship{
				Kind:            contractpb.RelationshipKind_IMPLEMENTS,
				TargetElementId: facadeElem.GetId(),
				Note:            "pw_facade backend (" + backendModule + " → " + facadeModule + ")",
			})
			edges = append(edges, ImplementsEdge{
				BackendElementID: backend.GetId(),
				FacadeElementID:  facadeElem.GetId(),
			})
		}
	}
	return edges
}

// localName returns the part of an element ID after the final "/".
// For IDs with no slash the whole string is returned.
func localName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// hasImplementsEdge reports whether e already has an IMPLEMENTS edge
// pointing at targetID — keeps the post-pass idempotent and prevents
// double-counting when the adapter already emitted the same edge.
func hasImplementsEdge(e *contractpb.ContractElement, targetID string) bool {
	for _, r := range e.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_IMPLEMENTS &&
			r.GetTargetElementId() == targetID {
			return true
		}
	}
	return false
}
