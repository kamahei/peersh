// Package wire provides length-prefixed protobuf framing for messages flowing
// over QUIC streams (and any other byte-stream transport).
//
// Each frame is a varint-encoded length followed by that many bytes of
// protobuf payload. The layout is straightforward and matches the convention
// used by google.golang.org/protobuf for delimited streams.
package wire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// MaxFrameSize is the largest frame the helpers will read in one call.
// Messages over this size are rejected. 4 MiB is generous for control-plane
// messages and small enough to bound test memory.
const MaxFrameSize = 4 << 20 // 4 MiB

// ErrFrameTooLarge is returned by Read when a frame's declared length exceeds
// MaxFrameSize.
var ErrFrameTooLarge = errors.New("wire: frame exceeds MaxFrameSize")

// Write encodes m and writes a length-prefixed frame to w.
func Write(w io.Writer, m proto.Message) error {
	payload, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("wire: marshal: %w", err)
	}
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], uint64(len(payload)))
	if _, err := w.Write(buf[:n]); err != nil {
		return fmt.Errorf("wire: write length prefix: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("wire: write payload: %w", err)
	}
	return nil
}

// Read decodes one length-prefixed frame from r into m.
//
// r should be a *bufio.Reader (or wrap one with NewReader) so that the varint
// decode does not over-read. Read will buffer-wrap r if it is not already a
// ByteReader.
func Read(r io.Reader, m proto.Message) error {
	br, ok := r.(byteReader)
	if !ok {
		br = bufio.NewReader(r)
	}
	length, err := binary.ReadUvarint(br)
	if err != nil {
		return fmt.Errorf("wire: read length: %w", err)
	}
	if length > MaxFrameSize {
		return ErrFrameTooLarge
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		return fmt.Errorf("wire: read payload: %w", err)
	}
	if err := proto.Unmarshal(payload, m); err != nil {
		return fmt.Errorf("wire: unmarshal: %w", err)
	}
	return nil
}

// byteReader is the union of io.Reader and io.ByteReader. *bufio.Reader and
// *bytes.Buffer satisfy it.
type byteReader interface {
	io.Reader
	io.ByteReader
}

// NewReader returns a buffered byte-reader suitable for repeated Read calls
// against the same stream. Use this once at the start of a stream to avoid
// re-wrapping on every call.
func NewReader(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }
