// Package affordance implements the cross-source matcher that
// discovers SAME_AFFORDANCE relationships across ContractElement rows
// emitted by different adapters.
//
// An "affordance" is one underlying capability the system offers,
// regardless of how many surfaces declare it. The canonical case is a
// config knob like `max_connections` that shows up as a CONFIG_KNOB in
// `.cml`, as a CONFIG_KNOB derived from an argh `option, default = "…"`,
// as an env-var binding, and as a YAML key in a config file — all four
// rows are the same affordance reached through different surfaces.
//
// v1 scope: match CONFIG_KNOB ↔ CONFIG_KNOB by normalized name plus
// (when present) type compatibility. The matcher is deliberately
// conservative — it emits SAME_AFFORDANCE relationships with explicit
// confidence scores, and the consumer decides what threshold to act on.
// FACET_OF (the directional refinement) is reserved for v2 when we know
// how to pick the canonical side of a cluster.
//
// Output:
//   - Each matched element gains one Relationship per peer in its
//     cluster (pairwise, bidirectional). For a cluster of N elements
//     each element gets N-1 relationships.
//   - Confidence: 0.7 baseline for same normalized name + same kind;
//     +0.15 if EcosystemMeta `type` matches; +0.10 if `max_size` or
//     `max_count` constraint matches. Capped at 0.95.
//   - Clusters larger than MaxClusterSize are skipped with a log line
//     (their existence is itself anomalous and a sign the heuristic
//     fired on something too-generic like "verbose").
package affordance

import (
	"sort"
	"strings"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

const Name = "affordance"
const Version = "0.1.0"

const (
	// defaultConfidenceBaseline is awarded for any same-normalized-name,
	// same-kind match. Below this, no relationship is emitted at all.
	defaultConfidenceBaseline = 0.70

	// defaultConfidenceTypeMatch is added when EcosystemMeta["type"]
	// matches between two elements.
	defaultConfidenceTypeMatch = 0.15

	// defaultConfidenceConstraintMatch is added per matching constraint
	// field (max_size, max_count, default).
	defaultConfidenceConstraintMatch = 0.05

	// defaultMaxClusterSize caps how large a same-name cluster may be
	// before the matcher treats it as too-generic and skips it. The
	// rationale: a knob like "verbose" or "debug" appears in dozens of
	// tools; emitting hundreds of SAME_AFFORDANCE edges for those would
	// drown the signal and almost always be wrong (they're independent
	// knobs that happen to share a name).
	defaultMaxClusterSize = 8

	// defaultConfidenceMax caps the score so the matcher's best output
	// stays below the level a structurally-grounded relationship would
	// carry implicitly.
	defaultConfidenceMax = 0.95
)

type Config struct {
	// MinConfidence: relationships scoring below this are not emitted.
	// Defaults to defaultConfidenceBaseline (0.70).
	MinConfidence float64

	// MaxClusterSize: clusters larger than this are skipped. Defaults
	// to defaultMaxClusterSize (8).
	MaxClusterSize int

	// MatchKinds: which ContractElementKind values participate in
	// matching. Defaults to CONFIG_KNOB only. Future versions may add
	// METHOD ↔ CLI_COMMAND etc.
	MatchKinds []contractpb.ContractElementKind
}

type Matcher struct {
	minConfidence  float64
	maxClusterSize int
	matchKinds     map[contractpb.ContractElementKind]bool
}

func New(cfg Config) *Matcher {
	min := cfg.MinConfidence
	if min == 0 {
		min = defaultConfidenceBaseline
	}
	max := cfg.MaxClusterSize
	if max == 0 {
		max = defaultMaxClusterSize
	}
	kinds := map[contractpb.ContractElementKind]bool{}
	if len(cfg.MatchKinds) == 0 {
		kinds[contractpb.ContractElementKind_CONFIG_KNOB] = true
	} else {
		for _, k := range cfg.MatchKinds {
			kinds[k] = true
		}
	}
	return &Matcher{
		minConfidence:  min,
		maxClusterSize: max,
		matchKinds:     kinds,
	}
}

func (m *Matcher) Name() string    { return Name }
func (m *Matcher) Version() string { return Version }

// Annotate walks the element corpus, finds SAME_AFFORDANCE clusters,
// and mutates each matched element by appending one SAME_AFFORDANCE
// Relationship per peer. Returns a Stats summary for logging.
//
// Idempotent: calling Annotate twice on the same input doesn't
// double-emit relationships (it dedups by (kind, target_id) on the
// element's existing Relationships slice).
func (m *Matcher) Annotate(elements []*contractpb.ContractElement) Stats {
	stats := Stats{}
	if len(elements) == 0 {
		return stats
	}

	// 1. Bucket by (kind, normalized_name).
	type bucketKey struct {
		kind contractpb.ContractElementKind
		name string
	}
	buckets := map[bucketKey][]*contractpb.ContractElement{}
	for _, e := range elements {
		if !m.matchKinds[e.GetKind()] {
			continue
		}
		nn := normalizeName(e.GetId())
		if nn == "" {
			continue
		}
		k := bucketKey{kind: e.GetKind(), name: nn}
		buckets[k] = append(buckets[k], e)
	}

	// 2. For each bucket with >= 2 elements, emit pairwise relationships.
	// Sort bucket-keys for deterministic iteration so test output is stable.
	keys := make([]bucketKey, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		return keys[i].kind < keys[j].kind
	})

	for _, k := range keys {
		cluster := buckets[k]
		if len(cluster) < 2 {
			continue
		}
		if len(cluster) > m.maxClusterSize {
			stats.SkippedTooLarge++
			stats.SkippedClusterNames = append(stats.SkippedClusterNames, k.name)
			continue
		}
		// Sort the cluster by ID so emission is deterministic.
		sort.Slice(cluster, func(i, j int) bool { return cluster[i].GetId() < cluster[j].GetId() })

		// Emit pairwise (bidirectional) SAME_AFFORDANCE edges.
		for i := 0; i < len(cluster); i++ {
			for j := 0; j < len(cluster); j++ {
				if i == j {
					continue
				}
				src, tgt := cluster[i], cluster[j]
				conf := scoreMatch(src, tgt)
				if conf < m.minConfidence {
					continue
				}
				if hasRelationship(src, contractpb.RelationshipKind_SAME_AFFORDANCE, tgt.GetId()) {
					continue
				}
				src.Relationships = append(src.Relationships, &contractpb.Relationship{
					Kind:            contractpb.RelationshipKind_SAME_AFFORDANCE,
					TargetElementId: tgt.GetId(),
					Confidence:      conf,
					Note:            "affordance/" + Version,
				})
				stats.RelationshipsEmitted++
			}
		}
		stats.ClustersMatched++
	}
	return stats
}

