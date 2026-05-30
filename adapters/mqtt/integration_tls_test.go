//go:build integration && mqtt_tls

package mqtt

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/tests/testutil"
)

// TEST-TIME-LITERAL-01: file-local const prevents bare time.Duration literals in test bodies.
const tlsTestCertValidity = time.Hour

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
//
// Ported from adapters/grpc/cert_test.go::genIntegChain with naming adapted
// for the MQTT TLS context.
func genMQTTTLSChain(t *testing.T) mqttTLSChain {
	t.Helper()

	// Root CA.
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: root CA key: %v", err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mqtt-test-root"},
		NotBefore:             time.Now().Add(-tlsTestCertValidity),
		NotAfter:              time.Now().Add(tlsTestCertValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: root CA cert: %v", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: parse root CA: %v", err)
	}
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})

	// Server leaf — SAN includes 127.0.0.1 + localhost for TLS dial verification.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: server key: %v", err)
	}
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "mqtt-test-server"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-tlsTestCertValidity),
		NotAfter:     time.Now().Add(tlsTestCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, rootCert, &serverKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: server cert: %v", err)
	}
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: marshal server key: %v", err)
	}
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})

	// Client leaf — ExtKeyUsageClientAuth required for mTLS peer verification.
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: client key: %v", err)
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "mqtt-test-client"},
		NotBefore:    time.Now().Add(-tlsTestCertValidity),
		NotAfter:     time.Now().Add(tlsTestCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, rootCert, &clientKey.PublicKey, rootKey)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: client cert: %v", err)
	}
	clientLeaf, err := x509.ParseCertificate(clientDER)
	if err != nil {
		t.Fatalf("genMQTTTLSChain: parse client cert: %v", err)
	}

	return mqttTLSChain{
		caCertPEM:     caCertPEM,
		serverCertPEM: serverCertPEM,
		serverKeyPEM:  serverKeyPEM,
		clientCert: tls.Certificate{
			Certificate: [][]byte{clientDER},
			PrivateKey:  clientKey,
			Leaf:        clientLeaf,
		},
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
	cfg := Config{
		ClientID: cid,
		Brokers:  []string{brokerURL},
		TLS: &tls.Config{
			RootCAs:      caPool,
			Certificates: []tls.Certificate{chain.clientCert},
			ServerName:   "127.0.0.1",
		},
		ConnectTimeout: testtime.D10s,
		KeepAlive:      testtime.D10s,
		Backoff: BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
		PublishTimeout: testtime.D5s,
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

// TestIntegrationTLS_InsecureSkipVerify_Rejected asserts that Config validation
// rejects a TLS config with InsecureSkipVerify=true before any network connection
// is attempted. This test requires no container — it is a pure validation check.
func TestIntegrationTLS_InsecureSkipVerify_Rejected(t *testing.T) {
	cid, err := ParseEphemeralClientID("itest", "tls-insecure")
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	cfg := Config{
		ClientID: cid,
		Brokers:  []string{"tls://127.0.0.1:8883"},
		TLS: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // intentional: this is what we are testing rejection of
		},
		ConnectTimeout: testtime.D5s,
		KeepAlive:      testtime.D10s,
		Backoff: BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
	}

	valErr := cfg.Validate()
	if valErr == nil {
		t.Fatal("expected Validate to reject InsecureSkipVerify=true, got nil")
	}
	var ec *errcode.Error
	if !errors.As(valErr, &ec) {
		t.Fatalf("expected *errcode.Error, got: %T: %v", valErr, valErr)
	}
	if ec.Code != ErrAdapterMQTTInvalidConfig {
		t.Fatalf("error code = %v, want %v", ec.Code, ErrAdapterMQTTInvalidConfig)
	}
}
