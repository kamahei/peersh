// Package signaling is the peersh signaling-channel client library used by
// peershd and peersh-cli (and, in Phase 4, the mobile app).
//
// Wire format: WebSocket binary frames carrying one peersh.signal.v1.Frame
// per message. The client handles Hello → Register at Dial time, then
// surfaces Connect messages as inbound traffic and lets callers send Connect
// messages outbound.
package signaling

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/peersh/peersh/core/auth/psk"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"google.golang.org/protobuf/proto"
	"nhooyr.io/websocket"
)

// ProtocolVersion is the signaling-channel wire version. Locked at 1.
const ProtocolVersion = 1

// DialOptions describes how to connect and what identity to register.
//
// Two auth modes are supported:
//   - PSK: populate UserID + Secret. The client signs the Register frame
//     with HMAC-SHA256 over the secret.
//   - Firebase: populate FirebaseIDToken. The server's firebase auth
//     provider verifies the token and uses its `uid` as the user_id;
//     UserID + Secret may be left empty.
type DialOptions struct {
	URL         string // ws://host/ws or wss://host/ws
	UserID      string
	Secret      []byte // PSK
	DeviceID    string
	PublicKey   []byte
	Kind        signalv1.DeviceKind
	DisplayName string
	ClientID    string // free-form identifier, e.g. "peersh-cli/0.1"

	// FirebaseIDToken, when non-empty, switches the client into Firebase
	// mode: the Register frame carries this token verbatim and the
	// server's firebase auth provider validates it. PSK fields are
	// ignored in this mode.
	FirebaseIDToken string

	// FirebaseAppCheckToken is forwarded as-is on the Register frame.
	// Optional even in Firebase mode: enforcement is decided by the
	// server's `app_check_required` config.
	FirebaseAppCheckToken string

	// Optional. Defaults to http.DefaultClient.
	HTTPClient *http.Client

	// HandshakeTimeout caps how long Hello / Register may take. Default 10s.
	HandshakeTimeout time.Duration
}

// Client is a connected, registered signaling session. Methods are safe for
// concurrent use; Recv blocks until a message arrives or the connection
// closes.
type Client struct {
	conn       *websocket.Conn
	serverID   string
	deviceID   string
	userID     string
	closeOnce  sync.Once
	closeErr   error
	cancelPump context.CancelFunc

	mu       sync.Mutex
	incoming chan *signalv1.Connect
	closed   chan struct{}
}

// Errors surfaced by the client.
var (
	ErrRegisterRejected = errors.New("signaling: register rejected by server")
	ErrClosed           = errors.New("signaling: client closed")
)

// Dial opens a WebSocket, completes Hello + Register, and starts the read
// pump. The returned Client is ready for Send / Recv.
func Dial(ctx context.Context, opts DialOptions) (*Client, error) {
	if opts.HandshakeTimeout == 0 {
		opts.HandshakeTimeout = 10 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, opts.HandshakeTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(dialCtx, opts.URL, &websocket.DialOptions{
		HTTPClient: opts.HTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("signaling: dial %q: %w", opts.URL, err)
	}
	conn.SetReadLimit(1 << 20) // 1 MiB; signaling frames are tiny.

	c := &Client{
		conn:     conn,
		userID:   opts.UserID,
		deviceID: opts.DeviceID,
		incoming: make(chan *signalv1.Connect, 8),
		closed:   make(chan struct{}),
	}

	if err := c.handshake(dialCtx, opts); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "handshake failed")
		return nil, err
	}

	pumpCtx, cancelPump := context.WithCancel(context.Background())
	c.cancelPump = cancelPump
	go c.readPump(pumpCtx)
	return c, nil
}

