// Package ffxinvoke canonicalizes the ordered literal argument list of a
// single ffx subprocess invocation into command + flag ContractRefs.
//
// It is the shared core behind the ffx-anchored subprocess-invocation
// extractors in the rust-test (.ffx([…]) / Command::new(ffx).args([…]))
// and python-test (Honeydew FFX.run(["target","list",…])) adapters. Both
// adapters do their own *language-specific* anchoring + literal slicing —
// "is this call an ffx invocation, and what are its literal arg tokens" —
// then hand the resulting []string here to get refs whose form equals the
// cobra element-ID exactly (`ffx target list`, `ffx target list --format`),
// so the indexer's Strategy-1 direct-ref match lands.
//
// The canonicalization itself routes through internal/climatch — the same
// shared "what command does this invoke" parser the markdown and workflows
// adapters use — after stripping ffx's leading global flags so the command
// path resolves past them.
//
// Scope tag: Generic with respect to the args→refs mechanism, but the
// global-flag tables and the leading-binary string ("ffx ") are
// ffx-provider-specific. Keeping them here (rather than in each adapter)
// means the two adapters agree on ffx's globals by construction.
package ffxinvoke

import (
	"strings"

	"github.com/sheaf-data/sheaf/internal/climatch"
)

// Binary is the ffx binary token the canonical command line is anchored on.
const Binary = "ffx"

// GlobalStringFlags are ffx's global / persistent string-valued options —
// the ones that may appear BEFORE the subcommand on a real invocation
// (`ffx --target X target list`, `ffx --machine json daemon socket`) and
// that consume the following token as their value. They come from the ffx
// root command's options[] in the synthesized cobra schema.
// climatch.InvocationRef stops at the first `--flag`, so without stripping
// these the whole command path collapses to bare "ffx" and the per-command
// flags after the subcommand are lost.
var GlobalStringFlags = map[string]bool{
	"config":      true,
	"env":         true,
	"machine":     true,
	"stamp":       true,
	"target":      true,
	"timeout":     true,
	"log-level":   true,
	"isolate-dir": true,
	"log-output":  true,
}

// GlobalBoolFlags are the bool-valued globals (no value token follows).
var GlobalBoolFlags = map[string]bool{
	"schema":         true,
	"verbose":        true,
	"no-environment": true,
	"strict":         true,
}

// Stats accumulates what canonicalization could not turn into a command
// path, so a caller can log it instead of silently truncating.
type Stats struct {
	// FullyDynamic counts invocations whose literal args held no literal
	// subcommand token at all (every meaningful arg was a variable /
	// f-string / call). These produce no command path.
	FullyDynamic int
}

// Add folds another Stats into the receiver.
func (s *Stats) Add(o Stats) {
	s.FullyDynamic += o.FullyDynamic
}

// Canonicalize turns the ordered literal args of one ffx invocation into
// command + flag ContractRefs via internal/climatch, after stripping ffx's
// leading global flags so the command path resolves past them.
//
// The args are the subcommand-and-onward tokens of the invocation WITHOUT
// the leading "ffx" binary token (which is how both the rust harness arrays
// and the Honeydew cmd lists carry them). Empty/blank literals are dropped
// (e.g. from a placeholder); a dynamic token already elided by the caller's
// slicer simply isn't present.
//
//	["target","list","--format","json"]
//	  -> cmd refs:  ffx target, ffx target list
//	     flag refs: ffx target list --format
//	["--target","[::1]:8022","target","list","--format","a","--no-probe"]
//	  -> cmd refs:  ffx target, ffx target list
//	     flag refs: ffx target list --format, ffx target list --no-probe,
//	                ffx --target          (global flag, credited at root)
//
// A fully-dynamic invocation (no literal subcommand token) yields no command
// path; it is counted in stats rather than emitting a bare "ffx".
func Canonicalize(literalArgs []string) (cmdRefs, flagRefs []string, stats Stats) {
	// Drop empty literals.
	args := make([]string, 0, len(literalArgs))
	for _, a := range literalArgs {
		if strings.TrimSpace(a) != "" {
			args = append(args, a)
		}
	}
	if len(args) == 0 {
		stats.FullyDynamic++
		return nil, nil, stats
	}

	// Separate leading ffx global flags (and their values) from the rest,
	// remembering which globals were named so we can credit them at the
	// `ffx` root. Stop at the first non-global token — that's where the
	// subcommand path begins.
	var leadingGlobals []string // e.g. ["--machine","--target"]
	rest := args
stripLoop:
	for len(rest) > 0 {
		name, isLong := longFlagName(rest[0])
		if !isLong {
			break // first positional/subcommand token — globals end here
		}
		switch {
		case GlobalBoolFlags[name]:
			leadingGlobals = append(leadingGlobals, "--"+name)
			rest = rest[1:]
		case GlobalStringFlags[name]:
			leadingGlobals = append(leadingGlobals, "--"+name)
			rest = rest[1:]
			// Consume the value token if present and it isn't itself a flag.
			if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
				rest = rest[1:]
			}
		default:
			// A long flag that isn't a known global appears before any
			// subcommand — unusual; leave it in place and stop stripping so
			// we don't accidentally swallow a subcommand-scoped flag.
			break stripLoop
		}
	}

	// Build the canonical command line and run it through climatch exactly
	// as the markdown / workflows adapters do.
	line := Binary + " " + strings.Join(rest, " ")
	cmd := climatch.InvocationRef(line, Binary)
	if cmd == "" || cmd == Binary {
		// No subcommand resolved (everything after the globals was dynamic
		// or flag-only). Don't emit a bare "ffx" ref. Only count it as a
		// fully-dynamic skip when there were no globals to credit either.
		if len(leadingGlobals) == 0 {
			stats.FullyDynamic++
		}
		flagRefs = append(flagRefs, creditGlobals(leadingGlobals)...)
		return nil, flagRefs, stats
	}
	cmdRefs = climatch.Prefixes(cmd, 2)
	flagRefs = climatch.FlagRefs(line, Binary, cmd)
	// Credit leading global flags at the ffx root (e.g. "ffx --machine").
	flagRefs = append(flagRefs, creditGlobals(leadingGlobals)...)
	return cmdRefs, flagRefs, stats
}

// creditGlobals maps stripped leading global flags to their root element
// refs ("ffx --machine"). Deduplicated, order preserved.
func creditGlobals(globals []string) []string {
	if len(globals) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range globals {
		ref := Binary + " " + g
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// longFlagName returns the lowercase long-flag name for a `--name` /
// `--name=value` token and whether it is a well-formed long flag. Short
// flags (`-t`), bare `--`, and non-flags return ("", false). Mirrors the
// flag-name shape climatch accepts so the two agree on what a flag is.
func longFlagName(tok string) (string, bool) {
	if len(tok) <= 2 || !strings.HasPrefix(tok, "--") {
		return "", false
	}
	name := tok[2:]
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	if name == "" {
		return "", false
	}
	if !(name[0] >= 'a' && name[0] <= 'z') {
		return "", false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return "", false
		}
	}
	return name, true
}
