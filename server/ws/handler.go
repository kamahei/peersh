// Package ws hosts the WebSocket upgrade and per-connection frame loop.
package ws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/peersh/peersh/core/auth"
	fbauth "github.com/peersh/peersh/core/auth/firebase"
	"github.com/peersh/peersh/core/auth/psk"
	"github.com/peersh/peersh/core/devid"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/server/ratelimit"
	"github.com/peersh/peersh/server/room"
	"google.golang.org/protobuf/proto"
	"nhooyr.io/websocket"
)

// ProtocolVersion is the signaling-channel wire version. Locked at 1.
const ProtocolVersion = 1

// Server is the dependency-injected handler for /ws. Construct via New and
// register with a *http.ServeMux.
type Server struct {
	ServerID string
	Store    store.Store

	// Auth is the auth.Provider used at Register. Phase 2 set it to a
	// *psk.Provider directly; Phase 5 introduced firebase as a sibling
	// option, so the field is now an interface and main.go constructs
	// the right concrete provider per the [auth_provider] config.
	Auth auth.Provider

	// AuthBuilder turns a Register message into the auth.Credentials
	// shape Auth.Authenticate expects. main.go selects the builder based
	// on the configured auth_provider (PSK reads hmac_signature; Firebase
	// reads firebase_id_token).
	AuthBuilder CredentialsBuilder

	// AuthKind is recorded with newly-created users so store.User.AuthProvider
	// matches the runtime provider.
	AuthKind store.AuthProvider

	// AppCheckVerifier is consulted on every Register frame when set.
	// Nil disables App Check verification entirely. When non-nil, every
	// missing-or-invalid token is logged; the AppCheckRequired flag
	// decides whether to reject the connection.
	AppCheckVerifier fbauth.AppCheckVerifier

	// AppCheckRequired forces rejection of Register frames missing a
	// valid App Check token. Effective only when AppCheckVerifier is
	// also set.
	AppCheckRequired bool

	Registry    *room.Registry
	IPLimit     *ratelimit.Bucket // upgrade-time per-IP
	UserLimit   *ratelimit.Bucket // per user_id at Register
	DeviceLimit *ratelimit.Bucket // per device_id at Connect
	Logger      *slog.Logger

	// IdleTimeout caps how long a registered connection may sit
	// without sending any frame before the server closes it with a
	// ServerError("idle_timeout"). This bounds Cloud Run billing
	// when a client (host or mobile) freezes mid-session and stops
	// sending Connects without closing the WS. The wake-listener
	// path in v2-A typically holds the WS for under 20 seconds, so
	// 60 s is comfortably above normal operation.
	//
	// 0 means use the default (60 s). Negative values disable the
	// idle close entirely; tests use a small positive value so they
	// don't wait the full minute.
	IdleTimeout time.Duration

	// Metrics is optional. Nil disables collection (the helper methods
	// on *Metrics are nil-safe).
	Metrics *Metrics
}

// CredentialsBuilder extracts auth.Credentials from a signaling Register
// message. The shape varies by auth.Provider implementation.
type CredentialsBuilder func(reg *signalv1.Register) (auth.Credentials, error)

// PSKCredentialsBuilder is the default builder for PSK mode. main.go uses
// it directly for configurations with auth_provider = "psk".
func PSKCredentialsBuilder(reg *signalv1.Register) (auth.Credentials, error) {
	return psk.CredentialsFromRegister(reg)
}

// New constructs a Server with sensible defaults filled in for any nil
// rate-limit buckets. The required deps (Store, Auth, Registry) must be
// non-nil.
func New(s *Server) *Server {
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	if s.IPLimit == nil {
		s.IPLimit = ratelimit.New(10.0/60.0, 3) // 10/min burst 3
	}
	if s.UserLimit == nil {
		s.UserLimit = ratelimit.New(10.0/60.0, 3)
	}
	if s.DeviceLimit == nil {
		s.DeviceLimit = ratelimit.New(30.0/60.0, 5) // 30/min burst 5
	}
	if s.ServerID == "" {
		s.ServerID = "peersh-signaling/0.1"
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = 60 * time.Second
	}
	return s
}

