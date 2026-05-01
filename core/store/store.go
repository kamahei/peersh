package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get* / Delete* when the requested record does not
// exist. Callers should compare with errors.Is.
var ErrNotFound = errors.New("store: not found")

// DeviceKind distinguishes Windows hosts, mobile clients, and CLI clients.
type DeviceKind int

const (
	DeviceKindUnknown DeviceKind = iota
	DeviceKindWindowsHost
	DeviceKindMobileClient
	DeviceKindCLI
)

// String makes DeviceKind printable in logs.
func (k DeviceKind) String() string {
	switch k {
	case DeviceKindWindowsHost:
		return "windows_host"
	case DeviceKindMobileClient:
		return "mobile_client"
	case DeviceKindCLI:
		return "cli"
	default:
		return "unknown"
	}
}

// AuthProvider identifies which auth.Provider a User authenticated under.
// A user belongs to exactly one provider for life — switching providers
// means a different user.
type AuthProvider int

const (
	AuthProviderUnknown AuthProvider = iota
	AuthProviderNone
	AuthProviderPSK
	AuthProviderFirebase
)

func (p AuthProvider) String() string {
	switch p {
	case AuthProviderNone:
		return "none"
	case AuthProviderPSK:
		return "psk"
	case AuthProviderFirebase:
		return "firebase"
	default:
		return "unknown"
	}
}

// User is an account that owns devices.
//
// In PSK mode the operator chooses the user_id at PSK creation time. In
// Firebase mode it's the Firebase UID. In none mode a fixed sentinel is used.
type User struct {
	ID           string
	AuthProvider AuthProvider
	CreatedAt    time.Time
}

// Device is a registered participant in peersh: a Windows host, a mobile
// client, or a CLI. The ID is derived from the public key; see core/devid.
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

// PSKRecord is a (user_id, secret) pair for the psk auth.Provider.
//
// The secret is stored raw because HMAC-SHA256 verification needs it
// server-side. Operators are advised to host the SQLite DB on a
// disk-encrypted volume (see docs/deploy/self-hosting.md).
type PSKRecord struct {
	UserID       string
	Secret       []byte // high-entropy, ≥ 32 bytes recommended
	DisplayLabel string
	CreatedAt    time.Time
	RevokedAt    time.Time // zero when not revoked
}

// IsRevoked reports whether this PSK has been revoked.
func (r PSKRecord) IsRevoked() bool { return !r.RevokedAt.IsZero() }

// Pairing is the implicit-or-explicit association of a mobile/CLI device
// with a Windows host under the same user. In Phase 2 pairings are derived
// from shared user_id and persisted on first use to track LastUsedAt.
type Pairing struct {
	UserID         string
	MobileDeviceID string
	HostDeviceID   string
	CreatedAt      time.Time
	LastUsedAt     time.Time
}

// Store is the pluggable persistence interface backing the signaling server
// and peershd. Phase 1 ships in-memory only; Phase 2 adds SQLite plus the
// User / PSKRecord / Pairing methods. Phase 5 adds Firestore.
//
// Implementations must be safe for concurrent use.
type Store interface {
	PutDevice(ctx context.Context, d Device) error
	GetDevice(ctx context.Context, id string) (Device, error)
	ListDevicesByOwner(ctx context.Context, userID string) ([]Device, error)
	DeleteDevice(ctx context.Context, id string) error

	PutSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	DeleteSession(ctx context.Context, id string) error

	PutUser(ctx context.Context, u User) error
	GetUser(ctx context.Context, id string) (User, error)

	PutPSKRecord(ctx context.Context, r PSKRecord) error
	GetPSKRecord(ctx context.Context, userID string) (PSKRecord, error)
	ListPSKRecords(ctx context.Context) ([]PSKRecord, error)
	DeletePSKRecord(ctx context.Context, userID string) error

	PutPairing(ctx context.Context, p Pairing) error
	GetPairing(ctx context.Context, userID, mobileDeviceID, hostDeviceID string) (Pairing, error)
	ListPairingsByUser(ctx context.Context, userID string) ([]Pairing, error)
	DeletePairing(ctx context.Context, userID, mobileDeviceID, hostDeviceID string) error

	Close() error
}
