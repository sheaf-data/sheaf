// Package helmvalues implements a contract-anchor adapter for a Helm
// chart's values surface — the configurable knobs a user sets in their
// own values file to drive the chart's templates.
//
// Two input paths, in preference order:
//
//   - PREFERRED: values.schema.json (Helm 3's formal JSON-Schema values
//     contract). The adapter walks the schema's `properties` recursively
//     (resolving JSON-Schema `$ref` into `#/$defs/...`), emitting one
//     CONFIG_KNOB per value key. Each schema node's `description` becomes
//     the element's doc_comment_excerpt; a key with no description is a
//     real undocumented finding (left blank, not synthesized). This is
//     the authoritative path — the schema is the contract the chart
//     author actually published.
//
//   - FALLBACK: values.yaml defaults + the helm-docs `# -- <description>`
//     comment convention. When a chart ships no schema, the adapter walks
//     the default values tree and reads the leading comment of each key as
//     its doc source. A key with no `# --` comment is undocumented.
//
// For a given chart directory the adapter uses the schema path when a
// values.schema.json is present and the values.yaml path otherwise — the
// two never both emit for the same chart, so there is no double-counting.
//
// HONESTY CAVEAT (surfaced in KNOWN_LIMITATIONS.md and the report):
// neither path is guaranteed complete. values.yaml defaults are NOT the
// full reference surface — the true surface is whatever the chart's
// templates actually reference, and a template may read a value that has
// no default and no schema entry. The schema is more authoritative but
// may still omit values the templates use. This adapter reports the
// values surface as published, not a proof of completeness.
//
// Each emitted ContractElement has:
//   - the chart    → Kind = LIBRARY,     Id = "<chart>"
//   - each value   → Kind = CONFIG_KNOB, Id = "<chart>.<dotted.path>"
//   - Ecosystem = "helm" (NEVER "kubernetes")
//   - Library = the chart name (from Chart.yaml, else the directory name)
//   - Location pointing at the line of the key node in the source
//     values.schema.json (schema path) or values.yaml (fallback path)
//   - EcosystemMeta carrying the value's type, default, and source path.
//
// Purely mechanical: parses JSON/YAML, no LLM, no cluster access, and —
// critically — NO raw Helm template parsing ({{ ... }}). The adapter
// never reads templates/*.yaml; it reads only values.schema.json and
// values.yaml, both of which are valid JSON/YAML.
package helmvalues

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	"google.golang.org/protobuf/types/known/structpb"
)

const Name = "helmvalues"
const Version = "0.1.0"

const ecosystem = "helm"

// helmDocsMarker is the helm-docs comment convention that introduces a
// field's description in values.yaml: `# -- <description>`.
const helmDocsMarker = "# --"

type Adapter struct {
	include []string
	exclude []string
}

type Config struct {
	// Include / exclude globs evaluated against repo-relative paths.
	// Defaults to the two Helm values files: values.schema.json and
	// values.yaml. A chart's schema, when present, wins over its
	// values.yaml for that chart.
	Include []string
	Exclude []string
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/values.schema.json", "**/values.yaml", "**/values.yml"}
	}
	return &Adapter{
		include: include,
		exclude: cfg.Exclude,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Discover walks the repo for Helm values files and emits a LIBRARY for
// each chart plus a CONFIG_KNOB for each value key.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	elems, _, err := a.discover(ctx, repoRoot, scope)
	return elems, err
}

// DiscoverWithDocs runs the same walk but additionally returns the inline
// DocClaims extracted from each value's documentation (the schema
// `description` on the preferred path, the `# --` helm-docs comment on
// the fallback path). The orchestrator type-asserts for this entry point
// so the chart's own inline docs feed the doc-coverage join — a value
// with a real description counts as documented; one with none is a true
// undocumented finding. No external doc source is consulted. This mirrors
// the crd adapter, whose CRDs carry inline OpenAPI descriptions.
func (a *Adapter) DiscoverWithDocs(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, error) {
	return a.discover(ctx, repoRoot, scope)
}

