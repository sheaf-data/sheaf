package fidl

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
)

const Name = "fidl"
const Version = "0.1.0"

// Adapter implements adapters.ContractAnchorParser by parsing .fidl
// source files directly (no fidlc required). When fidlc-derived IR
// is available via prebuilt_ir_dir, it should be preferred — that
// path is reserved for a future build pass.
type Adapter struct {
	include []string
	exclude []string
	// urlBase optionally produces canonical fuchsia.dev URLs for emitted
	// ContractElements. Defaults to fuchsia.dev's reference layout.
	urlBase string
}

// Config bundles the adapter-construction inputs.
type Config struct {
	Include []string
	Exclude []string
	URLBase string // e.g. "https://fuchsia.dev/reference/fidl/"
}

// New constructs an Adapter.
func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.fidl"}
	}
	urlBase := cfg.URLBase
	if urlBase == "" {
		urlBase = "https://fuchsia.dev/reference/fidl/"
	}
	return &Adapter{include: include, exclude: cfg.Exclude, urlBase: urlBase}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Discover walks the repo, parses every matching .fidl file, and
// translates the parsed structure into ContractElements.
//
// The adapter also produces inline DocClaims for any `///` comments
// attached to protocols, methods, types, consts, and aliases — those
// are the same comments fidldoc would otherwise render to fuchsia.dev.
// DocClaims are returned via the secondary slice; the orchestrator
// merges them with other doc adapters.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	elems, _, err := a.discover(ctx, repoRoot, scope)
	return elems, err
}

// DiscoverWithDocs runs the same walk but additionally returns the
// inline DocClaims extracted from `///` comments. The orchestrator
// uses this entry point for FIDL specifically.
func (a *Adapter) DiscoverWithDocs(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, error) {
	return a.discover(ctx, repoRoot, scope)
}

func (a *Adapter) discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, error) {
	// Map of library -> []*File so we can build the namespacing right.
	libFiles := make(map[string][]*File)
	err := adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("fidl: read %s: %w", rel, err)
		}
		f, err := Parse(string(body), rel)
		if err != nil {
			// Bad file; skip but don't abort.
			return nil
		}
		if f.Library == "" {
			return nil
		}
		// Apply scope filter.
		if !libraryInScope(f.Library, scope) {
			return nil
		}
		libFiles[f.Library] = append(libFiles[f.Library], f)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	var elems []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim
	for lib, files := range libFiles {
		e, c := a.buildElements(lib, files)
		elems = append(elems, e...)
		claims = append(claims, c...)
	}
	return elems, claims, nil
}

func libraryInScope(lib string, scope adapters.ScopeConfig) bool {
	// exclude takes precedence
	for _, ex := range scope.Exclude {
		if matchLibrary(ex, lib) {
			return false
		}
	}
	// no scope constraints -> include everything
	if len(scope.Libraries) == 0 && len(scope.AlsoInclude) == 0 {
		return true
	}
	for _, l := range scope.Libraries {
		if matchLibrary(l, lib) {
			return true
		}
	}
	for _, l := range scope.AlsoInclude {
		if matchLibrary(l, lib) {
			return true
		}
	}
	return false
}

// matchLibrary supports trailing-wildcard like "fuchsia.driver.*".
func matchLibrary(pattern, lib string) bool {
	if pattern == lib {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(lib, prefix)
	}
	return false
}

