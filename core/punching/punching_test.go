package punching_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/punching"
	"github.com/pion/stun/v2"
)

// runStubSTUN starts an in-process STUN responder on a 127.0.0.1 UDP socket
// and returns its address. The responder echoes XOR-MAPPED-ADDRESS = caller
// address. Cleans up via t.Cleanup.
func runStubSTUN(t *testing.T) *net.UDPAddr {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("STUN ListenUDP: %v", err)
	}
	t.Cleanup(func() { pc.Close() })

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			req := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
			if err := req.Decode(); err != nil {
				continue
			}
			if req.Type != stun.BindingRequest {
				continue
			}
			resp, err := stun.Build(
				stun.NewTransactionIDSetter(req.TransactionID),
				stun.BindingSuccess,
				&stun.XORMappedAddress{IP: from.IP, Port: from.Port},
				stun.Fingerprint,
			)
			if err != nil {
				continue
			}
			out, err := resp.MarshalBinary()
			if err != nil {
				continue
			}
			_, _ = pc.WriteToUDP(out, from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func TestDiscoverHappyPath(t *testing.T) {
	stun := runStubSTUN(t)

	clientPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("client ListenUDP: %v", err)
	}
	defer clientPC.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	srflx, err := punching.Discover(ctx, clientPC, punching.Options{
		STUNServer:  stun.String(),
		STUNTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := clientPC.LocalAddr().(*net.UDPAddr)
	if srflx.Port != want.Port {
		t.Fatalf("port mismatch: got %d want %d", srflx.Port, want.Port)
	}
	// IP comparison via Equal handles 4-in-6 differences.
	if !srflx.IP.Equal(want.IP) {
		t.Fatalf("ip mismatch: got %v want %v", srflx.IP, want.IP)
	}
}

func TestDiscoverTimeout(t *testing.T) {
	// Point at a black-hole port (no STUN responder there).
	clientPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("client ListenUDP: %v", err)
	}
	defer clientPC.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = punching.Discover(ctx, clientPC, punching.Options{
		STUNServer:  "127.0.0.1:1", // closed port
		STUNTimeout: 200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestPunchDeliversBurst(t *testing.T) {
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("peer ListenUDP: %v", err)
	}
	defer peer.Close()

	clientPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("client ListenUDP: %v", err)
	}
	defer clientPC.Close()

	var (
		mu       sync.Mutex
		received int
	)
	go func() {
		buf := make([]byte, 1500)
		for {
			peer.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _, err := peer.ReadFrom(buf)
			if err != nil {
				return
			}
			if punching.IsPunchPacket(buf[:n]) {
				mu.Lock()
				received++
				mu.Unlock()
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := punching.Punch(ctx, clientPC, []*net.UDPAddr{peer.LocalAddr().(*net.UDPAddr)}, punching.Options{
		PunchPackets:  3,
		PunchInterval: 10 * time.Millisecond,
	}); err != nil {
		t.Fatalf("Punch: %v", err)
	}
	// Brief settle so the listener can drain the queue.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	got := received
	mu.Unlock()
	if got != 3 {
		t.Fatalf("expected 3 punch packets, got %d", got)
	}
}

func TestSortCandidatesOrder(t *testing.T) {
	in := []*signalv1.EndpointCandidate{
		{Address: "192.168.1.5", Port: 7777, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},      // v4 host
		{Address: "203.0.113.10", Port: 7777, Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE}, // v4 srflx
		{Address: "fd00::1", Port: 7777, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},          // v6 host
		{Address: "2001:db8::1", Port: 7777, Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE}, // v6 srflx
	}
	out := punching.SortCandidates(in)
	got := []string{}
	for _, c := range out {
		got = append(got, c.GetAddress())
	}
	want := []string{"2001:db8::1", "203.0.113.10", "fd00::1", "192.168.1.5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: got %q want %q (full order %v)", i, got[i], want[i], got)
		}
	}
}

func TestErrTraversalFailedMessage(t *testing.T) {
	// The error string is the user-facing message; documented in
	// docs/deploy/self-hosting.md. Don't change it without updating docs.
	if got := punching.ErrTraversalFailed.Error(); got != "Direct connection not possible from this network." {
		t.Fatalf("ErrTraversalFailed message changed: %q", got)
	}
	if !errors.Is(punching.ErrTraversalFailed, punching.ErrTraversalFailed) {
		t.Fatal("errors.Is should match")
	}
}

func TestCandidatesToUDPAddrs(t *testing.T) {
	in := []*signalv1.EndpointCandidate{
		{Address: "127.0.0.1", Port: 1234, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},
		{Address: "not-an-ip", Port: 1234, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},
	}
	out := punching.CandidatesToUDPAddrs(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 valid addr, got %d", len(out))
	}
	if out[0].IP.String() != "127.0.0.1" || out[0].Port != 1234 {
		t.Fatalf("unexpected addr: %v", out[0])
	}
}
