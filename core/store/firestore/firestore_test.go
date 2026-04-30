package firestore_test

import (
	"context"
	"errors"
	"testing"

	fsstore "github.com/peersh/peersh/core/store/firestore"
	"github.com/peersh/peersh/core/store"
)

// Compile-time check that the Firestore store implements store.Store.
func TestImplementsStoreInterface(t *testing.T) {
	var _ store.Store = (*fsstore.Store)(nil)
}

// PSK methods always return ErrNotFound / unsupported in Firebase mode.
// These checks need no real Firestore client because they don't touch
// the wire — tests that do require a Firestore emulator and are
// documented in docs/firebase-setup.md.
func TestPSKMethodsBehavior(t *testing.T) {
	s := fsstore.Open(nil) // client never reached
	ctx := context.Background()
	if _, err := s.GetPSKRecord(ctx, "any"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.DeletePSKRecord(ctx, "any"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	list, err := s.ListPSKRecords(ctx)
	if err != nil {
		t.Fatalf("ListPSKRecords: %v", err)
	}
	if list != nil {
		t.Fatalf("expected nil list, got %v", list)
	}
}
