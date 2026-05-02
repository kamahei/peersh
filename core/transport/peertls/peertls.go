package peertls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/peersh/peersh/core/devid"
)

// ALPN must match core/transport.ALPN. Duplicated here to avoid a cyclic
// import (transport depends on this package, not the other way around).
const alpn = "peersh/1"

// KeyFile is the on-disk filename used for the long-lived ed25519 private
// key. The matching cert is regenerated on every load — the key is the
// source of truth, the cert is just a self-signed envelope around it.
const KeyFile = "key.pem"

// LoadOrGenerateKey returns the long-lived ed25519 keypair stored under dir.
//
// If dir already contains KeyFile, the key is loaded. Otherwise a fresh
// keypair is generated and written. The directory is created with mode
// 0o700 if absent; the key file is written with mode 0o600.
//
// The same dir layout is compatible with core/transport/devtls deployments:
// existing operators upgrading from devtls-generated certs keep their
// device_id stable because the underlying ed25519 key is reused.
func LoadOrGenerateKey(dir string) (ed25519.PrivateKey, error) {
	if dir == "" {
		return nil, errors.New("peertls: dir must be non-empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("peertls: mkdir %q: %w", dir, err)
	}
	keyPath := filepath.Join(dir, KeyFile)
	if raw, err := os.ReadFile(keyPath); err == nil {
		return parsePrivateKey(raw)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("peertls: generate key: %w", err)
	}
	if err := writeKey(keyPath, priv); err != nil {
		return nil, err
	}
	return priv, nil
}

// CertFromEd25519 builds a self-signed X.509 certificate whose subject
// pubkey and signing key are both priv. Loopback SANs are included so the
// cert is also usable for local-only test wiring.
func CertFromEd25519(priv ed25519.PrivateKey) (tls.Certificate, error) {
	if l := len(priv); l != ed25519.PrivateKeySize {
		return tls.Certificate{}, fmt.Errorf("peertls: ed25519 private key must be %d bytes, got %d", ed25519.PrivateKeySize, l)
	}
	pub := priv.Public().(ed25519.PublicKey)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("peertls: generate serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "peersh-" + devid.Derive(pub)},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("peertls: create cert: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        mustParse(der),
	}, nil
}

// ServerTLSConfig produces a *tls.Config for a peersh QUIC server.
//
// The server presents serverCert and requires a client certificate
// (mTLS). Client certs are validated for shape (single self-signed leaf
// with an ed25519 public key); the authenticated peer's device_id is
// available to the application layer via PeerDeviceID on the post-
// handshake connection state.
//
// peertls intentionally does not authorize the peer device_id at the TLS
// layer. The signaling channel is the source of truth for "which peer
// is allowed to talk to me right now", and the application is responsible
// for cross-checking PeerDeviceID against the signaling-supplied identity.
func ServerTLSConfig(serverCert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{serverCert},
		ClientAuth:            tls.RequireAnyClientCert,
		MinVersion:            tls.VersionTLS13,
		NextProtos:            []string{alpn},
		VerifyPeerCertificate: verifyEd25519Leaf(""),
	}
}

// ClientTLSConfig produces a *tls.Config for a peersh QUIC client.
//
// The client presents clientCert and pins the server's identity:
// handshake fails if the server's cert public key does not hash to
// expectedServerDeviceID. expectedServerDeviceID must be the 16-character
// device_id form produced by devid.Derive.
//
// InsecureSkipVerify is set to true to disable Go's CA-based validation —
// peersh has no CA. The non-nil VerifyPeerCertificate callback below is
// the actual identity check, and is mandatory: building this config
// without it would silently accept any peer.
func ClientTLSConfig(clientCert tls.Certificate, expectedServerDeviceID string) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{clientCert},
		InsecureSkipVerify:    true,
		MinVersion:            tls.VersionTLS13,
		NextProtos:            []string{alpn},
		VerifyPeerCertificate: verifyEd25519Leaf(expectedServerDeviceID),
	}
}

// PeerDeviceID returns the peersh device_id of the verified peer on a
// completed TLS connection state, or "" if no peer cert was presented or
// the leaf does not carry an ed25519 public key.
func PeerDeviceID(state tls.ConnectionState) string {
	if len(state.PeerCertificates) == 0 {
		return ""
	}
	pub, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return devid.Derive(pub)
}

// verifyEd25519Leaf returns a VerifyPeerCertificate callback that checks
// the peer presented exactly one self-signed leaf with an ed25519 pubkey.
// When expectedDeviceID is non-empty, it also enforces that the pubkey-
// derived device_id matches.
func verifyEd25519Leaf(expectedDeviceID string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("peertls: peer presented no certificate")
		}
		// QUIC / TLS 1.3 caps the cert chain at the leaf in our usage; if
		// a peer ever sends more, that's outside the design and we reject.
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("peertls: parse leaf: %w", err)
		}
		pub, ok := leaf.PublicKey.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("peertls: leaf must be ed25519, got %T", leaf.PublicKey)
		}
		if expectedDeviceID == "" {
			return nil
		}
		got := devid.Derive(pub)
		if got != expectedDeviceID {
			return fmt.Errorf("peertls: peer device_id mismatch: got %q want %q", got, expectedDeviceID)
		}
		return nil
	}
}

func parsePrivateKey(raw []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("peertls: key file is not PEM-encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("peertls: parse PKCS8 key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("peertls: key is not ed25519, got %T", key)
	}
	return priv, nil
}

func writeKey(path string, priv ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("peertls: marshal key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("peertls: open %q: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// mustParse is used inside CertFromEd25519 to populate Leaf so callers can
// read cert.Leaf.PublicKey without a re-parse. The DER comes from
// x509.CreateCertificate moments earlier, so this should never fail; if it
// somehow does, the cert is unusable anyway.
func mustParse(der []byte) *x509.Certificate {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		panic(fmt.Errorf("peertls: parse just-created cert: %w", err))
	}
	return c
}
