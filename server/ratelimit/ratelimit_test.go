package ratelimit_test

import (
	"testing"
	"time"

	"github.com/peersh/peersh/server/ratelimit"
)

func TestBurstThenThrottle(t *testing.T) {
	b := ratelimit.New(0, 3) // 0 refill rate, capacity 3
	for i := 0; i < 3; i++ {
		if !b.Allow("ip-1") {
			t.Fatalf("expected allow %d, denied", i)
		}
	}
	if b.Allow("ip-1") {
		t.Fatal("expected denial after burst exhausted")
	}
}

func TestRefillOverTime(t *testing.T) {
	b := ratelimit.New(10, 1) // 10 tokens/sec, burst 1
	if !b.Allow("k") {
		t.Fatal("first allow expected")
	}
	if b.Allow("k") {
		t.Fatal("second allow should be throttled (no time elapsed)")
	}
	time.Sleep(150 * time.Millisecond) // ~1.5 tokens accumulated
	if !b.Allow("k") {
		t.Fatal("expected refill to allow")
	}
}

func TestPerKeyIndependent(t *testing.T) {
	b := ratelimit.New(0, 2)
	if !b.Allow("a") || !b.Allow("a") {
		t.Fatal("a burst")
	}
	if b.Allow("a") {
		t.Fatal("a exhausted")
	}
	if !b.Allow("b") || !b.Allow("b") {
		t.Fatal("b should have its own bucket")
	}
}
