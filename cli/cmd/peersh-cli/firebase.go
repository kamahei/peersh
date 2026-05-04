// Firebase auth bootstrap for peersh-cli, mirroring the subset of
// peershd's flows that make sense from a client perspective:
//
//   - one-shot browser sign-in (`-firebase-login`) that persists a
//     refresh token to disk for subsequent runs
//   - reuse of the persisted refresh token (no flag needed)
//   - pair-code consumption (`-pair-code`) for headless boot
//   - service-account JSON (`-firebase-credentials`) for CI / scripts
//
// The CLI never needs the wake listener, RTDB stream, FCM heartbeat,
// or device record bookkeeping — those are host-only concerns. We only
// need to mint a fresh Firebase ID token and hand it to the signaling
// Dial's FirebaseIDToken field; the signaling server's firebase auth
// provider validates the token and resolves user_id from its `uid`.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	fbpeershd "github.com/peersh/peersh/windows/firebase"
)

// firebaseOpts bundles the Firebase-related flag values so the
// dispatch path stays readable.
type firebaseOpts struct {
	ProjectID          string
	APIKey             string
	Region             string
	RtdbRegion         string
	TokenFile          string
	Login              bool
	PairCode           string
	Credentials        string
	Email              string
	UID                string
	GoogleClientID     string
	GoogleClientSecret string
}

// requested reports whether the operator (or the embedded build
// defaults) asked for Firebase mode.
func (o firebaseOpts) requested() bool {
	return o.ProjectID != "" || o.APIKey != "" || o.Login || o.PairCode != "" ||
		o.TokenFile != "" || o.Credentials != "" || o.Email != "" || o.UID != ""
}

// buildCLIFirebaseTokenSource picks the right Firebase backend based on
// which Firebase-related flags the operator passed. Mirrors peershd's
// buildFirebaseTokenSource; kept as a separate copy so the CLI doesn't
// drag in peershd's option struct.
func buildCLIFirebaseTokenSource(ctx context.Context, opts firebaseOpts) (fbpeershd.TokenSource, error) {
	if opts.APIKey == "" || opts.ProjectID == "" {
		return nil, errors.New("firebase mode requires -firebase-project and -firebase-api-key")
	}
	tokenPath := opts.TokenFile
	if tokenPath == "" {
		tokenPath = fbpeershd.DefaultRefreshTokenPath()
	}

	if opts.Login {
		slog.Info("starting browser-based Google sign-in", "project", opts.ProjectID, "token_file", tokenPath)
		src, err := fbpeershd.GoogleSignIn(ctx, opts.GoogleClientID, opts.GoogleClientSecret, opts.APIKey, tokenPath, nil)
		if err != nil {
			return nil, fmt.Errorf("firebase login: %w", err)
		}
		slog.Info("Google sign-in complete", "uid", src.UID())
		return src, nil
	}

	if opts.PairCode != "" {
		region := opts.Region
		if region == "" {
			region = "asia-northeast1"
		}
		slog.Info("pairing peersh-cli with Firebase", "project", opts.ProjectID, "token_file", tokenPath)
		src, err := fbpeershd.Pair(ctx, opts.ProjectID, region, opts.APIKey, opts.PairCode, tokenPath)
		if err != nil {
			return nil, fmt.Errorf("pair: %w", err)
		}
		return src, nil
	}

	if opts.Credentials != "" {
		if opts.Email == "" && opts.UID == "" {
			return nil, errors.New("-firebase-credentials requires one of -firebase-email or -firebase-uid")
		}
		src, err := fbpeershd.New(ctx, opts.ProjectID, opts.Credentials, opts.Email, opts.UID, opts.APIKey)
		if err != nil {
			return nil, fmt.Errorf("firebase auth source: %w", err)
		}
		return src, nil
	}

	src, err := fbpeershd.NewRefreshSource(tokenPath, opts.APIKey)
	if err != nil {
		return nil, fmt.Errorf("load persisted refresh token (run with -firebase-login or -pair-code first): %w", err)
	}
	slog.Info("loaded persisted Firebase refresh token", "token_file", tokenPath)
	return src, nil
}

