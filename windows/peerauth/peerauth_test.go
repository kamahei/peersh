package peerauth

import (
	"testing"
	"time"
)

// fakeClock returns a controllable time source for the TTL tests.
type fakeClock struct{ t time.Time }

func (f *fakeClock) Now() time.Time          { return f.t }
func (f *fakeClock) Advance(d time.Duration) { f.t = f.t.Add(d) }

func newFakeAuthz(ttl time.Duration) (*Authz, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	a := New(ttl)
	a.now = clock.Now
	return a, clock
}

func TestAllowThenCheckSucceedsOnce(t *testing.T) {
	a, _ := newFakeAuthz(60 * time.Second)
	a.Allow("ABCDEFGHIJKLMNOP")
	if !a.Check("ABCDEFGHIJKLMNOP") {
		t.Fatal("first Check should succeed")
	}
	if a.Check("ABCDEFGHIJKLMNOP") {
		t.Fatal("second Check should fail — Allow grants one connection")
	}
}

func TestCheckFailsForUnknownID(t *testing.T) {
	a, _ := newFakeAuthz(60 * time.Second)
	if a.Check("UNKNOWN0000UNKNOWN") {
		t.Fatal("Check on unknown id should fail")
	}
}

func TestCheckFailsAfterTTLExpires(t *testing.T) {
	a, clock := newFakeAuthz(60 * time.Second)
	a.Allow("ABCDEFGHIJKLMNOP")
	clock.Advance(61 * time.Second)
	if a.Check("ABCDEFGHIJKLMNOP") {
		t.Fatal("Check should fail past TTL")
	}
}

func TestAllowRefreshesDeadline(t *testing.T) {
	a, clock := newFakeAuthz(60 * time.Second)
	a.Allow("ABCDEFGHIJKLMNOP")
	clock.Advance(50 * time.Second)
	a.Allow("ABCDEFGHIJKLMNOP") // refresh
	clock.Advance(50 * time.Second)
	// Without the refresh the entry would have expired; with it, the
	// new deadline is at +60s from the second Allow, i.e. still valid.
	if !a.Check("ABCDEFGHIJKLMNOP") {
		t.Fatal("refreshed Allow should still be valid 50s in")
	}
}

func TestSweepRemovesExpiredEntries(t *testing.T) {
	a, clock := newFakeAuthz(30 * time.Second)
	a.Allow("AAAAAAAAAAAAAAAA")
	a.Allow("BBBBBBBBBBBBBBBB")
	clock.Advance(31 * time.Second)
	a.Sweep()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) != 0 {
		t.Fatalf("Sweep should have emptied map, got %d entries", len(a.pending))
	}
}

func TestEmptyDeviceIDIsRejected(t *testing.T) {
	a, _ := newFakeAuthz(60 * time.Second)
	a.Allow("")
	if a.Check("") {
		t.Fatal("empty device_id must never be allowed")
	}
}
