package adapterplugin

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	pluginpb "github.com/sheaf-data/sheaf/proto/adapterplugin"
)

func TestWriteRead_RoundTrip(t *testing.T) {
	in := &pluginpb.DiscoverRequest{
		ProtocolVersion: ProtocolVersion,
		Role:            pluginpb.Role_ROLE_TEST_PARSER,
		RepoPath:        "/tmp/repo",
		Scope:           &pluginpb.Scope{Library: []string{"lib.a"}, Exclude: []string{"**/x"}},
		Config:          &pluginpb.AdapterConfig{Include: []string{"**/*_test.go"}, Option: map[string]string{"binary_name": "docker"}},
	}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, in); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	var out pluginpb.DiscoverRequest
	if err := ReadMessage(&buf, &out); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("round-trip mismatch:\n in = %v\nout = %v", in, &out)
	}
}

func TestWriteRead_SequentialFrames(t *testing.T) {
	// Two messages on the same stream must decode in order — the frame
	// header is what delimits them.
	var buf bytes.Buffer
	a := &pluginpb.DiscoverResponse{Warnings: []string{"a"}}
	b := &pluginpb.DiscoverResponse{Warnings: []string{"b"}}
	for _, m := range []proto.Message{a, b} {
		if err := WriteMessage(&buf, m); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
	}
	for _, want := range []string{"a", "b"} {
		var got pluginpb.DiscoverResponse
		if err := ReadMessage(&buf, &got); err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		if len(got.GetWarnings()) != 1 || got.GetWarnings()[0] != want {
			t.Fatalf("got warnings %v, want [%q]", got.GetWarnings(), want)
		}
	}
}

func TestReadMessage_EmptyStreamIsEOF(t *testing.T) {
	var got pluginpb.DiscoverRequest
	if err := ReadMessage(bytes.NewReader(nil), &got); err != io.EOF {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

func TestReadMessage_TruncatedHeader(t *testing.T) {
	var got pluginpb.DiscoverRequest
	// Only two of the four header bytes.
	if err := ReadMessage(bytes.NewReader([]byte{0, 0}), &got); err != io.ErrUnexpectedEOF {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadMessage_TruncatedPayload(t *testing.T) {
	// Header claims 10 bytes; supply 3.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 10)
	stream := append(hdr[:], []byte{1, 2, 3}...)
	var got pluginpb.DiscoverRequest
	if err := ReadMessage(bytes.NewReader(stream), &got); err != io.ErrUnexpectedEOF {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadMessage_FrameTooLarge(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxFrameSize+1)
	var got pluginpb.DiscoverRequest
	err := ReadMessage(bytes.NewReader(hdr[:]), &got)
	if err == nil {
		t.Fatal("expected an error for an over-large frame header")
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		t.Fatalf("got %v, want a size-limit error", err)
	}
}

func TestReadMessage_GarbagePayload(t *testing.T) {
	// A valid frame whose payload is not a valid proto for the target.
	var hdr [4]byte
	payload := []byte{0xff, 0xff, 0xff, 0xff} // invalid wire bytes
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	stream := append(hdr[:], payload...)
	var got pluginpb.DiscoverRequest
	if err := ReadMessage(bytes.NewReader(stream), &got); err == nil {
		t.Fatal("expected an unmarshal error for garbage payload")
	}
}
