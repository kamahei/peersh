package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/core/store/memory"
)

func TestImplementsStoreInterface(t *testing.T) {
	var _ store.Store = memory.New()
}

func TestDeviceCRUD(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	d := store.Device{
		ID:          "DEVICE000000000A",
		PublicKey:   []byte{0x01, 0x02},
		OwnerUserID: "alice",
		Kind:        store.DeviceKindWindowsHost,
		DisplayName: "test-host",
		CreatedAt:   time.Now(),
		LastSeenAt:  time.Now(),
	}

	if err := s.PutDevice(ctx, d); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	got, err := s.GetDevice(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.ID != d.ID || got.OwnerUserID != "alice" {
		t.Fatalf("unexpected device: %+v", got)
	}

	list, err := s.ListDevicesByOwner(ctx, "alice")
	if err != nil {
		t.Fatalf("ListDevicesByOwner: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 device, got %d", len(list))
	}

	if err := s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if _, err := s.GetDevice(ctx, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.DeleteDevice(ctx, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on double-delete, got %v", err)
	}
}

func TestSessionCRUD(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	sess := store.Session{
		ID:             "session-1",
		UserID:         "alice",
		MobileDeviceID: "M00000000000000A",
		HostDeviceID:   "H00000000000000A",
		State:          store.SessionStateConnected,
		CreatedAt:      time.Now(),
		LastActiveAt:   time.Now(),
	}

	if err := s.PutSession(ctx, sess); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.State != store.SessionStateConnected {
		t.Fatalf("unexpected state: %v", got.State)
	}

	if err := s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.GetSession(ctx, sess.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUserCRUD(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	u := store.User{ID: "alice", AuthProvider: store.AuthProviderPSK, CreatedAt: time.Now()}
	if err := s.PutUser(ctx, u); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	got, err := s.GetUser(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.AuthProvider != store.AuthProviderPSK {
		t.Fatalf("unexpected provider: %v", got.AuthProvider)
	}
	if _, err := s.GetUser(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPSKRecordCRUD(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	r := store.PSKRecord{
		UserID:       "alice",
		Secret:       []byte("a-32-byte-or-longer-random-secret"),
		DisplayLabel: "alice-laptop",
		CreatedAt:    time.Now(),
	}
	if err := s.PutPSKRecord(ctx, r); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}
	got, err := s.GetPSKRecord(ctx, "alice")
	if err != nil {
		t.Fatalf("GetPSKRecord: %v", err)
	}
	if string(got.Secret) != string(r.Secret) {
		t.Fatalf("secret mismatch")
	}

	all, err := s.ListPSKRecords(ctx)
	if err != nil {
		t.Fatalf("ListPSKRecords: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 PSK, got %d", len(all))
	}

	if err := s.DeletePSKRecord(ctx, "alice"); err != nil {
		t.Fatalf("DeletePSKRecord: %v", err)
	}
	if _, err := s.GetPSKRecord(ctx, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPairingCRUD(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	p := store.Pairing{
		UserID:         "alice",
		MobileDeviceID: "M00000000000000A",
		HostDeviceID:   "H00000000000000A",
		CreatedAt:      time.Now(),
		LastUsedAt:     time.Now(),
	}
	if err := s.PutPairing(ctx, p); err != nil {
		t.Fatalf("PutPairing: %v", err)
	}
	got, err := s.GetPairing(ctx, "alice", "M00000000000000A", "H00000000000000A")
	if err != nil {
		t.Fatalf("GetPairing: %v", err)
	}
	if got.UserID != "alice" {
		t.Fatalf("unexpected pairing: %+v", got)
	}
	list, err := s.ListPairingsByUser(ctx, "alice")
	if err != nil {
		t.Fatalf("ListPairingsByUser: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 pairing, got %d", len(list))
	}
	if err := s.DeletePairing(ctx, "alice", "M00000000000000A", "H00000000000000A"); err != nil {
		t.Fatalf("DeletePairing: %v", err)
	}
}

func TestConcurrentDeviceAccess(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	const N = 200
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("D%015d", i)
			d := store.Device{ID: id, OwnerUserID: "owner", Kind: store.DeviceKindMobileClient}
			if err := s.PutDevice(ctx, d); err != nil {
				t.Errorf("PutDevice: %v", err)
				return
			}
			got, err := s.GetDevice(ctx, id)
			if err != nil {
				t.Errorf("GetDevice: %v", err)
				return
			}
			if got.ID != id {
				t.Errorf("got %q want %q", got.ID, id)
			}
		}(i)
	}
	wg.Wait()

	list, err := s.ListDevicesByOwner(ctx, "owner")
	if err != nil {
		t.Fatalf("ListDevicesByOwner: %v", err)
	}
	if len(list) != N {
		t.Fatalf("expected %d devices, got %d", N, len(list))
	}
}

func TestConcurrentPSKAccess(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			uid := fmt.Sprintf("u%03d", i)
			r := store.PSKRecord{UserID: uid, Secret: []byte(uid + "-secret")}
			if err := s.PutPSKRecord(ctx, r); err != nil {
				t.Errorf("PutPSKRecord: %v", err)
				return
			}
			if got, err := s.GetPSKRecord(ctx, uid); err != nil || string(got.Secret) != string(r.Secret) {
				t.Errorf("readback mismatch for %s: %v / %s", uid, err, got.Secret)
			}
		}(i)
	}
	wg.Wait()
	all, err := s.ListPSKRecords(ctx)
	if err != nil {
		t.Fatalf("ListPSKRecords: %v", err)
	}
	if len(all) != N {
		t.Fatalf("expected %d PSKs, got %d", N, len(all))
	}
}
