package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/core/store/sqlite"
)

func newTempStore(t *testing.T) *sqlite.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peersh-test.db")
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestImplementsStoreInterface(t *testing.T) {
	var _ store.Store = newTempStore(t)
}

func TestMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peersh-test.db")
	first, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	first.Close()
	second, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	second.Close()
}

func TestUserAndPSKAndDevice(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	u := store.User{ID: "alice", AuthProvider: store.AuthProviderPSK, CreatedAt: now}
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	gotU, err := s.GetUser(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if gotU.AuthProvider != store.AuthProviderPSK || !gotU.CreatedAt.Equal(now) {
		t.Fatalf("user round-trip failed: %+v", gotU)
	}

	r := store.PSKRecord{
		UserID:       "alice",
		Secret:       []byte("highly-random-secret-bytes"),
		DisplayLabel: "alice-laptop",
		CreatedAt:    now,
	}
	if err := s.PutPSKRecord(ctx, r); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}
	gotR, err := s.GetPSKRecord(ctx, "alice")
	if err != nil {
		t.Fatalf("GetPSKRecord: %v", err)
	}
	if string(gotR.Secret) != string(r.Secret) || gotR.DisplayLabel != "alice-laptop" {
		t.Fatalf("psk round-trip failed: %+v", gotR)
	}
	if gotR.IsRevoked() {
		t.Fatal("expected not revoked")
	}

	d := store.Device{
		ID:          "DEVICE000000000A",
		PublicKey:   []byte{1, 2, 3, 4},
		OwnerUserID: "alice",
		Kind:        store.DeviceKindWindowsHost,
		DisplayName: "test-host",
		CreatedAt:   now,
		LastSeenAt:  now,
	}
	if err := s.PutDevice(ctx, d); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	gotD, err := s.GetDevice(ctx, "DEVICE000000000A")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if gotD.OwnerUserID != "alice" || string(gotD.PublicKey) != string(d.PublicKey) {
		t.Fatalf("device round-trip failed: %+v", gotD)
	}

	list, err := s.ListDevicesByOwner(ctx, "alice")
	if err != nil {
		t.Fatalf("ListDevicesByOwner: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 device, got %d", len(list))
	}
}

func TestPSKRevoke(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t)
	r := store.PSKRecord{UserID: "x", Secret: []byte("s"), CreatedAt: time.Now().UTC()}
	if err := s.PutPSKRecord(ctx, r); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}
	r.RevokedAt = time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	if err := s.PutPSKRecord(ctx, r); err != nil {
		t.Fatalf("PutPSKRecord (update): %v", err)
	}
	got, err := s.GetPSKRecord(ctx, "x")
	if err != nil {
		t.Fatalf("GetPSKRecord: %v", err)
	}
	if !got.IsRevoked() {
		t.Fatalf("expected IsRevoked true; revoked_at=%v", got.RevokedAt)
	}
}

func TestPairingComposite(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	p := store.Pairing{
		UserID:         "alice",
		MobileDeviceID: "M00000000000000A",
		HostDeviceID:   "H00000000000000A",
		CreatedAt:      now,
		LastUsedAt:     now,
	}
	if err := s.PutPairing(ctx, p); err != nil {
		t.Fatalf("PutPairing: %v", err)
	}
	got, err := s.GetPairing(ctx, "alice", "M00000000000000A", "H00000000000000A")
	if err != nil {
		t.Fatalf("GetPairing: %v", err)
	}
	if !got.CreatedAt.Equal(now) {
		t.Fatalf("created_at mismatch: %v vs %v", got.CreatedAt, now)
	}
	if err := s.DeletePairing(ctx, "alice", "M00000000000000A", "H00000000000000A"); err != nil {
		t.Fatalf("DeletePairing: %v", err)
	}
	if _, err := s.GetPairing(ctx, "alice", "M00000000000000A", "H00000000000000A"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNotFoundAcrossEntities(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t)
	if _, err := s.GetUser(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("user")
	}
	if _, err := s.GetPSKRecord(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("psk")
	}
	if _, err := s.GetDevice(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("device")
	}
	if _, err := s.GetSession(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("session")
	}
	if _, err := s.GetPairing(ctx, "u", "m", "h"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("pairing")
	}
}
