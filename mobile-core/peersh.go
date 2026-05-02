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
	"github.com/peersh/peersh/core/transport/peertls"
	"github.com/peersh/peersh/core/wire"
)

// Build is updated when a new mobile-core release is cut. The Flutter app
// reads this via Version() to surface in About / debug screens.
const Build = "mobile-core/0.3+phase8t1"

// protocolVersion is the wire-format version this build speaks. Bumped
// from 1 to 2 in Phase 8 when the per-stream first frame switched from
// raw ExecRequest to a StreamRequest envelope.
const protocolVersion = 2

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

	mu        sync.Mutex
	lastPTYID int64 // monotonically increasing, used as PTYRequest.pty_id
}

// OpenDirectSession dials addr (host:port) over QUIC and runs Hello. No
// signaling, no target device_id pin. Used by the spike screen and dev
// workflows. peershd requires a client cert (mTLS) so we still present
// one, but we cannot verify the server's identity without an expected
// device_id supplied through some other channel.
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
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("generate device key: %w", err)
	}
	cert, err := peertls.CertFromEd25519(priv)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("build client cert: %w", err)
	}
	tr := transport.New(pc, peertls.ClientTLSConfig(cert, ""))
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

// OpenFirebaseSignalingSession is the Phase 5b counterpart to
// OpenSignalingSession: instead of a PSK + user id pair, the caller
// supplies a Firebase ID token (obtained on the platform side via
// google_sign_in + firebase_auth.signInWithCredential). The server's
// firebase auth provider verifies the token and uses its uid as the
// peersh user_id. Everything else (STUN, signaling Connect, NAT punch,
// QUIC dial, Hello) is identical.
//
// firebaseAppCheckToken is forwarded as the App Check token on the
// Register frame; pass an empty string when App Check is not in use.
func OpenFirebaseSignalingSession(signalingURL, firebaseIDToken, firebaseAppCheckToken, targetDeviceID, stunServer string) (*Session, error) {
	return openSignalingInternal(signalingURL, "", nil, firebaseIDToken, firebaseAppCheckToken, targetDeviceID, stunServer)
}

// OpenSignalingSession registers with a signaling server, requests a
// Connect to targetDeviceID, runs STUN + Punch + sequential dial, and
// runs Hello. stunServer = "" disables STUN (HOST-only candidates).
func OpenSignalingSession(signalingURL, userID, pskHex, targetDeviceID, stunServer string) (*Session, error) {
	secret, err := hex.DecodeString(strings.TrimSpace(pskHex))
	if err != nil {
		return nil, fmt.Errorf("decode psk: %w", err)
	}
	return openSignalingInternal(signalingURL, userID, secret, "", "", targetDeviceID, stunServer)
}

