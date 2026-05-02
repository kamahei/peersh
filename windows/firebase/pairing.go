// Pairing flow — replaces the service-account JSON model for personal
// deployments.
//
// One-shot bootstrap (`-pair-code 123456`):
//
//   1. POST https://<region>-<project>.cloudfunctions.net/claimPairingCode
//      {code: "123456"} -> {uid, custom_token}
//   2. POST identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken
//      ?key=<api_key> {token: custom_token, returnSecureToken: true}
//      -> {idToken, refreshToken, expiresIn}
//   3. Persist refreshToken to disk (default
//      %LOCALAPPDATA%\peersh\firebase-refresh-token.txt).
//
// Subsequent runs use only the refresh token + Web API key:
//
//   POST https://securetoken.googleapis.com/v1/token?key=<api_key>
//   grant_type=refresh_token&refresh_token=<token>
//   -> {id_token, refresh_token, expires_in, user_id}
//
// The persisted refresh token is scoped to a single Firebase uid — far
// narrower than the service-account JSON it replaces (which can mint
// tokens for every uid in the project).

package firebase

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RefreshAuthSource produces fresh Firebase ID tokens by exchanging a
// long-lived refresh token at the Google securetoken endpoint. It does
// NOT require a service-account JSON.
type RefreshAuthSource struct {
	apiKey       string
	tokenPath    string

	mu           sync.Mutex
	refreshToken string
	uid          string
	idToken      string
	expires      time.Time
}

// NewRefreshSource loads a refresh token from tokenPath and returns an
// AuthSource that uses it to mint fresh ID tokens. The first call to
// Token performs the exchange so the constructor stays cheap.
func NewRefreshSource(tokenPath, apiKey string) (*RefreshAuthSource, error) {
	if tokenPath == "" {
		return nil, errors.New("firebase: empty token path")
	}
	if apiKey == "" {
		return nil, errors.New("firebase: empty apiKey")
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("firebase: read refresh token: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return nil, fmt.Errorf("firebase: refresh token file %q is empty", tokenPath)
	}
	return &RefreshAuthSource{
		apiKey:       apiKey,
		tokenPath:    tokenPath,
		refreshToken: tok,
	}, nil
}

// Token returns a non-expired Firebase ID token. The first call mints
// one via the refresh token; subsequent calls reuse the cached value
// until expiry approaches.
func (s *RefreshAuthSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idToken != "" && time.Now().Before(s.expires.Add(-2*time.Minute)) {
		return s.idToken, nil
	}
	out, err := exchangeRefreshTokenForIDToken(ctx, s.apiKey, s.refreshToken)
	if err != nil {
		return "", err
	}
	s.idToken = out.IDToken
	s.uid = out.UserID
	s.expires = time.Now().Add(time.Duration(out.ExpiresInSeconds) * time.Second)
	if out.RefreshToken != "" && out.RefreshToken != s.refreshToken {
		s.refreshToken = out.RefreshToken
		if err := writeTokenFile(s.tokenPath, out.RefreshToken); err != nil {
			return "", fmt.Errorf("firebase: persist rotated refresh token: %w", err)
		}
	}
	return s.idToken, nil
}

// UID returns the uid this source mints tokens for. Empty until the
// first Token call (the uid is reported by the securetoken endpoint).
func (s *RefreshAuthSource) UID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uid != "" {
		return s.uid
	}
	if u, ok := uidFromJWT(s.idToken); ok {
		return u
	}
	return ""
}

// Pair claims a one-time pairing code, exchanges the resulting Custom
// Token for an ID + Refresh Token pair, and writes the refresh token to
// tokenPath. Returns a ready-to-use RefreshAuthSource.
//
// projectID is the Firebase project id (used to build the Cloud Function
// URL). region is e.g. "asia-northeast1".
func Pair(
	ctx context.Context,
	projectID, region, apiKey, code, tokenPath string,
) (*RefreshAuthSource, error) {
	if projectID == "" {
		return nil, errors.New("firebase: pair: empty project id")
	}
	if region == "" {
		return nil, errors.New("firebase: pair: empty region")
	}
	if apiKey == "" {
		return nil, errors.New("firebase: pair: empty apiKey")
	}
	if !looksLikeSixDigits(code) {
		return nil, errors.New("firebase: pair: code must be 6 digits")
	}
	if tokenPath == "" {
		return nil, errors.New("firebase: pair: empty token path")
	}

	claim, err := claimPairingCode(ctx, projectID, region, code)
	if err != nil {
		return nil, err
	}
	tokens, err := signInWithCustomToken(ctx, apiKey, claim.CustomToken)
	if err != nil {
		return nil, err
	}
	if tokens.RefreshToken == "" {
		return nil, errors.New("firebase: pair: identity toolkit returned empty refresh token")
	}
	if err := writeTokenFile(tokenPath, tokens.RefreshToken); err != nil {
		return nil, err
	}
	return &RefreshAuthSource{
		apiKey:       apiKey,
		tokenPath:    tokenPath,
		refreshToken: tokens.RefreshToken,
		uid:          claim.UID,
		idToken:      tokens.IDToken,
		expires:      time.Now().Add(time.Duration(tokens.ExpiresInSeconds) * time.Second),
	}, nil
}

