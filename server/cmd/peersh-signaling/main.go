// Command peersh-signaling is the peersh signaling server.
//
// Subcommands:
//
//	peersh-signaling serve   --config /etc/peersh/signaling.toml
//	peersh-signaling psk add --user <id> [--label <text>]
//	peersh-signaling psk list
//	peersh-signaling psk revoke --user <id>
//
// The server is connection-setup-only — it never sees PowerShell command
// content. All command bytes flow peer-to-peer over QUIC.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/peersh/peersh/core/auth"
	fbauth "github.com/peersh/peersh/core/auth/firebase"
	"github.com/peersh/peersh/core/auth/psk"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/store"
	fsstore "github.com/peersh/peersh/core/store/firestore"
	"github.com/peersh/peersh/core/store/sqlite"
	"github.com/peersh/peersh/server/admin"
	"github.com/peersh/peersh/server/config"
	"github.com/peersh/peersh/server/ratelimit"
	"github.com/peersh/peersh/server/room"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/peersh/peersh/server/ws"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	switch cmd {
	case "serve":
		if err := runServe(args); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	case "psk":
		if err := runPSK(args); err != nil {
			fmt.Fprintln(os.Stderr, "psk:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	case "version", "-v", "--version":
		fmt.Println("peersh-signaling/0.1")
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `peersh-signaling — peersh signaling server

Usage:
  peersh-signaling serve [-config <path>]
  peersh-signaling psk add --user <id> [--label <text>]
  peersh-signaling psk list
  peersh-signaling psk revoke --user <id>
  peersh-signaling version
`)
}

// runServe loads config, opens the store, and starts the HTTP/WS server.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "path to signaling.toml (optional; defaults + env vars used otherwise)")
	insecureHTTP := fs.Bool("insecure-http", false, "serve plain HTTP even when no TLS is configured (DEV ONLY)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(logger)
	logger.Info("loaded config",
		"listen", cfg.ListenAddr,
		"auth", cfg.AuthProvider,
		"store", cfg.StoreBackend,
		"tls", !cfg.TLS.Insecure())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	if err := applyBootstrapPSKs(ctx, st, cfg.BootstrapPSKs, logger); err != nil {
		return fmt.Errorf("bootstrap psk: %w", err)
	}

	authProvider, authBuilder, authKind, err := buildAuth(ctx, cfg, st)
	if err != nil {
		return fmt.Errorf("build auth: %w", err)
	}

	metrics := ws.NewMetrics()
	if err := metrics.Register(prometheus.DefaultRegisterer); err != nil {
		return fmt.Errorf("metrics: %w", err)
	}

	server := ws.New(&ws.Server{
		ServerID:    cfg.ServerID,
		Store:       st,
		Auth:        authProvider,
		AuthBuilder: authBuilder,
		AuthKind:    authKind,
		Registry:    room.New(),
		IPLimit:     ratelimit.New(config.PerSecond(cfg.RateLimit.IPPerMinute), cfg.RateLimit.IPBurst),
		UserLimit:   ratelimit.New(config.PerSecond(cfg.RateLimit.UserPerMinute), cfg.RateLimit.UserBurst),
		DeviceLimit: ratelimit.New(config.PerSecond(cfg.RateLimit.DevicePerMinute), cfg.RateLimit.DeviceBurst),
		Logger:      logger,
		Metrics:     metrics,
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", server.Handler())
	// Cloud Run / Google Front End intercepts /healthz at the edge, so the
	// app-level liveness probe is exposed at /health instead.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/.well-known/peersh.json", ws.DiscoveryHandler(ws.DiscoveryConfig{
		Version:       1,
		WSURL:         cfg.Discovery.WSURL,
		STUNServers:   cfg.Discovery.STUNServers,
		AuthProviders: []string{cfg.AuthProvider},
	}))

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if cfg.TLS.Insecure() {
			if !*insecureHTTP {
				errCh <- errors.New("no TLS configured; pass -insecure-http to allow plain HTTP (development only)")
				return
			}
			logger.Warn("listening on plain HTTP — DEVELOPMENT ONLY", "addr", cfg.ListenAddr)
			errCh <- httpSrv.ListenAndServe()
			return
		}
		logger.Info("listening on HTTPS", "addr", cfg.ListenAddr)
		errCh <- httpSrv.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	}
}

// openStore returns the configured store.Store implementation.
func openStore(ctx context.Context, cfg config.Config) (store.Store, error) {
	switch cfg.StoreBackend {
	case "sqlite":
		return sqlite.Open(cfg.DBPath)
	case "firestore":
		return fsstore.OpenWithProject(ctx, cfg.Firebase.ProjectID, cfg.Firebase.CredentialsPath)
	default:
		return nil, fmt.Errorf("config: unknown store_backend %q", cfg.StoreBackend)
	}
}

