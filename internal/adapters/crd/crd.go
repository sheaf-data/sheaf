// Package crd implements a contract-anchor adapter for Kubernetes
// CustomResourceDefinition (CRD) YAML. It walks each CRD's
// spec.versions[].schema.openAPIV3Schema recursively and emits one
// ContractElement per CRD kind, per schema field, and per API group.
//
// The doc join is free: an OpenAPI schema's `description` fields are
// written inline (kubebuilder generates them from Go doc comments), so
// each field carries its own documentation in the same file. A field
// with an empty description is a real "undocumented" finding — this
// adapter leaves DocCommentExcerpt blank rather than synthesizing one.
//
// This adapter is purely mechanical: it parses YAML, no LLM, no cluster
// access, no template rendering. Every emitted element sets Location
// (file:line) into the actual CRD YAML, derived from yaml.Node line
// numbers, so provenance is exact.
//
// Each emitted ContractElement has:
//   - the CRD kind  → Kind = TYPE,        Id = "<group>/<Kind>"
//   - each field    → Kind = CONFIG_KNOB, Id = "<group>/<Kind>.<dotted.path>"
//   - the API group → Kind = LIBRARY,     Id = "<group>"
//   - Ecosystem = "crd" (NEVER "kubernetes")
//   - Library = the CRD's spec.group (the API group as a synthetic whole)
//   - Location pointing at the line of the schema node in the source YAML
//   - EcosystemMeta carrying the field's OpenAPI type and (for the TYPE
//     element) the resource's group/kind/scope.
package crd

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
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	"google.golang.org/protobuf/types/known/structpb"
)

const Name = "crd"
const Version = "0.1.0"

const ecosystem = "crd"

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

// Discover walks the repo for YAML files, parses every
// CustomResourceDefinition document it finds (a single file may hold
// several, separated by `---`), and emits ContractElements for the
// group, each kind, and each schema field.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	elems, _, err := a.discover(ctx, repoRoot, scope)
	return elems, err
}

// DiscoverWithDocs runs the same walk but additionally returns the
// inline DocClaims extracted from each schema field's OpenAPI
// `description`. The orchestrator routes through this entry point so the
// CRD's own inline docs feed the doc-coverage join — a field with a
// real description counts as documented; a field with no description is
// a true undocumented finding. No external doc source is consulted.
func (a *Adapter) DiscoverWithDocs(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, error) {
	return a.discover(ctx, repoRoot, scope)
}

func (a *Adapter) discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	// Group elements are synthesized once per group across all files.
	groups := map[string]*contractpb.ContractElement{}
	var out []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim

	err := adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return nil // skip unreadable file; not a hard error
		}
		elems, cls, grpElems := parseCRDFile(rel, body, scope)
		out = append(out, elems...)
		claims = append(claims, cls...)
		for g, e := range grpElems {
			if _, seen := groups[g]; !seen {
				groups[g] = e
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("crd: walk repo: %w", err)
	}

	for _, e := range groups {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetId() < out[j].GetId() })
	return out, claims, nil
}

// parseCRDFile decodes every YAML document in body, keeps the ones whose
// kind is CustomResourceDefinition, and emits elements + inline
// DocClaims. The third return value maps each API group seen to its
// synthetic LIBRARY element (deduplicated across files by the caller).
func parseCRDFile(relPath string, body []byte, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, map[string]*contractpb.ContractElement) {
	var out []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim
	groups := map[string]*contractpb.ContractElement{}

	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			break // EOF or a malformed trailing doc; stop cleanly
		}
		root := documentRoot(&doc)
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}
		if !isCRD(root) {
			continue
		}
		crdElems, crdClaims, group, groupLine := parseCRD(relPath, root, scope)
		if group == "" {
			continue
		}
		out = append(out, crdElems...)
		claims = append(claims, crdClaims...)
		if _, ok := groups[group]; !ok {
			groups[group] = &contractpb.ContractElement{
				Id:        group,
				Kind:      contractpb.ContractElementKind_LIBRARY,
				Ecosystem: ecosystem,
				Library:   group,
				Location:  &commonpb.SourceLocation{Path: relPath, Line: uint32(groupLine)},
			}
		}
	}
	return out, claims, groups
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

