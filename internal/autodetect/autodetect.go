// Package autodetect performs a cheap, deterministic file-sniff over a
// checkout to decide which adapters a zero-config `sheaf scan --auto`
// run should wire. It is the entry point of the LLM-augmented funnel:
// schema-backed surfaces are routed to their authoritative deterministic
// adapter and NEVER to the LLM; only the schemaless C++ header tail is
// additionally handed to the llmextract adapter.
//
// Detection is intentionally lexical and bounded: extension bucketing is
// free, and the few content checks (clap derives, gtest/rust test
// markers) stop reading a category as soon as one positive marker is
// found. No toolchain is invoked and no file is parsed.
//
// The Result drives three downstream consumers: adapter selection
// (orchestrator), the generated sheaf.textproto (the structural freeze),
// and sheaf-hardening.md (what to replace with deterministic passes).
package autodetect

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
)

// Role classifies which corpus a detection feeds.
type Role string

const (
	RoleContract Role = "contract"
	RoleTest     Role = "test"
	RoleDoc      Role = "doc"
)

// Tier mirrors RowProvenance.Tier: a schema/grammar adapter is
// deterministic; the llmextract adapter is the LLM tier.
type Tier string

const (
	TierDeterministic Tier = "deterministic"
	TierLLM           Tier = "llm"
)

// Detection is one ecosystem the sniffer found, with the adapter to wire
// and the evidence behind it.
type Detection struct {
	Adapter   string   // "proto" | "fidl" | "clap" | "cml" | "cppheader" | "llmextract" | "gtest" | "rust-test" | "markdown" | "rst"
	Role      Role     // which corpus it feeds
	Tier      Tier     // deterministic vs llm
	FileCount int      // matching files in scope
	Samples   []string // up to maxSamples repo-relative paths, sorted
	Include   []string // glob(s) to scope the adapter to
}

// Result is the full detection over a tree.
type Result struct {
	Detections []Detection
	// SchemalessHeaders is true when C++ headers are present. C++ headers
	// have no authoritative machine-readable generator, so they are the
	// schemaless tail where the LLM extractor earns its place — this flag
	// is what makes auto wire llmextract alongside cppheader.
	SchemalessHeaders bool
	// CppTestMacros are custom test-DECLARING macros (suite,name,… shape)
	// found in the C++ test corpus beyond TEST/TEST_F/TEST_P — e.g.
	// Pigweed's PW_CONSTEXPR_TEST. autoconfig folds them into protocpp's
	// extra_test_macros so those tests aren't silently invisible. Sorted,
	// deduped. Intra-test helper/assertion macros are excluded.
	CppTestMacros []string
	// CppAttributeMacros are leading class/struct attribute macros found in
	// the public headers — `class PW_LOCKABLE Foo {…}` — which otherwise
	// poison cppheader's "next token is the class name" heuristic and drop
	// the type from the contract. autoconfig folds them into cppheader's
	// ignored_attribute_macros. Sorted, deduped.
	CppAttributeMacros []string
	// HasDoxygen is true when the tree ships a Doxyfile (the project
	// documents its C/C++ API with Doxygen). autoconfig wires the doxygen
	// doc-parser; the --auto runner generates the XML.
	HasDoxygen bool
}

// Contract returns the detections that feed the contract corpus.
func (r *Result) Contract() []Detection { return r.byRole(RoleContract) }

// Tests returns the detections that feed the test corpus.
func (r *Result) Tests() []Detection { return r.byRole(RoleTest) }

// Docs returns the detections that feed the doc corpus.
func (r *Result) Docs() []Detection { return r.byRole(RoleDoc) }

func (r *Result) byRole(role Role) []Detection {
	var out []Detection
	for _, d := range r.Detections {
		if d.Role == role {
			out = append(out, d)
		}
	}
	return out
}

// Has reports whether an adapter was detected.
func (r *Result) Has(adapter string) bool {
	for _, d := range r.Detections {
		if d.Adapter == adapter {
			return true
		}
	}
	return false
}

const maxSamples = 5

// extension → (adapter, role) for the pure-extension detectors. The
// content-gated detectors (clap/gtest/rust-test) and the dual
// header→cppheader+llmextract routing are handled separately below.
type extRule struct {
	adapter string
	role    Role
}

var extContract = map[string]extRule{
	".proto": {"proto", RoleContract},
	".fidl":  {"fidl", RoleContract},
	".cml":   {"cml", RoleContract},
}

var extDoc = map[string]extRule{
	".md":  {"markdown", RoleDoc},
	".rst": {"rst", RoleDoc},
}

// bucket accumulates files for one detected adapter.
type bucket struct {
	count   int
	samples []string
	globs   map[string]bool
}

func (b *bucket) add(rel, glob string) {
	b.count++
	if len(b.samples) < maxSamples {
		b.samples = append(b.samples, rel)
	}
	if b.globs == nil {
		b.globs = map[string]bool{}
	}
	b.globs[glob] = true
}

