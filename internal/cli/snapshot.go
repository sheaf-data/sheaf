package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sheaf-data/sheaf/utils/scanner"
)

// Snapshot runs `sheaf snapshot` — it scans the project in-process (no
// server) and emits the library Snapshot JSON: the exact data the report
// builder consumes and the MCP server's library_snapshot op serves. Pipe it
// into `scanner --from-snapshot` to render a report offline, diff it across
// commits to see contract movement, or feed it to another tool.
func Snapshot(args []string) int {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath, repoPath  string
		library, libraryLabel string
		output                string
	)
	fs.StringVar(&configPath, "config", "", "Path to sheaf.textproto (default: <repo>/sheaf.textproto)")
	fs.StringVar(&repoPath, "repo", ".", "Project repo root")
	fs.StringVar(&library, "library", "", "Library to snapshot. Accepts a comma-separated list to roll several libraries into one snapshot.")
	fs.StringVar(&libraryLabel, "library-label", "", "Label for a multi-library snapshot (default: comma-joined names). Ignored for a single library.")
	fs.StringVar(&output, "out", "", "Write the snapshot JSON here (default: stdout).")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return runSnapshot(os.Stdout, os.Stderr, configPath, repoPath, library, libraryLabel, output)
}

func runSnapshot(stdout, stderr io.Writer, configPath, repoPath, library, libraryLabel, output string) int {
	if library == "" {
		fmt.Fprintln(stderr, "sheaf snapshot: --library is required")
		return 2
	}
	cfgPath, _, err := resolveConfigPaths(configPath, repoPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}
	snap, err := scanner.BuildSnapshot(context.Background(), cfgPath, repoPath, library, libraryLabel, "")
	if err != nil {
		fmt.Fprintf(stderr, "sheaf snapshot: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "sheaf snapshot: marshal: %v\n", err)
		return 1
	}
	if output == "" {
		if _, err := stdout.Write(append(data, '\n')); err != nil {
			fmt.Fprintf(stderr, "sheaf snapshot: write: %v\n", err)
			return 1
		}
		return 0
	}
	if dir := filepath.Dir(output); dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			fmt.Fprintf(stderr, "sheaf snapshot: create output dir %s: %v\n", dir, mkErr)
			return 1
		}
	}
	if err := os.WriteFile(output, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "sheaf snapshot: write %s: %v\n", output, err)
		return 1
	}
	fmt.Fprintf(stderr, "sheaf snapshot: wrote %s (%d elements, %d profiles, %d findings, %d bytes)\n",
		output, len(snap.Elements), len(snap.Profiles), len(snap.Findings), len(data))
	return 0
}
