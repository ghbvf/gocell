package deviceidentity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
)

// ─── mtls_test.go ────────────────────────────────────────────────────────────
//
// Unit tests for NewDeviceMTLSServerConfig and newEphemeralServerCert.
// Uses fakeSigner (defined in service_test.go) and clockmock.

// TestNewDeviceMTLSServerConfig covers the two visible outcomes:
//   - empty CA bundle → error (cannot build client CA pool)
//   - valid CA bundle → returns a *tls.Config with RequireAnyClientCert and
//     non-empty ClientCAs
func TestNewDeviceMTLSServerConfig(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(testClk(t).Now())

	t.Run("empty_bundle_returns_error", func(t *testing.T) {
		t.Parallel()
		signer := &fakeSigner{trustBundle: nil}
		cfg, err := NewDeviceMTLSServerConfig(context.Background(), clk, signer)
		if err == nil {
			t.Fatal("expected error for empty trust bundle, got nil")
		}
		if cfg != nil {
			t.Fatal("expected nil *tls.Config on error")
		}
	})

	t.Run("valid_bundle_returns_config", func(t *testing.T) {
		t.Parallel()
		// fakeSigner.TrustBundle returns a real self-signed cert DER so
		// tlsutil.NewClientCAPool can parse it.
		signer := &fakeSigner{trustBundle: [][]byte{testCertDER(t)}}
		cfg, err := NewDeviceMTLSServerConfig(context.Background(), clk, signer)
		if err != nil {
			t.Fatalf("NewDeviceMTLSServerConfig: unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil *tls.Config")
		}
		// The device-mTLS listener must require client certs.
		if cfg.ClientAuth < tls.RequireAnyClientCert {
			t.Errorf("ClientAuth = %v, want >= RequireAnyClientCert", cfg.ClientAuth)
		}
		if cfg.ClientCAs == nil {
			t.Error("ClientCAs must be set (non-nil) when a trust bundle is provided")
		}
	})

	t.Run("signer_error_returns_error", func(t *testing.T) {
		t.Parallel()
		signer := &fakeSigner{err: errSignerDown}
		cfg, err := NewDeviceMTLSServerConfig(context.Background(), clk, signer)
		if err == nil {
			t.Fatal("expected error when signer returns an error")
		}
		if cfg != nil {
			t.Fatal("expected nil *tls.Config on signer error")
		}
	})
}

// errSignerDown is a sentinel for fakeSigner error injection in mtls tests.
var errSignerDown = errors.New("signer down")

// TestNewEphemeralServerCert verifies that the generated PEM pair:
//   - can be parsed as a valid x509.Certificate
//   - contains "localhost" in DNS SANs
//   - has a non-nil private key PEM block
func TestNewEphemeralServerCert(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(testClk(t).Now())

	certPEM, keyPEM, err := newEphemeralServerCert(clk)
	if err != nil {
		t.Fatalf("newEphemeralServerCert: %v", err)
	}

	// Cert PEM must decode to a parseable certificate.
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("certPEM: pem.Decode returned nil block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate: %v", err)
	}

	// DNS SANs must include "localhost".
	foundLocalhost := false
	for _, dns := range cert.DNSNames {
		if dns == "localhost" {
			foundLocalhost = true
			break
		}
	}
	if !foundLocalhost {
		t.Errorf("expected 'localhost' in DNSNames, got %v", cert.DNSNames)
	}

	// Key PEM must decode to a non-empty block.
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("keyPEM: pem.Decode returned nil block")
	}
	if len(keyBlock.Bytes) == 0 {
		t.Fatal("keyPEM block bytes are empty")
	}
}
