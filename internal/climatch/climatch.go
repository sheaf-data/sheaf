// Package climatch extracts CLI command invocations — `<binary>
// <subcommand>…` paths — from shell / terminal text such as fenced code
// blocks. It is the CLI analogue of internal/fidlmatch and
// internal/protomatch: a single, shared definition of "what command does
// this line invoke" that any adapter can call, instead of each rolling its
// own parser.
//
// Consumed by:
//   - the workflows rendered-reference adapter (recipe fences -> ordered
//     WORKFLOW refs), and
//   - the markdown doc parser (shell/terminal example fences -> EXAMPLE
//     refs attributed to command elements).
package climatch

import (
	"regexp"
	"strings"
)

// maxSubcommandDepth caps how many subcommand tokens past the binary we
// treat as part of the command path. 3 covers the deepest legitimate CLI
// paths in practice (e.g. "kubectl create secret tls",
// "ffx debug crash report"). The cap is the main defense against treating
// positional arguments / resource names as subcommand tokens.
const maxSubcommandDepth = 3

// subcommandTokenRx matches a bare subcommand name (lowercase letter,
// then letters/digits/hyphens). Excludes flags, placeholders, paths,
// numbers, and UpperCase identifiers.
var subcommandTokenRx = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// flagNameRx matches a long-flag name (the text after `--`): lowercase
// letter, then letters/digits/hyphens. Excludes UpperCase / placeholder
// shapes so positional or value tokens don't masquerade as flags.
var flagNameRx = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// InvocationRef parses one shell/terminal line for a `<binary> <subcmd>…`
// invocation and returns the canonical command path, or "" if the line
// doesn't invoke binary.
//
//	$ kubectl get pods             → "kubectl get pods"
//	  kubectl apply -f deploy.yaml → "kubectl apply"
//	cat x | ffx audio play         → "ffx audio play"
//
// It strips a leading shell prompt, finds the binary token anywhere on the
// line (so pipes / env-prefixes don't hide it), then collects following
// subcommand-shaped tokens until the first flag (`-…`), placeholder
// (`<` `[` `"` `'`), shell connector (`|` `&` `;` `$`), non-subcommand
// token, or the depth cap.
func InvocationRef(line, binary string) string {
	line = stripShellNoise(line)
	tokens := strings.Fields(line)
	idx := -1
	for i, t := range tokens {
		if t == binary {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	parts := []string{binary}
	depth := 0
	for i := idx + 1; i < len(tokens) && depth < maxSubcommandDepth; i++ {
		t := tokens[i]
		if t == "" {
			continue
		}
		if t[0] == '-' || t[0] == '<' || t[0] == '[' || t[0] == '"' || t[0] == '\'' || t[0] == '|' || t[0] == '&' || t[0] == ';' || t[0] == '$' {
			break
		}
		if !subcommandTokenRx.MatchString(t) {
			break
		}
		parts = append(parts, t)
		depth++
	}
	return strings.Join(parts, " ")
}

// Prefixes returns every minDepth-or-deeper token prefix of a
// space-joined command path, so a longest-prefix join can attribute to the
// deepest command element that actually exists. With minDepth=2,
// "ffx audio gen" → ["ffx audio", "ffx audio gen"]. Returns nil when the
// path has fewer than minDepth tokens.
func Prefixes(commandPath string, minDepth int) []string {
	if minDepth < 1 {
		minDepth = 1
	}
	parts := strings.Fields(commandPath)
	if len(parts) < minDepth {
		return nil
	}
	out := make([]string, 0, len(parts)-minDepth+1)
	for d := minDepth; d <= len(parts); d++ {
		out = append(out, strings.Join(parts[:d], " "))
	}
	return out
}

// FlagRefs extracts long flags from a shell line and joins each to the
// given command path, so example invocations credit flag elements.
//
//	("ffx audio gen sine --duration 5ms --frequency 440", "ffx",
//	 "ffx audio gen sine") -> ["ffx audio gen sine --duration",
//	                           "ffx audio gen sine --frequency"]
//
// It strips shell noise, finds the binary token, then for every token
// after it that begins with "--" (length > 2) takes the substring after
// the dashes, cuts anything from the first "=" onward (so "--flag=value"
// credits "--flag"), and accepts it only if it is a lowercase flag name
// (`^[a-z][a-z0-9-]*$`). Bare "--" and short flags ("-x") are skipped.
// Refs are emitted only at commandPath (the most-specific prefix), never
// at shallower depths — that avoids crediting a parent command's
// same-named flag. Order is preserved and duplicates dropped. Returns
// nil when commandPath is empty or the line carries no long flags.
func FlagRefs(line, binary, commandPath string) []string {
	if commandPath == "" {
		return nil
	}
	line = stripShellNoise(line)
	tokens := strings.Fields(line)
	idx := -1
	for i, t := range tokens {
		if t == binary {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	var refs []string
	seen := map[string]bool{}
	for i := idx + 1; i < len(tokens); i++ {
		t := tokens[i]
		if len(t) <= 2 || !strings.HasPrefix(t, "--") {
			continue // bare "--", short flags ("-x"), and non-flags
		}
		name := t[2:]
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		if !flagNameRx.MatchString(name) {
			continue
		}
		ref := commandPath + " --" + name
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}

// stripShellNoise trims a leading prompt (`$ `, `# `, `> `, `% `, `$`) and
// surrounding whitespace so the parser sees a clean command line.
func stripShellNoise(s string) string {
	s = strings.TrimLeft(s, " \t")
	for _, prefix := range []string{"$ ", "# ", "> ", "% ", "$"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			s = strings.TrimLeft(s, " \t")
			break
		}
	}
	return s
}
