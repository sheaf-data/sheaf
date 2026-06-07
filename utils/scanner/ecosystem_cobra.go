package scanner

import "strings"

// cobraView is the EcosystemView for CLI-shaped surfaces (docker,
// helm, gh, kubectl, sheaf itself, …). The contract surface is a
// tree of subcommands; leaves are the user-callable commands, with
// flags and switches hanging off each command.
//
// The view is registered under ecosystem id "cli" — the shape the
// report renders, not the specific input adapter that fed it. Cobra
// YAML is the canonical input format (per the cobra contract anchor),
// but a future argh-derived adapter that emits the same SUBCOMMAND /
// FLAG element kinds would share this same view.
//
// Tier mapping:
//   - "Commands" (SUBCOMMAND) is the primary detail tier — every
//     subcommand path ("docker", "docker container", "docker container
//     run") is one element. Substance grading + the worklist run on
//     this tier.
//   - "Flags" (FLAG / SWITCH / POSITIONAL) is the modifier tier —
//     these belong to a single command and are part of that command's
//     coverage receipts, but are also counted in the header for total
//     surface visibility.
type cobraView struct{}

func (cobraView) ID() string { return "cli" }

func (cobraView) Tiers() []TierSpec {
	return []TierSpec{
		{ID: "primary", Label: "Commands", Kinds: []string{"SUBCOMMAND"}, ShowInHeader: true},
		{ID: "modifier", Label: "Flags",
			Kinds:        []string{"FLAG", "SWITCH", "POSITIONAL"},
			ShowInHeader: true},
	}
}

func (cobraView) PrimaryDetailKinds() []string { return []string{"SUBCOMMAND"} }

// ContainerOf returns the parent command path. Cobra IDs are
// space-separated tokens ("docker container run", "docker --config").
// The parent is everything before the last space-separated token —
// for "docker container run" → "docker container"; for the root
// command "docker" → "" (no parent).
//
// Flags' containers are the subcommand they attach to: "docker --debug"
// → "docker" via the same last-space-strip rule.
func (cobraView) ContainerOf(id string, _ map[string]any) string {
	sp := strings.LastIndex(id, " ")
	if sp < 0 {
		return ""
	}
	return id[:sp]
}

func (cobraView) Noun() (string, string) { return "command", "commands" }

// TotalNoun — cobra's surface is commands + flags, so the umbrella
// noun for .Total is "commands & flags". The primary-detail Noun
// stays "command" / "commands" because substance grading, the
// worklist, and the per-element listing all operate on subcommands
// only.
func (cobraView) TotalNoun() (string, string) { return "element", "commands & flags" }

// VersionScheme is empty — cobra has no @available equivalent. The
// versionscheme package falls back to the FIDL scheme when this is
// empty, but no deprecation parsing fires on cobra elements anyway
// (the cobra adapter doesn't emit versionConstraints).
func (cobraView) VersionScheme() string { return "" }

func init() {
	RegisterEcosystem(cobraView{})
}

// Earlier versions of this file registered binary-name aliases
// ("docker", "kubectl", "gh") so users could pass --ecosystem=<binary>
// and not have to know the surface is cobra-shaped. That conflated
// two concepts: --library names the project being scanned;
// --ecosystem names the rendering shape. The aliases were removed in
// favor of a single canonical id "cli" — invocations that still pass
// --ecosystem docker / kubectl / gh fall through to nounsFor() in
// compute.go (which keeps "command"/"commands" nouns) and continue
// to render correctly, just labelled "docker surface" instead of
// "cli surface" in the footer.
