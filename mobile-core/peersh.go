package peersh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/peersh/peersh/core/devid"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/punching"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
	"github.com/peersh/peersh/core/wire"
)

// Build is updated when a new mobile-core release is cut. The Flutter app
// reads this via Version() to surface in About / debug screens.
const Build = "mobile-core/0.2+phase4b"

// Version returns the mobile-core build identifier. Smoke test for "is the
// gomobile bind alive at all".
func Version() string { return Build }

// Output is the gomobile callback interface that platform code (Kotlin /
// Swift) implements to receive streamed exec output. The methods are
// invoked from a Go-side worker goroutine; the platform side is expected
// to forward the events to a Flutter EventChannel sink.
//
// The signatures use only []byte and string so gomobile's Java / ObjC
// bindings can be generated without further annotations.
type Output interface {
	OnStdout(data []byte)
	OnStderr(data []byte)
	// OnDone is called exactly once. errMessage is "" on clean success,
	// non-empty on failure.
	OnDone(errMessage string)
}

// Session is one QUIC connection to a peersh host. Methods are safe for
// concurrent use only when callers serialize Exec calls (one Exec at a
// time per Session).
type Session struct {
	pc   net.PacketConn
	tr   *transport.Transport
	conn *transport.Conn

	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex
}

// OpenDirectSession dials addr (host:port) over QUIC and runs Hello. No
// signaling, no auth. Used by the spike screen and dev workflows.
func OpenDirectSession(addr string) (*Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	uaddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", addr, err)
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("ListenUDP: %w", err)
	}
	tr := transport.New(pc, devtls.DevClientTLSConfig())
	conn, err := tr.Dial(ctx, uaddr)
	if err != nil {
		_ = tr.Close()
		_ = pc.Close()
		return nil, fmt.Errorf("dial: %w", err)
	}
	if err := doHello(ctx, conn); err != nil {
		_ = conn.CloseWithError(0, "")
		_ = tr.Close()
		_ = pc.Close()
		return nil, err
	}
	sCtx, sCancel := context.WithCancel(context.Background())
	return &Session{pc: pc, tr: tr, conn: conn, ctx: sCtx, cancel: sCancel}, nil
}

// OpenSignalingSession registers with a signaling server, requests a
// Connect to targetDeviceID, runs STUN + Punch + sequential dial, and
// runs Hello. stunServer = "" disables STUN (HOST-only candidates).
func OpenSignalingSession(signalingURL, userID, pskHex, targetDeviceID, stunServer string) (*Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	secret, err := hex.DecodeString(strings.TrimSpace(pskHex))
	if err != nil {
		return nil, fmt.Errorf("decode psk: %w", err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate device key: %w", err)
	}
	deviceID := devid.Derive(pub)

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("ListenUDP: %w", err)
	}

	var srflx *net.UDPAddr
	if stunServer != "" {
		stunCtx, stunCancel := context.WithTimeout(ctx, 4*time.Second)
		srflx, _ = punching.Discover(stunCtx, pc, punching.Options{STUNServer: stunServer})
		stunCancel()
	}

	tr := transport.New(pc, devtls.DevClientTLSConfig())

	sc, err := signaling.Dial(ctx, signaling.DialOptions{
		URL:         signalingURL,
		UserID:      userID,
		Secret:      secret,
		DeviceID:    deviceID,
		PublicKey:   pub,
		Kind:        signalv1.DeviceKind_DEVICE_KIND_MOBILE_CLIENT,
		DisplayName: "peersh-mobile",
		ClientID:    "mobile-core/0.2",
	})
	if err != nil {
		_ = tr.Close()
		_ = pc.Close()
		return nil, fmt.Errorf("signaling.Dial: %w", err)
	}
	defer sc.Close()

	cands := localCandidates(pc.LocalAddr().(*net.UDPAddr), srflx)
	if err := sc.SendConnect(ctx, targetDeviceID, cands); err != nil {
		_ = tr.Close()
		_ = pc.Close()
		return nil, fmt.Errorf("SendConnect: %w", err)
	}
	reply, err := sc.Recv(ctx)
	if err != nil {
		_ = tr.Close()
		_ = pc.Close()
		return nil, fmt.Errorf("recv reply: %w", err)
	}
	if reply.GetFromDeviceId() != targetDeviceID {
		_ = tr.Close()
		_ = pc.Close()
		return nil, fmt.Errorf("got Connect from %q, expected %q", reply.GetFromDeviceId(), targetDeviceID)
	}
	sortedPeer := punching.SortCandidates(reply.GetCandidates())
	peerAddrs := punching.CandidatesToUDPAddrs(sortedPeer)
	if len(peerAddrs) == 0 {
		_ = tr.Close()
		_ = pc.Close()
		return nil, errors.New("target returned no candidates")
	}
	_ = punching.Punch(ctx, pc, peerAddrs, punching.Options{})

	var conn *transport.Conn
	for _, p := range peerAddrs {
		dialCtx, dCancel := context.WithTimeout(ctx, 2*time.Second)
		c, dErr := tr.Dial(dialCtx, p)
		dCancel()
		if dErr == nil {
			conn = c
			break
		}
	}
	if conn == nil {
		_ = tr.Close()
		_ = pc.Close()
		return nil, punching.ErrTraversalFailed
	}

	if err := doHello(ctx, conn); err != nil {
		_ = conn.CloseWithError(0, "")
		_ = tr.Close()
		_ = pc.Close()
		return nil, err
	}

	sCtx, sCancel := context.WithCancel(context.Background())
	return &Session{pc: pc, tr: tr, conn: conn, ctx: sCtx, cancel: sCancel}, nil
}