// Detect walks repoRoot restricted to `include` (default ["**/*"]) minus
// `exclude`, and returns the detected adapters. Build-artifact dirs are
// skipped by the walker. Content sniffing for clap / gtest / rust-test
// reads at most a handful of candidate files (it stops once a category
// is confirmed), so a large tree stays cheap.
func Detect(repoRoot string, include, exclude []string) (*Result, error) {
	// scoped is true when the caller narrowed the scan to explicit globs
	// (not the whole-repo default). In that case the generated per-adapter
	// Include should be those exact globs — the walk only visited files
	// matching them, so every populated bucket is within them — rather
	// than the broad `**/*.ext` we derive for a true zero-config run.
	scoped := len(include) > 0 && !(len(include) == 1 && include[0] == "**/*")
	userInclude := append([]string(nil), include...)
	if len(include) == 0 {
		include = []string{"**/*"}
	}

	buckets := map[string]*bucket{}
	get := func(adapter string) *bucket {
		b := buckets[adapter]
		if b == nil {
			b = &bucket{}
			buckets[adapter] = b
		}
		return b
	}

	// Candidate paths for content-gated detection, sniffed after the walk
	// so we can stop early per category.
	var rsFiles, ccFiles, hdrFiles []string
	headerPresent := false
	doxygenPresent := false

	err := adapters.WalkMatching(repoRoot, include, exclude, func(rel string, _ fs.DirEntry) error {
		lower := strings.ToLower(rel)
		ext := extOf(lower)

		// Doxygen config: a Doxyfile means the project documents its C/C++
		// API with Doxygen — the authoritative reference surface.
		if base := baseName(lower); base == "doxyfile" || strings.HasSuffix(base, ".doxyfile") {
			doxygenPresent = true
			return nil
		}
		// Pure-extension contract surfaces.
		if r, ok := extContract[ext]; ok {
			get(r.adapter).add(rel, "**/*"+ext)
			return nil
		}
		// Docs.
		if r, ok := extDoc[ext]; ok {
			get(r.adapter).add(rel, "**/*"+ext)
			return nil
		}
		// C++ headers → cppheader (deterministic) + llmextract candidate.
		if ext == ".h" || ext == ".hpp" || ext == ".hh" {
			headerPresent = true
			hdrFiles = append(hdrFiles, rel)
			get("cppheader").add(rel, "**/*"+ext)
			return nil
		}
		// C++ sources are gtest candidates (content-gated).
		if ext == ".cc" || ext == ".cpp" || ext == ".cxx" {
			ccFiles = append(ccFiles, rel)
			return nil
		}
		// Rust sources are clap / rust-test candidates (content-gated).
		if ext == ".rs" {
			rsFiles = append(rsFiles, rel)
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Content-gated: gtest. A C++ source naming a TEST/TEST_F/TEST_P macro.
	sniffCategory(repoRoot, ccFiles, []string{"TEST(", "TEST_F(", "TEST_P(", "TYPED_TEST("}, func(rel string) {
		get("gtest").add(rel, "**/*"+extOf(strings.ToLower(rel)))
	})
	// Content-gated: rust-test and clap (a .rs file can be both).
	sniffCategory(repoRoot, rsFiles, []string{"#[test]", "#[tokio::test]"}, func(rel string) {
		get("rust-test").add(rel, "**/*.rs")
	})
	sniffCategory(repoRoot, rsFiles, []string{"derive(Parser)", "derive(Subcommand)", "derive(Args)"}, func(rel string) {
		get("clap").add(rel, "**/*.rs")
	})

	// C++ headers have no authoritative generator: route the same set to
	// llmextract as the LLM tier alongside cppheader. This is the only
	// place the funnel admits the LLM extractor.
	if headerPresent {
		hb := get("cppheader")
		lx := get("llmextract")
		lx.count = hb.count
		lx.samples = append([]string(nil), hb.samples...)
		lx.globs = cloneGlobs(hb.globs)
	}

	// Doxygen is a repo-level fact: detect the Doxyfile at conventional root
	// locations too, so a scan scoped to a subtree (which may not contain the
	// Doxyfile — it usually lives at the root or under docs/) still wires the
	// reference surface.
	if !doxygenPresent && hasDoxyfileAtRoot(repoRoot) {
		doxygenPresent = true
	}

	res := &Result{SchemalessHeaders: headerPresent, HasDoxygen: doxygenPresent}
	// C++ tuning surveys: only meaningful when there's a C++ header contract.
	if headerPresent {
		res.CppTestMacros = surveyTestMacros(repoRoot, ccFiles)
		res.CppAttributeMacros = surveyAttributeMacros(repoRoot, hdrFiles)
	}
	for adapter, b := range buckets {
		inc := sortedGlobs(b.globs)
		if scoped {
			inc = userInclude
		}
		res.Detections = append(res.Detections, Detection{
			Adapter:   adapter,
			Role:      roleOf(adapter),
			Tier:      tierOf(adapter),
			FileCount: b.count,
			Samples:   b.samples,
			Include:   inc,
		})
	}
	// Deterministic ordering: contract before test before doc, then by
	// adapter name. Keeps the generated config byte-stable.
	sort.Slice(res.Detections, func(i, j int) bool {
		ri, rj := roleRank(res.Detections[i].Role), roleRank(res.Detections[j].Role)
		if ri != rj {
			return ri < rj
		}
		return res.Detections[i].Adapter < res.Detections[j].Adapter
	})
	return res, nil
}

// sniffCategory reads each candidate (bounded) and calls hit(rel) for the
// first file containing any marker, then stops — presence is all we need.
func sniffCategory(repoRoot string, candidates, markers []string, hit func(rel string)) {
	for _, rel := range candidates {
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			continue
		}
		s := string(body)
		for _, m := range markers {
			if strings.Contains(s, m) {
				hit(rel)
				return
			}
		}
	}
}

func extOf(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[i:]
	}
	return ""
}

// hasDoxyfileAtRoot checks the conventional Doxyfile locations at the repo
// root so Doxygen is detected even when the scan is scoped to a subtree that
// doesn't itself contain the Doxyfile.
func hasDoxyfileAtRoot(repoRoot string) bool {
	for _, rel := range []string{"Doxyfile", "docs/Doxyfile", "docs/doxygen/Doxyfile", "doc/Doxyfile"} {
		if _, err := adapters.ReadFile(repoRoot, rel); err == nil {
			return true
		}
	}
	return false
}

// baseName returns the final element of a forward-slash repo-relative path.
func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// customTestMacroRx matches a SCREAMING_CASE macro containing "TEST" invoked
// as MACRO(Suite, Name, …) where Suite and Name are CamelCase — the
// test-DECLARING shape. That excludes intra-test assertion/helper macros
// (PW_TEST_EXPECT_EQ(actual, expected), TEST_STRING(create_string, …)) whose
// args are lowercase/expressions, so a helper never lands in extra_test_macros.
var customTestMacroRx = regexp.MustCompile(
	`\b([A-Z][A-Z0-9_]*TEST[A-Z0-9_]*)\s*\(\s*[A-Z][A-Za-z0-9_]*\s*,\s*[A-Z][A-Za-z0-9_]*`)

var builtinTestMacros = map[string]bool{
	"TEST": true, "TEST_F": true, "TEST_P": true,
	"TYPED_TEST": true, "TYPED_TEST_P": true,
}

// surveyTestMacros scans the C++ test corpus for custom test-declaring macros
// beyond the gtest built-ins, so protocpp's extra_test_macros covers them and
// those tests aren't silently invisible.
func surveyTestMacros(repoRoot string, ccFiles []string) []string {
	seen := map[string]bool{}
	for _, rel := range ccFiles {
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			continue
		}
		for _, m := range customTestMacroRx.FindAllStringSubmatch(string(body), -1) {
			if name := m[1]; !builtinTestMacros[name] {
				seen[name] = true
			}
		}
	}
	return sortedKeys(seen)
}

