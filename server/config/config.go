// Package config loads peersh-signaling configuration from a TOML file plus
// environment-variable overrides.
//
// Precedence: defaults < TOML file < env vars. Env vars are picked up at
// load time only (no live reload).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the resolved server configuration.
type Config struct {
	ListenAddr string `toml:"listen_addr"`
	DBPath     string `toml:"db_path"`
	ServerID   string `toml:"server_id"`
	LogLevel   string `toml:"log_level"`

	// AuthProvider selects the auth.Provider used at Register. Values:
	// "psk" (default; Phase 2) or "firebase" (Phase 5; the official
	// hosted server option).
	AuthProvider string `toml:"auth_provider"`

	// StoreBackend selects the store.Store implementation. Values:
	// "sqlite" (default; Phase 2) or "firestore" (Phase 5).
	StoreBackend string `toml:"store_backend"`

	TLS       TLSConfig       `toml:"tls"`
	Clock     ClockConfig     `toml:"clock"`
	RateLimit RateLimitConfig `toml:"rate_limit"`
	Discovery DiscoveryConfig `toml:"discovery"`
	Firebase  FirebaseConfig  `toml:"firebase"`

	// BootstrapPSKs is the list of PSK records to ensure-exist on every
	// server startup. Useful on platforms with ephemeral filesystems
	// (Cloud Run, Render Free, etc.) where the SQLite store does not
	// survive cold starts. Configured via TOML or via the
	// PEERSH_SIGNALING_BOOTSTRAP_PSK env var.
	BootstrapPSKs []BootstrapPSK `toml:"bootstrap_psk"`

	// MetricsToken, when non-empty, gates the /metrics endpoint behind
	// `Authorization: Bearer <token>`. Empty (the default) disables
	// /metrics entirely (fail-closed) so a misconfigured deploy does
	// not silently leak Prometheus telemetry to the public internet.
	// Configured via TOML key `metrics_token` or the
	// PEERSH_SIGNALING_METRICS_TOKEN env var.
	MetricsToken string `toml:"metrics_token"`
}

// BootstrapPSK is one (user_id, secret) pair to seed at startup.
type BootstrapPSK struct {
	UserID    string `toml:"user_id"`
	SecretHex string `toml:"secret_hex"`
	Label     string `toml:"label"`
}

// FirebaseConfig configures the firebase auth provider and the firestore
// store. Both share the same Google Cloud project; specifying separate
// credentials is supported but unusual.
type FirebaseConfig struct {
	ProjectID       string `toml:"project_id"`
	CredentialsPath string `toml:"credentials_path"`

	// AppCheckRequired enforces Firebase App Check. When true, Register
	// frames without a valid App Check token are rejected (only
	// applicable in firebase auth mode). Roll out by enabling on
	// clients first, then flipping this on once telemetry confirms
	// every active client is sending tokens.
	AppCheckRequired bool `toml:"app_check_required"`
}

// DiscoveryConfig populates the /.well-known/peersh.json document the
// mobile app fetches from the server's HTTPS root. WS_URL must be set if
// discovery is exposed publicly; STUNServers may be empty.
type DiscoveryConfig struct {
	WSURL       string   `toml:"ws_url"`
	STUNServers []string `toml:"stun_servers"`
}

// TLSConfig points to certificate material on disk. Empty values mean run
// plain HTTP (only acceptable for local development).
type TLSConfig struct {
	CertFile string `toml:"cert_file"`
	KeyFile  string `toml:"key_file"`
}

// Insecure reports whether the TLS config asks the server to listen on
// plain HTTP.
func (t TLSConfig) Insecure() bool { return t.CertFile == "" && t.KeyFile == "" }

// ClockConfig controls PSK signature validity.
type ClockConfig struct {
	Skew        Duration `toml:"skew"`
	NonceWindow Duration `toml:"nonce_window"`
}

// RateLimitConfig is the per-key token-bucket configuration.
type RateLimitConfig struct {
	IPPerMinute     float64 `toml:"ip_per_minute"`
	IPBurst         float64 `toml:"ip_burst"`
	UserPerMinute   float64 `toml:"user_per_minute"`
	UserBurst       float64 `toml:"user_burst"`
	DevicePerMinute float64 `toml:"device_per_minute"`
	DeviceBurst     float64 `toml:"device_burst"`
}

// Duration is a TOML-friendly time.Duration that accepts strings like "60s"
// or "5m".
type Duration struct{ time.Duration }

// UnmarshalText satisfies encoding.TextUnmarshaler for TOML.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText satisfies encoding.TextMarshaler for TOML.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

// Defaults returns a Config populated with safe defaults. Used as a base
// before applying file + env overrides.
func Defaults() Config {
	return Config{
		ListenAddr:   ":8443",
		DBPath:       "peersh-signaling.db",
		ServerID:     "peersh-signaling/0.1",
		LogLevel:     "info",
		AuthProvider: "psk",
		StoreBackend: "sqlite",
		Clock: ClockConfig{
			Skew:        Duration{60 * time.Second},
			NonceWindow: Duration{5 * time.Minute},
		},
		RateLimit: RateLimitConfig{
			IPPerMinute:     10,
			IPBurst:         3,
			UserPerMinute:   10,
			UserBurst:       3,
			DevicePerMinute: 30,
			DeviceBurst:     5,
		},
	}
}