// buildElements turns the parsed Files for one library into Contract
// Elements and the inline DocClaims that go with them.
func (a *Adapter) buildElements(lib string, files []*File) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim) {
	var elems []*contractpb.ContractElement
	var claims []*docclaimpb.DocClaim

	for _, f := range files {
		// Protocols → PROTOCOL element + one METHOD per declared method.
		for _, p := range f.Protocols {
			protocolID := lib + "/" + p.Name
			protocolElem := &contractpb.ContractElement{
				Id:        protocolID,
				Kind:      contractpb.ContractElementKind_PROTOCOL,
				Ecosystem: "fidl",
				Library:   lib,
				Location: &commonpb.SourceLocation{
					Path: f.Path,
					Line: p.Line,
				},
				DocCommentExcerpt: p.Doc,
			}
			// COMPOSED_FROM relationships
			for _, c := range p.Composes {
				target := resolveComposeTarget(lib, c.Target)
				protocolElem.Relationships = append(protocolElem.Relationships, &contractpb.Relationship{
					Kind:            contractpb.RelationshipKind_COMPOSED_FROM,
					TargetElementId: target,
					Note:            fmt.Sprintf("composes %s", c.Target),
					DeclarationSite: &commonpb.SourceLocation{Path: f.Path, Line: c.Line},
				})
			}
			// @available on the protocol itself (previously dropped).
			if p.Available != nil {
				protocolElem.VersionConstraints = append(protocolElem.VersionConstraints, availableToVC(p.Available))
			}
			elems = append(elems, protocolElem)
			if p.Doc != "" {
				claims = append(claims, makeInlineDocClaim(protocolID, p.Doc, f.Path, p.Line, a.urlBase, lib))
			}

			// METHODS
			for _, m := range p.Methods {
				methodID := lib + "/" + p.Name + "." + m.Name
				methodElem := &contractpb.ContractElement{
					Id:        methodID,
					Kind:      contractpb.ContractElementKind_METHOD,
					Ecosystem: "fidl",
					Library:   lib,
					Location: &commonpb.SourceLocation{
						Path: f.Path,
						Line: m.Line,
					},
					DocCommentExcerpt: m.Doc,
				}
				// ACCEPTS_TYPE / RETURNS_TYPE
				for _, ref := range m.ParamTypeRefs {
					methodElem.Relationships = append(methodElem.Relationships, &contractpb.Relationship{
						Kind:            contractpb.RelationshipKind_ACCEPTS_TYPE,
						TargetElementId: lib + "/" + ref,
						Note:            "method param",
					})
				}
				for _, ref := range m.ResultTypeRefs {
					methodElem.Relationships = append(methodElem.Relationships, &contractpb.Relationship{
						Kind:            contractpb.RelationshipKind_RETURNS_TYPE,
						TargetElementId: lib + "/" + ref,
						Note:            "method result",
					})
				}
				// @available → VersionConstraint
				if m.Available != nil {
					methodElem.VersionConstraints = append(methodElem.VersionConstraints, availableToVC(m.Available))
				}
				elems = append(elems, methodElem)
				if m.Doc != "" {
					claims = append(claims, makeInlineDocClaim(methodID, m.Doc, f.Path, m.Line, a.urlBase, lib))
				}
			}
		}

		// Types → TYPE element.
		for _, t := range f.Types {
			typeID := lib + "/" + t.Name
			elem := &contractpb.ContractElement{
				Id:                typeID,
				Kind:              contractpb.ContractElementKind_TYPE,
				Ecosystem:         "fidl",
				Library:           lib,
				Location:          &commonpb.SourceLocation{Path: f.Path, Line: t.Line},
				DocCommentExcerpt: t.Doc,
			}
			if t.Available != nil {
				elem.VersionConstraints = append(elem.VersionConstraints, availableToVC(t.Available))
			}
			elems = append(elems, elem)
			if t.Doc != "" {
				claims = append(claims, makeInlineDocClaim(typeID, t.Doc, f.Path, t.Line, a.urlBase, lib))
			}
		}

		// Constants and aliases are kept as TYPE-kind elements with
		// a distinguishing meta field. Cheap and lets analyzers find
		// stale or undocumented constants without a new schema field.
		for _, c := range f.Constants {
			id := lib + "/" + c.Name
			elem := &contractpb.ContractElement{
				Id:                id,
				Kind:              contractpb.ContractElementKind_TYPE,
				Ecosystem:         "fidl",
				Library:           lib,
				Location:          &commonpb.SourceLocation{Path: f.Path, Line: c.Line},
				DocCommentExcerpt: c.Doc,
			}
			elems = append(elems, elem)
			if c.Doc != "" {
				claims = append(claims, makeInlineDocClaim(id, c.Doc, f.Path, c.Line, a.urlBase, lib))
			}
		}
		for _, al := range f.Aliases {
			id := lib + "/" + al.Name
			elem := &contractpb.ContractElement{
				Id:                id,
				Kind:              contractpb.ContractElementKind_TYPE,
				Ecosystem:         "fidl",
				Library:           lib,
				Location:          &commonpb.SourceLocation{Path: f.Path, Line: al.Line},
				DocCommentExcerpt: al.Doc,
			}
			elems = append(elems, elem)
			if al.Doc != "" {
				claims = append(claims, makeInlineDocClaim(id, al.Doc, f.Path, al.Line, a.urlBase, lib))
			}
		}
	}

	// Synthesize a LIBRARY-kind element when any file in this library
	// carries a library-level @available annotation. The element ID is
	// the bare library name (no "/"), which is otherwise never used as
	// a real element ID. Downstream consumers (scanner, report) look
	// this up to render library-level deprecation banners.
	for _, f := range files {
		if f.LibraryAvailable == nil {
			continue
		}
		elems = append(elems, &contractpb.ContractElement{
			Id:        lib, // bare library name = library-scoped record
			Kind:      contractpb.ContractElementKind_LIBRARY,
			Ecosystem: "fidl",
			Library:   lib,
			Location: &commonpb.SourceLocation{
				Path: f.Path,
				Line: 1, // best we can do without recording the annotation's own line
			},
			VersionConstraints: []*contractpb.VersionConstraint{
				availableToVC(f.LibraryAvailable),
			},
		})
		break // only emit one LIBRARY element per library (first file wins)
	}
	return elems, claims
}

