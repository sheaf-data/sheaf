package conceptdoc

import (
	"encoding/json"

	"google.golang.org/protobuf/encoding/protojson"

	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

// This file wires the docs.concepts surface INTO the live scan snapshot.
//
// The data model is additive (REQUIREMENTS-concept-ingest.md decision 3): the
// CoverageProfile proto has no `concepts` field — the existing singular
// `concept` field stays the `///`-fed reference surface and is never touched.
// So instead of mutating the proto/corpus, the in-process scan path runs the
// anchored-mention engine over the config's declared narrative docs and grafts
// the resulting claims onto the generic-map snapshot profiles under
// docs.concepts. utils/scanner's countConceptDoc reads exactly that bucket; the
// legacy countConcept reads docs.concept / docs.reference and is unaffected.
//
// Detection here is ANCHORED-ONLY — conceptdoc.Detect → grounding.Anchored-
// Mentions — never the loose markdown adapter's reference matching. A bare
// prose collision does not attribute.

// ClaimsByElement groups a Result's flat anchored-claim list by the element it
// attributes to (its sole contract_ref). Order within each element is the
// engine's scan order (document-then-offset), already deterministic. Elements
// with no anchored claim are absent from the map (the not-covered signal).
func ClaimsByElement(res *Result) map[string][]*docclaimpb.DocClaim {
	out := map[string][]*docclaimpb.DocClaim{}
	if res == nil {
		return out
	}
	for _, c := range res.Claims {
		refs := c.GetContractRefs()
		if len(refs) == 0 {
			continue
		}
		// A docs.concepts claim carries exactly one contract_ref (the
		// attributed element); fold defensively in case that ever changes.
		for _, id := range refs {
			if id == "" {
				continue
			}
			out[id] = append(out[id], c)
		}
	}
	return out
}

// InjectIntoProfiles grafts the docs.concepts claims onto the generic-map
// snapshot profiles, keyed by element id. For each profile whose elementId has
// anchored claims, it sets profile["docs"]["concepts"] to the protojson-
// rendered claim list (a []any of map[string]any, matching how the rest of the
// snapshot's coverage buckets are shaped). Profiles with no claims are left
// untouched — countConceptDoc returns 0 for an absent bucket, the correct
// not-covered reading.
//
// The injection is idempotent per element and overwrites any pre-existing
// concepts bucket (there is none in a normal scan — the proto cannot produce
// one). It marshals each DocClaim through protojson with UseProtoNames so the
// keys match the snapshot's snake-cased wire shape; countConceptDoc keys on the
// single-word lowercased "docs"/"concepts" so casing is moot, but we stay
// consistent with PBToMap regardless.
//
// Returns the number of profiles that received a concepts bucket. Marshal
// errors on an individual claim skip that claim rather than abort the scan —
// a single malformed claim must not blank a whole report's narrative surface.
func InjectIntoProfiles(profiles []map[string]any, claimsByElem map[string][]*docclaimpb.DocClaim) int {
	if len(profiles) == 0 || len(claimsByElem) == 0 {
		return 0
	}
	mo := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}
	injected := 0
	for _, prof := range profiles {
		if prof == nil {
			continue
		}
		id, _ := prof["elementId"].(string)
		if id == "" {
			continue
		}
		claims := claimsByElem[id]
		if len(claims) == 0 {
			continue
		}
		bucket := make([]any, 0, len(claims))
		for _, c := range claims {
			m, err := claimToMap(mo, c)
			if err != nil || m == nil {
				continue
			}
			bucket = append(bucket, m)
		}
		if len(bucket) == 0 {
			continue
		}
		docs, _ := prof["docs"].(map[string]any)
		if docs == nil {
			docs = map[string]any{}
			prof["docs"] = docs
		}
		docs["concepts"] = bucket
		injected++
	}
	return injected
}

// VerdictByElement maps each referenced element id to its clear/ambiguous
// verdict (silent elements — no anchored mention — are absent from the map,
// exactly like ClaimsByElement). This is the per-element half of the partition
// the snapshot must carry so compute.go can roll up clear/ambiguous/silent
// without re-deriving site fan-out (the injected DocClaim does not carry the
// token/anchor needed to recompute degree).
func VerdictByElement(res *Result) map[string]Verdict {
	out := map[string]Verdict{}
	if res == nil {
		return out
	}
	for _, e := range res.Elements {
		if e.Verdict == VerdictClear || e.Verdict == VerdictAmbiguous {
			out[e.ElementID] = e.Verdict
		}
	}
	return out
}

// InjectResultIntoProfiles grafts BOTH the docs.concepts claim buckets AND the
// per-element clear/ambiguous verdict onto the snapshot profiles in one pass.
// It is the wired entry point (utils/scanner.BuildSnapshot calls it); the
// split ClaimsByElement + InjectIntoProfiles pair is retained for callers that
// only need the claim buckets.
//
// The verdict is stamped as a plain string under docs["conceptsVerdict"]
// ("clear" | "ambiguous"); a silent element gets neither a concepts bucket nor
// a verdict, so compute.go reads it as silent by absence. The verdict is set
// only on profiles that ALSO received a concepts bucket (a referenced element
// always has >=1 claim), keeping the two signals consistent.
func InjectResultIntoProfiles(profiles []map[string]any, res *Result) int {
	if len(profiles) == 0 || res == nil {
		return 0
	}
	claimsByElem := ClaimsByElement(res)
	injected := InjectIntoProfiles(profiles, claimsByElem)
	verdicts := VerdictByElement(res)
	if len(verdicts) == 0 {
		return injected
	}
	for _, prof := range profiles {
		if prof == nil {
			continue
		}
		id, _ := prof["elementId"].(string)
		if id == "" {
			continue
		}
		v, ok := verdicts[id]
		if !ok {
			continue
		}
		docs, _ := prof["docs"].(map[string]any)
		if docs == nil {
			docs = map[string]any{}
			prof["docs"] = docs
		}
		docs["conceptsVerdict"] = string(v)
	}
	return injected
}

// claimToMap renders one DocClaim into a generic map via protojson, mirroring
// librarysnapshot.PBToMap so the injected bucket is byte-shaped exactly like
// every other coverage bucket the snapshot carries.
func claimToMap(mo protojson.MarshalOptions, c *docclaimpb.DocClaim) (map[string]any, error) {
	b, err := mo.Marshal(c)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
