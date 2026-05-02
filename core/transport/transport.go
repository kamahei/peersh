package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// ALPN is the negotiated application-layer protocol identifier for peersh.
// It is part of the wire contract and is bumped only when protocol_version
// changes.
const ALPN = "peersh/1"

// Transport is a thin wrapper around quic.Transport. The PacketConn passed to
// New is owned by the caller; Transport never creates its own UDP socket.
//
// This caller-owned-PacketConn shape is non-negotiable: Phase 3's UDP hole
// punching produces a punched *net.UDPConn that must be reusable as the
// QUIC transport. A wrapper that opens its own socket internally cannot be
// retrofitted, so the API is expressed this way from Phase 1.
type Transport struct {
	qt        *quic.Transport
	tlsConfig *tls.Config
}

// New wraps an externally-supplied PacketConn for use by QUIC.
//
// The tlsConfig must include ALPN entries that match the peersh wire
// contract; production callers use helpers in core/transport/peertls.
// core/transport/devtls produces conformant configs for tests.
//
// The caller retains ownership of pc. Calling (*Transport).Close does NOT
// close pc — that is the caller's responsibility.
func New(pc net.PacketConn, tlsConfig *tls.Config) *Transport {
	return &Transport{
		qt:        &quic.Transport{Conn: pc},
		tlsConfig: tlsConfig,
	}
}

// Listen binds a QUIC server endpoint to the underlying PacketConn.
func (t *Transport) Listen(_ context.Context) (*Listener, error) {
	l, err := t.qt.Listen(t.tlsConfig, defaultQUICConfig())
	if err != nil {
		return nil, fmt.Errorf("transport: Listen: %w", err)
	}
	return &Listener{l: l}, nil
}

// Dial opens a QUIC client connection to addr.
//
// addr must be a *net.UDPAddr (the only address type QUIC speaks).
func (t *Transport) Dial(ctx context.Context, addr net.Addr) (*Conn, error) {
	uaddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("transport: Dial requires *net.UDPAddr, got %T", addr)
	}
	qc, err := t.qt.Dial(ctx, uaddr, t.tlsConfig, defaultQUICConfig())
	if err != nil {
		return nil, fmt.Errorf("transport: Dial: %w", err)
	}
	return &Conn{qc: qc}, nil
}

// Close releases Transport-internal resources (e.g. background goroutines).
// It does NOT close the underlying PacketConn.
func (t *Transport) Close() error {
	return t.qt.Close()
}

// Listener accepts incoming QUIC connections.
type Listener struct {
	l *quic.Listener
}

// Accept blocks until a new connection arrives.
func (l *Listener) Accept(ctx context.Context) (*Conn, error) {
	qc, err := l.l.Accept(ctx)
	if err != nil {
		return nil, err
	}
	return &Conn{qc: qc}, nil
}

// Addr returns the local listening address.
func (l *Listener) Addr() net.Addr { return l.l.Addr() }

// Close stops accepting and closes the listener (but not the Transport's
// PacketConn).
func (l *Listener) Close() error { return l.l.Close() }

// Conn is one QUIC connection.
type Conn struct {
	qc *quic.Conn
}

// OpenStream opens a new bidirectional stream on this connection.
func (c *Conn) OpenStream(ctx context.Context) (*Stream, error) {
	s, err := c.qc.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &Stream{s: s}, nil
}

// AcceptStream accepts an incoming bidirectional stream.
func (c *Conn) AcceptStream(ctx context.Context) (*Stream, error) {
	s, err := c.qc.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return &Stream{s: s}, nil
}

// LocalAddr returns the local end of the connection.
func (c *Conn) LocalAddr() net.Addr { return c.qc.LocalAddr() }

// RemoteAddr returns the remote end of the connection.
func (c *Conn) RemoteAddr() net.Addr { return c.qc.RemoteAddr() }

// CloseWithError closes the QUIC connection and signals the given application
// error code to the peer. err may be nil for a clean close.
func (c *Conn) CloseWithError(code uint64, msg string) error {
	return c.qc.CloseWithError(quic.ApplicationErrorCode(code), msg)
}

// Stream is one bidirectional QUIC stream.
type Stream struct {
	s *quic.Stream
}

// Read implements io.Reader.
func (s *Stream) Read(p []byte) (int, error) { return s.s.Read(p) }

// Write implements io.Writer.
func (s *Stream) Write(p []byte) (int, error) { return s.s.Write(p) }

// Close closes the write side of the stream. The read side closes when the
// peer closes its write side. To abort both sides, call CancelRead and
// CancelWrite or close the parent Conn.
func (s *Stream) Close() error { return s.s.Close() }

// StreamID returns the QUIC stream id, useful for log correlation.
func (s *Stream) StreamID() int64 { return int64(s.s.StreamID()) }

// Compile-time interface checks.
var (
	_ io.ReadWriteCloser = (*Stream)(nil)
)

// ErrAddrType is returned by Dial when given an address that isn't a
// *net.UDPAddr.
var ErrAddrType = errors.New("transport: address must be *net.UDPAddr")

func defaultQUICConfig() *quic.Config {
	return &quic.Config{
		HandshakeIdleTimeout: 10 * time.Second,
		MaxIdleTimeout:       60 * time.Second,
		KeepAlivePeriod:      15 * time.Second,
	}
}
