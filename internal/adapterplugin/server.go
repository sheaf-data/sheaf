package adapterplugin

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/sheaf-data/sheaf/internal/adapters"
	pluginpb "github.com/sheaf-data/sheaf/proto/adapterplugin"
)

// InfoFlag, passed as an argument to a plugin, makes it emit a single
// length-prefixed PluginInfo frame to stdout and exit 0 — the host's
// up-front handshake/diagnostics probe (used by `sheaf doctor` and the
// host's optional pre-flight version check).
const InfoFlag = "--sheaf-adapter-info"

// Info describes a plugin for the handshake and diagnostics.
type Info struct {
	Name    string
	Version string
	Roles   []pluginpb.Role
}

// HandlerFunc performs one Discover. It reads req.Role / req.RepoPath /
// req.Scope / req.Config and returns the rows for that role on the
// response. Returning a non-nil error is reported to the host as a
// failed adapter (soft-fail); equivalently the handler may set
// DiscoverResponse.Error itself.
type HandlerFunc func(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error)

// Serve runs the stdio plugin protocol on os.Stdin/os.Stdout, honoring
// the --sheaf-adapter-info probe via os.Args, then exits the process.
// A plugin's main function typically calls this and nothing else:
//
//	func main() {
//	    adapterplugin.Serve(info, handler)
//	}
func Serve(info Info, h HandlerFunc) {
	os.Exit(serveMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, info, h))
}

// serveMain is the os.Exit-free body of Serve, returning a process exit
// code. Split out so tests can drive it with in-memory streams.
func serveMain(args []string, in io.Reader, out, errw io.Writer, info Info, h HandlerFunc) int {
	for _, a := range args {
		if a == InfoFlag {
			pi := &pluginpb.PluginInfo{
				ProtocolVersion: ProtocolVersion,
				Name:            info.Name,
				Version:         info.Version,
				Roles:           info.Roles,
			}
			if err := WriteMessage(out, pi); err != nil {
				fmt.Fprintf(errw, "%s: write info: %v\n", info.Name, err)
				return 1
			}
			return 0
		}
	}
	if err := ServeConn(context.Background(), in, out, info, h); err != nil {
		fmt.Fprintf(errw, "%s: %v\n", info.Name, err)
		return 1
	}
	return 0
}

// ServeConn runs exactly one request/response exchange over the given
// streams: it reads one DiscoverRequest from in, dispatches it, and
// writes one DiscoverResponse to out. It is the transport-agnostic,
// os.Exit-free core of Serve, exported for tests and for embedding the
// protocol in another transport. A handler panic is recovered and
// returned to the host as DiscoverResponse.Error so a misbehaving plugin
// degrades to a soft adapter failure rather than a torn stream.
func ServeConn(ctx context.Context, in io.Reader, out io.Writer, info Info, h HandlerFunc) error {
	var req pluginpb.DiscoverRequest
	if err := ReadMessage(in, &req); err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	resp := dispatch(ctx, &req, info, h)
	resp.ProtocolVersion = ProtocolVersion
	if err := WriteMessage(out, resp); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	return nil
}

func dispatch(ctx context.Context, req *pluginpb.DiscoverRequest, info Info, h HandlerFunc) (resp *pluginpb.DiscoverResponse) {
	defer func() {
		if r := recover(); r != nil {
			resp = &pluginpb.DiscoverResponse{Error: fmt.Sprintf("%s: panic: %v", info.Name, r)}
		}
	}()
	if req.GetProtocolVersion() != ProtocolVersion {
		return &pluginpb.DiscoverResponse{
			Error: fmt.Sprintf("%s: protocol version mismatch: host speaks v%d, plugin speaks v%d",
				info.Name, req.GetProtocolVersion(), ProtocolVersion),
		}
	}
	out, err := h(ctx, req)
	if err != nil {
		return &pluginpb.DiscoverResponse{Error: err.Error()}
	}
	if out == nil {
		out = &pluginpb.DiscoverResponse{}
	}
	return out
}

// ScopeFromProto converts a wire Scope into an adapters.ScopeConfig, so
// plugin handlers can pass it straight to a stock adapter's Discover.
func ScopeFromProto(s *pluginpb.Scope) adapters.ScopeConfig {
	return adapters.ScopeConfig{
		Libraries:   s.GetLibrary(),
		AlsoInclude: s.GetAlsoInclude(),
		Exclude:     s.GetExclude(),
	}
}

// ScopeToProto converts an adapters.ScopeConfig into a wire Scope, used
// by the host when building a DiscoverRequest.
func ScopeToProto(s adapters.ScopeConfig) *pluginpb.Scope {
	return &pluginpb.Scope{
		Library:     s.Libraries,
		AlsoInclude: s.AlsoInclude,
		Exclude:     s.Exclude,
	}
}