// Handler returns the http.Handler that accepts /ws upgrades.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	clientIP := remoteIP(r)
	if !s.IPLimit.Allow(clientIP) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		s.Logger.Info("ws: ip rate-limited", "ip", clientIP)
		return
	}

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Phase 2 deployment is operator-controlled; permitting any origin
		// is acceptable. Production / Phase 5+ should pin origins.
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.Logger.Warn("ws: upgrade failed", "ip", clientIP, "err", err)
		return
	}
	wsConn.SetReadLimit(1 << 20) // 1 MiB

	c := &Connection{
		ws:     wsConn,
		ip:     clientIP,
		log:    s.Logger.With("ip", clientIP),
		server: s,
	}
	c.run(r.Context())
}

// remoteIP returns the client IP from RemoteAddr or X-Forwarded-For.
func remoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry is the original client when behind a trusted proxy.
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Connection is one accepted WebSocket session. It implements room.Conn
// after Register completes.
type Connection struct {
	ws     *websocket.Conn
	ip     string
	log    *slog.Logger
	server *Server

	// Set after a successful Register.
	mu       sync.Mutex
	userID   string
	deviceID string

	registered       bool
	registeredAt     time.Time // for SessionDuration observation
	firstConnectSeen bool      // tracks the first Connect on this conn
}

// UserID implements room.Conn.
func (c *Connection) UserID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.userID
}

// DeviceID implements room.Conn.
func (c *Connection) DeviceID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deviceID
}

// Send marshals frame and writes it as one binary WebSocket message.
func (c *Connection) Send(ctx context.Context, frame *signalv1.Frame) error {
	data, err := proto.Marshal(frame)
	if err != nil {
		return fmt.Errorf("ws: marshal: %w", err)
	}
	return c.ws.Write(ctx, websocket.MessageBinary, data)
}

// run drives the full per-connection state machine.
func (c *Connection) run(ctx context.Context) {
	defer func() {
		if c.registered {
			c.server.Registry.Unregister(c)
			c.log.Info("ws: connection closed", "user", c.userID, "device", c.deviceID)
			c.server.Metrics.observeSessionDuration(time.Since(c.registeredAt).Seconds())
		}
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
		c.server.Metrics.observeConnectionClosed()
	}()

	if err := c.handshake(ctx); err != nil {
		c.log.Info("ws: handshake failed", "err", err)
		_ = c.sendServerError(ctx, "handshake_failed", err.Error())
		c.server.Metrics.observeRegisterRejected("handshake_failed")
		return
	}
	c.registered = true
	c.registeredAt = time.Now()
	c.server.Metrics.observeRegisterAccepted()
	if prev := c.server.Registry.Register(c); prev != nil {
		c.log.Info("ws: replaced previous registration", "user", c.userID, "device", c.deviceID)
		_ = prev.Send(ctx, &signalv1.Frame{Body: &signalv1.Frame_ServerError{
			ServerError: &signalv1.ServerError{Code: "replaced", Message: "another device with this id reconnected"},
		}})
	}
	c.log.Info("ws: registered", "user", c.userID, "device", c.deviceID)

	for {
		f, err := c.readFrameWithIdleTimeout(ctx)
		if err != nil {
			if errors.Is(err, errIdleTimeout) {
				c.log.Info("ws: idle timeout", "after", c.server.IdleTimeout)
				_ = c.sendServerError(ctx, "idle_timeout", "no frame received within deadline")
				c.server.Metrics.observeConnectionIdleClosed()
				return
			}
			if !isCloseErr(err) {
				c.log.Info("ws: read error", "err", err)
			}
			return
		}
		if err := c.dispatch(ctx, f); err != nil {
			c.log.Info("ws: dispatch error", "err", err)
			_ = c.sendServerError(ctx, "dispatch_error", err.Error())
			return
		}
	}
}

// errIdleTimeout marks an idle-close exit so run() can distinguish it
// from generic read errors and emit the right ServerError code.
var errIdleTimeout = errors.New("idle timeout")

