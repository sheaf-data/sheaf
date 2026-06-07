// Package k8smanifest implements a contract-anchor adapter for plain /
// rendered Kubernetes manifests — valid YAML only (e.g. the output of
// `helm template`). It is the universal fallback for charts that ship no
// schema: where the `crd` adapter needs a CustomResourceDefinition and
// the `helmvalues` adapter needs values, this adapter reads whatever
// finished manifest the user already has and reports the resource kinds
// and the fields actually present in it.
//
// A single file may contain several YAML documents separated by `---`;
// the adapter handles multi-document streams. Each document that looks
// like a Kubernetes object (it has both `apiVersion` and `kind`) yields:
//
//   - the API group       → Kind = LIBRARY,     Id = "<group>"
//   - each resource kind  → Kind = TYPE,        Id = "<group>/<Kind>"
//   - each field present   → Kind = CONFIG_KNOB, Id = "<group>/<Kind>.<dotted.path>"
//
// The group is derived from apiVersion: "apps/v1" → "apps", a core
// object ("v1") → "core". Ecosystem = "manifest" on every element
// (NEVER "kubernetes"). Library = the group.
//
// Hard constraints (see the K8s-ingestion spec §4/§5):
//   - This adapter parses RENDERED, valid YAML ONLY. It NEVER parses raw
//     Helm templates ({{ ... }}); a document that fails to parse as YAML
//     is skipped gracefully, never crashes the walk and never emits a
//     "{{.Values.x}}" pseudo-field. Rendering is the user's `helm
//     template` step, outside Sheaf.
//   - Provenance or it doesn't render: every element sets Location
//     (file:line) into the actual manifest YAML, derived from yaml.Node
//     line numbers exactly as the crd adapter does.
//   - Purely mechanical: no LLM, no cluster access, no template
//     rendering.
//
// Unlike CRDs, a rendered manifest carries no inline field documentation,
// so this adapter emits no DocClaims — every field is reported as a real
// surface element, and whether it is documented is the doc side's job.
package k8smanifest

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	"google.golang.org/protobuf/types/known/structpb"
)

const Name = "k8smanifest"
const Version = "0.1.0"

const ecosystem = "manifest"

// coreGroup is the synthetic library name for core (group-less) objects
// whose apiVersion is a bare version like "v1". Kubernetes itself calls
// this the "core" / "" group; "core" reads better as a library label.
const coreGroup = "core"

type Adapter struct {
	include []string
	exclude []string
}