// buildAuth returns the configured auth.Provider plus the matching
// CredentialsBuilder and the AuthProvider tag persisted alongside new
// users.
func buildAuth(ctx context.Context, cfg config.Config, st store.Store) (auth.Provider, ws.CredentialsBuilder, store.AuthProvider, error) {
	switch cfg.AuthProvider {
	case "psk":
		p := psk.New(st)
		p.ClockSkew = cfg.Clock.Skew.Duration
		p.Nonces = psk.NewMemoryNonceCache(cfg.Clock.NonceWindow.Duration)
		return p, ws.PSKCredentialsBuilder, store.AuthProviderPSK, nil
	case "firebase":
		p, err := fbauth.NewFromServiceAccount(ctx, cfg.Firebase.ProjectID, cfg.Firebase.CredentialsPath)
		if err != nil {
			return nil, nil, store.AuthProviderUnknown, err
		}
		builder := func(reg *signalv1.Register) (auth.Credentials, error) {
			return fbauth.Credentials{IDToken: reg.GetFirebaseIdToken()}, nil
		}
		return p, builder, store.AuthProviderFirebase, nil
	default:
		return nil, nil, store.AuthProviderUnknown, fmt.Errorf("config: unknown auth_provider %q", cfg.AuthProvider)
	}
}

// applyBootstrapPSKs upserts every PSK record listed in cfg.BootstrapPSKs
// so the server can run on a platform with an ephemeral filesystem
// (Cloud Run / Render Free) without losing PSKs across cold starts.
//
// Bootstrap entries are upserts: an entry that already exists with a
// different secret is overwritten with the env-var value. Operators
// using a persistent backend (sqlite + disk, firestore) can leave the
// list empty.
func applyBootstrapPSKs(ctx context.Context, st store.Store, entries []config.BootstrapPSK, logger *slog.Logger) error {
	if len(entries) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for _, e := range entries {
		secret, err := hex.DecodeString(e.SecretHex)
		if err != nil {
			logger.Warn("bootstrap psk: bad hex; skipping",
				"user", e.UserID, "err", err)
			continue
		}
		if _, err := st.GetUser(ctx, e.UserID); err != nil {
			if err := st.PutUser(ctx, store.User{
				ID:           e.UserID,
				AuthProvider: store.AuthProviderPSK,
				CreatedAt:    now,
			}); err != nil {
				return fmt.Errorf("put user %s: %w", e.UserID, err)
			}
		}
		if err := st.PutPSKRecord(ctx, store.PSKRecord{
			UserID:       e.UserID,
			Secret:       secret,
			DisplayLabel: e.Label,
			CreatedAt:    now,
		}); err != nil {
			return fmt.Errorf("put psk %s: %w", e.UserID, err)
		}
		logger.Info("bootstrap psk applied", "user", e.UserID, "label", e.Label)
	}
	return nil
}

// runPSK dispatches the psk subsubcommand.
func runPSK(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: psk <add|list|revoke> ...")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "add":
		return runPSKAdd(rest)
	case "list":
		return runPSKList(rest)
	case "revoke":
		return runPSKRevoke(rest)
	default:
		return fmt.Errorf("unknown psk subcommand %q", cmd)
	}
}

func runPSKAdd(args []string) error {
	fs := flag.NewFlagSet("psk add", flag.ExitOnError)
	configPath := fs.String("config", "", "config path (for db_path resolution)")
	user := fs.String("user", "", "user_id to attach the PSK to")
	label := fs.String("label", "", "human-readable label (e.g. \"alice-laptop\")")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *user == "" {
		return errors.New("--user is required")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	res, err := admin.AddPSK(context.Background(), store, *user, *label)
	if err != nil {
		return err
	}
	fmt.Println("PSK created. Save this — it cannot be retrieved later.")
	fmt.Printf("  user:   %s\n", res.UserID)
	fmt.Printf("  label:  %s\n", res.Label)
	fmt.Printf("  secret: %s\n", res.SecretHex)
	fmt.Printf("  added:  %s\n", res.CreatedAt.Format(time.RFC3339))
	return nil
}

func runPSKList(args []string) error {
	fs := flag.NewFlagSet("psk list", flag.ExitOnError)
	configPath := fs.String("config", "", "config path (for db_path resolution)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	all, err := admin.ListPSKs(context.Background(), store)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "USER\tLABEL\tCREATED\tREVOKED")
	for _, r := range all {
		revoked := "—"
		if r.IsRevoked() {
			revoked = r.RevokedAt.Format(time.RFC3339)
		}
		label := r.DisplayLabel
		if label == "" {
			label = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.UserID, label, r.CreatedAt.Format(time.RFC3339), revoked)
	}
	return tw.Flush()
}

func runPSKRevoke(args []string) error {
	fs := flag.NewFlagSet("psk revoke", flag.ExitOnError)
	configPath := fs.String("config", "", "config path (for db_path resolution)")
	user := fs.String("user", "", "user_id to revoke")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *user == "" {
		return errors.New("--user is required")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	if err := admin.RevokePSK(context.Background(), store, *user); err != nil {
		return err
	}
	fmt.Printf("revoked: %s\n", *user)
	return nil
}
