// delta.json — structured artifact written alongside the markdown
// comment, capturing the inputs and the rendered delta so the HTML
// render can be regenerated without re-running the scan.

package prbot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/sheaf-data/sheaf/internal/analyze"
	"github.com/sheaf-data/sheaf/internal/corpus"
	"github.com/sheaf-data/sheaf/internal/coverage/refs"
	coveragepb "github.com/sheaf-data/sheaf/proto/coverage"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// DeltaArtifact is the in-memory shape of delta.json. Pretty-printed
// with sorted keys and 2-space indent for deterministic diffs.
type DeltaArtifact struct {
	SchemaVersion      string               `json:"schema_version"`
	System             string               `json:"system"`
	Config             string               `json:"config,omitempty"`
	BaseRef            string               `json:"base_ref"`
	HeadRef            string               `json:"head_ref"`
	BaseShort          string               `json:"base_short"`
	HeadShort          string               `json:"head_short"`
	ScanID             string               `json:"scan_id,omitempty"`
	ScannedAt          string               `json:"scanned_at"`
	SheafVersion       string               `json:"sheaf_version,omitempty"`
	RendererVersion    int                  `json:"renderer_version"`
	PRRefDisplayed     string               `json:"pr_ref_displayed"`
	BaseCorpusSummary  CorpusSummary        `json:"base_corpus_summary"`
	HeadCorpusSummary  CorpusSummary        `json:"head_corpus_summary"`
	CoverageDelta      CoverageDeltaSummary `json:"coverage_delta"`
	Findings           []FindingRecord      `json:"findings"`
	SuggestedReviewers []string             `json:"suggested_reviewers"`
	Subscribers        []string             `json:"subscribers"`
	Title              string               `json:"title"`
	Body               string               `json:"body"`
}

// CorpusSummary is the cheap-to-serialize snapshot of a corpus.
// We deliberately do not embed full corpora (per §11 open decision).
type CorpusSummary struct {
	Elements  int `json:"elements"`
	Tests     int `json:"tests"`
	DocClaims int `json:"doc_claims"`
	Profiles  int `json:"profiles"`
	TotalRefs int `json:"total_refs"`
}

// CoverageDeltaSummary describes the rendered delta itself.
type CoverageDeltaSummary struct {
	AffectedElements []string       `json:"affected_elements"`
	SurfaceCounts    map[string]int `json:"surface_counts"`
}

// FindingRecord is the per-finding row included in delta.json. We
// flatten the proto into a stable, sorted JSON shape rather than
// piping protojson's output through — protojson's field order and
// wrapping varies across releases, which would defeat A4.
type FindingRecord struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	Subject  string           `json:"subject"`
	Severity string           `json:"severity"`
	Analyzer string           `json:"analyzer"`
	Message  string           `json:"message"`
	Evidence []EvidenceRecord `json:"evidence,omitempty"`
}

type EvidenceRecord struct {
	Description string `json:"description,omitempty"`
	Path        string `json:"path,omitempty"`
	Line        uint32 `json:"line,omitempty"`
	URL         string `json:"url,omitempty"`
}

// BuildDeltaArtifact assembles a DeltaArtifact from the inputs to and
// outputs of a Render() call. Caller supplies system/refs/version
// metadata that prbot doesn't itself know about.
type DeltaInputs struct {
	System         string
	Config         string
	BaseRef        string
	HeadRef        string
	BaseShort      string
	HeadShort      string
	ScanID         string
	ScannedAt      time.Time
	SheafVersion   string
	PRRefDisplayed string
}

