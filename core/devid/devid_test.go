package devid_test

import (
	"errors"
	"testing"

	"github.com/peersh/peersh/core/devid"
)

func TestDeriveDeterministic(t *testing.T) {
	pub := []byte("not-a-real-key-but-bytes")
	a := devid.Derive(pub)
	b := devid.Derive(pub)
	if a != b {
		t.Fatalf("derive not deterministic: %q vs %q", a, b)
	}
	if len(a) != devid.Length {
		t.Fatalf("derive returned length %d, want %d", len(a), devid.Length)
	}
}

func TestDeriveDifferentForDifferentKeys(t *testing.T) {
	a := devid.Derive([]byte("key-one"))
	b := devid.Derive([]byte("key-two"))
	if a == b {
		t.Fatalf("two distinct keys produced same id %q", a)
	}
}

func TestVerifyMatching(t *testing.T) {
	pub := []byte("verify-me")
	id := devid.Derive(pub)
	if err := devid.Verify(id, pub); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyMismatch(t *testing.T) {
	pub := []byte("verify-me")
	otherID := devid.Derive([]byte("other-key"))
	err := devid.Verify(otherID, pub)
	if !errors.Is(err, devid.ErrMismatch) {
		t.Fatalf("expected ErrMismatch, got %v", err)
	}
}

func TestVerifyWrongShape(t *testing.T) {
	if err := devid.Verify("TOOSHORT", []byte("k")); err == nil {
		t.Fatal("expected length error")
	}
}

func TestDeriveEmptyKey(t *testing.T) {
	if got := devid.Derive(nil); got != "" {
		t.Fatalf("expected empty id for empty key, got %q", got)
	}
}
