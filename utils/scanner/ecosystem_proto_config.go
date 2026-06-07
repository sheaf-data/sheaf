package scanner

import "strings"

// protoConfigView is the EcosystemView for proto surfaces that are
// pure data-schema — protobuf `message` declarations with no
// corresponding `service` declarations. envoy's `envoy.config.*` and
// `envoy.extensions.*` trees fit this shape: integrators write YAML
// against these messages, never call them as RPCs. Using the stock
// `proto` view on these libraries renders a "Zero Services · Zero
// Methods" masthead because PROTOCOL/METHOD are genuinely empty;
// this view promotes the TYPE tier (which holds the proto messages)
// to the primary slot so the header reads as the corpus actually is.
//
// Nomenclature: we label the primary tier "Messages" — not "Types" —
// to match both protobuf grammar (`message Foo { ... }`) and envoy's
// own Sphinx domain (`envoy_v3_api_msg_...` slug encoding). FIDL's
// ecosystem view uses "Types" because FIDL grammar declares `type`;
// each ecosystem owns its preferred surface noun via the
// EcosystemView interface (TierSpec.Label / Noun / TotalNoun).
//
// Tier mapping:
//   - "Messages" (TYPE) is the primary tier — one element per
//     top-level `message`. Shown in the header.
//   - PROTOCOL / METHOD are accepted (so a config library that
//     happens to declare a stray service still surfaces it) but
//     hidden from the header. Flip back to ecosystem `proto` for
//     service-shaped libraries.
//
// Registered under ecosystem id "proto-config". Rendered with
// `scanner --ecosystem proto-config --library ...`.
type protoConfigView struct{}

func (protoConfigView) ID() string { return "proto-config" }

func (protoConfigView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "primary", Label: "Messages", Kinds: []string{"TYPE"}, ShowInHeader: true},
		{ID: "container", Label: "Services", Kinds: []string{"PROTOCOL"}, ShowInHeader: false},
		{ID: "method", Label: "Methods", Kinds: []string{"METHOD"}, ShowInHeader: false},
	}
}

func (protoConfigView) PrimaryDetailKinds() []string { return []string{"TYPE"} }

// ContainerOf — same dotted-package convention as protoView, except
// the "container" of a TYPE is its package (the dotted-prefix up to
// the last dot). e.g. envoy.config.cluster.v3/Cluster → container
// "envoy.config.cluster.v3".
func (protoConfigView) ContainerOf(id string, _ map[string]any) string {
	// Element IDs from the proto adapter take the form
	// "<pkg>/<TypeName>" — the slash separates package from local
	// name. The container is the package.
	if slash := strings.IndexByte(id, '/'); slash > 0 {
		return id[:slash]
	}
	if dot := strings.LastIndex(id, "."); dot > 0 {
		return id[:dot]
	}
	return ""
}

func (protoConfigView) Noun() (string, string) { return "message", "messages" }

// TotalNoun — messages are the actionable surface in a config-only
// proto library; the umbrella count matches the primary-detail noun.
func (protoConfigView) TotalNoun() (string, string) { return "message", "messages" }

// VersionScheme — protobuf has no @available equivalent.
func (protoConfigView) VersionScheme() string { return "" }

func init() {
	RegisterEcosystem(protoConfigView{})
}
