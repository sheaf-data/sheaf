// sheaf — the main binary. Subcommands route to internal/cli.
package main

import (
	"fmt"
	"os"

	"github.com/sheaf-data/sheaf/internal/cli"
)

const (
	usageHeader = `sheaf — contract-coverage analysis

Usage:
  sheaf <command> [options]

Commands:
  scan       Run the scan pipeline against a project and print a summary.
  gaps       List findings (filterable by kind / library / severity).
  coverage   Print the CoverageProfile for one element.
  report     Bulk dump all coverage profiles as CSV or JSON.
  snapshot   Emit a library's Snapshot JSON (in-process; pipe to sheaf render --from-snapshot).
  render     Render an HTML report from a saved Snapshot JSON (in-process; no server).
  verify     Adversarially check a snapshot's top-line numbers (vs the join + disk) before a report is shown.
  serve      Start the MCP server.
  review     Render a PR review comment from base + head corpora.
  review-html Render comment.html from a previously-emitted delta.json.
  init       Scaffold a starter sheaf.textproto + source map.
  doctor     Diagnose configuration and adapter health.
  version    Print version information.

Run 'sheaf <command> --help' for command-specific options.
`
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageHeader)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "scan":
		os.Exit(cli.Scan(args))
	case "doctor":
		os.Exit(cli.Doctor(args))
	case "version":
		os.Exit(cli.Version(args))
	case "gaps":
		os.Exit(cli.Gaps(args))
	case "coverage":
		os.Exit(cli.Coverage(args))
	case "report":
		os.Exit(cli.Report(args))
	case "snapshot":
		os.Exit(cli.Snapshot(args))
	case "render":
		os.Exit(cli.Render(args))
	case "verify":
		os.Exit(cli.Verify(args))
	case "serve":
		os.Exit(cli.Serve(args))
	case "review":
		os.Exit(cli.Review(args))
	case "review-html":
		os.Exit(cli.ReviewHTML(args))
	case "init":
		os.Exit(cli.Init(args))
	case "-h", "--help", "help":
		fmt.Print(usageHeader)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "sheaf: unknown command %q\n\n%s", cmd, usageHeader)
		os.Exit(2)
	}
}
