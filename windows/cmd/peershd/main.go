// Command peershd is the peersh Windows host.
//
// Two operating modes coexist:
//
//   - Direct (Phase 1). The host accepts QUIC dials on -listen. The peer
//     must already know the address. No auth.
//
//   - Signaling-mediated (Phase 2). The host registers with a signaling
//     server, waits for incoming Connect messages from peers under the
//     same PSK user_id, replies with its own local candidates, and the
//     peer dials the existing QUIC listener.
//
// Both modes share the same QUIC listener; the signaling integration is
// purely a discovery / address-exchange overlay.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/peersh/peersh/core/devid"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/core/punching"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/peertls"
	"github.com/peersh/peersh/core/wire"
	"sync"

	fbpeershd "github.com/peersh/peersh/windows/firebase"
	"github.com/peersh/peersh/windows/peerauth"
	"github.com/peersh/peersh/windows/ptyhost"
	"github.com/peersh/peersh/windows/pwsh"
)

const protocolVersion = 2

const (
	defaultDirectListen    = "127.0.0.1:7777"
	defaultSignalingListen = ":7777"
)

// supportedCapabilities is the capability list peershd advertises in
// ServerHello. "pty.v1" tells the client it may open StreamRequest{pty: ...}
// streams; "files.v1" enables the Tier 2 session-scoped file API.
var supportedCapabilities = []string{"pty.v1", "files.v1"}

func main() {
	// Phase 7: detect Windows-Service install / uninstall / SCM-dispatch
	// modes first. runService returns handled=true when the binary
	// performed (or is performing) a service action, in which case main
	// should exit here.
	handled, err := runService(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if handled {
		return
	}

	taskHandled, err := runLogonTask(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if taskHandled {
		return
	}

	// Subcommands that don't run the daemon. Single-token entry points
	// to keep flags simple: `peershd version`, `peershd update -check`.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "version", "-version", "--version":
			versionCommand()
			return
		case "update":
			if err := runUpdate(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}

	if err := runWithCtx(nil, os.Args[1:]); err != nil {
		slog.Error("peershd exiting on error", "err", err)
		os.Exit(1)
	}
}

// runWithCtx is the body of the original main(). It is called both by
// the interactive entry point (with ctx == nil; signal.NotifyContext
// produces its own) and by the Windows Service program.Start handler
// (with a context that the SCM cancels at Stop time).
func runWithCtx(serviceCtx context.Context, args []string) error {
	fs := flag.NewFlagSet("peershd", flag.ExitOnError)
	listen := fs.String("listen", defaultDirectListen, "UDP address to listen on for QUIC; defaults to loopback without signaling, or :7777 when -signaling is set")
	insecureDirect := fs.Bool("insecure-direct", false, "explicitly allow binding a non-loopback -listen without -signaling; without this flag, non-loopback binds require signaling to be enabled so peershd is not a public no-auth shell")
	certDir := fs.String("cert-dir", "", "directory for self-signed dev cert (default: platform-specific app data dir)")
	debug := fs.Bool("debug", false, "enable debug logging")
	defaultRegion := embeddedFirebaseRegion
	if defaultRegion == "" {
		defaultRegion = "asia-northeast1"
	}
	signalingURL := fs.String("signaling", embeddedSignalingURL, "signaling server URL (ws:// or wss://); empty disables signaling")
	userID := fs.String("user", "", "user_id under which to register (PSK signaling mode)")
	pskFile := fs.String("psk-file", "", "path to a file containing a hex-encoded PSK (PSK signaling mode)")
	displayName := fs.String("display-name", "", "display name to register (defaults to hostname)")
	stunServer := fs.String("stun", punching.DefaultSTUNServer, "STUN server for srflx discovery; empty disables STUN")
	firebaseProjectID := fs.String("firebase-project", embeddedFirebaseProjectID, "Firebase project id (Firebase signaling mode)")
	firebaseCredentials := fs.String("firebase-credentials", "", "path to Firebase service-account JSON (advanced; the pairing flow does not need this)")
	firebaseEmail := fs.String("firebase-email", "", "email of the Firebase account this peershd registers under (with -firebase-credentials); resolved to uid via Admin SDK")
	firebaseUID := fs.String("firebase-uid", "", "explicit Firebase uid (alternative to -firebase-email)")
	firebaseAPIKey := fs.String("firebase-api-key", embeddedFirebaseAPIKey, "Firebase Web API key for signInWithCustomToken / token refresh (Firebase signaling mode)")
	firebaseRegion := fs.String("firebase-region", defaultRegion, "region of the deployed Cloud Functions (claimPairingCode)")
	defaultRtdbRegion := embeddedFirebaseRtdbRegion
	if defaultRtdbRegion == "" {
		defaultRtdbRegion = "asia-southeast1"
	}
	firebaseRtdbRegion := fs.String("firebase-rtdb-region", defaultRtdbRegion, "region of the Firebase Realtime Database instance hosting wake_requests / devices")
	firebasePairCode := fs.String("pair-code", "", "one-time 6-digit pairing code shown by the mobile app's Pair PC screen; consumed once and replaced by a persisted refresh token")
	firebaseTokenFile := fs.String("firebase-token-file", "", "path to the persisted Firebase refresh token (default: %LOCALAPPDATA%\\peersh\\firebase-refresh-token.txt)")
	firebaseLogin := fs.Bool("firebase-login", false, "open the default browser to sign in with Google (one-shot bootstrap; replaces -pair-code on desktops with a browser)")
	googleClientID := fs.String("google-client-id", embeddedGoogleClientID, "OAuth 2.0 'Desktop app' client id (required with -firebase-login)")
	googleClientSecret := fs.String("google-client-secret", embeddedGoogleClientSecret, "OAuth 2.0 'Desktop app' client secret (required with -firebase-login)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if *certDir == "" {
		*certDir = defaultCertDir()
	}
	listenExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			listenExplicit = true
		}
	})

	return run(serviceCtx, runOpts{
		listen:              *listen,
		listenExplicit:      listenExplicit,
		insecureDirect:      *insecureDirect,
		certDir:             *certDir,
		signalingURL:        *signalingURL,
		userID:              *userID,
		pskFile:             *pskFile,
		displayName:         *displayName,
		stunServer:          *stunServer,
		firebaseProjectID:   *firebaseProjectID,
		firebaseCredentials: *firebaseCredentials,
		firebaseEmail:       *firebaseEmail,
		firebaseUID:         *firebaseUID,
		firebaseAPIKey:      *firebaseAPIKey,
		firebaseRegion:      *firebaseRegion,
		firebaseRtdbRegion:  *firebaseRtdbRegion,
		firebasePairCode:    *firebasePairCode,
		firebaseTokenFile:   *firebaseTokenFile,
		firebaseLogin:       *firebaseLogin,
		googleClientID:      *googleClientID,
		googleClientSecret:  *googleClientSecret,
	})
}

