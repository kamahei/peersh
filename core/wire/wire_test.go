package wire_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/core/wire"
	"google.golang.org/protobuf/proto"
)

func TestRoundTrip(t *testing.T) {
	in := &v1.ClientHello{
		ProtocolVersion: 1,
		Capabilities:    []string{"a", "b"},
		ClientId:        "peersh-cli/0.1",
	}
	var buf bytes.Buffer
	if err := wire.Write(&buf, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := &v1.ClientHello{}
	if err := wire.Read(&buf, out); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestMultipleFrames(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 5; i++ {
		m := &v1.ExecResponse{Chunk: &v1.ExecResponse_Stdout{Stdout: []byte("chunk")}}
		if err := wire.Write(&buf, m); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	r := wire.NewReader(&buf)
	for i := 0; i < 5; i++ {
		out := &v1.ExecResponse{}
		if err := wire.Read(r, out); err != nil {
			t.Fatalf("Read %d: %v", i, err)
		}
		if string(out.GetStdout()) != "chunk" {
			t.Fatalf("frame %d: unexpected payload %q", i, out.GetStdout())
		}
	}
}

func TestFrameTooLargeRejected(t *testing.T) {
	var buf bytes.Buffer
	// Encode a length that exceeds MaxFrameSize.
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], wire.MaxFrameSize+1)
	buf.Write(lenBuf[:n])

	out := &v1.ClientHello{}
	if err := wire.Read(&buf, out); !errors.Is(err, wire.ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}
