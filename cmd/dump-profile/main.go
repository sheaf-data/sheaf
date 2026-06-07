// dump-profile runs the full pipeline (ingest+index) against a repo
// and prints the CoverageProfile for one named element. Used to
// spot-check stage-3 output during validation.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/sheaf-data/sheaf/internal/config"
	"github.com/sheaf-data/sheaf/internal/orchestrator"
	categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	repo := flag.String("repo", ".", "repo root")
	cfgPath := flag.String("config", "", "sheaf.textproto path")
	rulesPath := flag.String("rules", "", "source map (categorization-rules.textproto) path; optional")
	elementID := flag.String("element", "fuchsia.io/Directory.Open", "ContractElement ID to dump")
	listOnly := flag.Bool("list", false, "list element IDs matching the pattern instead of dumping a profile")
	summary := flag.Bool("summary", false, "summary of profiles with non-empty test buckets")
	flag.Parse()

	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		fatal(err)
	}
	var rules *categorizationpb.Rules
	if *rulesPath != "" {
		r, err := config.LoadRules(*rulesPath)
		if err != nil {
			fatal(err)
		}
		rules = r
	}
	o, err := orchestrator.New(cfg, rules, *repo)
	if err != nil {
		fatal(err)
	}
	res, err := o.Run(context.Background())
	if err != nil {
		fatal(err)
	}

	if *listOnly {
		ids := res.Corpus.ElementIDs()
		for _, id := range ids {
			fmt.Println(id)
		}
		return
	}

	if *summary {
		printSummary(res)
		return
	}

	prof := res.Corpus.Profile(*elementID)
	if prof == nil {
		fmt.Fprintf(os.Stderr, "no profile for element %q\n", *elementID)
		fmt.Fprintf(os.Stderr, "ingest produced %d elements; try --list to browse\n", res.Corpus.Stats().Elements)
		os.Exit(2)
	}

	m := protojson.MarshalOptions{Multiline: true, Indent: "  "}
	out, err := m.Marshal(prof)
	if err != nil {
		fatal(err)
	}
	// Pretty-print via json.Indent in case protojson's format isn't pretty.
	var pretty map[string]any
	if err := json.Unmarshal(out, &pretty); err == nil {
		b, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Println(string(out))
	}
}

func printSummary(res *orchestrator.Result) {
	type entry struct {
		id    string
		tests int
		docs  int
		gaps  int
	}
	var rows []entry
	for _, p := range res.Corpus.Profiles() {
		e := entry{id: p.GetElementId()}
		if p.Tests != nil {
			e.tests = len(p.Tests.Unit) + len(p.Tests.Integration) + len(p.Tests.E2E) + len(p.Tests.Ctf) + len(p.Tests.Performance) + len(p.Tests.Fuzz) + len(p.Tests.Golden)
		}
		if p.Docs != nil {
			if p.Docs.Reference != nil {
				e.docs = len(p.Docs.Reference.Fidldoc) + len(p.Docs.Reference.Clidoc)
			}
			e.docs += len(p.Docs.Concept) + len(p.Docs.Tutorial)
		}
		if p.GapsSummary != nil {
			e.gaps = len(p.GapsSummary.Missing)
		}
		if e.tests > 0 || e.docs > 0 {
			rows = append(rows, e)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		// Sort by tests desc, then docs desc.
		if rows[i].tests != rows[j].tests {
			return rows[i].tests > rows[j].tests
		}
		return rows[i].docs > rows[j].docs
	})
	fmt.Printf("%-60s %6s %6s %6s\n", "ELEMENT_ID", "TESTS", "DOCS", "GAPS")
	for i, r := range rows {
		if i >= 40 {
			fmt.Printf("... (%d more)\n", len(rows)-40)
			break
		}
		fmt.Printf("%-60s %6d %6d %6d\n", trunc(r.id, 60), r.tests, r.docs, r.gaps)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
