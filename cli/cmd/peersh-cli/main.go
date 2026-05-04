// Command peersh-cli is a developer REPL client for peersh.
//
// Two operating modes coexist:
//
//   - Direct (Phase 1). Pass -addr <host:port> and the CLI dials QUIC
//     straight at that endpoint. No auth, no signaling.
//
//   - Signaling-mediated (Phase 2). Pass -signaling, -user, -psk-file,
//     and -target. The CLI registers with the signaling server, requests
//     a Connect to the target device, learns the host's candidates, and
//     dials QUIC at the first reachable one.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/peersh/peersh/core/devid"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/punching"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/peertls"
	"github.com/peersh/peersh/core/wire"
)

const (
	protocolVersion = 2
	clientID        = "peersh-cli/0.2"
)

// embeddedSignalingURL is set at build time via -ldflags
// "-X main.embeddedSignalingURL=wss://...". When non-empty and the
// operator passes neither -signaling nor -addr, it becomes the
// default for -signaling so the daily `peersh-cli -user ... -psk-file
// ... -target ... -pty` invocation doesn't need to repeat the URL.
// Empty by default; the dev workflow keeps -signaling explicit.
var embeddedSignalingURL string

// Embedded Firebase defaults populated by build-peersh-cli.{cmd,sh}
// from local/peershd-build.env so the CLI shares the same Firebase
// project as peershd without a second config file. Empty defaults
// keep the dev / from-source workflow unchanged (operator must pass
// the flags explicitly).
var (
	embeddedFirebaseProjectID   string
	embeddedFirebaseAPIKey      string
	embeddedFirebaseRegion      string
	embeddedFirebaseRtdbRegion  string
	embeddedGoogleClientID      string
	embeddedGoogleClientSecret  string
)

