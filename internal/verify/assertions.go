package verify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// This file completes the validate.py merge: the attribution true/false-
// positive half (the precision question — over-attribution). The
// false-negative half (missed edges) already lives in diskgrep.go behind
// --disk. Here Go does only the deterministic work — sampling the attributed
// claims and, in `summarize`, the precision arithmetic. The semantic call —
// "does this test actually exercise this element, or did it match a shared
// token?" — stays the agent's, because only reading the test/doc body can
// answer it. So verify emits a bounded `assertions` array with verdict=null
// for the agent to fill, mirroring `validate.py extract`, and `summarize`
// reads the filled verdicts back, mirroring `validate.py summarize`.

const (
	// defaultMaxAssertionElements caps how many elements per run are sampled
	// for the precision workflow so a huge corpus stays bounded.
	defaultMaxAssertionElements = 50
	// sampleTestsPerElement / sampleDocsPerElement cap the refs sampled
	// within one element (mirrors validate.py --sample-tests/--sample-docs).
	sampleTestsPerElement = 10
	sampleDocsPerElement  = 5
	// collisionWeightBonus pushes common-single-word-named elements (the
	// collision-prone ones) to the front of the sample — they are the
	// likeliest false positives, so they are the most valuable to verify.
	collisionWeightBonus = 1_000_000
)

// Assertion is one sampled attributed claim for the agent to adjudicate.
// JSON field names match validate.py's records so a verdicted JSONL written
// against either tool interoperates. Verdict/Reason are pointers so they
// serialize as null until the agent fills them ("tp" | "fp" | "ambiguous").
type Assertion struct {
	Kind    string `json:"kind"` // "tested_by" | "documented_by"
	Library string `json:"library"`
	Element string `json:"element"`
	Bucket  string `json:"bucket"`

	// tested_by
	TestName  string `json:"test_name,omitempty"`
	TestPath  string `json:"test_path,omitempty"`
	TestLine  int    `json:"test_line,omitempty"`
	Framework string `json:"framework,omitempty"`

	// documented_by
	DocPath string `json:"doc_path,omitempty"`
	DocLine int    `json:"doc_line,omitempty"`
	DocURL  string `json:"doc_url,omitempty"`
	Adapter string `json:"adapter,omitempty"`

	// sampling context — lets the agent (and summarize) know this claim
	// stands in for total_in_bucket claims on the element.
	TotalInBucket int `json:"total_in_bucket"`
	SampleSize    int `json:"sample_size"`

	// filled by the agent after reading the source.
	Verdict *string `json:"verdict"`
	Reason  *string `json:"reason"`
}

type testRefRec struct {
	bucket, path, testName, framework string
	line                              int
}

type docRefRec struct {
	bucket, path, url, adapter string
	line                       int
}

// libraryOf extracts the library prefix of an element id ("<lib>/<local>"),
// or "" when the id is unscoped. Mirrors validate.py library_of.
func libraryOf(elementID string) string {
	if i := strings.Index(elementID, "/"); i >= 0 {
		return elementID[:i]
	}
	return ""
}

// harvestTestRefs returns every deterministic TestRef in a profile, tagged
// with its bucket (tests.unit_tests, tests.integration, …) — mirrors
// validate.py harvest_test_refs so the assertion shape matches.
func harvestTestRefs(prof map[string]any) []testRefRec {
	var out []testRefRec
	if prof == nil {
		return out
	}
	tests, _ := prof["tests"].(map[string]any)
	if tests == nil {
		return out
	}
	for _, b := range detTestBuckets {
		arr, _ := tests[b].([]any)
		bucket := "tests." + b
		if b == "unit" {
			bucket = "tests.unit_tests" // match the categorization-rules leaf
		}
		for _, it := range arr {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, testRefRec{
				bucket:    bucket,
				path:      mstr(m, "path"),
				line:      mint(m, "line"),
				testName:  mstr(m, "testName", "test_name"),
				framework: mstr(m, "framework"),
			})
		}
	}
	return out
}