type runOpts struct {
	listen, certDir, signalingURL  string
	listenExplicit, insecureDirect bool
	userID, pskFile                string
	displayName, stunServer        string
	firebaseProjectID              string
	firebaseCredentials            string
	firebaseEmail                  string
	firebaseUID                    string
	firebaseAPIKey                 string
	firebaseRegion                 string
	firebaseRtdbRegion             string
	firebasePairCode               string
	firebaseTokenFile              string
	firebaseLogin                  bool
	googleClientID                 string
	googleClientSecret             string
}

func effectiveListen(listen, signalingURL string, listenExplicit bool) string {
	if signalingURL != "" && !listenExplicit && listen == defaultDirectListen {
		return defaultSignalingListen
	}
	return listen
}

// isLoopbackBind returns true when udpAddr resolves to a loopback IP.
// "Unspecified" (0.0.0.0 / ::) does not count: an unspecified bind is
// reachable on every interface, which is exactly what direct mode must
// not do without -insecure-direct.
func isLoopbackBind(udpAddr *net.UDPAddr) bool {
	if udpAddr == nil || udpAddr.IP == nil {
		return false
	}
	if udpAddr.IP.IsUnspecified() {
		return false
	}
	return udpAddr.IP.IsLoopback()
}

func defaultCertDir() string {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "peersh", "dev")
		}
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "peersh", "dev")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "peersh", "dev")
	}
	return filepath.Join(".", "peersh-dev")
}

