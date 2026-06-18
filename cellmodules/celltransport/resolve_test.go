package celltransport_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cellmodules/celltransport"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/http/tlsutil"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// Test-time durations as file-local consts (TEST-TIME-LITERAL-01: site-specific
// test deadlines, not inline literals).
const (
	testReadinessProbeDeadline = 200 * time.Millisecond // short ctx so a silent peer handshake fails fast
	testCAValidity             = 2 * time.Hour          // test CA cert validity window
	testSNICaptureWait         = 2 * time.Second        // upper bound waiting for the captured ClientHello SNI
)

func assertKindInternal(t *testing.T, err error) {
	t.Helper()
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Kind != errcode.KindInternal {
		t.Errorf("Kind = %v, want KindInternal", ec.Kind)
	}
}

// assertMessageContains asserts the errcode message names the expected failure
// class. The diagnostic naming is the #2278 PR-3 contribution: the eager
// celltransport.Resolve seam IS the sync-dimension missing-dependency runtime
// fail-fast (ADR 202606131142-1423 §#1967), so its two failure paths must name
// their class ("topology under-declared" / "local dependency missing") rather
// than a generic "not classified" string. Asserts on ec.Message (not err.Error,
// which renders the internal cellID attr when present).
func assertMessageContains(t *testing.T, err error, want string) {
	t.Helper()
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if !strings.Contains(ec.Message, want) {
		t.Errorf("error message %q does not name failure class %q", ec.Message, want)
	}
}

func remoteTopo(t *testing.T, endpoint string) bootstrap.DeploymentTopology {
	t.Helper()
	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Remote: []bootstrap.RemoteCellEndpoint{{CellID: "configcore", Endpoint: endpoint}},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}
	return topo
}

// TestResolve_Colocated verifies that a co-located cellID returns the inProc transport.
func TestResolve_Colocated(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Colocated: []string{"configcore"},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	got, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != inProc {
		t.Errorf("Resolve returned %v, want %v (inProc)", got, inProc)
	}
	// Co-located has no remote peer → contributes no readiness probe resource.
	if len(res) != 0 {
		t.Errorf("co-located Resolve returned %d resources, want 0", len(res))
	}
}

// TestResolve_Remote verifies that a remote cellID returns a non-nil
// *transport.RemoteHTTPTransport distinct from inProc.
func TestResolve_Remote(t *testing.T) {
	t.Parallel()

	topo := remoteTopo(t, "127.0.0.1:9090")
	inProc := transport.NewInProcess(nil)
	got, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil {
		t.Fatal("Resolve returned nil for remote cell")
	}
	if got == transport.CellTransport(inProc) {
		t.Error("Resolve returned inProc for a remote cell — expected a RemoteHTTPTransport")
	}
	if _, ok := got.(*transport.RemoteHTTPTransport); !ok {
		t.Errorf("Resolve returned %T for remote cell, want *transport.RemoteHTTPTransport", got)
	}
}

// TestResolve_NilClockPanics verifies that passing a nil clock to Resolve
// triggers a registered panic (clock.MustHaveClock inside Resolve).
func TestResolve_NilClockPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil clock in celltransport.Resolve, got none")
		}
	}()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Colocated: []string{"configcore"},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	_, _, _ = celltransport.Resolve(topo, "configcore", inProc, nil, transport.CrossCellObs{}, tlsutil.ClientIdentity{})
}

// TestResolve_UnclassifiedCellReturnsKindInternal verifies defense-in-depth for
// cells that are neither co-located nor remote in an explicit topology.
func TestResolve_UnclassifiedCellReturnsKindInternal(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Colocated: []string{"accesscore"},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	_, _, resolveErr := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if resolveErr == nil {
		t.Fatal("expected error for unclassified cell, got nil")
	}
	assertKindInternal(t, resolveErr)
	// Failure class: the consumed provider is absent from the topology partition
	// (neither co-located nor remote) — "topology under-declared".
	assertMessageContains(t, resolveErr, "topology under-declared")
}

// TestResolve_ColocatedNilInProcReturnsError verifies that a nil inProc
// transport for a co-located cell returns KindInternal.
func TestResolve_ColocatedNilInProcReturnsError(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{
		Colocated: []string{"configcore"},
	})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	_, _, resolveErr := celltransport.Resolve(topo, "configcore", nil, clock.Real(), transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if resolveErr == nil {
		t.Fatal("expected error for nil inProc, got nil")
	}
	assertKindInternal(t, resolveErr)
	// Failure class: the provider is declared co-located but its in-process
	// transport was not minted — the local provider is not mounted here ("local
	// dependency missing").
	assertMessageContains(t, resolveErr, "local dependency missing")
}