// isCRD reports whether a top-level mapping is a CustomResourceDefinition.
func isCRD(root *yaml.Node) bool {
	if k := mapValue(root, "kind"); k != nil {
		return strings.TrimSpace(k.Value) == "CustomResourceDefinition"
	}
	return false
}

// parseCRD emits the TYPE element for each kind/version and a CONFIG_KNOB
// per schema field, plus an inline DocClaim for every element carrying a
// non-empty description. It returns the elements, the claims, the
// resolved API group, and the source line of the group declaration (for
// the LIBRARY element).
func parseCRD(relPath string, root *yaml.Node, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, string, int) {
	spec := mapValue(root, "spec")
	if spec == nil || spec.Kind != yaml.MappingNode {
		return nil, nil, "", 0
	}
	groupNode := mapValue(spec, "group")
	if groupNode == nil {
		return nil, nil, "", 0
	}
	group := strings.TrimSpace(groupNode.Value)
	if group == "" {
		return nil, nil, "", 0
	}
	if !libraryInScope(group, scope) {
		return nil, nil, "", 0
	}

	scopeKind := "" // Namespaced / Cluster
	kind := ""
	if names := mapValue(spec, "names"); names != nil {
		if kn := mapValue(names, "kind"); kn != nil {
			kind = strings.TrimSpace(kn.Value)
		}
	}
	if sn := mapValue(spec, "scope"); sn != nil {
		scopeKind = strings.TrimSpace(sn.Value)
	}
	if kind == "" {
		return nil, nil, "", 0
	}

	versions := mapValue(spec, "versions")
	if versions == nil || versions.Kind != yaml.SequenceNode {
		return nil, nil, group, groupNode.Line
	}

	var out []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim
	// A CRD may declare several served versions, each with its own
	// schema. Emit one TYPE per (kind, version) and namespace fields by
	// version so v1beta1 and v1 don't collide.
	multiVersion := len(versions.Content) > 1
	for _, ver := range versions.Content {
		if ver.Kind != yaml.MappingNode {
			continue
		}
		verName := ""
		if vn := mapValue(ver, "name"); vn != nil {
			verName = strings.TrimSpace(vn.Value)
		}
		schema := schemaNode(ver)
		if schema == nil {
			continue
		}

		typeID := group + "/" + kind
		fieldPrefix := kind
		if multiVersion && verName != "" {
			typeID = group + "/" + kind + "@" + verName
			fieldPrefix = kind + "@" + verName
		}

		typeMeta := map[string]interface{}{
			"group": group,
			"kind":  kind,
		}
		if verName != "" {
			typeMeta["version"] = verName
		}
		if scopeKind != "" {
			typeMeta["scope"] = scopeKind
		}
		typeMetaStruct, _ := structpb.NewStruct(typeMeta)
		typeDoc := descriptionOf(schema)
		out = append(out, &contractpb.ContractElement{
			Id:                typeID,
			Kind:              contractpb.ContractElementKind_TYPE,
			Ecosystem:         ecosystem,
			Library:           group,
			Location:          &commonpb.SourceLocation{Path: relPath, Line: uint32(schema.Line)},
			DocCommentExcerpt: typeDoc,
			EcosystemMeta:     typeMetaStruct,
		})
		if typeDoc != "" {
			claims = append(claims, makeInlineDocClaim(typeID, typeDoc, relPath, uint32(schema.Line)))
		}

		fieldElems, fieldClaims := walkSchema(relPath, group, fieldPrefix, "", schema)
		out = append(out, fieldElems...)
		claims = append(claims, fieldClaims...)
	}
	return out, claims, group, groupNode.Line
}

// schemaNode returns the openAPIV3Schema mapping node for a version, or
// nil if absent.
func schemaNode(ver *yaml.Node) *yaml.Node {
	sc := mapValue(ver, "schema")
	if sc == nil {
		return nil
	}
	return mapValue(sc, "openAPIV3Schema")
}

