// Package psk implements the HMAC-SHA256 pre-shared-key auth.Provider for
// peersh signaling. The server stores raw secret bytes (HMAC verification
// requires it); operators are advised to host the SQLite database on a
// disk-encrypted volume.
package psk

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/peersh/peersh/core/auth"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/store"
	"google.golang.org/protobuf/proto"
)

// Kind is the provider tag for PSK.
const Kind = "psk"

// NonceLength is the required length of the per-request nonce, in bytes.
const NonceLength = 16

// Errors surfaced by the provider. They are public so callers can branch on
// them via errors.Is.
var (
	ErrSignatureInvalid = errors.New("psk: signature invalid")
	ErrNonceReuse       = errors.New("psk: nonce reused within window")
	ErrTimestampSkew    = errors.New("psk: signed_at outside acceptable skew")
	ErrRevoked          = errors.New("psk: psk revoked")
	ErrUnknownUser      = errors.New("psk: unknown user")
	ErrNonceShape       = errors.New("psk: nonce wrong length")
)

// Credentials is the wire-derived material that authenticates one request.
//
// SignedBody is the bytes the signature commits to. For Register it is the
// canonical marshal of the Register message with HmacSignature cleared (see
// CanonicalRegisterBody). Holding the bytes here keeps the Provider API
// independent of the protocol details.
type Credentials struct {
	UserID       string
	SignedAtUnix int64
	Nonce        []byte
	Signature    []byte
	SignedBody   []byte
}

// Kind reports the provider tag.
func (Credentials) Kind() string { return Kind }

// NonceCache rejects (userID, nonce) reuse within a sliding window.
type NonceCache interface {
	// Add records (userID, nonce) at time now. Returns false if the pair was
	// already present within the window.
	Add(userID string, nonce []byte, now time.Time) bool
}

// MemoryNonceCache is a process-local NonceCache. Safe for concurrent use.
type MemoryNonceCache struct {
	Window time.Duration

	mu   sync.Mutex
	seen map[string]time.Time
}

// NewMemoryNonceCache returns a cache that rejects pairs reused within
// window.
func NewMemoryNonceCache(window time.Duration) *MemoryNonceCache {
	return &MemoryNonceCache{Window: window, seen: make(map[string]time.Time)}
}

// Add implements NonceCache.
func (c *MemoryNonceCache) Add(userID string, nonce []byte, now time.Time) bool {
	key := userID + ":" + hex.EncodeToString(nonce)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Lazy cleanup: drop entries older than the window before checking.
	for k, t := range c.seen {
		if now.Sub(t) > c.Window {
			delete(c.seen, k)
		}
	}
	if _, ok := c.seen[key]; ok {
		return false
	}
	c.seen[key] = now
	return true
}

// Provider implements auth.Provider for PSK.
type Provider struct {
	Store     store.Store
	Nonces    NonceCache
	ClockSkew time.Duration // ±window for signed_at_unix; default 60s
	Now       func() time.Time
}

// New returns a Provider configured with sane defaults. The caller still
// owns lifecycle of Store.
func New(s store.Store) *Provider {
	return &Provider{
		Store:     s,
		Nonces:    NewMemoryNonceCache(5 * time.Minute),
		ClockSkew: 60 * time.Second,
		Now:       time.Now,
	}
}

// Authenticate verifies creds and returns the bound Identity on success.
func (p *Provider) Authenticate(ctx context.Context, creds auth.Credentials) (auth.Identity, error) {
	if creds == nil {
		return auth.Identity{}, &auth.WrongKindError{Got: "<nil>", Want: Kind}
	}
	pc, ok := creds.(Credentials)
	if !ok {
		return auth.Identity{}, &auth.WrongKindError{Got: creds.Kind(), Want: Kind}
	}
	if len(pc.Nonce) != NonceLength {
		return auth.Identity{}, ErrNonceShape
	}

	now := p.Now()
	delta := now.Unix() - pc.SignedAtUnix
	if delta < 0 {
		delta = -delta
	}
	if delta > int64(p.ClockSkew/time.Second) {
		return auth.Identity{}, ErrTimestampSkew
	}

	rec, err := p.Store.GetPSKRecord(ctx, pc.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return auth.Identity{}, ErrUnknownUser
		}
		return auth.Identity{}, fmt.Errorf("psk: store: %w", err)
	}
	if rec.IsRevoked() {
		return auth.Identity{}, ErrRevoked
	}

	if !p.Nonces.Add(pc.UserID, pc.Nonce, now) {
		return auth.Identity{}, ErrNonceReuse
	}

	expected := hmacSHA256(rec.Secret, pc.SignedBody)
	if !hmac.Equal(expected, pc.Signature) {
		return auth.Identity{}, ErrSignatureInvalid
	}
	return auth.Identity{UserID: pc.UserID}, nil
}

func hmacSHA256(key, body []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return mac.Sum(nil)
}

// CanonicalRegisterBody returns the bytes that the HMAC signature commits to
// for a Register message: deterministic marshal with HmacSignature cleared.
//
// reg is not mutated.
func CanonicalRegisterBody(reg *signalv1.Register) ([]byte, error) {
	clone := proto.Clone(reg).(*signalv1.Register)
	clone.HmacSignature = nil
	return proto.MarshalOptions{Deterministic: true}.Marshal(clone)
}

// SignRegister fills SignedAtUnix, Nonce, and HmacSignature on reg. The
// caller passes the user's PSK secret. reg is mutated in place.
func SignRegister(secret []byte, reg *signalv1.Register) error {
	nonce := make([]byte, NonceLength)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("psk: nonce: %w", err)
	}
	reg.SignedAtUnix = time.Now().Unix()
	reg.Nonce = nonce
	reg.HmacSignature = nil
	body, err := CanonicalRegisterBody(reg)
	if err != nil {
		return fmt.Errorf("psk: canonical body: %w", err)
	}
	reg.HmacSignature = hmacSHA256(secret, body)
	return nil
}

// CredentialsFromRegister reads the authenticator fields out of a signed
// Register and returns Credentials suitable for Provider.Authenticate. The
// returned SignedBody is the canonical body for that Register.
//
// reg is not mutated.
func CredentialsFromRegister(reg *signalv1.Register) (Credentials, error) {
	body, err := CanonicalRegisterBody(reg)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{
		UserID:       reg.GetUserId(),
		SignedAtUnix: reg.GetSignedAtUnix(),
		Nonce:        reg.GetNonce(),
		Signature:    reg.GetHmacSignature(),
		SignedBody:   body,
	}, nil
}
