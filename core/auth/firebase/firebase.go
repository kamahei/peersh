package firebase

import (
	"context"
	"errors"
	"fmt"

	firebase "firebase.google.com/go/v4"
	fbauth "firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"

	"github.com/peersh/peersh/core/auth"
)

// Kind is the provider tag for Firebase Auth.
const Kind = "firebase"

// Errors surfaced by the provider.
var (
	ErrTokenInvalid = errors.New("firebase: id token invalid")
	ErrTokenExpired = errors.New("firebase: id token expired")
)

// Credentials is what the client sends. The IDToken is a Firebase Auth
// ID token (JWT signed by Google with the project's private signing key).
type Credentials struct {
	IDToken string
}

// Kind reports the provider tag.
func (Credentials) Kind() string { return Kind }

// TokenVerifier is the small subset of *fbauth.Client this package needs.
// Tests substitute a fake.
type TokenVerifier interface {
	VerifyIDToken(ctx context.Context, idToken string) (*fbauth.Token, error)
}

// Provider implements auth.Provider for Firebase ID tokens.
type Provider struct {
	Verifier TokenVerifier
}

// NewFromApp constructs a Provider from a *firebase.App, deriving the
// auth client from it.
func NewFromApp(ctx context.Context, app *firebase.App) (*Provider, error) {
	c, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase: auth client: %w", err)
	}
	return &Provider{Verifier: c}, nil
}

// NewFromServiceAccount constructs a *firebase.App and Provider from a
// service-account JSON file path. Pass projectID empty to let Firebase
// infer it from the service-account credentials.
func NewFromServiceAccount(ctx context.Context, projectID, credentialsPath string) (*Provider, error) {
	cfg := &firebase.Config{}
	if projectID != "" {
		cfg.ProjectID = projectID
	}
	opts := []option.ClientOption{}
	if credentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsPath))
	}
	app, err := firebase.NewApp(ctx, cfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("firebase: NewApp: %w", err)
	}
	return NewFromApp(ctx, app)
}

// Authenticate verifies creds.IDToken via the configured TokenVerifier
// and returns auth.Identity{UserID: uid}.
func (p *Provider) Authenticate(ctx context.Context, creds auth.Credentials) (auth.Identity, error) {
	if creds == nil {
		return auth.Identity{}, &auth.WrongKindError{Got: "<nil>", Want: Kind}
	}
	pc, ok := creds.(Credentials)
	if !ok {
		return auth.Identity{}, &auth.WrongKindError{Got: creds.Kind(), Want: Kind}
	}
	if pc.IDToken == "" {
		return auth.Identity{}, ErrTokenInvalid
	}
	tok, err := p.Verifier.VerifyIDToken(ctx, pc.IDToken)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("%w: %s", ErrTokenInvalid, err.Error())
	}
	if tok == nil || tok.UID == "" {
		return auth.Identity{}, ErrTokenInvalid
	}
	return auth.Identity{UserID: tok.UID}, nil
}
