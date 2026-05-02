// Package firebase wires peershd into a Firebase Auth project so the
// host can register against a peersh-signaling server running in
// `auth_provider = "firebase"` mode.
//
// Strategy: the operator hands peershd a service-account JSON (the same
// one peersh-signaling itself uses, OR a sibling SA in the same Firebase
// project that has the `Firebase Authentication Admin` role). peershd:
//
//   1. Mints a custom token for the user's chosen Firebase uid.
//   2. Exchanges that custom token for an ID token via the Identity
//      Toolkit REST endpoint (signInWithCustomToken).
//   3. Refreshes the ID token before the 1-hour expiry.
//
// The mobile app (Phase 5b) signs in with Google sign-in and gets an ID
// token whose uid is the same Google account. peershd uses the same uid
// (configured via -firebase-uid) so both sides land in the same
// `users/{uid}/devices/...` namespace in Firestore.
package firebase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"google.golang.org/api/option"
)

// AuthSource produces fresh Firebase ID tokens for the configured uid.
// Safe for concurrent use; the underlying refresh runs at most once per
// 50 minutes.
type AuthSource struct {
	app    *firebase.App
	auth   *auth.Client
	uid    string
	apiKey string

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New returns an AuthSource bound to the supplied service-account JSON
// and target Firebase identity. apiKey is the Firebase Web API key
// (find it under the Firebase console → Project settings → General →
// Your apps → Web app → "apiKey"). It's required because the
// signInWithCustomToken REST endpoint authenticates the request via
// ?key=API_KEY.
//
// The identity is one of `email` or `uid`; supply email when possible
// (more readable in operator-side configuration). New() resolves
// email → uid via the Firebase Admin SDK at construction time.
func New(ctx context.Context, projectID, credentialsJSONPath, email, uid, apiKey string) (*AuthSource, error) {
	if email == "" && uid == "" {
		return nil, errors.New("firebase: supply -firebase-email or -firebase-uid")
	}
	if apiKey == "" {
		return nil, errors.New("firebase: empty apiKey")
	}
	cfg := &firebase.Config{ProjectID: projectID}
	app, err := firebase.NewApp(ctx, cfg, option.WithCredentialsFile(credentialsJSONPath))
	if err != nil {
		return nil, fmt.Errorf("firebase: NewApp: %w", err)
	}
	authClient, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase: Auth client: %w", err)
	}
	resolvedUID := uid
	if resolvedUID == "" {
		// Resolve email -> uid via Admin SDK. Cheaper than the user
		// guessing their Firebase uid from the console.
		rec, err := authClient.GetUserByEmail(ctx, email)
		if err != nil {
			return nil, fmt.Errorf("firebase: lookup uid for %q: %w", email, err)
		}
		resolvedUID = rec.UID
	}
	return &AuthSource{
		app:    app,
		auth:   authClient,
		uid:    resolvedUID,
		apiKey: apiKey,
	}, nil
}

// Token returns a non-expired Firebase ID token for the configured uid.
// The first call mints a fresh one; subsequent calls reuse the cached
// token until its expiry approaches.
func (s *AuthSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.expires.Add(-2*time.Minute)) {
		return s.token, nil
	}

	custom, err := s.auth.CustomToken(ctx, s.uid)
	if err != nil {
		return "", fmt.Errorf("firebase: CustomToken: %w", err)
	}
	idToken, err := exchangeCustomTokenForIDToken(ctx, s.apiKey, custom)
	if err != nil {
		return "", err
	}
	s.token = idToken
	// signInWithCustomToken returns a 1-hour-validity ID token.
	s.expires = time.Now().Add(58 * time.Minute)
	return idToken, nil
}

// UID returns the uid this source mints tokens for.
func (s *AuthSource) UID() string { return s.uid }

// exchangeCustomTokenForIDToken posts a custom token to the
// Identity Toolkit signInWithCustomToken endpoint and returns the
// resulting ID token. Documented at
// https://firebase.google.com/docs/reference/rest/auth#section-verify-custom-token
func exchangeCustomTokenForIDToken(ctx context.Context, apiKey, customToken string) (string, error) {
	url := "https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key=" + apiKey
	body := map[string]any{"token": customToken, "returnSecureToken": true}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(payload)))
	if err != nil {
		return "", fmt.Errorf("firebase: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("firebase: identity toolkit POST: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("firebase: identity toolkit %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		IDToken string `json:"idToken"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("firebase: parse identity toolkit response: %w", err)
	}
	if out.IDToken == "" {
		return "", errors.New("firebase: identity toolkit returned empty idToken")
	}
	return out.IDToken, nil
}
