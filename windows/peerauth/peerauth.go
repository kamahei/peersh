// Package peerauth tracks which peer device_ids are authorized to dial
// peershd over QUIC right now.
//
// It exists to cross-check what the signaling channel says ("peer X
// asked to connect, here are their candidates") against what the QUIC
// mTLS handshake proves ("peer X presented an ed25519 cert that hashes
// to the same device_id"). On its own, peertls only verifies that the
// peer's cert key is well-formed and pubkey-bound; it deliberately does
// not encode any room/membership policy, because the signaling channel
// is the source of truth for that. This package is the bridge.
//
// Usage:
//
//	authz := peerauth.New(60 * time.Second)
//	go authz.RunSweeper(ctx, 10*time.Second)
//
//	// In the signaling Connect-handler goroutine:
//	authz.Allow(connect.GetFromDeviceId())
//
//	// In the QUIC accept loop, after the handshake completes:
//	if !authz.Check(peertls.PeerDeviceID(conn.TLSState())) {
//	    conn.CloseWithError(...) // unauthorized peer
//	}
//
// The TTL exists so a stale Connect grant (peer never followed through
// with a QUIC dial) does not leave authorization permanently open. The
// reaper goroutine just keeps the map small; correctness only requires
// Check to compare against the recorded expiry.
package peerauth

import (
	"context"
	"sync"
	"time"
)

// Authz is the in-memory set of authorized peer device_ids with TTL.
// All methods are safe for concurrent use.
type Authz struct {
	ttl time.Duration

	mu      sync.Mutex
	pending map[string]time.Time

	now func() time.Time
}

// New returns an Authz that grants each device_id `ttl` of validity from
// the moment Allow is called.
func New(ttl time.Duration) *Authz {
	return &Authz{
		ttl:     ttl,
		pending: map[string]time.Time{},
		now:     time.Now,
	}
}

// Allow records that deviceID is permitted to QUIC-dial within ttl.
// Re-calling Allow with the same id refreshes the deadline.
func (a *Authz) Allow(deviceID string) {
	if deviceID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending[deviceID] = a.now().Add(a.ttl)
}

// Check returns true if deviceID has an unexpired Allow entry. It also
// removes the entry on a hit so a single grant authorizes one connection
// (subsequent reconnects require a fresh signaling Connect).
func (a *Authz) Check(deviceID string) bool {
	if deviceID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.pending[deviceID]
	if !ok {
		return false
	}
	if a.now().After(exp) {
		delete(a.pending, deviceID)
		return false
	}
	delete(a.pending, deviceID)
	return true
}

// Sweep removes expired entries. Call periodically from RunSweeper.
func (a *Authz) Sweep() {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for id, exp := range a.pending {
		if now.After(exp) {
			delete(a.pending, id)
		}
	}
}

// RunSweeper drives Sweep on the given interval until ctx is done.
func (a *Authz) RunSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Sweep()
		}
	}
}
