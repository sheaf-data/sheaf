// Package proto implements a contract-anchor adapter for
// protobuf/gRPC. Input is the repo's .proto source files; output is
// one PROTOCOL ContractElement per service, one METHOD per rpc, and
// one TYPE per top-level message or enum.
//
// Contract IDs use the "<package>/<Service>.<Method>" form, matching
// the slash/dot convention the fidl adapter uses. Library = the
// proto package (e.g. "grpc.health.v1").
//
// The adapter does not parse .proto text on its own — it shells out
// to `protoc --include_source_info --descriptor_set_out=…` and walks
// the resulting FileDescriptorSet via google.golang.org/protobuf/types
// /descriptorpb. protoc is the canonical reference for what's a
// valid proto, ships its own google/protobuf well-knowns, and is
// universally available in proto users' dev environments — so the
// shell-out costs less than maintaining a parser.
package proto

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
)

const Name = "proto"
const Version = "0.1.0"

// Descriptor-proto field numbers we index into SourceCodeInfo.Location.Path.
// See https://protobuf.dev/reference/protobuf/google.protobuf/#source-code-info
const (
	fdpMessageTypeField = 4 // FileDescriptorProto.message_type
	fdpEnumTypeField    = 5 // FileDescriptorProto.enum_type
	fdpServiceField     = 6 // FileDescriptorProto.service
	sdpMethodField      = 2 // ServiceDescriptorProto.method
)

type Adapter struct {
	protocPath string
	include    []string
	exclude    []string
	protoPath  []string // -I directories (repo-relative or absolute)
}

type Config struct {
	ProtocPath string
	Include    []string
	Exclude    []string
	ProtoPath  []string
}

func New(cfg Config) *Adapter {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.proto"}
	}
	return &Adapter{
		protocPath: cfg.ProtocPath,
		include:    include,
		exclude:    cfg.Exclude,
		protoPath:  cfg.ProtoPath,
	}
}

func (a *Adapter) Name() string    { return Name }
func (a *Adapter) Version() string { return Version }

