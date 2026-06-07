package scanner

import "strings"

// cppView is the EcosystemView for C++ public-header surfaces emitted
// by the cppheader contract anchor — Pigweed modules, or any C++
// library whose contract is its public classes, free functions, and
// macros. The contract surface shape differs from FIDL/proto in that
// the "callable" tier is a mix of class methods and free functions,
// and macros are a first-class part of the API (Pigweed's PW_LOG_*,
// PW_ASSERT, …) rather than incidental.
//
// Registered under ecosystem id "cpp".
//
// Tier mapping (kinds emitted by internal/adapters/cppheader):
//   - "Classes" (CPP_CLASS) is the container tier — one element per
//     public class / struct.
//   - "Functions" (METHOD + CPP_FREE_FUNCTION) is the primary detail
//     tier — public member functions and free functions. Substance
//     grading + worklist + per-element listing run on this tier.
//   - "Macros" (CPP_MACRO) is a modifier tier, shown in the header
//     because for macro-heavy embedded APIs it's a large slice of the
//     real surface.
//   - "Enums" (TYPE) is addressable but hidden from the header row
//     (matches proto's Messages / FIDL's Types).
type cppView struct{}

func (cppView) ID() string { return "cpp" }

func (cppView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "container", Label: "Classes", Kinds: []string{"CPP_CLASS"}, ShowInHeader: true},
		{ID: "primary", Label: "Functions", Kinds: []string{"METHOD", "CPP_FREE_FUNCTION"}, ShowInHeader: true},
		{ID: "macro", Label: "Macros", Kinds: []string{"CPP_MACRO"}, ShowInHeader: true},
		{ID: "type", Label: "Enums", Kinds: []string{"TYPE"}, ShowInHeader: false},
	}
}

func (cppView) PrimaryDetailKinds() []string { return []string{"METHOD", "CPP_FREE_FUNCTION"} }

// ContainerOf — cppheader element IDs are "<library>/<qualified::name>"
// (e.g. "pw_rpc/pw::rpc::Server::ProcessPacket"). The container of a
// method is its class ("pw_rpc/pw::rpc::Server"); strip the trailing
// "::member". Free functions and classes have no "::"-separated
// member tail under their library prefix, so they return "".
func (cppView) ContainerOf(id string, _ map[string]any) string {
	if idx := strings.LastIndex(id, "::"); idx > 0 {
		return id[:idx]
	}
	return ""
}

func (cppView) Noun() (string, string) { return "function", "functions" }

// TotalNoun — the umbrella spans classes, functions, macros, and
// enums, so the .Total figure shouldn't read as "N functions". Use a
// neutral "symbol".
func (cppView) TotalNoun() (string, string) { return "symbol", "symbols" }

// VersionScheme is empty — C++ headers carry no schema-level
// @available equivalent; the cppheader adapter emits no
// versionConstraints.
func (cppView) VersionScheme() string { return "" }

func init() {
	RegisterEcosystem(cppView{})
}
