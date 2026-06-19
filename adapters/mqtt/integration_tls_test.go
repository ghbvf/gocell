//go:build integration && mqtt_tls

package mqtt

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil/tlsutiltest"
	"github.com/ghbvf/gocell/tests/testutil"
)

// mqttTLSChain holds PEM-encoded server materials and a ready-to-use client
// tls.Certificate for a full mTLS round-trip test against a mosquitto broker.
type mqttTLSChain struct {
	caCertPEM     []byte
	serverCertPEM []byte
	serverKeyPEM  []byte
	clientCert    tls.Certificate
}

// genMQTTTLSChain produces a self-signed CA + server leaf (SAN: 127.0.0.1 +
// localhost) + client leaf (ExtKeyUsageClientAuth). Uses ECDSA P-256.
func genMQTTTLSChain(t *testing.T) mqttTLSChain {
	t.Helper()

	ca := tlsutiltest.NewCA(t)

	// Server leaf — SAN includes 127.0.0.1 + localhost for TLS dial verification.
	server := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		DNSNames: []string{"localhost"},
		IPs:      []net.IP{net.ParseIP("127.0.0.1")},
		EKU:      []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	// Client leaf — ExtKeyUsageClientAuth required for mTLS peer verification.
	client := ca.IssueLeaf(t, tlsutiltest.LeafOptions{
		EKU: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	return mqttTLSChain{
		caCertPEM:     ca.CertPEM,
		serverCertPEM: server.CertPEM,
		serverKeyPEM:  server.KeyPEM,
		clientCert:    client.TLSCert,
	}
}

// mosquittoTLSConf returns the in-memory mosquitto v2 TLS listener configuration.
// Certificates are mounted at the paths referenced here by startMosquittoContainerTLS.
func mosquittoTLSConf() string {
	return `listener 8883 0.0.0.0
protocol mqtt
cafile /mosquitto/config/ca.crt
certfile /mosquitto/config/server.crt
keyfile /mosquitto/config/server.key
require_certificate true
use_identity_as_username true
allow_anonymous true
`
}

// startMosquittoContainerTLS starts an eclipse-mosquitto v2 broker with a TLS
// listener on port 8883 using the provided server cert/key/CA PEM bytes.
// Cert files are mounted as ContainerFile entries. The returned url has the form
// "tls://127.0.0.1:<host-port>" and cleanup terminates the container.
//
// ref: startMosquittoContainer in testmain_integration_test.go for the
// GenericContainer + ContainerFile pattern.
func startMosquittoContainerTLS(
	t *testing.T,
	serverCertPEM, serverKeyPEM, caCertPEM []byte,
) (url string, cleanup func()) {
	t.Helper()
	testutil.RequireDocker(t)
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testutil.MosquittoImage,
			ExposedPorts: []string{"8883/tcp"},
			Files: []testcontainers.ContainerFile{
				{
					Reader:            strings.NewReader(mosquittoTLSConf()),
					ContainerFilePath: "/mosquitto/config/mosquitto.conf",
					FileMode:          0o644,
				},
				{
					Reader:            bytes.NewReader(serverCertPEM),
					ContainerFilePath: "/mosquitto/config/server.crt",
					FileMode:          0o644,
				},
				{
					Reader:            bytes.NewReader(serverKeyPEM),
					ContainerFilePath: "/mosquitto/config/server.key",
					FileMode:          0o600,
				},
				{
					Reader:            bytes.NewReader(caCertPEM),
					ContainerFilePath: "/mosquitto/config/ca.crt",
					FileMode:          0o644,
				},
			},
			WaitingFor: wait.ForListeningPort("8883/tcp").
				WithStartupTimeout(testtime.D30s),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("startMosquittoContainerTLS: container start: %v", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("startMosquittoContainerTLS: host: %v", err)
	}
	port, err := container.MappedPort(ctx, "8883/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("startMosquittoContainerTLS: mapped port: %v", err)
	}
	brokerURL := fmt.Sprintf("tls://%s:%s", host, port.Port())
	cleanupFn := func() {
		termCtx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
		defer cancel()
		_ = container.Terminate(termCtx)
	}
	return brokerURL, cleanupFn
}

// TestIntegrationTLS_MutualTLS_PublishRoundTrip verifies end-to-end mTLS publish
// against a real Mosquitto v2 broker: generates a CA+server+client chain, starts
// a TLS-only mosquitto, opens a connection with mutual TLS, and asserts a QoS1
// publish (PUBACK) succeeds.
func TestIntegrationTLS_MutualTLS_PublishRoundTrip(t *testing.T) {
	chain := genMQTTTLSChain(t)

	brokerURL, cleanup := startMosquittoContainerTLS(
		t,
		chain.serverCertPEM,
		chain.serverKeyPEM,
		chain.caCertPEM,
	)
	t.Cleanup(cleanup)

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(chain.caCertPEM) {
		t.Fatal("TestIntegrationTLS_MutualTLS_PublishRoundTrip: failed to append CA cert to pool")
	}

	cid, err := ParseEphemeralClientID("itest", "tls-roundtrip")
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	cfg, cfgErr := NewConfig(cid, []string{brokerURL},
		WithTLS(&tls.Config{
			RootCAs:      caPool,
			Certificates: []tls.Certificate{chain.clientCert},
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS12,
		}),
		WithConnectTimeout(testtime.D10s),
		WithConnectDeadline(testtime.D10s),
		WithKeepAlive(testtime.D10s),
		WithBackoff(BackoffConfig{BaseDelay: testtime.D100ms, MaxDelay: testtime.D2s}),
		WithPublishTimeout(testtime.D5s),
	)
	if cfgErr != nil {
		t.Fatalf("NewConfig: %v", cfgErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D30s)
	defer cancel()

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open (mTLS): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/tls/" + uuid.NewString()
	if err := pub.Publish(ctx, topic, []byte(`{"hello":"mtls"}`)); err != nil {
		t.Fatalf("Publish over mTLS: %v", err)
	}
}

// TestIntegrationTLS_UntrustedClientCert_Rejected asserts that an mTLS broker
// rejects a client whose certificate is signed by a CA the broker does NOT trust,
// while the client itself trusts the server. This is the security regression gate
// for `require_certificate true`: it proves mutual-auth enforcement is real.
//
// Setup: trusted chain (brokerCA) → broker server cert; separate, independent
// chain (untrustedCA) → client cert. The broker's cafile trusts ONLY brokerCA.
// The client's RootCAs trust the server cert (brokerCA) so the TLS client-side
// server-auth passes; only the broker's client-auth check fails.
func TestIntegrationTLS_UntrustedClientCert_Rejected(t *testing.T) {
	// Trusted chain: broker server cert + CA. The broker only trusts this CA.
	trusted := genMQTTTLSChain(t)
	brokerURL, cleanup := startMosquittoContainerTLS(
		t,
		trusted.serverCertPEM,
		trusted.serverKeyPEM,
		trusted.caCertPEM,
	)
	t.Cleanup(cleanup)

	// Untrusted chain: independent CA + client cert. Broker's cafile does NOT
	// contain this CA, so the broker will reject this client cert during mTLS.
	untrusted := genMQTTTLSChain(t)

	// Client trusts the SERVER (uses trusted.caCertPEM) so TLS client-side
	// server-auth succeeds. The failure is strictly the BROKER rejecting the
	// CLIENT cert (mutual-auth enforcement).
	serverCAPool := x509.NewCertPool()
	if !serverCAPool.AppendCertsFromPEM(trusted.caCertPEM) {
		t.Fatal("TestIntegrationTLS_UntrustedClientCert_Rejected: failed to append trusted CA cert")
	}

	cid, err := ParseEphemeralClientID("itest", "tls-untrusted-client")
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	cfg, cfgErr := NewConfig(cid, []string{brokerURL},
		WithTLS(&tls.Config{
			RootCAs:      serverCAPool,
			Certificates: []tls.Certificate{untrusted.clientCert}, // NOT trusted by broker
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS12,
		}),
		WithConnectTimeout(testtime.D10s),
		WithConnectDeadline(testtime.D10s),
		WithKeepAlive(testtime.D10s),
		WithBackoff(BackoffConfig{BaseDelay: testtime.D100ms, MaxDelay: testtime.D2s}),
		WithPublishTimeout(testtime.D5s),
	)
	if cfgErr != nil {
		t.Fatalf("NewConfig: %v", cfgErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D30s)
	defer cancel()

	_, openErr := Open(ctx, clock.Real(), cfg)
	if openErr == nil {
		t.Fatal("TestIntegrationTLS_UntrustedClientCert_Rejected: Open succeeded; " +
			"expected broker to reject the untrusted client cert during mTLS handshake — " +
			"require_certificate is NOT being enforced")
	}
	// The exact error shape varies by broker / paho version (CONNACK, TLS alert,
	// connection reset). Assert only that Open fails — the broker rejected the
	// client cert. Do not over-constrain the error code.
	t.Logf("Open returned (expected) error: %v", openErr)
}

// TestIntegrationTLS_NoClientCert_Rejected asserts that a client presenting NO
// client certificate is rejected by the mTLS broker (require_certificate true).
// The client trusts the server (RootCAs = trusted CA pool) so the client-side
// server-auth step succeeds; the broker rejects the connection during its
// client-auth check because no certificate is presented. This is the
// require_certificate enforcement gate — distinct from
// TestIntegrationTLS_UntrustedClientCert_Rejected (which sends a cert signed by
// the wrong CA).
func TestIntegrationTLS_NoClientCert_Rejected(t *testing.T) {
	chain := genMQTTTLSChain(t)
	brokerURL, cleanup := startMosquittoContainerTLS(
		t,
		chain.serverCertPEM,
		chain.serverKeyPEM,
		chain.caCertPEM,
	)
	t.Cleanup(cleanup)

	serverCAPool := x509.NewCertPool()
	if !serverCAPool.AppendCertsFromPEM(chain.caCertPEM) {
		t.Fatal("TestIntegrationTLS_NoClientCert_Rejected: failed to append CA cert to pool")
	}

	cid, err := ParseEphemeralClientID("itest", "tls-no-cert")
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	// No Certificates field — client presents no cert during mTLS handshake.
	cfg, cfgErr := NewConfig(cid, []string{brokerURL},
		WithTLS(&tls.Config{
			RootCAs:    serverCAPool,
			ServerName: "127.0.0.1",
			MinVersion: tls.VersionTLS12,
		}),
		WithConnectTimeout(testtime.D10s),
		WithConnectDeadline(testtime.D10s),
		WithKeepAlive(testtime.D10s),
		WithBackoff(BackoffConfig{BaseDelay: testtime.D100ms, MaxDelay: testtime.D2s}),
		WithPublishTimeout(testtime.D5s),
	)
	if cfgErr != nil {
		t.Fatalf("NewConfig: %v", cfgErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D30s)
	defer cancel()

	_, openErr := Open(ctx, clock.Real(), cfg)
	if openErr == nil {
		t.Fatal("TestIntegrationTLS_NoClientCert_Rejected: Open succeeded; " +
			"expected broker to reject a client presenting no certificate — " +
			"require_certificate true is NOT being enforced")
	}
	// The exact error shape varies (TLS alert, connection reset, CONNACK, or
	// connect-timeout after retry budget). Assert only that Open fails.
	t.Logf("Open returned (expected) error: %v", openErr)
}

// TestIntegrationTLS_InsecureSkipVerify_Rejected asserts that Config validation
// rejects a TLS config with InsecureSkipVerify=true before any network connection
// is attempted. This test requires no container — it is a pure validation check.
func TestIntegrationTLS_InsecureSkipVerify_Rejected(t *testing.T) {
	cid, err := ParseEphemeralClientID("itest", "tls-insecure")
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	_, valErr := NewConfig(cid, []string{"tls://127.0.0.1:8883"},
		WithTLS(&tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // intentional: this is what we are testing rejection of
		}),
		WithConnectTimeout(testtime.D5s),
		WithConnectDeadline(testtime.D10s),
		WithKeepAlive(testtime.D10s),
		WithBackoff(BackoffConfig{BaseDelay: testtime.D100ms, MaxDelay: testtime.D2s}),
	)
	if valErr == nil {
		t.Fatal("expected NewConfig to reject InsecureSkipVerify=true, got nil")
	}
	var ec *errcode.Error
	if !errors.As(valErr, &ec) {
		t.Fatalf("expected *errcode.Error, got: %T: %v", valErr, valErr)
	}
	if ec.Code != ErrAdapterMQTTInvalidConfig {
		t.Fatalf("error code = %v, want %v", ec.Code, ErrAdapterMQTTInvalidConfig)
	}
}