// Load reads a TOML file at path, applies env-var overrides, and validates
// the result. If path is empty, only defaults + env-var overrides are used.
func Load(path string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config: read %q: %w", path, err)
		}
		md, err := toml.Decode(string(data), &cfg)
		if err != nil {
			return Config{}, fmt.Errorf("config: decode %q: %w", path, err)
		}
		if u := md.Undecoded(); len(u) > 0 {
			keys := make([]string, 0, len(u))
			for _, k := range u {
				keys = append(keys, k.String())
			}
			return Config{}, fmt.Errorf("config: unknown keys: %s", strings.Join(keys, ", "))
		}
	}

	applyEnv(&cfg)
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv overrides fields from PEERSH_SIGNALING_* env vars.
func applyEnv(cfg *Config) {
	if v := os.Getenv("PEERSH_SIGNALING_LISTEN"); v != "" {
		cfg.ListenAddr = v
	} else if v := os.Getenv("PORT"); v != "" {
		// $PORT is the convention used by Render.com, Cloud Run, App
		// Engine, Heroku, Fly.io, etc. We honour it when
		// PEERSH_SIGNALING_LISTEN is unset so the same Docker image
		// drops in on those platforms unchanged.
		cfg.ListenAddr = ":" + v
	}
	if v := os.Getenv("PEERSH_SIGNALING_DB"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_TLS_CERT"); v != "" {
		cfg.TLS.CertFile = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_TLS_KEY"); v != "" {
		cfg.TLS.KeyFile = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_DISCOVERY_WS_URL"); v != "" {
		cfg.Discovery.WSURL = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_DISCOVERY_STUN_SERVERS"); v != "" {
		cfg.Discovery.STUNServers = splitCSV(v)
	}
	if v := os.Getenv("PEERSH_SIGNALING_AUTH_PROVIDER"); v != "" {
		cfg.AuthProvider = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_STORE_BACKEND"); v != "" {
		cfg.StoreBackend = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_FIREBASE_PROJECT_ID"); v != "" {
		cfg.Firebase.ProjectID = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_FIREBASE_CREDENTIALS"); v != "" {
		cfg.Firebase.CredentialsPath = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_FIREBASE_APP_CHECK_REQUIRED"); v != "" {
		cfg.Firebase.AppCheckRequired = v == "1" || v == "true" || v == "TRUE"
	}
	if v := os.Getenv("PEERSH_SIGNALING_BOOTSTRAP_PSK"); v != "" {
		if parsed, err := parseBootstrapPSKs(v); err == nil {
			cfg.BootstrapPSKs = append(cfg.BootstrapPSKs, parsed...)
		}
	}
	if v := os.Getenv("PEERSH_SIGNALING_METRICS_TOKEN"); v != "" {
		cfg.MetricsToken = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_SERVER_ID"); v != "" {
		cfg.ServerID = v
	}
	if v := os.Getenv("PEERSH_SIGNALING_CLOCK_SKEW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Clock.Skew = Duration{d}
		}
	}
	if v := os.Getenv("PEERSH_SIGNALING_NONCE_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Clock.NonceWindow = Duration{d}
		}
	}
	if v := os.Getenv("PEERSH_SIGNALING_IP_PER_MINUTE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.RateLimit.IPPerMinute = f
		}
	}
}

// splitCSV is a small helper for env-var-encoded comma-separated lists.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseBootstrapPSKs parses the PEERSH_SIGNALING_BOOTSTRAP_PSK env var
// format:
//
//	user1:hex_secret1[:label1],user2:hex_secret2[:label2]
//
// Whitespace around tokens is trimmed. Entries that do not parse are
// silently dropped (the server still starts; only the broken entry is
// missing).
func parseBootstrapPSKs(s string) ([]BootstrapPSK, error) {
	out := make([]BootstrapPSK, 0)
	for _, entry := range splitCSV(s) {
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) < 2 {
			continue
		}
		bp := BootstrapPSK{
			UserID:    strings.TrimSpace(parts[0]),
			SecretHex: strings.TrimSpace(parts[1]),
		}
		if len(parts) == 3 {
			bp.Label = strings.TrimSpace(parts[2])
		}
		if bp.UserID == "" || bp.SecretHex == "" {
			continue
		}
		out = append(out, bp)
	}
	return out, nil
}

func (c Config) validate() error {
	if c.ListenAddr == "" {
		return errors.New("config: listen_addr must not be empty")
	}
	switch c.AuthProvider {
	case "psk", "firebase":
	default:
		return fmt.Errorf("config: auth_provider must be psk or firebase, got %q", c.AuthProvider)
	}
	switch c.StoreBackend {
	case "sqlite", "firestore":
	default:
		return fmt.Errorf("config: store_backend must be sqlite or firestore, got %q", c.StoreBackend)
	}
	if c.StoreBackend == "sqlite" && c.DBPath == "" {
		return errors.New("config: db_path must not be empty when store_backend = sqlite")
	}
	if c.StoreBackend == "firestore" && c.Firebase.ProjectID == "" {
		return errors.New("config: firebase.project_id is required when store_backend = firestore")
	}
	if c.AuthProvider == "firebase" && c.Firebase.ProjectID == "" {
		return errors.New("config: firebase.project_id is required when auth_provider = firebase")
	}
	if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
		return errors.New("config: tls.cert_file and tls.key_file must be both set or both empty")
	}
	if c.Clock.Skew.Duration <= 0 {
		return errors.New("config: clock.skew must be > 0")
	}
	if c.Clock.NonceWindow.Duration <= 0 {
		return errors.New("config: clock.nonce_window must be > 0")
	}
	return nil
}

// SlogLevel returns the parsed slog.Level. Defaults to INFO on parse error.
func (c Config) SlogLevel() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// PerSecond converts a per-minute rate to per-second tokens.
func PerSecond(perMinute float64) float64 { return perMinute / 60.0 }
