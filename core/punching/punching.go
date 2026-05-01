package punching

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"time"

	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/pion/stun/v2"
)

// DefaultSTUNServer is the public STUN endpoint used when Options.STUNServer
// is empty. Operators can override via the -stun flag on peershd / peersh-cli
// or by setting Options.STUNServer directly.
const DefaultSTUNServer = "stun.l.google.com:19302"

// PunchMagic is the 4-byte prefix on every magic-byte sentinel packet ("pesh").
// The remainder of the packet is a random 12-byte nonce, for total 16 bytes.
// The first byte (0x70) does not have QUIC's long-header bit set, so a peer's
// quic-go Transport will demux these as non-QUIC and drop them harmlessly.
var PunchMagic = []byte{0x70, 0x65, 0x73, 0x68}

// PunchPacketSize is the size of each magic-byte sentinel packet.
const PunchPacketSize = 16

// ErrTraversalFailed is returned by callers (peersh-cli's dial loop) when
// every candidate dial attempt has failed. The Error() string is what users
// see; do not change it without updating docs/deploy/self-hosting.md.
var ErrTraversalFailed = errors.New("Direct connection not possible from this network.")

// Options tunes the punching helpers. Zero values fall back to documented
// defaults.
type Options struct {
	STUNServer    string
	STUNTimeout   time.Duration
	PunchPackets  int
	PunchInterval time.Duration
	Logger        *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.STUNServer == "" {
		o.STUNServer = DefaultSTUNServer
	}
	if o.STUNTimeout == 0 {
		o.STUNTimeout = 3 * time.Second
	}
	if o.PunchPackets == 0 {
		o.PunchPackets = 5
	}
	if o.PunchInterval == 0 {
		o.PunchInterval = 200 * time.Millisecond
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Discover queries opts.STUNServer using pc and returns the reflexive
// address from XOR-MAPPED-ADDRESS.
//
// pc is borrowed; its read deadline is temporarily adjusted while waiting for
// the STUN response and restored afterwards. The caller retains ownership.
func Discover(ctx context.Context, pc net.PacketConn, opts Options) (*net.UDPAddr, error) {
	opts = opts.withDefaults()
	if opts.STUNServer == "" {
		return nil, errors.New("punching: STUN server is empty")
	}

	server, err := net.ResolveUDPAddr("udp", opts.STUNServer)
	if err != nil {
		return nil, fmt.Errorf("punching: resolve STUN %q: %w", opts.STUNServer, err)
	}

	msg, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		return nil, fmt.Errorf("punching: build STUN request: %w", err)
	}
	out, err := msg.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("punching: marshal STUN: %w", err)
	}

	deadline := time.Now().Add(opts.STUNTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := pc.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("punching: SetReadDeadline: %w", err)
	}
	defer pc.SetReadDeadline(time.Time{})

	if _, err := pc.WriteTo(out, server); err != nil {
		return nil, fmt.Errorf("punching: send STUN request: %w", err)
	}

	buf := make([]byte, 1500)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return nil, fmt.Errorf("punching: read STUN response: %w", err)
		}
		// Some non-STUN traffic may arrive on the socket (e.g. the punch
		// packets a peer is firing at us). Loop until we see a STUN packet
		// from the STUN server we actually queried.
		if !equalUDP(from, server) {
			continue
		}
		var resp stun.Message
		resp.Raw = append(resp.Raw[:0], buf[:n]...)
		if err := resp.Decode(); err != nil {
			continue
		}
		if resp.TransactionID != msg.TransactionID {
			continue
		}
		var xor stun.XORMappedAddress
		if err := xor.GetFrom(&resp); err != nil {
			return nil, fmt.Errorf("punching: read XOR-MAPPED-ADDRESS: %w", err)
		}
		opts.Logger.Info("stun discovered srflx", "addr", &net.UDPAddr{IP: xor.IP, Port: xor.Port})
		return &net.UDPAddr{IP: xor.IP, Port: xor.Port}, nil
	}
}

