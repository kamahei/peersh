package room_test

import (
	"context"
	"errors"
	"testing"

	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/server/room"
)

type mockConn struct {
	user, device string
	sent         chan *signalv1.Frame
}

func (m *mockConn) UserID() string   { return m.user }
func (m *mockConn) DeviceID() string { return m.device }
func (m *mockConn) Send(_ context.Context, f *signalv1.Frame) error {
	m.sent <- f
	return nil
}

func TestForwardHappyPath(t *testing.T) {
	r := room.New()
	a := &mockConn{user: "alice", device: "A", sent: make(chan *signalv1.Frame, 1)}
	b := &mockConn{user: "alice", device: "B", sent: make(chan *signalv1.Frame, 1)}
	r.Register(a)
	r.Register(b)

	if err := r.Forward(context.Background(), a, &signalv1.Connect{
		TargetDeviceId: "B",
		Candidates:     []*signalv1.EndpointCandidate{{Address: "192.168.1.5", Port: 7777}},
	}); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	got := <-b.sent
	c := got.GetConnect()
	if c == nil {
		t.Fatal("expected Connect frame")
	}
	if c.GetFromDeviceId() != "A" {
		t.Fatalf("expected from=A, got %q", c.GetFromDeviceId())
	}
	if len(c.GetCandidates()) != 1 {
		t.Fatalf("expected 1 candidate")
	}
}

func TestForwardSelfRejected(t *testing.T) {
	r := room.New()
	a := &mockConn{user: "alice", device: "A", sent: make(chan *signalv1.Frame, 1)}
	r.Register(a)
	err := r.Forward(context.Background(), a, &signalv1.Connect{TargetDeviceId: "A"})
	if !errors.Is(err, room.ErrSenderEqualsTarget) {
		t.Fatalf("expected ErrSenderEqualsTarget, got %v", err)
	}
}

func TestForwardUnknownTarget(t *testing.T) {
	r := room.New()
	a := &mockConn{user: "alice", device: "A", sent: make(chan *signalv1.Frame, 1)}
	r.Register(a)
	err := r.Forward(context.Background(), a, &signalv1.Connect{TargetDeviceId: "Z"})
	if !errors.Is(err, room.ErrTargetUnknown) {
		t.Fatalf("expected ErrTargetUnknown, got %v", err)
	}
}

func TestUnregisterRemoves(t *testing.T) {
	r := room.New()
	a := &mockConn{user: "alice", device: "A", sent: make(chan *signalv1.Frame, 1)}
	r.Register(a)
	if r.CountByUser("alice") != 1 {
		t.Fatal("expected 1")
	}
	r.Unregister(a)
	if r.CountByUser("alice") != 0 {
		t.Fatal("expected 0 after Unregister")
	}
}
