package scanner

import "strings"

// fidlView is the EcosystemView for FIDL libraries — the original
// shape the scanner was built for. PROTOCOL is the container tier
// (named services / interfaces), METHOD is the primary detail (the
// RPCs inside those protocols), TYPE rolls into the total but
// doesn't get its own header tile.
type fidlView struct{}

func (fidlView) ID() string { return "fidl" }

func (fidlView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "container", Label: "Protocols", Kinds: []string{"PROTOCOL"}, ShowInHeader: true},
		{ID: "primary", Label: "Methods", Kinds: []string{"METHOD"}, ShowInHeader: true},
		{ID: "type", Label: "Types", Kinds: []string{"TYPE"}, ShowInHeader: false},
	}
}

func (fidlView) PrimaryDetailKinds() []string { return []string{"METHOD"} }

// ContainerOf strips the last "." segment of the id's local part.
// FIDL ids look like "library.name/Protocol.Method"; the container
// for a method is "library.name/Protocol". Standalone elements
// (LIBRARY-kind, top-level TYPE) return "".
func (fidlView) ContainerOf(id string, _ map[string]any) string {
	slash := strings.Index(id, "/")
	if slash < 0 {
		// No library separator — the id is a bare local name. Strip
		// after the last "." if present.
		if dot := strings.LastIndex(id, "."); dot > 0 {
			return id[:dot]
		}
		return ""
	}
	rest := id[slash+1:]
	dot := strings.LastIndex(rest, ".")
	if dot < 0 {
		return ""
	}
	return id[:slash+1+dot]
}

func (fidlView) Noun() (string, string) { return "method", "methods" }

// TotalNoun — FIDL's primary detail is METHOD; types and protocols
// roll into the same total but the actionable count is methods, so
// the umbrella stays "methods" (same as Noun). If a future variant
// wants "protocols & methods" as the umbrella, override here.
func (fidlView) TotalNoun() (string, string) { return "method", "methods" }

func (fidlView) VersionScheme() string { return "fidl" }

func init() {
	RegisterEcosystem(fidlView{})
}
