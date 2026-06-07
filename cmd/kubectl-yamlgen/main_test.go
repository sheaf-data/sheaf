// Smoke tests for the pure --help parsing path. The end-to-end flow
// (exec.Command kubectl, walk subcommands recursively, emit YAML)
// needs either an exec mock or a fixture binary on $PATH; the
// parsers themselves are pure-string and tested directly here.
package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Synthetic kubectl-shaped --help body. Trimmed but covers the section
// shapes the parser cares about: a preamble with short/long, a Usage
// section, two subcommand sections under different headers ("Basic
// Commands (Beginner):" and "Other Commands:"), and an Options block
// with one bool flag + one string flag that has a default value.
const fakeKubectlHelp = `kubectl controls the Kubernetes cluster manager.

 Find more information at: https://kubernetes.io/docs/reference/kubectl/

Usage:
  kubectl [flags] [options]

Basic Commands (Beginner):
  create        Create a resource from a file or from stdin
  get           Display one or many resources

Other Commands:
  api-resources  Print the supported API resources on the server

Subcommands provided by plugins:
  plugin-foo     Something a user installed

Options:
    --kubeconfig='':
        Path to the kubeconfig file to use for CLI requests.

    -v, --v=0:
        Number for the log level verbosity.
`

func TestParseHelp_KubectlShape(t *testing.T) {
	doc := parseHelp(fakeKubectlHelp)

	if doc.short != "kubectl controls the Kubernetes cluster manager." {
		t.Errorf("short = %q; want kubectl tagline", doc.short)
	}
	if !strings.Contains(doc.long, "Find more information") {
		t.Errorf("long missing the long-body URL line: %q", doc.long)
	}
	if doc.usage != "kubectl [flags] [options]" {
		t.Errorf("usage = %q; want kubectl [flags] [options]", doc.usage)
	}

	// Subcommands: collected from "Basic Commands (Beginner)" + "Other
	// Commands"; "Subcommands provided by plugins" is skipped (not core
	// surface). Compare sorted because parseHelp doesn't guarantee
	// cross-section ordering and the smoke test cares about the *set*.
	wantSubs := []string{"api-resources", "create", "get"}
	gotSubs := append([]string(nil), doc.subcommands...)
	sort.Strings(gotSubs)
	if !reflect.DeepEqual(gotSubs, wantSubs) {
		t.Errorf("subcommands = %#v; want %#v (sorted)", gotSubs, wantSubs)
	}

	// Options: two flags. --kubeconfig has no short, empty default;
	// --v has short -v and default "0".
	if len(doc.options) != 2 {
		t.Fatalf("options length = %d; want 2", len(doc.options))
	}
	if doc.options[0].Option != "kubeconfig" || doc.options[0].Shorthand != "" {
		t.Errorf("options[0] = %+v; want --kubeconfig no shorthand", doc.options[0])
	}
	if doc.options[1].Option != "v" || doc.options[1].Shorthand != "v" {
		t.Errorf("options[1] = %+v; want --v shorthand v", doc.options[1])
	}
	if doc.options[1].DefaultValue != "0" {
		t.Errorf("options[1].DefaultValue = %q; want 0", doc.options[1].DefaultValue)
	}
	if !strings.Contains(doc.options[1].Description, "log level verbosity") {
		t.Errorf("options[1].Description = %q; want substring 'log level verbosity'", doc.options[1].Description)
	}
}

func TestParseSubcommandList_StripsDescriptions(t *testing.T) {
	body := []string{
		"  create        Create a resource",
		"  get           Display resources",
		"",
		"  not-indented-enough", // single-space indent: ignored
		"create-no-description", // no indent + no two-space gap: ignored
	}

	got := parseSubcommandList(body)
	want := []string{"create", "get"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("subcommands = %#v; want %#v", got, want)
	}
}

func TestParseOptionsBlock_DefaultsAndShorthand(t *testing.T) {
	text := `Options:
    -A, --all-namespaces=false:
        If present, list across all namespaces.

    --token='':
        Bearer token for authentication.

    --chunk-size=500:
        Return large lists in chunks rather than all at once.
`

	opts := parseOptionsBlock(text)
	if len(opts) != 3 {
		t.Fatalf("options length = %d; want 3", len(opts))
	}

	// -A, --all-namespaces=false
	if opts[0].Option != "all-namespaces" || opts[0].Shorthand != "A" || opts[0].DefaultValue != "false" {
		t.Errorf("opts[0] = %+v; want --all-namespaces shorthand A default false", opts[0])
	}
	// --token=''
	if opts[1].Option != "token" || opts[1].Shorthand != "" || opts[1].DefaultValue != "" {
		t.Errorf("opts[1] = %+v; want --token no shorthand empty default", opts[1])
	}
	// --chunk-size=500
	if opts[2].Option != "chunk-size" || opts[2].DefaultValue != "500" {
		t.Errorf("opts[2] = %+v; want --chunk-size default 500", opts[2])
	}
}

// TestParseOptionsBlock_GhInlineFlags covers the gh / cobra-default
// inline flag form: an ALL-CAPS "FLAGS" header, single-line flags with
// a bare type placeholder (or none for a bool switch), a placeholder
// that contains brackets and slashes ("[HOST/]OWNER/REPO"), and an
// "INHERITED FLAGS" sub-section that must NOT be captured — per-command
// YAMLs hold local flags only, mirroring the kubectl path's
// skip-global-flags behavior.
func TestParseOptionsBlock_GhInlineFlags(t *testing.T) {
	text := `FLAGS
  -a, --assignee login           Assign people by their login. Use "@me" to self-assign.
  -w, --web                      Open the browser to create an issue
      --recover string           Recover input from a failed run of create
  -R, --repo [HOST/]OWNER/REPO   Select a repository

INHERITED FLAGS
      --help   Show help for command

EXAMPLES
  $ gh issue create --title "Bug"
`

	opts := parseOptionsBlock(text)
	if len(opts) != 4 {
		t.Fatalf("options length = %d; want 4 local flags (inherited + examples excluded)", len(opts))
	}

	// -a, --assignee login → string-valued, shorthand a, desc captured
	if opts[0].Option != "assignee" || opts[0].Shorthand != "a" || opts[0].ValueType != "string" {
		t.Errorf("opts[0] = %+v; want --assignee/a/string", opts[0])
	}
	if !strings.Contains(opts[0].Description, "Assign people by their login") {
		t.Errorf("opts[0].Description = %q; want assignee desc", opts[0].Description)
	}
	// -w, --web → bool switch (no placeholder), desc still captured
	if opts[1].Option != "web" || opts[1].Shorthand != "w" || opts[1].ValueType != "bool" {
		t.Errorf("opts[1] = %+v; want --web/w/bool", opts[1])
	}
	if !strings.Contains(opts[1].Description, "Open the browser") {
		t.Errorf("opts[1].Description = %q; want web desc", opts[1].Description)
	}
	// --recover string → no shorthand, string-valued
	if opts[2].Option != "recover" || opts[2].Shorthand != "" || opts[2].ValueType != "string" {
		t.Errorf("opts[2] = %+v; want --recover/none/string", opts[2])
	}
	// -R, --repo [HOST/]OWNER/REPO → placeholder with brackets/slashes
	if opts[3].Option != "repo" || opts[3].Shorthand != "R" || opts[3].ValueType != "string" {
		t.Errorf("opts[3] = %+v; want --repo/R/string", opts[3])
	}

	// The INHERITED FLAGS block must be excluded entirely.
	for _, o := range opts {
		if o.Option == "help" {
			t.Errorf("captured inherited flag --help; want local flags only")
		}
	}
}
