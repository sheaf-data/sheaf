// Package grounding implements the Grounding lens: a mechanical,
// deterministic pass over a library's concept docs that answers, per
// reference, whether a coding agent can tell which contract element the
// prose is talking about — or whether it has to guess.
//
// It is a sibling of Lag (Lag asks "is the doc current?"; Grounding asks
// "is the doc's reference resolvable?"). It is NOT a coverage presence-bit
// and NOT AI-judged. The detection algorithm lives in this package
// (see detect.go).
//
// The output type Report marshals to EXACTLY the shape in
// docs/grounding/grounding.fixture.json (snake_case JSON keys), validated
// by docs/grounding/grounding.schema.json. The UI renders against that
// fixture; this detector emits the real data in the same shape so the two
// sides never drift.
package grounding

// SchemaVersion is the frozen contract version. Bump only with a
// deliberate schema_version change discussed with the UI side.
const SchemaVersion = 1

// AdapterName / AdapterVersion stamp every finding's provenance. The
// fixture pins adapter "grounding" at version "0.1.0".
const (
	AdapterName    = "grounding"
	AdapterVersion = "0.1.0"
)

// State is the three-state grounding ladder plus the element-rollup-only
// "not_mentioned" coverage state. Finding-level states never use
// not_mentioned (a finding is, by definition, a detected reference).
type State string

const (
	StateGrounded     State = "grounded"
	StateGuessing     State = "guessing"
	StateUngrounded   State = "ungrounded"
	StateNotMentioned State = "not_mentioned"
)

// AnchorKind enumerates the grounding anchors tested per reference. The
// presence of a sufficient anchor is what moves a reference to Grounded;
// the full set is recorded on every finding as the "Sheaf checked …"
// audit trail (decision 5: every red shows its work).
type AnchorKind string

const (
	AnchorPageTitle        AnchorKind = "page_title"
	AnchorHeading          AnchorKind = "heading"
	AnchorFirstUse         AnchorKind = "first_use"
	AnchorDefinedTerm      AnchorKind = "defined_term"
	AnchorQualifiedMention AnchorKind = "qualified_mention"
	AnchorLink             AnchorKind = "link"
)

// FixKind enumerates the suggested-edit kinds rendered as a fix chip.
type FixKind string

const (
	FixLinkFirstUse FixKind = "link_first_use"
	FixDefineTerm   FixKind = "define_term"
	FixQualify      FixKind = "qualify"
)

// Report is the top-level Grounding payload — one per library. JSON tags
// reproduce grounding.fixture.json exactly. Fields ordered to match the
// fixture for readable diffs; encoding/json honors the tags regardless.
type Report struct {
	SchemaVersion  int         `json:"schema_version"`
	Surface        string      `json:"surface"`
	GeneratedAt    string      `json:"generated_at"`
	ModelStamp     *ModelStamp `json:"model_stamp"`
	Repo           string      `json:"repo"`
	Commit         string      `json:"commit"`
	Library        string      `json:"library"`
	LibraryDisplay string      `json:"library_display"`

	Summary    Summary               `json:"summary"`
	DisplayMap map[string]StateStyle `json:"display_map"`
	Elements   []Element             `json:"elements"`
	Findings   []Finding             `json:"findings"`
}

// ModelStamp is null for a purely mechanical run (reproducible). The
// detector always sets it nil; it exists only so the shape round-trips
// when an AI tier later contributes.
type ModelStamp struct {
	Model   string `json:"model"`
	Version string `json:"version"`
	RunAt   string `json:"run_at"`
}

// Summary is the per-library rollup + the forces_a_guess headline number.
type Summary struct {
	ElementsTotal        int `json:"elements_total"`
	ElementsMentioned    int `json:"elements_mentioned"`
	ElementsNotMentioned int `json:"elements_not_mentioned"`
	ElementsGrounded     int `json:"elements_grounded"`
	ElementsGuessing     int `json:"elements_guessing"`
	ElementsUngrounded   int `json:"elements_ungrounded"`
	ReferencesTotal      int `json:"references_total"`
	ReferencesGrounded   int `json:"references_grounded"`
	ReferencesGuessing   int `json:"references_guessing"`
	ReferencesUngrounded int `json:"references_ungrounded"`
	// ForcesAGuess = guessing + ungrounded references — the cold-stranger
	// headline number.
	ForcesAGuess int    `json:"forces_a_guess"`
	Headline     string `json:"headline"`
}

