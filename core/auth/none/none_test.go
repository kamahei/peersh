package none_test

import (
	"context"
	"errors"
	"testing"

	"github.com/peersh/peersh/core/auth"
	"github.com/peersh/peersh/core/auth/none"
)

func TestAuthenticateAcceptsNilCredentials(t *testing.T) {
	id, err := none.New().Authenticate(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.UserID != "anonymous" {
		t.Fatalf("expected anonymous user, got %q", id.UserID)
	}
}

func TestAuthenticateAcceptsNoneCredentials(t *testing.T) {
	id, err := none.New().Authenticate(context.Background(), none.Credentials{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id.UserID != "anonymous" {
		t.Fatalf("expected anonymous user, got %q", id.UserID)
	}
}

type fakeCreds struct{}

func (fakeCreds) Kind() string { return "psk" }

func TestAuthenticateRejectsWrongKind(t *testing.T) {
	_, err := none.New().Authenticate(context.Background(), fakeCreds{})
	if err == nil {
		t.Fatal("expected WrongKindError, got nil")
	}
	var wke *auth.WrongKindError
	if !errors.As(err, &wke) {
		t.Fatalf("expected *auth.WrongKindError, got %T: %v", err, err)
	}
	if wke.Got != "psk" || wke.Want != "none" {
		t.Fatalf("unexpected fields: got=%q want=%q", wke.Got, wke.Want)
	}
}

func TestProviderImplementsInterface(t *testing.T) {
	var _ auth.Provider = none.New()
	var _ auth.Credentials = none.Credentials{}
}