// TestResolve_ZeroTopoIsColocated verifies that a zero topology (all-colocated
// default) returns the inProc transport for any cellID.
func TestResolve_ZeroTopoIsColocated(t *testing.T) {
	t.Parallel()

	topo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{})
	if err != nil {
		t.Fatalf("NewDeploymentTopology: %v", err)
	}

	inProc := transport.NewInProcess(nil)
	got, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != inProc {
		t.Errorf("Resolve returned %v, want inProc for zero topo", got)
	}
}

// --- #2251 P1.3: tracer propagation through the sealed bundle ---

// TestResolve_Remote_PropagatesTracer asserts the remote transport opens a span
// with transport_mode=remote using the tracer carried by the CrossCellObs bundle
// (closes ADR D4 span half: remote calls were previously NoopTracer-only).
func TestResolve_Remote_PropagatesTracer(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := &recTracer{}
	topo := remoteTopo(t, srv.URL)
	inProc := transport.NewInProcess(nil)

	ct, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.NewCrossCellObs(nil, rec), tlsutil.ClientIdentity{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "http://placeholder/internal/v1/config/k", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := ct.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	_ = resp.Body.Close()

	if !rec.hasAttr("transport_mode", "remote") {
		t.Error("remote DoContract span missing transport_mode=remote attr (tracer not propagated)")
	}
	if !rec.hasAttr("contract.id", "http.config.internal.get.v1") {
		t.Error("remote DoContract span missing contract.id attr")
	}
}

// --- #2251 P2.7: remote-peer readiness probe ---

// TestResolve_Remote_ContributesReadinessProbe asserts the remote branch returns
// exactly one readiness ManagedResource named "<cell>_remote_ready".
func TestResolve_Remote_ContributesReadinessProbe(t *testing.T) {
	t.Parallel()

	topo := remoteTopo(t, "127.0.0.1:9090")
	inProc := transport.NewInProcess(nil)

	_, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("remote Resolve returned %d resources, want 1", len(res))
	}
	probes := res[0].Probes()
	if len(probes) != 1 {
		t.Fatalf("readiness resource exposed %d probes, want 1", len(probes))
	}
	if got := probes[0].Name().String(); got != "configcore_remote_ready" {
		t.Errorf("probe name = %q, want configcore_remote_ready", got)
	}
	// The readiness resource owns no goroutine and nothing to close.
	if res[0].Worker() != nil {
		t.Error("readiness resource Worker() must be nil")
	}
	if err := res[0].Close(context.Background()); err != nil {
		t.Errorf("readiness resource Close() = %v, want nil", err)
	}
}

// TestResolve_RemoteProbe_TCPDial is the cascade-safety core: the probe TCP-dials
// the peer endpoint only (it does NOT issue an HTTP /readyz), so a peer that
// accepts TCP but speaks no HTTP is still reported ready — proving no recursive
// /readyz cascade. A closed endpoint degrades the probe; a canceled ctx errors.
func TestResolve_RemoteProbe_TCPDial(t *testing.T) {
	t.Parallel()

	// A raw TCP listener that accepts connections but never speaks HTTP.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	probe := remoteReadinessProbe(t, ln.Addr().String())

	// Reachable: TCP dial succeeds even though the listener never sends HTTP.
	if err := probe.Check(context.Background()); err != nil {
		t.Errorf("probe.Check on reachable raw-TCP peer = %v, want nil (TCP-only, no /readyz)", err)
	}

	// Canceled ctx must surface as an error (honors the /readyz deadline).
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := probe.Check(cctx); err == nil {
		t.Error("probe.Check with canceled ctx = nil, want error")
	}

	// Unreachable: bind+close to get a (very likely) refused address.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(dead): %v", err)
	}
	deadAddr := dead.Addr().String()
	_ = dead.Close()
	deadProbe := remoteReadinessProbe(t, deadAddr)
	if err := deadProbe.Check(context.Background()); err == nil {
		t.Errorf("probe.Check on closed endpoint %q = nil, want error (peer down → readiness degrades)", deadAddr)
	}
}

// remoteReadinessProbe resolves a remote topology for endpoint and returns the
// single readiness probe its ManagedResource exposes.
func remoteReadinessProbe(t *testing.T, endpoint string) interface {
	Check(context.Context) error
} {
	t.Helper()
	topo := remoteTopo(t, endpoint)
	inProc := transport.NewInProcess(nil)
	_, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(), transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res) != 1 || len(res[0].Probes()) != 1 {
		t.Fatalf("expected 1 readiness probe, got resources=%d", len(res))
	}
	return res[0].Probes()[0]
}

// --- #2263: client mTLS gate ---