func main() {
	addr := flag.String("addr", "", "(direct mode) peershd host (host:port)")
	hostDevice := flag.String("host-device", "", "(direct mode, optional) expected peershd device_id to pin at the TLS layer")

	signalingURL := flag.String("signaling", "", "(signaling mode) signaling server URL (ws:// or wss://)")
	userID := flag.String("user", "", "(signaling, PSK mode) user_id under which to register; ignored in Firebase mode (uid resolved from token)")
	pskFile := flag.String("psk-file", "", "(signaling, PSK mode) path to a file containing a hex-encoded PSK")
	target := flag.String("target", "", "(signaling mode) target peershd device_id to connect to")
	stunServer := flag.String("stun", punching.DefaultSTUNServer, "STUN server for srflx discovery; empty disables STUN")
	keyDir := flag.String("key-dir", "", "directory holding the CLI's persistent ed25519 device key; empty = generate a fresh ephemeral key on every run")

	firebaseProjectID := flag.String("firebase-project", embeddedFirebaseProjectID, "(signaling, Firebase mode) Firebase project id")
	firebaseAPIKey := flag.String("firebase-api-key", embeddedFirebaseAPIKey, "(signaling, Firebase mode) Firebase Web API key for signInWithCustomToken / token refresh")
	firebaseRegion := flag.String("firebase-region", embeddedFirebaseRegion, "(signaling, Firebase mode) Firebase / Cloud Functions region for the pair-code endpoint")
	firebaseRtdbRegion := flag.String("firebase-rtdb-region", embeddedFirebaseRtdbRegion, "(signaling, Firebase mode) Realtime Database region used to discover registered hosts when -target is empty")
	firebaseTokenFile := flag.String("firebase-token-file", "", "(signaling, Firebase mode) path to the persisted Firebase refresh token (default: %LOCALAPPDATA%\\peersh\\firebase-refresh-token.txt or ~/.local/share/peersh/...)")
	firebaseLogin := flag.Bool("firebase-login", false, "(signaling, Firebase mode) open the default browser to sign in with Google and persist a refresh token (one-shot bootstrap)")
	firebaseLoginOnly := flag.Bool("firebase-login-only", false, "(signaling, Firebase mode) exit after persisting the refresh token, without dialling peershd; pair with -firebase-login")
	firebasePairCode := flag.String("pair-code", "", "(signaling, Firebase mode) one-time 6-digit pairing code; consumed once and replaced by a persisted refresh token")
	firebaseCredentials := flag.String("firebase-credentials", "", "(signaling, Firebase mode) path to Firebase service-account JSON (advanced)")
	firebaseEmail := flag.String("firebase-email", "", "email of the Firebase account to authenticate as (with -firebase-credentials)")
	firebaseUID := flag.String("firebase-uid", "", "explicit Firebase uid (alternative to -firebase-email)")
	googleClientID := flag.String("google-client-id", embeddedGoogleClientID, "(signaling, Firebase mode) OAuth 2.0 'Desktop app' client id (required with -firebase-login)")
	googleClientSecret := flag.String("google-client-secret", embeddedGoogleClientSecret, "(signaling, Firebase mode) OAuth 2.0 'Desktop app' client secret (required with -firebase-login)")

	ptyMode := flag.Bool("pty", false, "open an interactive PTY instead of the one-shot REPL (Phase 8 Tier 1). Default behaviour: list persisted PTYs and prompt; combine with -pty-new / -pty-reattach / -pty-list to skip the picker.")
	ptyCmd := flag.String("pty-cmd", "", "executable to spawn under a fresh PTY; empty = operator-default shell. Ignored when reattaching to an existing handle.")
	ptyNew := flag.Bool("pty-new", false, "always spawn a fresh PTY without showing the picker (back-compat with the pre-multi-attach behaviour)")
	ptyReattach := flag.String("pty-reattach", "", "directly reattach to this server-issued handle without showing the picker")
	ptyList := flag.Bool("pty-list", false, "print persisted PTYs and exit (script-friendly; implies -pty)")

	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	// Fall back to the build-time embedded signaling URL only when the
	// operator passed neither -signaling nor -addr. Explicit -addr
	// (direct mode) still wins; explicit -signaling overrides the
	// embedded value.
	if *signalingURL == "" && *addr == "" && embeddedSignalingURL != "" {
		*signalingURL = embeddedSignalingURL
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if (*addr == "") == (*signalingURL == "") {
		fmt.Fprintln(os.Stderr, "exactly one of -addr or -signaling must be supplied")
		os.Exit(2)
	}
	if *hostDevice != "" && *signalingURL != "" {
		fmt.Fprintln(os.Stderr, "-host-device only applies to direct mode (-addr); use -target in signaling mode")
		os.Exit(2)
	}
	// -pty-list / -pty-reattach / -pty-new all imply -pty. Promote here so
	// the user doesn't have to pass both.
	if *ptyList || *ptyReattach != "" || *ptyNew {
		*ptyMode = true
	}
	// -pty-new and -pty-reattach are mutually exclusive — picking one
	// means "skip the picker", picking both is a contradiction.
	if *ptyNew && *ptyReattach != "" {
		fmt.Fprintln(os.Stderr, "-pty-new and -pty-reattach are mutually exclusive")
		os.Exit(2)
	}

	ptyOpts := ptyDispatch{
		Command:  *ptyCmd,
		New:      *ptyNew,
		Reattach: *ptyReattach,
		ListOnly: *ptyList,
	}

	fbOpts := firebaseOpts{
		ProjectID:          *firebaseProjectID,
		APIKey:             *firebaseAPIKey,
		Region:             *firebaseRegion,
		RtdbRegion:         *firebaseRtdbRegion,
		TokenFile:          *firebaseTokenFile,
		Login:              *firebaseLogin,
		PairCode:           *firebasePairCode,
		Credentials:        *firebaseCredentials,
		Email:              *firebaseEmail,
		UID:                *firebaseUID,
		GoogleClientID:     *googleClientID,
		GoogleClientSecret: *googleClientSecret,
	}

	// Pick auth mode for the signaling channel. PSK wins when -psk-file
	// is set (the operator explicitly chose it). Otherwise, if any
	// Firebase-related flag is set or the build embedded Firebase
	// defaults, go Firebase. Empty PSK + empty Firebase ⇒ direct mode
	// only, validated below by the -addr / -signaling check.
	useFirebase := *signalingURL != "" && *pskFile == "" && fbOpts.requested()

	if err := run(runOpts{
		Addr:              *addr,
		HostDevice:        *hostDevice,
		SignalingURL:      *signalingURL,
		UserID:            *userID,
		PskFile:           *pskFile,
		Target:            *target,
		StunServer:        *stunServer,
		KeyDir:            *keyDir,
		PtyMode:           *ptyMode,
		Pty:               ptyOpts,
		UseFirebase:       useFirebase,
		Firebase:          fbOpts,
		FirebaseLoginOnly: *firebaseLoginOnly,
	}); err != nil {
		slog.Error("peersh-cli exiting on error", "err", err)
		os.Exit(1)
	}
}

// runOpts bundles every flag value that survived parsing. Beats the
// 13-arg run() signature that grew out of the original two-mode CLI.
type runOpts struct {
	Addr              string
	HostDevice        string
	SignalingURL      string
	UserID            string
	PskFile           string
	Target            string
	StunServer        string
	KeyDir            string
	PtyMode           bool
	Pty               ptyDispatch
	UseFirebase       bool
	Firebase          firebaseOpts
	FirebaseLoginOnly bool
}

// ptyDispatch bundles the -pty-* flag values so the run / pty-mode
// branches don't drift from the flag parsing in main.
type ptyDispatch struct {
	Command  string
	New      bool
	Reattach string
	ListOnly bool
}

func run(opts runOpts) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Firebase login-only mode: bootstrap the refresh token and exit
	// before touching UDP / QUIC / signaling. Mirrors peershd's
	// -firebase-login-only so install scripts can do "first do the
	// browser dance, then enable the daily flow" without the CLI
	// trying to dial a target it doesn't have yet.
	if opts.UseFirebase && opts.FirebaseLoginOnly {
		src, err := buildCLIFirebaseTokenSource(ctx, opts.Firebase)
		if err != nil {
			return err
		}
		// Force one Token() call so the refresh exchange persists
		// even when the operator only set -firebase-credentials and
		// no token file is read upfront.
		if _, err := src.Token(ctx); err != nil {
			return fmt.Errorf("firebase token bootstrap: %w", err)
		}
		slog.Info("firebase login complete; exiting (firebase-login-only set)", "uid", src.UID())
		return nil
	}

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("ListenUDP: %w", err)
	}
	defer pc.Close()

	// In signaling mode, run STUN before Transport.New takes over reads.
	var srflx *net.UDPAddr
	if opts.SignalingURL != "" && opts.StunServer != "" {
		stunCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		srflx, err = punching.Discover(stunCtx, pc, punching.Options{STUNServer: opts.StunServer})
		cancel()
		if err != nil {
			slog.Warn("stun discover failed; continuing without srflx candidate", "err", err)
		}
	}

	// The CLI's ed25519 keypair drives both the signaling Register
	// frame (via devid.Derive on the pubkey) and the mTLS client cert
	// presented to peershd, so the server sees a consistent identity on
	// the QUIC and signaling sides. -key-dir gives the CLI a stable
	// device_id across runs (useful when peershd uses a per-client
	// allowlist); empty -key-dir keeps the historical ephemeral
	// behavior for ad-hoc / scripted invocations.
	priv, err := loadOrGenerateKey(opts.KeyDir)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	deviceID := devid.Derive(pub)
	clientCert, err := peertls.CertFromEd25519(priv)
	if err != nil {
		return fmt.Errorf("build client cert: %w", err)
	}

	if opts.SignalingURL != "" {
		var (
			secret  []byte
			idToken string
			target  = opts.Target
		)
		if opts.UseFirebase {
			src, err := buildCLIFirebaseTokenSource(ctx, opts.Firebase)
			if err != nil {
				return err
			}
			idToken, err = src.Token(ctx)
			if err != nil {
				return fmt.Errorf("firebase token: %w", err)
			}
			slog.Info("firebase token acquired", "uid", src.UID())
			// Auto-discover the host when -target is empty: list the
			// user's registered Windows hosts via Realtime Database
			// (same subtree the mobile app's DevicePickerSheet reads)
			// and pick the only one, or prompt when multiple.
			if target == "" {
				picked, err := pickFirebaseHost(ctx, src, opts.Firebase)
				if err != nil {
					return err
				}
				target = picked
			}
			// Wake-mode peershd keeps its signaling WS closed except
			// in response to a wake_request RTDB write. Fire one in
			// parallel with the upcoming Connect; the retry wrapper
			// inside negotiateConnectWithRetry covers the cold-start
			// race. Pair-code-mode peershd ignores the wake_request
			// (it already holds a persistent WS), so this is harmless
			// in that mode.
			fireWakeRequest(ctx, src, opts.Firebase.ProjectID, opts.Firebase.RtdbRegion, target)
		} else {
			if opts.UserID == "" || opts.PskFile == "" {
				return errors.New("-signaling requires -user + -psk-file (PSK mode), or any -firebase-* flag (Firebase mode)")
			}
			secret, err = readPSKFile(opts.PskFile)
			if err != nil {
				return fmt.Errorf("read psk: %w", err)
			}
		}
		if target == "" {
			return errors.New("-signaling requires -target")
		}
		// Signaling mode pins the (now-resolved) target device_id at
		// the TLS layer.
		tr := transport.New(pc, peertls.ClientTLSConfig(clientCert, target))
		defer tr.Close()
		conn, err := rendezvousAndDial(ctx, tr, pc, opts.SignalingURL, opts.UserID, secret, idToken, target, pc.LocalAddr().(*net.UDPAddr), srflx, pub, deviceID)
		if err != nil {
			return err
		}
		defer conn.CloseWithError(0, "")
		slog.Info("connected", "remote", conn.RemoteAddr())
		if err := doHandshake(ctx, conn); err != nil {
			return fmt.Errorf("handshake: %w", err)
		}
		if opts.PtyMode {
			return dispatchPTY(ctx, conn, opts.Pty)
		}
		return repl(ctx, conn)
	}

	// Direct mode (-addr) pins only if the operator passed -host-device;
	// otherwise we accept any pubkey-bound server cert, which is the
	// dev workflow where the host's device_id is not known ahead of
	// time.
	tr := transport.New(pc, peertls.ClientTLSConfig(clientCert, opts.HostDevice))
	defer tr.Close()
	dialAddr, err := net.ResolveUDPAddr("udp", opts.Addr)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", opts.Addr, err)
	}
	conn, err := tr.Dial(ctx, dialAddr)
	if err != nil {
		return fmt.Errorf("Dial %s: %w", dialAddr, err)
	}
	defer conn.CloseWithError(0, "")
	slog.Info("connected", "remote", dialAddr)
	if err := doHandshake(ctx, conn); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	if opts.PtyMode {
		return dispatchPTY(ctx, conn, opts.Pty)
	}
	return repl(ctx, conn)
}

