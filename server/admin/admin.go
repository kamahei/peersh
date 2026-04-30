// Package admin contains operator-facing PSK management logic. The
// peersh-signaling psk subcommands are thin wrappers around this package;
// integration tests can also use it directly.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/peersh/peersh/core/store"
)

// SecretLength is the number of bytes in a freshly-generated PSK. 32 bytes
// (256 bits) is well above what HMAC-SHA256 needs.
const SecretLength = 32

// ErrUserExists is returned by AddPSK when a record for the same user_id
// already exists. Callers can choose to revoke + replace or refuse.
var ErrUserExists = errors.New("admin: psk for that user already exists")

// AddResult is returned by AddPSK so callers can re-display the secret once.
type AddResult struct {
	UserID    string
	Secret    []byte
	SecretHex string
	CreatedAt time.Time
	Label     string
}

// AddPSK generates a fresh secret and stores a new PSKRecord. It refuses
// to overwrite an existing record for the same user_id.
func AddPSK(ctx context.Context, s store.Store, userID, label string) (AddResult, error) {
	if userID == "" {
		return AddResult{}, errors.New("admin: user must not be empty")
	}
	if _, err := s.GetPSKRecord(ctx, userID); err == nil {
		return AddResult{}, ErrUserExists
	} else if !errors.Is(err, store.ErrNotFound) {
		return AddResult{}, fmt.Errorf("admin: lookup user: %w", err)
	}

	secret := make([]byte, SecretLength)
	if _, err := rand.Read(secret); err != nil {
		return AddResult{}, fmt.Errorf("admin: generate secret: %w", err)
	}
	now := time.Now().UTC()
	rec := store.PSKRecord{
		UserID:       userID,
		Secret:       secret,
		DisplayLabel: label,
		CreatedAt:    now,
	}
	if err := s.PutPSKRecord(ctx, rec); err != nil {
		return AddResult{}, fmt.Errorf("admin: PutPSKRecord: %w", err)
	}
	if _, err := s.GetUser(ctx, userID); errors.Is(err, store.ErrNotFound) {
		_ = s.PutUser(ctx, store.User{
			ID: userID, AuthProvider: store.AuthProviderPSK, CreatedAt: now,
		})
	}

	return AddResult{
		UserID:    userID,
		Secret:    secret,
		SecretHex: hex.EncodeToString(secret),
		CreatedAt: now,
		Label:     label,
	}, nil
}

// ListPSKs returns all PSK records (revoked included).
func ListPSKs(ctx context.Context, s store.Store) ([]store.PSKRecord, error) {
	return s.ListPSKRecords(ctx)
}

// RevokePSK marks a PSK as revoked. The secret is left in place so existing
// signed requests with old timestamps don't suddenly start failing on a
// "user not found" error during the skew window. Once revoked, the auth
// provider returns ErrRevoked.
func RevokePSK(ctx context.Context, s store.Store, userID string) error {
	rec, err := s.GetPSKRecord(ctx, userID)
	if err != nil {
		return fmt.Errorf("admin: lookup: %w", err)
	}
	if rec.IsRevoked() {
		return nil
	}
	rec.RevokedAt = time.Now().UTC()
	if err := s.PutPSKRecord(ctx, rec); err != nil {
		return fmt.Errorf("admin: PutPSKRecord: %w", err)
	}
	return nil
}

// DeletePSK removes the record entirely. Different from revoke: re-using the
// same user_id later would create a fresh record, while revoke leaves the
// row in place for audit purposes.
func DeletePSK(ctx context.Context, s store.Store, userID string) error {
	return s.DeletePSKRecord(ctx, userID)
}
