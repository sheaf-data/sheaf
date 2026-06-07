// Package cobra implements a contract-anchor adapter for CLI tools
// built on github.com/spf13/cobra. The canonical example is the
// `docker` CLI, but anything using cobra and shipping a YAML
// reference bundle works.
//
// Input is a directory of per-subcommand YAML files in the schema
// docker/cli's `make yamldocs` emits — one file per subcommand path,
// with fields {command, short, long, usage, options[],
// inherited_options[], cname[], pname, plink, ...}. Filename
// underscores map to spaces in the subcommand path
// (docker_compose_up.yaml → "docker compose up"). The `command:`
// field, when present, is the authoritative path; the filename is a
// fallback.
//
// This adapter does NOT compile or run the target binary; it parses
// only the YAML bundle. Generating that bundle is the user's job
// (docker/cli ships a `make yamldocs` target; cobra's own
// `cobra/doc.GenYamlTree` can produce equivalent output for other
// tools).

package cobra

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

const Name = "cobra"
const Version = "0.1.0"

type Adapter struct {
	yamlDir    string
	include    []string
	exclude    []string
	binaryName string
	urlBase    string
}

type Config struct {
	YAMLDir    string
	Include    []string
	Exclude    []string
	BinaryName string
	// URLBase is the pattern for an element's canonical clickable URL.
	// Supports {basename} (yaml filename minus extension, e.g.
	// "kubectl_get") and {subcommand} (underscores→slashes form,
	// e.g. "kubectl/get") substitutions. Empty disables URL emission.
	URLBase string
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.yaml"}
	}
	return &Adapter{
		yamlDir:    cfg.YAMLDir,
		include:    include,
		exclude:    cfg.Exclude,
		binaryName: cfg.BinaryName,
		urlBase:    cfg.URLBase,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// commandYAML mirrors the schema docker/cli's yaml docs emit. Fields
// we don't use are intentionally omitted; yaml.v3 ignores unknown keys.
type commandYAML struct {
	Command          string       `yaml:"command"`
	Short            string       `yaml:"short"`
	Long             string       `yaml:"long"`
	Usage            string       `yaml:"usage"`
	Pname            string       `yaml:"pname"`
	Plink            string       `yaml:"plink"`
	Options          []optionYAML `yaml:"options"`
	InheritedOptions []optionYAML `yaml:"inherited_options"`
	Cname            []string     `yaml:"cname"`
	Deprecated       bool         `yaml:"deprecated"`
	Experimental     bool         `yaml:"experimental"`
	Examples         string       `yaml:"examples"`
	// Aliases is emitted by cobra's GenYamlTree as a comma-separated
	// string ("docker container run, docker run") but other emitters
	// may use a YAML sequence. Decode loose, normalize via aliasList.
	Aliases yaml.Node `yaml:"aliases"`
}

// aliasList normalizes the Aliases yaml.Node into a []string,
// tolerating both the comma-separated string form docker/cli emits
// and the YAML sequence form some other generators use.
func (c *commandYAML) aliasList() []string {
	if c == nil {
		return nil
	}
	switch c.Aliases.Kind {
	case yaml.ScalarNode:
		var s string
		if err := c.Aliases.Decode(&s); err != nil {
			return nil
		}
		return splitTrim(s, ",")
	case yaml.SequenceNode:
		var out []string
		if err := c.Aliases.Decode(&out); err != nil {
			return nil
		}
		// Defensive trim: some generators leave whitespace.
		for i := range out {
			out[i] = strings.TrimSpace(out[i])
		}
		return out
	}
	return nil
}

func splitTrim(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

type optionYAML struct {
	Option       string `yaml:"option"`
	Shorthand    string `yaml:"shorthand"`
	ValueType    string `yaml:"value_type"`
	DefaultValue string `yaml:"default_value"`
	Description  string `yaml:"description"`
	Deprecated   bool   `yaml:"deprecated"`
	Hidden       bool   `yaml:"hidden"`
	Experimental bool   `yaml:"experimental"`
}

// parsedYAML carries a single YAML file's decoded contents plus the
// paths used to construct ContractElements.
type parsedYAML struct {
	doc     commandYAML
	repoRel string
	walkRel string
	command string // resolved commandID (from doc.Command or filename)
}

// Discover walks yaml_dir, parses every matching YAML in a first
// pass, builds alias groups, deduplicates mutual-alias YAMLs (so a
// command registered as both "docker run" and "docker container
// run" produces ONE element with both names as aliases), and emits
// ContractElements per subcommand and per flag in a second pass.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	if a.yamlDir == "" {
		return nil, fmt.Errorf("cobra: yaml_dir is empty")
	}
	if !libraryInScope(a.binaryName, scope) {
		return nil, nil
	}
	walkBase := a.yamlDir
	if !filepath.IsAbs(walkBase) {
		walkBase = filepath.Join(repoRoot, walkBase)
	}

	// First pass: parse every file.
	var parsed []parsedYAML
	err := adapters.WalkMatching(walkBase, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := filepath.Join(walkBase, rel)
		body, err := os.ReadFile(abs)
		if err != nil {
			return fmt.Errorf("cobra: read %s: %w", abs, err)
		}
		var doc commandYAML
		if err := yaml.Unmarshal(body, &doc); err != nil {
			return fmt.Errorf("cobra: parse %s: %w", abs, err)
		}
		commandID := strings.TrimSpace(doc.Command)
		if commandID == "" {
			commandID = commandPathFromFilename(rel, a.binaryName)
		}
		if commandID == "" {
			return nil
		}
		repoRel := rel
		if !filepath.IsAbs(a.yamlDir) {
			repoRel = filepath.ToSlash(filepath.Join(a.yamlDir, rel))
		}
		parsed = append(parsed, parsedYAML{
			doc:     doc,
			repoRel: repoRel,
			walkRel: rel,
			command: commandID,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Build a dedup view: when two YAMLs are mutual aliases of each
	// other (cobra registers the same command at multiple paths,
	// e.g. `docker run` AND `docker container run`), pick the
	// longest-named one as canonical and drop the rest.
	keepers, aliasesByCommand := dedupMutualAliases(parsed)

	// Second pass: emit ContractElements only for the canonical
	// YAMLs. Flag aliases are computed by substituting each parent
	// alias prefix.
	var out []*contractpb.ContractElement
	for _, p := range keepers {
		binary := binaryFromCommandID(p.command)
		if binary == "" {
			binary = a.binaryName
		}
		loc := &commonpb.SourceLocation{Path: p.repoRel, Line: 1}
		// Cobra YAMLs are typically machine-generated output that lives
		// only on the user's disk (e.g. `make yamldocs` artifact, or
		// the sheaf-cobra-yaml/ tree produced by kubectl-yamlgen).
		// Linking to their on-disk path produces 404s on github. The
		// project's public reference page is the canonical user-facing
		// URL — derive it from url_base + filename.
		if a.urlBase != "" {
			basename := strings.TrimSuffix(filepath.Base(p.repoRel), filepath.Ext(p.repoRel))
			subcommand := strings.ReplaceAll(basename, "_", "/")
			u := strings.ReplaceAll(a.urlBase, "{basename}", basename)
			u = strings.ReplaceAll(u, "{subcommand}", subcommand)
			loc.Url = u
		}
		subAliases := aliasesByCommand[p.command]

		out = append(out, &contractpb.ContractElement{
			Id:                p.command,
			Kind:              contractpb.ContractElementKind_SUBCOMMAND,
			Ecosystem:         "cobra",
			Library:           binary,
			Location:          loc,
			DocCommentExcerpt: pickDescription(p.doc.Short, p.doc.Long),
			Aliases:           subAliases,
		})

		for _, o := range p.doc.Options {
			if o.Option == "" {
				continue
			}
			kind := contractpb.ContractElementKind_FLAG
			if strings.EqualFold(o.ValueType, "bool") {
				kind = contractpb.ContractElementKind_SWITCH
			}
			flagSuffix := " --" + o.Option
			flagAliases := make([]string, 0, len(subAliases))
			for _, alias := range subAliases {
				flagAliases = append(flagAliases, alias+flagSuffix)
			}
			out = append(out, &contractpb.ContractElement{
				Id:                p.command + flagSuffix,
				Kind:              kind,
				Ecosystem:         "cobra",
				Library:           binary,
				Location:          loc,
				DocCommentExcerpt: o.Description,
				Aliases:           flagAliases,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out, nil
}

// dedupMutualAliases identifies sets of YAMLs that share an alias
// group (each lists the others in its `aliases:` field) and returns:
//
//   - keepers: one parsedYAML per group — the one with the most
//     space-separated tokens in its command (or alphabetically first
//     on tie), treated as canonical.
//   - aliasesByCommand: for each kept command, the full list of
//     alternate names (including the canonical itself) discovered
//     from the union of the group's aliases fields.
//
// YAMLs that aren't members of any mutual-alias group pass through
// untouched, with their own aliases: field as the alias list.
func dedupMutualAliases(parsed []parsedYAML) ([]parsedYAML, map[string][]string) {
	// Index by command for O(1) lookup.
	byCommand := make(map[string]parsedYAML, len(parsed))
	for _, p := range parsed {
		byCommand[p.command] = p
	}

	// Union-find: union each command with its declared aliases when
	// that alias also exists as a top-level command in the bundle.
	parent := make(map[string]string, len(parsed))
	for c := range byCommand {
		parent[c] = c
	}
	var find func(string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, p := range parsed {
		for _, alias := range p.doc.aliasList() {
			alias = strings.TrimSpace(alias)
			if _, ok := byCommand[alias]; ok && alias != p.command {
				union(p.command, alias)
			}
		}
	}

	// Group members by representative.
	groups := make(map[string][]parsedYAML)
	for _, p := range parsed {
		root := find(p.command)
		groups[root] = append(groups[root], p)
	}

	var keepers []parsedYAML
	aliasesByCommand := make(map[string][]string)
	for _, members := range groups {
		canonical := pickCanonical(members)
		keepers = append(keepers, canonical)
		// Build the alias list as the union of:
		//  - every member's own command name
		//  - every entry from every member's aliases: field that maps
		//    to a real command in the bundle (so we don't emit aliases
		//    pointing at nothing)
		aliasSet := make(map[string]bool)
		aliasSet[canonical.command] = true
		for _, m := range members {
			aliasSet[m.command] = true
			for _, alias := range m.doc.aliasList() {
				alias = strings.TrimSpace(alias)
				if alias != "" {
					aliasSet[alias] = true
				}
			}
		}
		ordered := make([]string, 0, len(aliasSet))
		for a := range aliasSet {
			ordered = append(ordered, a)
		}
		sort.Strings(ordered)
		aliasesByCommand[canonical.command] = ordered
	}
	return keepers, aliasesByCommand
}

// pickCanonical chooses the canonical YAML from a mutual-alias
// group. Preference: most space-separated tokens (deepest in the
// hierarchy), then alphabetic order on tie.
func pickCanonical(members []parsedYAML) parsedYAML {
	best := members[0]
	for _, m := range members[1:] {
		mTokens := strings.Count(m.command, " ")
		bTokens := strings.Count(best.command, " ")
		switch {
		case mTokens > bTokens:
			best = m
		case mTokens == bTokens && m.command < best.command:
			best = m
		}
	}
	return best
}

// commandPathFromFilename converts "docker_compose_up.yaml" → "docker compose up".
// If binaryName is non-empty and the filename's first segment differs,
// binaryName is prepended (e.g. "container_ls.yaml" + binary="docker"
// → "docker container ls"). Directory components in walkRel are
// ignored — only the leaf filename is consulted.
func commandPathFromFilename(walkRel, binaryName string) string {
	base := filepath.Base(walkRel)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" {
		return ""
	}
	parts := strings.Split(base, "_")
	if binaryName != "" && (len(parts) == 0 || parts[0] != binaryName) {
		parts = append([]string{binaryName}, parts...)
	}
	return strings.Join(parts, " ")
}

// binaryFromCommandID extracts "docker" from "docker container ls".
func binaryFromCommandID(commandID string) string {
	if i := strings.Index(commandID, " "); i >= 0 {
		return commandID[:i]
	}
	return commandID
}

func pickDescription(short, long string) string {
	long = strings.TrimSpace(long)
	if long != "" {
		return long
	}
	return strings.TrimSpace(short)
}

func libraryInScope(lib string, scope adapters.ScopeConfig) bool {
	for _, ex := range scope.Exclude {
		if matchLib(ex, lib) {
			return false
		}
	}
	if len(scope.Libraries) == 0 && len(scope.AlsoInclude) == 0 {
		return true
	}
	for _, l := range scope.Libraries {
		if matchLib(l, lib) {
			return true
		}
	}
	for _, l := range scope.AlsoInclude {
		if matchLib(l, lib) {
			return true
		}
	}
	return false
}

func matchLib(pattern, lib string) bool {
	if pattern == lib {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		return strings.HasPrefix(lib, strings.TrimSuffix(pattern, "*"))
	}
	return false
}
