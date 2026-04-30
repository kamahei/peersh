package ws_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/peersh/peersh/core/auth/psk"
	"github.com/peersh/peersh/core/devid"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/punching"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/core/store/memory"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
	"github.com/peersh/peersh/core/wire"
	"github.com/peersh/peersh/server/ratelimit"
	"github.com/peersh/peersh/server/room"
	"github.com/peersh/peersh/server/ws"
	"github.com/pion/stun/v2"
)

// startStubSTUN runs an in-process STUN responder on a 127.0.0.1 UDP socket
// and returns its address. The responder echoes XOR-MAPPED-ADDRESS = caller
// address.
func startStubSTUN(t *testing.T) *net.UDPAddr {
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

// TestPhase3FullChain exercises the Phase 3 path end-to-end on loopback:
//
//   - both sides discover their reflexive address against a stub STUN
//   - both sides advertise SRFLX + HOST candidates via signaling
//   - both sides send punch packets at the peer's candidates
//   - the cli QUIC-dials the peer (sequential preferred-order)
//   - Hello + Exec round-trip succeeds
//
// The host echoes ExecRequest.command back as stdout, so this test is
// platform-independent (no pwsh involvement).
func TestPhase3FullChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stunAddr := startStubSTUN(t)

	// --- signaling server ---
	st := memory.New()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := st.PutPSKRecord(ctx, store.PSKRecord{UserID: "alice", Secret: secret, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}
	srv := ws.New(&ws.Server{
		Store:       st,
		Auth:        psk.New(st),
		Registry:    room.New(),
		IPLimit:     ratelimit.New(1000, 1000),
		UserLimit:   ratelimit.New(1000, 1000),
		DeviceLimit: ratelimit.New(1000, 1000),
		ServerID:    "phase3-test/0.1",
	})
	mux := http.NewServeMux()
	mux.Handle("/ws", srv.Handler())
	hs := httptest.NewServer(mux)
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"

	// --- generate keypairs ---
	hostPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cliPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostDevID := devid.Derive(hostPub)
	cliDevID := devid.Derive(cliPub)

	// --- host: STUN, transport, listener, signaling ---
	hostUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("host ListenUDP: %v", err)
	}
	defer hostUDP.Close()
	hostListenAddr := hostUDP.LocalAddr().(*net.UDPAddr)

	stunCtx, stunCancel := context.WithTimeout(ctx, 3*time.Second)
	hostSrflx, err := punching.Discover(stunCtx, hostUDP, punching.Options{STUNServer: stunAddr.String()})
	stunCancel()
	if err != nil {
		t.Fatalf("host Discover: %v", err)
	}
	if hostSrflx.Port != hostListenAddr.Port {
		t.Fatalf("host srflx port mismatch: %d vs %d", hostSrflx.Port, hostListenAddr.Port)
	}

	cert, err := devtls.LoadOrGenerate(t.TempDir())
	if err != nil {
		t.Fatalf("devtls: %v", err)
	}
	hostT := transport.New(hostUDP, devtls.ServerTLSConfig(cert))
	defer hostT.Close()
	hostListener, err := hostT.Listen(ctx)
	if err != nil {
		t.Fatalf("host Listen: %v", err)
	}
	defer hostListener.Close()

	go runEchoHost(ctx, hostListener)

	hostSC, err := signaling.Dial(ctx, signaling.DialOptions{
		URL: wsURL, UserID: "alice", Secret: secret, DeviceID: hostDevID,
		PublicKey: hostPub, Kind: signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST,
		ClientID: "phase3-host",
	})
	if err != nil {
		t.Fatalf("host signaling.Dial: %v", err)
	}
	defer hostSC.Close()

	var hostPunched atomic.Int32
	go func() {
		for {
			c, err := hostSC.Recv(ctx)
			if err != nil {
				return
			}
			peer := punching.SortCandidates(c.GetCandidates())
			peerAddrs := punching.CandidatesToUDPAddrs(peer)
			if err := punching.Punch(ctx, hostUDP, peerAddrs, punching.Options{PunchPackets: 2, PunchInterval: 10 * time.Millisecond}); err == nil {
				hostPunched.Add(1)
			}
			cands := []*signalv1.EndpointCandidate{
				{Address: hostSrflx.IP.String(), Port: uint32(hostSrflx.Port), Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE},
				{Address: hostListenAddr.IP.String(), Port: uint32(hostListenAddr.Port), Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},
			}
			_ = hostSC.SendConnect(ctx, c.GetFromDeviceId(), cands)
		}
	}()

	// --- cli: STUN, transport, signaling, Connect, Punch, Dial ---
	cliUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("cli ListenUDP: %v", err)
	}
	defer cliUDP.Close()
	cliListenAddr := cliUDP.LocalAddr().(*net.UDPAddr)

	stunCtx2, stunCancel2 := context.WithTimeout(ctx, 3*time.Second)
	cliSrflx, err := punching.Discover(stunCtx2, cliUDP, punching.Options{STUNServer: stunAddr.String()})
	stunCancel2()
	if err != nil {
		t.Fatalf("cli Discover: %v", err)
	}

	cliT := transport.New(cliUDP, devtls.DevClientTLSConfig())
	defer cliT.Close()

	cliSC, err := signaling.Dial(ctx, signaling.DialOptions{
		URL: wsURL, UserID: "alice", Secret: secret, DeviceID: cliDevID,
		PublicKey: cliPub, Kind: signalv1.DeviceKind_DEVICE_KIND_CLI,
		ClientID: "phase3-cli",
	})
	if err != nil {
		t.Fatalf("cli signaling.Dial: %v", err)
	}
	defer cliSC.Close()

	cliCands := []*signalv1.EndpointCandidate{
		{Address: cliSrflx.IP.String(), Port: uint32(cliSrflx.Port), Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE},
		{Address: cliListenAddr.IP.String(), Port: uint32(cliListenAddr.Port), Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},
	}
	if err := cliSC.SendConnect(ctx, hostDevID, cliCands); err != nil {
		t.Fatalf("cli SendConnect: %v", err)
	}
	reply, err := cliSC.Recv(ctx)
	if err != nil {
		t.Fatalf("cli Recv: %v", err)
	}
	if reply.GetFromDeviceId() != hostDevID {
		t.Fatalf("expected from=%s, got %s", hostDevID, reply.GetFromDeviceId())
	}

	sortedPeer := punching.SortCandidates(reply.GetCandidates())
	peerAddrs := punching.CandidatesToUDPAddrs(sortedPeer)
	if err := punching.Punch(ctx, cliUDP, peerAddrs, punching.Options{PunchPackets: 2, PunchInterval: 10 * time.Millisecond}); err != nil {
		t.Fatalf("cli Punch: %v", err)
	}

	var conn *transport.Conn
	for _, p := range peerAddrs {
		dialCtx, dCancel := context.WithTimeout(ctx, 2*time.Second)
		c, err := cliT.Dial(dialCtx, p)
		dCancel()
		if err == nil {
			conn = c
			break
		}
	}
	if conn == nil {
		t.Fatal("all candidate dials failed")
	}
	defer conn.CloseWithError(0, "")

	// --- application Hello + Exec ---
	ctrl, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream(control): %v", err)
	}
	if err := wire.Write(ctrl, &v1.ClientHello{ProtocolVersion: 1, ClientId: "phase3-cli"}); err != nil {
		t.Fatalf("write ClientHello: %v", err)
	}
	_ = ctrl.Close()
	r := wire.NewReader(ctrl)
	srvHello := &v1.ServerHello{}
	if err := wire.Read(r, srvHello); err != nil {
		t.Fatalf("read ServerHello: %v", err)
	}

	exec, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream(exec): %v", err)
	}
	if err := wire.Write(exec, &v1.ExecRequest{Command: "phase3-echo"}); err != nil {
		t.Fatalf("write ExecRequest: %v", err)
	}
	er := wire.NewReader(exec)
	var got string
	for {
		resp := &v1.ExecResponse{}
		if err := wire.Read(er, resp); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("read ExecResponse: %v", err)
		}
		if d := resp.GetStdout(); len(d) > 0 {
			got += string(d)
		}
		if resp.GetDone() {
			break
		}
	}
	if got != "phase3-echo" {
		t.Fatalf("expected echo %q, got %q", "phase3-echo", got)
	}
	if hostPunched.Load() == 0 {
		t.Fatal("host did not record any successful Punch")
	}
}
