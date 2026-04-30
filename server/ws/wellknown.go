package ws

import (
	"encoding/json"
	"net/http"
)

// DiscoveryConfig is what the mobile app fetches from
// /.well-known/peersh.json to learn how to talk to a signaling server
// when the user types only its hostname.
type DiscoveryConfig struct {
	// Version is incremented for breaking shape changes. Phase 4 ships v1.
	Version int `json:"version"`

	// WSURL is the full wss:// or ws:// URL of the signaling endpoint.
	WSURL string `json:"ws_url"`

	// STUNServers is a hint for clients that haven't been told otherwise.
	// Empty list is allowed (clients fall back to their compiled-in
	// default).
	STUNServers []string `json:"stun_servers"`

	// AuthProviders lists the auth.Provider Kind() values this server
	// supports. Phase 2 ships ["psk"]; Phase 5 will add "firebase" on the
	// official server.
	AuthProviders []string `json:"auth_providers"`
}

// DiscoveryHandler returns an HTTP handler that serves cfg as the
// /.well-known/peersh.json document. The response is JSON with a
// no-cache header so configuration changes propagate quickly.
func DiscoveryHandler(cfg DiscoveryConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		body = append(body, '\n')
		_, _ = w.Write(body)
	})
}
