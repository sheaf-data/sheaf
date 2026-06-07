// Package indexer: codegenbridge.go
//
// codegen_bridge is the generic mechanism that lets a config declare
// that contract elements in ecosystem A are the same logical thing as
// elements in ecosystem B, materialized via a deterministic codegen
// tool (pwpb proto→cpp, nanopb, gRPC's own C++ stubs, FlatBuffers→cpp,
// Cap'n Proto→rust, …). The resolver walks every source-ecosystem
// element, renders the target_name_template against that element's
// variables, and emits a SAME_AS edge whenever the rendered target ID
// names an existing element in the target ecosystem.
//
// The mechanism is pure — no project-specific naming rules live here.
// Pigweed's pwpb naming is one config entry; gRPC's C++ codegen is
// another; neither needs an adapter change.
//
// See proto/config.proto::CodegenBridge for the config message and
// proto/contract.proto::RelationshipKind::SAME_AS for the relationship
// kind the indexer materializes from the resolver's output.

package indexer

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	configpb "github.com/sheaf-data/sheaf/proto/config"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

// Bridge is one parsed codegen_bridge config entry.
type Bridge struct {
	Source string             // source ecosystem id (e.g. "proto")
	Target string             // target ecosystem id (e.g. "cpp")
	Tmpl   *template.Template // parsed target_name_template
}

// SameAsEdge is one resolver output. The indexer turns it into a pair
// of bidirectional SAME_AS relationships on the source + target
// elements.
type SameAsEdge struct {
	SourceID string
	TargetID string
	BridgeID int // index into the bridges slice this edge was resolved from
}

// ParseBridges turns config entries into Bridges. Templates are parsed
// (and validated) at config-load time so a bad template fails fast on
// scan start rather than mid-resolve. An entry with a blank source or
// target ecosystem, or a blank template, is rejected.
func ParseBridges(cfg []*configpb.CodegenBridge) ([]Bridge, error) {
	out := make([]Bridge, 0, len(cfg))
	for i, b := range cfg {
		src := strings.TrimSpace(b.GetSourceEcosystem())
		tgt := strings.TrimSpace(b.GetTargetEcosystem())
		tmpl := b.GetTargetNameTemplate()
		if src == "" {
			return nil, fmt.Errorf("codegen_bridges[%d]: source_ecosystem is required", i)
		}
		if tgt == "" {
			return nil, fmt.Errorf("codegen_bridges[%d]: target_ecosystem is required", i)
		}
		if strings.TrimSpace(tmpl) == "" {
			return nil, fmt.Errorf("codegen_bridges[%d]: target_name_template is required", i)
		}
		t, err := template.New(fmt.Sprintf("bridge[%d]", i)).
			Option("missingkey=zero").
			Parse(tmpl)
		if err != nil {
			return nil, fmt.Errorf("codegen_bridges[%d]: parse target_name_template: %w", i, err)
		}
		out = append(out, Bridge{Source: src, Target: tgt, Tmpl: t})
	}
	return out, nil
}

// Resolve walks every source-ecosystem element through each applicable
// bridge and returns the list of SAME_AS edges to add to the graph. An
// edge is only emitted when the rendered target ID exists in the
// target-ecosystem element index. Missing targets are silently dropped
// (a bridge is best-effort, not validated); template render errors are
// also silently dropped so a partially-named element doesn't fail the
// whole resolve.
//
// Self-edges (source == target) are dropped — they're never useful and
// turning them on would just add noise to the relationships list.
func Resolve(bridges []Bridge, elements []*contractpb.ContractElement) []SameAsEdge {
	if len(bridges) == 0 || len(elements) == 0 {
		return nil
	}
	// Bucket elements by ecosystem for O(1) target lookup. Same map
	// keys the source iteration too; one pass over `elements`.
	bySrc := make(map[string][]*contractpb.ContractElement)
	byTgt := make(map[string]map[string]*contractpb.ContractElement)
	for _, e := range elements {
		eco := e.GetEcosystem()
		bySrc[eco] = append(bySrc[eco], e)
		if byTgt[eco] == nil {
			byTgt[eco] = make(map[string]*contractpb.ContractElement)
		}
		byTgt[eco][e.GetId()] = e
	}

	// Pre-compute which bridges are applicable: source ecosystem must
	// have at least one element, target ecosystem must have at least
	// one element. Skip the rest — they can't possibly fire.
	type liveBridge struct {
		idx int
		b   Bridge
	}
	live := make([]liveBridge, 0, len(bridges))
	for i, b := range bridges {
		if len(bySrc[b.Source]) == 0 {
			continue
		}
		if len(byTgt[b.Target]) == 0 {
			continue
		}
		live = append(live, liveBridge{idx: i, b: b})
	}
	if len(live) == 0 {
		return nil
	}

	var out []SameAsEdge
	for _, lb := range live {
		targetIndex := byTgt[lb.b.Target]
		for _, e := range bySrc[lb.b.Source] {
			vars := templateVarsFor(e)
			if vars == nil {
				continue // unknown source ecosystem variable scheme
			}
			var buf bytes.Buffer
			if err := lb.b.Tmpl.Execute(&buf, vars); err != nil {
				continue
			}
			rendered := buf.String()
			if rendered == "" {
				continue
			}
			tgt, ok := targetIndex[rendered]
			if !ok {
				continue
			}
			if tgt.GetId() == e.GetId() {
				continue
			}
			out = append(out, SameAsEdge{
				SourceID: e.GetId(),
				TargetID: tgt.GetId(),
				BridgeID: lb.idx,
			})
		}
	}
	return out
}

