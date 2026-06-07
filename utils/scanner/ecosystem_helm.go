package scanner

import "strings"

// helmView is the EcosystemView for Helm chart configuration surfaces.
// A chart exposes a set of values (from values.schema.json when present,
// otherwise values.yaml defaults + helm-docs `# --` comments). The
// helmvalues adapter emits one LIBRARY per chart and one CONFIG_KNOB per
// value key.
//
// Registered under ecosystem id "helm" — matches
// ContractElement.Ecosystem set by internal/adapters/helmvalues.
//
// Tier mapping:
//   - "Values" (CONFIG_KNOB) is the primary detail tier — one element
//     per value key. There is no separate container tier; the chart
//     itself is the LIBRARY umbrella.
type helmView struct{}

func (helmView) ID() string { return "helm" }

func (helmView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "primary", Label: "Values", Kinds: []string{"CONFIG_KNOB"}, ShowInHeader: true},
	}
}

func (helmView) PrimaryDetailKinds() []string { return []string{"CONFIG_KNOB"} }

// EvidenceSurfaces scopes helm reports to Contract + Docs only: chart
// values have no tests/examples/workflows corpus (the inline value
// description is the sole doc surface), so those surfaces never render.
func (helmView) EvidenceSurfaces() []string { return []string{"contract", "docs"} }

// ContainerOf — helm value IDs are dotted key paths
// ("controller.service.type"); the container is the parent path
// ("controller.service"). Top-level keys have no dot and return "".
func (helmView) ContainerOf(id string, _ map[string]any) string {
	if dot := strings.LastIndex(id, "."); dot >= 0 {
		return id[:dot]
	}
	return ""
}

func (helmView) Noun() (string, string) { return "value", "values" }

// TotalNoun — values are the whole surface here, so the umbrella noun
// matches the primary-detail noun.
func (helmView) TotalNoun() (string, string) { return "value", "values" }

// VersionScheme is empty — chart values carry no @available-style
// per-key deprecation scheme.
func (helmView) VersionScheme() string { return "" }

func init() {
	RegisterEcosystem(helmView{})
}
