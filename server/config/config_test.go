package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/peersh/peersh/server/config"
)

func TestDefaultsValidate(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if cfg.ListenAddr != ":8443" {
		t.Fatalf("listen_addr default: %q", cfg.ListenAddr)
	}
	if cfg.Clock.Skew.Duration != 60*time.Second {
		t.Fatalf("clock.skew default: %v", cfg.Clock.Skew.Duration)
	}
}

func TestTOMLOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "signaling.toml")
	body := `
listen_addr = ":9999"
db_path = "/tmp/test.db"
log_level = "debug"

[clock]
skew = "30s"
nonce_window = "10m"

[rate_limit]
ip_per_minute = 5
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":9999" {
		t.Fatalf("listen_addr override: %q", cfg.ListenAddr)
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Fatalf("db_path override: %q", cfg.DBPath)
	}
	if cfg.Clock.Skew.Duration != 30*time.Second {
		t.Fatalf("clock.skew override: %v", cfg.Clock.Skew.Duration)
	}
	if cfg.RateLimit.IPPerMinute != 5 {
		t.Fatalf("rate_limit override: %v", cfg.RateLimit.IPPerMinute)
	}
}

func TestEnvOverridesTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	if err := os.WriteFile(path, []byte(`listen_addr = ":7000"`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEERSH_SIGNALING_LISTEN", ":8000")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":8000" {
		t.Fatalf("expected env override to win, got %q", cfg.ListenAddr)
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	if err := os.WriteFile(path, []byte(`mystery_key = "value"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected rejection of unknown keys")
	}
}

func TestValidateTLSPair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.toml")
	if err := os.WriteFile(path, []byte(`
[tls]
cert_file = "x"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error when only one TLS file is set")
	}
}
