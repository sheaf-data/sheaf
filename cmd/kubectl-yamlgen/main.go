// kubectl-yamlgen — introspect a kubectl binary via its own --help
// output and emit per-subcommand YAML files in the schema the cobra
// adapter expects (the schema docker/cli's `make yamldocs` produces).
//
// The kubectl source tree lives inside kubernetes/kubernetes (~700MB)
// and doesn't ship a standalone YAML generator the way docker/cli
// does. This tool sidesteps that: it walks `kubectl <path> --help`
// recursively, parses the textual help format, and writes
// kubectl_<sub>_<sub>.yaml files into an output directory. The
// existing cobra adapter then consumes that directory unchanged.
//
// Usage:
//
//	kubectl-yamlgen --binary kubectl --out docs/examples/kubectl-yaml
//
// `kubectl options` is consulted once and copied verbatim into every
// command's inherited_options block (kubectl's per-subcommand help
// omits global flags). Local Options: blocks on each command go into
// `options`.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type option struct {
	Option       string `yaml:"option"`
	Shorthand    string `yaml:"shorthand,omitempty"`
	ValueType    string `yaml:"value_type"`
	DefaultValue string `yaml:"default_value,omitempty"`
	Description  string `yaml:"description"`
}

type commandDoc struct {
	Command          string   `yaml:"command"`
	Short            string   `yaml:"short,omitempty"`
	Long             string   `yaml:"long,omitempty"`
	Usage            string   `yaml:"usage,omitempty"`
	Pname            string   `yaml:"pname,omitempty"`
	Plink            string   `yaml:"plink,omitempty"`
	Options          []option `yaml:"options,omitempty"`
	InheritedOptions []option `yaml:"inherited_options,omitempty"`
}

func main() {
	var (
		binary  string
		out     string
		verbose bool
	)
	flag.StringVar(&binary, "binary", "kubectl", "kubectl binary to introspect (path or name on $PATH)")
	flag.StringVar(&out, "out", "kubectl-yaml", "output directory for YAML files (created if missing)")
	flag.BoolVar(&verbose, "v", false, "log each command walked")
	flag.Parse()

	if err := os.MkdirAll(out, 0o755); err != nil {
		die("mkdir %s: %v", out, err)
	}

	binName := filepath.Base(binary)

	globals := parseGlobals(binary, verbose)

	var emitted int
	walk(binary, binName, nil, out, globals, &emitted, verbose)
	fmt.Fprintf(os.Stderr, "wrote %d YAML files to %s\n", emitted, out)
}

func walk(binary, binName string, path []string, outDir string, globals []option, emitted *int, verbose bool) {
	args := append(append([]string(nil), path...), "--help")
	if verbose {
		fmt.Fprintf(os.Stderr, "  %s %s\n", binary, strings.Join(args, " "))
	}
	out, err := runHelp(binary, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skip %s %s: %v\n", binary, strings.Join(args, " "), err)
		return
	}

	parsed := parseHelp(out)

	cmdPath := append([]string{binName}, path...)
	doc := commandDoc{
		Command:          strings.Join(cmdPath, " "),
		Short:            parsed.short,
		Long:             parsed.long,
		Usage:            parsed.usage,
		Options:          parsed.options,
		InheritedOptions: globals,
	}
	if len(cmdPath) > 1 {
		doc.Pname = strings.Join(cmdPath[:len(cmdPath)-1], " ")
		doc.Plink = filenameFor(cmdPath[:len(cmdPath)-1]) + ".yaml"
	}

	writeYAML(outDir, cmdPath, doc)
	*emitted++

	for _, sub := range parsed.subcommands {
		walk(binary, binName, append(append([]string(nil), path...), sub), outDir, globals, emitted, verbose)
	}
}