func openSignalingInternal(signalingURL, userID string, secret []byte, firebaseIDToken, firebaseAppCheckToken, targetDeviceID, stunServer string) (*Session, error) {
	// targetDeviceID drives the QUIC mTLS pin; an empty string would
	// silently fall through to "no pin" and undo the protection mTLS is
	// supposed to provide. Reject at the API boundary so a misbehaving
	// caller cannot accidentally open an unauthenticated session.
	if targetDeviceID == "" {
		return nil, errors.New("targetDeviceID is required in signaling mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// One ed25519 keypair drives both the signaling Register frame
	// (via devid.Derive on the pubkey) and the QUIC mTLS client cert,
	// so the host sees a single identity on both channels.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate device key: %w", err)
	}
	deviceID := devid.Derive(pub)
	clientCert, err := peertls.CertFromEd25519(priv)
	if err != nil {
		return nil, fmt.Errorf("build client cert: %w", err)
	}

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

	// Pin the host's expected device_id at the TLS layer; a server
	// presenting a cert that hashes to a different ID will fail the
	// handshake before any application bytes flow.
	tr := transport.New(pc, peertls.ClientTLSConfig(clientCert, targetDeviceID))

	sc, err := signaling.Dial(ctx, signaling.DialOptions{
		URL:                   signalingURL,
		UserID:                userID,
		Secret:                secret,
		FirebaseIDToken:       firebaseIDToken,
		FirebaseAppCheckToken: firebaseAppCheckToken,
		DeviceID:              deviceID,
		PublicKey:             pub,
		Kind:                  signalv1.DeviceKind_DEVICE_KIND_MOBILE_CLIENT,
		DisplayName:           "peersh-mobile",
		ClientID:              "mobile-core/0.3",
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

	if err := wire.Write(stream, &v1.StreamRequest{
		Kind: &v1.StreamRequest_Exec{Exec: &v1.ExecRequest{Command: command}},
	}); err != nil {
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

// --- PTY (interactive) ---------------------------------------------------

// PTYHandler is the platform-side callback that receives PTY output bytes
// and a final exit notification. Like Output, the methods are invoked
// from a Go-side worker goroutine; the platform side forwards them to a
// Flutter EventChannel sink. Bytes are merged stdout/stderr from ConPTY
// (no separate channel tagging).
type PTYHandler interface {
	OnData(data []byte)
	// OnExit is called exactly once when the child process terminates or
	// the stream tears down. exitCode is the process exit status; -1 if
	// unknown. errMessage is "" on clean exit, non-empty for failures.
	OnExit(exitCode int, errMessage string)
}

// PTYSession is one open pseudo-console on the peersh host. The platform
// side calls Write to forward keystrokes, Resize when the local terminal
// changes dimensions, and Close to terminate.
type PTYSession struct {
	stream *transport.Stream
	parent *Session
	id     int64
	handle string // server-assigned reattach handle (Phase 6b Tier 2)

	mu     sync.Mutex
	closed bool

	// writeMu serializes wire.Write calls (the read pump and the
	// platform-side input goroutine both write).
	writeMu sync.Mutex

	pumpDone chan struct{}
}

// ID returns the client-assigned PTY id this session was opened under.
// Used by the file-API helpers (Session.GetCWD, ListSessionFiles, etc.)
// to address the right host-side PTY.
func (p *PTYSession) ID() int64 { return p.id }

// Handle returns the server-assigned reattach handle, set after the
// PTY's first PTYReattachAck arrives. Empty until the ack frame has
// landed; callers needing a guaranteed-non-empty value should call
// HandleAfterFirstFrame which blocks briefly.
func (p *PTYSession) Handle() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handle
}

// OpenPTY opens a pseudo-console on the host and starts streaming output
// to handler. command empty / "auto" / "pwsh" / "powershell" / "cmd" picks
// the operator-configured default shell (with the OSC 9;9 prompt
// instrumentation); any other value is run verbatim with args.
//
// One PTYSession per Session is the supported topology for Tier 1; the
// caller may run a PTY and one-shot Execs concurrently against the same
// Session because each lives on its own QUIC stream.
func (s *Session) OpenPTY(command string, cols, rows int, handler PTYHandler) (*PTYSession, error) {
	return s.openPTYInternal(command, "", cols, rows, handler)
}

// OpenPTYReattach reattaches to a previously-persisted PTY by its
// server-assigned handle. cols/rows are still applied to resize the
// existing pseudo-console. The host streams the scrollback ring buffer
// before live data resumes.
//
// Reattach can fail if the handle is unknown, expired, or another
// client is currently bound; in that case the handler's OnExit fires
// with the rejection reason and PTYSession.Close should still be
// called to release the local stream.
func (s *Session) OpenPTYReattach(reattachHandle string, cols, rows int, handler PTYHandler) (*PTYSession, error) {
	if reattachHandle == "" {
		return nil, errors.New("OpenPTYReattach: empty handle")
	}
	return s.openPTYInternal("", reattachHandle, cols, rows, handler)
}

func (s *Session) openPTYInternal(command, reattachHandle string, cols, rows int, handler PTYHandler) (*PTYSession, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	id := s.nextPTYID()
	stream, err := s.conn.OpenStream(s.ctx)
	if err != nil {
		return nil, fmt.Errorf("OpenStream: %w", err)
	}
	if err := wire.Write(stream, &v1.StreamRequest{
		Kind: &v1.StreamRequest_Pty{Pty: &v1.PTYRequest{
			Command:        command,
			Cols:           uint32(cols),
			Rows:           uint32(rows),
			PtyId:          id,
			ReattachHandle: reattachHandle,
		}},
	}); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("write StreamRequest: %w", err)
	}

	p := &PTYSession{stream: stream, parent: s, id: id, pumpDone: make(chan struct{})}
	go p.pump(handler)
	return p, nil
}

// nextPTYID returns a fresh client-side ID for a new PTY session. Used to
// address the host-side PTY in subsequent file-API requests.
func (s *Session) nextPTYID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPTYID++
	return s.lastPTYID
}

func (p *PTYSession) pump(handler PTYHandler) {
	defer close(p.pumpDone)
	r := wire.NewReader(p.stream)
	for {
		frame := &v1.PTYFrame{}
		if err := wire.Read(r, frame); err != nil {
			handler.OnExit(-1, err.Error())
			return
		}
		switch k := frame.GetKind().(type) {
		case *v1.PTYFrame_ReattachAck:
			ack := k.ReattachAck
			p.mu.Lock()
			p.handle = ack.GetHandle()
			p.mu.Unlock()
			if !ack.GetAccepted() {
				// Reattach refused; surface as an error exit so the
				// client UI can react.
				handler.OnExit(-1, "reattach refused: "+ack.GetReason())
				return
			}
		case *v1.PTYFrame_Data:
			if d := k.Data.GetData(); len(d) > 0 {
				handler.OnData(d)
			}
		case *v1.PTYFrame_Exit:
			handler.OnExit(int(k.Exit.GetExitCode()), k.Exit.GetError())
			return
		default:
			// Server should not send Input/Resize on this stream.
		}
	}
}

// Write forwards keystrokes / paste payloads from the local user to the
// remote child process.
func (p *PTYSession) Write(data []byte) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("ptysession: closed")
	}
	p.mu.Unlock()
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return wire.Write(p.stream, &v1.PTYFrame{
		Kind: &v1.PTYFrame_Input{Input: &v1.PTYInput{Data: data}},
	})
}

