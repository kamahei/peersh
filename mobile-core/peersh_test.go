package peersh

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
)

func TestIsRetryableConnectError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"target_unknown bare", errors.New("server error: target_unknown: no such device"), true},
		{"target_unknown wrapped", wrapErr(errors.New("server error: target_unknown: x")), true},
		{"unrelated", errors.New("connection refused"), false},
		{"empty", errors.New(""), false},
		{"contains keyword in benign text", errors.New("policy says target_unknown is rate-limited"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetryableConnectError(tc.err)
			if got != tc.want {
				t.Errorf("isRetryableConnectError(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }

func wrapErr(err error) error { return &wrappedErr{inner: err} }

// fakeNegotiator is a swap-in for negotiateConnect that records calls
// and returns canned replies / errors.
type fakeNegotiator struct {
	calls   atomic.Int32
	replies []negotiateOutcome
}

type negotiateOutcome struct {
	reply *signalv1.Connect
	err   error
}

func (f *fakeNegotiator) negotiate(
	ctx context.Context,
	_ string, _ string, _ []byte,
	_ string, _ string,
	_ string, _ ed25519.PublicKey,
	_ string,
	_ []*signalv1.EndpointCandidate,
) (*signalv1.Connect, error) {
	idx := int(f.calls.Add(1)) - 1
	if idx >= len(f.replies) {
		return nil, errors.New("fake: no more outcomes scheduled")
	}
	out := f.replies[idx]
	return out.reply, out.err
}

func withFakeNegotiator(t *testing.T, fake *fakeNegotiator) {
	t.Helper()
	orig := negotiateConnectFn
	negotiateConnectFn = fake.negotiate
	t.Cleanup(func() { negotiateConnectFn = orig })
}

func TestNegotiateConnectWithRetry_SucceedsImmediately(t *testing.T) {
	want := &signalv1.Connect{FromDeviceId: "host1"}
	fake := &fakeNegotiator{replies: []negotiateOutcome{{reply: want}}}
	withFakeNegotiator(t, fake)

	got, err := negotiateConnectWithRetry(context.Background(), "", "", nil, "", "", "dev", nil, "host1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %v; want %v", got, want)
	}
	if calls := fake.calls.Load(); calls != 1 {
		t.Errorf("fake called %d times; want 1", calls)
	}
}

func TestNegotiateConnectWithRetry_RetriesUntilSuccess(t *testing.T) {
	want := &signalv1.Connect{FromDeviceId: "host1"}
	fake := &fakeNegotiator{replies: []negotiateOutcome{
		{err: errors.New("server error: target_unknown: not registered yet")},
		{err: errors.New("server error: target_unknown: not registered yet")},
		{reply: want},
	}}
	withFakeNegotiator(t, fake)

	start := time.Now()
	got, err := negotiateConnectWithRetry(context.Background(), "", "", nil, "", "", "dev", nil, "host1", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %v; want %v", got, want)
	}
	if calls := fake.calls.Load(); calls != 3 {
		t.Errorf("fake called %d times; want 3", calls)
	}
	// Backoff after attempts 1 and 2 totals 200ms + 400ms = 600ms.
	if elapsed < 500*time.Millisecond {
		t.Errorf("retry returned in %v; backoff appears skipped", elapsed)
	}
}

func TestNegotiateConnectWithRetry_DoesNotRetryNonRetryable(t *testing.T) {
	fake := &fakeNegotiator{replies: []negotiateOutcome{
		{err: errors.New("connection refused")},
	}}
	withFakeNegotiator(t, fake)

	_, err := negotiateConnectWithRetry(context.Background(), "", "", nil, "", "", "dev", nil, "host1", nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected propagated error; got %v", err)
	}
	if calls := fake.calls.Load(); calls != 1 {
		t.Errorf("fake called %d times; want 1", calls)
	}
}

func TestNegotiateConnectWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	outcomes := make([]negotiateOutcome, 5)
	for i := range outcomes {
		outcomes[i] = negotiateOutcome{err: errors.New("server error: target_unknown: still cold")}
	}
	fake := &fakeNegotiator{replies: outcomes}
	withFakeNegotiator(t, fake)

	_, err := negotiateConnectWithRetry(context.Background(), "", "", nil, "", "", "dev", nil, "host1", nil)
	if err == nil {
		t.Fatalf("expected error after exhausting retries")
	}
	if calls := fake.calls.Load(); calls != 5 {
		t.Errorf("fake called %d times; want 5 (initial + 4 retries)", calls)
	}
}

func TestNegotiateConnectWithRetry_RespectsContextCancellation(t *testing.T) {
	outcomes := make([]negotiateOutcome, 5)
	for i := range outcomes {
		outcomes[i] = negotiateOutcome{err: errors.New("server error: target_unknown: still cold")}
	}
	fake := &fakeNegotiator{replies: outcomes}
	withFakeNegotiator(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := negotiateConnectWithRetry(ctx, "", "", nil, "", "", "dev", nil, "host1", nil)
	if err == nil {
		t.Fatalf("expected context-related error")
	}
}
