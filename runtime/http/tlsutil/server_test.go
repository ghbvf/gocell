package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testChain holds PEM-encoded materials produced by genTestChain.
type testChain struct {
	rootCertPEM   []byte
	serverCertPEM []byte
	serverKeyPEM  []byte
}

// genTestChain produces a self-signed root CA + a server leaf signed by the
// root, all in PEM form. Uses ECDSA P-256 (fast, ~1ms total). The test only
// needs material that tls.X509KeyPair will accept and x509.AppendCertsFromPEM
// will parse — no real handshake is performed at the tlsutil package level.
func genTestChain(t *testing.T) testChain {
	t.Helper()

	// Root CA.
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	rootCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})

	// Server leaf.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-server"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	require.NoError(t, err)
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, rootCert, &serverKey.PublicKey, rootKey)
	require.NoError(t, err)
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	require.NoError(t, err)
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})

	return testChain{
		rootCertPEM:   rootCertPEM,
		serverCertPEM: serverCertPEM,
		serverKeyPEM:  serverKeyPEM,
	}
}

func TestNewServerMTLSConfig_ValidSetsTLS13RequireAndCAs(t *testing.T) {
	chain := genTestChain(t)
	pool, err := NewClientCAPool(chain.rootCertPEM)
	require.NoError(t, err)

	cfg, err := NewServerMTLSConfig(chain.serverCertPEM, chain.serverKeyPEM, pool)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
	assert.Equal(t, tls.RequireAndVerifyClientCert, cfg.ClientAuth)
	assert.Same(t, pool, cfg.ClientCAs)
	require.Len(t, cfg.Certificates, 1)
}

func TestNewServerMTLSConfig_ErrorPaths(t *testing.T) {
	chain := genTestChain(t)
	pool, err := NewClientCAPool(chain.rootCertPEM)
	require.NoError(t, err)

	t.Run("empty_cert_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerMTLSConfig(nil, chain.serverKeyPEM, pool)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("empty_key_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerMTLSConfig(chain.serverCertPEM, nil, pool)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("malformed_cert_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerMTLSConfig([]byte("not a pem"), chain.serverKeyPEM, pool)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("mismatched_cert_key_returns_error", func(t *testing.T) {
		other := genTestChain(t)
		cfg, err := NewServerMTLSConfig(chain.serverCertPEM, other.serverKeyPEM, pool)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("nil_ClientCAs_returns_error", func(t *testing.T) {
		cfg, err := NewServerMTLSConfig(chain.serverCertPEM, chain.serverKeyPEM, nil)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})
}

func TestNewClientCAPool_SingleAndMultipleBundles(t *testing.T) {
	a := genTestChain(t)
	b := genTestChain(t)

	t.Run("single_PEM_block", func(t *testing.T) {
		pool, err := NewClientCAPool(a.rootCertPEM)
		require.NoError(t, err)
		require.NotNil(t, pool)
		assert.Len(t, pool.Subjects(), 1) //nolint:staticcheck // Subjects() simplest count; alternatives require typed-cert reflection
	})

	t.Run("multiple_PEM_blocks_merged", func(t *testing.T) {
		pool, err := NewClientCAPool(a.rootCertPEM, b.rootCertPEM)
		require.NoError(t, err)
		require.NotNil(t, pool)
		assert.Len(t, pool.Subjects(), 2) //nolint:staticcheck // Subjects() simplest count; alternatives require typed-cert reflection
	})

	t.Run("concatenated_PEM_blocks_merged", func(t *testing.T) {
		joined := append([]byte{}, a.rootCertPEM...)
		joined = append(joined, b.rootCertPEM...)
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
	chain := genTestChain(t)

	cfg, err := NewServerTLSConfig(chain.serverCertPEM, chain.serverKeyPEM)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion, "must pin TLS 1.3")
	assert.Equal(t, tls.NoClientCert, cfg.ClientAuth, "must not require client cert")
	require.Len(t, cfg.Certificates, 1)
}

func TestNewServerTLSConfig_ErrorPaths(t *testing.T) {
	chain := genTestChain(t)

	t.Run("empty_cert_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerTLSConfig(nil, chain.serverKeyPEM)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("empty_key_PEM_returns_error", func(t *testing.T) {
		cfg, err := NewServerTLSConfig(chain.serverCertPEM, nil)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("cert_key_parse_error_returns_error", func(t *testing.T) {
		cfg, err := NewServerTLSConfig([]byte("not a pem"), chain.serverKeyPEM)
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("mismatched_cert_key_returns_error", func(t *testing.T) {
		other := genTestChain(t)
		cfg, err := NewServerTLSConfig(chain.serverCertPEM, other.serverKeyPEM)
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
	valid := genTestChain(t)

	t.Run("valid_then_garbage_returns_error", func(t *testing.T) {
		pool, err := NewClientCAPool(valid.rootCertPEM, []byte("not a cert"))
		assert.Error(t, err)
		assert.Nil(t, pool)
	})

	t.Run("valid_then_empty_returns_error", func(t *testing.T) {
		pool, err := NewClientCAPool(valid.rootCertPEM, []byte{})
		assert.Error(t, err)
		assert.Nil(t, pool)
	})

	t.Run("garbage_then_valid_returns_error", func(t *testing.T) {
		pool, err := NewClientCAPool([]byte("not a cert"), valid.rootCertPEM)
		assert.Error(t, err)
		assert.Nil(t, pool)
	})
}