// TestResolve_NonLoopbackPlaintext_FailsClosed asserts a non-loopback peer
// reached over a plaintext (bare host:port) endpoint is rejected: mTLS is
// mandatory across a network boundary (no private-network plaintext fallback).
func TestResolve_NonLoopbackPlaintext_FailsClosed(t *testing.T) {
	t.Parallel()
	topo := remoteTopo(t, "configcore.svc:9090") // non-loopback, plaintext
	inProc := transport.NewInProcess(nil)
	_, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err == nil {
		t.Fatal("expected fail-closed for non-loopback plaintext peer, got nil")
	}
	assertKindInternal(t, err)
}

// TestResolve_HTTPSWithoutClientIdentity_FailsClosed asserts an https peer with
// no provisioned client mTLS identity is rejected (never silently dialed without
// a client cert).
func TestResolve_HTTPSWithoutClientIdentity_FailsClosed(t *testing.T) {
	t.Parallel()
	topo := remoteTopo(t, "https://configcore.svc:8443")
	inProc := transport.NewInProcess(nil)
	_, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err == nil {
		t.Fatal("expected fail-closed for https peer without client identity, got nil")
	}
	assertKindInternal(t, err)
}

// TestResolve_HTTPSWithClientIdentity_BuildsMTLSTransport asserts that an https
// peer + a provisioned client identity builds a RemoteHTTPTransport plus a
// readiness probe (the TLS dial path is exercised end-to-end in the integration
// test).
func TestResolve_HTTPSWithClientIdentity_BuildsMTLSTransport(t *testing.T) {
	t.Parallel()
	topo := remoteTopo(t, "https://configcore.svc:8443")
	inProc := transport.NewInProcess(nil)
	ct, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, genClientIdentity(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := ct.(*transport.RemoteHTTPTransport); !ok {
		t.Errorf("Resolve returned %T, want *transport.RemoteHTTPTransport", ct)
	}
	if len(res) != 1 {
		t.Fatalf("https remote Resolve returned %d resources, want 1", len(res))
	}
}

// TestResolve_MTLSReadiness_CompletesHandshake asserts the mTLS readiness probe
// goes beyond a TCP dial: against a raw TCP listener that never speaks TLS the
// probe FAILS (handshake cannot complete) — proving cert/trust misconfiguration
// degrades readiness rather than surfacing only on the first real request, and
// distinguishing it from the plaintext TCP-only probe (TestResolve_RemoteProbe_TCPDial).
func TestResolve_MTLSReadiness_CompletesHandshake(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	topo := remoteTopo(t, "https://"+ln.Addr().String())
	inProc := transport.NewInProcess(nil)
	_, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, genClientIdentity(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	probe := res[0].Probes()[0]
	// Short deadline so the handshake against a silent raw-TCP peer fails fast.
	ctx, cancel := context.WithTimeout(context.Background(), testReadinessProbeDeadline)
	defer cancel()
	if err := probe.Check(ctx); err == nil {
		t.Error("mTLS readiness probe against a non-TLS peer = nil, want error (handshake must complete)")
	}
}

// genClientIdentity builds a CA + this-cell leaf (SPIFFE URI SAN) and returns the
// resulting client mTLS identity for trust domain "example.org".
func genClientIdentity(t *testing.T) tlsutil.ClientIdentity {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(testCAValidity),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	must(err)
	caCert, err := x509.ParseCertificate(caDER)
	must(err)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	uri, err := url.Parse("spiffe://example.org/cell/accesscore")
	must(err)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "accesscore"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:        []*url.URL{uri},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	must(err)
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	must(err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	pool, err := tlsutil.NewClientCAPool(caPEM)
	must(err)
	id, err := tlsutil.NewClientIdentity(certPEM, keyPEM, pool, "example.org")
	must(err)
	return id
}

// --- F9.3: scheme-branch tests for remoteClientTLSConfig ---

// TestResolve_HTTPSLoopback_RequiresIdentity asserts that an https:// endpoint
// always requires a provisioned client mTLS identity, even for a loopback
// address. The loopback plaintext exemption only applies to bare host:port or
// http:// endpoints — not https://.
func TestResolve_HTTPSLoopback_RequiresIdentity(t *testing.T) {
	t.Parallel()
	topo := remoteTopo(t, "https://localhost:8443")
	inProc := transport.NewInProcess(nil)
	_, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err == nil {
		t.Fatal("expected error: https endpoint requires client identity even for loopback, got nil")
	}
	assertKindInternal(t, err)
}

// TestResolve_HTTPSLoopback_WithIdentity_OK asserts that an https:// loopback
// endpoint with a valid client mTLS identity resolves successfully to a
// *transport.RemoteHTTPTransport (the TLS dial is exercised end-to-end by
// remote_mtls_integration_test.go's happy-path test).
func TestResolve_HTTPSLoopback_WithIdentity_OK(t *testing.T) {
	t.Parallel()
	topo := remoteTopo(t, "https://localhost:8443")
	inProc := transport.NewInProcess(nil)
	ct, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, genClientIdentity(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := ct.(*transport.RemoteHTTPTransport); !ok {
		t.Errorf("Resolve returned %T, want *transport.RemoteHTTPTransport", ct)
	}
}

// TestResolve_HTTPNonLoopback_FailsClosed asserts that an explicit http://
// non-loopback endpoint is rejected with KindInternal — plaintext across a real
// network boundary is forbidden regardless of whether the scheme is explicit or
// implicit (gocell validate TOPO gate also rejects this at build time; this is
// the runtime defense-in-depth).
func TestResolve_HTTPNonLoopback_FailsClosed(t *testing.T) {
	t.Parallel()
	topo := remoteTopo(t, "http://configcore.svc:9090") // explicit http://, non-loopback
	inProc := transport.NewInProcess(nil)
	_, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, tlsutil.ClientIdentity{})
	if err == nil {
		t.Fatal("expected fail-closed for explicit http:// non-loopback peer, got nil")
	}
	assertKindInternal(t, err)
}

// TestResolve_MaterialButPlaintextEndpoint_FailsClosed: TLS material provisioned
// (non-zero ClientIdentity) but a plaintext loopback endpoint → fail-closed
// (#2263 F2: material present ⇒ endpoint must be https, so server/client TLS
// modes agree).
func TestResolve_MaterialButPlaintextEndpoint_FailsClosed(t *testing.T) {
	t.Parallel()
	topo := remoteTopo(t, "127.0.0.1:9090") // bare loopback, plaintext
	inProc := transport.NewInProcess(nil)
	_, _, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, genClientIdentity(t)) // material present
	if err == nil {
		t.Fatal("expected fail-closed when TLS material is provisioned but endpoint is plaintext")
	}
	assertKindInternal(t, err)
}

// TestResolve_Readiness_SendsSNI: the mTLS readiness probe sends the dial host as
// SNI (ServerName), matching http.Transport (#2263 F3) — so an SNI-routed peer
// is probed the same way real requests reach it. A raw TLS listener captures the
// ClientHello ServerName; a hostname endpoint is used because Go omits SNI for IP
// literals.
func TestResolve_Readiness_SendsSNI(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	sniCh := make(chan string, 1)
	go func() {
		conn, accErr := ln.Accept()
		if accErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// GetConfigForClient runs on receipt of the ClientHello — capture SNI then
		// abort (no real cert needed; the handshake fails afterwards, which is fine).
		_ = tls.Server(conn, &tls.Config{
			MinVersion: tls.VersionTLS13,
			GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
				select {
				case sniCh <- h.ServerName:
				default:
				}
				return nil, errors.New("capture-only server")
			},
		}).HandshakeContext(context.Background())
	}()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	topo := remoteTopo(t, "https://localhost:"+port)
	inProc := transport.NewInProcess(nil)
	_, res, err := celltransport.Resolve(topo, "configcore", inProc, clock.Real(),
		transport.CrossCellObs{}, genClientIdentity(t))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testReadinessProbeDeadline)
	defer cancel()
	_ = res[0].Probes()[0].Check(ctx) // handshake will fail (capture server), but SNI is sent first

	select {
	case sni := <-sniCh:
		if sni != "localhost" {
			t.Errorf("readiness ClientHello ServerName = %q, want %q", sni, "localhost")
		}
	case <-time.After(testSNICaptureWait):
		t.Fatal("readiness probe did not send a ClientHello (no SNI captured)")
	}
}

// --- local test tracer (celltransport_test cannot reach transport's internal one) ---

type recTracer struct {
	attrs []wrapper.Attr
}

func (rt *recTracer) Start(ctx context.Context, _ string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	rt.attrs = append(rt.attrs, attrs...)
	return ctx, &recSpan{rt: rt}
}

func (rt *recTracer) hasAttr(key string, val any) bool {
	for _, a := range rt.attrs {
		if a.Key == key && a.Value == val {
			return true
		}
	}
	return false
}

type recSpan struct{ rt *recTracer }

func (s *recSpan) SetAttributes(attrs ...wrapper.Attr)  { s.rt.attrs = append(s.rt.attrs, attrs...) }
func (s *recSpan) RecordError(error)                    {}
func (s *recSpan) SetStatus(wrapper.StatusCode, string) {}
func (s *recSpan) End()                                 {}
