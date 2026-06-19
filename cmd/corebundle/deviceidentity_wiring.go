package main

import (
	"context"
	"fmt"
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
// (renew) listener: AuthMTLS auth scheme + the server *tls.Config built by the
// sanctioned device-identity mTLS-material helper (deviceidentity.NewDeviceMTLSServerConfig,
// CELLTLS-MATERIAL-FUNNEL-01 #1904) — the composition root delegates mTLS-material
// construction rather than calling tlsutil directly.
func deviceMTLSListenerOption(ctx context.Context, clk clock.Clock, signer certsigning.Signer, addr string) (bootstrap.Option, error) {
	tlsCfg, err := deviceidentity.NewDeviceMTLSServerConfig(ctx, clk, signer)
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