// Resize tells the remote pseudo-console the terminal grid changed.
func (p *PTYSession) Resize(cols, rows int) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("ptysession: closed")
	}
	p.mu.Unlock()
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return wire.Write(p.stream, &v1.PTYFrame{
		Kind: &v1.PTYFrame_Resize{Resize: &v1.PTYResize{Cols: uint32(cols), Rows: uint32(rows)}},
	})
}

// Close terminates the PTY by closing the underlying QUIC stream. The
// host's pseudo-console pump observes EOF and tears down the child.
func (p *PTYSession) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	err := p.stream.Close()
	<-p.pumpDone
	return err
}

// --- Session lifecycle (continued) ---------------------------------------

// --- Session-scoped file API (Phase 8 Tier 2) ---------------------------

// FileEntry is the gomobile-friendly mirror of v1.FileEntry. Methods are
// non-pointer so the binding generator can expose them as Java fields.
type FileEntry struct {
	Name           string
	Path           string
	IsDir          bool
	Size           int64
	ModifiedUnixMs int64
}

// FileEntryList is a thin wrapper around []FileEntry for gomobile. The
// generator does not expose Go slices directly, so we hide them behind
// indexed accessors.
type FileEntryList struct {
	entries []FileEntry
}

func (l *FileEntryList) Len() int { return len(l.entries) }
func (l *FileEntryList) Get(i int) *FileEntry {
	if i < 0 || i >= len(l.entries) {
		return nil
	}
	e := l.entries[i]
	return &e
}

// PTYHandle mirrors v1.PTYHandle for gomobile.
type PTYHandle struct {
	Handle         string
	Command        string
	Attached       bool
	CWD            string
	LastSeenUnixMs int64
}

// PTYHandleList wraps []PTYHandle.
type PTYHandleList struct {
	entries []PTYHandle
}

func (l *PTYHandleList) Len() int { return len(l.entries) }
func (l *PTYHandleList) Get(i int) *PTYHandle {
	if i < 0 || i >= len(l.entries) {
		return nil
	}
	e := l.entries[i]
	return &e
}

// FileContent is the gomobile-friendly result of ReadSessionFile.
type FileContent struct {
	Content   []byte
	Encoding  string
	Size      int64
	Truncated bool
	Error     string
}

// GetCWD asks the host for the current working directory of the PTY
// identified by ptyID (typically PTYSession.ID() of the Terminal screen
// the user is looking at). Returns "" if the host hasn't observed a
// prompt yet, or if the request failed.
func (s *Session) GetCWD(ptyID int64) string {
	resp, err := s.fileExchange(&v1.FilesRequest{
		Kind: &v1.FilesRequest_GetSession{GetSession: &v1.GetSessionRequest{PtyId: ptyID}},
	})
	if err != nil || resp == nil {
		return ""
	}
	if resp.GetError() != "" {
		return ""
	}
	return resp.GetGetSession().GetCwd()
}

