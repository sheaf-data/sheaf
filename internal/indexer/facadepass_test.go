package indexer

import (
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// stubHints lets a test declare arbitrary FacadeOf answers without
// pulling in pwfacade's BUILD.gn parser.
type stubFacadeHints struct {
	facade map[string][2]string
	// module maps a backend module name to its facade module, emulating
	// the module-level backing relation (FacadeModule).
	module map[string]string
	// sym maps a backend element's local name to a facade local name,
	// emulating a recognizer's implements-by-convention rule.
	sym map[string]string
}

func (s stubFacadeHints) IsPublic(string) (bool, bool) { return false, false }

func (s stubFacadeHints) FacadeOf(relPath string) (string, string, bool) {
	if v, ok := s.facade[relPath]; ok {
		return v[0], v[1], true
	}
	return "", "", false
}

func (s stubFacadeHints) FacadeModule(module string) (string, bool) {
	if v, ok := s.module[module]; ok {
		return v, true
	}
	return "", false
}

func (s stubFacadeHints) FacadeSymbol(_, backendLocalName string) (string, bool) {
	if v, ok := s.sym[backendLocalName]; ok {
		return v, true
	}
	return "", false
}

func TestFacadePass_NoHints(t *testing.T) {
	elems := []*contractpb.ContractElement{
		{Id: "pw_chrono/SystemClock", Library: "pw_chrono"},
		{Id: "pw_chrono_stl/SystemClock", Library: "pw_chrono_stl"},
	}
	// Snapshot to compare relationship lengths.
	before := make([]int, len(elems))
	for i, e := range elems {
		before[i] = len(e.GetRelationships())
	}
	edges := RunFacadePass(adapters.NopHints{}, elems)
	if len(edges) != 0 {
		t.Errorf("NopHints: want zero edges, got %d", len(edges))
	}
	for i, e := range elems {
		if len(e.GetRelationships()) != before[i] {
			t.Errorf("elem[%d] relationships mutated under NopHints: %d → %d", i, before[i], len(e.GetRelationships()))
		}
	}
}

func TestFacadePass_NilHints(t *testing.T) {
	if edges := RunFacadePass(nil, nil); edges != nil {
		t.Errorf("nil hints: want nil edges, got %v", edges)
	}
}

func TestFacadePass_Basic(t *testing.T) {
	hints := stubFacadeHints{facade: map[string][2]string{
		"pw_chrono_stl/public/pw_chrono_stl/system_clock.h": {"pw_chrono", "pw_chrono_stl"},
	}}
	facadeElem := &contractpb.ContractElement{
		Id:        "pw_chrono/SystemClock",
		Library:   "pw_chrono",
		Ecosystem: "cpp",
		Kind:      contractpb.ContractElementKind_CPP_CLASS,
		Location:  &commonpb.SourceLocation{Path: "pw_chrono/public/pw_chrono/system_clock.h", Line: 10},
	}
	backendElem := &contractpb.ContractElement{
		Id:        "pw_chrono_stl/SystemClock",
		Library:   "pw_chrono_stl",
		Ecosystem: "cpp",
		Kind:      contractpb.ContractElementKind_CPP_CLASS,
		Location:  &commonpb.SourceLocation{Path: "pw_chrono_stl/public/pw_chrono_stl/system_clock.h", Line: 10},
	}
	elems := []*contractpb.ContractElement{facadeElem, backendElem}
	edges := RunFacadePass(hints, elems)
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d: %+v", len(edges), edges)
	}
	if edges[0].BackendElementID != backendElem.Id || edges[0].FacadeElementID != facadeElem.Id {
		t.Errorf("edge: want (%q → %q), got %+v", backendElem.Id, facadeElem.Id, edges[0])
	}
	// The backend element's Relationships list should now contain
	// an IMPLEMENTS edge pointing at the facade element.
	if !hasImplementsEdge(backendElem, facadeElem.Id) {
		t.Errorf("backend element missing IMPLEMENTS edge: %+v", backendElem.Relationships)
	}
	// The facade element is untouched.
	if len(facadeElem.GetRelationships()) != 0 {
		t.Errorf("facade element mutated: %+v", facadeElem.Relationships)
	}
}

func TestFacadePass_NoMatch(t *testing.T) {
	// hints say backend.h belongs to (pw_chrono, pw_chrono_stl), but no
	// facade element with the matching local name exists in pw_chrono.
	hints := stubFacadeHints{facade: map[string][2]string{
		"pw_chrono_stl/public/pw_chrono_stl/system_clock.h": {"pw_chrono", "pw_chrono_stl"},
	}}
	backendElem := &contractpb.ContractElement{
		Id:        "pw_chrono_stl/SystemClock",
		Library:   "pw_chrono_stl",
		Ecosystem: "cpp",
		Kind:      contractpb.ContractElementKind_CPP_CLASS,
		Location:  &commonpb.SourceLocation{Path: "pw_chrono_stl/public/pw_chrono_stl/system_clock.h"},
	}
	// pw_chrono only has a DIFFERENT class, not SystemClock.
	facadeElem := &contractpb.ContractElement{
		Id:      "pw_chrono/SystemTimer",
		Library: "pw_chrono",
	}
	elems := []*contractpb.ContractElement{facadeElem, backendElem}
	edges := RunFacadePass(hints, elems)
	if len(edges) != 0 {
		t.Errorf("no matching facade element: want 0 edges, got %d", len(edges))
	}
	if len(backendElem.GetRelationships()) != 0 {
		t.Errorf("backend mutated despite no match: %+v", backendElem.Relationships)
	}
}

