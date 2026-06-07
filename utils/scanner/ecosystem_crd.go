package scanner

import "strings"

// crdView is the EcosystemView for Kubernetes CustomResourceDefinition
// surfaces. A CRD declares one or more Kinds (the addressable resource
// types) whose OpenAPI schema enumerates the spec/status fields a user
// can set. The crd adapter emits one LIBRARY per API group, one TYPE
// per Kind, and one CONFIG_KNOB per schema field.
//
// Registered under ecosystem id "crd" — matches ContractElement.Ecosystem
// set by internal/adapters/crd.
//
// Tier mapping:
//   - "Kinds" (TYPE) is the container tier — one element per resource
//     Kind the CRD declares.
//   - "Fields" (CONFIG_KNOB) is the primary detail tier — one per
//     schema field. Substance grading, the worklist, and the
//     per-element listing run on this tier.
type crdView struct{}

func (crdView) ID() string { return "crd" }

func (crdView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "container", Label: "Kinds", Kinds: []string{"TYPE"}, ShowInHeader: true},
		{ID: "primary", Label: "Fields", Kinds: []string{"CONFIG_KNOB"}, ShowInHeader: true},
	}
}

func (crdView) PrimaryDetailKinds() []string { return []string{"CONFIG_KNOB"} }

// EvidenceSurfaces scopes crd reports to Contract + Docs only: there is
// no tests/examples/workflows corpus for CRDs (the inline schema
// description is the sole doc surface), so those surfaces never render.
func (crdView) EvidenceSurfaces() []string { return []string{"contract", "docs"} }

// ContainerOf — crd field IDs are "<group>/<Kind>.<dotted.path>"
// (e.g. "cert-manager.io/Certificate.spec.dnsNames"). The container of
// a field is its Kind ("cert-manager.io/Certificate"): everything up to
// the first "." that follows the "/". TYPE-kind Kind IDs have no field
// dot, so they return "" (their container is the group/LIBRARY).
func (crdView) ContainerOf(id string, _ map[string]any) string {
	slash := strings.Index(id, "/")
	if slash < 0 {
		return ""
	}
	if dot := strings.Index(id[slash:], "."); dot >= 0 {
		return id[:slash+dot]
	}
	return ""
}

func (crdView) Noun() (string, string) { return "field", "fields" }

// TotalNoun — the umbrella surface is Kinds + Fields, so the .Total
// noun is the generic "element" rather than "field" (a count like
// "5019" would otherwise read as fields when most-but-not-all are).
func (crdView) TotalNoun() (string, string) { return "element", "elements" }

// VersionScheme is empty — CRDs carry served/storage version flags but
// no @available-style per-field deprecation scheme; the crd adapter
// emits no versionConstraints.
func (crdView) VersionScheme() string { return "" }

func init() {
	RegisterEcosystem(crdView{})
}
