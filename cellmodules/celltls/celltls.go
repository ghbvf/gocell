package celltls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/spiffeid"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
)

// Environment variable names for operator-provisioned transport mTLS material.
const (
	EnvCertFile    = "GOCELL_TRANSPORT_TLS_CERT_FILE"
	EnvKeyFile     = "GOCELL_TRANSPORT_TLS_KEY_FILE"
	EnvCAFile      = "GOCELL_TRANSPORT_TLS_CA_FILE"
	EnvTrustDomain = "GOCELL_SPIFFE_TRUST_DOMAIN"
)

// Error message constants — MESSAGE-CONST-LITERAL-01.
const (
	msgFailClosed = "celltls: deployment topology has a non-loopback remote cell but no transport TLS material is configured; " +
		"set GOCELL_TRANSPORT_TLS_CERT_FILE/KEY_FILE/CA_FILE + GOCELL_SPIFFE_TRUST_DOMAIN (split mTLS is mandatory for non-loopback peers)"
	msgPartialConfig   = "celltls: transport TLS material partially configured; cert/key/CA file + SPIFFE trust domain are all-or-nothing"
	msgReadCertFile    = "celltls: read cert file"
	msgReadKeyFile     = "celltls: read key file"
	msgReadCAFile      = "celltls: read CA file"
	msgBuildCAPool     = "celltls: build trust-root pool"
	msgBuildClientID   = "celltls: build client identity"
	msgBuildServerMTLS = "celltls: build server mTLS config"
	msgCertReadLeaf    = "celltls: parse local workload certificate leaf"
	// msgCertColocatedMismatch fails closed (#2297) when the local workload
	// certificate's cell-SPIFFE-ID set does not EXACTLY equal the cells this
	// process hosts (topology Colocated): the cert must cover every hosted cell
	// (no missing identity → its cross-bind/peer-verify would fail) and carry no
	// extra cell it does not host (least privilege). Re-provision the workload
	// cert with exactly this process's cell SPIFFE IDs as URI SANs.
	msgCertColocatedMismatch = "celltls: local workload certificate cell-SAN set does not exactly match the cells this " +
		"process hosts; the cert URI SANs must be exactly the colocated cells' SPIFFE IDs (#2297 allow-set)"
)

// Config carries the operator-provided transport mTLS material locations. The
// composition root populates it from the environment via LoadConfigFromEnv;
// tests construct it directly with temp-file paths.
type Config struct {
	CertFile    string
	KeyFile     string
	CAFile      string
	TrustDomain string
}

// configuredCount returns how many of the four fields are non-empty (used to
// distinguish "not configured" (0) from "fully configured" (4) from a partial
// misconfiguration (1–3)).
func (c Config) configuredCount() int {
	n := 0
	for _, v := range []string{c.CertFile, c.KeyFile, c.CAFile, c.TrustDomain} {
		if v != "" {
			n++
		}
	}
	return n
}

// LoadConfigFromEnv reads the transport mTLS material locations from the
// environment. The composition root calls this and passes the result to Resolve.
func LoadConfigFromEnv() Config {
	return Config{
		CertFile:    os.Getenv(EnvCertFile),
		KeyFile:     os.Getenv(EnvKeyFile),
		CAFile:      os.Getenv(EnvCAFile),
		TrustDomain: os.Getenv(EnvTrustDomain),
	}
}

// Deps bundles the resolved transport mTLS material.
//
// When mTLS is not configured (and not required), ClientIdentity is the zero
// value (IsZero) and ServerTLS is nil — both the client transport and the
// internal listener stay plaintext (service-token only). When configured, both
// are populated: celltransport.Resolve injects ClientIdentity into the remote
// http.Client (per-peer config), and the composition root wires ServerTLS onto
// the internal listener (AuthMTLS + WithListenerTLS).
type Deps struct {
	ClientIdentity tlsutil.ClientIdentity
	ServerTLS      *tls.Config
}

// Resolve selects the cell's transport mTLS material for the given deployment
// topology. See the package doc for the material set and the fail-closed gate.
func Resolve(topo bootstrap.DeploymentTopology, cfg Config) (Deps, error) {
	switch cfg.configuredCount() {
	case 0:
		// Not configured. Mandatory only when a non-loopback peer will be dialed.
		if topo.HasNonLoopbackRemoteCells() {
			return Deps{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgFailClosed,
				errcode.WithInternal(
					errcode.InternalAttr("hint", "set env vars: "+EnvCertFile+", "+EnvKeyFile+", "+EnvCAFile+", "+EnvTrustDomain),
				))
		}
		return Deps{}, nil
	case 4:
		// Fully configured — build below (honored regardless of topology).
	default:
		return Deps{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgPartialConfig,
			errcode.WithInternal(
				errcode.InternalAttr("configured_count", cfg.configuredCount()),
			))
	}

	certPEM, err := os.ReadFile(cfg.CertFile)
	if err != nil {
		return Deps{}, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgReadCertFile, err,
			errcode.WithInternal(errcode.InternalAttr("file", cfg.CertFile)))
	}
	keyPEM, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		return Deps{}, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgReadKeyFile, err,
			errcode.WithInternal(errcode.InternalAttr("file", cfg.KeyFile)))
	}
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return Deps{}, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgReadCAFile, err,
			errcode.WithInternal(errcode.InternalAttr("file", cfg.CAFile)))
	}

	rootCAs, err := tlsutil.NewClientCAPool(caPEM)
	if err != nil {
		return Deps{}, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgBuildCAPool, err,
			errcode.WithInternal(errcode.InternalAttr("ca_file", cfg.CAFile)))
	}
	clientID, err := tlsutil.NewClientIdentity(certPEM, keyPEM, rootCAs, cfg.TrustDomain)
	if err != nil {
		return Deps{}, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgBuildClientID, err,
			errcode.WithInternal(
				errcode.InternalAttr("cert_file", cfg.CertFile),
				errcode.InternalAttr("key_file", cfg.KeyFile),
				errcode.InternalAttr("trust_domain", cfg.TrustDomain),
			))
	}
	// Server side uses the SAME root pool as ClientCAs (single root signs every
	// cell cert) and presents the same leaf cert/key.
	serverTLS, err := tlsutil.NewServerMTLSConfig(certPEM, keyPEM, rootCAs)
	if err != nil {
		return Deps{}, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgBuildServerMTLS, err,
			errcode.WithInternal(
				errcode.InternalAttr("cert_file", cfg.CertFile),
				errcode.InternalAttr("key_file", cfg.KeyFile),
			))
	}
	if err := validateCertCoversColocated(certPEM, cfg.TrustDomain, topo); err != nil {
		return Deps{}, err
	}
	return Deps{ClientIdentity: clientID, ServerTLS: serverTLS}, nil
}

