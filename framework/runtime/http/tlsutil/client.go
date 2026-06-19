package tlsutil

import (
	"crypto/tls"
	"crypto/x509"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/spiffeid"
)

// Client-side mTLS message constants — MESSAGE-CONST-LITERAL-01.
const (
	msgClientEmptyCert      = "tlsutil: certPEMBlock is empty; pass the PEM-encoded client certificate (see os.ReadFile)"
	msgClientEmptyKey       = "tlsutil: keyPEMBlock is empty; pass the PEM-encoded client private key (see os.ReadFile)"
	msgClientNilRoots       = "tlsutil: rootCAs is nil; build a pool with NewClientCAPool (peer-cert trust anchors)"
	msgClientZeroPeerID     = "tlsutil: expectedPeerID is the zero CellID; pass the target cell's SPIFFE ID (spiffeid.ForCell)"
	msgClientEmptyTD        = "tlsutil: trustDomain is empty; pass the SPIFFE trust domain (GOCELL_SPIFFE_TRUST_DOMAIN)"
	msgClientIdentityZero   = "tlsutil: ConfigForPeer called on a zero-value ClientIdentity (use celltls.Resolve / NewClientIdentity)"
	msgVerifyNoPeerCert     = "tlsutil: peer presented no certificate"
	msgVerifyChainFailed    = "tlsutil: peer certificate chain verification failed"
	msgVerifyNoCellID       = "tlsutil: peer certificate carries no cell SPIFFE ID (URI SAN spiffe://<td>/cell/<cell>)"
	msgVerifyMixedTD        = "tlsutil: peer certificate carries cell SPIFFE IDs from more than one trust domain"
	msgVerifyPeerIDMismatch = "tlsutil: expected target cell is not in the peer certificate's cell set"
)

// NewClientMTLSConfig builds a *tls.Config for client-side mutual TLS that
// authenticates the SERVER peer by its SPIFFE cell ID (a URI SAN), NOT by DNS
// hostname.
//
// Why InsecureSkipVerify is true (and why this is NOT fail-open): a cell's remote
// endpoint is a deployment address (k8s Service / pod IP), which is unrelated to
// the cell's cryptographic identity — the identity lives in the cert's
// spiffe://<td>/cell/<cell> URI SAN. The stdlib default verification matches the
// dial HOSTNAME against the cert, which is the wrong check here, so it is disabled.
// It is REPLACED by VerifyConnection, which performs the FULL security check:
//   - builds and verifies the peer chain against rootCAs (signature, validity
//     window, and ExtKeyUsage=ServerAuth) — an untrusted or expired peer is
//     rejected exactly as stdlib verification would; and
//   - extracts the peer's cell SPIFFE-ID SET and requires expectedPeerID to be a
//     MEMBER (the peer may be a multi-cell workload hosting several cells, #2297).
//
// So a man-in-the-middle presenting a cert whose cell set does not include the
// target cell (or one not chained to rootCAs) fails the handshake. This mirrors
// spiffe/go-spiffe v2/spiffetls/tlsconfig.MTLSClientConfig + tlsconfig.AuthorizeMemberOf.
//
// The returned config also presents the caller's own client certificate
// (certPEMBlock/keyPEMBlock) for the server's RequireAndVerifyClientCert side and
// pins MinVersion to TLS 1.3 (matches NewServerMTLSConfig). Returns an error on
// empty PEM, cert/key mismatch, nil rootCAs, or a zero expectedPeerID.
//
// CALLERS MUST NOT MUTATE the returned config's InsecureSkipVerify,
// VerifyConnection, MinVersion, or Certificates — flipping InsecureSkipVerify to
// false (re-enabling hostname check) or clearing VerifyConnection (dropping the
// SPIFFE-ID check) silently breaks peer authentication. Go's crypto/tls accepts
// only *tls.Config, so a sealed opaque wrapper is infeasible (same documented Go
// ceiling as NewServerMTLSConfig); the construction funnel (sealed ClientIdentity,
// sole minter celltls.Resolve) plus the archtest ban on bare InsecureSkipVerify
// outside this package are the enforcement (see TLSUTIL archtest godoc).
func NewClientMTLSConfig(certPEMBlock, keyPEMBlock []byte, rootCAs *x509.CertPool, expectedPeerID spiffeid.CellID) (*tls.Config, error) {
	if len(certPEMBlock) == 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientEmptyCert)
	}
	if len(keyPEMBlock) == 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientEmptyKey)
	}
	if rootCAs == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientNilRoots)
	}
	if expectedPeerID.IsZero() {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientZeroPeerID)
	}
	cert, err := tls.X509KeyPair(certPEMBlock, keyPEMBlock)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"tlsutil: parse client cert/key PEM", err)
	}
	// InsecureSkipVerify is true on purpose and is NOT fail-open: the default
	// hostname check is intentionally replaced by the SPIFFE-ID + chain
	// verification in verifyPeerCellIdentity (see NewClientMTLSConfig godoc).
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec // G402: see comment above + godoc
		VerifyConnection:   verifyPeerCellIdentity(rootCAs, expectedPeerID),
	}, nil
}

