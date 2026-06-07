package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rulesIsolationManifest builds a manifest with four entries, each with its
// OWN config directory (so each gets its OWN sibling categorization-rules
// file). The libraries and outputs are distinct per entry. The caller
// supplies, per entry, the sibling rules body or "" for no sibling rules.
//
// Every entry shares the same repo (cobra yaml under repoRoot/cmds-<lib>),
// so under the OLD staging approach all four would have raced on a single
// repoRoot/categorization-rules.textproto. The per-entry sibling layout is
// what the parallel fan-out must read in isolation.
func rulesIsolationManifest(t *testing.T, rules map[string]string) (manifestPath, repoRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	manifestDir := t.TempDir()
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	libs := []string{"alpha", "bravo", "charlie", "delta"}
	var entries strings.Builder
	for _, lib := range libs {
		// Each library gets its own cobra yaml dir + config dir.
		write(filepath.Join(repoRoot, "cmds-"+lib, lib+".yaml"), fmt.Sprintf(toolYAML, lib))
		write(filepath.Join(manifestDir, "cfg-"+lib, "sheaf.textproto"), fmt.Sprintf(toolConfig, lib, "cmds-"+lib))
		if body, ok := rules[lib]; ok && body != "" {
			write(filepath.Join(manifestDir, "cfg-"+lib, "categorization-rules.textproto"), body)
		}
		fmt.Fprintf(&entries,
			"entries { config_path: %q library: %q ecosystem: \"cli\" library_label: %q output: %q }\n",
			"cfg-"+lib+"/sheaf.textproto", lib, strings.ToUpper(lib[:1])+lib[1:]+" CLI", lib+".html")
	}

	manifestPath = filepath.Join(manifestDir, "manifest.textproto")
	write(manifestPath, entries.String())
	return manifestPath, repoRoot
}

// TestRunManifest_RulesIsolation_Parallel is the concurrency-correctness
// test. Four entries each carry their own sibling categorization-rules
// file; exactly one ("bravo") is MALFORMED. Run under jobs>1 (and -race),
// the only entry that may fail is bravo (its own rules don't parse); the
// other three must always succeed with their own (valid or absent) rules.
//
// This is the cross-contamination assertion the old shared-repoRoot rules
// staging could not satisfy: if entries raced on one rules file, bravo's
// bad file could clobber a neighbor's good one (a spurious extra failure)
// or a neighbor's good file could overwrite bravo's before it loaded
// (bravo spuriously passes). The strict "{bravo} fails, the rest pass,
// every iteration" invariant only holds when each entry reads its own
// sibling file in isolation.
//
// It also asserts the index lists entries in MANIFEST ORDER regardless of
// completion order under parallelism (order is load-bearing for the index
// grouping), and that each succeeding entry rendered ITS OWN report (its
// library label appears in its own output file, never a neighbor's).
func TestRunManifest_RulesIsolation_Parallel(t *testing.T) {
	const validRules = `version: 1
category {
  dotted_path: "cli.commands"
  paths: "**/*.yaml"
}
`
	const validRulesDelta = `version: 1
category {
  dotted_path: "docs.concepts"
  paths: "docs/**/*.md"
}
`
	// bravo's rules are syntactically broken textproto — LoadRules fails.
	const badRules = `version: 1
category { this is not valid textproto ][ `

	// Distinct per-entry rules: alpha valid, bravo malformed, charlie none,
	// delta a DIFFERENT valid ruleset. No two share a file or a body.
	rules := map[string]string{
		"alpha": validRules,
		"bravo": badRules,
		// charlie: intentionally absent (uncategorized path)
		"delta": validRulesDelta,
	}

	// Hammer it under jobs=4 across several iterations to shake any race.
	for iter := 0; iter < 8; iter++ {
		manifestPath, repoRoot := rulesIsolationManifest(t, rules)
		outDir := t.TempDir()
		var log bytes.Buffer
		// Caching OFF (empty dir) so every entry actually scans and the
		// rules-load path runs for all four on every iteration.
		err := RunManifest(context.Background(), manifestPath, outDir, repoRoot, "", "", false, false, 4, "", false, &log)
		if err != nil {
			t.Fatalf("iter %d: RunManifest (continue-on-failure) should not error: %v\nlog:\n%s", iter, err, log.String())
		}

		logStr := log.String()
		// Exactly bravo must be reported FAILED; the others must not be.
		if !strings.Contains(logStr, "[bravo] FAILED") {
			t.Errorf("iter %d: expected bravo to FAIL on its own malformed rules; log:\n%s", iter, logStr)
		}
		for _, lib := range []string{"alpha", "charlie", "delta"} {
			if strings.Contains(logStr, "["+lib+"] FAILED") {
				t.Errorf("iter %d: %s FAILED — bravo's malformed rules leaked across entries (cross-contamination); log:\n%s", iter, lib, logStr)
			}
			// The succeeding entry must have rendered its own report file…
			_, cDir := splitOut(outDir)
			outFile := filepath.Join(cDir, lib+".html")
			html, rerr := os.ReadFile(outFile)
			if rerr != nil {
				t.Errorf("iter %d: %s.html missing (entry should have rendered): %v", iter, lib, rerr)
				continue
			}
			// …whose MASTHEAD subject is its own library, never a neighbor's
			// — proof the config/rules pair didn't cross wires under
			// parallelism. The report's subject library is the unique
			// `strip-lib` span (the run-switcher nav lists every library, so
			// a bare name match would false-positive on the nav; strip-lib
			// is singular and identifies the rendered report itself).
			ownStrip := `class="strip-lib">` + lib + `<`
			if !strings.Contains(string(html), ownStrip) {
				t.Errorf("iter %d: %s.html masthead subject is not its own library %q", iter, lib, lib)
			}
			for _, other := range []string{"alpha", "bravo", "charlie", "delta"} {
				if other == lib {
					continue
				}
				if strings.Contains(string(html), `class="strip-lib">`+other+`<`) {
					t.Errorf("iter %d: %s.html masthead subject leaked neighbor library %q (config/rules crossed wires)", iter, lib, other)
				}
			}
		}

		// Index order must equal manifest order (alpha, bravo, charlie,
		// delta) regardless of which entry finished first. We check the
		// order of the per-entry links in index.html.
		idxPath, _ := splitOut(outDir)
		index, _ := os.ReadFile(idxPath)
		assertOrder(t, iter, string(index), []string{"alpha.html", "charlie.html", "delta.html"})
	}
}

// assertOrder fails if the needles do not appear in the given order within
// haystack (each needle must appear, and at a strictly increasing index).
func assertOrder(t *testing.T, iter int, haystack string, needles []string) {
	t.Helper()
	prev := -1
	for _, n := range needles {
		i := strings.Index(haystack, n)
		if i < 0 {
			t.Errorf("iter %d: index.html missing %q", iter, n)
			return
		}
		if i <= prev {
			t.Errorf("iter %d: index.html order wrong: %q appeared before a prior entry", iter, n)
			return
		}
		prev = i
	}
}
