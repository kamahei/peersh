package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateMetricsBind(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		token   string
		wantErr bool
	}{
		{"loopback no token", "127.0.0.1:9101", "", false},
		{"loopback with token", "127.0.0.1:9101", "tok", false},
		{"localhost no token", "localhost:9101", "", false},
		{"public bind with token", "0.0.0.0:9101", "tok", false},
		{"public bind no token", "0.0.0.0:9101", "", true},
		{"specific public bind no token", "10.0.0.5:9101", "", true},
		{"all interfaces no host no token", ":9101", "", true},
		{"empty addr", "", "", true},
		{"malformed", "garbage", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateMetricsBind(c.addr, c.token)
			if (err != nil) != c.wantErr {
				t.Errorf("validateMetricsBind(%q, %q) err=%v; wantErr=%v", c.addr, c.token, err, c.wantErr)
			}
		})
	}
}

func TestMetricsTokenHandler_NoTokenAllowsAll(t *testing.T) {
	// On loopback peershd skips the bearer check entirely.
	srv := httptest.NewServer(metricsTokenHandler("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d; want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q; want ok", string(body))
	}
}

func TestMetricsTokenHandler_TokenRequired(t *testing.T) {
	srv := httptest.NewServer(metricsTokenHandler("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})))
	defer srv.Close()

	// No header → 401
	resp, _ := http.Get(srv.URL + "/metrics")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-token status = %d; want 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("missing WWW-Authenticate Bearer challenge")
	}
	resp.Body.Close()

	// Wrong header → 401
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-token status = %d; want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Right header → 200
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("good-token status = %d; want 200", resp.StatusCode)
	}
	resp.Body.Close()
}
