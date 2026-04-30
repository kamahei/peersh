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

	"github.com/peersh/peersh/core/auth/psk"
	"github.com/peersh/peersh/core/store/sqlite"
	"github.com/peersh/peersh/server/admin"
	"github.com/peersh/peersh/server/config"
	"github.com/peersh/peersh/server/ratelimit"
	"github.com/peersh/peersh/server/room"
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
	logger.Info("loaded config", "listen", cfg.ListenAddr, "db", cfg.DBPath, "tls", !cfg.TLS.Insecure())

	store, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	provider := psk.New(store)
	provider.ClockSkew = cfg.Clock.Skew.Duration
	provider.Nonces = psk.NewMemoryNonceCache(cfg.Clock.NonceWindow.Duration)

	server := ws.New(&ws.Server{
		ServerID:    cfg.ServerID,
		Store:       store,
		Auth:        provider,
		Registry:    room.New(),
		IPLimit:     ratelimit.New(config.PerSecond(cfg.RateLimit.IPPerMinute), cfg.RateLimit.IPBurst),
		UserLimit:   ratelimit.New(config.PerSecond(cfg.RateLimit.UserPerMinute), cfg.RateLimit.UserBurst),
		DeviceLimit: ratelimit.New(config.PerSecond(cfg.RateLimit.DevicePerMinute), cfg.RateLimit.DeviceBurst),
		Logger:      logger,
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", server.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