func run(serviceCtx context.Context, opts runOpts) error {
	listen := opts.listen
	certDir := opts.certDir
	signalingURL := opts.signalingURL
	userID := opts.userID
	pskFile := opts.pskFile
	displayName := opts.displayName
	stunServer := opts.stunServer
	var ctx context.Context
	var stop func()
	if serviceCtx != nil {
		// Running under SCM: the parent owns the cancellation. Wrap it
		// so we can still observe the OS signals interactively, but
		// SCM Stop is the primary trigger.
		ctx = serviceCtx
		stop = func() {}
	} else {
		ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
	defer stop()
	listen = effectiveListen(listen, signalingURL, opts.listenExplicit)

	priv, err := peertls.LoadOrGenerateKey(certDir)
	if err != nil {
		return fmt.Errorf("load device key: %w", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	cert, err := peertls.CertFromEd25519(priv)
	if err != nil {
		return fmt.Errorf("build cert: %w", err)
	}
	deviceID := devid.Derive(pub)
	slog.Info("device key ready", "dir", certDir, "device_id", deviceID)

	udpAddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return fmt.Errorf("resolve listen %q: %w", listen, err)
	}

	// Direct (no-signaling) mode has no signaling channel to authorize
	// the peer (peerauth runs nil; serveConn skips the check). Refuse to
	// expose that combination on a non-loopback bind unless the operator
	// explicitly opted in with -insecure-direct, so a default install
	// does not become a public no-auth shell.
	if signalingURL == "" && !isLoopbackBind(udpAddr) && !opts.insecureDirect {
		return fmt.Errorf("refusing to bind non-loopback -listen %q without -signaling: peershd would be reachable from the network with no signaling-issued authorization. Either enable signaling, change -listen to a loopback address, or pass -insecure-direct if you really mean it", listen)
	}
	if opts.insecureDirect && signalingURL == "" && !isLoopbackBind(udpAddr) {
		slog.Warn("INSECURE-DIRECT MODE: peershd is reachable from the network with no signaling authorization; any client presenting a valid ed25519 cert will get a shell", "listen", listen)
	}

	pc, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp %q: %w", listen, err)
	}
	defer pc.Close()
	listenAddr := pc.LocalAddr().(*net.UDPAddr)
	slog.Info("listening for QUIC", "addr", listenAddr)

	// STUN runs BEFORE Transport.New takes over reads on pc. The discovered
	// srflx is cached and emitted as a SERVER_REFLEXIVE candidate on every
	// Connect reply. For cone NATs (the common case) one srflx port works
	// for any peer destination; symmetric NATs are the documented fail case.
	var srflx *net.UDPAddr
	if signalingURL != "" && stunServer != "" {
		stunCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		srflx, err = punching.Discover(stunCtx, pc, punching.Options{STUNServer: stunServer})
		cancel()
		if err != nil {
			slog.Warn("stun discover failed; continuing without srflx candidate", "err", err)
		}
	}

	tr := transport.New(pc, peertls.ServerTLSConfig(cert))
	defer tr.Close()
	listener, err := tr.Listen(ctx)
	if err != nil {
		return fmt.Errorf("transport.Listen: %w", err)
	}
	defer listener.Close()

	// Phase 6: SessionManager keeps pwsh.Host instances alive across
	// QUIC reconnects so a client presenting a known session_id resumes
	// where it left off (cwd, variables intact). Idle sessions are
	// reaped after pwsh.DefaultIdleTimeout.
	mgr := pwsh.NewSessionManager()
	defer mgr.Close()
	go mgr.Run(ctx)

	// Authz bridges signaling Connect grants to the QUIC accept path.
	// In direct (no-signaling) mode authz stays nil; the accept loop then
	// only relies on peertls's pubkey-binding check, since there is no
	// signaling channel to carry membership policy. The 60-second TTL is
	// generous enough to cover NAT punching and a few dial retries while
	// still expiring abandoned grants.
	var authz *peerauth.Authz
	if signalingURL != "" {
		authz = peerauth.New(60 * time.Second)
		go authz.RunSweeper(ctx, 10*time.Second)

		if displayName == "" {
			displayName, _ = os.Hostname()
		}
		// Pick auth mode: PSK (default) or Firebase. Firebase mode
		// always uses the wake-listener path — Realtime Database SSE
		// keeps the signaling WebSocket closed except during a wake
		// window. The same path serves both pair-code (or browser
		// login) and service-account credentials, since both produce
		// Firebase ID tokens via the TokenSource interface.
		useFirebase := opts.firebaseCredentials != "" || opts.firebaseProjectID != "" || opts.firebaseEmail != "" || opts.firebaseUID != "" || opts.firebasePairCode != "" || opts.firebaseTokenFile != "" || opts.firebaseLogin
		if useFirebase {
			src, err := buildFirebaseTokenSource(ctx, opts)
			if err != nil {
				return err
			}
			rt, err := fbpeershd.StartWakeRuntime(ctx, opts.firebaseProjectID, opts.firebaseRtdbRegion, src, src.UID(), deviceID)
			if err != nil {
				return fmt.Errorf("start wake runtime: %w", err)
			}
			defer rt.Close()
			go runHeartbeat(ctx, rt)
			go runWakePump(ctx, rt, signalingURL, src, deviceID, pub, displayName, listenAddr, srflx, pc, authz)
		} else {
			if userID == "" || pskFile == "" {
				return errors.New("-signaling requires -user and -psk-file (PSK mode), or -pair-code + -firebase-project + -firebase-api-key (Firebase pairing mode)")
			}
			secret, err := readPSKFile(pskFile)
			if err != nil {
				return fmt.Errorf("read psk: %w", err)
			}
			go runSignaling(ctx, signalingURL, userID, secret, deviceID, pub, displayName, listenAddr, srflx, pc, authz)
		}
	}

	// Phase 1 QUIC accept loop.
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
				slog.Info("listener stopped", "reason", err)
				return nil
			}
			return fmt.Errorf("Accept: %w", err)
		}
		go serveConn(ctx, conn, mgr, authz)
	}
}