// walkSchema recursively emits a CONFIG_KNOB per field in an OpenAPI
// schema mapping, plus an inline DocClaim per field carrying a non-empty
// description. dotted is the field path accumulated so far (empty at the
// root). It descends into object `properties` and array `items`.
func walkSchema(relPath, group, kindPrefix, dotted string, schema *yaml.Node) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim) {
	if schema == nil || schema.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim

	props := mapValue(schema, "properties")
	if props != nil && props.Kind == yaml.MappingNode {
		// properties is a mapping of fieldName -> sub-schema; key/value
		// pairs alternate in Content.
		for i := 0; i+1 < len(props.Content); i += 2 {
			keyNode := props.Content[i]
			valNode := props.Content[i+1]
			fieldName := keyNode.Value
			path := fieldName
			if dotted != "" {
				path = dotted + "." + fieldName
			}
			elem, claim := fieldElement(relPath, group, kindPrefix, path, keyNode, valNode)
			out = append(out, elem)
			if claim != nil {
				claims = append(claims, claim)
			}
			subElems, subClaims := walkSchema(relPath, group, kindPrefix, path, valNode)
			out = append(out, subElems...)
			claims = append(claims, subClaims...)
		}
	}

	// Array element schema: descend, keeping the same dotted path with a
	// [] marker so `spec.containers[].name` reads naturally.
	if items := mapValue(schema, "items"); items != nil && items.Kind == yaml.MappingNode {
		itemPath := dotted
		if itemPath != "" {
			itemPath += "[]"
		}
		subElems, subClaims := walkSchema(relPath, group, kindPrefix, itemPath, items)
		out = append(out, subElems...)
		claims = append(claims, subClaims...)
	}

	return out, claims
}

// fieldElement builds a CONFIG_KNOB element for one schema field plus,
// when the field has a non-empty description, its inline DocClaim. The
// location points at the field's key node (its declaration line). A nil
// claim means the field is undocumented — a real finding.
func fieldElement(relPath, group, kindPrefix, path string, keyNode, valNode *yaml.Node) (*contractpb.ContractElement, *docclaimpb.DocClaim) {
	meta := map[string]interface{}{}
	if t := mapValue(valNode, "type"); t != nil && t.Value != "" {
		meta["type"] = t.Value
	}
	if f := mapValue(valNode, "format"); f != nil && f.Value != "" {
		meta["format"] = f.Value
	}
	if enum := mapValue(valNode, "enum"); enum != nil && enum.Kind == yaml.SequenceNode {
		vals := make([]string, 0, len(enum.Content))
		for _, e := range enum.Content {
			vals = append(vals, e.Value)
		}
		if len(vals) > 0 {
			meta["enum"] = strings.Join(vals, ",")
		}
	}
	ecoMeta, _ := structpb.NewStruct(meta)

	id := group + "/" + kindPrefix + "." + path
	doc := descriptionOf(valNode)
	elem := &contractpb.ContractElement{
		Id:                id,
		Kind:              contractpb.ContractElementKind_CONFIG_KNOB,
		Ecosystem:         ecosystem,
		Library:           group,
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

// makeInlineDocClaim turns a schema field's inline OpenAPI `description`
// into a REFERENCE DocClaim attributed to the field's own element. This
// mirrors the FIDL adapter's inline-`///` handling: the documentation
// ships in the contract source itself, so the doc-coverage join is free
// and mechanical. Substance buckets follow the same word-count tiers the
// FIDL adapter uses.
func makeInlineDocClaim(elemID, doc, path string, line uint32) *docclaimpb.DocClaim {
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
		SourcePath:   path,
		Location:     &commonpb.SourceLocation{Path: path, Line: line},
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
	if d := mapValue(schema, "description"); d != nil {
		return strings.TrimSpace(d.Value)
	}
	return ""
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

// libraryInScope mirrors the cml/cobra scope semantics: empty filter
// means "include everything"; otherwise the group must match a Libraries
// or AlsoInclude entry and not be excluded. Patterns ending in ".*" match
// by prefix.
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
