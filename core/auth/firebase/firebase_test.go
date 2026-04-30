package firebase_test

import (
	"context"
	"errors"
	"testing"

	fbauth "firebase.google.com/go/v4/auth"

	"github.com/peersh/peersh/core/auth"
	fbprovider "github.com/peersh/peersh/core/auth/firebase"
)

type fakeVerifier struct {
	uid string
	err error
}

func (f fakeVerifier) VerifyIDToken(_ context.Context, _ string) (*fbauth.Token, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &fbauth.Token{UID: f.uid}, nil
}

func TestAuthenticateHappyPath(t *testing.T) {
	p := &fbprovider.Provider{Verifier: fakeVerifier{uid: "alice"}}
	id, err := p.Authenticate(context.Background(), fbprovider.Credentials{IDToken: "token"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.UserID != "alice" {
		t.Fatalf("expected alice, got %q", id.UserID)
	}
}

func TestAuthenticateRejectsEmptyToken(t *testing.T) {
	p := &fbprovider.Provider{Verifier: fakeVerifier{uid: "alice"}}
	_, err := p.Authenticate(context.Background(), fbprovider.Credentials{})
	if !errors.Is(err, fbprovider.ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestAuthenticateWrongKindRejected(t *testing.T) {
	p := &fbprovider.Provider{Verifier: fakeVerifier{uid: "alice"}}
	type fake struct{}
	// We can't construct fakeCreds inline easily; reuse a small shim.
	_, err := p.Authenticate(context.Background(), wrongKindCreds{})
	if err == nil {
		t.Fatal("expected wrong-kind error")
	}
	var wke *auth.WrongKindError
	if !errors.As(err, &wke) {
		t.Fatalf("expected *auth.WrongKindError, got %T: %v", err, err)
	}
	_ = fake{}
}

type wrongKindCreds struct{}

func (wrongKindCreds) Kind() string { return "psk" }

func TestAuthenticateBubblesVerifierError(t *testing.T) {
	p := &fbprovider.Provider{Verifier: fakeVerifier{err: errors.New("boom")}}
	_, err := p.Authenticate(context.Background(), fbprovider.Credentials{IDToken: "x"})
	if !errors.Is(err, fbprovider.ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid wrapped, got %v", err)
	}
}