// runSignaling dials the signaling server, registers, and replies to
// incoming Connect messages with our local candidates. The actual data
// connection arrives separately at the QUIC listener.
//
// srflx, if non-nil, is included as a SERVER_REFLEXIVE candidate. pc is the
// shared QUIC UDP socket; punching writes to it concurrently with QUIC reads.
//
// authz is the bridge to the QUIC accept loop: every Connect that arrives
// is recorded as an "allowed to dial me right now" grant. The accept loop
// later consumes that grant when the matching mTLS handshake lands.
func runSignaling(ctx context.Context, url, userID string, secret []byte, deviceID string, pub ed25519.PublicKey, displayName string, listenAddr *net.UDPAddr, srflx *net.UDPAddr, pc net.PacketConn, authz *peerauth.Authz) {
	log := slog.With("signaling", url, "user", userID, "device", deviceID)
	if srflx != nil {
		log.Info("srflx ready for advertisement", "srflx", srflx)
	}
	sc, err := signaling.Dial(ctx, signaling.DialOptions{
		URL:         url,
		UserID:      userID,
		Secret:      secret,
		DeviceID:    deviceID,
		PublicKey:   pub,
		Kind:        signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST,
		DisplayName: displayName,
		ClientID:    "peershd/0.1",
	})
	if err != nil {
		log.Error("signaling dial failed", "err", err)
		return
	}
	defer sc.Close()
	log.Info("registered with signaling server", "server_id", sc.ServerID())

	for {
		conn, err := sc.Recv(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Info("signaling closed", "err", err)
			return
		}
		from := conn.GetFromDeviceId()
		log.Info("connect request received", "from", from, "candidates", len(conn.GetCandidates()))

		// Authorize this device_id to land on the QUIC accept loop. The
		// grant is one-shot and TTL-bounded (see peerauth) so a peer
		// that fails to dial does not leave a lingering hole.
		if authz != nil {
			authz.Allow(from)
		}

		// Punch the peer's candidates first so our NAT installs the mapping
		// for their address before they QUIC-dial us. Sorted by preferred
		// order; we punch all of them cheaply (5 packets each).
		peerCands := punching.SortCandidates(conn.GetCandidates())
		if err := punching.Punch(ctx, pc, punching.CandidatesToUDPAddrs(peerCands), punching.Options{}); err != nil {
			log.Warn("punch failed", "err", err)
		}

		// Reply with our local candidates so the peer can dial us.
		cands := enumerateCandidates(listenAddr, srflx)
		if err := sc.SendConnect(ctx, from, cands); err != nil {
			log.Warn("send Connect reply", "err", err)
			continue
		}
		log.Info("sent local candidates", "to", from, "count", len(cands))
	}
}