// resolveComposeTarget normalizes a `compose X` target into an
// element ID. If X is already qualified (`lib.path/Name`), it's
// returned verbatim; otherwise the local library is prepended.
func resolveComposeTarget(currentLib, target string) string {
	if strings.Contains(target, "/") {
		return target
	}
	// Some Fuchsia FIDL uses `compose fuchsia.unknown.Cloneable;` style.
	// If the bare token contains a dot, treat the rightmost slug as
	// the protocol and the rest as the library.
	if i := strings.LastIndex(target, "."); i >= 0 {
		// Distinguish "fuchsia.unknown.Cloneable" (library.proto) from
		// just a multi-part library reference. Heuristic: if the last
		// segment starts uppercase, it's a protocol name.
		last := target[i+1:]
		if last != "" && last[0] >= 'A' && last[0] <= 'Z' {
			return target[:i] + "/" + last
		}
	}
	return currentLib + "/" + target
}

func availableToVC(a *Available) *contractpb.VersionConstraint {
	return &contractpb.VersionConstraint{
		Ecosystem:  "fidl",
		Added:      a.Added,
		Deprecated: a.Deprecated,
		Removed:    a.Removed,
		Note:       a.Note,
	}
}

func makeInlineDocClaim(elemID, doc, path string, line uint32, urlBase, lib string) *docclaimpb.DocClaim {
	wc := countDocWords(doc)
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
	// Canonical URL: convention used by fidldoc on fuchsia.dev.
	url := urlBase + lib
	if i := strings.Index(elemID, "/"); i >= 0 {
		rest := elemID[i+1:]
		url = urlBase + lib + "#" + rest
	}
	return &docclaimpb.DocClaim{
		SourcePath:   path,
		Location:     &commonpb.SourceLocation{Path: path, Line: line},
		RawText:      truncate(doc, 300),
		ContractRefs: []string{elemID},
		Url:          url,
		Substance:    substance,
		WordCount:    uint32(wc),
		Kind:         docclaimpb.DocClaimKind_REFERENCE,
		Adapter:      Name,
	}
}

func countDocWords(s string) int {
	return len(strings.Fields(s))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
