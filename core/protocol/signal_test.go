package protocol_test

import (
	"testing"

	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"google.golang.org/protobuf/proto"
)

func TestSignalFrameClientHello(t *testing.T) {
	in := &signalv1.Frame{
		Body: &signalv1.Frame_ClientHello{
			ClientHello: &signalv1.ClientHello{
				ProtocolVersion: 1,
				Capabilities:    []string{"a", "b"},
				ClientId:        "peershd/0.1",
			},
		},
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := &signalv1.Frame{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch")
	}
	hello := out.GetClientHello()
	if hello == nil || hello.GetClientId() != "peershd/0.1" {
		t.Fatalf("expected ClientHello with id peershd/0.1, got %+v", out)
	}
}

func TestSignalRegisterDeterministicMarshal(t *testing.T) {
	r := &signalv1.Register{
		UserId:       "alice",
		DeviceId:     "DEVICE0000000ABC",
		PublicKey:    []byte{0x01, 0x02, 0x03},
		Kind:         signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST,
		DisplayName:  "alice-laptop",
		SignedAtUnix: 1714512000,
		Nonce:        []byte{0xaa, 0xbb, 0xcc, 0xdd},
		// hmac_signature omitted; this is the body that gets signed.
	}
	opt := proto.MarshalOptions{Deterministic: true}
	a, err := opt.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal a: %v", err)
	}
	b, err := opt.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal b: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("deterministic marshal not stable")
	}
}

func TestSignalConnectRoundTrip(t *testing.T) {
	in := &signalv1.Frame{
		Body: &signalv1.Frame_Connect{
			Connect: &signalv1.Connect{
				TargetDeviceId: "TARGET00000000AB",
				FromDeviceId:   "",
				Candidates: []*signalv1.EndpointCandidate{
					{Address: "192.168.1.5", Port: 7777, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},
				},
			},
		},
	}
	wire, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out := &signalv1.Frame{}
	if err := proto.Unmarshal(wire, out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip mismatch")
	}
	c := out.GetConnect()
	if c == nil || len(c.GetCandidates()) != 1 || c.GetCandidates()[0].GetAddress() != "192.168.1.5" {
		t.Fatalf("connect candidates not preserved: %+v", c)
	}
}