// StateStyle is one entry of the display_map (state -> presentation).
type StateStyle struct {
	Label        string `json:"label"`
	WorkingLabel string `json:"working_label"`
	Lighthouse   string `json:"lighthouse"`
	Glyph        string `json:"glyph"`
	Tone         string `json:"tone"`
	Blurb        string `json:"blurb"`
}

// Element is the per-element rollup: best state across its refs (or
// not_mentioned), plus the per-state reference counts.
type Element struct {
	ElementID  string    `json:"element_id"`
	Display    string    `json:"display"`
	Kind       string    `json:"kind"`
	State      State     `json:"state"`
	Mentioned  bool      `json:"mentioned"`
	RefCounts  RefCounts `json:"ref_counts"`
	FindingIDs []string  `json:"finding_ids"`
}

// RefCounts holds the per-state reference tally for one element.
type RefCounts struct {
	Grounded   int `json:"grounded"`
	Guessing   int `json:"guessing"`
	Ungrounded int `json:"ungrounded"`
}

// Finding is the atom of the UI — one per detected reference (a mention
// of a word colliding with a contract element).
type Finding struct {
	ID                    string          `json:"id"`
	ElementID             string          `json:"element_id"`
	ElementDisplay        string          `json:"element_display"`
	State                 State           `json:"state"`
	SourcePath            string          `json:"source_path"`
	Line                  int             `json:"line"`
	SectionPath           []string        `json:"section_path"`
	Excerpt               string          `json:"excerpt"`
	Token                 string          `json:"token"`
	TokenSpan             Span            `json:"token_span"`
	Candidates            []Candidate     `json:"candidates"`
	CompetingContractRefs int             `json:"competing_contract_refs"`
	Checked               []CheckedAnchor `json:"checked"`
	Fix                   *Fix            `json:"fix"`
	Severity              float64         `json:"severity"`
	Provenance            Provenance      `json:"provenance"`
}

// Span is the offset+len of the colliding token within the excerpt, for
// highlight rendering.
type Span struct {
	Start int `json:"start"`
	Len   int `json:"len"`
}

// Candidate is one entry of the "Could mean ->" set. The contract
// candidate(s) carry a non-empty ElementID; the english gloss carries
// null (omitted-as-null) ElementID.
type Candidate struct {
	ElementID  *string `json:"element_id"`
	Label      string  `json:"label"`
	Kind       string  `json:"kind"` // "contract" | "english" | "other"
	IsContract bool    `json:"is_contract"`
}

// CheckedAnchor is one row of the per-reference anchor audit trail.
type CheckedAnchor struct {
	Anchor AnchorKind `json:"anchor"`
	Found  bool       `json:"found"`
	Detail *string    `json:"detail"`
}

// Fix is the suggested-edit chip. Nil for grounded findings.
type Fix struct {
	Kind       FixKind `json:"kind"`
	Suggestion string  `json:"suggestion"`
	Markdown   string  `json:"markdown"`
}

// Provenance stamps the mechanical tier on every finding. tier is always
// "mechanical" for this detector -> the solid ● badge in the UI.
type Provenance struct {
	Tier           string `json:"tier"`
	Adapter        string `json:"adapter"`
	AdapterVersion string `json:"adapter_version"`
}

// defaultDisplayMap returns the frozen state->presentation map exactly as
// the fixture pins it. The voice-split (label vs working_label) and the
// non-color glyph signal are contract, not styling choices the detector
// gets to make — they ship byte-for-byte with the fixture.
func defaultDisplayMap() map[string]StateStyle {
	return map[string]StateStyle{
		"grounded": {
			Label: "Grounded", WorkingLabel: "Grounded", Lighthouse: "pass",
			Glyph: "●", Tone: "green",
			Blurb: "The page binds this reference to the API. The agent knows what you mean.",
		},
		"guessing": {
			Label: "Guessing", WorkingLabel: "Inferred", Lighthouse: "needs-work",
			Glyph: "◐", Tone: "amber",
			Blurb: "Context suggests a referent but nothing confirms it. The agent has to infer.",
		},
		"ungrounded": {
			Label: "Ungrounded", WorkingLabel: "Ungrounded", Lighthouse: "fail",
			Glyph: "○", Tone: "red",
			Blurb: "Nothing on the page ties this to the API. The agent is blind — and will hallucinate.",
		},
	}
}

func strptr(s string) *string { return &s }