// buildFirebaseTokenSource picks the right Firebase auth backend for the
// invocation flags:
//
//  1. -firebase-login given: open browser, run OAuth 2.0 + signInWithIdp,
//     persist refresh token. One-shot bootstrap (desktop only).
//  2. -pair-code given: claim it, exchange for refresh token, persist.
//     One-shot bootstrap (works on headless hosts).
//  3. -firebase-credentials given: legacy service-account-JSON path.
//  4. Otherwise: load the persisted refresh token (path defaults to
//     LOCALAPPDATA\peersh\firebase-refresh-token.txt).
//
// All Firebase paths require -firebase-project and -firebase-api-key.
func buildFirebaseTokenSource(ctx context.Context, opts runOpts) (fbpeershd.TokenSource, error) {
	if opts.firebaseAPIKey == "" || opts.firebaseProjectID == "" {
		return nil, errors.New("firebase mode requires -firebase-project and -firebase-api-key")
	}
	tokenPath := opts.firebaseTokenFile
	if tokenPath == "" {
		tokenPath = fbpeershd.DefaultRefreshTokenPath()
	}

	if opts.firebaseLogin {
		slog.Info("starting browser-based Google sign-in", "project", opts.firebaseProjectID, "token_file", tokenPath)
		src, err := fbpeershd.GoogleSignIn(ctx, opts.googleClientID, opts.googleClientSecret, opts.firebaseAPIKey, tokenPath, nil)
		if err != nil {
			return nil, fmt.Errorf("firebase login: %w", err)
		}
		slog.Info("Google sign-in complete", "uid", src.UID())
		return src, nil
	}

	if opts.firebasePairCode != "" {
		slog.Info("pairing peershd with Firebase", "project", opts.firebaseProjectID, "token_file", tokenPath)
		src, err := fbpeershd.Pair(ctx, opts.firebaseProjectID, opts.firebaseRegion, opts.firebaseAPIKey, opts.firebasePairCode, tokenPath)
		if err != nil {
			return nil, fmt.Errorf("pair: %w", err)
		}
		return src, nil
	}

	if opts.firebaseCredentials != "" {
		if opts.firebaseEmail == "" && opts.firebaseUID == "" {
			return nil, errors.New("-firebase-credentials requires one of -firebase-email or -firebase-uid")
		}
		src, err := fbpeershd.New(ctx, opts.firebaseProjectID, opts.firebaseCredentials, opts.firebaseEmail, opts.firebaseUID, opts.firebaseAPIKey)
		if err != nil {
			return nil, fmt.Errorf("firebase auth source: %w", err)
		}
		return src, nil
	}

	src, err := fbpeershd.NewRefreshSource(tokenPath, opts.firebaseAPIKey)
	if err != nil {
		return nil, fmt.Errorf("load persisted refresh token (run with -firebase-login or -pair-code first): %w", err)
	}
	slog.Info("loaded persisted Firebase refresh token", "token_file", tokenPath)
	return src, nil
}

// Wake-pump tunables. SHORT_TTL caps how long a freshly opened WS waits
// for the first Connect; DRAIN_TTL is the grace window after each
// Connect during which we keep the WS open for additional wakes and
// for the QUIC handshake to complete.
const (
	wakeShortTTL = 15 * time.Second
	wakeDrainTTL = 5 * time.Second
	heartbeatEvery = 5 * time.Minute
)

// runHeartbeat refreshes users/{uid}/devices/{deviceId}.last_seen_at on
// a fixed interval so mobile clients can decide the host is reachable
// before issuing a wake request.
func runHeartbeat(ctx context.Context, rt *fbpeershd.Runtime) {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := rt.Heartbeat(ctx); err != nil {
				slog.Warn("device heartbeat failed", "err", err)
			}
		}
	}
}

// runWakePump consumes wake events from the Realtime Database listener
// and, for each batch of events, opens a short-lived signaling WS to
// receive the matching Connect frames. The WS closes after wakeShortTTL
// of idle (no Connect arriving) or wakeDrainTTL after the last Connect,
// leaving peershd in IDLE state with only the RTDB SSE stream open.
//
// Both pair-code and service-account modes use this single path; the
// only difference is which TokenSource implementation produces the
// Firebase ID token used to authenticate the RTDB stream.
func runWakePump(
	ctx context.Context,
	rt *fbpeershd.Runtime,
	signalingURL string,
	src fbpeershd.TokenSource,
	deviceID string,
	pub ed25519.PublicKey,
	displayName string,
	listenAddr *net.UDPAddr,
	srflx *net.UDPAddr,
	pc net.PacketConn,
	authz *peerauth.Authz,
) {
	log := slog.With("component", "wake-pump", "uid", src.UID(), "device", deviceID)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-rt.Events():
			if !ok {
				return
			}
			if err := handleWakeBatch(ctx, ev, rt, signalingURL, src, deviceID, pub, displayName, listenAddr, srflx, pc, authz, log); err != nil {
				log.Warn("wake batch failed", "err", err, "request", ev.RequestID)
			}
			drainWakeChannel(rt.Events())
		}
	}
}