// Discover walks the repo for matching .proto files, runs protoc on
// them all in one invocation, and emits ContractElements for the
// services / methods / messages / enums each user-supplied file
// declares. Transitive imports (well-knowns, deps) are pulled in for
// type resolution but their contents are NOT emitted as elements.
func (a *Adapter) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	absRepo, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("proto: resolve repo: %w", err)
	}

	// Collect user-supplied input files (repo-relative).
	var inputs []string
	err = adapters.WalkMatching(repoRoot, a.include, a.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		inputs = append(inputs, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("proto: walk repo: %w", err)
	}
	if len(inputs) == 0 {
		return nil, nil
	}

	// Resolve -I include paths to absolute. Default to repo root when
	// the user supplied none.
	var includeDirs []string
	if len(a.protoPath) == 0 {
		includeDirs = []string{absRepo}
	} else {
		for _, p := range a.protoPath {
			if !filepath.IsAbs(p) {
				p = filepath.Join(absRepo, p)
			}
			includeDirs = append(includeDirs, p)
		}
	}

	fds, err := a.runProtoc(ctx, absRepo, inputs, includeDirs)
	if err != nil {
		return nil, err
	}

	// Build the set of "user-supplied files" as protoc-normalized names
	// (one of the include-dir prefixes stripped off the repo-relative
	// path). That set is what filters out transitive imports during the
	// element-emission walk below.
	userFiles := normalizeInputs(inputs, includeDirs, absRepo)

	// Build a fully-qualified-name → (package, simple_name) map for
	// top-level messages and enums, used to translate input_type /
	// output_type references like ".grpc.health.v1.HealthCheckRequest"
	// into ContractElement IDs.
	typeIndex := buildTypeIndex(fds)

	var elems []*contractpb.ContractElement
	for _, f := range fds.File {
		fname := f.GetName()
		repoRel, ok := userFiles[fname]
		if !ok {
			continue // transitive import; skip
		}
		pkg := f.GetPackage()
		if !packageInScope(pkg, scope) {
			continue
		}
		docs := buildDocIndex(f.GetSourceCodeInfo())
		startLines := buildStartLineIndex(f.GetSourceCodeInfo())

		// Services + methods. Each element carries the slash-form ID
		// (e.g. "grpc.health.v1/Health.Check") and the dotted FQDN
		// (e.g. "grpc.health.v1.Health.Check") as an alias. proto
		// docs and code conventionally use the dotted form; aliases
		// let the indexer's doc-matcher and test-matcher join either
		// notation to the canonical element.
		for si, svc := range f.Service {
			svcID := elemID(pkg, svc.GetName())
			svcLine := startLines[pathKey(fdpServiceField, int32(si))]
			elems = append(elems, &contractpb.ContractElement{
				Id:        svcID,
				Kind:      contractpb.ContractElementKind_PROTOCOL,
				Ecosystem: Name,
				Library:   pkg,
				Location: &commonpb.SourceLocation{
					Path: repoRel,
					Line: svcLine,
				},
				DocCommentExcerpt: docs[pathKey(fdpServiceField, int32(si))],
				Aliases:           protoAliases(pkg, svc.GetName()),
			})
			for mi, m := range svc.Method {
				methodID := svcID + "." + m.GetName()
				methodLine := startLines[pathKey(fdpServiceField, int32(si), sdpMethodField, int32(mi))]
				doc := docs[pathKey(fdpServiceField, int32(si), sdpMethodField, int32(mi))]
				if doc == "" {
					doc = synthRPCSig(m)
				}
				elem := &contractpb.ContractElement{
					Id:        methodID,
					Kind:      contractpb.ContractElementKind_METHOD,
					Ecosystem: Name,
					Library:   pkg,
					Location: &commonpb.SourceLocation{
						Path: repoRel,
						Line: methodLine,
					},
					DocCommentExcerpt: doc,
					Aliases:           protoAliases(pkg, svc.GetName()+"."+m.GetName()),
				}
				if id := typeIndex[trimLeadingDot(m.GetInputType())]; id != "" {
					note := "rpc input"
					if m.GetClientStreaming() {
						note = "rpc input (client streaming)"
					}
					elem.Relationships = append(elem.Relationships, &contractpb.Relationship{
						Kind:            contractpb.RelationshipKind_ACCEPTS_TYPE,
						TargetElementId: id,
						Note:            note,
					})
				}
				if id := typeIndex[trimLeadingDot(m.GetOutputType())]; id != "" {
					note := "rpc output"
					if m.GetServerStreaming() {
						note = "rpc output (server streaming)"
					}
					elem.Relationships = append(elem.Relationships, &contractpb.Relationship{
						Kind:            contractpb.RelationshipKind_RETURNS_TYPE,
						TargetElementId: id,
						Note:            note,
					})
				}
				elems = append(elems, elem)
			}
		}

		// Top-level messages.
		for mi, msg := range f.MessageType {
			id := elemID(pkg, msg.GetName())
			line := startLines[pathKey(fdpMessageTypeField, int32(mi))]
			elems = append(elems, &contractpb.ContractElement{
				Id:        id,
				Kind:      contractpb.ContractElementKind_TYPE,
				Ecosystem: Name,
				Library:   pkg,
				Location: &commonpb.SourceLocation{
					Path: repoRel,
					Line: line,
				},
				DocCommentExcerpt: docs[pathKey(fdpMessageTypeField, int32(mi))],
				Aliases:           protoAliases(pkg, msg.GetName()),
			})
		}
		// Top-level enums.
		for ei, en := range f.EnumType {
			id := elemID(pkg, en.GetName())
			line := startLines[pathKey(fdpEnumTypeField, int32(ei))]
			elems = append(elems, &contractpb.ContractElement{
				Id:        id,
				Kind:      contractpb.ContractElementKind_TYPE,
				Ecosystem: Name,
				Library:   pkg,
				Location: &commonpb.SourceLocation{
					Path: repoRel,
					Line: line,
				},
				DocCommentExcerpt: docs[pathKey(fdpEnumTypeField, int32(ei))],
				Aliases:           protoAliases(pkg, en.GetName()),
			})
		}
	}
	return elems, nil
}