func (a *Adapter) discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	// First pass: group candidate files by chart directory so we can
	// pick the schema path over the values.yaml path per chart. A chart
	// dir is the directory holding the values file.
	type chartFiles struct {
		schemaRel string
		valuesRel string
	}
	charts := map[string]*chartFiles{}
	var dirOrder []string

	err := adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := path.Dir(rel)
		cf := charts[dir]
		if cf == nil {
			cf = &chartFiles{}
			charts[dir] = cf
			dirOrder = append(dirOrder, dir)
		}
		base := path.Base(rel)
		switch {
		case base == "values.schema.json":
			cf.schemaRel = rel
		case base == "values.yaml" || base == "values.yml":
			if cf.valuesRel == "" {
				cf.valuesRel = rel
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("helmvalues: walk repo: %w", err)
	}

	var out []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim

	for _, dir := range dirOrder {
		cf := charts[dir]
		chart := chartName(repoRoot, dir)
		if chart == "" {
			continue
		}
		if !libraryInScope(chart, scope) {
			continue
		}

		var elems []*contractpb.ContractElement
		var cls []*docclaimpb.DocClaim
		var srcRel string
		switch {
		case cf.schemaRel != "":
			body, rerr := adapters.ReadFile(repoRoot, cf.schemaRel)
			if rerr != nil {
				continue
			}
			elems, cls = parseSchemaFile(cf.schemaRel, chart, body)
			srcRel = cf.schemaRel
		case cf.valuesRel != "":
			body, rerr := adapters.ReadFile(repoRoot, cf.valuesRel)
			if rerr != nil {
				continue
			}
			elems, cls = parseValuesFile(cf.valuesRel, chart, body)
			srcRel = cf.valuesRel
		default:
			continue
		}
		if len(elems) == 0 {
			continue
		}
		// LIBRARY element for the chart, located at the source file's
		// first line.
		out = append(out, &contractpb.ContractElement{
			Id:        chart,
			Kind:      contractpb.ContractElementKind_LIBRARY,
			Ecosystem: ecosystem,
			Library:   chart,
			Location:  &commonpb.SourceLocation{Path: srcRel, Line: 1},
		})
		out = append(out, elems...)
		claims = append(claims, cls...)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out, claims, nil
}

// chartName resolves a chart's library identity: the `name` field of the
// Chart.yaml in the chart directory if readable, else the directory's
// base name. A top-level values file (dir ".") falls back to "chart".
func chartName(repoRoot, dir string) string {
	chartYaml := "Chart.yaml"
	if dir != "." && dir != "" {
		chartYaml = path.Join(dir, "Chart.yaml")
	}
	if body, err := adapters.ReadFile(repoRoot, chartYaml); err == nil {
		var doc yaml.Node
		if yaml.Unmarshal(body, &doc) == nil {
			root := documentRoot(&doc)
			if n := mapValue(root, "name"); n != nil {
				if name := strings.TrimSpace(n.Value); name != "" {
					return name
				}
			}
		}
	}
	if dir == "." || dir == "" {
		return "chart"
	}
	return path.Base(dir)
}

// ============================================================
// schema path (values.schema.json — preferred)
// ============================================================

// parseSchemaFile parses a values.schema.json and emits one CONFIG_KNOB
// per value key plus an inline DocClaim per key carrying a non-empty
// description. JSON is parsed via yaml.Node (YAML is a JSON superset) so
// every key node carries its source .Line for exact file:line provenance
// — encoding/json discards line numbers.
func parseSchemaFile(relPath, chart string, body []byte) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, nil // not parseable as JSON/YAML; skip gracefully
	}
	root := documentRoot(&doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, nil
	}
	defs := mapValue(root, "$defs")

	// The root schema may itself be a $ref (cert-manager's generated
	// schema is `{ "$defs": {...}, "$ref": "#/$defs/helm-values" }`), or
	// it may carry `properties` inline. Resolve to the effective schema
	// node before walking.
	start := resolveRef(root, defs)
	if start == nil {
		start = root
	}

	var out []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim
	// guard against cyclic $ref chains.
	visiting := map[*yaml.Node]bool{}
	walkSchema(relPath, chart, "", start, defs, visiting, &out, &claims)
	return out, claims
}

