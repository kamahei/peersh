package psk_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/peersh/peersh/core/auth/psk"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/core/store/memory"
)

func newProviderWithUser(t *testing.T, userID string, secret []byte) *psk.Provider {
	t.Helper()
	s := memory.New()
	if err := s.PutPSKRecord(context.Background(), store.PSKRecord{
		UserID: userID, Secret: secret, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}
	return psk.New(s)
}

func freshSecret(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

func goodRegister(userID string) *signalv1.Register {
	return &signalv1.Register{
		UserId:      userID,
		DeviceId:    "DEVICE000000000A",
		PublicKey:   []byte{0x01, 0x02},
		Kind:        signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST,
		DisplayName: "test",
	}
}

func TestSignAndVerifyHappyPath(t *testing.T) {
	secret := freshSecret(t)
	p := newProviderWithUser(t, "alice", secret)
	reg := goodRegister("alice")

	if err := psk.SignRegister(secret, reg); err != nil {
		t.Fatalf("SignRegister: %v", err)
	}
	creds, err := psk.CredentialsFromRegister(reg)
	if err != nil {
		t.Fatalf("CredentialsFromRegister: %v", err)
	}
	id, err := p.Authenticate(context.Background(), creds)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.UserID != "alice" {
		t.Fatalf("expected user alice, got %q", id.UserID)
	}
}

func TestSignatureRejectedAfterMutation(t *testing.T) {
	secret := freshSecret(t)
	p := newProviderWithUser(t, "alice", secret)
	reg := goodRegister("alice")
	if err := psk.SignRegister(secret, reg); err != nil {
		t.Fatalf("SignRegister: %v", err)
	}
	// Tamper after signing.
	reg.DeviceId = "DEVICE000000000B"

	creds, err := psk.CredentialsFromRegister(reg)
	if err != nil {
		t.Fatalf("CredentialsFromRegister: %v", err)
	}
	_, err = p.Authenticate(context.Background(), creds)
	if !errors.Is(err, psk.ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid, got %v", err)
	}
}

func TestNonceReuseRejected(t *testing.T) {
	secret := freshSecret(t)
	p := newProviderWithUser(t, "alice", secret)
	reg := goodRegister("alice")
	if err := psk.SignRegister(secret, reg); err != nil {
		t.Fatalf("SignRegister: %v", err)
	}
	creds, err := psk.CredentialsFromRegister(reg)
	if err != nil {
		t.Fatalf("CredentialsFromRegister: %v", err)
	}
	if _, err := p.Authenticate(context.Background(), creds); err != nil {
		t.Fatalf("first Authenticate: %v", err)
	}
	if _, err := p.Authenticate(context.Background(), creds); !errors.Is(err, psk.ErrNonceReuse) {
		t.Fatalf("expected ErrNonceReuse, got %v", err)
	}
}

func TestTimestampSkewRejected(t *testing.T) {
	secret := freshSecret(t)
	p := newProviderWithUser(t, "alice", secret)
	p.ClockSkew = 60 * time.Second
	// Pin server "now" to a fixed point.
	p.Now = func() time.Time { return time.Unix(1_000_000_000, 0) }

	reg := goodRegister("alice")
	if err := psk.SignRegister(secret, reg); err != nil {
		t.Fatalf("SignRegister: %v", err)
	}
	// SignRegister stamped real now(). Compared against pinned "now", that's
	// way outside the window.
	creds, err := psk.CredentialsFromRegister(reg)
	if err != nil {
		t.Fatalf("CredentialsFromRegister: %v", err)
	}
	_, err = p.Authenticate(context.Background(), creds)
	if !errors.Is(err, psk.ErrTimestampSkew) {
		t.Fatalf("expected ErrTimestampSkew, got %v", err)
	}
}

func TestUnknownUser(t *testing.T) {
	secret := freshSecret(t)
	p := newProviderWithUser(t, "alice", secret)
	reg := goodRegister("bob") // server has no record for bob
	// Sign with same secret bytes (incidentally) but the lookup will miss.
	if err := psk.SignRegister(secret, reg); err != nil {
		t.Fatalf("SignRegister: %v", err)
	}
	creds, err := psk.CredentialsFromRegister(reg)
	if err != nil {
		t.Fatalf("CredentialsFromRegister: %v", err)
	}
	_, err = p.Authenticate(context.Background(), creds)
	if !errors.Is(err, psk.ErrUnknownUser) {
		t.Fatalf("expected ErrUnknownUser, got %v", err)
	}
}

func TestRevokedRejected(t *testing.T) {
	ctx := context.Background()
	secret := freshSecret(t)
	p := newProviderWithUser(t, "alice", secret)
	rec, err := p.Store.GetPSKRecord(ctx, "alice")
	if err != nil {
		t.Fatalf("GetPSKRecord: %v", err)
	}
	rec.RevokedAt = time.Now()
	if err := p.Store.PutPSKRecord(ctx, rec); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}

	reg := goodRegister("alice")
	if err := psk.SignRegister(secret, reg); err != nil {
		t.Fatalf("SignRegister: %v", err)
	}
	creds, err := psk.CredentialsFromRegister(reg)
	if err != nil {
		t.Fatalf("CredentialsFromRegister: %v", err)
	}
	_, err = p.Authenticate(ctx, creds)
	if !errors.Is(err, psk.ErrRevoked) {
		t.Fatalf("expected ErrRevoked, got %v", err)
	}
}

type fakeCreds struct{}

func (fakeCreds) Kind() string { return "fake" }

func TestWrongCredentialsKindRejected(t *testing.T) {
	p := psk.New(memory.New())
	_, err := p.Authenticate(context.Background(), fakeCreds{})
	if err == nil {
		t.Fatal("expected error for wrong credentials kind")
	}
	// auth.WrongKindError is the documented carrier for this case.
	var wke interface{ Error() string }
	if !errors.As(err, &wke) {
		t.Fatalf("expected error type, got %T: %v", err, err)
	}
}

func TestNilCredentialsRejected(t *testing.T) {
	p := psk.New(memory.New())
	_, err := p.Authenticate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil credentials")
	}
}

func TestNonceWrongLength(t *testing.T) {
	secret := freshSecret(t)
	p := newProviderWithUser(t, "alice", secret)
	creds := psk.Credentials{
		UserID:       "alice",
		SignedAtUnix: time.Now().Unix(),
		Nonce:        []byte{1, 2, 3}, // too short
		Signature:    make([]byte, 32),
		SignedBody:   []byte("anything"),
	}
	_, err := p.Authenticate(context.Background(), creds)
	if !errors.Is(err, psk.ErrNonceShape) {
		t.Fatalf("expected ErrNonceShape, got %v", err)
	}
}
