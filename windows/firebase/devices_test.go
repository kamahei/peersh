package firebase

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	fs "cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"grpc not found", status.Error(codes.NotFound, "missing"), true},
		{"grpc internal", status.Error(codes.Internal, "internal"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isNotFound(tc.err)
			if got != tc.want {
				t.Errorf("isNotFound(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestOpenFirestoreClient_RejectsEmptyProject(t *testing.T) {
	_, err := OpenFirestoreClient(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected error on empty project id")
	}
	if !strings.Contains(err.Error(), "empty project id") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// --- emulator-gated integration tests --------------------------------------
//
// These run only when FIRESTORE_EMULATOR_HOST is set (the standard env
// var honored by cloud.google.com/go/firestore). Start an emulator
// with:
//
//	gcloud emulators firestore start --host-port=127.0.0.1:8080
//
// then export FIRESTORE_EMULATOR_HOST=127.0.0.1:8080 before
// `go test ./...`.

func newEmulatorClient(t *testing.T) *fs.Client {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; skipping emulator integration test")
	}
	ctx := context.Background()
	c, err := fs.NewClient(ctx, "peersh-test")
	if err != nil {
		t.Fatalf("emulator NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRegisterDevice_StampsLastSeenAt(t *testing.T) {
	c := newEmulatorClient(t)
	ctx := context.Background()
	uid, dev := "u1", "dev-register-1"
	if err := RegisterDevice(ctx, c, uid, dev); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	snap, err := c.Collection("users").Doc(uid).Collection("devices").Doc(dev).Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := snap.Data()["last_seen_at"]; !ok {
		t.Errorf("expected last_seen_at field; got data=%v", snap.Data())
	}
}

func TestRegisterDevice_IsIdempotent(t *testing.T) {
	c := newEmulatorClient(t)
	ctx := context.Background()
	uid, dev := "u1", "dev-register-2"
	for i := 0; i < 3; i++ {
		if err := RegisterDevice(ctx, c, uid, dev); err != nil {
			t.Fatalf("RegisterDevice attempt %d: %v", i, err)
		}
	}
	snap, err := c.Collection("users").Doc(uid).Collection("devices").Doc(dev).Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := snap.Data()["last_seen_at"]; !ok {
		t.Errorf("expected last_seen_at after repeated upserts; got %v", snap.Data())
	}
}

func TestHeartbeat_BumpsLastSeenAt(t *testing.T) {
	c := newEmulatorClient(t)
	ctx := context.Background()
	uid, dev := "u1", "dev-heartbeat"
	if err := RegisterDevice(ctx, c, uid, dev); err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	first, err := c.Collection("users").Doc(uid).Collection("devices").Doc(dev).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstTs := first.Data()["last_seen_at"].(time.Time)

	// Server timestamps tick at millisecond granularity; sleep to
	// ensure the second write resolves to a later instant.
	time.Sleep(10 * time.Millisecond)

	if err := Heartbeat(ctx, c, uid, dev); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	second, err := c.Collection("users").Doc(uid).Collection("devices").Doc(dev).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondTs := second.Data()["last_seen_at"].(time.Time)
	if !secondTs.After(firstTs) {
		t.Errorf("Heartbeat did not advance last_seen_at: first=%v second=%v", firstTs, secondTs)
	}
}

func TestMarkConsumed_FlipsConsumed(t *testing.T) {
	c := newEmulatorClient(t)
	ctx := context.Background()
	uid, rid := "u1", "wake-mark-1"
	_, err := c.Collection("users").Doc(uid).Collection("wake_requests").Doc(rid).Set(ctx, map[string]any{
		"target_device_id": "dev-x",
		"consumed":         false,
	})
	if err != nil {
		t.Fatalf("seed wake_request: %v", err)
	}
	if err := MarkConsumed(ctx, c, uid, rid); err != nil {
		t.Fatalf("MarkConsumed: %v", err)
	}
	snap, err := c.Collection("users").Doc(uid).Collection("wake_requests").Doc(rid).Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v, _ := snap.Data()["consumed"].(bool); !v {
		t.Errorf("consumed not flipped: %v", snap.Data())
	}
	if _, ok := snap.Data()["consumed_at"]; !ok {
		t.Errorf("consumed_at not stamped: %v", snap.Data())
	}
}

func TestMarkConsumed_OnMissingDocIsNoop(t *testing.T) {
	c := newEmulatorClient(t)
	ctx := context.Background()
	// MarkConsumed uses Set+MergeAll which creates the doc if it
	// doesn't exist (Firestore semantics). The contract is "no
	// error"; we don't require that the doc remain absent.
	if err := MarkConsumed(ctx, c, "u1", "wake-never-existed"); err != nil {
		t.Fatalf("MarkConsumed on missing doc: %v", err)
	}
}
