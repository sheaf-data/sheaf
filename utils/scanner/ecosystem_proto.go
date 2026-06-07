package scanner

import "strings"

// protoView is the EcosystemView for protobuf-shaped surfaces — gRPC
// services, Envoy xDS, Kubernetes API protos, any package/service/RPC
// hierarchy expressed in .proto files. Mirrors fidlView's tier
// structure because the contract surface shape is the same (services
// own methods; types are addressable but not their own header tile);
// only the labels change ("Services" vs "Protocols", "Methods" the
// same, "Messages" vs "Types").
//
// The view is registered under ecosystem id "proto" — the shape the
// report renders, not the specific protocol or framework. gRPC,
// Envoy xDS, and any kubernetes proto surface all share this same
// view; if a specific framework's report ever needs different tier
// labels (e.g. Envoy's listener filters), that's a separate
// registration, not an alias.
//
// Tier mapping:
//   - "Services" (PROTOCOL) is the container tier — one element per
//     `service` declaration in the .proto.
//   - "Methods" (METHOD) is the primary detail tier — one per `rpc`.
//     Substance grading + the worklist + the per-element listing all
//     run on this tier.
//   - "Messages" (TYPE) is the modifier tier — addressable so
//     references resolve to elements, but hidden from the header row
//     (matches fidlView's Types).
type protoView struct{}

func (protoView) ID() string { return "proto" }

func (protoView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "container", Label: "Services", Kinds: []string{"PROTOCOL"}, ShowInHeader: true},
		{ID: "primary", Label: "Methods", Kinds: []string{"METHOD"}, ShowInHeader: true},
		{ID: "type", Label: "Messages", Kinds: []string{"TYPE"}, ShowInHeader: false},
	}
}

func (protoView) PrimaryDetailKinds() []string { return []string{"METHOD"} }

// ContainerOf — proto element IDs are dotted-package qualified
// ("pkg.subpkg.Service.Method"); the container of a method is its
// service ("pkg.subpkg.Service"). Standalone messages (TYPE-kind)
// whose IDs end with the type name return the package as their
// container; messages without a dot return "".
func (protoView) ContainerOf(id string, _ map[string]any) string {
	if dot := strings.LastIndex(id, "."); dot > 0 {
		return id[:dot]
	}
	return ""
}

func (protoView) Noun() (string, string) { return "method", "methods" }

// TotalNoun — methods are the primary actionable surface; services
// and messages live in supporting tiers. Same shape as fidlView: the
// umbrella matches the primary-detail noun.
func (protoView) TotalNoun() (string, string) { return "method", "methods" }

// VersionScheme is empty — protobuf has no @available equivalent at
// the schema level. The proto adapter doesn't emit
// versionConstraints, so the per-element deprecation parser stays
// quiet either way.
func (protoView) VersionScheme() string { return "" }

func init() {
	RegisterEcosystem(protoView{})
}
