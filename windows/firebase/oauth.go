// Browser-based Google sign-in for peershd.
//
// One-shot bootstrap (`-firebase-login`):
//
//   1. peershd opens a localhost listener on a random port, generates a
//      PKCE code_verifier + code_challenge pair, and opens the user's
//      default browser at Google's OAuth 2.0 authorization endpoint.
//   2. The user signs in with Google in the browser; Google redirects
//      to http://127.0.0.1:<port>/?code=AUTHCODE.
//   3. peershd exchanges the authorization code for a Google ID token
//      (PKCE — no client secret strictly required, but we accept one
//      since Google's "Desktop app" client type asks for it anyway).
//   4. POST identitytoolkit.googleapis.com/v1/accounts:signInWithIdp
//      with the Google ID token -> Firebase ID + refresh token.
//   5. Persist the refresh token (same path the pair-code flow uses).
//
// From then on peershd uses RefreshAuthSource — identical to the
// post-pair-code state.

package firebase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// GoogleSignIn runs the OAuth 2.0 Installed App Flow against Google,
// trades the resulting Google ID token for a Firebase refresh token via
// signInWithIdp, persists the refresh token to tokenPath, and returns a
// ready-to-use RefreshAuthSource.
//
// clientID / clientSecret are the operator's OAuth 2.0 "Desktop app"
// client credentials (created once per Firebase project in
// Google Cloud Console -> APIs & Services -> Credentials). For an
// "Installed" client the secret is not really secret — Google's docs
// explicitly say so — but the token endpoint still requires it.
//
// apiKey is the Firebase Web API key (for signInWithIdp + refresh).
func GoogleSignIn(
	ctx context.Context,
	clientID, clientSecret, apiKey, tokenPath string,
	openBrowser func(string) error,
) (*RefreshAuthSource, error) {
	if clientID == "" {
		return nil, errors.New("firebase: -google-client-id is required for -firebase-login")
	}
	if apiKey == "" {
		return nil, errors.New("firebase: -firebase-api-key is required for -firebase-login")
	}
	if tokenPath == "" {
		return nil, errors.New("firebase: empty token path")
	}
	if openBrowser == nil {
		openBrowser = OpenBrowser
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("firebase: bind loopback: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d", port)

	verifier, err := pkceVerifier()
	if err != nil {
		return nil, err
	}
	challenge := pkceChallenge(verifier)
	state, err := randomURLToken(24)
	if err != nil {
		return nil, err
	}

	authURL := buildGoogleAuthURL(clientID, redirectURI, challenge, state)

	type cbResult struct {
		code  string
		state string
		err   error
	}
	resCh := make(chan cbResult, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errStr := q.Get("error"); errStr != "" {
			fmt.Fprintln(w, "Sign-in failed:", errStr)
			resCh <- cbResult{err: fmt.Errorf("oauth error: %s (%s)", errStr, q.Get("error_description"))}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, `<!doctype html><meta charset="utf-8"><title>peersh</title>
<body style="font-family:system-ui;text-align:center;padding-top:80px">
<h1>peersh</h1>
<p>Sign-in successful. You can close this window and return to peershd.</p>
</body>`)
		resCh <- cbResult{code: q.Get("code"), state: q.Get("state")}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := openBrowser(authURL); err != nil {
		return nil, fmt.Errorf("firebase: open browser: %w (URL: %s)", err, authURL)
	}

	timeout := 5 * time.Minute
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("firebase: sign-in timed out after %s", timeout)
	case r := <-resCh:
		if r.err != nil {
			return nil, r.err
		}
		if r.state != state {
			return nil, errors.New("firebase: oauth state mismatch")
		}
		if r.code == "" {
			return nil, errors.New("firebase: oauth callback missing code")
		}
		google, err := exchangeAuthCodeForGoogleIDToken(ctx, clientID, clientSecret, r.code, redirectURI, verifier)
		if err != nil {
			return nil, err
		}
		fbTokens, err := signInWithGoogleIDToken(ctx, apiKey, google.IDToken, redirectURI)
		if err != nil {
			return nil, err
		}
		if fbTokens.RefreshToken == "" {
			return nil, errors.New("firebase: signInWithIdp returned empty refresh token")
		}
		if err := writeTokenFile(tokenPath, fbTokens.RefreshToken); err != nil {
			return nil, err
		}
		return &RefreshAuthSource{
			apiKey:       apiKey,
			tokenPath:    tokenPath,
			refreshToken: fbTokens.RefreshToken,
			uid:          fbTokens.UserID,
			idToken:      fbTokens.IDToken,
			expires:      time.Now().Add(time.Duration(fbTokens.ExpiresInSeconds) * time.Second),
		}, nil
	}
}

// OpenBrowser opens url in the user's default browser. Returns an error
// if the platform-specific command cannot be launched.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 is more reliable than `start` when invoked from a
		// non-cmd context (services, headless shells).
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// --- internals -------------------------------------------------------

const googleAuthEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
const googleTokenEndpoint = "https://oauth2.googleapis.com/token"

func buildGoogleAuthURL(clientID, redirectURI, challenge, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "openid email profile")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("access_type", "online")
	q.Set("prompt", "select_account")
	return googleAuthEndpoint + "?" + q.Encode()
}

type googleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeAuthCodeForGoogleIDToken(
	ctx context.Context,
	clientID, clientSecret, code, redirectURI, codeVerifier string,
) (*googleTokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	form.Set("code", code)
	form.Set("code_verifier", codeVerifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("firebase: build google token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firebase: google token POST: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("firebase: google token %d: %s", resp.StatusCode, string(raw))
	}
	out := &googleTokenResponse{}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("firebase: parse google token response: %w", err)
	}
	if out.IDToken == "" {
		return nil, errors.New("firebase: google token response missing id_token")
	}
	return out, nil
}

func signInWithGoogleIDToken(ctx context.Context, apiKey, googleIDToken, requestURI string) (*idTokenResponse, error) {
	endpoint := "https://identitytoolkit.googleapis.com/v1/accounts:signInWithIdp?key=" + apiKey
	postBody := fmt.Sprintf("id_token=%s&providerId=google.com", url.QueryEscape(googleIDToken))
	body, _ := json.Marshal(map[string]any{
		"postBody":            postBody,
		"requestUri":          requestURI,
		"returnSecureToken":   true,
		"returnIdpCredential": false,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("firebase: build signInWithIdp request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firebase: signInWithIdp POST: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("firebase: signInWithIdp %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		IDToken      string `json:"idToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    string `json:"expiresIn"`
		LocalID      string `json:"localId"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("firebase: parse signInWithIdp response: %w", err)
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

func pkceVerifier() (string, error) { return randomURLToken(64) }

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomURLToken(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("firebase: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