// validateCertCoversColocated fails closed (#2297) when the local workload
// certificate's cell-SPIFFE-ID set does not EXACTLY match the cells this process
// hosts (topo.ColocatedCells): every hosted cell must be present (otherwise its
// cross-bind / peer-verify would fail at first call) AND the cert must carry no
// cell it does not host (least privilege). The check is skipped when the topology
// does not enumerate local cells (ColocatedCells empty — a zero-value all-colocated
// monolith or a remote-only role): bootstrap has no assembly cell set there, so
// there is nothing to match against and runtime membership still governs.
func validateCertCoversColocated(certPEM []byte, trustDomain string, topo bootstrap.DeploymentTopology) error {
	hosted := topo.ColocatedCells()
	if len(hosted) == 0 {
		return nil
	}
	set, err := certCellSet(certPEM)
	if err != nil {
		return err
	}
	missing := make([]string, 0, len(hosted))
	for _, cell := range hosted {
		id, ferr := spiffeid.ForCell(trustDomain, cell)
		if ferr != nil {
			return errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgCertColocatedMismatch, ferr,
				errcode.WithInternal(errcode.InternalAttr("cell", cell)))
		}
		if !set.Contains(id) {
			missing = append(missing, cell)
		}
	}
	// Exact match: every hosted cell present (no missing) AND no extra cell in the
	// cert beyond the hosted set (cardinality equal).
	if len(missing) == 0 && set.Len() == len(hosted) {
		return nil
	}
	return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgCertColocatedMismatch,
		errcode.WithInternal(
			errcode.InternalAttr("hosted_cells", strings.Join(hosted, ",")),
			errcode.InternalAttr("cert_set", set.String()),
			errcode.InternalAttr("missing", strings.Join(missing, ",")),
			errcode.InternalAttr("extra", strings.Join(certExtraCells(set, hosted), ",")),
		))
}

// certExtraCells returns the canonical SPIFFE IDs in set whose cell name is not in
// the hosted list — the cells a workload cert carries BEYOND what the process
// hosts (a least-privilege violation), for the mismatch diagnostic.
func certExtraCells(set spiffeid.CellSet, hosted []string) []string {
	hostedSet := make(map[string]struct{}, len(hosted))
	for _, h := range hosted {
		hostedSet[h] = struct{}{}
	}
	var extra []string
	for _, id := range set.Cells() {
		if _, ok := hostedSet[id.Cell()]; !ok {
			extra = append(extra, id.String())
		}
	}
	return extra
}

// certCellSet parses the leaf certificate from a PEM block and returns its cell
// SPIFFE-ID set (the URI SAN cell IDs).
func certCellSet(certPEM []byte) (spiffeid.CellSet, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return spiffeid.CellSet{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgCertReadLeaf)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return spiffeid.CellSet{}, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgCertReadLeaf, err)
	}
	return spiffeid.CellSetFromURIs(leaf.URIs)
}

// InternalListenerSecurity augments an internal-listener service-token auth chain
// with transport-layer mTLS when serverTLS is non-nil (split topology with TLS
// material). It is the SINGLE place composition roots wire server-side cross-cell
// mTLS, so corebundle and ssobff cannot drift in chain shape.
//
//   - serverTLS == nil → returns base unchanged + no listener options (demo /
//     loopback / no-material: service-token-only, as before).
//   - serverTLS != nil → prepends kauth.AuthMTLS{} (handshake enforces
//     RequireAndVerifyClientCert + installs middleware.MTLS via auth_plan_apply)
//     and returns bootstrap.WithListenerTLS(serverTLS). The resulting
//     "mtls+service-token" chain layers transport peer auth (outer) over the
//     message-layer service-token guard (inner) — they compose, not conflict
//     (auth_plan_describe already names this combination).
//
// base is not mutated (a fresh slice is returned).
func InternalListenerSecurity(serverTLS *tls.Config, base []kauth.ListenerAuth) ([]kauth.ListenerAuth, []bootstrap.ListenerOption) {
	if serverTLS == nil {
		return base, nil
	}
	chain := make([]kauth.ListenerAuth, 0, len(base)+1)
	chain = append(chain, kauth.AuthMTLS{})
	chain = append(chain, base...)
	return chain, []bootstrap.ListenerOption{bootstrap.WithListenerTLS(serverTLS)}
}
