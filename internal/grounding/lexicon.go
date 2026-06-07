package grounding

import (
	"regexp"
	"sort"
	"strings"

	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// lexEntry is one contract element lifted into the collision lexicon: the
// element itself plus the surface forms a prose author might write for it.
type lexEntry struct {
	elementID string
	display   string // the bare name after the last "/" — e.g. "Node"
	kind      string // "PROTOCOL" | "TYPE" | ...

	// surfaceForms are the lowercased prose spellings that should match
	// this element when they appear as a token run in prose. Always
	// includes the lowercased display name; for CamelCase names it also
	// includes the space-separated word split ("CompositeNodeManager" ->
	// "composite node manager") and, when the LAST word is a common-English
	// noun, that bare word too ("node", "manager", "controller") — those
	// bare-word forms are exactly the collision risk Grounding measures.
	surfaceForms []surfaceForm
}

// surfaceForm is one lowercased prose spelling for an element, tagged with
// whether it collides with common English. A collision form (bare "node")
// is what makes a reference ambiguous; a non-collision multi-word form
// ("composite node manager") is far less likely to be a generic phrase.
type surfaceForm struct {
	text      string // lowercased, single-spaced
	words     int    // number of whitespace-separated words
	collision bool   // last word is a common-English noun
	exactName bool   // text == lowercased full display name (vs a sub-phrase)
}

// lexicon is the per-library collision dictionary plus its match machinery.
type lexicon struct {
	library string
	entries []lexEntry

	// byForm maps a lowercased surface form -> the lexEntries that own it.
	// One form can be owned by several elements (the blast-radius signal:
	// "node" colliding with both Node and NodeController). Forms are
	// matched longest-first so "composite node manager" wins over "node".
	byForm map[string][]formOwner

	// formsByLen is the distinct surface forms sorted by descending word
	// count then descending byte length, for greedy longest-match scanning.
	formsByLen []string
}

// formOwner ties a surface form back to one element and remembers whether,
// for that element, the form is the collision (bare common word) variant.
type formOwner struct {
	idx       int // index into lexicon.entries
	collision bool
	exactName bool
	words     int
}

// commonEnglishNouns is the closed set of contract-name tails that double
// as ordinary English — the words REQUIREMENTS §5.1 calls out (node,
// service, manager, realm, controller, …). A bare prose mention of one of
// these is a collision: the reader can't tell the API noun from the
// generic one without an anchor. Kept deliberately small and global
// (decision: one defensible anchor definition, no per-customer tuning).
var commonEnglishNouns = map[string]bool{
	"node":       true,
	"service":    true,
	"manager":    true,
	"realm":      true,
	"controller": true,
	"channel":    true,
	"handle":     true,
	"directory":  true,
	"file":       true,
	"server":     true,
	"client":     true,
	"connection": true,
	"event":      true,
	"object":     true,
	"resource":   true,
	"device":     true,
	"driver":     true,
	"host":       true,
	"index":      true,
	"factory":    true,
	"provider":   true,
	"registry":   true,
	"store":      true,
	"watcher":    true,
	"listener":   true,
	"request":    true,
	"response":   true,
	"message":    true,
	"property":   true,
	"key":        true,
	"value":      true,
	"entry":      true,
	"item":       true,
	"context":    true,
	"state":      true,
	"status":     true,
	"token":      true,
	"port":       true,
	"socket":     true,
	"stream":     true,
	"buffer":     true,
	"queue":      true,
	"pool":       true,
	"task":       true,
	"job":        true,
	"worker":     true,
	"module":     true,
	"component":  true,
	"protocol":   true,
	"interface":  true,
	"capability": true,
	"binding":    true,
	"loader":     true,
	"runner":     true,
	"dispatcher": true,
	"monitor":    true,
	"observer":   true,
}

// camelSplitRx finds CamelCase / PascalCase boundaries so
// "CompositeNodeManager" -> ["Composite","Node","Manager"]. Also splits a
// run of caps before a cap+lower (e.g. "HTTPServer" -> ["HTTP","Server"]).
var camelSplitRx = regexp.MustCompile(`[A-Z]+(?:[a-z0-9]+)?|[a-z0-9]+`)

// minCollisionLen guards against single-letter or 2-char names producing
// noisy collision forms. The markdown adapter uses the same {2,} floor.
const minCollisionLen = 3

// elementDisplay returns the human-facing short name for an element ID.
// The contract ID is "<library>/<Name>" (FIDL) or a dotted/slashed form;
// the display is the segment after the final "/", falling back to the
// segment after the final "." then the whole ID.
func elementDisplay(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
}

// buildLexicon lifts the library's contract elements into the collision
// dictionary. Only elements belonging to `library` are considered; LIBRARY
// synthetic elements and empty names are skipped (they have no prose form
// worth colliding on). Deterministic: entries and forms are sorted.
func buildLexicon(library string, elements []*contractpb.ContractElement) *lexicon {
	lx := &lexicon{
		library: library,
		byForm:  make(map[string][]formOwner),
	}
	for _, e := range elements {
		if e == nil || e.GetId() == "" {
			continue
		}
		if library != "" && e.GetLibrary() != library {
			continue
		}
		if e.GetKind() == contractpb.ContractElementKind_LIBRARY {
			continue
		}
		display := elementDisplay(e.GetId())
		if display == "" {
			continue
		}
		forms := surfaceFormsFor(display)
		// Element aliases (CLI alternate spellings) widen the net; treat
		// each alias's display tail the same way.
		for _, al := range e.GetAliases() {
			forms = append(forms, surfaceFormsFor(elementDisplay(al))...)
		}
		forms = dedupeForms(forms)
		if len(forms) == 0 {
			continue
		}
		lx.entries = append(lx.entries, lexEntry{
			elementID:    e.GetId(),
			display:      display,
			kind:         e.GetKind().String(),
			surfaceForms: forms,
		})
	}
	// Stable order for reproducible finding IDs downstream.
	sort.Slice(lx.entries, func(i, j int) bool {
		return lx.entries[i].elementID < lx.entries[j].elementID
	})
	lx.index()
	return lx
}

// index populates byForm and formsByLen from the (now sorted) entries.
func (lx *lexicon) index() {
	seen := map[string]bool{}
	for i := range lx.entries {
		for _, f := range lx.entries[i].surfaceForms {
			lx.byForm[f.text] = append(lx.byForm[f.text], formOwner{
				idx:       i,
				collision: f.collision,
				exactName: f.exactName,
				words:     f.words,
			})
			if !seen[f.text] {
				seen[f.text] = true
				lx.formsByLen = append(lx.formsByLen, f.text)
			}
		}
	}
	sort.Slice(lx.formsByLen, func(i, j int) bool {
		a, b := lx.formsByLen[i], lx.formsByLen[j]
		aw, bw := strings.Count(a, " "), strings.Count(b, " ")
		if aw != bw {
			return aw > bw // more words first
		}
		if len(a) != len(b) {
			return len(a) > len(b) // longer bytes first
		}
		return a < b
	})
}

// surfaceFormsFor derives the prose spellings to match for a display name.
//
//   - The full lowercased name, single-spaced if it already contains
//     spaces (rare). Marked exactName. Collision iff it is itself a common
//     English noun (a bare-named element like "Service").
//   - The CamelCase word-split joined with spaces ("composite node
//     manager"), when it differs from the lowercased name and has >1 word.
//   - The bare LAST word ("manager"/"node"/"controller") ONLY when that
//     word is in commonEnglishNouns — this is the collision form Grounding
//     exists to flag. We do NOT emit arbitrary sub-words (no "composite",
//     no "node" out of "NodePropertyKey" unless the tail itself collides),
//     keeping the finding set finite and on-contract.
func surfaceFormsFor(display string) []surfaceForm {
	var out []surfaceForm
	lower := strings.ToLower(strings.TrimSpace(display))
	if len(lower) < minCollisionLen {
		return out
	}
	words := camelSplitRx.FindAllString(display, -1)
	for i := range words {
		words[i] = strings.ToLower(words[i])
	}

	// Concatenated lowercased name — how the identifier appears verbatim in
	// prose ("NodeController" -> "nodecontroller"). This is the exact-name
	// form authors actually write; it is exactName and (for a multi-word or
	// non-common-noun name) non-colliding.
	out = append(out, surfaceForm{
		text:      lower,
		words:     1,
		collision: isCommonNoun(lower),
		exactName: true,
	})

	// Space-separated word-split form ("composite node manager"). Only when
	// it differs from the concatenated form and has >1 word. Also exactName,
	// non-colliding (a multi-word phrase rarely reads as generic English).
	if len(words) > 1 {
		spaced := strings.Join(words, " ")
		if spaced != lower {
			out = append(out, surfaceForm{
				text:      spaced,
				words:     len(words),
				collision: false,
				exactName: true,
			})
		}
	}

	// Bare common-English tail (the collision risk): "manager", "node",
	// "controller". Only emitted when the LAST word is a common-English
	// noun. This is the form Grounding exists to flag.
	if len(words) >= 1 {
		tail := words[len(words)-1]
		if len(tail) >= minCollisionLen && isCommonNoun(tail) && tail != lower {
			out = append(out, surfaceForm{
				text:      tail,
				words:     1,
				collision: true,
				exactName: false,
			})
		}
	}
	return out
}

func isCommonNoun(s string) bool { return commonEnglishNouns[s] }

//nolint:unused // dormant grounding detector, retained (PR #99)
func wordCount(s string) int { return len(strings.Fields(s)) }

// dedupeForms removes duplicate surface texts, keeping the strongest
// (exactName, then collision) variant for each text.
func dedupeForms(in []surfaceForm) []surfaceForm {
	best := map[string]surfaceForm{}
	order := []string{}
	for _, f := range in {
		if f.text == "" {
			continue
		}
		cur, ok := best[f.text]
		if !ok {
			best[f.text] = f
			order = append(order, f.text)
			continue
		}
		// Prefer exactName; otherwise keep whichever marks a collision.
		if (f.exactName && !cur.exactName) || (f.collision && !cur.collision) {
			best[f.text] = f
		}
	}
	out := make([]surfaceForm, 0, len(order))
	for _, t := range order {
		out = append(out, best[t])
	}
	return out
}
