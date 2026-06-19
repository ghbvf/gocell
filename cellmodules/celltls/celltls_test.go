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

// writeCellMaterialFor generates a CA + a single workload leaf whose URI SANs are
// the given cells' SPIFFE ids (a multi-cell workload cert, #2297) in trust domain
// example.org, writes cert/key/ca PEM files into a temp dir and returns their paths.
func writeCellMaterialFor(t *testing.T, cells ...string) (certFile, keyFile, caFile string) {
	t.Helper()
	ca := tlsutiltest.NewCA(t)
	uris := make([]*url.URL, 0, len(cells))
	for _, c := range cells {
		uris = append(uris, tlsutiltest.SPIFFEURI(t, "spiffe://example.org/cell/"+c))
	}
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{URIs: uris})
	return leaf.WriteFiles(t, t.TempDir(), ca)
}

// topoColocated builds an explicit topology declaring the given colocated (local)
// cells + remotes — exercises the #2297 startup cert↔colocated exact-match check.
func topoColocated(t *testing.T, colocated []string, remotes ...bootstrap.RemoteCellEndpoint) bootstrap.DeploymentTopology {
	t.Helper()
	dt, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{Colocated: colocated, Remote: remotes})
	require.NoError(t, err)
	return dt
}

// TestResolve_MultiCellSharedEndpoint_Succeeds: #2297 lifts the one-cell-per-process
// limit (former #2263 F1 fail-closed guard is gone). A process hosting multiple
// cells presents one workload cert carrying every hosted cell's SPIFFE id, and the
// shared-endpoint topology resolves successfully.
func TestResolve_MultiCellSharedEndpoint_Succeeds(t *testing.T) {
	t.Parallel()
	certFile, keyFile, caFile := writeCellMaterialFor(t, "accesscore", "configcore")
	cfg := celltls.Config{CertFile: certFile, KeyFile: keyFile, CAFile: caFile, TrustDomain: "example.org"}
	// This process hosts accesscore + configcore; auditcore is remote.
	topo := topoColocated(t, []string{"accesscore", "configcore"},
		bootstrap.RemoteCellEndpoint{CellID: "auditcore", Endpoint: "https://shared.svc:8443"},
	)
	deps, err := celltls.Resolve(topo, cfg)
	require.NoError(t, err, "a workload cert covering both hosted cells must resolve (one cell per process is lifted)")
	assert.False(t, deps.ClientIdentity.IsZero())
	assert.NotNil(t, deps.ServerTLS)
}

// TestResolve_CertColocatedExactMatch covers the #2297 startup fail-fast: the local
// workload cert's cell-SAN set must EXACTLY equal the cells this process hosts
// (least privilege) — neither missing a hosted cell nor carrying an extra one.
func TestResolve_CertColocatedExactMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		certCells []string
		colocated []string
		wantErr   bool
	}{
		{name: "exact match single", certCells: []string{"accesscore"}, colocated: []string{"accesscore"}},
		{name: "exact match multi", certCells: []string{"accesscore", "configcore"}, colocated: []string{"accesscore", "configcore"}},
		{name: "cert missing a hosted cell -> fail", certCells: []string{"accesscore"}, colocated: []string{"accesscore", "configcore"}, wantErr: true},
		{name: "cert carries an unhosted extra cell -> fail", certCells: []string{"accesscore", "configcore"}, colocated: []string{"accesscore"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			certFile, keyFile, caFile := writeCellMaterialFor(t, tc.certCells...)
			cfg := celltls.Config{CertFile: certFile, KeyFile: keyFile, CAFile: caFile, TrustDomain: "example.org"}
			topo := topoColocated(t, tc.colocated,
				bootstrap.RemoteCellEndpoint{CellID: "auditcore", Endpoint: "https://audit.svc:8443"})
			_, err := celltls.Resolve(topo, cfg)
			if tc.wantErr {
				assert.Error(t, err, "cert cell-SAN set must exactly match the hosted (colocated) cells")
				return
			}
			assert.NoError(t, err)
		})
	}
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
