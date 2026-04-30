package admin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/core/store/memory"
	"github.com/peersh/peersh/server/admin"
)

func TestAddPSKCreatesRecordAndUser(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	res, err := admin.AddPSK(ctx, s, "alice", "alice-laptop")
	if err != nil {
		t.Fatalf("AddPSK: %v", err)
	}
	if len(res.Secret) != admin.SecretLength {
		t.Fatalf("secret length: %d", len(res.Secret))
	}
	if res.SecretHex == "" {
		t.Fatal("hex empty")
	}

	got, err := s.GetPSKRecord(ctx, "alice")
	if err != nil {
		t.Fatalf("GetPSKRecord: %v", err)
	}
	if string(got.Secret) != string(res.Secret) {
		t.Fatal("secret mismatch")
	}
	if got.DisplayLabel != "alice-laptop" {
		t.Fatalf("label: %q", got.DisplayLabel)
	}
	u, err := s.GetUser(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.AuthProvider != store.AuthProviderPSK {
		t.Fatalf("user provider: %v", u.AuthProvider)
	}
}

func TestAddPSKRefusesDuplicate(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if _, err := admin.AddPSK(ctx, s, "alice", ""); err != nil {
		t.Fatalf("first AddPSK: %v", err)
	}
	if _, err := admin.AddPSK(ctx, s, "alice", ""); !errors.Is(err, admin.ErrUserExists) {
		t.Fatalf("expected ErrUserExists, got %v", err)
	}
}

func TestRevoke(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if _, err := admin.AddPSK(ctx, s, "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := admin.RevokePSK(ctx, s, "alice"); err != nil {
		t.Fatalf("RevokePSK: %v", err)
	}
	got, err := s.GetPSKRecord(ctx, "alice")
	if err != nil {
		t.Fatalf("GetPSKRecord: %v", err)
	}
	if !got.IsRevoked() {
		t.Fatal("expected IsRevoked")
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	for _, u := range []string{"a", "b", "c"} {
		if _, err := admin.AddPSK(ctx, s, u, ""); err != nil {
			t.Fatal(err)
		}
	}
	all, err := admin.ListPSKs(ctx, s)
	if err != nil {
		t.Fatalf("ListPSKs: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3, got %d", len(all))
	}
}
