package celltls_test

import (
	"crypto/tls"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cellmodules/celltls"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil/tlsutiltest"
)

// writeCellMaterial generates a CA + a cell leaf (with a SPIFFE URI SAN) and
// writes cert/key/ca PEM files into a temp dir, returning their paths.
func writeCellMaterial(t *testing.T) (certFile, keyFile, caFile string) {
	t.Helper()
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		URIs: []*url.URL{tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/accesscore")},
	})
	return leaf.WriteFiles(t, t.TempDir(), ca)
}

func topo(t *testing.T, remotes ...bootstrap.RemoteCellEndpoint) bootstrap.DeploymentTopology {
	t.Helper()
	dt, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{Remote: remotes})
	require.NoError(t, err)
	return dt
}

func TestResolve_NotConfigured(t *testing.T) {
	t.Parallel()

	t.Run("all-colocated -> no mTLS, no error", func(t *testing.T) {
		t.Parallel()
		deps, err := celltls.Resolve(topo(t), celltls.Config{})
		require.NoError(t, err)
		assert.True(t, deps.ClientIdentity.IsZero())
		assert.Nil(t, deps.ServerTLS)
	})

	t.Run("loopback remote -> no mTLS, no error", func(t *testing.T) {
		t.Parallel()
		deps, err := celltls.Resolve(topo(t, bootstrap.RemoteCellEndpoint{CellID: "configcore", Endpoint: "127.0.0.1:9090"}), celltls.Config{})
		require.NoError(t, err)
		assert.True(t, deps.ClientIdentity.IsZero())
		assert.Nil(t, deps.ServerTLS)
	})

	t.Run("non-loopback remote + no material -> FAIL-CLOSED", func(t *testing.T) {
		t.Parallel()
		split := topo(t, bootstrap.RemoteCellEndpoint{CellID: "configcore", Endpoint: "https://configcore.svc:8443"})
		_, err := celltls.Resolve(split, celltls.Config{})
		assert.Error(t, err, "non-loopback split with no TLS material must fail closed, never silently plaintext")
	})
}

func TestResolve_Configured(t *testing.T) {
	t.Parallel()
	certFile, keyFile, caFile := writeCellMaterial(t)
	cfg := celltls.Config{CertFile: certFile, KeyFile: keyFile, CAFile: caFile, TrustDomain: "example.org"}

	t.Run("full material + non-loopback remote -> mTLS built", func(t *testing.T) {
		t.Parallel()
		deps, err := celltls.Resolve(topo(t, bootstrap.RemoteCellEndpoint{CellID: "configcore", Endpoint: "https://configcore.svc:8443"}), cfg)
		require.NoError(t, err)
		assert.False(t, deps.ClientIdentity.IsZero())
		require.NotNil(t, deps.ServerTLS)
		// per-target client config mints from the resolved identity
		ctlsCfg, err := deps.ClientIdentity.ConfigForPeer("configcore")
		require.NoError(t, err)
		assert.NotNil(t, ctlsCfg.VerifyConnection)
	})

	t.Run("full material honored even for colocated topology (operator opt-in)", func(t *testing.T) {
		t.Parallel()
		deps, err := celltls.Resolve(topo(t), cfg)
		require.NoError(t, err)
		assert.False(t, deps.ClientIdentity.IsZero())
		assert.NotNil(t, deps.ServerTLS)
	})
}

func TestInternalListenerSecurity(t *testing.T) {
	t.Parallel()
	base := []kauth.ListenerAuth{kauth.AuthNone{}} // stand-in for the service-token plan

	t.Run("nil serverTLS -> chain unchanged, no options", func(t *testing.T) {
		t.Parallel()
		chain, opts := celltls.InternalListenerSecurity(nil, base)
		assert.Len(t, chain, 1)
		assert.Empty(t, opts)
		_, isMTLS := chain[0].(kauth.AuthMTLS)
		assert.False(t, isMTLS, "no AuthMTLS prepended when serverTLS is nil")
	})

	t.Run("non-nil serverTLS -> prepends AuthMTLS + WithListenerTLS opt", func(t *testing.T) {
		t.Parallel()
		chain, opts := celltls.InternalListenerSecurity(&tls.Config{MinVersion: tls.VersionTLS13}, base)
		require.Len(t, chain, 2, "AuthMTLS prepended to base")
		_, isMTLS := chain[0].(kauth.AuthMTLS)
		assert.True(t, isMTLS, "AuthMTLS must be the outer (first) plan")
		assert.Len(t, opts, 1, "WithListenerTLS option contributed")
	})
}

// TestResolve_SharedMTLSEndpoint_FailsClosed: material configured + two remote
// cells sharing one non-loopback endpoint → fail-closed (#2263 F1: split mTLS is
// one cell per process; a shared mTLS endpoint cannot present a per-cell cert).
func TestResolve_SharedMTLSEndpoint_FailsClosed(t *testing.T) {
	t.Parallel()
	certFile, keyFile, caFile := writeCellMaterial(t)
	cfg := celltls.Config{CertFile: certFile, KeyFile: keyFile, CAFile: caFile, TrustDomain: "example.org"}
	shared := topo(t,
		bootstrap.RemoteCellEndpoint{CellID: "configcore", Endpoint: "https://shared.svc:8443"},
		bootstrap.RemoteCellEndpoint{CellID: "auditcore", Endpoint: "https://shared.svc:8443"},
	)
	_, err := celltls.Resolve(shared, cfg)
	assert.Error(t, err, "mTLS + two cells at the same non-loopback endpoint must fail closed (one cell per process)")
}

func TestResolve_ErrorPaths(t *testing.T) {
	t.Parallel()
	certFile, keyFile, caFile := writeCellMaterial(t)
	full := celltls.Config{CertFile: certFile, KeyFile: keyFile, CAFile: caFile, TrustDomain: "example.org"}
	splitTopo := topo(t, bootstrap.RemoteCellEndpoint{CellID: "configcore", Endpoint: "https://configcore.svc:8443"})

	tests := []struct {
		name string
		cfg  celltls.Config
	}{
		{name: "partial: cert+key but no CA", cfg: celltls.Config{CertFile: certFile, KeyFile: keyFile, TrustDomain: "example.org"}},
		{name: "partial: material but no trust domain", cfg: celltls.Config{CertFile: certFile, KeyFile: keyFile, CAFile: caFile}},
		{name: "partial: only trust domain", cfg: celltls.Config{TrustDomain: "example.org"}},
		{name: "bad cert path", cfg: celltls.Config{CertFile: "/no/such/cert", KeyFile: keyFile, CAFile: caFile, TrustDomain: "example.org"}},
		{name: "bad ca path", cfg: celltls.Config{CertFile: certFile, KeyFile: keyFile, CAFile: "/no/such/ca", TrustDomain: "example.org"}},
		{name: "invalid trust domain", cfg: func() celltls.Config { c := full; c.TrustDomain = "Example.ORG"; return c }()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := celltls.Resolve(splitTopo, tc.cfg)
			assert.Error(t, err)
		})
	}
}
