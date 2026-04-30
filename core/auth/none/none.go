package none

import (
	"context"

	"github.com/peersh/peersh/core/auth"
)

// Kind is the credential / provider tag for the no-op auth provider.
const Kind = "none"

// Credentials is the credential value carried by clients of the "none"
// provider. It is empty by design — the provider accepts any caller.
type Credentials struct{}

// Kind reports the provider tag.
func (Credentials) Kind() string { return Kind }

// Provider implements auth.Provider as a permissive no-op. Use only for
// development, LAN-only deployments, and Phase 1.
type Provider struct{}

// New returns a ready-to-use no-op provider.
func New() *Provider { return &Provider{} }

// Authenticate accepts any credentials and returns an anonymous identity.
// It rejects credentials whose Kind() is not "none" so that calling code that
// passes the wrong credentials shape gets a clear signal rather than silent
// success.
func (Provider) Authenticate(_ context.Context, creds auth.Credentials) (auth.Identity, error) {
	if creds != nil && creds.Kind() != Kind {
		return auth.Identity{}, &auth.WrongKindError{Got: creds.Kind(), Want: Kind}
	}
	return auth.Anonymous(), nil
}
