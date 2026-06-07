package scanner

// EcosystemView captures the per-ecosystem shape of a coverage report.
// One view per ecosystem (fidl, cobra, …) supplies the labels and
// kind-to-tier mapping the otherwise-generic compute.go +
// report.html.tmpl need to render correctly.
//
// See docs/scanner/multi-ecosystem-report-architecture.md for the
// design rationale. Today: FIDL, cobra (with docker / kubectl / gh
// aliases), and the default fallback are all registered views; the
// template ranges over .HeaderTiers directly. The legacy nounsFor()
// noun table is retained as the noun fallback for ecosystems that
// don't yet have a registered View (grpc / cargo / envoy at time of
// writing).
type EcosystemView interface {
	// ID — what --ecosystem matches against and what
	// ContractElement.Ecosystem typically carries. Lowercase.
	ID() string

	// Tiers — the polymorphic header. Ordered for header rendering.
	// Each tier carries its label and the ContractElement kinds whose
	// elements roll up into its Count. The template ranges over
	// .HeaderTiers (the visible-in-header subset) directly.
	Tiers() []TierSpec

	// PrimaryDetailKinds — the kinds counted in the substance synopsis +
	// bar, the worklist, and the per-element listing. A subset of the
	// kinds across all Tiers. For FIDL this is just METHOD; for cobra
	// it's SUBCOMMAND; for the parked COMPONENT view it'll be the
	// union of METHOD and CONFIG_KNOB.
	PrimaryDetailKinds() []string

	// ContainerOf — given an element's id (and the raw element map),
	// returns the id of its container in the surface hierarchy, or ""
	// for ecosystems without a container tier. Cobra:
	// "docker container run" → "docker container". FIDL:
	// "lib/Protocol.Method" → "lib/Protocol". CML: knobs have no
	// container, so returns "".
	//
	// Currently unused at render time (no container tree section yet);
	// the method is on the interface so a future container-tree view
	// can light up without re-shaping the interface.
	ContainerOf(id string, elem map[string]any) string

	// Noun — singular and plural for the primary-detail kind. Used in
	// stock sentences ("80 of 109 commands have docs that explain
	// behavior"). For unregistered ecosystems the caller falls back
	// to nounsFor() in compute.go.
	Noun() (singular, plural string)

	// TotalNoun — singular and plural for the *umbrella* element-count,
	// used wherever the report cites .Total (sticky strip, hero strip,
	// "Show all N", footer, deprecation banner). For ecosystems with a
	// single-tier surface (FIDL: methods), this matches Noun. For
	// multi-tier surfaces (cobra: commands + flags) it names the union
	// so a number like "909" doesn't read as "909 commands" when 765
	// of them are flags. Defaults to ("element", "elements") in the
	// fallback view.
	TotalNoun() (singular, plural string)

	// VersionScheme — the internal/versionscheme lookup key. Empty
	// falls back to the FIDL scheme.
	VersionScheme() string
}

// TierSpec defines one header tier — a group of element kinds that
// roll up into a single header tile (Protocols, Methods, Subcommands,
// Configuration settings, …).
type TierSpec struct {
	// ID — stable identifier ("container" | "primary" | "modifier").
	// Used in CSS class names + template keys; not displayed.
	ID string

	// Label — display label ("Protocols", "Subcommands",
	// "Configuration settings"). Rendered as the header tile text.
	Label string

	// Kinds — element kinds whose count rolls into this tier.
	Kinds []string

	// ShowInHeader — false suppresses the header tile but keeps the
	// kind countable in the total. TYPE is the canonical example:
	// addressable and bridgeable, but not its own header tile.
	ShowInHeader bool
}

// Tier is the computed counterpart of TierSpec, with the Count
// populated for the current report. ReportData.Tiers carries one of
// these per spec; the template ranges over them for the header
// (starting in Phase 2).
type Tier struct {
	ID           string
	Label        string
	Kinds        []string
	Count        int
	ShowInHeader bool
}

// evidenceSurfaceScoper is an optional EcosystemView capability:
// a view that implements it restricts which evidence surfaces render
// (masthead tiles + per-element evidence panel) to the returned set.
// Views that don't implement it render all admitted surfaces.
type evidenceSurfaceScoper interface{ EvidenceSurfaces() []string }

// viewAllowsSurface reports whether the view permits surface `key`
// (one of "contract","docs","tests","examples","workflows","implementations").
// True when the view declares no restriction (doesn't implement the
// optional interface); otherwise true iff key is in the declared set.
func viewAllowsSurface(view EcosystemView, key string) bool {
	s, ok := view.(evidenceSurfaceScoper)
	if !ok {
		return true
	}
	for _, k := range s.EvidenceSurfaces() {
		if k == key {
			return true
		}
	}
	return false
}

