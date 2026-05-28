// Package tlsutil builds server-side TLS configurations for GoCell HTTP
// listeners.
//
// Scope: enabling helpers, not a substitute for a TLS-terminating proxy. Large
// scale mTLS termination (rotation, OCSP stapling, dynamic SNI) is delegated to
// the K8s/Service Mesh layer; this package only covers the "I have PEM bytes,
// give me a correct *tls.Config" path used during framework wiring.
//
// ref: spiffe/go-spiffe v2/spiffetls/tlsconfig — MTLSServerConfig field set.
// ref: kubernetes/apiserver pkg/server/secure_serving.go — server cert plumbing.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// NewServerMTLSConfig builds a *tls.Config suitable for server-side mutual
// TLS. The returned config:
//
//   - pins MinVersion to TLS 1.3 (fail-closed; no negotiation down to weaker
//     suites; matches spiffetls.MTLSServerConfig and modern Go TLS defaults);
//   - sets ClientAuth = tls.RequireAndVerifyClientCert so the handshake layer
//     rejects clients that present no certificate or one not chained to
//     clientCAs;
//   - installs clientCAs as the verification pool;
//   - parses certPEMBlock + keyPEMBlock via tls.X509KeyPair as the server
//     identity.
//
// Returns an error on PEM parse failure, cert/key mismatch, or nil clientCAs.
// Callers should usually feed the result to bootstrap.WithListenerTLS.
//
// CALLERS MUST NOT MUTATE the returned *tls.Config's MinVersion, ClientAuth,
// or ClientCAs fields. The builder pins these to fail-closed defaults; mutating
// them post-return downgrades the security posture without compiler/runtime
// signal. If you need different values, re-call this builder with the desired
// inputs; do not edit the returned config in place. (This is a Go ecosystem
// limitation — crypto/tls.Server accepts only *tls.Config, so a sealed
// opaque wrapper is not feasible here.)
func NewServerMTLSConfig(certPEMBlock, keyPEMBlock []byte, clientCAs *x509.CertPool) (*tls.Config, error) {
	if len(certPEMBlock) == 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"tlsutil: certPEMBlock is empty; pass the PEM-encoded server certificate (see os.ReadFile)")
	}
	if len(keyPEMBlock) == 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"tlsutil: keyPEMBlock is empty; pass the PEM-encoded server private key (see os.ReadFile)")
	}
	if clientCAs == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"tlsutil: clientCAs is nil; build a pool with NewClientCAPool")
	}
	cert, err := tls.X509KeyPair(certPEMBlock, keyPEMBlock)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"tlsutil: parse server cert/key PEM", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		Certificates: []tls.Certificate{cert},
	}, nil
}

// NewClientCAPool parses one or more PEM-encoded CA bundles into an
// x509.CertPool used to verify incoming peer certificates. Multiple
// bundles are appended into the same pool; intra-bundle concatenation
// (`cat ca1.pem ca2.pem > bundle.pem`) is also supported.
//
// Returns an error when no input is supplied or none of the supplied bytes
// parse to at least one certificate.
func NewClientCAPool(caPEMBlocks ...[]byte) (*x509.CertPool, error) {
	if len(caPEMBlocks) == 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"tlsutil: no CA PEM bundles supplied")
	}
	pool := x509.NewCertPool()
	any := false
	for _, b := range caPEMBlocks {
		if len(b) == 0 {
			continue
		}
		if pool.AppendCertsFromPEM(b) {
			any = true
		}
	}
	if !any {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"tlsutil: no valid CA certificates parsed from supplied PEM bundles")
	}
	return pool, nil
}
