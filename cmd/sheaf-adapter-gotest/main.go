// Command sheaf-adapter-gotest is the reference runtime adapter: it
// exposes sheaf's stock in-process gotest test-parser as a standalone
// executable that speaks the adapter-plugin protocol over stdio.
//
// It is the worked example for docs/adapter-protocol.md and
// docs/examples/runtime-adapter/: the entire adapter is the stock
// internal/adapters/gotest parser, unchanged, wrapped by
// adapterplugin.Serve. The same parser, run in-process or as this
// plugin, emits byte-identical TestCases (asserted by the equivalence
// test in internal/adapters/external).
//
// Protocol, in one screen:
//   - The host writes one length-prefixed DiscoverRequest to our stdin.
//   - We read config (include/exclude globs + the "binary_name" option),
//     run the gotest parser against req.repo_path, and write one
//     length-prefixed DiscoverResponse of TestCases to stdout.
//   - `--sheaf-adapter-info` makes us emit a PluginInfo frame and exit,
//     for the host's handshake / `sheaf doctor`.
//
// Wire it into sheaf.textproto with:
//
//	test_parser {
//	  name: "external"
//	  external {
//	    command: "sheaf-adapter-gotest"
//	    include: "**/*_test.go"
//	    option { key: "binary_name" value: "docker" }  # optional
//	  }
//	}
package main

import (
	"context"

	"github.com/sheaf-data/sheaf/internal/adapterplugin"
	"github.com/sheaf-data/sheaf/internal/adapters/gotest"
	pluginpb "github.com/sheaf-data/sheaf/proto/adapterplugin"
)

func main() {
	info := adapterplugin.Info{
		Name:    gotest.Name,
		Version: gotest.Version,
		Roles:   []pluginpb.Role{pluginpb.Role_ROLE_TEST_PARSER},
	}
	adapterplugin.Serve(info, handle)
}

// handle runs one Discover: build the gotest config from the wire
// request, parse, and return the TestCases. Returning an error (or a
// parser error) is surfaced to the host as a soft adapter failure.
func handle(ctx context.Context, req *pluginpb.DiscoverRequest) (*pluginpb.DiscoverResponse, error) {
	cfg := gotest.Config{
		Include:    req.GetConfig().GetInclude(),
		Exclude:    req.GetConfig().GetExclude(),
		BinaryName: req.GetConfig().GetOption()["binary_name"],
	}
	tests, err := gotest.New(cfg).Discover(ctx, req.GetRepoPath(), adapterplugin.ScopeFromProto(req.GetScope()))
	if err != nil {
		return nil, err
	}
	return &pluginpb.DiscoverResponse{Tests: tests}, nil
}
