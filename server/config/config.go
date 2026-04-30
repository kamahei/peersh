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

	TLS       TLSConfig       `toml:"tls"`
	Clock     ClockConfig     `toml:"clock"`
	RateLimit RateLimitConfig `toml:"rate_limit"`
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
		ListenAddr: ":8443",
		DBPath:     "peersh-signaling.db",
		ServerID:   "peersh-signaling/0.1",
		LogLevel:   "info",
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

func (c Config) validate() error {
	if c.ListenAddr == "" {
		return errors.New("config: listen_addr must not be empty")
	}
	if c.DBPath == "" {
		return errors.New("config: db_path must not be empty")
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