func parseGlobals(binary string, verbose bool) []option {
	if verbose {
		fmt.Fprintf(os.Stderr, "  %s options\n", binary)
	}
	out, err := runHelp(binary, []string{"options"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: %s options: %v (skipping global flags)\n", binary, err)
		return nil
	}
	// `kubectl options` opens with a preamble sentence ("The following
	// options can be passed to any command:") that looks like a
	// section header. Drop everything before the first indented `-`
	// line, then wrap in a synthetic "Options:" header so
	// parseOptionsBlock can chew on it uniformly.
	lines := strings.Split(out, "\n")
	start := 0
	for i, ln := range lines {
		if optionRE.MatchString(ln) {
			start = i
			break
		}
	}
	return parseOptionsBlock("Options:\n" + strings.Join(lines[start:], "\n"))
}

type helpDoc struct {
	short       string
	long        string
	usage       string
	subcommands []string
	options     []option
}

// Section header lines: no leading whitespace, capital-letter start.
// Two shapes are recognized:
//   - cobra default: title-case ending in a colon — "Available
//     Commands:", "Options:", "Examples:", "Usage:".
//   - gh-style custom help: ALL-CAPS with no colon — "CORE COMMANDS",
//     "GITHUB ACTIONS COMMANDS", "FLAGS", "USAGE".
var sectionRE = regexp.MustCompile(`^([A-Z][A-Za-z0-9 (),/&'-]*:|[A-Z][A-Z ]{2,})\s*$`)

func parseHelp(text string) helpDoc {
	lines := strings.Split(text, "\n")

	// First pass: chunk into sections. Lines before the first header
	// are the preamble (short + long). Then a map from header → body
	// (lines without the header).
	type section struct {
		header string
		body   []string
	}
	var preamble []string
	var sections []section
	cur := -1
	for _, ln := range lines {
		if sectionRE.MatchString(ln) {
			sections = append(sections, section{header: strings.TrimSuffix(strings.TrimSpace(ln), ":")})
			cur = len(sections) - 1
			continue
		}
		if cur < 0 {
			preamble = append(preamble, ln)
		} else {
			sections[cur].body = append(sections[cur].body, ln)
		}
	}

	doc := helpDoc{}
	doc.short, doc.long = splitShortLong(preamble)

	for _, s := range sections {
		body := strings.Join(s.body, "\n")
		switch {
		case s.header == "Usage":
			doc.usage = firstNonEmptyLine(s.body)
		case s.header == "Options" || strings.EqualFold(s.header, "Flags"):
			doc.options = parseOptionsBlock("Options:\n" + body)
		case strings.Contains(strings.ToLower(s.header), "command"):
			// "Available Commands", "Basic Commands (Beginner)",
			// "Deploy Commands", "Other Commands", and gh's all-caps
			// "CORE COMMANDS" / "GITHUB ACTIONS COMMANDS" etc. all list
			// subcommands (matched case-insensitively).
			if s.header == "Subcommands provided by plugins" {
				continue // user-installed; not part of the core surface
			}
			doc.subcommands = append(doc.subcommands, parseSubcommandList(s.body)...)
		}
	}

	// Subcommands may be listed under multiple headers (kubectl
	// top-level help splits into Basic / Deploy / Cluster Mgmt /
	// etc.). Dedup while preserving order.
	doc.subcommands = dedupStable(doc.subcommands)
	return doc
}

func splitShortLong(preamble []string) (string, string) {
	// Drop leading/trailing blank lines.
	for len(preamble) > 0 && strings.TrimSpace(preamble[0]) == "" {
		preamble = preamble[1:]
	}
	for len(preamble) > 0 && strings.TrimSpace(preamble[len(preamble)-1]) == "" {
		preamble = preamble[:len(preamble)-1]
	}
	if len(preamble) == 0 {
		return "", ""
	}
	short := strings.TrimSpace(preamble[0])
	if len(preamble) == 1 {
		return short, ""
	}
	// Everything after the first line (and a blank) is the long
	// description. Kubectl indents long-body lines by one space; trim
	// the uniform leading space for readability.
	var longLines []string
	for _, ln := range preamble[1:] {
		longLines = append(longLines, strings.TrimPrefix(ln, " "))
	}
	long := strings.TrimSpace(strings.Join(longLines, "\n"))
	return short, long
}

// Subcommand list entries: two-space indent, then NAME, an optional
// trailing colon (gh prints "auth:   desc"; cobra default prints
// "apply   desc"), then a run of spaces, then the description.
// Subcommand names use [a-z0-9-].
var subcommandRE = regexp.MustCompile(`^\s{2,}([a-z0-9][a-z0-9-]*):?\s{2,}\S`)

func parseSubcommandList(body []string) []string {
	var subs []string
	for _, ln := range body {
		m := subcommandRE.FindStringSubmatch(ln)
		if m != nil {
			subs = append(subs, m[1])
		}
	}
	return subs
}

// Option line: indent, optional "-S, ", then "--name", then "=DEFAULT"
// (the default value, quoted or bare), then a trailing colon.
// Examples:
//
//	"    -A, --all-namespaces=false:"
//	"    --as=''":
//	"    --as-group=[]:"
//	"    --chunk-size=500:"
//	"        --token='':"
var optionRE = regexp.MustCompile(`^\s+(?:-([A-Za-z0-9]),\s+)?--([A-Za-z0-9][A-Za-z0-9-]*)(?:=(.*))?:\s*$`)

// inlineOptionRE matches the single-line flag form used by gh and by
// cobra's default help (helm, etc.): an indented flag, an OPTIONAL bare
// type placeholder (no "="), then two-or-more spaces and the
// description on the SAME line. No trailing colon. The placeholder is a
// non-space run so it tolerates gh's "[HOST/]OWNER/REPO"; a missing
// placeholder (the 2-space gap follows the flag directly) means a bool
// switch, e.g. "-w, --web    Open the browser".
//
//	"  -a, --assignee login   Assign people by their login"
//	"  -F, --body-file file   Read body text from file"
//	"      --recover string   Recover input from a failed run"
//	"  -w, --web              Open the browser"
var inlineOptionRE = regexp.MustCompile(`^\s+(?:-([A-Za-z0-9]),\s+)?--([A-Za-z0-9][A-Za-z0-9-]*)(?:\s+(\S+))?\s{2,}(\S.*)$`)

// isOptionsHeader reports whether ln opens a flags/options block. It
// accepts cobra's default "Options:" / "Flags:" / "Global Flags:" and
// the ALL-CAPS custom headers gh-style CLIs print ("FLAGS",
// "INHERITED FLAGS"). Matching is case-insensitive and tolerant of a
// trailing colon and surrounding whitespace.
func isOptionsHeader(ln string) bool {
	s := strings.TrimSpace(ln)
	s = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(s, ":")))
	switch s {
	case "options", "inherited options", "global options", "general options",
		"flags", "inherited flags", "global flags", "local flags", "additional flags":
		return true
	}
	return false
}

