package grpc_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	// testCertValidity is the validity window for self-signed test certificates.
	// TEST-TIME-LITERAL-01: file-local const prevents bare time.Duration literals in test bodies.
	testCertValidity = time.Hour
)

// integTestChain holds PEM-encoded server materials and a ready-to-use client
// tls.Certificate for a full TLS/mTLS round-trip test.
type integTestChain struct {
	rootCertPEM   []byte
	serverCertPEM []byte
	serverKeyPEM  []byte
	clientCert    tls.Certificate
	clientLeaf    *x509.Certificate
}

// genIntegChain produces a self-signed CA + server leaf (SAN: 127.0.0.1 + localhost)
// + client leaf (ExtKeyUsageClientAuth). Uses ECDSA P-256.
//
// The server SAN must include 127.0.0.1 and localhost so the gRPC client's TLS
// handshake verification succeeds when dialing those addresses.
func genIntegChain(t *testing.T) integTestChain {
	t.Helper()

	// Root CA.
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "grpc-test-root"},
		NotBefore:             time.Now().Add(-testCertValidity),
		NotAfter:              time.Now().Add(testCertValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	rootCert, err := x509.ParseCertificate(rootDER)
	require.NoError(t, err)
	rootCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})

	// Server leaf — SAN must include 127.0.0.1 + localhost for TLS dial verification.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "grpc-test-server"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:    time.Now().Add(-testCertValidity),
		NotAfter:     time.Now().Add(testCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, rootCert, &serverKey.PublicKey, rootKey)
	require.NoError(t, err)
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	require.NoError(t, err)
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})

	// Client leaf — ExtKeyUsageClientAuth required for mTLS peer verification.
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "grpc-test-client"},
		NotBefore:    time.Now().Add(-testCertValidity),
		NotAfter:     time.Now().Add(testCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, rootCert, &clientKey.PublicKey, rootKey)
	require.NoError(t, err)
	clientLeaf, err := x509.ParseCertificate(clientDER)
	require.NoError(t, err)

	return integTestChain{
		rootCertPEM:   rootCertPEM,
		serverCertPEM: serverCertPEM,
		serverKeyPEM:  serverKeyPEM,
		clientCert: tls.Certificate{
			Certificate: [][]byte{clientDER},
			PrivateKey:  clientKey,
			Leaf:        clientLeaf,
		},
		clientLeaf: clientLeaf,
	}
}