// dispatchPTY decides between -pty-list (one-shot list, exit), explicit
// reattach, explicit fresh PTY, and the default interactive picker.
func dispatchPTY(ctx context.Context, conn *transport.Conn, opts ptyDispatch) error {
	switch {
	case opts.ListOnly:
		ptys, err := listPTYs(ctx, conn)
		if err != nil {
			return fmt.Errorf("list ptys: %w", err)
		}
		renderPTYList(ptys)
		return nil
	case opts.Reattach != "":
		return runPTY(ctx, conn, "", opts.Reattach)
	case opts.New:
		return runPTY(ctx, conn, opts.Command, "")
	}
	choice, err := runPicker(ctx, conn)
	if err != nil {
		return err
	}
	if choice.NewPTY {
		return runPTY(ctx, conn, opts.Command, "")
	}
	if choice.Handle == "" {
		return nil // user quit
	}
	return runPTY(ctx, conn, "", choice.Handle)
}

// rendezvousAndDial handles the full Phase 2 + Phase 3 signaling-mediated
// connection setup: register, send Connect, receive the host's reply,
// punch the peer's candidates to install local NAT mappings, and dial each
// candidate in preferred order until one succeeds.
//
// pub / deviceID come from the same ed25519 keypair the caller already
// installed in the QUIC mTLS client cert, so signaling Register and the
// TLS handshake advertise a consistent identity.
//
// Returns punching.ErrTraversalFailed if every candidate dial attempt
// fails.
func rendezvousAndDial(ctx context.Context, tr *transport.Transport, pc net.PacketConn, url, userID string, secret []byte, firebaseIDToken, targetDeviceID string, localAddr, srflx *net.UDPAddr, pub ed25519.PublicKey, deviceID string) (*transport.Conn, error) {
	cands := localCandidates(localAddr, srflx)
	slog.Info("requesting connect", "target", targetDeviceID, "candidates", len(cands))
	reply, err := negotiateConnectWithRetry(ctx, url, userID, secret, firebaseIDToken, deviceID, pub, targetDeviceID, cands)
	if err != nil {
		return nil, err
	}
	if len(reply.GetCandidates()) == 0 {
		return nil, errors.New("target returned no candidates")
	}

	sorted := punching.SortCandidates(reply.GetCandidates())
	peerAddrs := punching.CandidatesToUDPAddrs(sorted)
	slog.Info("rendezvous complete", "candidates", len(peerAddrs))

	// Punch first so our NAT installs mappings for the peer's addresses.
	if err := punching.Punch(ctx, pc, peerAddrs, punching.Options{}); err != nil {
		slog.Warn("punch failed", "err", err)
	}

	// Sequential dial in preferred order; first success wins.
	for _, p := range peerAddrs {
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := tr.Dial(dialCtx, p)
		cancel()
		if err == nil {
			slog.Info("dialed candidate", "addr", p)
			return conn, nil
		}
		slog.Info("candidate dial failed", "addr", p, "err", err)
	}
	return nil, punching.ErrTraversalFailed
}