// Stats summarizes one Annotate pass.
type Stats struct {
	ClustersMatched      int
	RelationshipsEmitted int
	SkippedTooLarge      int
	SkippedClusterNames  []string
}

// ----- scoring -----

// scoreMatch returns the SAME_AFFORDANCE confidence for a pair of
// candidate elements that already share a normalized name and kind.
// Capped at defaultConfidenceMax.
func scoreMatch(a, b *contractpb.ContractElement) float64 {
	score := defaultConfidenceBaseline
	ta, tb := metaString(a, "type"), metaString(b, "type")
	if ta != "" && ta == tb {
		score += defaultConfidenceTypeMatch
	}
	for _, field := range []string{"max_size", "max_count", "default"} {
		va, vb := metaString(a, field), metaString(b, field)
		if va != "" && va == vb {
			score += defaultConfidenceConstraintMatch
		}
	}
	if score > defaultConfidenceMax {
		score = defaultConfidenceMax
	}
	return score
}

func metaString(e *contractpb.ContractElement, field string) string {
	m := e.GetEcosystemMeta()
	if m == nil {
		return ""
	}
	v, ok := m.GetFields()[field]
	if !ok {
		return ""
	}
	return v.GetStringValue()
}

// ----- name normalization -----

// normalizeName extracts a comparable name from a ContractElement.Id
// and reduces it to lowercase ASCII identifier characters with `_` as
// the only separator. Examples:
//
//	"cml:rust/config_example/greeting"          → "greeting"
//	"cml:cpp/config_example/greeting"           → "greeting"
//	"argh:create --arch"                         → "arch"
//	"argh:list --include-components"             → "include_components"
//	"argh:list --include_components"             → "include_components"
//	"argh:server MAX_CONNECTIONS"                → "max_connections"
//	"fuchsia.io/Directory.Open"                  → "open"
func normalizeName(id string) string {
	last := id
	// Strip ecosystem prefix like "cml:" or "argh:".
	if i := strings.IndexByte(last, ':'); i >= 0 {
		last = last[i+1:]
	}
	// Take the last path-like segment.
	if i := strings.LastIndexAny(last, "/."); i >= 0 {
		last = last[i+1:]
	}
	// Argh CLI flags ("subcmd --foo") and POSITIONALs ("subcmd <foo>")
	// are space-separated in the ID; take the trailing token.
	if i := strings.LastIndexByte(last, ' '); i >= 0 {
		last = last[i+1:]
	}
	// Strip CLI flag/positional sigils.
	last = strings.TrimPrefix(last, "--")
	last = strings.TrimPrefix(last, "-")
	last = strings.TrimPrefix(last, "<")
	last = strings.TrimSuffix(last, ">")
	// Lowercase + replace separator characters with underscore.
	var b strings.Builder
	b.Grow(len(last))
	for i := 0; i < len(last); i++ {
		c := last[i]
		switch {
		case c >= 'A' && c <= 'Z':
			// Insert underscore before camelCase boundary unless at start
			// or the previous char was already an upper / underscore.
			if i > 0 {
				prev := last[i-1]
				if prev >= 'a' && prev <= 'z' {
					b.WriteByte('_')
				}
			}
			b.WriteByte(c - 'A' + 'a')
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_':
			b.WriteByte(c)
		case c == '-' || c == '.' || c == '/':
			b.WriteByte('_')
		}
	}
	out := b.String()
	// Collapse repeated underscores and trim leading/trailing.
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return strings.Trim(out, "_")
}

// hasRelationship checks for an existing (kind, target_id) edge to
// keep Annotate idempotent across repeated invocations.
func hasRelationship(e *contractpb.ContractElement, k contractpb.RelationshipKind, target string) bool {
	for _, r := range e.GetRelationships() {
		if r.GetKind() == k && r.GetTargetElementId() == target {
			return true
		}
	}
	return false
}
