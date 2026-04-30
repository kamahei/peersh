package protocol_test

import (
	"testing"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"google.golang.org/protobuf/proto"
)

func TestClientHelloRoundTrip(t *testing.T) {
	in := &v1.ClientHello{
		ProtocolVersion: 1,
		Capabilities:    []string{"session_resume", "ipv6"},
		ClientId:        "peersh-cli/0.1",
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := &v1.ClientHello{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch:\n  in  = %v\n  out = %v", in, out)
	}
}

func TestServerHelloRoundTrip(t *testing.T) {
	in := &v1.ServerHello{
		ProtocolVersion: 1,
		Capabilities:    nil,
		ServerId:        "peershd/0.1",
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := &v1.ServerHello{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch:\n  in  = %v\n  out = %v", in, out)
	}
}

func TestExecRequestRoundTrip(t *testing.T) {
	in := &v1.ExecRequest{
		Command:    "Get-Process | Select-Object -First 5",
		WorkingDir: "C:\\Users\\test",
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := &v1.ExecRequest{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestExecResponseRoundTripStdout(t *testing.T) {
	in := &v1.ExecResponse{
		Chunk: &v1.ExecResponse_Stdout{Stdout: []byte("hello\n")},
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := &v1.ExecResponse{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch")
	}
	if string(out.GetStdout()) != "hello\n" {
		t.Fatalf("expected stdout 'hello\\n', got %q", out.GetStdout())
	}
}

func TestExecResponseRoundTripStderr(t *testing.T) {
	in := &v1.ExecResponse{
		Chunk: &v1.ExecResponse_Stderr{Stderr: []byte("oops\n")},
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := &v1.ExecResponse{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestExecResponseRoundTripDone(t *testing.T) {
	in := &v1.ExecResponse{Done: true}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := &v1.ExecResponse{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.GetDone() {
		t.Fatalf("expected done=true, got false")
	}
}
