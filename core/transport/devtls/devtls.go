// Package devtls produces development TLS material for peersh.
//
// Everything in this package is dev-only. The grep-able constant
// DevSelfSignedOnly makes that obvious in any code path that imports it.
//
// Phase 1 uses self-signed certs and InsecureSkipVerify on the client. Real
// certificate verification (mTLS bound to keypair-derived device IDs) lands
// when signaling makes mutual auth meaningful.
package devtls

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
)

// DevSelfSignedOnly is exported so that any code path using devtls helpers is
// trivially identifiable as not-for-production. Do not flip this to false.
const DevSelfSignedOnly = true

// CertFile is the on-disk filename used for the self-signed cert.
const CertFile = "cert.pem"

// KeyFile is the on-disk filename used for the private key.
const KeyFile = "key.pem"

// LoadOrGenerate returns a TLS certificate suitable for peersh dev use. If
// dir already contains cert + key files, they are loaded; otherwise a fresh
// ed25519 keypair and self-signed cert are generated and written to dir.
//
// The directory is created with mode 0o700 if it does not exist. The key
// file is written with mode 0o600.
func LoadOrGenerate(dir string) (tls.Certificate, error) {
	if dir == "" {
		return tls.Certificate{}, errors.New("devtls: dir must be non-empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("devtls: mkdir %q: %w", dir, err)
	}
	certPath := filepath.Join(dir, CertFile)
	keyPath := filepath.Join(dir, KeyFile)
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			return tls.LoadX509KeyPair(certPath, keyPath)
		}
	}
	return generate(certPath, keyPath)
}

// generate produces a fresh ed25519 keypair and a self-signed cert that
// covers localhost plus the loopback IPs.
func generate(certPath, keyPath string) (tls.Certificate, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("devtls: generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("devtls: generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "peersh-dev"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("devtls: create cert: %w", err)
	}

	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return tls.Certificate{}, err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("devtls: marshal key: %w", err)
	}
	if err := writePEM(keyPath, "PRIVATE KEY", keyDER, 0o600); err != nil {
		return tls.Certificate{}, err
	}

	return tls.LoadX509KeyPair(certPath, keyPath)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("devtls: open %q: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}

// ServerTLSConfig produces a *tls.Config that presents the given certificate
// and advertises the peersh ALPN.
func ServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"peersh/1"},
		MinVersion:   tls.VersionTLS13,
	}
}

// DevClientTLSConfig produces a *tls.Config with InsecureSkipVerify=true and
// the peersh ALPN.
//
// THIS IS DEV-ONLY. The DevSelfSignedOnly constant exists to make audit
// searches trivial: any production-leaning build should fail review the
// moment this helper is referenced.
func DevClientTLSConfig() *tls.Config {
	_ = DevSelfSignedOnly // intentional reference for grep-ability
	return &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"peersh/1"},
		MinVersion:         tls.VersionTLS13,
	}
}