// viewDeclaresSurface reports whether the view *explicitly* declares
// surface `key` in its EvidenceSurfaces() set. Unlike viewAllowsSurface
// (which is permissive — true for views that declare no restriction),
// this is strict: a view that doesn't implement evidenceSurfaceScoper
// returns false. It is used by surface *admission* in compute.go, where
// a view's declared surface set can admit a surface that the element
// kind heuristic alone would miss (openapi: PROTOCOL/METHOD kinds read
// as a pure interface, so the heuristic never admits Tests even though
// an OpenAPI corpus has endpoint tests). Because non-scoping views
// (fidl, cobra, proto, cpp, proto-config) return false here, and the
// scoping views that exist (manifest, crd, helm) declare only
// {contract, docs}, OR-ing this into an admission predicate cannot
// change their behavior — only a view that newly declares the surface
// is affected.
func viewDeclaresSurface(view EcosystemView, key string) bool {
	s, ok := view.(evidenceSurfaceScoper)
	if !ok {
		return false
	}
	for _, k := range s.EvidenceSurfaces() {
		if k == key {
			return true
		}
	}
	return false
}

// ecosystemRegistry holds the registered views. Populated via
// RegisterEcosystem from each ecosystem_*.go init().
var ecosystemRegistry = map[string]EcosystemView{}

// RegisterEcosystem records a view under its ID. Called from each
// ecosystem_*.go init(). Re-registering an existing ID overwrites,
// which is intentional — lets tests substitute a stub view if needed.
func RegisterEcosystem(v EcosystemView) {
	if v == nil {
		return
	}
	ecosystemRegistry[v.ID()] = v
}

// EcosystemFor returns the registered view for id, falling back to
// defaultEcosystemView when id is unknown so callers always get a
// non-nil view they can call methods on. The default's rendering
// (no tiers, "element / elements" noun) is the honest answer for an
// ecosystem sheaf doesn't have a tailored shape for — better than
// silently rendering everything as if it were FIDL.
func EcosystemFor(id string) EcosystemView {
	if v, ok := ecosystemRegistry[id]; ok {
		return v
	}
	return defaultEcosystemView{}
}

// defaultEcosystemView is the absolute fallback used only when no
// view is registered at all (shouldn't happen in production builds;
// exists so unit tests that exercise compute.go without importing
// the view files don't panic).
type defaultEcosystemView struct{}

func (defaultEcosystemView) ID() string                                { return "default" }
func (defaultEcosystemView) Tiers() []TierSpec                         { return nil }
func (defaultEcosystemView) PrimaryDetailKinds() []string              { return nil }
func (defaultEcosystemView) ContainerOf(string, map[string]any) string { return "" }
func (defaultEcosystemView) Noun() (string, string)                    { return "method", "methods" }
func (defaultEcosystemView) TotalNoun() (string, string)               { return "element", "elements" }
func (defaultEcosystemView) VersionScheme() string                     { return "fidl" }

// kindIn — true if kind appears anywhere in kinds. Tiny utility used
// by the per-tier increment loop in compute.go and by ad-hoc tests.
func kindIn(kind string, kinds []string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// isPrimaryDetail — true if kind is in the view's primary-detail set.
// Replaces the hardcoded `kind == "METHOD"` check that compute.go used
// before this refactor.
func isPrimaryDetail(view EcosystemView, kind string) bool {
	if view == nil {
		return false
	}
	for _, k := range view.PrimaryDetailKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// isModifier — true if kind belongs to a header tier that isn't the
// view's primary-detail set. For cobra: FLAG / SWITCH / POSITIONAL.
// For FIDL: types and fields (when those tiers are configured).
// Used by the masthead's 2×3 grid to roll up the modifier-tier
// coverage separately from the command-tier coverage so the pooled
// percentage doesn't hide a wildly imbalanced split.
func isModifier(view EcosystemView, kind string) bool {
	if view == nil || isPrimaryDetail(view, kind) {
		return false
	}
	for _, t := range view.Tiers() {
		if !t.ShowInHeader {
			continue
		}
		if kindIn(kind, t.Kinds) {
			// Skip the primary-detail tier; everything else
			// shown in the header is a modifier.
			isPrimary := false
			for _, k := range t.Kinds {
				if isPrimaryDetail(view, k) {
					isPrimary = true
					break
				}
			}
			if !isPrimary {
				return true
			}
		}
	}
	return false
}

// isContainer — true if kind belongs to the view's "container" tier
// (the parent surface above the primary detail tier). For FIDL:
// PROTOCOL elements. For ecosystems without a container tier (cobra,
// where SUBCOMMAND is itself the primary tier), returns false.
//
// Used by the masthead to render a separate tile-section ABOVE the
// primary section so parents (Protocols) display above children
// (Methods). Without this, FIDL's PROTOCOL rows tally into the
// counters in the strip but get no visual section of their own —
// readers see the methods grid first with no signal that protocols
// even exist as a distinct surface tier.
func isContainer(view EcosystemView, kind string) bool {
	if view == nil {
		return false
	}
	for _, t := range view.Tiers() {
		if t.ID != "container" {
			continue
		}
		if kindIn(kind, t.Kinds) {
			return true
		}
	}
	return false
}

// containerTierSpec returns the container tier's spec or nil if the
// view declares none. Used to derive the container section's display
// label ("Protocols") without re-walking Tiers() at every render
// callsite.
func containerTierSpec(view EcosystemView) *TierSpec {
	if view == nil {
		return nil
	}
	for _, t := range view.Tiers() {
		if t.ID == "container" {
			cp := t
			return &cp
		}
	}
	return nil
}