func handleWakeBatch(
	ctx context.Context,
	first fbpeershd.WakeEvent,
	rt *fbpeershd.Runtime,
	signalingURL string,
	src fbpeershd.TokenSource,
	deviceID string,
	pub ed25519.PublicKey,
	displayName string,
	listenAddr *net.UDPAddr,
	srflx *net.UDPAddr,
	pc net.PacketConn,
	authz *peerauth.Authz,
	log *slog.Logger,
) error {
	log.Info("wake received; opening signaling WS", "request", first.RequestID, "from", first.MobileDeviceID)
	idToken, err := src.Token(ctx)
	if err != nil {
		return fmt.Errorf("mint id token: %w", err)
	}
	sc, err := signaling.Dial(ctx, signaling.DialOptions{
		URL:             signalingURL,
		FirebaseIDToken: idToken,
		DeviceID:        deviceID,
		PublicKey:       pub,
		Kind:            signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST,
		DisplayName:     displayName,
		ClientID:        "peershd/0.1+firebase-wake",
	})
	if err != nil {
		return fmt.Errorf("signaling dial: %w", err)
	}
	defer sc.Close()
	log.Info("registered with signaling server", "server_id", sc.ServerID())

	deadline := time.Now().Add(wakeShortTTL)
	for {
		timeout := time.Until(deadline)
		if timeout <= 0 {
			log.Info("wake window expired; closing WS")
			break
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		conn, recvErr := sc.Recv(rctx)
		cancel()
		if recvErr != nil {
			if errors.Is(recvErr, context.DeadlineExceeded) {
				log.Info("signaling idle; closing WS")
				break
			}
			if errors.Is(recvErr, context.Canceled) {
				return nil
			}
			return fmt.Errorf("signaling recv: %w", recvErr)
		}
		from := conn.GetFromDeviceId()
		log.Info("connect request", "from", from, "candidates", len(conn.GetCandidates()))
		if authz != nil {
			authz.Allow(from)
		}
		peerCands := punching.SortCandidates(conn.GetCandidates())
		if err := punching.Punch(ctx, pc, punching.CandidatesToUDPAddrs(peerCands), punching.Options{}); err != nil {
			log.Warn("punch failed", "err", err)
		}
		cands := enumerateCandidates(listenAddr, srflx)
		if err := sc.SendConnect(ctx, from, cands); err != nil {
			log.Warn("send Connect reply", "err", err)
			continue
		}
		log.Info("sent local candidates", "to", from, "count", len(cands))
		if drain := time.Now().Add(wakeDrainTTL); drain.After(deadline) {
			deadline = drain
		}
	}

	if err := rt.DeleteWakeRequest(ctx, first.RequestID); err != nil {
		log.Warn("wake_request delete failed", "err", err, "request", first.RequestID)
	}
	return nil
}

func drainWakeChannel(ch <-chan fbpeershd.WakeEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// readPSKFile reads a hex-encoded PSK from disk. Whitespace is trimmed.
func readPSKFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(raw))
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("psk file %q: %w", path, err)
	}
	return out, nil
}

// enumerateCandidates returns the candidate list peershd advertises in its
// Connect reply. Order:
//   - SRFLX (if STUN succeeded), one entry.
//   - HOST candidates: either the bound IP, or every non-loopback /
//     non-link-local interface IP.
//
// SortCandidates on the receiver side reshuffles by IPv6/IPv4 within
// SRFLX/HOST; ordering here is purely for log readability.
func enumerateCandidates(listen *net.UDPAddr, srflx *net.UDPAddr) []*signalv1.EndpointCandidate {
	port := uint32(listen.Port)
	var out []*signalv1.EndpointCandidate
	if srflx != nil {
		out = append(out, &signalv1.EndpointCandidate{
			Address: srflx.IP.String(), Port: uint32(srflx.Port),
			Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE,
		})
	}
	if !listen.IP.IsUnspecified() {
		out = append(out, &signalv1.EndpointCandidate{
			Address: listen.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST,
		})
		return out
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() || ipnet.IP.IsLinkLocalMulticast() {
			continue
		}
		out = append(out, &signalv1.EndpointCandidate{
			Address: ipnet.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST,
		})
	}
	return out
}

// serveConn handles one QUIC connection: Hello (with optional reattach)
// on the control stream, then a fresh per-command stream for each
// ExecRequest. The host's pwsh process is owned by mgr, not by this
// function — when the QUIC connection closes, the session is detached
// (mgr's idle timer takes over) instead of killed.
func serveConn(ctx context.Context, conn *transport.Conn, mgr *pwsh.SessionManager, authz *peerauth.Authz) {
	remote := conn.RemoteAddr().String()
	peerID := peertls.PeerDeviceID(conn.TLSState())
	log := slog.With("peer", remote, "peer_device_id", peerID)
	log.Info("connection accepted")
	defer func() {
		_ = conn.CloseWithError(0, "")
		log.Info("connection closed")
	}()

	// authz is nil in direct (no-signaling) mode, where there is no
	// signaling channel to issue grants. Production deployments always
	// run with signaling enabled and the check is mandatory there.
	if authz != nil {
		if peerID == "" {
			log.Warn("rejecting peer without ed25519 cert")
			return
		}
		if !authz.Check(peerID) {
			log.Warn("rejecting unauthorized peer device_id")
			return
		}
	}

	ctrl, err := conn.AcceptStream(ctx)
	if err != nil {
		log.Warn("AcceptStream(control): connection ending", "err", err)
		return
	}

	sessionID, host, err := doHandshake(ctx, ctrl, mgr)
	if err != nil {
		log.Warn("handshake failed", "err", err)
		return
	}
	defer mgr.Detach(sessionID)
	log.Info("handshake complete", "session", sessionID, "pwsh", host.Path())

	registry := newPTYRegistry()
	ptyMgr := ptyhost.NewManager()
	defer ptyMgr.Close()
	go runPTYSweeper(ctx, ptyMgr)
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Info("AcceptStream: end of connection", "err", err)
			return
		}
		go serveStream(ctx, host, stream, registry, ptyMgr, log)
	}
}