// ListSessionFiles enumerates entries at `path` (cwd-relative) inside
// the host's CWD. Returns nil and an error string in FileContent.Error
// when the request fails.
func (s *Session) ListSessionFiles(ptyID int64, path string) *FileEntryList {
	resp, err := s.fileExchange(&v1.FilesRequest{
		Kind: &v1.FilesRequest_ListFiles{ListFiles: &v1.ListSessionFilesRequest{PtyId: ptyID, Path: path}},
	})
	out := &FileEntryList{}
	if err != nil || resp == nil {
		return out
	}
	if resp.GetError() != "" {
		return out
	}
	for _, e := range resp.GetListFiles().GetEntries() {
		out.entries = append(out.entries, FileEntry{
			Name:           e.GetName(),
			Path:           e.GetPath(),
			IsDir:          e.GetIsDir(),
			Size:           e.GetSize(),
			ModifiedUnixMs: e.GetModifiedUnixMs(),
		})
	}
	return out
}

// ListPTYs enumerates the persisted PTYs the host is currently
// holding for this connection. Empty list when no PTYs are alive.
func (s *Session) ListPTYs() *PTYHandleList {
	resp, err := s.fileExchange(&v1.FilesRequest{
		Kind: &v1.FilesRequest_ListPtys{ListPtys: &v1.ListPTYsRequest{}},
	})
	out := &PTYHandleList{}
	if err != nil || resp == nil || resp.GetError() != "" {
		return out
	}
	for _, h := range resp.GetListPtys().GetPtys() {
		out.entries = append(out.entries, PTYHandle{
			Handle:         h.GetHandle(),
			Command:        h.GetCommand(),
			Attached:       h.GetAttached(),
			CWD:            h.GetCwd(),
			LastSeenUnixMs: h.GetLastSeenUnixMs(),
		})
	}
	return out
}

// KillPTY tears down a persisted PTY by its handle (closes the child
// process and drops the ring buffer immediately).
func (s *Session) KillPTY(handle string) string {
	resp, err := s.fileExchange(&v1.FilesRequest{
		Kind: &v1.FilesRequest_KillPty{KillPty: &v1.KillPTYRequest{Handle: handle}},
	})
	if err != nil {
		return err.Error()
	}
	if resp.GetError() != "" {
		return resp.GetError()
	}
	return ""
}

// ReadSessionFile fetches the contents of a cwd-relative file. Returns a
// FileContent with Error set on failure.
func (s *Session) ReadSessionFile(ptyID int64, path string, maxBytes int64) *FileContent {
	resp, err := s.fileExchange(&v1.FilesRequest{
		Kind: &v1.FilesRequest_ReadFile{ReadFile: &v1.ReadSessionFileRequest{PtyId: ptyID, Path: path, MaxBytes: maxBytes}},
	})
	if err != nil {
		return &FileContent{Error: err.Error()}
	}
	if resp == nil {
		return &FileContent{Error: "no response"}
	}
	if e := resp.GetError(); e != "" {
		return &FileContent{Error: e}
	}
	r := resp.GetReadFile()
	return &FileContent{
		Content:   r.GetContent(),
		Encoding:  r.GetEncoding(),
		Size:      r.GetSize(),
		Truncated: r.GetTruncated(),
	}
}

// fileExchange opens a fresh stream, sends a FilesRequest envelope,
// reads a single FilesResponse, and returns it. The stream is closed
// before returning.
func (s *Session) fileExchange(req *v1.FilesRequest) (*v1.FilesResponse, error) {
	stream, err := s.conn.OpenStream(s.ctx)
	if err != nil {
		return nil, fmt.Errorf("OpenStream: %w", err)
	}
	defer stream.Close()
	if err := wire.Write(stream, &v1.StreamRequest{
		Kind: &v1.StreamRequest_Files{Files: req},
	}); err != nil {
		return nil, fmt.Errorf("write FilesRequest: %w", err)
	}
	resp := &v1.FilesResponse{}
	if err := wire.Read(wire.NewReader(stream), resp); err != nil {
		return nil, fmt.Errorf("read FilesResponse: %w", err)
	}
	return resp, nil
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
	if err := wire.Write(ctrl, &v1.ClientHello{ProtocolVersion: protocolVersion, ClientId: "mobile-core"}); err != nil {
		return fmt.Errorf("write ClientHello: %w", err)
	}
	_ = ctrl.Close()
	srv := &v1.ServerHello{}
	if err := wire.Read(wire.NewReader(ctrl), srv); err != nil {
		return fmt.Errorf("read ServerHello: %w", err)
	}
	if srv.GetProtocolVersion() != protocolVersion {
		return fmt.Errorf("server protocol_version %d, expected %d", srv.GetProtocolVersion(), protocolVersion)
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