// runProtoc invokes protoc against the chosen include paths and
// input files, returning the parsed FileDescriptorSet.
func (a *Adapter) runProtoc(ctx context.Context, absRepo string, inputsRepoRel []string, includeDirs []string) (*descriptorpb.FileDescriptorSet, error) {
	protocPath := a.protocPath
	if protocPath == "" {
		p, err := exec.LookPath("protoc")
		if err != nil {
			return nil, fmt.Errorf("proto: protoc not on PATH and no protoc_path configured: %w", err)
		}
		protocPath = p
	}

	tmp, err := os.CreateTemp("", "sheaf-proto-adapter-*.pb")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	args := []string{
		"--include_imports",
		"--include_source_info",
		"--descriptor_set_out=" + tmp.Name(),
	}
	for _, d := range includeDirs {
		args = append(args, "-I"+d)
	}
	for _, in := range inputsRepoRel {
		args = append(args, filepath.Join(absRepo, in))
	}
	cmd := exec.CommandContext(ctx, protocPath, args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("proto: protoc: %w", err)
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &fds); err != nil {
		return nil, fmt.Errorf("proto: decode FileDescriptorSet: %w", err)
	}
	return &fds, nil
}

// normalizeInputs maps each protoc-recorded file name to the
// original repo-relative path of the matching user-supplied input.
// Used to filter out transitive imports during element emission and
// to attach correct SourceLocation.path values.
//
// protoc records FileDescriptorProto.name as the input path with
// the FIRST matching -I prefix stripped off (left-to-right in argv
// order). So for each input we register every possible normalized
// form (one per -I that contains it) — the eventual lookup
// `userFiles[f.GetName()]` then hits regardless of which prefix
// protoc chose. Cheaper than predicting protoc's order semantics
// exactly, and robust if protoc ever changes them.
func normalizeInputs(inputs, includeDirs []string, absRepo string) map[string]string {
	out := make(map[string]string, len(inputs)*2)
	absIncludes := make([]string, 0, len(includeDirs))
	for _, d := range includeDirs {
		dAbs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		absIncludes = append(absIncludes, dAbs)
	}
	for _, rel := range inputs {
		abs := filepath.Join(absRepo, rel)
		matched := false
		for _, dAbs := range absIncludes {
			if abs == dAbs {
				continue
			}
			if strings.HasPrefix(abs, dAbs+string(filepath.Separator)) {
				normalized := filepath.ToSlash(strings.TrimPrefix(abs, dAbs+string(filepath.Separator)))
				out[normalized] = rel
				matched = true
			}
		}
		if !matched {
			out[filepath.ToSlash(rel)] = rel
		}
	}
	return out
}

// packageInScope respects ScopeConfig.Libraries / AlsoInclude /
// Exclude. Matches operate on the proto `package` string.
func packageInScope(pkg string, scope adapters.ScopeConfig) bool {
	for _, ex := range scope.Exclude {
		if matchLibrary(ex, pkg) {
			return false
		}
	}
	if len(scope.Libraries) == 0 && len(scope.AlsoInclude) == 0 {
		return true
	}
	for _, l := range scope.Libraries {
		if matchLibrary(l, pkg) {
			return true
		}
	}
	for _, l := range scope.AlsoInclude {
		if matchLibrary(l, pkg) {
			return true
		}
	}
	return false
}

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