// attrMacroRx matches a leading class/struct attribute macro: a SCREAMING_CASE
// token between `class`/`struct` and the (following) type name. The captured
// token has no lowercase letter, which distinguishes an attribute macro from a
// normal CamelCase class name (`class Foo {` has no second identifier and never
// matches).
var attrMacroRx = regexp.MustCompile(
	`(?m)^\s*(?:class|struct)\s+([A-Z_][A-Z0-9_]+)\s+[A-Za-z_]`)

// surveyAttributeMacros scans the headers for leading class/struct attribute
// macros that would otherwise poison cppheader's name heuristic and drop the
// type from the contract, so cppheader's ignored_attribute_macros skips them.
func surveyAttributeMacros(repoRoot string, hdrFiles []string) []string {
	seen := map[string]bool{}
	for _, rel := range hdrFiles {
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			continue
		}
		for _, m := range attrMacroRx.FindAllStringSubmatch(string(body), -1) {
			seen[m[1]] = true
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func roleOf(adapter string) Role {
	switch adapter {
	case "gtest", "rust-test":
		return RoleTest
	case "markdown", "rst":
		return RoleDoc
	default:
		return RoleContract
	}
}

func tierOf(adapter string) Tier {
	if adapter == "llmextract" {
		return TierLLM
	}
	return TierDeterministic
}

func roleRank(r Role) int {
	switch r {
	case RoleContract:
		return 0
	case RoleTest:
		return 1
	case RoleDoc:
		return 2
	default:
		return 3
	}
}

func cloneGlobs(m map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func sortedGlobs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for g := range m {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}
