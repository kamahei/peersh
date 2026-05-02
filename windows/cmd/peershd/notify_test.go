package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	fbpeershd "github.com/peersh/peersh/windows/firebase"
)

type fakeNotifier struct {
	calls atomic.Int32
	last  fbpeershd.NotificationPayload
}

func (f *fakeNotifier) NotifyCommandReady(ctx context.Context, p fbpeershd.NotificationPayload) error {
	f.calls.Add(1)
	f.last = p
	return nil
}

func newTestState(t *testing.T, n *fakeNotifier) *ptyNotifyState {
	t.Helper()
	return newPTYNotifyState(&notifyCtx{
		notifier:     n,
		hostDeviceID: "HOST123",
		hostName:     "test-host",
	}, 7, slog.Default())
}

func waitForCalls(t *testing.T, n *fakeNotifier, want int32, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if n.calls.Load() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// OSC 9;9;<path>\x07
func oscSeq(path string) []byte {
	return append(append([]byte{0x1b, ']'}, []byte("9;9;"+path)...), 0x07)
}

func TestNotify_DisabledWhenNoConfig(t *testing.T) {
	n := &fakeNotifier{}
	s := newTestState(t, n)
	s.onInput()
	time.Sleep(20 * time.Millisecond)
	s.onOutput(context.Background(), oscSeq("C:\\"))
	time.Sleep(50 * time.Millisecond)
	if got := n.calls.Load(); got != 0 {
		t.Errorf("calls = %d; want 0 (no config)", got)
	}
}

func TestNotify_FiresOnPromptAfterThreshold(t *testing.T) {
	n := &fakeNotifier{}
	s := newTestState(t, n)
	s.setConfig(&v1.PTYNotificationConfig{
		Enabled:          true,
		ThresholdSeconds: 0, // use default — but we'll inject by manipulating lastInputAt
		MobileDeviceId:   "mob-1",
		TabLabel:         "Tab A",
	})
	s.onInput()
	// Force lastInputAt back so the elapsed time exceeds the
	// notifyDefaultThreshold (10 s).
	s.mu.Lock()
	s.lastInputAt = time.Now().Add(-15 * time.Second)
	s.mu.Unlock()
	s.onOutput(context.Background(), oscSeq("C:\\Users\\me"))
	waitForCalls(t, n, 1, 500*time.Millisecond)
	if got := n.calls.Load(); got != 1 {
		t.Fatalf("calls = %d; want 1", got)
	}
	if n.last.MobileDeviceID != "mob-1" {
		t.Errorf("MobileDeviceID = %q; want mob-1", n.last.MobileDeviceID)
	}
	if n.last.HostDeviceID != "HOST123" {
		t.Errorf("HostDeviceID = %q", n.last.HostDeviceID)
	}
}

func TestNotify_SuppressesQuickCommands(t *testing.T) {
	n := &fakeNotifier{}
	s := newTestState(t, n)
	s.setConfig(&v1.PTYNotificationConfig{
		Enabled:          true,
		ThresholdSeconds: 5,
		MobileDeviceId:   "mob-1",
	})
	s.onInput()
	// Immediate prompt — duration < 5s threshold
	s.onOutput(context.Background(), oscSeq("C:\\"))
	time.Sleep(50 * time.Millisecond)
	if got := n.calls.Load(); got != 0 {
		t.Errorf("calls = %d; want 0 (quick command should not notify)", got)
	}
}

func TestNotify_CooldownDebounces(t *testing.T) {
	n := &fakeNotifier{}
	s := newTestState(t, n)
	s.setConfig(&v1.PTYNotificationConfig{
		Enabled:          true,
		ThresholdSeconds: 1,
		MobileDeviceId:   "mob-1",
	})
	// First fire
	s.onInput()
	s.mu.Lock()
	s.lastInputAt = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()
	s.onOutput(context.Background(), oscSeq("C:\\"))
	waitForCalls(t, n, 1, 200*time.Millisecond)

	// Second prompt within cooldown — must not fire
	s.onInput()
	s.mu.Lock()
	s.lastInputAt = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()
	s.onOutput(context.Background(), oscSeq("C:\\Users"))
	time.Sleep(100 * time.Millisecond)
	if got := n.calls.Load(); got != 1 {
		t.Errorf("calls = %d; want 1 (cooldown should suppress 2nd fire)", got)
	}
}

func TestNotify_IdleHeuristicFires(t *testing.T) {
	n := &fakeNotifier{}
	s := newTestState(t, n)
	s.setConfig(&v1.PTYNotificationConfig{
		Enabled:        true,
		IdleSeconds:    1,
		MobileDeviceId: "mob-1",
	})
	s.onInput()
	// Force lastOutputAt back so silence window is satisfied
	s.mu.Lock()
	s.lastOutputAt = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()
	// Drive the idle path manually instead of waiting for the ticker
	s.maybeFire(context.Background(), "idle")
	waitForCalls(t, n, 1, 200*time.Millisecond)
	if got := n.calls.Load(); got != 1 {
		t.Errorf("calls = %d; want 1 (idle should fire)", got)
	}
}

func TestNotify_NilStateIsNoOp(t *testing.T) {
	var s *ptyNotifyState
	s.onInput()
	s.setConfig(&v1.PTYNotificationConfig{Enabled: true})
	s.onOutput(context.Background(), []byte("hi"))
	s.maybeFire(context.Background(), "prompt")
	// just assert no panic
}