// buildTypeIndex walks every file in the descriptor set and returns
// a map of fully-qualified type name → ContractElement ID for every
// top-level message and enum. Nested types are skipped; references
// to nested types resolve to empty (no relationship emitted) which
// is the safer failure mode.
func buildTypeIndex(fds *descriptorpb.FileDescriptorSet) map[string]string {
	out := map[string]string{}
	for _, f := range fds.File {
		pkg := f.GetPackage()
		for _, m := range f.MessageType {
			fq := pkg + "." + m.GetName()
			out[fq] = elemID(pkg, m.GetName())
		}
		for _, e := range f.EnumType {
			fq := pkg + "." + e.GetName()
			out[fq] = elemID(pkg, e.GetName())
		}
	}
	return out
}

// buildDocIndex maps SourceCodeInfo path (joined by dots) to the
// trimmed first line of the leading comment at that descriptor path.
// The same key shape (pathKey) is used to look up doc text per
// element class below.
func buildDocIndex(sci *descriptorpb.SourceCodeInfo) map[string]string {
	out := map[string]string{}
	if sci == nil {
		return out
	}
	for _, loc := range sci.Location {
		c := strings.TrimSpace(loc.GetLeadingComments())
		if c == "" {
			continue
		}
		if i := strings.IndexByte(c, '\n'); i >= 0 {
			c = strings.TrimSpace(c[:i])
		}
		if len(c) > 200 {
			c = c[:200] + "…"
		}
		out[pathKeyFromSlice(loc.Path)] = c
	}
	return out
}

// buildStartLineIndex maps SourceCodeInfo path → 1-based start line
// of the spanned declaration. Location.Span is encoded as
// [start_line, start_col, end_line] (3 ints) or
// [start_line, start_col, end_line, end_col] (4 ints), zero-indexed.
func buildStartLineIndex(sci *descriptorpb.SourceCodeInfo) map[string]uint32 {
	out := map[string]uint32{}
	if sci == nil {
		return out
	}
	for _, loc := range sci.Location {
		if len(loc.Span) < 3 {
			continue
		}
		// Span[0] is the 0-based start line.
		line := uint32(loc.Span[0]) + 1
		key := pathKeyFromSlice(loc.Path)
		// Only set if absent; the first (most-specific) entry wins.
		if _, ok := out[key]; !ok {
			out[key] = line
		}
	}
	return out
}

func elemID(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "/" + name
}

// protoAliases returns the alternative ID forms a proto element
// might appear under in docs and tests. Currently:
//   - the fully-qualified dotted form ("grpc.health.v1.Health.Check"),
//     which is the canonical way proto docs and protoc errors name
//     elements;
//   - the bare local form ("Health.Check"), which is how single-package
//     docs typically refer to their own service.
//
// The bare form is the same suffix-fuzzy form the indexer's
// docClaimRefsElement already matches against, but adding it as an
// explicit alias also covers test-case matching (Strategy 1).
func protoAliases(pkg, localName string) []string {
	if pkg == "" {
		return []string{localName}
	}
	return []string{pkg + "." + localName, localName}
}

func pathKey(parts ...int32) string {
	return pathKeyFromSlice(parts)
}

func pathKeyFromSlice(parts []int32) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteByte('.')
		}
		fmt.Fprintf(&b, "%d", p)
	}
	return b.String()
}

// synthRPCSig produces a short "(InputType) returns (OutputType)"
// fallback used when a method has no leading comment. The signature
// alone disambiguates similarly-named methods across services
// without sending the reader hunting in the source.
func synthRPCSig(m *descriptorpb.MethodDescriptorProto) string {
	in := trimLeadingDot(m.GetInputType())
	out := trimLeadingDot(m.GetOutputType())
	clientStream := ""
	serverStream := ""
	if m.GetClientStreaming() {
		clientStream = "stream "
	}
	if m.GetServerStreaming() {
		serverStream = "stream "
	}
	return fmt.Sprintf("(%s%s) returns (%s%s)", clientStream, in, serverStream, out)
}

func trimLeadingDot(s string) string {
	return strings.TrimPrefix(s, ".")
}
