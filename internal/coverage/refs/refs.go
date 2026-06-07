// Package refs centralizes how the rest of Sheaf enumerates doc
// references from a CoverageProfile.
//
// Background: Reference doc-claims can come from several adapters
// (fidldoc, clidoc, markdowncli, and any future ecosystem-specific
// adapter that ships). The CoverageProfile proto carries the FIDL-
// shaped ones as typed fields (fidldoc, clidoc) and the rest in a
// `by_adapter` map keyed by adapter name.
//
// Without this package, every place that wants "all reference refs
// for this element" has to know the typed-field names plus iterate
// the map by hand. That doesn't scale — adding a new adapter means
// editing every consumer. The helpers here are the single point of
// expansion: add an adapter and every analyzer, the PR-bot, the
// HTML reporter, and the CLI pick up its refs automatically.
//
// Use AllReferenceRefs for "documented?" questions, HasReference
// for the boolean, and ByAdapter when you need to attribute to a
// specific source (rare).

package refs

import (
	"sort"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
)

// AllReferenceRefs returns every rendered-reference DocRef on the
// given Reference message, across typed fields AND the by_adapter
// map. Order is deterministic: typed fields first in declaration
// order, then by_adapter entries in sorted key order. The legacy
// `dockerdoc` typed field is enumerated too — kept around for
// back-compat with already-serialized profiles; new scans should
// route through by_adapter instead.
func AllReferenceRefs(r *coveragepb.DocCoverage_Reference) []*commonpb.DocRef {
	if r == nil {
		return nil
	}
	var out []*commonpb.DocRef
	out = append(out, r.GetFidldoc()...)
	out = append(out, r.GetClidoc()...)
	out = append(out, r.GetDockerdoc()...)
	out = append(out, byAdapterInOrder(r)...)
	return out
}

// HasReferenceDocs reports whether the Reference message contains at
// least one rendered-reference ref (across all sources). Cheaper than
// calling len(AllReferenceRefs) when you only need the boolean.
func HasReferenceDocs(r *coveragepb.DocCoverage_Reference) bool {
	if r == nil {
		return false
	}
	if len(r.GetFidldoc()) > 0 || len(r.GetClidoc()) > 0 || len(r.GetDockerdoc()) > 0 {
		return true
	}
	for _, list := range r.GetByAdapter() {
		if len(list.GetRefs()) > 0 {
			return true
		}
	}
	return false
}

// CountReferenceRefs returns the total number of rendered-reference
// refs across all sources.
func CountReferenceRefs(r *coveragepb.DocCoverage_Reference) int {
	if r == nil {
		return 0
	}
	n := len(r.GetFidldoc()) + len(r.GetClidoc()) + len(r.GetDockerdoc())
	for _, list := range r.GetByAdapter() {
		n += len(list.GetRefs())
	}
	return n
}

// ByAdapter returns the refs produced by a specific adapter. Honors
// both the typed fields (when adapter name is "fidldoc"/"clidoc")
// and the by_adapter map for everything else.
func ByAdapter(r *coveragepb.DocCoverage_Reference, adapter string) []*commonpb.DocRef {
	if r == nil {
		return nil
	}
	switch adapter {
	case "fidldoc":
		return r.GetFidldoc()
	case "clidoc":
		return r.GetClidoc()
	}
	if list := r.GetByAdapter()[adapter]; list != nil {
		return list.GetRefs()
	}
	return nil
}

// FirstReferencePath returns the path of the first rendered-reference
// ref in any source, or "". Used by analyzers that pick a single
// representative doc path (e.g. for staleness checks).
func FirstReferencePath(r *coveragepb.DocCoverage_Reference) string {
	if r == nil {
		return ""
	}
	if len(r.GetFidldoc()) > 0 {
		return r.GetFidldoc()[0].GetPath()
	}
	if len(r.GetClidoc()) > 0 {
		return r.GetClidoc()[0].GetPath()
	}
	if len(r.GetDockerdoc()) > 0 {
		return r.GetDockerdoc()[0].GetPath()
	}
	for _, key := range sortedAdapterKeys(r) {
		if refs := r.GetByAdapter()[key].GetRefs(); len(refs) > 0 {
			return refs[0].GetPath()
		}
	}
	return ""
}

// AppendRef appends a ref into the appropriate bucket. Typed adapter
// names route to the typed fields; everything else lands in
// by_adapter (lazily allocating the inner list message).
func AppendRef(r *coveragepb.DocCoverage_Reference, adapter string, ref *commonpb.DocRef) {
	switch adapter {
	case "fidldoc":
		r.Fidldoc = append(r.Fidldoc, ref)
	case "clidoc":
		r.Clidoc = append(r.Clidoc, ref)
	default:
		if r.ByAdapter == nil {
			r.ByAdapter = make(map[string]*coveragepb.DocCoverage_DocRefList)
		}
		list := r.ByAdapter[adapter]
		if list == nil {
			list = &coveragepb.DocCoverage_DocRefList{}
			r.ByAdapter[adapter] = list
		}
		list.Refs = append(list.Refs, ref)
	}
}

// UnionReference appends every ref from src onto dst, preserving the
// per-adapter bucket structure. Used by the SAME_AS evidence
// aggregation step to fold a sibling element's reference docs onto
// this element's profile.
func UnionReference(dst, src *coveragepb.DocCoverage_Reference) {
	if dst == nil || src == nil {
		return
	}
	dst.Fidldoc = append(dst.Fidldoc, src.Fidldoc...)
	dst.Clidoc = append(dst.Clidoc, src.Clidoc...)
	if len(src.ByAdapter) == 0 {
		return
	}
	if dst.ByAdapter == nil {
		dst.ByAdapter = make(map[string]*coveragepb.DocCoverage_DocRefList, len(src.ByAdapter))
	}
	for adapter, list := range src.ByAdapter {
		if list == nil || len(list.GetRefs()) == 0 {
			continue
		}
		existing := dst.ByAdapter[adapter]
		if existing == nil {
			existing = &coveragepb.DocCoverage_DocRefList{}
			dst.ByAdapter[adapter] = existing
		}
		existing.Refs = append(existing.Refs, list.GetRefs()...)
	}
}

func byAdapterInOrder(r *coveragepb.DocCoverage_Reference) []*commonpb.DocRef {
	var out []*commonpb.DocRef
	for _, key := range sortedAdapterKeys(r) {
		out = append(out, r.GetByAdapter()[key].GetRefs()...)
	}
	return out
}

func sortedAdapterKeys(r *coveragepb.DocCoverage_Reference) []string {
	if r == nil || len(r.GetByAdapter()) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.GetByAdapter()))
	for k := range r.GetByAdapter() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
