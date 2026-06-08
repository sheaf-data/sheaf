// Package corpus is the in-memory store of everything an adapter run
// produced — contract elements, test cases, doc claims, and (post-
// indexing) coverage profiles. Built once per scan; treated as
// immutable by downstream consumers.
package corpus

import (
	"sort"
	"sync"

	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// Corpus collects all per-scan data. Concurrent inserts are safe;
// reads after the build phase are intended to be lock-free (callers
// must not mutate after handing off to readers).
type Corpus struct {
	mu        sync.Mutex
	elements  map[string]*contractpb.ContractElement
	tests     map[string]*testcasepb.TestCase
	docclaims []*docclaimpb.DocClaim
	profiles  map[string]*coveragepb.CoverageProfile
}

// New returns an empty Corpus.
func New() *Corpus {
	return &Corpus{
		elements: make(map[string]*contractpb.ContractElement),
		tests:    make(map[string]*testcasepb.TestCase),
		profiles: make(map[string]*coveragepb.CoverageProfile),
	}
}

// AddElement records a ContractElement. On an ID collision the
// DETERMINISTIC tier wins over the LLM tier: a reproducible schema/
// grammar extraction is higher-trust than an LLM proposal for the same
// element, and resolving collisions by tier (rather than arrival order)
// keeps the merge order-independent even though contract anchors run
// concurrently. Same-tier collisions keep last-write-wins, which is fine
// since deterministic adapters generally don't emit duplicates.
func (c *Corpus) AddElement(e *contractpb.ContractElement) {
	if e == nil || e.GetId() == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.elements[e.GetId()]; ok {
		if isLLMTier(e.GetProvenance()) && !isLLMTier(existing.GetProvenance()) {
			return // keep the deterministic row
		}
	}
	c.elements[e.GetId()] = e
}

func isLLMTier(p *commonpb.RowProvenance) bool {
	return p.GetTier() == commonpb.RowProvenance_LLM
}

// AddElements bulk-adds. Convenience for adapter output.
func (c *Corpus) AddElements(es []*contractpb.ContractElement) {
	for _, e := range es {
		c.AddElement(e)
	}
}

// AddTest records a TestCase.
func (c *Corpus) AddTest(t *testcasepb.TestCase) {
	if t == nil || t.GetId() == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tests[t.GetId()] = t
}

// AddTests bulk-adds.
func (c *Corpus) AddTests(ts []*testcasepb.TestCase) {
	for _, t := range ts {
		c.AddTest(t)
	}
}

// AddDocClaim records a DocClaim.
func (c *Corpus) AddDocClaim(d *docclaimpb.DocClaim) {
	if d == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.docclaims = append(c.docclaims, d)
}

// AddDocClaims bulk-adds.
func (c *Corpus) AddDocClaims(ds []*docclaimpb.DocClaim) {
	for _, d := range ds {
		c.AddDocClaim(d)
	}
}

// SetProfile stores a CoverageProfile keyed by element_id.
func (c *Corpus) SetProfile(p *coveragepb.CoverageProfile) {
	if p == nil || p.GetElementId() == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.profiles[p.GetElementId()] = p
}

// Element returns the ContractElement with id, or nil if absent.
func (c *Corpus) Element(id string) *contractpb.ContractElement {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.elements[id]
}

// ElementIDs returns sorted element IDs.
func (c *Corpus) ElementIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.elements))
	for id := range c.elements {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Elements returns a sorted snapshot.
func (c *Corpus) Elements() []*contractpb.ContractElement {
	ids := c.ElementIDs()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*contractpb.ContractElement, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.elements[id])
	}
	return out
}

// Tests returns a sorted snapshot of TestCases.
func (c *Corpus) Tests() []*testcasepb.TestCase {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.tests))
	for id := range c.tests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*testcasepb.TestCase, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.tests[id])
	}
	return out
}

// DocClaims returns the recorded DocClaims in a deterministic order.
//
// Insertion order is NOT stable across runs: doc adapters run concurrently, so
// the order claims land in c.docclaims varies. Downstream attribution
// (indexer.docClaimRefsElement → placeDocRef) is order-sensitive in its
// dedup/bucketing, so an unsorted slice made a report's per-element doc counts
// flip run-to-run (e.g. fuchsia.io/File.Close). Sorting here — mirroring
// Elements() and Tests(), which already sort — makes consumption deterministic.
func (c *Corpus) DocClaims() []*docclaimpb.DocClaim {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*docclaimpb.DocClaim, len(c.docclaims))
	copy(out, c.docclaims)
	sort.SliceStable(out, func(a, b int) bool { return docClaimLess(out[a], out[b]) })
	return out
}

// docClaimLess is a total ordering over DocClaims by stable, content-derived
// fields (no insertion-time identity), so the sort result is reproducible.
func docClaimLess(a, b *docclaimpb.DocClaim) bool {
	if x, y := a.GetSourcePath(), b.GetSourcePath(); x != y {
		return x < y
	}
	la, lb := a.GetLocation(), b.GetLocation()
	if x, y := la.GetLine(), lb.GetLine(); x != y {
		return x < y
	}
	if x, y := la.GetColumn(), lb.GetColumn(); x != y {
		return x < y
	}
	if x, y := a.GetKind(), b.GetKind(); x != y {
		return x < y
	}
	if x, y := a.GetAdapter(), b.GetAdapter(); x != y {
		return x < y
	}
	ra, rb := a.GetContractRefs(), b.GetContractRefs()
	for k := 0; k < len(ra) && k < len(rb); k++ {
		if ra[k] != rb[k] {
			return ra[k] < rb[k]
		}
	}
	if len(ra) != len(rb) {
		return len(ra) < len(rb)
	}
	return a.GetRawText() < b.GetRawText()
}

// Profile returns the CoverageProfile for elementID, or nil.
func (c *Corpus) Profile(elementID string) *coveragepb.CoverageProfile {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.profiles[elementID]
}

// Profiles returns all profiles, sorted by element_id.
func (c *Corpus) Profiles() []*coveragepb.CoverageProfile {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.profiles))
	for id := range c.profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*coveragepb.CoverageProfile, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.profiles[id])
	}
	return out
}

// Stats returns counts for quick reporting.
type Stats struct {
	Elements  int
	Tests     int
	DocClaims int
	Profiles  int
}

func (c *Corpus) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Elements:  len(c.elements),
		Tests:     len(c.tests),
		DocClaims: len(c.docclaims),
		Profiles:  len(c.profiles),
	}
}
