package verify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// detectNoSourceMap flags the silent missing-source-map trap: a doc source
// was configured (a concept-doc narrative source, or rendered-reference doc
// dirs), yet EVERY docs.* surface attributed exactly 0. The usual cause is a
// missing categorization-rules.textproto — without it categorization is
// silently skipped and every docs surface reads 0 even though the docs plainly
// exist. That is the highest-confusion 0%: it looks like a real gap but is a
// wiring gap.
//
// Snapshot-derived (the config signals are in the snapshot), so it always
// runs. It stays a WARNING per the honesty rail — verify cannot prove the
// docs exist without reading disk; it can only say "you configured a doc
// source and got nothing, which is far more often a missing source map than
// real absence." The conservative trigger (every surface EXACTLY 0) keeps it
// from firing on a merely-sparse library.
func detectNoSourceMap(rep *Report, rd *scanner.ReportData, snap *scanner.Snapshot) {
	configured := snap.ConceptDocSource || len(snap.DocSurfaceDirs) > 0
	if !configured {
		return // no doc source configured → 0 docs is honest, not a trap
	}
	if rd.ConceptCount != 0 || rd.ConceptDocCount != 0 {
		return // some docs were attributed → categorization is working
	}

	var ev []string
	if snap.ConceptDocSource {
		ev = append(ev, "concept-doc source configured (concept_doc_source = true) but docs.concepts read 0")
	}
	if len(snap.DocSurfaceDirs) > 0 {
		dirs := make([]string, 0, len(snap.DocSurfaceDirs))
		for adapter, dir := range snap.DocSurfaceDirs {
			dirs = append(dirs, fmt.Sprintf("%s → %s", adapter, dir))
		}
		sort.Strings(dirs)
		ev = append(ev, "rendered-reference doc dirs configured but docs.reference read 0: "+strings.Join(dirs, ", "))
	}
	rep.add(Finding{
		Category: CatNoSourceMap, Severity: SeverityWarn,
		Surface:  "docs",
		Title:    "Every docs surface reads 0 but a doc source was configured — likely a missing source map",
		Detail:   "A doc source is configured (a concept-doc source and/or rendered-reference doc dirs), yet every docs.* surface attributed exactly 0. The usual cause is a missing source map (categorization-rules.textproto): without it categorization is silently skipped and every docs surface reads 0 even though the docs exist — a wiring gap, not a real coverage gap.",
		Evidence: ev,
		Fix:      "Stage the source map (categorization-rules.textproto) at the scan root (or pass it explicitly) and re-scan; confirm on disk that a sample of the 'undocumented' elements actually have docs.",
	})
}