// verifyPeerCellIdentity returns the VerifyConnection callback that fully
// authenticates the peer: chain verification against rootCAs (incl. validity +
// ServerAuth EKU) followed by a membership check — expectedPeerID must be IN the
// peer certificate's cell-SPIFFE-ID set (a multi-cell workload cert is valid).
func verifyPeerCellIdentity(rootCAs *x509.CertPool, expectedPeerID spiffeid.CellID) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgVerifyNoPeerCert)
		}
		leaf := cs.PeerCertificates[0]

		intermediates := x509.NewCertPool()
		for _, c := range cs.PeerCertificates[1:] {
			intermediates.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:         rootCAs,
			Intermediates: intermediates,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return errcode.Wrap(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgVerifyChainFailed, err)
		}

		peerSet, err := spiffeid.CellSetFromURIs(leaf.URIs)
		if err != nil {
			return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgVerifyMixedTD)
		}
		if peerSet.IsEmpty() {
			return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgVerifyNoCellID)
		}
		if !peerSet.Contains(expectedPeerID) {
			return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgVerifyPeerIDMismatch,
				errcode.WithInternal(
					errcode.InternalAttr("expected", expectedPeerID.String()),
					errcode.InternalAttr("peer_set", peerSet.String()),
				))
		}
		return nil
	}
}

// ClientIdentity is the sealed bundle of a cell's own client cert/key, the trust
// root that must sign peer certs, and the SPIFFE trust domain. It mints a
// per-target-cell *tls.Config via ConfigForPeer (the expected peer SPIFFE ID
// depends on the target cell, so one config-per-peer is required).
//
// Sealed: all fields unexported; the sole minter is [NewClientIdentity], which in
// turn is called only by cellmodules/celltls.Resolve (CELLTLS-MATERIAL-FUNNEL-01).
// The zero value is the "no client mTLS" state ([ClientIdentity.IsZero]); calling
// ConfigForPeer on it fails closed.
type ClientIdentity struct {
	certPEM     []byte
	keyPEM      []byte
	rootCAs     *x509.CertPool
	trustDomain string
}

// NewClientIdentity validates and bundles the cell's client mTLS material. It
// fails closed on empty PEM, an unparseable cert/key pair, a nil trust-root pool,
// or an invalid SPIFFE trust domain — so a misconfiguration surfaces at startup,
// not at first peer dial.
func NewClientIdentity(certPEM, keyPEM []byte, rootCAs *x509.CertPool, trustDomain string) (ClientIdentity, error) {
	if len(certPEM) == 0 {
		return ClientIdentity{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientEmptyCert)
	}
	if len(keyPEM) == 0 {
		return ClientIdentity{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientEmptyKey)
	}
	if rootCAs == nil {
		return ClientIdentity{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientNilRoots)
	}
	if trustDomain == "" {
		return ClientIdentity{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientEmptyTD)
	}
	if err := spiffeid.ValidateTrustDomain(trustDomain); err != nil {
		return ClientIdentity{}, err
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return ClientIdentity{}, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"tlsutil: parse client cert/key PEM", err)
	}
	return ClientIdentity{certPEM: certPEM, keyPEM: keyPEM, rootCAs: rootCAs, trustDomain: trustDomain}, nil
}

// IsZero reports whether ci is the zero (no-client-mTLS) value.
func (ci ClientIdentity) IsZero() bool {
	return ci.certPEM == nil && ci.keyPEM == nil && ci.rootCAs == nil && ci.trustDomain == ""
}

// ConfigForPeer mints the client *tls.Config for dialing targetCell: it pins the
// expected peer identity to spiffe://<trustDomain>/cell/<targetCell> and delegates
// to NewClientMTLSConfig. Fails closed on a zero-value identity or an invalid
// targetCell.
func (ci ClientIdentity) ConfigForPeer(targetCell string) (*tls.Config, error) {
	if ci.IsZero() {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgClientIdentityZero)
	}
	expected, err := spiffeid.ForCell(ci.trustDomain, targetCell)
	if err != nil {
		return nil, err
	}
	return NewClientMTLSConfig(ci.certPEM, ci.keyPEM, ci.rootCAs, expected)
}
