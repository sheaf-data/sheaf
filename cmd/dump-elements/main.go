// dump-elements is a debug utility that runs the FIDL adapter directly
// against a repo and prints a per-kind summary plus a sample of each.
// Used to validate adapter behavior against real Fuchsia source.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/sheaf-data/sheaf/internal/adapters"
	"github.com/sheaf-data/sheaf/internal/adapters/fidl"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

func main() {
	repo := flag.String("repo", ".", "repo root")
	libPattern := flag.String("library", "fuchsia.io", "library or pattern (e.g. fuchsia.driver.*)")
	include := flag.String("include", "sdk/fidl/**/*.fidl", "include glob")
	flag.Parse()

	a := fidl.New(fidl.Config{Include: []string{*include}})
	scope := adapters.ScopeConfig{Libraries: []string{*libPattern}}
	elems, claims, err := a.DiscoverWithDocs(context.Background(), *repo, scope)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Per-kind counts.
	byKind := make(map[contractpb.ContractElementKind]int)
	byLib := make(map[string]int)
	relCounts := make(map[contractpb.RelationshipKind]int)
	var docCovered, docSubstantive int
	for _, e := range elems {
		byKind[e.GetKind()]++
		byLib[e.GetLibrary()]++
		for _, r := range e.GetRelationships() {
			relCounts[r.GetKind()]++
		}
		if e.GetDocCommentExcerpt() != "" {
			docCovered++
		}
	}
	for _, c := range claims {
		if c.GetSubstance() >= 3 { // PARTIAL or SUBSTANTIVE
			docSubstantive++
		}
	}

	fmt.Printf("library pattern:    %s\n", *libPattern)
	fmt.Printf("total elements:     %d\n", len(elems))
	fmt.Printf("total doc claims:   %d\n", len(claims))
	fmt.Printf("documented elems:   %d\n", docCovered)
	fmt.Printf("PARTIAL+ docs:      %d\n", docSubstantive)
	fmt.Println()
	fmt.Println("By kind:")
	for k, n := range byKind {
		fmt.Printf("  %-12s %d\n", k.String(), n)
	}
	fmt.Println()
	fmt.Println("By library:")
	libs := make([]string, 0, len(byLib))
	for l := range byLib {
		libs = append(libs, l)
	}
	sort.Strings(libs)
	for _, l := range libs {
		fmt.Printf("  %-50s %d\n", l, byLib[l])
	}
	fmt.Println()
	fmt.Println("Relationship kinds:")
	for k, n := range relCounts {
		fmt.Printf("  %-20s %d\n", k.String(), n)
	}

	// Sample protocols with their composes
	fmt.Println()
	fmt.Println("Protocols and their composes (sample):")
	count := 0
	for _, e := range elems {
		if e.GetKind() != contractpb.ContractElementKind_PROTOCOL {
			continue
		}
		fmt.Printf("  %s\n", e.GetId())
		for _, r := range e.GetRelationships() {
			if r.GetKind() == contractpb.RelationshipKind_COMPOSED_FROM {
				fmt.Printf("    composes %s\n", r.GetTargetElementId())
			}
		}
		count++
		if count >= 10 {
			break
		}
	}
}