func BuildDeltaArtifact(in DeltaInputs, base, head *corpus.Corpus, comment *Comment) *DeltaArtifact {
	scannedAt := in.ScannedAt
	if scannedAt.IsZero() {
		scannedAt = time.Now().UTC()
	}
	a := &DeltaArtifact{
		SchemaVersion:     DeltaSchemaVersion,
		System:            in.System,
		Config:            in.Config,
		BaseRef:           in.BaseRef,
		HeadRef:           in.HeadRef,
		BaseShort:         in.BaseShort,
		HeadShort:         in.HeadShort,
		ScanID:            in.ScanID,
		ScannedAt:         scannedAt.UTC().Format(time.RFC3339),
		SheafVersion:      in.SheafVersion,
		RendererVersion:   RendererVersion,
		PRRefDisplayed:    in.PRRefDisplayed,
		BaseCorpusSummary: summarizeCorpus(base),
		HeadCorpusSummary: summarizeCorpus(head),
		CoverageDelta: CoverageDeltaSummary{
			AffectedElements: nonNilStrings(comment.AffectedElements),
			SurfaceCounts:    surfaceCounts(comment),
		},
		Findings:           findingsToRecords(comment.Findings),
		SuggestedReviewers: nonNilStrings(comment.SuggestedReviewers),
		Subscribers:        nonNilStrings(comment.Subscribers),
		Title:              comment.Title,
		Body:               comment.Body,
	}
	sort.Strings(a.CoverageDelta.AffectedElements)
	sort.Strings(a.SuggestedReviewers)
	sort.Strings(a.Subscribers)
	return a
}

// MarshalDelta serializes the artifact deterministically: 2-space
// indent, keys in struct-tag order (which we keep stable), trailing
// newline.
func MarshalDelta(a *DeltaArtifact) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(a); err != nil {
		return nil, fmt.Errorf("marshal delta: %w", err)
	}
	return buf.Bytes(), nil
}

func summarizeCorpus(c *corpus.Corpus) CorpusSummary {
	if c == nil {
		return CorpusSummary{}
	}
	s := c.Stats()
	total := 0
	for _, p := range c.Profiles() {
		total += profileRefCount(p)
	}
	return CorpusSummary{
		Elements:  s.Elements,
		Tests:     s.Tests,
		DocClaims: s.DocClaims,
		Profiles:  s.Profiles,
		TotalRefs: total,
	}
}

func profileRefCount(p *coveragepb.CoverageProfile) int {
	n := 0
	if t := p.GetTests(); t != nil {
		n += len(t.GetUnit()) + len(t.GetIntegration()) + len(t.GetE2E()) +
			len(t.GetCtf()) + len(t.GetPerformance()) + len(t.GetFuzz()) + len(t.GetGolden())
	}
	if d := p.GetDocs(); d != nil {
		if r := d.GetReference(); r != nil {
			n += refs.CountReferenceRefs(r)
		}
		n += len(d.GetConcept()) + len(d.GetTutorial())
	}
	return n
}

func surfaceCounts(c *Comment) map[string]int {
	out := make(map[string]int)
	for _, f := range c.Findings {
		k := analyze.KindString(f.GetKind())
		out[k]++
	}
	return out
}

// nonNilStrings returns a non-nil copy of the input slice so JSON
// encoding always produces `[]` rather than `null` for empty lists.
func nonNilStrings(s []string) []string {
	out := make([]string, 0, len(s))
	out = append(out, s...)
	return out
}

func findingsToRecords(fs []*findingpb.Finding) []FindingRecord {
	out := make([]FindingRecord, 0, len(fs))
	for _, f := range fs {
		out = append(out, FindingRecord{
			ID:       f.GetId(),
			Kind:     analyze.KindString(f.GetKind()),
			Subject:  f.GetSubject(),
			Severity: analyze.SeverityName(f.GetSeverity()),
			Analyzer: f.GetAnalyzer(),
			Message:  f.GetMessage(),
			Evidence: evidenceToRecords(f.GetEvidence()),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func evidenceToRecords(es []*findingpb.EvidencePointer) []EvidenceRecord {
	if len(es) == 0 {
		return nil
	}
	out := make([]EvidenceRecord, 0, len(es))
	for _, e := range es {
		out = append(out, EvidenceRecord{
			Description: e.GetDescription(),
			Path:        e.GetLocation().GetPath(),
			Line:        e.GetLocation().GetLine(),
			URL:         e.GetUrl(),
		})
	}
	return out
}
