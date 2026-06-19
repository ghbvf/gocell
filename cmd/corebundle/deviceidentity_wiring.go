package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/ghbvf/gocell/cellmodules/certdeps"
	"github.com/ghbvf/gocell/cellmodules/deviceidentity"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
	"github.com/ghbvf/gocell/framework/runtime/certsigning/pdpauthz"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
)

const (
	// deviceIdentityIssuerID is the issuer identifier stamped into the CertScope
	// metadata for device certificates. softca signs with its own intermediate
	// key/cert (it does NOT read this value into the issued cert), so this is pure
	// scope identity metadata — a stable, non-blank, deployment-wide label.
	deviceIdentityIssuerID = "gocell-device-ca"

	// deviceCertMaxTTL is the issuance ceiling the PDP-backed certsigning
	// Authorizer grants for every device certificate. The signer additionally
	// clamps notAfter to the issuing CA's expiry, so this is an upper bound, not a
	// guarantee. Devices renew before expiry over the device-mTLS listener.
	deviceCertMaxTTL = 90 * 24 * time.Hour

	// deviceMTLSAddrEnv overrides the device-mTLS (renew) listener bind address.
	deviceMTLSAddrEnv = "GOCELL_HTTP_DEVICE_MTLS_ADDR"

	// deviceMTLSDefaultAddr is the loopback default for the device-mTLS listener.
	// It is distinct from the primary (business), internal (9090), and health
	// (9091) listener ports. Operators expose it on a routable address via
	// deviceMTLSAddrEnv in real deployments.
	deviceMTLSDefaultAddr = "127.0.0.1:9092"

	// ephemeralServerCertTTL bounds the lifetime of the self-signed device-mTLS
	// server certificate. The certificate is regenerated on every process start
	// (it is never persisted), matching the ephemeral dev soft-CA whose trust
	// anchor also rotates on restart.
	ephemeralServerCertTTL = 365 * 24 * time.Hour
)

// deviceIdentityServingOptions builds the framework-owned device-identity EST
// serving surface (ADR 202606130635-1939, framework-owned contracts): the
// enroll + cacerts routes (mounted on PrimaryListener) and the renew route
// (mounted on the dedicated DeviceMTLSListener), plus the bootstrap option that
// wires the device-mTLS listener itself.
//
// The PDP-backed certsigning.Authorizer reuses the SAME ABAC engine that gates
// HTTP/gRPC (passed as pdp, the lazyAuthorizer resolved by AuthorizerFromCells);
// no device principal is forged — the cert path goes through the explicit-subject
// AuthorizeAs seam. The device-mTLS listener's client-CA pool is the SAME issuing
// CA trust bundle the enroll signer mints from, so a certificate this front-end
// issues at /enroll is exactly the one accepted by the renew listener's mutual
// TLS (single CA instance; the dev soft-CA rotates its anchor on restart).
func deviceIdentityServingOptions(
	ctx context.Context,
	clk clock.Clock,
	topo bootstrap.Topology,
	jwtVerifier auth.IntentTokenVerifier,
	authorizer auth.Authorizer,
	mtlsAddr string,
) ([]bootstrap.FrameworkServedRoute, bootstrap.Option, error) {
	// The cert-signing reuse bridge authorizes device enroll/renew through the SAME
	// ABAC PDP via the explicit-subject AuthorizeAs seam — no device principal is
	// forged. The lazyAuthorizer also implements auth.SubjectAuthorizer (delegating
	// to the resolved PDP). Fail fast: a primary authorizer that cannot do
	// explicit-subject decisions would silently disable device enrollment.
	pdp, ok := authorizer.(auth.SubjectAuthorizer)
	if !ok {
		return nil, nil, fmt.Errorf("primary authorizer does not implement auth.SubjectAuthorizer")
	}
	certDeps, err := certdeps.Resolve(clk, topo)
	if err != nil {
		return nil, nil, fmt.Errorf("device-identity cert deps: %w", err)
	}

	issuerID, err := certsigning.NewIssuerID(deviceIdentityIssuerID)
	if err != nil {
		return nil, nil, fmt.Errorf("device-identity issuer id: %w", err)
	}

	verifier, err := auth.NewEnrollmentCredentialVerifier(jwtVerifier)
	if err != nil {
		return nil, nil, fmt.Errorf("device-identity enrollment-credential verifier: %w", err)
	}

	certAuthz, err := pdpauthz.New(pdp, deviceCertMaxTTL)
	if err != nil {
		return nil, nil, fmt.Errorf("device-identity cert authorizer: %w", err)
	}

	svc, err := deviceidentity.NewService(clk, certDeps.Signer, certAuthz, verifier, issuerID)
	if err != nil {
		return nil, nil, fmt.Errorf("device-identity service: %w", err)
	}

	listenerOpt, err := deviceMTLSListenerOption(ctx, clk, certDeps.Signer, mtlsAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("device-mTLS listener: %w", err)
	}

	routes := []bootstrap.FrameworkServedRoute{
		svc.EnrollRoute(),
		svc.CacertsRoute(),
		svc.RenewRoute(),
	}
	return routes, listenerOpt, nil
}

// deviceMTLSListenerOption builds the bootstrap option for the device-mTLS
// (renew) listener: AuthMTLS auth scheme + a server *tls.Config whose client-CA
// pool is the issuing CA trust bundle (so only certificates this CA minted pass
// the handshake) and whose server identity is an ephemeral self-signed cert
// regenerated each start.
func deviceMTLSListenerOption(ctx context.Context, clk clock.Clock, signer certsigning.Signer, addr string) (bootstrap.Option, error) {
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
	tlsCfg, err := tlsutil.NewServerMTLSConfig(certPEM, keyPEM, clientCAs)
	if err != nil {
		return nil, fmt.Errorf("build device-mTLS server config: %w", err)
	}

	return bootstrap.WithListener(
		cell.DeviceMTLSListener, addr,
		[]kauth.ListenerAuth{kauth.AuthMTLS{}},
		bootstrap.WithListenerTLS(tlsCfg),
	), nil
}

// deviceMTLSAddrFromEnv resolves the device-mTLS (renew) listener bind address
// from deviceMTLSAddrEnv, defaulting to the loopback deviceMTLSDefaultAddr.
func deviceMTLSAddrFromEnv() string {
	if addr := os.Getenv(deviceMTLSAddrEnv); addr != "" {
		return addr
	}
	return deviceMTLSDefaultAddr
}

// newEphemeralServerCert mints a fresh self-signed ECDSA P-256 server
// certificate (loopback SANs) for the device-mTLS listener and returns its PEM
// cert + key blocks. It is regenerated on every process start and never
// persisted — the listener verifies DEVICE client certificates against the CA
// pool; the server's own identity is trusted out-of-band in dev (matching the
// ephemeral soft-CA). Operators front this listener with a real server cert /
// TLS-terminating proxy in production.
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