// walkSchema recursively emits a CONFIG_KNOB per property in a JSON-Schema
// object node, resolving `$ref` to the shared `$defs` pool. dotted is the
// value path accumulated so far (empty at the root). It descends into
// object `properties` and array `items`.
func walkSchema(relPath, chart, dotted string, schema, defs *yaml.Node, visiting map[*yaml.Node]bool, out *[]*contractpb.ContractElement, claims *[]*docclaimpb.DocClaim) {
	schema = resolveRef(schema, defs)
	if schema == nil || schema.Kind != yaml.MappingNode {
		return
	}
	if visiting[schema] {
		return // cycle guard
	}
	visiting[schema] = true
	defer delete(visiting, schema)

	if props := mapValue(schema, "properties"); props != nil && props.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(props.Content); i += 2 {
			keyNode := props.Content[i]
			valNode := props.Content[i+1]
			fieldName := keyNode.Value
			fieldPath := fieldName
			if dotted != "" {
				fieldPath = dotted + "." + fieldName
			}
			resolved := resolveRef(valNode, defs)
			elem, claim := knobElement(relPath, chart, fieldPath, keyNode, resolved)
			*out = append(*out, elem)
			if claim != nil {
				*claims = append(*claims, claim)
			}
			walkSchema(relPath, chart, fieldPath, valNode, defs, visiting, out, claims)
		}
	}

	// Array element schema: descend, keeping the same dotted path with a
	// [] marker so `extraArgs[]` reads naturally.
	if items := mapValue(schema, "items"); items != nil {
		itemPath := dotted
		if itemPath != "" {
			itemPath += "[]"
		}
		walkSchema(relPath, chart, itemPath, items, defs, visiting, out, claims)
	}
}

// resolveRef follows a JSON-Schema "$ref" of the form
// "#/$defs/<name>" to its target node in the defs mapping. A node with
// no $ref (or an unresolvable / external one) is returned unchanged.
func resolveRef(node, defs *yaml.Node) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return node
	}
	ref := mapValue(node, "$ref")
	if ref == nil || ref.Value == "" {
		return node
	}
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref.Value, prefix) || defs == nil {
		return node // external or non-defs ref; leave as-is
	}
	name := strings.TrimPrefix(ref.Value, prefix)
	if target := mapValue(defs, name); target != nil {
		return target
	}
	return node
}

// ============================================================
// values.yaml fallback path
// ============================================================

// parseValuesFile walks a values.yaml defaults tree and emits one
// CONFIG_KNOB per key. The doc source is the helm-docs `# -- <desc>`
// comment on (or just above) the key; a key with no such comment is a
// real undocumented finding (DocCommentExcerpt left blank).
func parseValuesFile(relPath, chart string, body []byte) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, nil
	}
	root := documentRoot(&doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim
	walkValues(relPath, chart, "", root, &out, &claims)
	return out, claims
}

// walkValues recursively emits a CONFIG_KNOB per key present in a
// values.yaml mapping. dotted is the path so far. Sequence values descend
// with a `[]` marker. The key's helm-docs `# --` comment (HeadComment or
// LineComment) supplies the doc source.
func walkValues(relPath, chart, dotted string, node *yaml.Node, out *[]*contractpb.ContractElement, claims *[]*docclaimpb.DocClaim) {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valNode := node.Content[i+1]
			fieldPath := keyNode.Value
			if dotted != "" {
				fieldPath = dotted + "." + keyNode.Value
			}
			doc := helmDocsComment(keyNode, valNode)
			elem, claim := valuesKnob(relPath, chart, fieldPath, keyNode, valNode, doc)
			*out = append(*out, elem)
			if claim != nil {
				*claims = append(*claims, claim)
			}
			walkValues(relPath, chart, fieldPath, valNode, out, claims)
		}
	case yaml.SequenceNode:
		itemPath := dotted
		if itemPath != "" {
			itemPath += "[]"
		}
		for _, item := range node.Content {
			walkValues(relPath, chart, itemPath, item, out, claims)
		}
	}
}

