package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get* / Delete* when the requested record does not
// exist. Callers should compare with errors.Is.
var ErrNotFound = errors.New("store: not found")

// DeviceKind distinguishes Windows hosts from mobile clients.
type DeviceKind int

const (
	DeviceKindUnknown DeviceKind = iota
	DeviceKindWindowsHost
	DeviceKindMobileClient
)

// String makes DeviceKind printable in logs.
func (k DeviceKind) String() string {
	switch k {
	case DeviceKindWindowsHost:
		return "windows_host"
	case DeviceKindMobileClient:
		return "mobile_client"
	default:
		return "unknown"
	}
}

// Device is a registered participant in peersh: a Windows host or a mobile
// client. The ID is derived from the public key; see core/devid.
type Device struct {
	ID          string
	PublicKey   []byte
	OwnerUserID string
	Kind        DeviceKind
	DisplayName string
	CreatedAt   time.Time
	LastSeenAt  time.Time
}

// SessionState tracks where in its lifecycle a Session currently sits.
type SessionState int

const (
	SessionStateUnknown SessionState = iota
	SessionStateSettingUp
	SessionStateConnected
	SessionStateDisconnected
	SessionStateExpired
	SessionStateClosed
)

func (s SessionState) String() string {
	switch s {
	case SessionStateSettingUp:
		return "setting_up"
	case SessionStateConnected:
		return "connected"
	case SessionStateDisconnected:
		return "disconnected"
	case SessionStateExpired:
		return "expired"
	case SessionStateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// Session is an active or recently-active connection between a paired mobile
// device and a Windows host.
type Session struct {
	ID             string
	UserID         string
	MobileDeviceID string
	HostDeviceID   string
	State          SessionState
	CreatedAt      time.Time
	LastActiveAt   time.Time
	IdleDeadlineAt time.Time
}

// Store is the pluggable persistence interface backing the signaling server
// and peershd. Phase 1 ships only the in-memory implementation; Phase 2 adds
// SQLite and Phase 5 adds Firestore. Adding new entities (PSKRecord,
// Pairing) in later phases is an additive interface extension and is not in
// scope here.
type Store interface {
	PutDevice(ctx context.Context, d Device) error
	GetDevice(ctx context.Context, id string) (Device, error)
	ListDevicesByOwner(ctx context.Context, userID string) ([]Device, error)
	DeleteDevice(ctx context.Context, id string) error

	PutSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	DeleteSession(ctx context.Context, id string) error

	Close() error
}