func runPTYSweeper(ctx context.Context, mgr *ptyhost.Manager) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			mgr.Sweep()
		}
	}
}

// serveStream dispatches a per-stream first frame (StreamRequest) to either
// the one-shot Exec path or the interactive PTY path. Each stream owns its
// own protocol; this function returns when the stream closes.
func serveStream(ctx context.Context, host *pwsh.Host, stream *transport.Stream, reg *ptyRegistry, mgr *ptyhost.Manager, log *slog.Logger) {
	defer stream.Close()
	r := wire.NewReader(stream)
	req := &v1.StreamRequest{}
	if err := wire.Read(r, req); err != nil {
		log.Warn("read StreamRequest", "err", err)
		return
	}
	switch kind := req.GetKind().(type) {
	case *v1.StreamRequest_Exec:
		serveExecStream(ctx, host, stream, kind.Exec, log)
	case *v1.StreamRequest_Pty:
		servePTYStream(ctx, stream, r, kind.Pty, reg, mgr, log)
	case *v1.StreamRequest_Files:
		serveFilesStream(stream, kind.Files, reg, mgr)
	default:
		log.Warn("StreamRequest with no kind set")
	}
}

func servePTYStream(ctx context.Context, stream *transport.Stream, r *bufio.Reader, req *v1.PTYRequest, reg *ptyRegistry, mgr *ptyhost.Manager, log *slog.Logger) {
	clog := log.With("stream", stream.StreamID(), "pty_cmd", req.GetCommand(), "pty_id", req.GetPtyId(), "reattach", req.GetReattachHandle() != "")
	cols := uint16(req.GetCols())
	rows := uint16(req.GetRows())

	var (
		sess   *ptyhost.Session
		handle ptyhost.ManagedHandle
		replay []byte
	)

	if h := req.GetReattachHandle(); h != "" {
		// Reattach path.
		s, snap, alreadyAttached, err := mgr.Attach(ptyhost.ManagedHandle(h))
		if err != nil {
			clog.Info("reattach rejected: unknown handle", "err", err)
			_ = wire.Write(stream, &v1.PTYFrame{Kind: &v1.PTYFrame_ReattachAck{ReattachAck: &v1.PTYReattachAck{
				Handle: h, Accepted: false, Reason: err.Error(),
			}}})
			return
		}
		if alreadyAttached {
			clog.Info("reattach rejected: already attached")
			_ = wire.Write(stream, &v1.PTYFrame{Kind: &v1.PTYFrame_ReattachAck{ReattachAck: &v1.PTYReattachAck{
				Handle: h, Accepted: false, Reason: "another client is currently attached",
			}}})
			return
		}
		sess = s
		handle = ptyhost.ManagedHandle(h)
		replay = snap
		if cols > 0 && rows > 0 {
			_ = sess.Resize(cols, rows)
		}
		clog.Info("pty reattached", "cols", cols, "rows", rows, "replay_bytes", len(replay))
	} else {
		// Fresh open.
		clog.Info("pty open", "cols", cols, "rows", rows)
		s, err := ptyhost.Open(req.GetCommand(), req.GetArgs(), cols, rows)
		if err != nil {
			clog.Warn("ptyhost.Open failed", "err", err)
			_ = wire.Write(stream, &v1.PTYFrame{Kind: &v1.PTYFrame_Exit{Exit: &v1.PTYExit{ExitCode: -1, Error: err.Error()}}})
			return
		}
		sess = s
		commandLabel := req.GetCommand()
		if commandLabel == "" {
			commandLabel = "auto"
		}
		handle = mgr.Register(sess, commandLabel)
	}

	// Stream-scoped registration in the per-connection PTY id registry
	// so the file API can reach this Session.
	reg.Register(req.GetPtyId(), sess)
	defer reg.Unregister(req.GetPtyId())

	// Always send a PTYReattachAck as the first server-bound frame so
	// the client learns the handle (whether fresh or reattached).
	_ = wire.Write(stream, &v1.PTYFrame{Kind: &v1.PTYFrame_ReattachAck{ReattachAck: &v1.PTYReattachAck{
		Handle: string(handle), Accepted: true,
	}}})
	if len(replay) > 0 {
		_ = wire.Write(stream, &v1.PTYFrame{Kind: &v1.PTYFrame_Data{Data: &v1.PTYData{Data: replay}}})
	}

	pumpCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// On stream close, detach from the Manager (PTY survives idle TTL).
	// The Session is NOT closed here — Manager.Sweep / Drop is the
	// authoritative way to terminate it.
	defer mgr.Detach(handle)

	var writeMu sync.Mutex
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		sess.Pump(pumpCtx, func(f *v1.PTYFrame) error {
			// Append to ring buffer alongside wire write so reattach
			// replay is byte-identical to what the client just saw.
			if data := f.GetData(); data != nil {
				mgr.Append(handle, data.GetData())
			}
			writeMu.Lock()
			defer writeMu.Unlock()
			return wire.Write(stream, f)
		})
	}()

	for {
		frame := &v1.PTYFrame{}
		if err := wire.Read(r, frame); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				clog.Info("pty read frame", "err", err)
			}
			break
		}
		switch k := frame.GetKind().(type) {
		case *v1.PTYFrame_Input:
			if _, err := sess.Write(k.Input.GetData()); err != nil {
				clog.Info("pty write to child", "err", err)
				break
			}
		case *v1.PTYFrame_Resize:
			if err := sess.Resize(uint16(k.Resize.GetCols()), uint16(k.Resize.GetRows())); err != nil {
				clog.Info("pty resize", "err", err)
			}
		default:
			// Server-bound frames (Data / Exit / ReattachAck) on the
			// input direction are protocol violations; ignore.
		}
	}

	cancel()
	<-pumpDone
	clog.Info("pty stream closed")
}

