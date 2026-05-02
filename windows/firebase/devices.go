// Realtime Database helpers used by the wake-listener path in peershd.
//
// All mutating operations require a Firebase ID token from the
// host's TokenSource (service-account or pair-code mode — both
// produce ID tokens via the existing AuthSource / RefreshAuthSource
// types in firebase.go and pairing.go). The token is appended to
// each REST request as ?auth=<token>; RTDB security rules apply.
//
// Wake-event consumption is a DELETE on
// /users/{uid}/wake_requests/{requestID}. Deleting the document
// (rather than flipping a "consumed" flag) is the simplest signal to
// the listener — it surfaces the deletion as a put with data:null —
// and naturally bounds the wake_requests subtree size if the host
// recovers from a crash before the document expires.

package firebase

import (
	"context"
	"fmt"
)

// devicePath returns "/users/{uid}/devices/{deviceID}".
func devicePath(uid, deviceID string) string {
	return "/users/" + uid + "/devices/" + deviceID
}

// wakeRequestPath returns "/users/{uid}/wake_requests/{requestID}".
func wakeRequestPath(uid, requestID string) string {
	return "/users/" + uid + "/wake_requests/" + requestID
}

// RegisterDevice upserts /users/{uid}/devices/{deviceID} with
// last_seen_at = ServerTimestamp. The signaling server's Register
// handler still owns the kind / display_name / public_key fields via
// its own Firestore writes; this helper exists so wake-mode hosts
// (which keep their signaling WS closed most of the time) can keep
// their presence timestamp fresh independently.
func RegisterDevice(ctx context.Context, client *Client, src TokenSource, uid, deviceID string) error {
	idToken, err := src.Token(ctx)
	if err != nil {
		return fmt.Errorf("rtdb: RegisterDevice token: %w", err)
	}
	if err := client.Set(ctx, devicePath(uid, deviceID)+"/last_seen_at", ServerTimestamp, idToken); err != nil {
		return fmt.Errorf("rtdb: RegisterDevice: %w", err)
	}
	return nil
}

// Heartbeat refreshes the device's last_seen_at timestamp.
func Heartbeat(ctx context.Context, client *Client, src TokenSource, uid, deviceID string) error {
	idToken, err := src.Token(ctx)
	if err != nil {
		return fmt.Errorf("rtdb: Heartbeat token: %w", err)
	}
	if err := client.Set(ctx, devicePath(uid, deviceID)+"/last_seen_at", ServerTimestamp, idToken); err != nil {
		return fmt.Errorf("rtdb: Heartbeat: %w", err)
	}
	return nil
}

// DeleteWakeRequest removes the wake_request that was just handled.
// The listener observes this as a put with data:null and skips it,
// so a successfully-handled wake never re-fires across reconnects.
func DeleteWakeRequest(ctx context.Context, client *Client, src TokenSource, uid, requestID string) error {
	idToken, err := src.Token(ctx)
	if err != nil {
		return fmt.Errorf("rtdb: DeleteWakeRequest token: %w", err)
	}
	if err := client.Delete(ctx, wakeRequestPath(uid, requestID), idToken); err != nil {
		return fmt.Errorf("rtdb: DeleteWakeRequest: %w", err)
	}
	return nil
}

// Runtime bundles the RTDB Client, the TokenSource, and the
// WakeListener so the rest of peershd doesn't need to thread three
// values through every call site.
type Runtime struct {
	Client   *Client
	Listener *WakeListener
	Source   TokenSource
	UID      string
	DeviceID string
}

// StartWakeRuntime opens an RTDB client (project + region from the
// caller), upserts the device's last_seen_at, and starts a wake
// listener attached to ctx. The returned Runtime owns both
// resources; call Close to stop the listener (the http.Client used
// by *Client is stateless and needs no explicit cleanup).
func StartWakeRuntime(ctx context.Context, projectID, region string, src TokenSource, uid, deviceID string) (*Runtime, error) {
	client, err := NewClient(projectID, region)
	if err != nil {
		return nil, err
	}
	if err := RegisterDevice(ctx, client, src, uid, deviceID); err != nil {
		return nil, err
	}
	listener := NewWakeListener(client, src, uid, deviceID)
	listener.Start(ctx)
	return &Runtime{
		Client:   client,
		Listener: listener,
		Source:   src,
		UID:      uid,
		DeviceID: deviceID,
	}, nil
}

// Events returns the channel of wake notifications.
func (r *Runtime) Events() <-chan WakeEvent { return r.Listener.C() }

// Heartbeat refreshes the device's last_seen_at timestamp.
func (r *Runtime) Heartbeat(ctx context.Context) error {
	return Heartbeat(ctx, r.Client, r.Source, r.UID, r.DeviceID)
}

// DeleteWakeRequest removes the wake_request that was just handled.
func (r *Runtime) DeleteWakeRequest(ctx context.Context, requestID string) error {
	return DeleteWakeRequest(ctx, r.Client, r.Source, r.UID, requestID)
}

// Close stops the listener.
func (r *Runtime) Close() error {
	if r.Listener != nil {
		r.Listener.Close()
	}
	return nil
}