type Config struct {
	Include []string // repo-relative globs; defaults to ["**/*.yaml", "**/*.yml"]
	Exclude []string
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.yaml", "**/*.yml"}
	}
	return &Adapter{
		include: include,
		exclude: cfg.Exclude,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Discover walks the repo for YAML files, parses every Kubernetes
// manifest document it finds (a single file may hold several, separated
// by `---`), and emits ContractElements for each API group, each
// resource kind, and each field actually present in the manifest.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Groups and types are synthesized once across all files; the same
	// kind appearing in several documents (e.g. many Deployments) maps to
	// one TYPE element pointing at its first occurrence.
	groups := map[string]*contractpb.ContractElement{}
	types := map[string]*contractpb.ContractElement{}
	knobs := map[string]*contractpb.ContractElement{}
	var order []string // CONFIG_KNOB ids in first-seen order (stable before sort)

	err := adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return nil // skip unreadable file; not a hard error
		}
		parseManifestFile(rel, body, scope, groups, types, knobs, &order)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("k8smanifest: walk repo: %w", err)
	}

	out := make([]*contractpb.ContractElement, 0, len(groups)+len(types)+len(knobs))
	for _, e := range groups {
		out = append(out, e)
	}
	for _, e := range types {
		out = append(out, e)
	}
	for _, e := range knobs {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out, nil
}

// parseManifestFile decodes every YAML document in body, keeps the ones
// that look like Kubernetes objects (apiVersion + kind present), and
// records elements into the caller's group/type/knob maps so the same
// group/kind/field across documents is deduplicated to its first sight.
// A document that fails to parse (e.g. an un-rendered Helm template) is
// skipped — the decoder stops at the malformed doc but documents already
// read are kept.
func parseManifestFile(relPath string, body []byte, scope adapters.ScopeConfig, groups, types, knobs map[string]*contractpb.ContractElement, order *[]string) {
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			// EOF, or a doc that is not valid YAML (a raw Helm template
			// reaches us as `{{ ... }}` which is not parseable here). Stop
			// cleanly; never crash, never emit a pseudo-field.
			break
		}
		root := documentRoot(&doc)
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}
		apiVersion := scalarValue(root, "apiVersion")
		kind := scalarValue(root, "kind")
		if apiVersion == "" || kind == "" {
			// Not a recognizable Kubernetes object — skip rather than
			// emit noise. (A List, a Helm NOTES block, etc.)
			continue
		}
		group := groupOf(apiVersion)
		if !libraryInScope(group, scope) {
			continue
		}

		// LIBRARY (one per group, first occurrence wins for location).
		if _, ok := groups[group]; !ok {
			line := keyLine(root, "apiVersion")
			groups[group] = &contractpb.ContractElement{
				Id:        group,
				Kind:      contractpb.ContractElementKind_LIBRARY,
				Ecosystem: ecosystem,
				Library:   group,
				Location:  &commonpb.SourceLocation{Path: relPath, Line: uint32(line)},
			}
		}

		// TYPE (one per <group>/<Kind>, first occurrence wins).
		typeID := group + "/" + kind
		if _, ok := types[typeID]; !ok {
			meta := map[string]interface{}{
				"group":      group,
				"kind":       kind,
				"apiVersion": apiVersion,
			}
			metaStruct, _ := structpb.NewStruct(meta)
			types[typeID] = &contractpb.ContractElement{
				Id:            typeID,
				Kind:          contractpb.ContractElementKind_TYPE,
				Ecosystem:     ecosystem,
				Library:       group,
				Location:      &commonpb.SourceLocation{Path: relPath, Line: uint32(keyLine(root, "kind"))},
				EcosystemMeta: metaStruct,
			}
		}

		// CONFIG_KNOB per field path actually present in this document.
		walkFields(relPath, group, kind, "", root, knobs, order)
	}
}

// walkFields recursively emits a CONFIG_KNOB per field present in a
// mapping node. dotted is the path accumulated so far (empty at the
// object root). Sequence values descend with a `[]` marker so a path
// reads naturally (spec.template.spec.containers[].image). The first
// occurrence of a given field path wins (location + scalar type), so a
// field repeated across documents collapses to one element.
//
// The four Kubernetes envelope keys (apiVersion, kind, metadata, status)
// are intentionally not emitted as their own CONFIG_KNOBs at the root —
// they are universal envelope, not part of the resource's configurable
// surface — but the object's real fields under spec/data/etc. are walked
// fully. (metadata is descended into so labels/annotations still surface
// as fields, but bare `metadata` itself is not a knob.)
func walkFields(relPath, group, kind, dotted string, node *yaml.Node, knobs map[string]*contractpb.ContractElement, order *[]string) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valNode := node.Content[i+1]
			fieldName := keyNode.Value
			if dotted == "" && isEnvelopeKey(fieldName) {
				// Skip the envelope key itself; do not descend into the
				// pure-identity ones (apiVersion/kind are scalars anyway).
				continue
			}
			path := fieldName
			if dotted != "" {
				path = dotted + "." + fieldName
			}
			emitKnob(relPath, group, kind, path, keyNode, valNode, knobs, order)
			walkFields(relPath, group, kind, path, valNode, knobs, order)
		}
	case yaml.SequenceNode:
		// Descend into the first element only: a rendered list (e.g.
		// containers) repeats the same field shape, so walking every
		// element would emit identical paths. The `[]` marker keeps the
		// path honest about being a list member.
		itemPath := dotted
		if itemPath != "" {
			itemPath += "[]"
		}
		for _, item := range node.Content {
			walkFields(relPath, group, kind, itemPath, item, knobs, order)
		}
	}
}

// emitKnob records a CONFIG_KNOB for one field path on first sight.
func emitKnob(relPath, group, kind, path string, keyNode, valNode *yaml.Node, knobs map[string]*contractpb.ContractElement, order *[]string) {
	id := group + "/" + kind + "." + path
	if _, ok := knobs[id]; ok {
		return
	}
	meta := map[string]interface{}{
		"yamlType": yamlNodeType(valNode),
	}
	metaStruct, _ := structpb.NewStruct(meta)
	knobs[id] = &contractpb.ContractElement{
		Id:            id,
		Kind:          contractpb.ContractElementKind_CONFIG_KNOB,
		Ecosystem:     ecosystem,
		Library:       group,
		Location:      &commonpb.SourceLocation{Path: relPath, Line: uint32(keyNode.Line)},
		EcosystemMeta: metaStruct,
	}
	*order = append(*order, id)
}

// isEnvelopeKey reports whether a root-level key is part of the universal
// Kubernetes object envelope rather than the resource's configurable
// surface.
func isEnvelopeKey(name string) bool {
	switch name {
	case "apiVersion", "kind":
		return true // pure identity; recorded on the TYPE element instead
	}
	return false
}

// groupOf extracts the API group from an apiVersion. "apps/v1" → "apps";
// "monitoring.coreos.com/v1" → "monitoring.coreos.com"; a bare "v1" (the
// core group) → "core".
func groupOf(apiVersion string) string {
	apiVersion = strings.TrimSpace(apiVersion)
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		g := apiVersion[:i]
		if g == "" {
			return coreGroup
		}
		return g
	}
	return coreGroup
}

// yamlNodeType returns a coarse type label for a value node, for the
// element's ecosystem_meta. Scalars are tagged with their YAML core type
// when known (str/int/bool/float/null), composites as map/list.
func yamlNodeType(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "map"
	case yaml.SequenceNode:
		return "list"
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!int":
			return "int"
		case "!!bool":
			return "bool"
		case "!!float":
			return "float"
		case "!!null":
			return "null"
		case "!!str":
			return "string"
		default:
			return "scalar"
		}
	default:
		return "scalar"
	}
}

// documentRoot unwraps a DocumentNode to its content mapping.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		return doc.Content[0]
	}
	return doc
}

// scalarValue returns the trimmed scalar value for key in a mapping, "".
func scalarValue(node *yaml.Node, key string) string {
	if v := mapValue(node, key); v != nil {
		return strings.TrimSpace(v.Value)
	}
	return ""
}

// keyLine returns the source line of key's KEY node in a mapping, 0 if
// absent.
func keyLine(node *yaml.Node, key string) int {
	if node == nil || node.Kind != yaml.MappingNode {
		return 0
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i].Line
		}
	}
	return 0
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// libraryInScope mirrors the crd/cml/cobra scope semantics: empty filter
// means "include everything"; otherwise the group must match a Libraries
// or AlsoInclude entry and not be excluded. Patterns ending in ".*" / "*"
// match by prefix.
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
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(lib, strings.TrimSuffix(pattern, "*"))
	}
	return false
}
