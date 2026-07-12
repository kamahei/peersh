package firebase

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDevicePath(t *testing.T) {
	if got := devicePath("alice", "dev-1"); got != "/users/alice/devices/dev-1" {
		t.Errorf("devicePath = %q", got)
	}
}

func TestWakeRequestPath(t *testing.T) {
	if got := wakeRequestPath("alice", "wake-1"); got != "/users/alice/wake_requests/wake-1" {
		t.Errorf("wakeRequestPath = %q", got)
	}
}

func TestRegisterDevice_WritesServerTimestamp(t *testing.T) {
	type req struct{ method, path, body string }
	var reqs []req
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs = append(reqs, req{r.Method, r.URL.Path + "?" + r.URL.RawQuery, string(body)})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, http: srv.Client()}
	src := &fakeTokenSource{uid: "alice", token: "tok"}
	if err := RegisterDevice(context.Background(), c, src, "alice", "dev-1", "MacBook", "mac"); err != nil {
		t.Fatal(err)
	}

	// last_seen_at must be a PUT of the server-timestamp sentinel with auth.
	var sawTimestamp bool
	for _, rq := range reqs {
		if strings.HasPrefix(rq.path, "/users/alice/devices/dev-1/last_seen_at.json?") {
			sawTimestamp = true
			if rq.method != http.MethodPut {
				t.Errorf("last_seen_at method = %q; want PUT", rq.method)
			}
			if !strings.Contains(rq.path, "auth=tok") {
				t.Errorf("missing auth: %q", rq.path)
			}
			if !strings.Contains(rq.body, `".sv":"timestamp"`) {
				t.Errorf("body missing server-timestamp sentinel: %q", rq.body)
			}
		}
	}
	if !sawTimestamp {
		t.Errorf("no last_seen_at write among %d requests", len(reqs))
	}

	// Discovery metadata the client picker reads: kind/platform/display_name.
	gotKind, gotPlatform, gotName := "", "", ""
	for _, rq := range reqs {
		switch {
		case strings.HasPrefix(rq.path, "/users/alice/devices/dev-1/kind.json?"):
			gotKind = rq.body
		case strings.HasPrefix(rq.path, "/users/alice/devices/dev-1/platform.json?"):
			gotPlatform = rq.body
		case strings.HasPrefix(rq.path, "/users/alice/devices/dev-1/display_name.json?"):
			gotName = rq.body
		}
	}
	if gotKind != `"host"` || gotPlatform != `"mac"` || gotName != `"MacBook"` {
		t.Errorf("discovery metadata = kind:%s platform:%s name:%s", gotKind, gotPlatform, gotName)
	}
}

func TestHeartbeat_WritesServerTimestamp(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, http: srv.Client()}
	src := &fakeTokenSource{uid: "alice", token: "tok"}
	if err := Heartbeat(context.Background(), c, src, "alice", "dev-1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/users/alice/devices/dev-1/last_seen_at.json" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestDeleteWakeRequest_SendsDELETE(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, http: srv.Client()}
	src := &fakeTokenSource{uid: "alice", token: "tok"}
	if err := DeleteWakeRequest(context.Background(), c, src, "alice", "wake-1"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q; want DELETE", gotMethod)
	}
	if gotPath != "/users/alice/wake_requests/wake-1.json" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestRuntime_DelegatesAll(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.Method+" "+r.URL.Path]++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, http: srv.Client()}
	src := &fakeTokenSource{uid: "alice", token: "tok"}
	rt := &Runtime{
		Client:   c,
		Source:   src,
		UID:      "alice",
		DeviceID: "dev-1",
	}
	ctx := context.Background()
	if err := rt.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.DeleteWakeRequest(ctx, "wake-1"); err != nil {
		t.Fatal(err)
	}
	if calls["PUT /users/alice/devices/dev-1/last_seen_at.json"] != 1 {
		t.Errorf("Heartbeat did not PUT last_seen_at: %v", calls)
	}
	if calls["DELETE /users/alice/wake_requests/wake-1.json"] != 1 {
		t.Errorf("DeleteWakeRequest did not DELETE: %v", calls)
	}
}
