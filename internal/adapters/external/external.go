// Package external is the host side of the runtime-adapter feature: it
// adapts an out-of-process plugin (any executable that speaks the
// adapter-plugin protocol over stdio — see internal/adapterplugin and
// docs/adapter-protocol.md) into the in-process adapter interfaces the
// orchestrator already consumes.
//
// One configured plugin maps to one role, chosen by which adapter-role
// block it appears under in sheaf.textproto (test_parser, doc_parser,
// contract_anchor, rendered_reference, or implements_map). Each role has
// a thin wrapper here that spawns the plugin once per Discover call,
// performs the one-shot request/response exchange, and returns the rows
// for that role. A plugin failure (missing binary, non-zero exit,
// protocol violation, or a populated response error) is returned to the
// orchestrator, which records it as a non-fatal per-adapter error —
// exactly as it treats an in-process adapter that errors.
package external

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sheaf-data/sheaf/internal/adapterplugin"
	"github.com/sheaf-data/sheaf/internal/adapters"
	pluginpb "github.com/sheaf-data/sheaf/proto/adapterplugin"
	contractpb "github.com/sheaf-data/sheaf/proto/contract"
	docclaimpb "github.com/sheaf-data/sheaf/proto/docclaim"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

// DefaultTimeout bounds a single plugin Discover call when the config
// does not set one.
const DefaultTimeout = 60 * time.Second

// Config is the host-side view of a configured external adapter,
// projected by the orchestrator from configpb.ExternalAdapterConfig.
type Config struct {
	Command string
	Args    []string
	Include []string
	Exclude []string
	Option  map[string]string
	Timeout time.Duration
	Name    string // provenance/display name; defaults to basename(Command)
}

// client performs the spawn + one-shot round-trip. Shared by every role
// wrapper; safe for concurrent Discover calls because each call spawns
// its own process and owns its own buffers.
type client struct {
	name    string
	command string
	args    []string
	cfg     *pluginpb.AdapterConfig
	timeout time.Duration

	verOnce sync.Once
	ver     string
}

func newClient(cfg Config) (*client, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("external: command is required")
	}
	name := cfg.Name
	if name == "" {
		name = filepath.Base(cfg.Command)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &client{
		name:    name,
		command: cfg.Command,
		args:    cfg.Args,
		cfg: &pluginpb.AdapterConfig{
			Include: cfg.Include,
			Exclude: cfg.Exclude,
			Option:  cfg.Option,
		},
		timeout: timeout,
	}, nil
}

// discover spawns the plugin, writes one DiscoverRequest for the given
// role to its stdin, and reads one DiscoverResponse from its stdout.
func (c *client) discover(ctx context.Context, role pluginpb.Role, repoPath string, scope adapters.ScopeConfig) (*pluginpb.DiscoverResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pluginpb.DiscoverRequest{
		ProtocolVersion: adapterplugin.ProtocolVersion,
		Role:            role,
		RepoPath:        repoPath,
		Scope:           adapterplugin.ScopeToProto(scope),
		Config:          c.cfg,
	}
	var reqBuf bytes.Buffer
	if err := adapterplugin.WriteMessage(&reqBuf, req); err != nil {
		return nil, fmt.Errorf("external %s: encode request: %w", c.name, err)
	}

	cmd := exec.CommandContext(ctx, c.command, c.args...)
	cmd.Stdin = &reqBuf
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// Decode the response regardless of exit status: a well-behaved plugin
	// reports failures via DiscoverResponse.Error and a clean exit, but
	// decoding first yields the better message when a plugin does both.
	var resp pluginpb.DiscoverResponse
	decodeErr := adapterplugin.ReadMessage(&stdout, &resp)

	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("external %s: timed out after %s", c.name, c.timeout)
		}
		if decodeErr == nil && resp.GetError() != "" {
			return nil, fmt.Errorf("external %s: %s", c.name, resp.GetError())
		}
		return nil, fmt.Errorf("external %s: %w%s", c.name, runErr, stderrTail(&stderr))
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("external %s: decode response: %w%s", c.name, decodeErr, stderrTail(&stderr))
	}
	if v := resp.GetProtocolVersion(); v != adapterplugin.ProtocolVersion {
		return nil, fmt.Errorf("external %s: protocol version mismatch: host speaks v%d, plugin speaks v%d",
			c.name, adapterplugin.ProtocolVersion, v)
	}
	if resp.GetError() != "" {
		return nil, fmt.Errorf("external %s: %s", c.name, resp.GetError())
	}
	return &resp, nil
}

// info runs the --sheaf-adapter-info probe and returns the plugin's
// self-reported identity. Best-effort; used for Version() and for
// optional host-side pre-flight validation.
func (c *client) info(ctx context.Context) (*pluginpb.PluginInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	args := append(append([]string{}, c.args...), adapterplugin.InfoFlag)
	cmd := exec.CommandContext(ctx, c.command, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("external %s: info probe: %w%s", c.name, err, stderrTail(&stderr))
	}
	var pi pluginpb.PluginInfo
	if err := adapterplugin.ReadMessage(&stdout, &pi); err != nil {
		return nil, fmt.Errorf("external %s: decode info: %w", c.name, err)
	}
	return &pi, nil
}

// checkHealth verifies the plugin is operational: it runs the info probe
// and confirms a compatible protocol version. Backs the HealthChecker
// interface consumed by `sheaf doctor`.
func (c *client) checkHealth(ctx context.Context) error {
	pi, err := c.info(ctx)
	if err != nil {
		return err
	}
	if v := pi.GetProtocolVersion(); v != adapterplugin.ProtocolVersion {
		return fmt.Errorf("external %s: protocol version mismatch: host speaks v%d, plugin speaks v%d",
			c.name, adapterplugin.ProtocolVersion, v)
	}
	return nil
}

