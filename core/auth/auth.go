package auth

import "context"

// Identity is the result of a successful authentication. UserID is empty for
// providers that don't bind to a user account (e.g. "none"). DeviceID is empty
// when the credential is not yet associated with a registered device.
type Identity struct {
	UserID   string
	DeviceID string
}

// Anonymous is the identity returned by providers that don't authenticate.
func Anonymous() Identity { return Identity{UserID: "anonymous"} }

// Credentials carry the per-provider auth material a client presents at
// signaling-server registration. Implementations live alongside their
// matching Provider implementation (e.g. psk.Credentials, firebase.Credentials).
type Credentials interface {
	// Kind returns a stable provider tag used by Provider implementations to
	// reject credentials of the wrong kind early.
	Kind() string
}

// Provider verifies presented Credentials and returns the resulting Identity.
//
// The interface is shaped so that PSK (Phase 2) and Firebase (Phase 5) can
// implement it without changes here. Adding new providers means adding a new
// subpackage; it must not require editing this package.
type Provider interface {
	// Authenticate verifies creds and returns the bound Identity, or an error
	// if verification fails. A nil error with a zero Identity is not allowed
	// — providers that don't bind to a user account must return Anonymous().
	Authenticate(ctx context.Context, creds Credentials) (Identity, error)
}
