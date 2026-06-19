package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil/tlsutiltest"
)

func TestNewServerMTLSConfig_ValidSetsTLS13RequireAndCAs(t *testing.T) {
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		DNSNames: []string{"localhost"},
		EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	pool, err := NewClientCAPool(ca.CertPEM)
	require.NoError(t, err)

	cfg, err := NewServerMTLSConfig(leaf.CertPEM, leaf.KeyPEM, pool)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
	assert.Equal(t, tls.RequireAndVerifyClientCert, cfg.ClientAuth)
	assert.Same(t, pool, cfg.ClientCAs)
	require.Len(t, cfg.Certificates, 1)
}

func TestNewServerMTLSConfig_ErrorPaths(t *testing.T) {
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		DNSNames: []string{"localhost"},
		EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	pool, err := NewClientCAPool(ca.CertPEM)
	require.NoError(t, err)

	t.Run("empty_cert_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerMTLSConfig(nil, leaf.KeyPEM, pool)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("empty_key_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerMTLSConfig(leaf.CertPEM, nil, pool)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("malformed_cert_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerMTLSConfig([]byte("not a pem"), leaf.KeyPEM, pool)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("mismatched_cert_key_returns_error", func(t *testing.T) {
		otherLeaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
			DNSNames: []string{"localhost"},
			EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		cfg, err := NewServerMTLSConfig(leaf.CertPEM, otherLeaf.KeyPEM, pool)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("nil_ClientCAs_returns_error", func(t *testing.T) {
		cfg, err := NewServerMTLSConfig(leaf.CertPEM, leaf.KeyPEM, nil)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})
}

func TestNewClientCAPool_SingleAndMultipleBundles(t *testing.T) {
	caA := tlsutiltest.NewCA(t)
	caB := tlsutiltest.NewCA(t)

	t.Run("single_PEM_block", func(t *testing.T) {
		pool, err := NewClientCAPool(caA.CertPEM)
		require.NoError(t, err)
		require.NotNil(t, pool)
		assert.Len(t, pool.Subjects(), 1) //nolint:staticcheck // Subjects() simplest count; alternatives require typed-cert reflection
	})

	t.Run("multiple_PEM_blocks_merged", func(t *testing.T) {
		pool, err := NewClientCAPool(caA.CertPEM, caB.CertPEM)
		require.NoError(t, err)
		require.NotNil(t, pool)
		assert.Len(t, pool.Subjects(), 2) //nolint:staticcheck // Subjects() simplest count; alternatives require typed-cert reflection
	})

	t.Run("concatenated_PEM_blocks_merged", func(t *testing.T) {
		joined := append([]byte{}, caA.CertPEM...)
		joined = append(joined, caB.CertPEM...)
		pool, err := NewClientCAPool(joined)
		require.NoError(t, err)
		require.NotNil(t, pool)
		assert.Len(t, pool.Subjects(), 2) //nolint:staticcheck // Subjects() simplest count; alternatives require typed-cert reflection
	})
}

func TestNewClientCAPool_NoValidCertReturnsError(t *testing.T) {
	t.Run("no_input", func(t *testing.T) {
		pool, err := NewClientCAPool()
		assert.Error(t, err)
		assert.Nil(t, pool)
	})

	t.Run("garbage_input", func(t *testing.T) {
		pool, err := NewClientCAPool([]byte("not a cert"))
		assert.Error(t, err)
		assert.Nil(t, pool)
	})

	t.Run("empty_bytes", func(t *testing.T) {
		pool, err := NewClientCAPool([]byte{})
		assert.Error(t, err)
		assert.Nil(t, pool)
	})
}

// ─── NewServerTLSConfig ───────────────────────────────────────────────────────

func TestNewServerTLSConfig_Valid(t *testing.T) {
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		DNSNames: []string{"localhost"},
		EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	cfg, err := NewServerTLSConfig(leaf.CertPEM, leaf.KeyPEM)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion, "must pin TLS 1.3")
	assert.Equal(t, tls.NoClientCert, cfg.ClientAuth, "must not require client cert")
	require.Len(t, cfg.Certificates, 1)
}

func TestNewServerTLSConfig_ErrorPaths(t *testing.T) {
	ca := tlsutiltest.NewCA(t)
	leaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		DNSNames: []string{"localhost"},
		EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	t.Run("empty_cert_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerTLSConfig(nil, leaf.KeyPEM)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("empty_key_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerTLSConfig(leaf.CertPEM, nil)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("cert_key_parse_error_returns_error", func(t *testing.T) {
		cfg, err := NewServerTLSConfig([]byte("not a pem"), leaf.KeyPEM)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("mismatched_cert_key_returns_error", func(t *testing.T) {
		otherLeaf := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
			DNSNames: []string{"localhost"},
			EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		cfg, err := NewServerTLSConfig(leaf.CertPEM, otherLeaf.KeyPEM)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})
}

// TestNewClientCAPool_FailsClosedOnBadBundleInMix locks the fail-closed
// contract (F4 / cluster C2): a non-contributing bundle anywhere in the
// variadic input fails the whole construction, regardless of position, even
// when another bundle is valid. The pre-fix code returned a pool that
// silently omitted the bad bundle's intended anchors.
func TestNewClientCAPool_FailsClosedOnBadBundleInMix(t *testing.T) {
	ca := tlsutiltest.NewCA(t)

	t.Run("valid_then_garbage_returns_error", func(t *testing.T) {
		pool, err := NewClientCAPool(ca.CertPEM, []byte("not a cert"))
		assert.Error(t, err)
		assert.Nil(t, pool)
	})

	t.Run("valid_then_empty_returns_error", func(t *testing.T) {
		pool, err := NewClientCAPool(ca.CertPEM, []byte{})
		assert.Error(t, err)
		assert.Nil(t, pool)
	})

	t.Run("garbage_then_valid_returns_error", func(t *testing.T) {
		pool, err := NewClientCAPool([]byte("not a cert"), ca.CertPEM)
		assert.Error(t, err)
		assert.Nil(t, pool)
	})
}