// readFrameWithIdleTimeout returns a frame, an idle-close signal, or
// the underlying read error. A negative IdleTimeout disables the
// deadline entirely.
//
// We can't simply pass a per-read context with a timeout because
// nhooyr.io/websocket tears down the entire WebSocket when its read
// context fires — which then prevents us from writing the
// ServerError("idle_timeout") frame the caller expects. Instead we
// run the read in a goroutine and race it against a timer; on
// timeout we leave the goroutine blocked (it will unblock when the
// caller's defer closes the WS) and signal errIdleTimeout. The
// channel is buffered so the orphaned goroutine does not leak.
func (c *Connection) readFrameWithIdleTimeout(parent context.Context) (*signalv1.Frame, error) {
	if c.server.IdleTimeout < 0 {
		return c.readFrame(parent)
	}
	type result struct {
		f   *signalv1.Frame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := c.readFrame(parent)
		ch <- result{f, err}
	}()
	timer := time.NewTimer(c.server.IdleTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.f, r.err
	case <-timer.C:
		return nil, errIdleTimeout
	case <-parent.Done():
		// Drain or return promptly; parent cancellation overrides idle.
		select {
		case r := <-ch:
			return r.f, r.err
		default:
			return nil, parent.Err()
		}
	}
}

func (c *Connection) handshake(ctx context.Context) error {
	hsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// 1. ClientHello → ServerHello
	hello, err := c.readFrame(hsCtx)
	if err != nil {
		return fmt.Errorf("read ClientHello: %w", err)
	}
	if hello.GetClientHello() == nil {
		return fmt.Errorf("expected ClientHello, got %T", hello.GetBody())
	}
	if v := hello.GetClientHello().GetProtocolVersion(); v != ProtocolVersion {
		return fmt.Errorf("client protocol_version=%d, server=%d", v, ProtocolVersion)
	}
	if err := c.Send(hsCtx, &signalv1.Frame{
		Body: &signalv1.Frame_ServerHello{ServerHello: &signalv1.ServerHello{
			ProtocolVersion: ProtocolVersion,
			ServerId:        c.server.ServerID,
		}},
	}); err != nil {
		return fmt.Errorf("send ServerHello: %w", err)
	}

	// 2. Register → RegisterAck
	regFrame, err := c.readFrame(hsCtx)
	if err != nil {
		return fmt.Errorf("read Register: %w", err)
	}
	reg := regFrame.GetRegister()
	if reg == nil {
		return fmt.Errorf("expected Register, got %T", regFrame.GetBody())
	}
	userLimitKey := reg.GetUserId()
	if userLimitKey == "" {
		userLimitKey = reg.GetDeviceId()
	}
	if !c.server.UserLimit.Allow(userLimitKey) {
		return fmt.Errorf("user rate limit")
	}
	if err := devid.Verify(reg.GetDeviceId(), reg.GetPublicKey()); err != nil {
		_ = c.Send(hsCtx, &signalv1.Frame{
			Body: &signalv1.Frame_RegisterAck{RegisterAck: &signalv1.RegisterAck{
				Accepted: false, Reason: "device identity: " + err.Error(), ServerId: c.server.ServerID,
			}},
		})
		return fmt.Errorf("device identity: %w", err)
	}

	if c.server.AppCheckVerifier != nil {
		if err := fbauth.VerifyAppCheck(c.server.AppCheckVerifier, reg.GetFirebaseAppCheckToken()); err != nil {
			c.server.Logger.Info("app check verification failed",
				"user_id", reg.GetUserId(),
				"required", c.server.AppCheckRequired,
				"err", err)
			if c.server.AppCheckRequired {
				_ = c.Send(hsCtx, &signalv1.Frame{
					Body: &signalv1.Frame_RegisterAck{RegisterAck: &signalv1.RegisterAck{
						Accepted: false, Reason: "app check: " + err.Error(), ServerId: c.server.ServerID,
					}},
				})
				return fmt.Errorf("app check: %w", err)
			}
		}
	}

	builder := c.server.AuthBuilder
	if builder == nil {
		builder = PSKCredentialsBuilder
	}
	creds, err := builder(reg)
	if err != nil {
		return fmt.Errorf("extract credentials: %w", err)
	}
	identity, err := c.server.Auth.Authenticate(hsCtx, creds)
	if err != nil {
		_ = c.Send(hsCtx, &signalv1.Frame{
			Body: &signalv1.Frame_RegisterAck{RegisterAck: &signalv1.RegisterAck{
				Accepted: false, Reason: "auth: " + err.Error(), ServerId: c.server.ServerID,
			}},
		})
		return fmt.Errorf("auth: %w", err)
	}

	// Persist Device + lazy User. (Use background ctx so a cancellation of
	// the request mid-write doesn't leave the registry inconsistent.)
	storeCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sCancel()
	authKind := c.server.AuthKind
	if authKind == store.AuthProviderUnknown {
		authKind = store.AuthProviderPSK
	}
	if _, err := c.server.Store.GetUser(storeCtx, identity.UserID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			_ = c.server.Store.PutUser(storeCtx, store.User{
				ID: identity.UserID, AuthProvider: authKind, CreatedAt: time.Now().UTC(),
			})
		}
	}
	now := time.Now().UTC()
	_ = c.server.Store.PutDevice(storeCtx, store.Device{
		ID: reg.GetDeviceId(), PublicKey: reg.GetPublicKey(), OwnerUserID: identity.UserID,
		Kind: deviceKindFromProto(reg.GetKind()), DisplayName: reg.GetDisplayName(),
		CreatedAt: now, LastSeenAt: now,
	})

	c.mu.Lock()
	c.userID = identity.UserID
	c.deviceID = reg.GetDeviceId()
	c.mu.Unlock()

	if err := c.Send(hsCtx, &signalv1.Frame{
		Body: &signalv1.Frame_RegisterAck{RegisterAck: &signalv1.RegisterAck{
			Accepted: true, ServerId: c.server.ServerID,
		}},
	}); err != nil {
		return fmt.Errorf("send RegisterAck: %w", err)
	}
	return nil
}