// inferInlineType maps a gh/cobra inline arg placeholder (the bare word
// after the flag, e.g. "string", "int", "login", "file") to a
// (value_type, default_value). gh prints no placeholder for boolean
// switches, so an empty placeholder is treated as a bool.
func inferInlineType(placeholder string) (string, string) {
	p := strings.ToLower(strings.TrimSpace(placeholder))
	if p == "" {
		return "bool", "false"
	}
	switch p {
	case "int", "number", "count", "uint", "n":
		return "int", ""
	case "duration":
		return "duration", ""
	case "strings", "stringarray", "values", "list":
		return "stringSlice", ""
	}
	return "string", ""
}

func parseOptionsBlock(text string) []option {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var (
		opts    []option
		cur     *option
		descBuf []string
	)
	flush := func() {
		if cur == nil {
			return
		}
		cur.Description = strings.TrimSpace(strings.Join(descBuf, " "))
		opts = append(opts, *cur)
		cur = nil
		descBuf = nil
	}
	inBlock := false
	for scanner.Scan() {
		ln := scanner.Text()
		if !inBlock {
			if isOptionsHeader(ln) {
				inBlock = true
			}
			continue
		}
		if sectionRE.MatchString(ln) {
			// Next section — stop.
			flush()
			break
		}
		m := optionRE.FindStringSubmatch(ln)
		if m != nil {
			flush()
			cur = &option{
				Option:    m[2],
				Shorthand: m[1],
			}
			cur.ValueType, cur.DefaultValue = inferType(m[3])
			continue
		}
		// Inline (gh / cobra-default) form: flag, optional bare type
		// placeholder, then the description on the SAME line. Only tried
		// after the two-line colon form above fails, so the kubectl path
		// is untouched.
		if im := inlineOptionRE.FindStringSubmatch(ln); im != nil {
			flush()
			cur = &option{
				Option:    im[2],
				Shorthand: im[1],
			}
			cur.ValueType, cur.DefaultValue = inferInlineType(im[3])
			if d := strings.TrimSpace(im[4]); d != "" {
				descBuf = append(descBuf, d)
			}
			continue
		}
		if cur != nil {
			s := strings.TrimSpace(ln)
			if s != "" {
				descBuf = append(descBuf, s)
			}
		}
	}
	flush()
	return opts
}

// inferType maps a kubectl default-value literal to (value_type,
// default_value). kubectl prints defaults like:
//
//	"false"           → bool, "false"
//	"true"            → bool, "true"
//	"''"              → string, ""
//	"'something'"     → string, "something"
//	"[]"              → stringSlice, ""
//	"500"             → int, "500"
//	"500ms"           → duration, "500ms"
//
// Anything unrecognized falls through as string with the literal default.
func inferType(raw string) (string, string) {
	switch raw {
	case "false", "true":
		return "bool", raw
	case "[]":
		return "stringSlice", ""
	case "":
		// No "=…" was present; treat as a bare switch.
		return "bool", "false"
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return "string", raw[1 : len(raw)-1]
	}
	// Numeric (with optional duration suffix).
	if isNumericLiteral(raw) {
		if isDuration(raw) {
			return "duration", raw
		}
		return "int", raw
	}
	return "string", raw
}

func isNumericLiteral(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != '-' && r != 'm' && r != 's' && r != 'h' && r != 'u' && r != 'n' && r != 'µ' {
			return false
		}
	}
	return true
}

func isDuration(s string) bool {
	if s == "" {
		return false
	}
	last := s[len(s)-1]
	return last == 's' || last == 'm' || last == 'h'
}

func runHelp(binary string, args []string) (string, error) {
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG=/dev/null")
	b, err := cmd.CombinedOutput()
	if err != nil {
		// kubectl returns nonzero from --help in some paths; if we got
		// usable output, treat it as success.
		if len(b) > 0 && strings.Contains(string(b), ":") {
			return string(b), nil
		}
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

func firstNonEmptyLine(lines []string) string {
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if s != "" {
			return s
		}
	}
	return ""
}

func dedupStable(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func filenameFor(path []string) string {
	return strings.Join(path, "_")
}

func writeYAML(outDir string, cmdPath []string, doc commandDoc) {
	name := filenameFor(cmdPath) + ".yaml"
	abs := filepath.Join(outDir, name)
	b, err := yaml.Marshal(doc)
	if err != nil {
		die("marshal %s: %v", name, err)
	}
	if err := os.WriteFile(abs, b, 0o644); err != nil {
		die("write %s: %v", abs, err)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