// version returns the plugin's self-reported version, probed once and
// cached. Falls back to "unknown" if the probe fails — Version() is
// informational and must never block a scan.
func (c *client) version() string {
	c.verOnce.Do(func() {
		c.ver = "unknown"
		if pi, err := c.info(context.Background()); err == nil && pi.GetVersion() != "" {
			c.ver = pi.GetVersion()
		}
	})
	return c.ver
}

// stderrTail returns a short, prefixed tail of captured plugin stderr for
// inclusion in error messages, or "" when there was none.
func stderrTail(b *bytes.Buffer) string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	const max = 2048
	if len(s) > max {
		s = "…" + s[len(s)-max:]
	}
	return ": " + s
}

// ============================================================
// Role wrappers — one per adapter interface.
// ============================================================

type testParser struct{ c *client }

// NewTestParser wraps a plugin as a TestParser.
func NewTestParser(cfg Config) (adapters.TestParser, error) {
	c, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return &testParser{c: c}, nil
}

func (t *testParser) Name() string                          { return t.c.name }
func (t *testParser) Version() string                       { return t.c.version() }
func (t *testParser) SupportedFrameworks() []string         { return []string{t.c.name} }
func (t *testParser) CheckHealth(ctx context.Context) error { return t.c.checkHealth(ctx) }

func (t *testParser) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*testcasepb.TestCase, error) {
	resp, err := t.c.discover(ctx, pluginpb.Role_ROLE_TEST_PARSER, repoRoot, scope)
	if err != nil {
		return nil, err
	}
	return resp.GetTests(), nil
}

type docParser struct{ c *client }

// NewDocParser wraps a plugin as a DocParser.
func NewDocParser(cfg Config) (adapters.DocParser, error) {
	c, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return &docParser{c: c}, nil
}

func (d *docParser) Name() string                          { return d.c.name }
func (d *docParser) Version() string                       { return d.c.version() }
func (d *docParser) SupportedFormats() []string            { return []string{d.c.name} }
func (d *docParser) CheckHealth(ctx context.Context) error { return d.c.checkHealth(ctx) }

func (d *docParser) Parse(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*docclaimpb.DocClaim, error) {
	resp, err := d.c.discover(ctx, pluginpb.Role_ROLE_DOC_PARSER, repoRoot, scope)
	if err != nil {
		return nil, err
	}
	return resp.GetDocClaims(), nil
}

// contractAnchor implements ContractAnchorParser and the optional
// DiscoverWithDocs entry point — a contract-anchor plugin may emit inline
// DocClaims (e.g. FIDL `///` comments) alongside its elements, and the
// orchestrator routes through DiscoverWithDocs when present.
type contractAnchor struct{ c *client }

// NewContractAnchor wraps a plugin as a ContractAnchorParser.
func NewContractAnchor(cfg Config) (adapters.ContractAnchorParser, error) {
	c, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return &contractAnchor{c: c}, nil
}

func (a *contractAnchor) Name() string                          { return a.c.name }
func (a *contractAnchor) Version() string                       { return a.c.version() }
func (a *contractAnchor) CheckHealth(ctx context.Context) error { return a.c.checkHealth(ctx) }

func (a *contractAnchor) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	resp, err := a.c.discover(ctx, pluginpb.Role_ROLE_CONTRACT_ANCHOR, repoRoot, scope)
	if err != nil {
		return nil, err
	}
	return resp.GetElements(), nil
}

func (a *contractAnchor) DiscoverWithDocs(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, []*docclaimpb.DocClaim, error) {
	resp, err := a.c.discover(ctx, pluginpb.Role_ROLE_CONTRACT_ANCHOR, repoRoot, scope)
	if err != nil {
		return nil, nil, err
	}
	return resp.GetElements(), resp.GetDocClaims(), nil
}

type renderedReference struct{ c *client }

// NewRenderedReference wraps a plugin as a RenderedReferenceParser.
func NewRenderedReference(cfg Config) (adapters.RenderedReferenceParser, error) {
	c, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return &renderedReference{c: c}, nil
}

func (r *renderedReference) Name() string                          { return r.c.name }
func (r *renderedReference) Version() string                       { return r.c.version() }
func (r *renderedReference) CheckHealth(ctx context.Context) error { return r.c.checkHealth(ctx) }

func (r *renderedReference) Parse(ctx context.Context) ([]*docclaimpb.DocClaim, error) {
	// Rendered references read their bundle paths from config, not from
	// the repo walk, so repo_path and scope are empty.
	resp, err := r.c.discover(ctx, pluginpb.Role_ROLE_RENDERED_REFERENCE, "", adapters.ScopeConfig{})
	if err != nil {
		return nil, err
	}
	return resp.GetDocClaims(), nil
}

type implementsMapper struct{ c *client }

// NewImplementsMapper wraps a plugin as an ImplementsMapper.
func NewImplementsMapper(cfg Config) (adapters.ImplementsMapper, error) {
	c, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	return &implementsMapper{c: c}, nil
}

func (m *implementsMapper) Name() string                          { return m.c.name }
func (m *implementsMapper) Version() string                       { return m.c.version() }
func (m *implementsMapper) CheckHealth(ctx context.Context) error { return m.c.checkHealth(ctx) }

func (m *implementsMapper) Discover(ctx context.Context, repoRoot string, scope adapters.ScopeConfig) ([]*contractpb.ContractElement, error) {
	resp, err := m.c.discover(ctx, pluginpb.Role_ROLE_IMPLEMENTS_MAP, repoRoot, scope)
	if err != nil {
		return nil, err
	}
	return resp.GetElements(), nil
}