func (c *Connection) dispatch(ctx context.Context, f *signalv1.Frame) error {
	switch body := f.GetBody().(type) {
	case *signalv1.Frame_Connect:
		if !c.firstConnectSeen {
			c.firstConnectSeen = true
			c.server.Metrics.observeRegisterToFirstConnect(time.Since(c.registeredAt).Seconds())
		}
		if !c.server.DeviceLimit.Allow(c.deviceID) {
			c.server.Metrics.observeConnectRejected("rate_limit")
			return fmt.Errorf("device rate limit")
		}
		if err := c.server.Registry.Forward(ctx, c, body.Connect); err != nil {
			reason := classifyForwardErr(err)
			c.server.Metrics.observeConnectRejected(reason)
			_ = c.sendServerError(ctx, reason, err.Error())
			return nil // don't tear down on a forwarding miss
		}
		c.server.Metrics.observeConnectForwarded()
		return nil
	case *signalv1.Frame_ClientHello, *signalv1.Frame_Register:
		return fmt.Errorf("unexpected re-handshake frame after registration")
	default:
		c.log.Debug("ws: ignoring unexpected frame", "type", fmt.Sprintf("%T", body))
		return nil
	}
}

func classifyForwardErr(err error) string {
	switch {
	case errors.Is(err, room.ErrTargetUnknown):
		return "target_unknown"
	case errors.Is(err, room.ErrCrossUserForbidden):
		return "cross_user_forbidden"
	case errors.Is(err, room.ErrSenderEqualsTarget):
		return "self_target"
	default:
		return "forward_error"
	}
}

func (c *Connection) sendServerError(ctx context.Context, code, message string) error {
	return c.Send(ctx, &signalv1.Frame{Body: &signalv1.Frame_ServerError{
		ServerError: &signalv1.ServerError{Code: code, Message: message},
	}})
}

func (c *Connection) readFrame(ctx context.Context) (*signalv1.Frame, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, fmt.Errorf("expected binary frame, got %v", typ)
	}
	f := &signalv1.Frame{}
	if err := proto.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return f, nil
}

func deviceKindFromProto(k signalv1.DeviceKind) store.DeviceKind {
	switch k {
	case signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST:
		return store.DeviceKindWindowsHost
	case signalv1.DeviceKind_DEVICE_KIND_MOBILE_CLIENT:
		return store.DeviceKindMobileClient
	case signalv1.DeviceKind_DEVICE_KIND_CLI:
		return store.DeviceKindCLI
	default:
		return store.DeviceKindUnknown
	}
}

func isCloseErr(err error) bool {
	return errors.Is(err, context.Canceled) ||
		websocket.CloseStatus(err) != -1
}
