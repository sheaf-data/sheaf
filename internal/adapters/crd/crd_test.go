package crd

import (
	"context"
	"testing"

	"github.com/sheaf-data/sheaf/internal/adapters"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// discover runs the adapter against the testdata directory and returns
// the emitted elements indexed by ID.
func runDiscover(t *testing.T, scope adapters.ScopeConfig) (map[string]*contractpb.ContractElement, []*contractpb.ContractElement) {
	t.Helper()
	a := New(Config{Include: []string{"**/*.yaml"}})
	elems, err := a.Discover(context.Background(), "testdata", scope)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	byID := make(map[string]*contractpb.ContractElement, len(elems))
	for _, e := range elems {
		byID[e.GetId()] = e
	}
	return byID, elems
}

func TestDiscover_FieldDiscovery(t *testing.T) {
	byID, all := runDiscover(t, adapters.ScopeConfig{})
	if len(all) == 0 {
		t.Fatal("expected non-empty element set")
	}

	// Every field down the nested + array paths must be discovered.
	wantFields := []string{
		"cert-manager.io/Certificate.spec",
		"cert-manager.io/Certificate.spec.renewBefore",
		"cert-manager.io/Certificate.spec.secretName",
		"cert-manager.io/Certificate.spec.duration",
		"cert-manager.io/Certificate.spec.dnsNames",
		"cert-manager.io/Certificate.spec.subject",
		"cert-manager.io/Certificate.spec.subject.organizations",
	}
	for _, id := range wantFields {
		e, ok := byID[id]
		if !ok {
			t.Errorf("missing expected field element %q", id)
			continue
		}
		if e.GetKind() != contractpb.ContractElementKind_CONFIG_KNOB {
			t.Errorf("%s: kind = %v, want CONFIG_KNOB", id, e.GetKind())
		}
	}
}

func TestDiscover_KindAssignment(t *testing.T) {
	byID, _ := runDiscover(t, adapters.ScopeConfig{})

	// TYPE for the CRD kind.
	typeEl, ok := byID["cert-manager.io/Certificate"]
	if !ok {
		t.Fatal("missing TYPE element cert-manager.io/Certificate")
	}
	if typeEl.GetKind() != contractpb.ContractElementKind_TYPE {
		t.Errorf("Certificate kind = %v, want TYPE", typeEl.GetKind())
	}

	// LIBRARY for the API group.
	grpEl, ok := byID["cert-manager.io"]
	if !ok {
		t.Fatal("missing LIBRARY element cert-manager.io")
	}
	if grpEl.GetKind() != contractpb.ContractElementKind_LIBRARY {
		t.Errorf("group kind = %v, want LIBRARY", grpEl.GetKind())
	}
}

func TestDiscover_Ecosystem(t *testing.T) {
	_, all := runDiscover(t, adapters.ScopeConfig{})
	for _, e := range all {
		if e.GetEcosystem() != "crd" {
			t.Errorf("%s: ecosystem = %q, want \"crd\" (never \"kubernetes\")", e.GetId(), e.GetEcosystem())
		}
		if e.GetLibrary() != "cert-manager.io" {
			t.Errorf("%s: library = %q, want \"cert-manager.io\"", e.GetId(), e.GetLibrary())
		}
	}
}

func TestDiscover_DescriptionExtraction(t *testing.T) {
	byID, _ := runDiscover(t, adapters.ScopeConfig{})

	// A field with a description must carry it.
	rb := byID["cert-manager.io/Certificate.spec.renewBefore"]
	if rb == nil {
		t.Fatal("missing renewBefore")
	}
	if got := rb.GetDocCommentExcerpt(); got != "How long before expiry a certificate should be renewed." {
		t.Errorf("renewBefore doc = %q, want the schema description", got)
	}

	// A field with NO description is a real undocumented finding: empty,
	// not synthesized.
	dur := byID["cert-manager.io/Certificate.spec.duration"]
	if dur == nil {
		t.Fatal("missing duration")
	}
	if got := dur.GetDocCommentExcerpt(); got != "" {
		t.Errorf("duration doc = %q, want empty (undocumented)", got)
	}
}

func TestDiscover_Location(t *testing.T) {
	byID, all := runDiscover(t, adapters.ScopeConfig{})

	// Every element must set a location with a real file:line.
	for _, e := range all {
		loc := e.GetLocation()
		if loc == nil {
			t.Errorf("%s: nil location", e.GetId())
			continue
		}
		if loc.GetPath() == "" {
			t.Errorf("%s: empty location path", e.GetId())
		}
		if loc.GetLine() == 0 {
			t.Errorf("%s: location line is 0", e.GetId())
		}
	}

	// Spot-check exact lines against the fixture (see
	// testdata/certificate-crd.yaml). The field element points at the
	// field's key line.
	cases := map[string]uint32{
		"cert-manager.io/Certificate.spec.renewBefore":           29,
		"cert-manager.io/Certificate.spec.secretName":            32,
		"cert-manager.io/Certificate.spec.duration":              35,
		"cert-manager.io/Certificate.spec.dnsNames":              37,
		"cert-manager.io/Certificate.spec.subject":               42,
		"cert-manager.io/Certificate.spec.subject.organizations": 46,
		"cert-manager.io": 10, // group declaration line
	}
	for id, wantLine := range cases {
		e := byID[id]
		if e == nil {
			t.Errorf("missing %q", id)
			continue
		}
		if got := e.GetLocation().GetLine(); got != wantLine {
			t.Errorf("%s: line = %d, want %d", id, got, wantLine)
		}
	}
}

func TestDiscover_ScopeFilter(t *testing.T) {
	// A scope naming a different group should yield nothing.
	_, all := runDiscover(t, adapters.ScopeConfig{Libraries: []string{"monitoring.coreos.com"}})
	if len(all) != 0 {
		t.Errorf("expected 0 elements for out-of-scope group, got %d", len(all))
	}

	// The matching group should yield elements.
	_, all = runDiscover(t, adapters.ScopeConfig{Libraries: []string{"cert-manager.io"}})
	if len(all) == 0 {
		t.Error("expected elements for in-scope group")
	}
}

func TestDiscoverWithDocs_InlineClaims(t *testing.T) {
	a := New(Config{Include: []string{"**/*.yaml"}})
	elems, claims, err := a.DiscoverWithDocs(context.Background(), "testdata", adapters.ScopeConfig{})
	if err != nil {
		t.Fatalf("DiscoverWithDocs: %v", err)
	}
	if len(elems) == 0 {
		t.Fatal("expected elements")
	}
	if len(claims) == 0 {
		t.Fatal("expected inline doc claims for described fields")
	}

	// A described field must produce exactly one REFERENCE claim pointing
	// back at the field's own element ID.
	byRef := map[string]*docclaimpb.DocClaim{}
	for _, c := range claims {
		if c.GetKind() != docclaimpb.DocClaimKind_REFERENCE {
			t.Errorf("claim %q: kind = %v, want REFERENCE", c.GetSourcePath(), c.GetKind())
		}
		if len(c.GetContractRefs()) != 1 {
			t.Errorf("claim has %d contract refs, want 1", len(c.GetContractRefs()))
			continue
		}
		byRef[c.GetContractRefs()[0]] = c
	}

	if _, ok := byRef["cert-manager.io/Certificate.spec.renewBefore"]; !ok {
		t.Error("expected an inline claim for the documented renewBefore field")
	}
	// The undocumented `duration` field must NOT yield a claim — it is a
	// real undocumented finding.
	if _, ok := byRef["cert-manager.io/Certificate.spec.duration"]; ok {
		t.Error("undocumented duration field should not have an inline claim")
	}
}

func TestDiscover_FieldMeta(t *testing.T) {
	byID, _ := runDiscover(t, adapters.ScopeConfig{})
	dns := byID["cert-manager.io/Certificate.spec.dnsNames"]
	if dns == nil {
		t.Fatal("missing dnsNames")
	}
	meta := dns.GetEcosystemMeta().AsMap()
	if meta["type"] != "array" {
		t.Errorf("dnsNames type meta = %v, want array", meta["type"])
	}
}
