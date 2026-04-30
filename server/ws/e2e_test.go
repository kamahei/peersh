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
	"testing"
	"time"

	"github.com/peersh/peersh/core/auth/psk"
	"github.com/peersh/peersh/core/devid"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/core/store/memory"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
	"github.com/peersh/peersh/core/wire"
	"github.com/peersh/peersh/server/ratelimit"
	"github.com/peersh/peersh/server/room"
	"github.com/peersh/peersh/server/ws"
)

// TestEndToEndSignalingPlusQUIC stands up a real signaling server, a real
// QUIC host and client (both backed by externally-constructed UDP conns,
// per the Phase 1 transport contract), and runs the full Phase 2 path:
//
//   - both endpoints register with the signaling server under the same
//     PSK user
//   - the client sends Connect{target=host}; the server forwards to the
//     host with from_device_id filled
//   - the host responds with its candidates; the server forwards back
//     to the client
//   - the client QUIC-dials the host's first candidate
//   - they complete the application-level Hello on stream 0
//   - the client opens a per-command stream and sends ExecRequest{"echo"}
//   - the host streams ExecResponse{stdout=..., done=true}
//
// pwsh is intentionally not involved: the host echoes ExecRequest.command
// back as stdout. This keeps the test platform-independent while still
// exercising every piece of Phase 2 plumbing end-to-end.
func TestEndToEndSignalingPlusQUIC(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// --- signaling server ---
	st := memory.New()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := st.PutPSKRecord(ctx, store.PSKRecord{
		UserID: "alice", Secret: secret, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}
	srv := ws.New(&ws.Server{
		Store:       st,
		Auth:        psk.New(st),
		Registry:    room.New(),
		IPLimit:     ratelimit.New(1000, 1000),
		UserLimit:   ratelimit.New(1000, 1000),
		DeviceLimit: ratelimit.New(1000, 1000),
		ServerID:    "test/0.1",
	})
	mux := http.NewServeMux()
	mux.Handle("/ws", srv.Handler())
	hs := httptest.NewServer(mux)
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"

	// --- generate two device keypairs ---
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

	// --- host: UDP + QUIC listener + signaling client ---
	hostUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("host ListenUDP: %v", err)
	}
	defer hostUDP.Close()
	hostListenAddr := hostUDP.LocalAddr().(*net.UDPAddr)

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

	// QUIC accept goroutine that does Hello + Echo.
	hostDone := make(chan error, 1)
	go func() { hostDone <- runEchoHost(ctx, hostListener) }()

	hostSC, err := signaling.Dial(ctx, signaling.DialOptions{
		URL: wsURL, UserID: "alice", Secret: secret, DeviceID: hostDevID,
		PublicKey: hostPub, Kind: signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST,
		ClientID: "test-host",
	})
	if err != nil {
		t.Fatalf("host signaling.Dial: %v", err)
	}
	defer hostSC.Close()

	// Host signaling read loop: reply to Connect with own candidates.
	go func() {
		for {
			c, err := hostSC.Recv(ctx)
			if err != nil {
				return
			}
			_ = hostSC.SendConnect(ctx, c.GetFromDeviceId(), []*signalv1.EndpointCandidate{
				{Address: hostListenAddr.IP.String(), Port: uint32(hostListenAddr.Port),
					Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},
			})
		}
	}()

	// --- client: UDP + signaling client + QUIC dial ---
	cliUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("cli ListenUDP: %v", err)
	}
	defer cliUDP.Close()
	cliListenAddr := cliUDP.LocalAddr().(*net.UDPAddr)
	cliT := transport.New(cliUDP, devtls.DevClientTLSConfig())
	defer cliT.Close()

	cliSC, err := signaling.Dial(ctx, signaling.DialOptions{
		URL: wsURL, UserID: "alice", Secret: secret, DeviceID: cliDevID,
		PublicKey: cliPub, Kind: signalv1.DeviceKind_DEVICE_KIND_CLI,
		ClientID: "test-cli",
	})
	if err != nil {
		t.Fatalf("cli signaling.Dial: %v", err)
	}
	defer cliSC.Close()

	if err := cliSC.SendConnect(ctx, hostDevID, []*signalv1.EndpointCandidate{
		{Address: cliListenAddr.IP.String(), Port: uint32(cliListenAddr.Port),
			Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},
	}); err != nil {
		t.Fatalf("cli SendConnect: %v", err)
	}
	reply, err := cliSC.Recv(ctx)
	if err != nil {
		t.Fatalf("cli Recv: %v", err)
	}
	if reply.GetFromDeviceId() != hostDevID {
		t.Fatalf("expected reply from %s, got %s", hostDevID, reply.GetFromDeviceId())
	}
	if len(reply.GetCandidates()) == 0 {
		t.Fatal("no candidates returned")
	}
	chosen := reply.GetCandidates()[0]
	dialAddr := &net.UDPAddr{IP: net.ParseIP(chosen.GetAddress()), Port: int(chosen.GetPort())}

	// --- QUIC dial + Hello + Exec round-trip ---
	conn, err := cliT.Dial(ctx, dialAddr)
	if err != nil {
		t.Fatalf("Dial QUIC: %v", err)
	}
	defer conn.CloseWithError(0, "")

	ctrl, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream(control): %v", err)
	}
	if err := wire.Write(ctrl, &v1.ClientHello{ProtocolVersion: 1, ClientId: "test-cli"}); err != nil {
		t.Fatalf("write ClientHello: %v", err)
	}
	_ = ctrl.Close()
	r := wire.NewReader(ctrl)
	srvHello := &v1.ServerHello{}
	if err := wire.Read(r, srvHello); err != nil {
		t.Fatalf("read ServerHello: %v", err)
	}
	if srvHello.GetProtocolVersion() != 1 {
		t.Fatalf("server protocol_version: %d", srvHello.GetProtocolVersion())
	}

	// One per-command stream: ExecRequest → ExecResponse(stdout=cmd, done=true).
	exec, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream(exec): %v", err)
	}
	if err := wire.Write(exec, &v1.ExecRequest{Command: "hello-world"}); err != nil {
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
	if got != "hello-world" {
		t.Fatalf("expected echo %q, got %q", "hello-world", got)
	}
}

// runEchoHost accepts QUIC connections and echoes ExecRequest.command back
// as ExecResponse stdout, terminated by done=true.
func runEchoHost(ctx context.Context, l *transport.Listener) error {
	for {
		conn, err := l.Accept(ctx)
		if err != nil {
			return err
		}
		go func(c *transport.Conn) {
			defer c.CloseWithError(0, "")
			ctrl, err := c.AcceptStream(ctx)
			if err != nil {
				return
			}
			r := wire.NewReader(ctrl)
			ch := &v1.ClientHello{}
			if err := wire.Read(r, ch); err != nil {
				return
			}
			_ = wire.Write(ctrl, &v1.ServerHello{ProtocolVersion: 1, ServerId: "echo-test"})
			for {
				s, err := c.AcceptStream(ctx)
				if err != nil {
					return
				}
				go func(s *transport.Stream) {
					defer s.Close()
					req := &v1.ExecRequest{}
					if err := wire.Read(wire.NewReader(s), req); err != nil {
						return
					}
					_ = wire.Write(s, &v1.ExecResponse{
						Chunk: &v1.ExecResponse_Stdout{Stdout: []byte(req.GetCommand())},
					})
					_ = wire.Write(s, &v1.ExecResponse{Done: true})
				}(s)
			}
		}(conn)
	}
}