// harvestDocRefs returns DocRefs across the reference/byAdapter, reference
// sub-lists, and the top-level concept/tutorial/guide buckets — mirrors
// validate.py harvest_doc_refs.
func harvestDocRefs(prof map[string]any) []docRefRec {
	var out []docRefRec
	if prof == nil {
		return out
	}
	docs, _ := prof["docs"].(map[string]any)
	if docs == nil {
		return out
	}
	ref, _ := docs["reference"].(map[string]any)
	if ref != nil {
		for _, sub := range []string{"fidldoc", "clidoc", "dockerdoc"} {
			for _, it := range asSlice(ref[sub]) {
				if m, ok := it.(map[string]any); ok {
					out = append(out, docRefRec{
						bucket: "docs.reference." + sub, path: mstr(m, "path"),
						line: mint(m, "line"), url: mstr(m, "url"),
						adapter: mstrDefault(m, sub, "adapter"),
					})
				}
			}
		}
		byAdapter, _ := ref["byAdapter"].(map[string]any)
		if byAdapter == nil {
			byAdapter, _ = ref["by_adapter"].(map[string]any)
		}
		// Deterministic adapter order.
		names := make([]string, 0, len(byAdapter))
		for a := range byAdapter {
			names = append(names, a)
		}
		sort.Strings(names)
		for _, adapter := range names {
			body, _ := byAdapter[adapter].(map[string]any)
			for _, it := range asSlice(body["refs"]) {
				if m, ok := it.(map[string]any); ok {
					out = append(out, docRefRec{
						bucket: "docs.reference." + adapter, path: mstr(m, "path"),
						line: mint(m, "line"), url: mstr(m, "url"), adapter: adapter,
					})
				}
			}
		}
	}
	for _, top := range []string{"concept", "tutorial", "releaseNotes", "faq"} {
		for _, it := range asSlice(docs[top]) {
			if m, ok := it.(map[string]any); ok {
				out = append(out, docRefRec{
					bucket: "docs." + strings.ToLower(top), path: mstr(m, "path"),
					line: mint(m, "line"), url: mstr(m, "url"), adapter: mstr(m, "adapter"),
				})
			}
		}
	}
	guide, _ := docs["guide"].(map[string]any)
	if guide != nil {
		for _, sub := range []string{"migration", "troubleshooting", "cookbook"} {
			for _, it := range asSlice(guide[sub]) {
				if m, ok := it.(map[string]any); ok {
					out = append(out, docRefRec{
						bucket: "docs.guide." + sub, path: mstr(m, "path"),
						line: mint(m, "line"), url: mstr(m, "url"), adapter: mstr(m, "adapter"),
					})
				}
			}
		}
	}
	return out
}

// sampleAssertions emits a deterministic, bounded sample of attributed
// claims for the agent to adjudicate. It is weighted toward the riskiest
// attributions — common-single-word names first (collision-prone, the
// likeliest FPs), then highest ref count — so a bounded sample concentrates
// on the claims most worth a human read. Same input → same sample (no RNG).
func sampleAssertions(rep *Report, snapLibrary string, rd *scanner.ReportData, profByID map[string]map[string]any, opts Options) {
	maxElems := opts.MaxAssertionElements
	if maxElems <= 0 {
		maxElems = defaultMaxAssertionElements
	}

	type cand struct {
		name   string
		weight int
		tests  []testRefRec
		docs   []docRefRec
	}
	var cands []cand
	for i := range rd.Methods {
		m := rd.Methods[i]
		if m.Removed {
			continue
		}
		tr := harvestTestRefs(profByID[m.Name])
		dr := harvestDocRefs(profByID[m.Name])
		if len(tr) == 0 && len(dr) == 0 {
			continue // only attributed elements carry a precision question
		}
		w := len(tr) + len(dr)
		if collisionWords[localName(m.Name)] {
			w += collisionWeightBonus
		}
		cands = append(cands, cand{name: m.Name, weight: w, tests: tr, docs: dr})
	}
	// Deterministic: riskiest first (weight desc), tie-break by name asc.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].weight != cands[j].weight {
			return cands[i].weight > cands[j].weight
		}
		return cands[i].name < cands[j].name
	})
	if len(cands) > maxElems {
		// #4 bounded, never silently truncated.
		rep.Caveats = append(rep.Caveats, fmt.Sprintf(
			"Attribution sample capped at %d of %d attributed elements (weighted toward collision-prone/high-count; raise --sample-assertions to cover more).",
			maxElems, len(cands)))
		cands = cands[:maxElems]
	}

	var out []Assertion
	for _, c := range cands {
		lib := snapLibrary
		if l := libraryOf(c.name); l != "" {
			lib = l
		}
		sortTestRefs(c.tests)
		ts := c.tests
		if len(ts) > sampleTestsPerElement {
			ts = ts[:sampleTestsPerElement]
		}
		for _, r := range ts {
			out = append(out, Assertion{
				Kind: "tested_by", Library: lib, Element: c.name, Bucket: r.bucket,
				TestName: r.testName, TestPath: r.path, TestLine: r.line, Framework: r.framework,
				TotalInBucket: len(c.tests), SampleSize: len(ts),
			})
		}
		sortDocRefs(c.docs)
		ds := c.docs
		if len(ds) > sampleDocsPerElement {
			ds = ds[:sampleDocsPerElement]
		}
		for _, r := range ds {
			out = append(out, Assertion{
				Kind: "documented_by", Library: lib, Element: c.name, Bucket: r.bucket,
				DocPath: r.path, DocLine: r.line, DocURL: r.url, Adapter: r.adapter,
				TotalInBucket: len(c.docs), SampleSize: len(ds),
			})
		}
	}
	rep.Assertions = out
}

func sortTestRefs(rs []testRefRec) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].path != rs[j].path {
			return rs[i].path < rs[j].path
		}
		if rs[i].line != rs[j].line {
			return rs[i].line < rs[j].line
		}
		return rs[i].testName < rs[j].testName
	})
}

func sortDocRefs(rs []docRefRec) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].path != rs[j].path {
			return rs[i].path < rs[j].path
		}
		if rs[i].line != rs[j].line {
			return rs[i].line < rs[j].line
		}
		return rs[i].bucket < rs[j].bucket
	})
}

// --- small generic-map readers --------------------------------------------

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func mstr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func mstrDefault(m map[string]any, def string, keys ...string) string {
	if s := mstr(m, keys...); s != "" {
		return s
	}
	return def
}

func mint(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}
