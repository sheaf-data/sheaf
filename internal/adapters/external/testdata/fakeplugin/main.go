// Command fakeplugin is an adversarial test peer for the external host
// adapter. It speaks the adapter-plugin protocol but, unlike a real
// plugin, hand-rolls its stdout so it can inject every failure mode the
// host must survive. The mode is selected by the request's
// option["mode"]; it always drains the request frame from stdin first so
// the host's stdin copy never breaks early.
//
// Built explicitly by the external package's TestMain (it lives under
// testdata so it is excluded from ./... builds and lint).
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sheaf-data/sheaf/internal/adapterplugin"
	pluginpb "github.com/sheaf-data/sheaf/proto/adapterplugin"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

func main() {
	for _, a := range os.Args[1:] {
		if a == adapterplugin.InfoFlag {
			_ = adapterplugin.WriteMessage(os.Stdout, &pluginpb.PluginInfo{
				ProtocolVersion: adapterplugin.ProtocolVersion,
				Name:            "fakeplugin",
				Version:         "0.0.1",
				Roles:           []pluginpb.Role{pluginpb.Role_ROLE_TEST_PARSER},
			})
			return
		}
	}

	// Always read the request first so the host's stdin write completes.
	var req pluginpb.DiscoverRequest
	if err := adapterplugin.ReadMessage(os.Stdin, &req); err != nil {
		fmt.Fprintf(os.Stderr, "fakeplugin: read request: %v\n", err)
		os.Exit(2)
	}
	opt := req.GetConfig().GetOption()
	mode := opt["mode"]
	if mode == "" {
		mode = "ok"
	}

	switch mode {
	case "ok":
		id := opt["id"]
		if id == "" {
			id = "fake-test"
		}
		write(&pluginpb.DiscoverResponse{
			ProtocolVersion: adapterplugin.ProtocolVersion,
			Tests: []*testcasepb.TestCase{{
				Id:        id,
				Framework: "fake",
				Location:  &commonpb.SourceLocation{Path: "fake_test.go", Line: 1},
			}},
		})
	case "error":
		msg := opt["msg"]
		if msg == "" {
			msg = "synthetic failure"
		}
		write(&pluginpb.DiscoverResponse{ProtocolVersion: adapterplugin.ProtocolVersion, Error: msg})
	case "badversion":
		write(&pluginpb.DiscoverResponse{ProtocolVersion: 999})
	case "garbage":
		// Not a valid length-prefixed frame.
		os.Stdout.Write([]byte("this is not protobuf"))
	case "exit":
		// Non-zero exit, no response written.
		os.Exit(atoiOr(opt["code"], 3))
	case "exitafter":
		// Write a clean error response, THEN exit non-zero — the host
		// should prefer the response's error message.
		write(&pluginpb.DiscoverResponse{ProtocolVersion: adapterplugin.ProtocolVersion, Error: "boom-then-exit"})
		os.Exit(atoiOr(opt["code"], 3))
	case "sleep":
		time.Sleep(time.Duration(atoiOr(opt["sleep_ms"], 10000)) * time.Millisecond)
		write(&pluginpb.DiscoverResponse{ProtocolVersion: adapterplugin.ProtocolVersion})
	default:
		fmt.Fprintf(os.Stderr, "fakeplugin: unknown mode %q\n", mode)
		os.Exit(2)
	}
}

func write(resp *pluginpb.DiscoverResponse) {
	if err := adapterplugin.WriteMessage(os.Stdout, resp); err != nil {
		fmt.Fprintf(os.Stderr, "fakeplugin: write: %v\n", err)
		os.Exit(2)
	}
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