func doHandshake(ctx context.Context, ctrl *transport.Stream, mgr *pwsh.SessionManager) (string, *pwsh.Host, error) {
	r := wire.NewReader(ctrl)
	hello := &v1.ClientHello{}
	if err := wire.Read(r, hello); err != nil {
		return "", nil, fmt.Errorf("read ClientHello: %w", err)
	}
	if hello.GetProtocolVersion() != protocolVersion {
		_ = wire.Write(ctrl, &v1.ServerHello{ProtocolVersion: protocolVersion, ServerId: "peershd/0.1"})
		return "", nil, fmt.Errorf("client protocol_version=%d, server expects %d",
			hello.GetProtocolVersion(), protocolVersion)
	}
	id, host, reattached, err := mgr.AttachOrCreate(ctx, hello.GetSessionId())
	if err != nil {
		return "", nil, fmt.Errorf("AttachOrCreate: %w", err)
	}
	if err := wire.Write(ctrl, &v1.ServerHello{
		ProtocolVersion: protocolVersion,
		Capabilities:    supportedCapabilities,
		ServerId:        "peershd/0.1",
		SessionId:       id,
		Reattached:      reattached,
	}); err != nil {
		return "", nil, fmt.Errorf("write ServerHello: %w", err)
	}
	return id, host, nil
}

func serveExecStream(ctx context.Context, host *pwsh.Host, stream *transport.Stream, req *v1.ExecRequest, log *slog.Logger) {
	cmd := req.GetCommand()
	clog := log.With("stream", stream.StreamID(), "cmd_len", len(cmd))
	clog.Info("exec received")

	out, err := host.Exec(ctx, cmd)
	if err != nil {
		clog.Warn("pwsh Exec error", "err", err)
		return
	}

	for {
		c, err := out.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			clog.Warn("Output.Recv error", "err", err)
			return
		}
		resp := &v1.ExecResponse{}
		if c.Stream == pwsh.Stderr {
			resp.Chunk = &v1.ExecResponse_Stderr{Stderr: c.Data}
		} else {
			resp.Chunk = &v1.ExecResponse_Stdout{Stdout: c.Data}
		}
		if err := wire.Write(stream, resp); err != nil {
			clog.Warn("write ExecResponse chunk", "err", err)
			return
		}
	}
	if err := wire.Write(stream, &v1.ExecResponse{Done: true}); err != nil {
		clog.Warn("write ExecResponse done", "err", err)
		return
	}
	clog.Info("exec done")
}
