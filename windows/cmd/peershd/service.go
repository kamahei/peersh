package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kardianos/service"
)

// serviceConfig is the canonical kardianos/service config used by both
// install/uninstall and the runtime SCM dispatch.
func serviceConfig(args []string) *service.Config {
	cfg := &service.Config{
		Name:        "peershd",
		DisplayName: "peersh host",
		Description: "peersh — remote-shell host daemon for the peersh peer-to-peer remote-shell tool.",
		Arguments:   args,
	}
	if runtime.GOOS == "darwin" {
		// Install as a per-user LaunchAgent (~/Library/LaunchAgents), not a
		// root LaunchDaemon: peershd spawns the user's login shell and needs
		// their environment / keychain, and a LaunchAgent starts at login —
		// the macOS analogue of the Windows logon task. RunAtLoad + KeepAlive
		// start it at login and restart it on crash.
		cfg.Option = service.KeyValue{
			"UserService": true,
			"RunAtLoad":   true,
			"KeepAlive":   true,
		}
	}
	return cfg
}

// program implements service.Interface. It defers to runApp() (the
// existing non-service entry point) when the SCM (or interactive mode)
// asks it to start.
type program struct {
	args     []string
	cancel   context.CancelFunc
	doneCh   chan struct{}
	exitErr  error
}

func (p *program) Start(_ service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.doneCh = make(chan struct{})
	go func() {
		defer close(p.doneCh)
		p.exitErr = runWithCtx(ctx, p.args)
	}()
	return nil
}

func (p *program) Stop(_ service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.doneCh != nil {
		select {
		case <-p.doneCh:
		case <-time.After(8 * time.Second):
			return errors.New("peershd: timed out waiting for clean shutdown")
		}
	}
	return p.exitErr
}

// runService is the entry point used when the binary detects it should
// run under the Windows SCM (or via -install / -uninstall). It either
// performs the service action and exits, or hands control off to
// kardianos's Run loop which calls program.Start / Stop.
//
// argv is the full os.Args. Returns nil if the caller should fall through
// to the default interactive main path; otherwise returns the program's
// exit status (or any error).
func runService(argv []string) (handled bool, err error) {
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	install := fs.Bool("install", false, "install peershd as a Windows Service and exit")
	uninstall := fs.Bool("uninstall", false, "uninstall the peershd Windows Service and exit")
	startSvc := fs.Bool("start", false, "start the installed peershd service and exit")
	stopSvc := fs.Bool("stop", false, "stop the installed peershd service and exit")
	statusSvc := fs.Bool("service-status", false, "print the installed service status and exit")
	// runService inspects argv for known flags but does not consume the
	// rest; the regular main flag parsing handles those. ContinueOnError
	// + Parse(argv[1:]) lets us inspect specific switches without bailing
	// on unknown ones.
	fs.SetOutput(devNull{})
	_ = fs.Parse(argv[1:])

	any := *install || *uninstall || *startSvc || *stopSvc || *statusSvc
	if !any && service.Interactive() {
		// Interactive run: fall through to the regular main.
		return false, nil
	}

	// Build a service for either the action commands or the SCM dispatch.
	// Forwarded args = original argv stripped of -install / -uninstall /
	// -start / -stop / -service-status. They are restored when the SCM
	// invokes us so the service still sees all peershd-specific flags
	// (-listen, -signaling, etc.).
	forwarded := stripServiceFlags(argv[1:])
	svc, sErr := service.New(&program{args: forwarded}, serviceConfig(forwarded))
	if sErr != nil {
		return true, fmt.Errorf("service.New: %w", sErr)
	}

	switch {
	case *install:
		if err := ensureRunnableExePath(); err != nil {
			return true, err
		}
		if err := svc.Install(); err != nil {
			return true, fmt.Errorf("service.Install: %w", err)
		}
		fmt.Println("peershd service installed.")
		return true, nil
	case *uninstall:
		if err := svc.Uninstall(); err != nil {
			return true, fmt.Errorf("service.Uninstall: %w", err)
		}
		fmt.Println("peershd service uninstalled.")
		return true, nil
	case *startSvc:
		if err := svc.Start(); err != nil {
			return true, fmt.Errorf("service.Start: %w", err)
		}
		fmt.Println("peershd service started.")
		return true, nil
	case *stopSvc:
		if err := svc.Stop(); err != nil {
			return true, fmt.Errorf("service.Stop: %w", err)
		}
		fmt.Println("peershd service stopped.")
		return true, nil
	case *statusSvc:
		st, err := svc.Status()
		if err != nil {
			return true, err
		}
		fmt.Printf("peershd service status: %s\n", svcStatusString(st))
		return true, nil
	}

	// Non-interactive (SCM dispatch). Run the program until the SCM
	// stops us.
	if err := svc.Run(); err != nil {
		return true, fmt.Errorf("service.Run: %w", err)
	}
	return true, nil
}

func ensureRunnableExePath() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("peershd executable not found at %s: %w", abs, err)
	}
	// Don't install the binary running from %TEMP% / a transient location.
	if strings.Contains(strings.ToLower(abs), `\temp\`) {
		return errors.New("peershd: refusing to install a binary running from %TEMP%; copy it to a stable path first")
	}
	return nil
}

func svcStatusString(st service.Status) string {
	switch st {
	case service.StatusRunning:
		return "running"
	case service.StatusStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

func stripServiceFlags(args []string) []string {
	out := make([]string, 0, len(args))
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		switch a {
		case "-install", "--install",
			"-uninstall", "--uninstall",
			"-start", "--start",
			"-stop", "--stop",
			"-service-status", "--service-status":
			continue
		}
		out = append(out, a)
	}
	_ = exec.Command // keep imports stable across edits
	return out
}

type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