// templateVarsFor returns the variable struct exposed to the
// target_name_template for elements of the given source ecosystem. The
// returned value is the data passed to template.Execute; nil means
// "unknown ecosystem — skip". Variable names are the field names on
// the returned struct (Go's text/template lookup rules).
//
// v1 covers proto and fidl. Extend here as new source ecosystems get
// bridge use cases — the resolver itself stays unchanged.
func templateVarsFor(e *contractpb.ContractElement) any {
	switch e.GetEcosystem() {
	case "proto":
		return protoVars(e)
	case "fidl":
		return fidlVars(e)
	default:
		return nil
	}
}

// protoTemplateVars is the variable set exposed to bridge templates
// for proto source elements. See docs/config.md for the worked list.
type protoTemplateVars struct {
	Package      string // dotted ("pw.log")
	PackageCpp   string // colon-form ("pw::log")
	PackageSlash string // slash-form ("pw/log")
	Kind         string // "message" | "service" | "method" | "enum"
	Name         string // local name ("LogEntry")
	Service      string // service name (METHOD only)
	Method       string // method name (METHOD only)
	FullName     string // Package.Name ("pw.log.LogEntry")
}

func protoVars(e *contractpb.ContractElement) protoTemplateVars {
	pkg := e.GetLibrary()
	v := protoTemplateVars{
		Package:      pkg,
		PackageCpp:   strings.ReplaceAll(pkg, ".", "::"),
		PackageSlash: strings.ReplaceAll(pkg, ".", "/"),
		Kind:         protoKindLabel(e),
	}
	// Element IDs from the proto adapter:
	//   service:  "pkg/Service"
	//   method:   "pkg/Service.Method"
	//   message:  "pkg/Message"
	//   enum:     "pkg/Enum"
	local := localAfterSlash(e.GetId())
	switch e.GetKind() {
	case contractpb.ContractElementKind_METHOD:
		svc, method := splitMethodLocal(local)
		v.Service = svc
		v.Method = method
		v.Name = method
	default:
		v.Name = local
	}
	if pkg != "" && v.Name != "" {
		v.FullName = pkg + "." + v.Name
	} else {
		v.FullName = v.Name
	}
	return v
}

func protoKindLabel(e *contractpb.ContractElement) string {
	switch e.GetKind() {
	case contractpb.ContractElementKind_METHOD:
		return "method"
	case contractpb.ContractElementKind_PROTOCOL:
		return "service"
	case contractpb.ContractElementKind_TYPE:
		// Proto adapter emits both messages and enums as TYPE. There's
		// currently no schema hook to distinguish them; default to
		// "message" since that's the dominant case for bridge use.
		// Bridges that need enum-specific naming can switch on .Kind
		// once the adapter learns to mark enums distinctly.
		return "message"
	}
	return ""
}

// fidlTemplateVars is the variable set for fidl source elements.
type fidlTemplateVars struct {
	Library    string // dotted ("fuchsia.io")
	LibraryCpp string // colon-form ("fuchsia::io")
	Kind       string // "protocol" | "method" | "type"
	Name       string // local name ("Directory")
	Protocol   string // protocol name (METHOD only)
	Method     string // method name (METHOD only)
}

func fidlVars(e *contractpb.ContractElement) fidlTemplateVars {
	lib := e.GetLibrary()
	v := fidlTemplateVars{
		Library:    lib,
		LibraryCpp: strings.ReplaceAll(lib, ".", "::"),
		Kind:       fidlKindLabel(e),
	}
	local := localAfterSlash(e.GetId())
	switch e.GetKind() {
	case contractpb.ContractElementKind_METHOD:
		proto, method := splitMethodLocal(local)
		v.Protocol = proto
		v.Method = method
		v.Name = method
	default:
		v.Name = local
	}
	return v
}

func fidlKindLabel(e *contractpb.ContractElement) string {
	switch e.GetKind() {
	case contractpb.ContractElementKind_METHOD:
		return "method"
	case contractpb.ContractElementKind_PROTOCOL:
		return "protocol"
	case contractpb.ContractElementKind_TYPE:
		return "type"
	}
	return ""
}

// localAfterSlash returns the part of an element ID after the final
// "/". For IDs without a slash the whole string is returned.
func localAfterSlash(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// splitMethodLocal splits "Service.Method" (or "Protocol.Method") into
// its two halves. Returns ("", "") when there's no dot.
func splitMethodLocal(local string) (parent, leaf string) {
	dot := strings.LastIndex(local, ".")
	if dot < 0 {
		return "", ""
	}
	return local[:dot], local[dot+1:]
}
