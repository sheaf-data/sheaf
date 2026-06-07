package scanner

import "strings"

// manifestView is the EcosystemView for plain / rendered Kubernetes
// manifests (e.g. `helm template` output). Each manifest declares
// resources of a given Kind whose fields are the keys actually present
// in the YAML. The k8smanifest adapter emits one LIBRARY per apiVersion
// group, one TYPE per resource Kind, and one CONFIG_KNOB per field.
//
// Registered under ecosystem id "manifest" — matches
// ContractElement.Ecosystem set by internal/adapters/k8smanifest.
//
// Tier mapping:
//   - "Resources" (TYPE) is the container tier — one element per
//     resource Kind present in the manifests.
//   - "Fields" (CONFIG_KNOB) is the primary detail tier — one per
//     field. Substance grading, the worklist, and the per-element
//     listing run on this tier.
type manifestView struct{}

func (manifestView) ID() string { return "manifest" }

func (manifestView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "container", Label: "Resources", Kinds: []string{"TYPE"}, ShowInHeader: true},
		{ID: "primary", Label: "Fields", Kinds: []string{"CONFIG_KNOB"}, ShowInHeader: true},
	}
}

func (manifestView) PrimaryDetailKinds() []string { return []string{"CONFIG_KNOB"} }

// EvidenceSurfaces scopes manifest reports to Contract + Docs only:
// rendered manifests carry no tests/examples/workflows corpus, so those
// surfaces never render.
func (manifestView) EvidenceSurfaces() []string { return []string{"contract", "docs"} }

// ContainerOf — manifest field IDs are "<group>/<Kind>.<dotted.path>"
// (same scheme as crd). The container of a field is its Kind:
// everything up to the first "." that follows the "/". Kind TYPE IDs
// have no field dot, so they return "".
func (manifestView) ContainerOf(id string, _ map[string]any) string {
	slash := strings.Index(id, "/")
	if slash < 0 {
		return ""
	}
	if dot := strings.Index(id[slash:], "."); dot >= 0 {
		return id[:slash+dot]
	}
	return ""
}

func (manifestView) Noun() (string, string) { return "field", "fields" }

// TotalNoun — umbrella surface is Resources + Fields, so .Total uses
// the generic "element".
func (manifestView) TotalNoun() (string, string) { return "element", "elements" }

// VersionScheme is empty — rendered manifests carry no per-field
// deprecation scheme.
func (manifestView) VersionScheme() string { return "" }

func init() {
	RegisterEcosystem(manifestView{})
}
