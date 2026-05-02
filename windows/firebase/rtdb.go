// Realtime Database REST + SSE client.
//
// Why a hand-rolled client when firebase.google.com/go/v4/db exists?
// The Admin SDK supports CRUD via service-account credentials but does
// not expose the streaming Listen API. peershd needs Listen because the
// wake-listener path replaces the persistent signaling WebSocket — and
// the wake event fan-out has to work for pair-code-mode hosts that hold
// only Firebase ID tokens (the Admin SDK requires GCP IAM tokens).
//
// REST + SSE is the documented public API that accepts Firebase ID
// tokens in the ?auth= query parameter, which is exactly what the
// existing TokenSource interface produces.
//
// Reference: https://firebase.google.com/docs/reference/rest/database

package firebase

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client targets a single Realtime Database instance.
type Client struct {
	baseURL string
	// extraQuery is appended to every request URL. Used by the emulator
	// shim where the database namespace must be passed as ?ns=...
	extraQuery url.Values
	http       *http.Client
}

// NewClient constructs a Client for the named project and RTDB region.
// region "" defaults to "us-central1" which uses the legacy
// firebaseio.com host. All other regions use the modern
// firebasedatabase.app host. When the FIREBASE_DATABASE_EMULATOR_HOST
// env var is set (host:port form, no scheme), traffic is sent there
// instead and ?ns=<project>-default-rtdb is appended on each request.
func NewClient(projectID, region string) (*Client, error) {
	if projectID == "" {
		return nil, errors.New("firebase: empty project id")
	}
	if emu := os.Getenv("FIREBASE_DATABASE_EMULATOR_HOST"); emu != "" {
		return &Client{
			baseURL:    "http://" + emu,
			extraQuery: url.Values{"ns": []string{projectID + "-default-rtdb"}},
			http:       &http.Client{},
		}, nil
	}
	host := projectID + "-default-rtdb." + rtdbHostSuffix(region)
	return &Client{
		baseURL: "https://" + host,
		http:    &http.Client{},
	}, nil
}

func rtdbHostSuffix(region string) string {
	switch region {
	case "", "us-central1":
		return "firebaseio.com"
	default:
		return region + ".firebasedatabase.app"
	}
}

// url builds a request URL for the given RTDB path.
// path uses forward slashes ("/users/{uid}/devices/{deviceId}"),
// without the trailing ".json" — that suffix is appended here. extra
// may be nil; the client's extraQuery (e.g. emulator's ?ns=...) is
// always merged in.
func (c *Client) url(path string, extra url.Values) string {
	rest := strings.TrimPrefix(path, "/") + ".json"
	combined := url.Values{}
	for k, vs := range c.extraQuery {
		combined[k] = vs
	}
	for k, vs := range extra {
		combined[k] = vs
	}
	out := c.baseURL + "/" + rest
	if len(combined) > 0 {
		out += "?" + combined.Encode()
	}
	return out
}

