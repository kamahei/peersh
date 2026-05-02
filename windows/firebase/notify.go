// Notification dispatch — host writes a notification request to RTDB,
// the Cloud Function `onNotificationCreated` reads the target mobile
// device's FCM token and sends the actual push.
//
// This file defines the host-side API the wake-pump / serveStream
// callers use; the Cloud Function side lives in firebase/functions/.

package firebase

import (
	"context"
	"fmt"
)

// Notifier is the abstraction servePTYStream uses to dispatch a
// command-completion notification. Nil-safe: a nil receiver is a no-op
// so PSK-mode peershd compiles without conditional checks.
type Notifier interface {
	NotifyCommandReady(ctx context.Context, payload NotificationPayload) error
}

// NotificationPayload carries everything the Cloud Function needs to
// build the actual FCM message.
type NotificationPayload struct {
	// MobileDeviceID is the UUID the mobile registered under
	// users/{uid}/devices/{...}. Used to look up the FCM token.
	MobileDeviceID string

	// HostDeviceID is the 16-char base32 device id of this peershd.
	// Echoed into the notification payload for telemetry.
	HostDeviceID string

	// Title is the notification headline.
	Title string

	// Body is the notification body text.
	Body string

	// DeepLink is the intent payload that maps the notification tap
	// back to the originating tab (server_id / tab_id / pty_id /
	// host_device_id, etc.). All values are strings so they survive
	// the FCM data-message wire format.
	DeepLink map[string]string
}

// NotifyCommandReady on a *Runtime writes the request to RTDB. The
// Cloud Function trigger handles the actual FCM dispatch and cleans
// up the source doc.
func (r *Runtime) NotifyCommandReady(ctx context.Context, p NotificationPayload) error {
	if r == nil {
		return nil
	}
	idToken, err := r.Source.Token(ctx)
	if err != nil {
		return fmt.Errorf("notify: token: %w", err)
	}
	body := map[string]any{
		"target_mobile_device_id": p.MobileDeviceID,
		"host_device_id":          p.HostDeviceID,
		"title":                   p.Title,
		"body":                    p.Body,
		"deep_link":               p.DeepLink,
		"created_at":              ServerTimestamp,
	}
	path := "/users/" + r.UID + "/notifications"
	if _, err := r.Client.Push(ctx, path, body, idToken); err != nil {
		return fmt.Errorf("notify: push: %w", err)
	}
	return nil
}
