package scanner

import "strings"

// openapiView is the EcosystemView for HTTP REST API surfaces
// described by an OpenAPI / Swagger spec. The motivating example is
// OpenAI's published openapi.yaml (https://github.com/openai/openai-openapi)
// — Chat Completions, Embeddings, Files, Fine-tuning, etc. — but the
// shape is general: Grafana, Stripe, GitHub's REST v3, Kubernetes'
// REST surface, and most B2B platforms publish surfaces with the same
// tag / endpoint / parameter hierarchy.
//
// The view is registered under ecosystem id "openapi" (the shape the
// report renders, not a specific provider). A scanner invocation that
// passes --ecosystem=openapi gets the labels below regardless of
// whether the input snapshot was derived from OpenAI's spec, Grafana's
// api-merged.json, or any other OpenAPI document.
//
// Tier mapping:
//   - "Resources" (PROTOCOL) is the container tier — one element per
//     OpenAPI tag ("Chat", "Dashboards", "Datasources"). Tags group
//     related endpoints and almost always have a dedicated reference
//     page in the upstream docs, so they're the natural unit for
//     coverage receipts to attach to in addition to per-endpoint
//     receipts.
//   - "Endpoints" (METHOD) is the primary detail tier — one element
//     per (path, HTTP verb) pair: "POST /v1/chat/completions" or
//     "GET /api/dashboards/uid/{uid}". Substance grading + the
//     worklist + the per-element listing all run on this tier.
//
// No parameter tier. OpenAPI request/response parameters are
// sub-elements of an endpoint; the endpoint's coverage subsumes the
// parameter's coverage in every reasonable read (upstream docs index
// at the endpoint level, not per-param). Emitting separate TYPE
// elements per parameter inflated the UNCLAIMED bucket without
// adding signal — the param elements have no plausible receipt and
// only obscure the endpoint-level picture. Adapters that genuinely
// want per-param tracking can register a sibling view.
//
// Element kinds reuse the canonical PROTOCOL / METHOD labels the
// rest of compute.go already understands. The View just relabels
// them in the header so an OpenAPI reader sees "Resources" instead of
// "Protocols" and "Endpoints" instead of "Methods" — the underlying
// substance pipeline, worklist, and bridge math are unchanged.
type openapiView struct{}

func (openapiView) ID() string { return "openapi" }

func (openapiView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "container", Label: "Resources", Kinds: []string{"PROTOCOL"}, ShowInHeader: true},
		{ID: "primary", Label: "Endpoints", Kinds: []string{"METHOD"}, ShowInHeader: true},
	}
}

func (openapiView) PrimaryDetailKinds() []string { return []string{"METHOD"} }

// EvidenceSurfaces scopes openapi reports to Contract + Docs + Tests +
// Examples — deliberately omitting "implementations". An OpenAPI spec
// describes an HTTP surface; there is no code-level implementation tree
// to attribute classes to, so the masthead's Implementations tile
// (which would otherwise render "N/A · no impl tree in scope" off the
// interface-kind heuristic) is suppressed. Tests and examples stay:
// an OpenAPI corpus routinely has endpoint tests and worked request /
// response examples, and listing "tests" here also ADMITS the tests
// surface for the masthead (see testsAdmitted in compute.go), since the
// PROTOCOL/METHOD kind heuristic alone treats this surface as a pure
// interface with no test corpus. "workflows" is omitted too: openapi
// has no separate workflow corpus, so usage == worked examples.
func (openapiView) EvidenceSurfaces() []string {
	return []string{"contract", "docs", "tests", "examples"}
}

// ContainerOf — OpenAPI element ids carry the tag as the first
// dotted segment after the library qualifier ("grafana/dashboards.GET_dashboards_uid_uid").
// The container of an endpoint is its tag ("grafana/dashboards").
// For tag elements (no dot in the local part) the container is "".
func (openapiView) ContainerOf(id string, _ map[string]any) string {
	slash := strings.Index(id, "/")
	if slash < 0 {
		if dot := strings.Index(id, "."); dot > 0 {
			return id[:dot]
		}
		return ""
	}
	rest := id[slash+1:]
	dot := strings.Index(rest, ".")
	if dot < 0 {
		return ""
	}
	return id[:slash+1+dot]
}

func (openapiView) Noun() (string, string) { return "endpoint", "endpoints" }

// TotalNoun — the umbrella count rolls together resources and
// endpoints, so naming it "endpoints" would overstate the endpoint
// count to a reader who knows the upstream figure (Grafana publishes
// "~320 endpoints"). Following cobraView's "commands & flags"
// precedent, we name the union explicitly so the masthead total is
// unambiguous and the per-tier labels in the header carry the
// breakdown.
func (openapiView) TotalNoun() (string, string) {
	return "API element", "resources & endpoints"
}

// VersionScheme is empty — OpenAPI's deprecation signal is the
// `deprecated: true` flag, not a numeric API level. The per-element
// deprecation parser (which expects FIDL-style @available constraints)
// stays quiet; if a future adapter wants to surface OpenAPI's
// deprecated flag, that's a separate plumbing job.
func (openapiView) VersionScheme() string { return "" }

func init() {
	RegisterEcosystem(openapiView{})
}