// helmDocsComment extracts a key's documentation from its YAML comments,
// preferring the helm-docs `# -- <desc>` convention. The marker may sit
// on the key's HeadComment (the comment block immediately above) or its
// LineComment (trailing the key). When a `# --` marker is present, only
// the text after it (and any continuation comment lines) is the doc; when
// no marker is present, the whole leading comment block is used as a
// best-effort doc source. Returns "" when there is no comment at all.
func helmDocsComment(keyNode, valNode *yaml.Node) string {
	// helm-docs places the marker on the key's line comment or the head
	// comment of the key (or, for block values, the value node).
	candidates := []string{keyNode.LineComment, keyNode.HeadComment}
	if valNode != nil {
		candidates = append(candidates, valNode.LineComment, valNode.HeadComment)
	}

	// Prefer an explicit `# --` marker anywhere in the candidates.
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if doc := extractMarkedDoc(c); doc != "" {
			return doc
		}
	}
	// No marker: fall back to the first non-empty leading comment block,
	// stripped of comment punctuation.
	for _, c := range []string{keyNode.HeadComment, keyNode.LineComment} {
		if c == "" {
			continue
		}
		if s := stripCommentPunctuation(c); s != "" {
			return s
		}
	}
	return ""
}

// extractMarkedDoc finds the helm-docs `# --` marker in a raw comment
// block and returns the description that follows it (joined across
// continuation lines). Returns "" when the block has no marker.
func extractMarkedDoc(raw string) string {
	lines := strings.Split(raw, "\n")
	var parts []string
	started := false
	for _, ln := range lines {
		stripped := strings.TrimLeft(ln, "# \t")
		full := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "#"))
		if !started {
			if idx := strings.Index(ln, helmDocsMarker); idx >= 0 {
				started = true
				after := strings.TrimSpace(ln[idx+len(helmDocsMarker):])
				if after != "" {
					parts = append(parts, after)
				}
			}
			continue
		}
		// Continuation: subsequent comment lines until a blank/non-comment.
		if strings.TrimSpace(ln) == "" || !strings.HasPrefix(strings.TrimSpace(ln), "#") {
			break
		}
		// A second `# --` would start a new field's doc; stop.
		if strings.Contains(ln, helmDocsMarker) {
			break
		}
		_ = stripped
		if full != "" {
			parts = append(parts, full)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// stripCommentPunctuation turns a raw multi-line `#`-comment block into
// plain prose (drops leading `#` and whitespace, joins lines with a
// space). Used as the no-marker fallback doc source.
func stripCommentPunctuation(raw string) string {
	lines := strings.Split(raw, "\n")
	var parts []string
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		s = strings.TrimPrefix(s, "#")
		s = strings.TrimSpace(s)
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// ============================================================
// element construction
// ============================================================

// knobElement builds a CONFIG_KNOB for one schema value plus, when the
// value has a non-empty description, its inline DocClaim. The location
// points at the property's key node. A nil claim means the value is
// undocumented — a real finding.
func knobElement(relPath, chart, valuePath string, keyNode, schema *yaml.Node) (*contractpb.ContractElement, *docclaimpb.DocClaim) {
	meta := map[string]interface{}{"source": "values.schema.json"}
	if schema != nil && schema.Kind == yaml.MappingNode {
		if t := mapValue(schema, "type"); t != nil && t.Value != "" {
			meta["type"] = t.Value
		}
		if d := mapValue(schema, "default"); d != nil && d.Kind == yaml.ScalarNode && d.Value != "" {
			meta["default"] = d.Value
		}
		if enum := mapValue(schema, "enum"); enum != nil && enum.Kind == yaml.SequenceNode {
			vals := make([]string, 0, len(enum.Content))
			for _, e := range enum.Content {
				vals = append(vals, e.Value)
			}
			if len(vals) > 0 {
				meta["enum"] = strings.Join(vals, ",")
			}
		}
	}
	ecoMeta, _ := structpb.NewStruct(meta)

	id := chart + "." + valuePath
	doc := descriptionOf(schema)
	elem := &contractpb.ContractElement{
		Id:                id,
		Kind:              contractpb.ContractElementKind_CONFIG_KNOB,
		Ecosystem:         ecosystem,
		Library:           chart,
		Location:          &commonpb.SourceLocation{Path: relPath, Line: uint32(keyNode.Line)},
		DocCommentExcerpt: doc,
		EcosystemMeta:     ecoMeta,
	}
	var claim *docclaimpb.DocClaim
	if doc != "" {
		claim = makeInlineDocClaim(id, doc, relPath, uint32(keyNode.Line))
	}
	return elem, claim
}

// valuesKnob builds a CONFIG_KNOB for one values.yaml key plus, when the
// key carries a helm-docs comment, its inline DocClaim.
func valuesKnob(relPath, chart, valuePath string, keyNode, valNode *yaml.Node, doc string) (*contractpb.ContractElement, *docclaimpb.DocClaim) {
	meta := map[string]interface{}{
		"source":   "values.yaml",
		"yamlType": yamlNodeType(valNode),
	}
	if valNode != nil && valNode.Kind == yaml.ScalarNode && valNode.Value != "" {
		meta["default"] = valNode.Value
	}
	ecoMeta, _ := structpb.NewStruct(meta)

	id := chart + "." + valuePath
	elem := &contractpb.ContractElement{
		Id:                id,
		Kind:              contractpb.ContractElementKind_CONFIG_KNOB,
		Ecosystem:         ecosystem,
		Library:           chart,
		Location:          &commonpb.SourceLocation{Path: relPath, Line: uint32(keyNode.Line)},
		DocCommentExcerpt: doc,
		EcosystemMeta:     ecoMeta,
	}
	var claim *docclaimpb.DocClaim
	if doc != "" {
		claim = makeInlineDocClaim(id, doc, relPath, uint32(keyNode.Line))
	}
	return elem, claim
}

// makeInlineDocClaim turns a value's inline documentation (schema
// `description` or helm-docs `# --` comment) into a REFERENCE DocClaim
// attributed to the value's own element. Mirrors the crd adapter's
// inline-description handling: the documentation ships in the contract
// source itself, so the doc-coverage join is free and mechanical.
func makeInlineDocClaim(elemID, doc, p string, line uint32) *docclaimpb.DocClaim {
	wc := len(strings.Fields(doc))
	var substance commonpb.Substance
	switch {
	case wc == 0:
		substance = commonpb.Substance_ABSENT
	case wc <= 4:
		substance = commonpb.Substance_SIGNATURE_ONLY
	case wc <= 19:
		substance = commonpb.Substance_PARTIAL
	default:
		substance = commonpb.Substance_SUBSTANTIVE
	}
	raw := doc
	if len(raw) > 300 {
		raw = raw[:300] + "…"
	}
	return &docclaimpb.DocClaim{
		SourcePath:   p,
		Location:     &commonpb.SourceLocation{Path: p, Line: line},
		RawText:      raw,
		ContractRefs: []string{elemID},
		Substance:    substance,
		WordCount:    uint32(wc),
		Kind:         docclaimpb.DocClaimKind_REFERENCE,
		Adapter:      Name,
	}
}

// descriptionOf returns the trimmed `description` scalar of a schema
// mapping, or "" when absent (a real undocumented finding).
func descriptionOf(schema *yaml.Node) string {
	if schema == nil {
		return ""
	}
	if d := mapValue(schema, "description"); d != nil {
		return strings.TrimSpace(d.Value)
	}
	return ""
}

// ============================================================
// shared yaml.Node helpers
// ============================================================

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

// yamlNodeType returns a coarse type label for a value node (values.yaml
// path), for the element's ecosystem_meta.
func yamlNodeType(n *yaml.Node) string {
	if n == nil {
		return "scalar"
	}
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

// libraryInScope mirrors the crd/k8smanifest/cml scope semantics: empty
// filter means "include everything"; otherwise the chart must match a
// Libraries or AlsoInclude entry and not be excluded. Patterns ending in
// ".*" / "*" match by prefix.
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