// Punch sends a brief burst of magic-byte sentinel packets to each peer
// address. The goal is purely side-effecting — installing the local-side
// NAT mapping for that destination — and no responses are validated.
//
// Both ends are expected to call Punch concurrently after candidate
// exchange so that each NAT installs the necessary mapping before the QUIC
// handshake starts.
func Punch(ctx context.Context, pc net.PacketConn, peers []*net.UDPAddr, opts Options) error {
	opts = opts.withDefaults()
	if len(peers) == 0 {
		return nil
	}
	opts.Logger.Info("punching peers", "count", len(peers), "packets", opts.PunchPackets)

	for i := 0; i < opts.PunchPackets; i++ {
		for _, p := range peers {
			pkt, err := newPunchPacket()
			if err != nil {
				return err
			}
			if _, err := pc.WriteTo(pkt, p); err != nil {
				// Network error on one address shouldn't kill the burst;
				// other candidates may still be reachable.
				opts.Logger.Debug("punch write failed", "peer", p, "err", err)
				continue
			}
		}
		if i == opts.PunchPackets-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opts.PunchInterval):
		}
	}
	return nil
}

// IsPunchPacket reports whether b looks like one of our magic-byte sentinels.
// It is exported for tests; production code does not need to identify them
// because they are dropped by the QUIC demux automatically.
func IsPunchPacket(b []byte) bool {
	if len(b) < len(PunchMagic) {
		return false
	}
	for i, v := range PunchMagic {
		if b[i] != v {
			return false
		}
	}
	return true
}

// SortCandidates returns a copy of candidates ordered by dial preference:
// SRFLX before HOST, IPv6 before IPv4. Within each bucket, original order
// is preserved.
func SortCandidates(candidates []*signalv1.EndpointCandidate) []*signalv1.EndpointCandidate {
	out := make([]*signalv1.EndpointCandidate, len(candidates))
	copy(out, candidates)
	sort.SliceStable(out, func(i, j int) bool {
		return rank(out[i]) < rank(out[j])
	})
	return out
}

// rank returns a sort key. Lower is preferred. SRFLX 0/1, HOST 2/3, others 4/5;
// even ranks are IPv6, odd are IPv4.
func rank(c *signalv1.EndpointCandidate) int {
	v6 := isIPv6(c.GetAddress())
	switch c.GetType() {
	case signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE:
		if v6 {
			return 0
		}
		return 1
	case signalv1.CandidateType_CANDIDATE_TYPE_HOST:
		if v6 {
			return 2
		}
		return 3
	default:
		if v6 {
			return 4
		}
		return 5
	}
}

func isIPv6(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	return ip.To4() == nil
}

// CandidatesToUDPAddrs converts protobuf EndpointCandidate values into
// net.UDPAddr values, dropping any with an unparseable address.
func CandidatesToUDPAddrs(candidates []*signalv1.EndpointCandidate) []*net.UDPAddr {
	out := make([]*net.UDPAddr, 0, len(candidates))
	for _, c := range candidates {
		ip := net.ParseIP(c.GetAddress())
		if ip == nil {
			continue
		}
		out = append(out, &net.UDPAddr{IP: ip, Port: int(c.GetPort())})
	}
	return out
}

func newPunchPacket() ([]byte, error) {
	pkt := make([]byte, PunchPacketSize)
	copy(pkt, PunchMagic)
	if _, err := rand.Read(pkt[len(PunchMagic):]); err != nil {
		return nil, fmt.Errorf("punching: random nonce: %w", err)
	}
	return pkt, nil
}

// equalUDP compares two UDP addresses by IP and port. Avoids the IP
// representation differences (4-in-6) that string comparison can hit.
func equalUDP(a net.Addr, b *net.UDPAddr) bool {
	ua, ok := a.(*net.UDPAddr)
	if !ok {
		return false
	}
	return ua.Port == b.Port && ua.IP.Equal(b.IP)
}