// DefaultRefreshTokenPath returns the conventional location for the
// persisted refresh token under the user's local-app data directory.
func DefaultRefreshTokenPath() string {
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		return filepath.Join(base, "peersh", "firebase-refresh-token.txt")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "peersh", "firebase-refresh-token.txt")
	}
	return filepath.Join(".", "peersh-firebase-refresh-token.txt")
}

// --- internals -------------------------------------------------------

type claimResponse struct {
	UID         string `json:"uid"`
	CustomToken string `json:"custom_token"`
}

func claimPairingCode(ctx context.Context, projectID, region, code string) (*claimResponse, error) {
	endpoint := fmt.Sprintf(
		"https://%s-%s.cloudfunctions.net/claimPairingCode",
		region, projectID,
	)
	body, _ := json.Marshal(map[string]string{"code": code})
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("firebase: build claim request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firebase: claim POST: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("firebase: claim %d: %s", resp.StatusCode, string(raw))
	}
	out := &claimResponse{}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("firebase: parse claim response: %w", err)
	}
	if out.CustomToken == "" {
		return nil, errors.New("firebase: claim returned empty custom_token")
	}
	return out, nil
}

type idTokenResponse struct {
	IDToken          string
	RefreshToken     string
	UserID           string
	ExpiresInSeconds int
}

func signInWithCustomToken(ctx context.Context, apiKey, customToken string) (*idTokenResponse, error) {
	endpoint := "https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key=" + apiKey
	body, _ := json.Marshal(map[string]any{
		"token":             customToken,
		"returnSecureToken": true,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("firebase: build signin request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firebase: signInWithCustomToken POST: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("firebase: signInWithCustomToken %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		IDToken      string `json:"idToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    string `json:"expiresIn"`
		LocalID      string `json:"localId"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("firebase: parse signInWithCustomToken response: %w", err)
	}
	exp := 3600
	if parsed.ExpiresIn != "" {
		_, _ = fmt.Sscanf(parsed.ExpiresIn, "%d", &exp)
	}
	return &idTokenResponse{
		IDToken:          parsed.IDToken,
		RefreshToken:     parsed.RefreshToken,
		UserID:           parsed.LocalID,
		ExpiresInSeconds: exp,
	}, nil
}

func exchangeRefreshTokenForIDToken(ctx context.Context, apiKey, refreshToken string) (*idTokenResponse, error) {
	endpoint := "https://securetoken.googleapis.com/v1/token?key=" + apiKey
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("firebase: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firebase: securetoken POST: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("firebase: securetoken %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    string `json:"expires_in"`
		UserID       string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("firebase: parse securetoken response: %w", err)
	}
	exp := 3600
	if parsed.ExpiresIn != "" {
		_, _ = fmt.Sscanf(parsed.ExpiresIn, "%d", &exp)
	}
	return &idTokenResponse{
		IDToken:          parsed.IDToken,
		RefreshToken:     parsed.RefreshToken,
		UserID:           parsed.UserID,
		ExpiresInSeconds: exp,
	}, nil
}

func writeTokenFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("firebase: mkdir for refresh token: %w", err)
	}
	// 0600 — owner read/write only. Windows ignores the bits but the
	// subdirectory permissions still narrow the surface.
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("firebase: write refresh token: %w", err)
	}
	return nil
}

func looksLikeSixDigits(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < 6; i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// uidFromJWT decodes the `sub` (or `user_id`) claim from a JWT without
// verifying its signature — peershd already trusts the token because it
// just received it from Google's endpoint over TLS.
func uidFromJWT(jwt string) (string, bool) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", false
	}
	if v, ok := claims["sub"].(string); ok && v != "" {
		return v, true
	}
	if v, ok := claims["user_id"].(string); ok && v != "" {
		return v, true
	}
	return "", false
}
