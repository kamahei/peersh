package firebase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeTokenSource is a TokenSource for tests.
type fakeTokenSource struct {
	uid   string
	token string
}

func (f *fakeTokenSource) Token(ctx context.Context) (string, error) { return f.token, nil }
func (f *fakeTokenSource) UID() string                               { return f.uid }

func TestWakeListener_CloseBeforeStart(t *testing.T) {
	wl := NewWakeListener(nil, &fakeTokenSource{uid: "u1"}, "u1", "d1")
	wl.Close()
	wl.Close()
}

func TestIsJSONNull(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"null", true},
		{"", true},
		{"{}", false},
		{"true", false},
		{`"foo"`, false},
	}
	for _, c := range cases {
		got := isJSONNull(json.RawMessage(c.raw))
		if got != c.want {
			t.Errorf("isJSONNull(%q) = %v; want %v", c.raw, got, c.want)
		}
	}
}

func TestDispatch_FullSubtree(t *testing.T) {
	wl := NewWakeListener(nil, &fakeTokenSource{uid: "u1"}, "u1", "host-A")
	defer close(wl.out) // we never start the run loop, so close to unblock dispatch
	go func() {
		// small drainer in case dispatch sends more than one event
	}()

	subtree := map[string]map[string]string{
		"r1": {"target_device_id": "host-A", "mobile_device_id": "m1"},
		"r2": {"target_device_id": "host-B", "mobile_device_id": "m2"},
		"r3": {"target_device_id": "host-A", "mobile_device_id": "m3"},
	}
	raw, _ := json.Marshal(subtree)
	ctx := context.Background()
	go wl.dispatch(ctx, SSEEvent{Kind: "put", Path: "/", Data: raw})

	got := map[string]string{}
	deadline := time.After(time.Second)
	for i := 0; i < 2; i++ {
		select {
		case ev := <-wl.out:
			got[ev.RequestID] = ev.MobileDeviceID
		case <-deadline:
			t.Fatalf("only collected %d events", len(got))
		}
	}
	if got["r1"] != "m1" {
		t.Errorf("missing r1: %v", got)
	}
	if got["r3"] != "m3" {
		t.Errorf("missing r3: %v", got)
	}
	if _, leaked := got["r2"]; leaked {
		t.Errorf("r2 (host-B) leaked through filter")
	}
}

func TestDispatch_SingleChildAddedOnly(t *testing.T) {
	wl := NewWakeListener(nil, &fakeTokenSource{uid: "u1"}, "u1", "host-A")
	defer close(wl.out)

	doc := map[string]string{
		"target_device_id": "host-A",
		"mobile_device_id": "m9",
	}
	raw, _ := json.Marshal(doc)
	ctx := context.Background()
	go wl.dispatch(ctx, SSEEvent{Kind: "put", Path: "/r9", Data: raw})

	select {
	case ev := <-wl.out:
		if ev.RequestID != "r9" || ev.MobileDeviceID != "m9" {
			t.Errorf("bad event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event surfaced")
	}
}

func TestDispatch_SkipsDeletion(t *testing.T) {
	wl := NewWakeListener(nil, &fakeTokenSource{uid: "u1"}, "u1", "host-A")
	defer close(wl.out)

	ctx := context.Background()
	go wl.dispatch(ctx, SSEEvent{Kind: "put", Path: "/r9", Data: json.RawMessage("null")})
	select {
	case ev := <-wl.out:
		t.Errorf("unexpected event for deletion: %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// expected
	}
}

func TestDispatch_SkipsWrongTarget(t *testing.T) {
	wl := NewWakeListener(nil, &fakeTokenSource{uid: "u1"}, "u1", "host-A")
	defer close(wl.out)

	doc := map[string]string{
		"target_device_id": "host-B",
		"mobile_device_id": "m9",
	}
	raw, _ := json.Marshal(doc)
	ctx := context.Background()
	go wl.dispatch(ctx, SSEEvent{Kind: "put", Path: "/r9", Data: raw})
	select {
	case ev := <-wl.out:
		t.Errorf("unexpected event for wrong target: %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// expected
	}
}

func TestListenerSubscribePath(t *testing.T) {
	got := listenerSubscribePath("alice")
	if !strings.HasSuffix(got, "/alice/wake_requests") {
		t.Errorf("subscribe path = %q", got)
	}
}
