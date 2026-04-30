package ws_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/peersh/peersh/server/ws"
)

func TestDiscoveryHandler(t *testing.T) {
	cfg := ws.DiscoveryConfig{
		Version:       1,
		WSURL:         "wss://signaling.example.com/ws",
		STUNServers:   []string{"stun.l.google.com:19302"},
		AuthProviders: []string{"psk"},
	}
	srv := httptest.NewServer(ws.DiscoveryHandler(cfg))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type: %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("cache-control: %q", cc)
	}

	var got ws.DiscoveryConfig
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != 1 || got.WSURL != cfg.WSURL ||
		len(got.STUNServers) != 1 || got.STUNServers[0] != "stun.l.google.com:19302" ||
		len(got.AuthProviders) != 1 || got.AuthProviders[0] != "psk" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestDiscoveryHandlerHEAD(t *testing.T) {
	srv := httptest.NewServer(ws.DiscoveryHandler(ws.DiscoveryConfig{
		Version: 1, WSURL: "ws://localhost/ws",
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodHead, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestDiscoveryHandlerMethodNotAllowed(t *testing.T) {
	srv := httptest.NewServer(ws.DiscoveryHandler(ws.DiscoveryConfig{Version: 1}))
	defer srv.Close()
	resp, err := http.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if a := resp.Header.Get("Allow"); a == "" {
		t.Fatal("Allow header missing")
	}
}
