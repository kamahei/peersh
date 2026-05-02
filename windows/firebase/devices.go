// Service-account-mode-only Firestore helpers used by the wake-listener
// path in peershd.
//
// Background: cloud.google.com/go/firestore is a server client library
// that authenticates with GCP IAM (service account or ADC) and bypasses
// Firebase Security Rules. It does not accept Firebase ID tokens, so
// pair-code mode hosts (which only have Firebase ID tokens minted via
// the Identity Toolkit refresh-token flow) cannot use this client.
// Pair-code mode keeps the persistent-WS path in main.go.
//
// Wake-mode hosts use this package to:
//   - Stamp users/{uid}/devices/{deviceId}.last_seen_at so mobile clients
//     can decide the host is reachable before issuing a wake request.
//   - Mark wake_requests/{rid}.consumed = true after a wake has been
//     handled, to stop the snapshot listener re-firing on the same doc.

package firebase

import (
	"context"
	"errors"
	"fmt"

	fs "cloud.google.com/go/firestore"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// OpenFirestoreClient returns a Firestore client authenticated with
// service-account credentials at credentialsPath, or with Application
// Default Credentials when credentialsPath is empty. Caller owns the
// returned client and must call Close when done.
func OpenFirestoreClient(ctx context.Context, projectID, credentialsPath string) (*fs.Client, error) {
	if projectID == "" {
		return nil, errors.New("firebase: empty project id")
	}
	opts := []option.ClientOption{}
	if credentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsPath))
	}
	c, err := fs.NewClient(ctx, projectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("firebase: firestore NewClient: %w", err)
	}
	return c, nil
}

// RegisterDevice upserts users/{uid}/devices/{deviceID} with last_seen_at
// set to the server's current time. Other fields (kind, display_name,
// public_key, created_at) are left to the signaling server's Register
// path, which fills them via store.Store.PutDevice on the next WS Hello.
func RegisterDevice(ctx context.Context, client *fs.Client, uid, deviceID string) error {
	_, err := client.Collection("users").Doc(uid).
		Collection("devices").Doc(deviceID).
		Set(ctx, map[string]any{
			"last_seen_at": fs.ServerTimestamp,
		}, fs.MergeAll)
	if err != nil {
		return fmt.Errorf("firebase: RegisterDevice: %w", err)
	}
	return nil
}

// Heartbeat refreshes last_seen_at on the device doc. peershd calls
// this periodically (default 5 min) so a mobile presence read sees a
// fresh timestamp without rewriting other fields.
func Heartbeat(ctx context.Context, client *fs.Client, uid, deviceID string) error {
	_, err := client.Collection("users").Doc(uid).
		Collection("devices").Doc(deviceID).
		Set(ctx, map[string]any{
			"last_seen_at": fs.ServerTimestamp,
		}, fs.MergeAll)
	if err != nil {
		return fmt.Errorf("firebase: Heartbeat: %w", err)
	}
	return nil
}

// Runtime bundles the Firestore client and the wake listener so the
// rest of peershd doesn't need to import cloud.google.com/go/firestore
// directly. Construct via StartWakeRuntime; Close to release both.
type Runtime struct {
	Client   *fs.Client
	Listener *WakeListener
	UID      string
	DeviceID string
}

// StartWakeRuntime opens a Firestore client, upserts the device's
// last_seen_at, and starts a wake listener attached to ctx. The
// returned Runtime owns both resources.
func StartWakeRuntime(ctx context.Context, projectID, credentialsPath, uid, deviceID string) (*Runtime, error) {
	client, err := OpenFirestoreClient(ctx, projectID, credentialsPath)
	if err != nil {
		return nil, err
	}
	if err := RegisterDevice(ctx, client, uid, deviceID); err != nil {
		_ = client.Close()
		return nil, err
	}
	listener := NewWakeListener(client, uid, deviceID)
	listener.Start(ctx)
	return &Runtime{
		Client:   client,
		Listener: listener,
		UID:      uid,
		DeviceID: deviceID,
	}, nil
}

// Events returns the channel of wake notifications.
func (r *Runtime) Events() <-chan WakeEvent { return r.Listener.C() }

// Heartbeat refreshes the device's last_seen_at timestamp.
func (r *Runtime) Heartbeat(ctx context.Context) error {
	return Heartbeat(ctx, r.Client, r.UID, r.DeviceID)
}

// MarkConsumed flips wake_requests/{requestID}.consumed = true.
func (r *Runtime) MarkConsumed(ctx context.Context, requestID string) error {
	return MarkConsumed(ctx, r.Client, r.UID, requestID)
}

// Close stops the listener and releases the Firestore client.
func (r *Runtime) Close() error {
	if r.Listener != nil {
		r.Listener.Close()
	}
	if r.Client != nil {
		return r.Client.Close()
	}
	return nil
}

// MarkConsumed flips wake_requests/{requestID}.consumed = true so the
// snapshot listener stops re-firing on the same wake event. Returns nil
// if the document was already deleted (TTL expired).
func MarkConsumed(ctx context.Context, client *fs.Client, uid, requestID string) error {
	_, err := client.Collection("users").Doc(uid).
		Collection("wake_requests").Doc(requestID).
		Set(ctx, map[string]any{
			"consumed":    true,
			"consumed_at": fs.ServerTimestamp,
		}, fs.MergeAll)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("firebase: MarkConsumed: %w", err)
	}
	return nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	return st.Code() == codes.NotFound
}