func TestFacadePass_Idempotent(t *testing.T) {
	hints := stubFacadeHints{facade: map[string][2]string{
		"pw_chrono_stl/public/pw_chrono_stl/system_clock.h": {"pw_chrono", "pw_chrono_stl"},
	}}
	facadeElem := &contractpb.ContractElement{
		Id:      "pw_chrono/SystemClock",
		Library: "pw_chrono",
	}
	backendElem := &contractpb.ContractElement{
		Id:       "pw_chrono_stl/SystemClock",
		Library:  "pw_chrono_stl",
		Location: &commonpb.SourceLocation{Path: "pw_chrono_stl/public/pw_chrono_stl/system_clock.h"},
	}
	elems := []*contractpb.ContractElement{facadeElem, backendElem}
	_ = RunFacadePass(hints, elems)
	_ = RunFacadePass(hints, elems)
	// Only one edge despite two passes.
	count := 0
	for _, r := range backendElem.GetRelationships() {
		if r.GetKind() == contractpb.RelationshipKind_IMPLEMENTS &&
			r.GetTargetElementId() == facadeElem.Id {
			count++
		}
	}
	if count != 1 {
		t.Errorf("want exactly 1 IMPLEMENTS edge after two passes, got %d", count)
	}
}

func TestFacadePass_ModuleLevelAndSymbolConvention(t *testing.T) {
	// Mirrors the pw_log case: the backend element's own header is NOT a
	// directly-named backend header (FacadeOf returns false), so the
	// post-pass falls back to the module-level backing relation. The
	// facade and backend share NO local name (PW_LOG vs PW_HANDLE_LOG),
	// so same-name matching would link nothing; the FacadeSymbol
	// convention bridges them.
	hints := stubFacadeHints{
		module: map[string]string{"pw_log_basic": "pw_log"},
		sym:    map[string]string{"PW_HANDLE_LOG": "PW_LOG"},
	}
	facadeElem := &contractpb.ContractElement{
		Id:      "pw_log/PW_LOG",
		Library: "pw_log",
	}
	backendElem := &contractpb.ContractElement{
		Id:       "pw_log_basic/PW_HANDLE_LOG",
		Library:  "pw_log_basic",
		Location: &commonpb.SourceLocation{Path: "pw_log_basic/public/pw_log_basic/log_basic.h"},
	}
	// A backend element with no symbol mapping and no same-name facade
	// match must NOT produce an edge.
	noise := &contractpb.ContractElement{
		Id:       "pw_log_basic/pw_Log",
		Library:  "pw_log_basic",
		Location: &commonpb.SourceLocation{Path: "pw_log_basic/public/pw_log_basic/log_basic.h"},
	}
	elems := []*contractpb.ContractElement{facadeElem, backendElem, noise}
	edges := RunFacadePass(hints, elems)
	if len(edges) != 1 {
		t.Fatalf("want exactly 1 edge (PW_HANDLE_LOG -> PW_LOG), got %d: %+v", len(edges), edges)
	}
	if edges[0].BackendElementID != backendElem.Id || edges[0].FacadeElementID != facadeElem.Id {
		t.Errorf("edge: want (%q -> %q), got %+v", backendElem.Id, facadeElem.Id, edges[0])
	}
	if !hasImplementsEdge(backendElem, facadeElem.Id) {
		t.Errorf("backend element missing IMPLEMENTS edge: %+v", backendElem.Relationships)
	}
}

func TestFacadePass_LibraryGuard(t *testing.T) {
	// hints report a path belonging to backend module pw_chrono_stl, but
	// the element at that path has Library = "something_else". The
	// post-pass should refuse the link to avoid bridging unrelated
	// elements just because they happen to live under the path.
	hints := stubFacadeHints{facade: map[string][2]string{
		"odd/path.h": {"pw_chrono", "pw_chrono_stl"},
	}}
	stray := &contractpb.ContractElement{
		Id:       "other_library/SystemClock",
		Library:  "other_library",
		Location: &commonpb.SourceLocation{Path: "odd/path.h"},
	}
	facadeElem := &contractpb.ContractElement{
		Id:      "pw_chrono/SystemClock",
		Library: "pw_chrono",
	}
	elems := []*contractpb.ContractElement{facadeElem, stray}
	if edges := RunFacadePass(hints, elems); len(edges) != 0 {
		t.Errorf("library guard: want 0 edges, got %d", len(edges))
	}
}
