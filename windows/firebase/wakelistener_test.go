package firebase

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestAsString(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"empty string", "", ""},
		{"nil", nil, ""},
		{"int", 42, ""},
		{"bool", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := asString(tc.in)
			if got != tc.want {
				t.Errorf("asString(%v) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAsBool(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"true", true, true},
		{"false", false, false},
		{"nil", nil, false},
		{"string", "true", false},
		{"int", 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := asBool(tc.in)
			if got != tc.want {
				t.Errorf("asBool(%v) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// CloseIsIdempotent verifies the listener can be Close()d before Start
// and multiple times safely.
func TestWakeListener_CloseBeforeStart(t *testing.T) {
	wl := NewWakeListener(nil, "u1", "d1")
	wl.Close()
	wl.Close() // second call must not panic
}

// --- emulator-gated integration tests --------------------------------------
//
// Run only when FIRESTORE_EMULATOR_HOST is set. See devices_test.go for
// setup instructions.

func TestWakeListener_DeliversAddedDoc(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; skipping emulator integration test")
	}
	c := newEmulatorClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	uid, dev := "u1", "wake-listener-1"
	wl := NewWakeListener(c, uid, dev)
	wl.Start(ctx)
	defer wl.Close()

	// Allow the listener to subscribe before we write.
	time.Sleep(200 * time.Millisecond)

	rid := "wake-event-1"
	_, err := c.Collection("users").Doc(uid).Collection("wake_requests").Doc(rid).Set(ctx, map[string]any{
		"target_device_id": dev,
		"mobile_device_id": "mob-1",
		"consumed":         false,
	})
	if err != nil {
		t.Fatalf("seed wake_request: %v", err)
	}

	select {
	case ev := <-wl.C():
		if ev.RequestID != rid {
			t.Errorf("RequestID = %q; want %q", ev.RequestID, rid)
		}
		if ev.MobileDeviceID != "mob-1" {
			t.Errorf("MobileDeviceID = %q; want %q", ev.MobileDeviceID, "mob-1")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for wake event")
	}
}

func TestWakeListener_FiltersConsumedDocs(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; skipping emulator integration test")
	}
	c := newEmulatorClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	uid, dev := "u1", "wake-listener-2"

	// Pre-seed an already-consumed doc; it must NOT surface to C().
	_, err := c.Collection("users").Doc(uid).Collection("wake_requests").Doc("old").Set(ctx, map[string]any{
		"target_device_id": dev,
		"consumed":         true,
	})
	if err != nil {
		t.Fatalf("seed consumed doc: %v", err)
	}

	wl := NewWakeListener(c, uid, dev)
	wl.Start(ctx)
	defer wl.Close()

	select {
	case ev := <-wl.C():
		t.Errorf("unexpected event for consumed doc: %+v", ev)
	case <-time.After(1 * time.Second):
		// Expected: no event delivered.
	}
}

func TestWakeListener_IgnoresOtherDevices(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; skipping emulator integration test")
	}
	c := newEmulatorClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	uid, dev := "u1", "wake-listener-3"
	wl := NewWakeListener(c, uid, dev)
	wl.Start(ctx)
	defer wl.Close()

	time.Sleep(200 * time.Millisecond)

	// Write a wake aimed at a different device.
	_, err := c.Collection("users").Doc(uid).Collection("wake_requests").Doc("misdirected").Set(ctx, map[string]any{
		"target_device_id": "some-other-device",
		"consumed":         false,
	})
	if err != nil {
		t.Fatalf("seed mismatched doc: %v", err)
	}

	select {
	case ev := <-wl.C():
		t.Errorf("listener picked up a wake for another device: %+v", ev)
	case <-time.After(1 * time.Second):
		// Expected: query filter prevented delivery.
	}
}
