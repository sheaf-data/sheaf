// Package adapterplugin implements the stdio wire protocol that connects
// sheaf to out-of-process ("runtime") adapters. It is used from two
// sides:
//
//   - Plugin authors call Serve to expose an adapter as an executable
//     that speaks the protocol over stdin/stdout.
//   - The host (internal/adapters/external) uses the codec to spawn a
//     plugin, send a DiscoverRequest, and read a DiscoverResponse.
//
// The wire types live in proto/adapterplugin; this package adds the
// framing, version handshake, and serving loop. See
// docs/adapter-protocol.md for the protocol specification.
package adapterplugin

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// ProtocolVersion is the wire protocol version this build speaks. v1
// requires an exact match on both sides; a mismatch is reported as a
// clear error rather than attempting best-effort decoding.
const ProtocolVersion = 1

// MaxFrameSize bounds a single message to guard against a corrupt or
// hostile length prefix. 256 MiB sits far above any realistic corpus
// payload while still preventing an unbounded allocation from a bad
// header.
const MaxFrameSize = 256 << 20

// WriteMessage writes m to w as one length-prefixed frame: a 4-byte
// big-endian unsigned length followed by the binary-marshaled message.
// The header and payload are written in two calls; callers that need
// atomicity across goroutines must serialize their own access to w.
func WriteMessage(w io.Writer, m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("adapterplugin: marshal: %w", err)
	}
	if len(b) > MaxFrameSize {
		return fmt.Errorf("adapterplugin: message too large: %d bytes (max %d)", len(b), MaxFrameSize)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("adapterplugin: write header: %w", err)
	}
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("adapterplugin: write payload: %w", err)
	}
	return nil
}

// ReadMessage reads one length-prefixed frame from r and unmarshals it
// into m. It returns io.EOF when the stream ends cleanly before any
// bytes of a frame (the expected end-of-conversation signal), and
// io.ErrUnexpectedEOF when a frame is truncated mid-way.
func ReadMessage(r io.Reader, m proto.Message) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		// io.EOF here means "nothing more to read" — a clean end. A
		// partial header surfaces as io.ErrUnexpectedEOF from ReadFull.
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return fmt.Errorf("adapterplugin: frame too large: %d bytes (max %d)", n, MaxFrameSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		// Either sub-case (zero or partial bytes read against a declared
		// length) means the frame was truncated; surface the canonical
		// sentinel so callers can match it directly.
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return io.ErrUnexpectedEOF
		}
		return fmt.Errorf("adapterplugin: read payload: %w", err)
	}
	if err := proto.Unmarshal(buf, m); err != nil {
		return fmt.Errorf("adapterplugin: unmarshal: %w", err)
	}
	return nil
}
