// Package librarysnapshot is the single source of truth for projecting an
// in-memory corpus + findings + config into the generic-map "library
// snapshot" shape. Three consumers share it so they cannot drift apart: the
// in-process render path (utils/scanner), the offline --from-snapshot
// artifact, and the agent-facing MCP server's library_snapshot op
// (internal/mcp). Before this package those projections were maintained by
// hand in two places and silently diverged.
package librarysnapshot

import (
	"encoding/json"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/sheaf-data/sheaf/internal/corpus"
	configpb "github.com/sheaf-data/sheaf/proto/config"
	findingpb "github.com/sheaf-data/sheaf/proto/finding"
)

// SchemaVersion is the version of the library-snapshot envelope + projection
// contract. Bump it whenever this projection changes shape — a new or removed
// top-level field, or a change to how elements/profiles/findings are matched
// and emitted. The inner element/profile/finding maps are protojson dumps of
// the underlying proto messages, so a proto change that consumers must notice
// rides under this same version: bump it then too. A snapshot read back with
// a different SchemaVersion cannot be assumed to render correctly.
const SchemaVersion = 1

// Projection is the set of generic-map slices that make up a library
// snapshot, before a consumer wraps them in its own output shape (the
// scanner.Snapshot struct, or the MCP op's response map). Keeping Project
// returning the components — rather than a final struct — lets each consumer
// own its wire shape (for MCP back-compat) while the drift-prone matching
// logic lives here exactly once.
type Projection struct {
	Elements         []map[string]any
	Profiles         []map[string]any
	Findings         []map[string]any
	Analyzers        []string
	SurfacesRequired []string
}

// Project builds the snapshot projection for one library from an in-memory
// corpus, the run's findings, and the sheaf config — any of which may be nil
// in test builds.
func Project(c *corpus.Corpus, findings []*findingpb.Finding, cfg *configpb.Config, library string) Projection {
	var elements []map[string]any
	var profiles []map[string]any
	if c != nil {
		for _, e := range c.Elements() {
			if e.GetLibrary() != library {
				continue
			}
			elements = append(elements, PBToMap(e))
			if prof := c.Profile(e.GetId()); prof != nil {
				profiles = append(profiles, PBToMap(prof))
			}
		}
	}
	// Findings carry only a Subject string, not a Library field. The
	// per-ecosystem subject shape varies — FIDL dotted paths
	// ("fuchsia.io/Node"), gRPC slash-then-dot ("grpc.health.v1.Health/Check"),
	// cobra space-separated tokens ("kubectl annotate --resource-version") —
	// so accept any separator or an exact match for the library element
	// itself, otherwise cobra-ecosystem libraries silently drop their findings.
	var matched []map[string]any
	for _, f := range findings {
		subj := f.GetSubject()
		if subj != library &&
			!strings.HasPrefix(subj, library+"/") &&
			!strings.HasPrefix(subj, library+".") &&
			!strings.HasPrefix(subj, library+" ") {
			continue
		}
		matched = append(matched, PBToMap(f))
	}
	var analyzers []string
	var surfacesRequired []string
	if cfg != nil {
		for _, a := range cfg.GetAnalyzer() {
			if name := a.GetName(); name != "" {
				analyzers = append(analyzers, name)
			}
		}
		surfacesRequired = append(surfacesRequired, cfg.GetSurfacesRequired()...)
	}
	return Projection{
		Elements:         elements,
		Profiles:         profiles,
		Findings:         matched,
		Analyzers:        analyzers,
		SurfacesRequired: surfacesRequired,
	}
}

// PBToMap marshals a protobuf message through protojson into a generic map,
// so snapshot consumers see the same field shape regardless of which path
// produced the snapshot.
func PBToMap(msg protoreflect.ProtoMessage) map[string]any {
	if msg == nil {
		return nil
	}
	b, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(msg)
	if err != nil {
		return map[string]any{"_error": err.Error()}
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}