// Exec runs command on the session and streams output to handler. The
// call returns when handler.OnDone has fired. Only one Exec at a time
// per Session; concurrent Execs serialize on an internal mutex.
func (s *Session) Exec(command string, handler Output) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.execLocked(command, handler)
}

func (s *Session) execLocked(command string, handler Output) error {
	stream, err := s.conn.OpenStream(s.ctx)
	if err != nil {
		handler.OnDone(err.Error())
		return err
	}
	defer stream.Close()

	if err := wire.Write(stream, &v1.ExecRequest{Command: command}); err != nil {
		handler.OnDone(err.Error())
		return err
	}
	r := wire.NewReader(stream)
	for {
		resp := &v1.ExecResponse{}
		if err := wire.Read(r, resp); err != nil {
			if errors.Is(err, io.EOF) {
				handler.OnDone("")
				return nil
			}
			msg := err.Error()
			handler.OnDone(msg)
			return err
		}
		if d := resp.GetStdout(); len(d) > 0 {
			// gomobile copies the slice across the bind boundary; we
			// hand over the bytes as-is.
			handler.OnStdout(d)
		}
		if d := resp.GetStderr(); len(d) > 0 {
			handler.OnStderr(d)
		}
		if resp.GetDone() {
			handler.OnDone("")
			return nil
		}
	}
}

// ReadFile is a convenience wrapper that runs
//
//	Get-Content -Raw -Encoding UTF8 -LiteralPath '<path>'
//
// against the session and returns the captured stdout. Used by the
// built-in text viewer. On failure the returned string starts with
// "ERROR: " (gomobile-friendly).
func (s *Session) ReadFile(path string) string {
	quoted := strings.ReplaceAll(path, "'", "''")
	cmd := "Get-Content -Raw -Encoding UTF8 -LiteralPath '" + quoted + "'"
	h := newCollector()
	if err := s.Exec(cmd, h); err != nil {
		return "ERROR: " + err.Error()
	}
	if h.errMsg != "" {
		return "ERROR: " + h.errMsg
	}
	return string(h.stdout)
}

// Close shuts down the QUIC connection and releases the underlying UDP
// socket. Safe to call multiple times.
func (s *Session) Close() error {
	s.cancel()
	if s.conn != nil {
		_ = s.conn.CloseWithError(0, "")
	}
	if s.tr != nil {
		_ = s.tr.Close()
	}
	if s.pc != nil {
		return s.pc.Close()
	}
	return nil
}

// Echo dials a peersh host directly at addr (host:port), runs the
// QUIC ClientHello/ServerHello on stream 0, sends one ExecRequest with
// the given command on a fresh stream, drains stdout/stderr, and returns
// the concatenated stdout text.
//
// Failures are returned as the string "ERROR: " + reason. This sacrifices
// type safety for gomobile-friendliness (no errors crossing the bind
// boundary).
func Echo(addr string, command string) string {
	s, err := OpenDirectSession(addr)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	defer s.Close()
	h := newCollector()
	if err := s.Exec(command, h); err != nil {
		return "ERROR: " + err.Error()
	}
	if h.errMsg != "" {
		return "ERROR: " + h.errMsg
	}
	return string(h.stdout)
}

// --- internal helpers (not exported via gomobile) -----------------------

// doHello runs ClientHello/ServerHello on a fresh control stream.
func doHello(ctx context.Context, conn *transport.Conn) error {
	ctrl, err := conn.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("control stream: %w", err)
	}
	if err := wire.Write(ctrl, &v1.ClientHello{ProtocolVersion: 1, ClientId: "mobile-core"}); err != nil {
		return fmt.Errorf("write ClientHello: %w", err)
	}
	_ = ctrl.Close()
	srv := &v1.ServerHello{}
	if err := wire.Read(wire.NewReader(ctrl), srv); err != nil {
		return fmt.Errorf("read ServerHello: %w", err)
	}
	if srv.GetProtocolVersion() != 1 {
		return fmt.Errorf("server protocol_version %d, expected 1", srv.GetProtocolVersion())
	}
	return nil
}

// localCandidates produces the candidate list this end advertises in
// signaling Connect messages.
func localCandidates(local *net.UDPAddr, srflx *net.UDPAddr) []*signalv1.EndpointCandidate {
	port := uint32(local.Port)
	var out []*signalv1.EndpointCandidate
	if srflx != nil {
		out = append(out, &signalv1.EndpointCandidate{
			Address: srflx.IP.String(), Port: uint32(srflx.Port),
			Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE,
		})
	}
	if !local.IP.IsUnspecified() {
		out = append(out, &signalv1.EndpointCandidate{
			Address: local.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST,
		})
		return out
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() || ipnet.IP.IsLinkLocalMulticast() {
			continue
		}
		out = append(out, &signalv1.EndpointCandidate{
			Address: ipnet.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST,
		})
	}
	return out
}

// collector is the in-process Output that buffers everything for ReadFile
// and Echo. Not exported via gomobile.
type collector struct {
	stdout []byte
	stderr []byte
	errMsg string
}

func newCollector() *collector { return &collector{} }

func (c *collector) OnStdout(data []byte)      { c.stdout = append(c.stdout, data...) }
func (c *collector) OnStderr(data []byte)      { c.stderr = append(c.stderr, data...) }
func (c *collector) OnDone(errMessage string)  { c.errMsg = errMessage }