// negotiateConnectWithRetry runs negotiateConnect with up to 5 attempts
// (1/2/4/8/16 s backoff) so a wake-mode peershd that's still bringing
// its WS up can register before the CLI gives up. Backoffs are
// longer than mobile-core's because rapid CLI redials from a desktop
// network easily trip the signaling server's per-IP rate limit (429
// after ~4 dials in <2 s).
func negotiateConnectWithRetry(
	ctx context.Context,
	url, userID string,
	secret []byte,
	firebaseIDToken, deviceID string,
	pub ed25519.PublicKey,
	targetDeviceID string,
	cands []*signalv1.EndpointCandidate,
) (*signalv1.Connect, error) {
	backoff := 1 * time.Second
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		reply, err := negotiateConnect(ctx, url, userID, secret, firebaseIDToken, deviceID, pub, targetDeviceID, cands)
		if err == nil {
			return reply, nil
		}
		lastErr = err
		if !isRetryableConnectError(err) {
			return nil, err
		}
		slog.Info("connect retry", "attempt", attempt+1, "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("signaling: %w", ctx.Err())
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, lastErr
}

// negotiateConnect dials signaling once, registers, sends Connect, and
// waits for the target's reply. Closes the WS before returning so the
// later UDP punching / QUIC dial isn't billed against an open Cloud
// Run WebSocket.
func negotiateConnect(
	ctx context.Context,
	url, userID string,
	secret []byte,
	firebaseIDToken, deviceID string,
	pub ed25519.PublicKey,
	targetDeviceID string,
	cands []*signalv1.EndpointCandidate,
) (*signalv1.Connect, error) {
	sc, err := signaling.Dial(ctx, signaling.DialOptions{
		URL:             url,
		UserID:          userID,
		Secret:          secret,
		FirebaseIDToken: firebaseIDToken,
		DeviceID:        deviceID,
		PublicKey:       pub,
		Kind:            signalv1.DeviceKind_DEVICE_KIND_CLI,
		DisplayName:     "peersh-cli",
		ClientID:        clientID,
	})
	if err != nil {
		return nil, fmt.Errorf("signaling.Dial: %w", err)
	}
	defer sc.Close()
	if err := sc.SendConnect(ctx, targetDeviceID, cands); err != nil {
		return nil, fmt.Errorf("SendConnect: %w", err)
	}
	reply, err := sc.Recv(ctx)
	if err != nil {
		return nil, fmt.Errorf("waiting for target reply: %w", err)
	}
	if reply.GetFromDeviceId() != targetDeviceID {
		return nil, fmt.Errorf("got Connect from %q, expected from %q",
			reply.GetFromDeviceId(), targetDeviceID)
	}
	return reply, nil
}

// isRetryableConnectError matches signaling ServerError frames whose
// code says the host hasn't yet finished registering — typical when
// the wake_request was just fired and peershd's wake-listener hasn't
// opened its short-lived signaling WS in time.
func isRetryableConnectError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "target_unknown")
}

