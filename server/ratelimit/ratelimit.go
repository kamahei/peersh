// Package ratelimit provides token-bucket rate limiting for the signaling
// server. One Bucket instance is keyed by an arbitrary string (e.g. an IP
// address); each key has an independent bucket with a shared rate / capacity.
//
// The implementation is in-process and intentionally small — no external
// dependency. Buckets are pruned lazily on Allow; idle keys age out.
package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a multi-key token-bucket limiter.
type Bucket struct {
	rate     float64       // tokens per second
	capacity float64       // burst size
	idleTTL  time.Duration // entries idle longer than this are pruned

	mu      sync.Mutex
	entries map[string]*entry
	now     func() time.Time
}

type entry struct {
	tokens float64
	last   time.Time
}

// New returns a Bucket that admits `rate` tokens per second per key with a
// burst capacity of `capacity` tokens. A typical config: rate=10/60 (10
// per minute) and capacity=3 (allow short bursts up to 3 in a row).
func New(rate, capacity float64) *Bucket {
	return &Bucket{
		rate:     rate,
		capacity: capacity,
		idleTTL:  10 * time.Minute,
		entries:  make(map[string]*entry),
		now:      time.Now,
	}
}

// Allow consumes one token for key. Returns true if the request is permitted.
func (b *Bucket) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	e, ok := b.entries[key]
	if !ok {
		e = &entry{tokens: b.capacity, last: now}
		b.entries[key] = e
	} else {
		// Refill since last check.
		dt := now.Sub(e.last).Seconds()
		e.tokens += dt * b.rate
		if e.tokens > b.capacity {
			e.tokens = b.capacity
		}
		e.last = now
	}

	// Lazy prune.
	for k, ent := range b.entries {
		if now.Sub(ent.last) > b.idleTTL {
			delete(b.entries, k)
		}
	}

	if e.tokens < 1.0 {
		return false
	}
	e.tokens -= 1.0
	return true
}

// Reset clears all buckets. Useful in tests.
func (b *Bucket) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = make(map[string]*entry)
}
