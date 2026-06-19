package grpc_test

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"

	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil/tlsutiltest"
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

// genIntegChain produces a self-signed CA + server leaf (SAN: 127.0.0.1 + ::1 +
// localhost) + client leaf (ExtKeyUsageClientAuth). Uses ECDSA P-256.
//
// The server SAN must include 127.0.0.1 and localhost so the gRPC client's TLS
// handshake verification succeeds when dialing those addresses.
func genIntegChain(t *testing.T) integTestChain {
	t.Helper()

	ca := tlsutiltest.NewCA(t)

	server := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		DNSNames: []string{"localhost"},
		IPs:      []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	// Client leaf — ExtKeyUsageClientAuth required for mTLS peer verification.
	// No SPIFFE URI: this is a plain mTLS client, not a workload identity principal.
	client := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		EKU: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	return integTestChain{
		rootCertPEM:   ca.CertPEM,
		serverCertPEM: server.CertPEM,
		serverKeyPEM:  server.KeyPEM,
		clientCert:    client.TLSCert,
		clientLeaf:    client.Cert,
	}
}
