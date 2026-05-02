package firebase

import (
	"context"
	"errors"
	"fmt"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/appcheck"
)

// ErrAppCheckInvalid is surfaced when an App Check token fails verification
// or is missing in a context that requires one.
var ErrAppCheckInvalid = errors.New("firebase: app check token invalid")

// AppCheckVerifier is the small subset of *appcheck.Client this package
// needs. Tests substitute a fake.
type AppCheckVerifier interface {
	VerifyToken(token string) (*appcheck.DecodedAppCheckToken, error)
}

// AppCheckFromApp constructs an AppCheckVerifier from a *firebase.App.
// Returns an error if the underlying SDK can't initialize the App Check
// client (project id misconfigured / no credentials).
func AppCheckFromApp(ctx context.Context, app *firebase.App) (AppCheckVerifier, error) {
	c, err := app.AppCheck(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase: app check client: %w", err)
	}
	return c, nil
}

// VerifyAppCheck validates token via the supplied verifier. Returns nil
// on success; ErrAppCheckInvalid (wrapped) on failure. Empty token is
// always treated as invalid — callers decide whether to enforce.
func VerifyAppCheck(verifier AppCheckVerifier, token string) error {
	if verifier == nil {
		return fmt.Errorf("%w: no verifier configured", ErrAppCheckInvalid)
	}
	if token == "" {
		return fmt.Errorf("%w: empty token", ErrAppCheckInvalid)
	}
	if _, err := verifier.VerifyToken(token); err != nil {
		return fmt.Errorf("%w: %s", ErrAppCheckInvalid, err.Error())
	}
	return nil
}
