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
	"github.com/peersh/peersh/core/auth/psk"
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

	Registry    *room.Registry
	IPLimit     *ratelimit.Bucket // upgrade-time per-IP
	UserLimit   *ratelimit.Bucket // per user_id at Register
	DeviceLimit *ratelimit.Bucket // per device_id at Connect
	Logger      *slog.Logger
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

	registered bool
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
		}
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	}()

	if err := c.handshake(ctx); err != nil {
		c.log.Info("ws: handshake failed", "err", err)
		_ = c.sendServerError(ctx, "handshake_failed", err.Error())
		return
	}
	c.registered = true
	if prev := c.server.Registry.Register(c); prev != nil {
		c.log.Info("ws: replaced previous registration", "user", c.userID, "device", c.deviceID)
		_ = prev.Send(ctx, &signalv1.Frame{Body: &signalv1.Frame_ServerError{
			ServerError: &signalv1.ServerError{Code: "replaced", Message: "another device with this id reconnected"},
		}})
	}
	c.log.Info("ws: registered", "user", c.userID, "device", c.deviceID)

	for {
		f, err := c.readFrame(ctx)
		if err != nil {
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
	if !c.server.UserLimit.Allow(reg.GetUserId()) {
		return fmt.Errorf("user rate limit")
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
		if !c.server.DeviceLimit.Allow(c.deviceID) {
			return fmt.Errorf("device rate limit")
		}
		if err := c.server.Registry.Forward(ctx, c, body.Connect); err != nil {
			_ = c.sendServerError(ctx, classifyForwardErr(err), err.Error())
			return nil // don't tear down on a forwarding miss
		}
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