// handshake performs Hello → Register on the established WS connection.
func (c *Client) handshake(ctx context.Context, opts DialOptions) error {
	if err := c.writeFrame(ctx, &signalv1.Frame{
		Body: &signalv1.Frame_ClientHello{ClientHello: &signalv1.ClientHello{
			ProtocolVersion: ProtocolVersion,
			ClientId:        opts.ClientID,
		}},
	}); err != nil {
		return fmt.Errorf("send ClientHello: %w", err)
	}
	srvHello, err := c.readFrame(ctx)
	if err != nil {
		return fmt.Errorf("read ServerHello: %w", err)
	}
	hello := srvHello.GetServerHello()
	if hello == nil {
		return fmt.Errorf("expected ServerHello, got %T", srvHello.Body)
	}
	if hello.GetProtocolVersion() != ProtocolVersion {
		return fmt.Errorf("server protocol_version=%d, client=%d", hello.GetProtocolVersion(), ProtocolVersion)
	}
	c.serverID = hello.GetServerId()

	reg := &signalv1.Register{
		UserId:      opts.UserID,
		DeviceId:    opts.DeviceID,
		PublicKey:   opts.PublicKey,
		Kind:        opts.Kind,
		DisplayName: opts.DisplayName,
	}
	if opts.FirebaseIDToken != "" {
		// Firebase mode: the server resolves user_id from the token.
		reg.FirebaseIdToken = opts.FirebaseIDToken
		reg.FirebaseAppCheckToken = opts.FirebaseAppCheckToken
	} else {
		// PSK mode: HMAC-sign the Register frame.
		if err := psk.SignRegister(opts.Secret, reg); err != nil {
			return fmt.Errorf("sign Register: %w", err)
		}
	}
	if err := c.writeFrame(ctx, &signalv1.Frame{
		Body: &signalv1.Frame_Register{Register: reg},
	}); err != nil {
		return fmt.Errorf("send Register: %w", err)
	}
	ackFrame, err := c.readFrame(ctx)
	if err != nil {
		return fmt.Errorf("read RegisterAck: %w", err)
	}
	ack := ackFrame.GetRegisterAck()
	if ack == nil {
		return fmt.Errorf("expected RegisterAck, got %T", ackFrame.Body)
	}
	if !ack.GetAccepted() {
		return fmt.Errorf("%w: %s", ErrRegisterRejected, ack.GetReason())
	}
	return nil
}

// readPump reads frames from the connection and dispatches them. Connect
// messages flow into the incoming channel; ServerError logs and continues.
func (c *Client) readPump(ctx context.Context) {
	defer close(c.closed)
	for {
		f, err := c.readFrame(ctx)
		if err != nil {
			c.recordCloseErr(err)
			return
		}
		switch body := f.GetBody().(type) {
		case *signalv1.Frame_Connect:
			select {
			case c.incoming <- body.Connect:
			case <-ctx.Done():
				return
			}
		case *signalv1.Frame_ServerError:
			// Log via stderr for now; surfacing to caller is future work.
			c.recordCloseErr(fmt.Errorf("server error: %s: %s",
				body.ServerError.GetCode(), body.ServerError.GetMessage()))
			return
		default:
			// Ignore unexpected frames after registration; logging deferred.
		}
	}
}

func (c *Client) recordCloseErr(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
	})
}

// SendConnect sends a Connect frame addressed to targetDeviceID with the
// supplied local candidates. The server fills from_device_id when forwarding.
func (c *Client) SendConnect(ctx context.Context, targetDeviceID string, candidates []*signalv1.EndpointCandidate) error {
	return c.writeFrame(ctx, &signalv1.Frame{
		Body: &signalv1.Frame_Connect{Connect: &signalv1.Connect{
			TargetDeviceId: targetDeviceID,
			Candidates:     candidates,
		}},
	})
}

// Recv blocks until a Connect frame arrives or the client closes.
func (c *Client) Recv(ctx context.Context) (*signalv1.Connect, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		if c.closeErr != nil {
			return nil, c.closeErr
		}
		return nil, ErrClosed
	case msg := <-c.incoming:
		return msg, nil
	}
}

// Close shuts down the WebSocket and read pump. Idempotent.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.cancelPump != nil {
			c.cancelPump()
		}
		c.closeErr = c.conn.Close(websocket.StatusNormalClosure, "client done")
	})
	return c.closeErr
}

// ServerID is the server identifier reported in ServerHello (empty before
// handshake completes).
func (c *Client) ServerID() string { return c.serverID }

// DeviceID is the device id this client registered with.
func (c *Client) DeviceID() string { return c.deviceID }

// UserID is the user this client registered as.
func (c *Client) UserID() string { return c.userID }

// writeFrame marshals f and writes it as one binary WebSocket message.
func (c *Client) writeFrame(ctx context.Context, f *signalv1.Frame) error {
	data, err := proto.Marshal(f)
	if err != nil {
		return fmt.Errorf("signaling: marshal frame: %w", err)
	}
	return c.conn.Write(ctx, websocket.MessageBinary, data)
}

// readFrame reads one binary WebSocket message and unmarshals it as a Frame.
func (c *Client) readFrame(ctx context.Context) (*signalv1.Frame, error) {
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		return nil, fmt.Errorf("signaling: expected binary frame, got %v", typ)
	}
	f := &signalv1.Frame{}
	if err := proto.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("signaling: unmarshal frame: %w", err)
	}
	return f, nil
}