// loadOrGenerateKey returns an ed25519 private key sourced from keyDir
// when non-empty (persistent identity), or freshly generated when empty
// (ephemeral identity, the legacy CLI behavior).
func loadOrGenerateKey(keyDir string) (ed25519.PrivateKey, error) {
	if keyDir == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate device key: %w", err)
		}
		return priv, nil
	}
	priv, err := peertls.LoadOrGenerateKey(keyDir)
	if err != nil {
		return nil, fmt.Errorf("load device key from %q: %w", keyDir, err)
	}
	return priv, nil
}

func readPSKFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("psk file %q: %w", path, err)
	}
	return out, nil
}

// localCandidates enumerates this CLI's locally reachable IPs at the bound
// port plus an optional SRFLX candidate from STUN.
func localCandidates(local *net.UDPAddr, srflx *net.UDPAddr) []*signalv1.EndpointCandidate {
	port := uint32(local.Port)
	var out []*signalv1.EndpointCandidate
	if srflx != nil {
		out = append(out, &signalv1.EndpointCandidate{
			Address: srflx.IP.String(), Port: uint32(srflx.Port),
			Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE,
		})
	}
	if !local.IP.IsUnspecified() {
		out = append(out, &signalv1.EndpointCandidate{
			Address: local.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST,
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

func doHandshake(ctx context.Context, conn *transport.Conn) error {
	ctrl, err := conn.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("OpenStream(control): %w", err)
	}
	if err := wire.Write(ctrl, &v1.ClientHello{
		ProtocolVersion: protocolVersion,
		Capabilities:    nil,
		ClientId:        clientID,
	}); err != nil {
		return err
	}
	if err := ctrl.Close(); err != nil {
		slog.Warn("control stream half-close", "err", err)
	}
	r := wire.NewReader(ctrl)
	srv := &v1.ServerHello{}
	if err := wire.Read(r, srv); err != nil {
		return fmt.Errorf("read ServerHello: %w", err)
	}
	if srv.GetProtocolVersion() != protocolVersion {
		return fmt.Errorf("protocol_version mismatch: server=%d, client=%d",
			srv.GetProtocolVersion(), protocolVersion)
	}
	slog.Info("handshake complete", "server_id", srv.GetServerId())
	return nil
}

func repl(ctx context.Context, conn *transport.Conn) error {
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("peersh> ")
		line, err := in.ReadString('\n')
		if errors.Is(err, io.EOF) {
			fmt.Println()
			return nil
		}
		if err != nil {
			return fmt.Errorf("stdin: %w", err)
		}
		cmd := strings.TrimRight(line, "\r\n")
		if cmd == "" {
			continue
		}
		if cmd == "exit" || cmd == "quit" {
			return nil
		}
		if err := runOnce(ctx, conn, cmd); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

func runOnce(ctx context.Context, conn *transport.Conn, cmd string) error {
	stream, err := conn.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("OpenStream: %w", err)
	}
	defer stream.Close()

	if err := wire.Write(stream, &v1.StreamRequest{
		Kind: &v1.StreamRequest_Exec{Exec: &v1.ExecRequest{Command: cmd}},
	}); err != nil {
		return fmt.Errorf("write StreamRequest: %w", err)
	}

	r := wire.NewReader(stream)
	for {
		resp := &v1.ExecResponse{}
		if err := wire.Read(r, resp); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read ExecResponse: %w", err)
		}
		if resp.GetDone() {
			return nil
		}
		if data := resp.GetStdout(); len(data) > 0 {
			os.Stdout.Write(data)
		}
		if data := resp.GetStderr(); len(data) > 0 {
			os.Stderr.Write(data)
		}
	}
}