// discoveredHost is one entry surfaced by listFirebaseHosts.
type discoveredHost struct {
	DeviceID       string
	DisplayName    string
	LastSeenUnixMs int64
}

// listFirebaseHosts reads /users/{uid}/devices from Realtime Database
// and returns the registered Windows hosts (kind=="host") sorted by
// most-recent activity. Mirrors the mobile app's
// FirebaseDeviceDiscoveryService so the operator's CLI sees the same
// list of PCs as their phone.
//
// Returns an empty slice if no hosts are registered. Callers should
// surface "run peershd with -firebase-login first" in that case.
func listFirebaseHosts(ctx context.Context, src fbpeershd.TokenSource, projectID, rtdbRegion string) ([]discoveredHost, error) {
	idToken, err := src.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase token: %w", err)
	}
	uid := src.UID()
	if uid == "" {
		return nil, errors.New("firebase: empty uid")
	}
	if rtdbRegion == "" {
		// Match peershd's default; required to build the RTDB URL.
		rtdbRegion = "asia-southeast1"
	}
	client, err := fbpeershd.NewClient(projectID, rtdbRegion)
	if err != nil {
		return nil, fmt.Errorf("rtdb client: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := client.Get(ctx, "/users/"+uid+"/devices", idToken, &raw); err != nil {
		return nil, fmt.Errorf("rtdb get devices: %w", err)
	}
	out := make([]discoveredHost, 0, len(raw))
	for deviceID, blob := range raw {
		// Each value can be a leaf int (legacy hosts that wrote only
		// last_seen_at) or a map carrying display_name / kind /
		// last_seen_at. Try the map first, fall back to the int.
		var asMap map[string]json.RawMessage
		if err := json.Unmarshal(blob, &asMap); err == nil && asMap != nil {
			kind := unmarshalString(asMap["kind"])
			if kind != "" && kind != "host" {
				continue
			}
			out = append(out, discoveredHost{
				DeviceID:       deviceID,
				DisplayName:    nonEmpty(unmarshalString(asMap["display_name"]), deviceID),
				LastSeenUnixMs: unmarshalInt(asMap["last_seen_at"]),
			})
			continue
		}
		var asInt int64
		if err := json.Unmarshal(blob, &asInt); err == nil {
			out = append(out, discoveredHost{
				DeviceID:       deviceID,
				DisplayName:    deviceID,
				LastSeenUnixMs: asInt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastSeenUnixMs > out[j].LastSeenUnixMs
	})
	return out, nil
}

func unmarshalString(b json.RawMessage) string {
	if len(b) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return ""
	}
	return s
}

func unmarshalInt(b json.RawMessage) int64 {
	if len(b) == 0 {
		return 0
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return 0
	}
	return n
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// fireWakeRequest pushes a /users/{uid}/wake_requests/<auto-id>
// record so a Firebase-mode peershd that's currently in the
// wake-listener idle state opens its short-lived signaling WS and
// responds to the CLI's incoming Connect. Mirrors the mobile app's
// _fireWakeRequest. Best-effort: errors are logged but don't abort
// the dial (a host with a persistent WS doesn't need the wake).
func fireWakeRequest(ctx context.Context, src fbpeershd.TokenSource, projectID, rtdbRegion, targetDeviceID string) {
	if rtdbRegion == "" {
		rtdbRegion = "asia-southeast1"
	}
	idToken, err := src.Token(ctx)
	if err != nil {
		slog.Warn("wake_request: token", "err", err)
		return
	}
	uid := src.UID()
	if uid == "" {
		slog.Warn("wake_request: empty uid")
		return
	}
	client, err := fbpeershd.NewClient(projectID, rtdbRegion)
	if err != nil {
		slog.Warn("wake_request: rtdb client", "err", err)
		return
	}
	body := map[string]any{
		"target_device_id": targetDeviceID,
		"created_at":       map[string]string{".sv": "timestamp"},
	}
	if _, err := client.Push(ctx, "/users/"+uid+"/wake_requests", body, idToken); err != nil {
		slog.Warn("wake_request: push", "err", err)
		return
	}
	slog.Info("wake_request fired", "target", targetDeviceID)
}
