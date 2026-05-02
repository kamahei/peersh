package firebase

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRTDBHostSuffix(t *testing.T) {
	cases := map[string]string{
		"":                "firebaseio.com",
		"us-central1":     "firebaseio.com",
		"asia-southeast1": "asia-southeast1.firebasedatabase.app",
		"europe-west1":    "europe-west1.firebasedatabase.app",
	}
	for region, want := range cases {
		if got := rtdbHostSuffix(region); got != want {
			t.Errorf("rtdbHostSuffix(%q) = %q; want %q", region, got, want)
		}
	}
}

func TestNewClient_RejectsEmptyProject(t *testing.T) {
	if _, err := NewClient("", "us-central1"); err == nil {
		t.Fatal("expected error on empty project id")
	}
}

func TestNewClient_BuildsExpectedURL(t *testing.T) {
	c, err := NewClient("peersh-test", "asia-southeast1")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://peersh-test-default-rtdb.asia-southeast1.firebasedatabase.app"
	if c.baseURL != want {
		t.Errorf("baseURL = %q; want %q", c.baseURL, want)
	}
}

func TestNewClient_HonorsEmulatorEnv(t *testing.T) {
	t.Setenv("FIREBASE_DATABASE_EMULATOR_HOST", "127.0.0.1:9000")
	c, err := NewClient("peersh-test", "asia-southeast1")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "http://127.0.0.1:9000" {
		t.Errorf("emulator baseURL = %q", c.baseURL)
	}
	if c.extraQuery.Get("ns") != "peersh-test-default-rtdb" {
		t.Errorf("emulator ns = %q", c.extraQuery.Get("ns"))
	}
}

func TestClientURL(t *testing.T) {
	c := &Client{baseURL: "https://x.firebaseio.com"}
	if got := c.url("/users/u1/devices/d1", nil); got != "https://x.firebaseio.com/users/u1/devices/d1.json" {
		t.Errorf("url no-query = %q", got)
	}
	emu := &Client{baseURL: "http://localhost:9000", extraQuery: url.Values{"ns": []string{"foo"}}}
	got := emu.url("/users/u1/wake_requests", url.Values{"auth": []string{"tok"}})
	if !strings.HasPrefix(got, "http://localhost:9000/users/u1/wake_requests.json?") {
		t.Errorf("emulator url path: %q", got)
	}
	if !strings.Contains(got, "ns=foo") {
		t.Errorf("emulator url missing ns: %q", got)
	}
	if !strings.Contains(got, "auth=tok") {
		t.Errorf("emulator url missing auth: %q", got)
	}
}

func TestPathToKey(t *testing.T) {
	cases := map[string]string{
		"/wake-1": "wake-1",
		"/":       "",
		"":        "",
	}
	for in, want := range cases {
		if got := PathToKey(in); got != want {
			t.Errorf("PathToKey(%q) = %q; want %q", in, got, want)
		}
	}
}

// --- SSE parser tests ------------------------------------------------------

// fakeStream returns a Stream backed by the supplied SSE wire bytes.
func fakeStream(t *testing.T, body string) *Stream {
	t.Helper()
	r, w := io.Pipe()
	go func() {
		_, _ = io.Copy(w, strings.NewReader(body))
		_ = w.Close()
	}()
	s := &Stream{
		body:   r,
		events: make(chan SSEEvent, 16),
		errCh:  make(chan error, 1),
	}
	go s.run()
	return s
}

func collectEvents(t *testing.T, s *Stream, n int, timeout time.Duration) []SSEEvent {
	t.Helper()
	out := make([]SSEEvent, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case ev, ok := <-s.C():
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("timed out collecting events; got %d/%d", len(out), n)
		}
	}
	return out
}

func TestSSEParser_PutPatchKeepalive(t *testing.T) {
	body := "" +
		"event: put\ndata: {\"path\":\"/\",\"data\":{\"a\":1}}\n\n" +
		"event: patch\ndata: {\"path\":\"/foo\",\"data\":{\"b\":2}}\n\n" +
		"event: keep-alive\ndata: null\n\n" +
		"event: cancel\ndata: null\n\n" +
		"event: auth_revoked\ndata: \"x\"\n\n"
	s := fakeStream(t, body)
	defer s.Close()

	got := collectEvents(t, s, 5, 2*time.Second)

	if got[0].Kind != "put" || got[0].Path != "/" {
		t.Errorf("event 0: %+v", got[0])
	}
	var data map[string]int
	if err := json.Unmarshal(got[0].Data, &data); err != nil || data["a"] != 1 {
		t.Errorf("event 0 data parse: %v %v", err, data)
	}

	if got[1].Kind != "patch" || got[1].Path != "/foo" {
		t.Errorf("event 1: %+v", got[1])
	}

	if got[2].Kind != "keep-alive" {
		t.Errorf("event 2: %+v", got[2])
	}
	if got[3].Kind != "cancel" {
		t.Errorf("event 3: %+v", got[3])
	}
	if got[4].Kind != "auth_revoked" {
		t.Errorf("event 4: %+v", got[4])
	}
}

func TestSSEParser_HandlesNullDataAsDelete(t *testing.T) {
	body := "event: put\ndata: {\"path\":\"/wake-1\",\"data\":null}\n\n"
	s := fakeStream(t, body)
	defer s.Close()

	got := collectEvents(t, s, 1, time.Second)
	if got[0].Kind != "put" || got[0].Path != "/wake-1" {
		t.Errorf("kind/path: %+v", got[0])
	}
	if string(got[0].Data) != "null" {
		t.Errorf("expected data=null; got %s", string(got[0].Data))
	}
}

// --- REST client tests via httptest ----------------------------------------

func TestSet_SendsPUTWithAuth(t *testing.T) {
	var gotBody string
	var gotPath string
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := c.Set(context.Background(), "/users/u/devices/d", map[string]int{"x": 1}, "tok"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if !strings.HasPrefix(gotPath, "/users/u/devices/d.json?") {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotPath, "auth=tok") {
		t.Errorf("missing auth: %q", gotPath)
	}
	if gotBody != `{"x":1}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestSet_PropagatesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, http: srv.Client()}
	err := c.Set(context.Background(), "/x", "y", "tok")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error; got %v", err)
	}
}

func TestDelete_SendsDELETE(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := c.Delete(context.Background(), "/x", "tok"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q; want DELETE", gotMethod)
	}
}

func TestPush_ReturnsServerName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"-NabcXYZ"}`))
	}))
	defer srv.Close()
	c := &Client{baseURL: srv.URL, http: srv.Client()}
	name, err := c.Push(context.Background(), "/users/u/wake_requests", map[string]string{"a": "b"}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if name != "-NabcXYZ" {
		t.Errorf("name = %q", name)
	}
}

func TestGet_DecodesIntoOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"x":42}`))
	}))
	defer srv.Close()
	c := &Client{baseURL: srv.URL, http: srv.Client()}
	var out map[string]int
	if err := c.Get(context.Background(), "/x", "", &out); err != nil {
		t.Fatal(err)
	}
	if out["x"] != 42 {
		t.Errorf("out = %v", out)
	}
}

func TestServerTimestampSentinel(t *testing.T) {
	raw, _ := json.Marshal(ServerTimestamp)
	want := `{".sv":"timestamp"}`
	if string(raw) != want {
		t.Errorf("ServerTimestamp marshal = %s; want %s", raw, want)
	}
}
