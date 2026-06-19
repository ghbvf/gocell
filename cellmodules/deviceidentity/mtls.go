package deviceidentity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
)

// ephemeralServerCertTTL bounds the lifetime of the self-signed device-mTLS
// server certificate. It is regenerated on every process start (never persisted),
// matching the ephemeral dev soft-CA whose trust anchor also rotates on restart.
const ephemeralServerCertTTL = 365 * 24 * time.Hour

// NewDeviceMTLSServerConfig builds the server-side *tls.Config for the
// device-mTLS (renew) listener: it verifies DEVICE client certificates against
// the issuing CA's trust bundle (so only certificates the enroll signer minted
// pass the handshake) and presents an ephemeral self-signed server identity
// regenerated each start.
//
// This is the sanctioned device-identity mTLS-material site (CELLTLS-MATERIAL-FUNNEL-01,
// #1904), parallel to cellmodules/celltls for cell-to-cell mTLS: the composition
// root delegates here rather than constructing tlsutil mTLS material ad-hoc. The
// same CA backs both this listener's client-CA pool and the enroll signer, so a
// certificate issued at /enroll is exactly the one accepted at /renew (dev soft-CA
// rotates the anchor on restart).
//
// The server's own identity is self-signed (trusted out-of-band in dev: the device
// uses skip-verify / a pinned cert). Operators front this listener with a real
// server cert / TLS-terminating proxy in production.
func NewDeviceMTLSServerConfig(ctx context.Context, clk clock.Clock, signer certsigning.Signer) (*tls.Config, error) {
	clock.MustHaveClock(clk, "deviceidentity.NewDeviceMTLSServerConfig")
	bundle, err := signer.TrustBundle(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch CA trust bundle: %w", err)
	}
	if len(bundle) == 0 {
		return nil, fmt.Errorf("CA trust bundle is empty; cannot verify device client certificates")
	}
	caPEMs := make([][]byte, 0, len(bundle))
	for _, der := range bundle {
		caPEMs = append(caPEMs, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	clientCAs, err := tlsutil.NewClientCAPool(caPEMs...)
	if err != nil {
		return nil, fmt.Errorf("build device client-CA pool: %w", err)
	}

	certPEM, keyPEM, err := newEphemeralServerCert(clk)
	if err != nil {
		return nil, fmt.Errorf("generate device-mTLS server cert: %w", err)
	}
	cfg, err := tlsutil.NewServerMTLSConfig(certPEM, keyPEM, clientCAs)
	if err != nil {
		return nil, fmt.Errorf("build device-mTLS server config: %w", err)
	}
	return cfg, nil
}

// newEphemeralServerCert mints a fresh self-signed ECDSA P-256 server certificate
// (loopback SANs) for the device-mTLS listener and returns its PEM cert + key
// blocks. It is regenerated on every process start and never persisted.
func newEphemeralServerCert(clk clock.Clock) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}
	now := clk.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "gocell-device-mtls"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(ephemeralServerCertTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