// Set issues a PUT (full replace) on path with body marshalled as JSON.
// idToken is the Firebase ID token used for ?auth=.
func (c *Client) Set(ctx context.Context, path string, body any, idToken string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("rtdb: marshal body: %w", err)
	}
	q := url.Values{}
	if idToken != "" {
		q.Set("auth", idToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.url(path, q), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("rtdb: build PUT: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rtdb: PUT %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rtdb: PUT %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}
	return nil
}

// Push issues a POST (server-generated child key) on path with body.
// Returns the auto-generated child key (the "name" field).
func (c *Client) Push(ctx context.Context, path string, body any, idToken string) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("rtdb: marshal body: %w", err)
	}
	q := url.Values{}
	if idToken != "" {
		q.Set("auth", idToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path, q), bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("rtdb: build POST: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("rtdb: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("rtdb: POST %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("rtdb: decode POST response: %w", err)
	}
	return out.Name, nil
}

// Delete removes the value at path.
func (c *Client) Delete(ctx context.Context, path string, idToken string) error {
	q := url.Values{}
	if idToken != "" {
		q.Set("auth", idToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url(path, q), nil)
	if err != nil {
		return fmt.Errorf("rtdb: build DELETE: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rtdb: DELETE %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rtdb: DELETE %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}
	return nil
}

// Get fetches the JSON value at path into out. out follows
// json.Unmarshal semantics.
func (c *Client) Get(ctx context.Context, path string, idToken string, out any) error {
	q := url.Values{}
	if idToken != "" {
		q.Set("auth", idToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path, q), nil)
	if err != nil {
		return fmt.Errorf("rtdb: build GET: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rtdb: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rtdb: GET %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- Server-Sent Events streaming ----------------------------------------

// SSEEvent is one parsed RTDB streaming frame.
//
// Kind is the event name reported by the server: "put", "patch",
// "keep-alive", "cancel", or "auth_revoked".
//
// For "put" and "patch", Path is the relative path inside the
// subscribed resource ("/" means the entire resource was replaced;
// "/<key>" means a single child changed) and Data is the new value
// (nil = deletion).
//
// "keep-alive" / "cancel" / "auth_revoked" have empty Path and nil
// Data. Cancel and auth_revoked indicate the stream is no longer valid
// and the caller should reconnect (with a fresh token for auth_revoked).
type SSEEvent struct {
	Kind string
	Path string
	Data json.RawMessage
}

// Stream is an open SSE subscription. Read events from C(); call Close
// to tear down the underlying HTTP body.
type Stream struct {
	body   io.ReadCloser
	events chan SSEEvent
	errCh  chan error
}

// C returns the channel of parsed SSE events.
func (s *Stream) C() <-chan SSEEvent { return s.events }

// Err returns the first error that ended the stream (or nil if it
// closed cleanly via Close). Blocks until the reader goroutine exits.
func (s *Stream) Err() error { return <-s.errCh }

// Close stops the reader goroutine.
func (s *Stream) Close() error { return s.body.Close() }

// Listen opens an SSE subscription on path. Caller owns the returned
// *Stream and must Close() it. The connection's underlying TCP/TLS
// stays open for the duration; reconnect-on-error is the caller's
// responsibility (typically with a fresh token).
func (c *Client) Listen(ctx context.Context, path string, idToken string) (*Stream, error) {
	q := url.Values{}
	if idToken != "" {
		q.Set("auth", idToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path, q), nil)
	if err != nil {
		return nil, fmt.Errorf("rtdb: build Listen: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	// Use a transport without read timeout so the long-lived stream
	// doesn't get cut by the default http.Client. Reuse the existing
	// http.Client's Transport but override Timeout via a per-request
	// Context (no Client.Timeout).
	streamClient := &http.Client{Transport: c.http.Transport}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rtdb: Listen: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("rtdb: Listen %s: HTTP %d: %s", path, resp.StatusCode, string(body))
	}

	s := &Stream{
		body:   resp.Body,
		events: make(chan SSEEvent, 16),
		errCh:  make(chan error, 1),
	}
	go s.run()
	return s, nil
}

// run consumes the SSE wire format from s.body and surfaces parsed
// events on s.events. Exits on body close or parse error.
func (s *Stream) run() {
	defer close(s.events)
	scanner := bufio.NewScanner(s.body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var ev SSEEvent
	flush := func() {
		if ev.Kind == "" {
			return
		}
		// keep-alive / cancel / auth_revoked carry no path/data;
		// surface them so the caller can react.
		if ev.Kind == "put" || ev.Kind == "patch" {
			var payload struct {
				Path string          `json:"path"`
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(ev.Data, &payload); err == nil {
				ev.Path = payload.Path
				ev.Data = payload.Data
			}
		}
		s.events <- ev
		ev = SSEEvent{}
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			ev.Kind = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			ev.Data = json.RawMessage(strings.TrimSpace(line[len("data:"):]))
		case line == "":
			flush()
		}
	}
	flush()

	if err := scanner.Err(); err != nil {
		s.errCh <- err
	} else {
		s.errCh <- nil
	}
}

// PathToKey returns the last path segment, e.g. "/wake-1" -> "wake-1".
// Useful when an SSE put with path "/wake-1" represents a child change.
func PathToKey(path string) string {
	return strings.TrimPrefix(path, "/")
}

// ServerTimestamp is the RTDB sentinel value that the server replaces
// with its own clock at write time. Use as a map value:
//
//	{"last_seen_at": rtdb.ServerTimestamp}
var ServerTimestamp = map[string]string{".sv": "timestamp"}

// Touch returns a Set wrapper that retries once when the network
// returns a transient error. Useful for heartbeats where a missed
// write is recoverable but a permanent failure should surface.
func (c *Client) Touch(ctx context.Context, path string, body any, idToken string) error {
	if err := c.Set(ctx, path, body, idToken); err != nil {
		// One short retry for transient connectivity issues.
		select {
		case <-ctx.Done():
			return err
		case <-time.After(500 * time.Millisecond):
		}
		return c.Set(ctx, path, body, idToken)
	}
	return nil
}
