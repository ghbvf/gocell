package certlifecycle_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/reconcile/reconciletest"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	cl "github.com/ghbvf/gocell/framework/runtime/certlifecycle"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

const (
	testTenant  = "11111111-1111-1111-1111-111111111111"
	testTenant2 = "22222222-2222-2222-2222-222222222222"
	testIssuer  = "ca-test"
)

// errFake is a reusable error for fake failure injection.
var errFake = errors.New("fake failure")

// deviceCSR generates a POP-valid PKCS#10 CSR DER for cn (self-signed by a fresh
// ecdsa key, so certsigning.NewCertRequest's CheckSignature passes).
func deviceCSR(t *testing.T, cn string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen csr key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	return der
}

// fakeSigner is a certsigning.Signer minting an IssuedCert from a per-call leaf
// DER (signed by a key generated once at construction). It records every
// AuthorizedCertRequest and can be configured to fail. Runtime crypto errors are
// returned (never panicked) so a Sign failure surfaces as a clean test failure.
type fakeSigner struct {
	mu        sync.Mutex
	key       *ecdsa.PrivateKey
	err       error
	notBefore time.Time
	notAfter  time.Time
	serial    int64
	requests  []cs.AuthorizedCertRequest
	calls     int
}

func newFakeSigner(t *testing.T, notBefore, notAfter time.Time) *fakeSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen signer key: %v", err)
	}
	return &fakeSigner{key: key, notBefore: notBefore, notAfter: notAfter, serial: 1000}
}

func (s *fakeSigner) Sign(_ context.Context, req cs.AuthorizedCertRequest) (cs.IssuedCert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.requests = append(s.requests, req)
	if s.err != nil {
		return cs.IssuedCert{}, s.err
	}
	s.serial++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(s.serial),
		Subject:      pkix.Name{CommonName: req.Request().Subject().CommonName()},
		NotBefore:    s.notBefore,
		NotAfter:     s.notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &s.key.PublicKey, s.key)
	if err != nil {
		return cs.IssuedCert{}, fmt.Errorf("fake sign: create cert: %w", err)
	}
	issued, err := cs.NewIssuedCert(req.Request().Scope(), der, nil, 0)
	if err != nil {
		return cs.IssuedCert{}, fmt.Errorf("fake sign: new issued cert: %w", err)
	}
	return issued, nil
}

func (s *fakeSigner) TrustBundle(context.Context) ([][]byte, error) { return nil, nil }

func (s *fakeSigner) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeSigner) lastRequest() (cs.AuthorizedCertRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return cs.AuthorizedCertRequest{}, false
	}
	return s.requests[len(s.requests)-1], true
}

// fakeAuthorizer is a certsigning.Authorizer with a configurable outcome.
type fakeAuthorizer struct {
	mu    sync.Mutex
	grant cs.SignConstraints
	err   error
	calls int
}

func (a *fakeAuthorizer) AuthorizeEnroll(context.Context, cs.EnrollmentClaim) (cs.SignConstraints, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return a.grant, a.err
}

func (a *fakeAuthorizer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// grantAll returns a granted SignConstraints with maxTTL and no SAN allowance
// (empty allowed SANs accommodates an empty requested SAN set).
func grantAll(t *testing.T, maxTTL time.Duration) cs.SignConstraints {
	t.Helper()
	emptySANs, err := cs.NewSubjectAltNames(nil, nil, nil)
	if err != nil {
		t.Fatalf("empty SANs: %v", err)
	}
	grant, err := cs.NewSignConstraints(maxTTL, emptySANs)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	return grant
}

// fakeRepo embeds the kernel fenced-repository fake (monotonic CAS + Effects) and
// adds ListRenewalCandidates. It is BOTH the cl.DeviceCertRepository the
// Reconciler holds AND the reconcile.FencedRepository the Loop writes through.
type fakeRepo struct {
	*reconciletest.FakeFencedRepository
	mu         sync.Mutex
	candidates []cl.Candidate
	listErr    error
	listCalls  int
	lastLimit  int
}

func newFakeRepo(candidates ...cl.Candidate) *fakeRepo {
	return &fakeRepo{
		FakeFencedRepository: reconciletest.NewFakeFencedRepository(),
		candidates:           candidates,
	}
}

func (r *fakeRepo) ListRenewalCandidates(_ context.Context, _ time.Time, limit int) ([]cl.Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	r.lastLimit = limit
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := append([]cl.Candidate(nil), r.candidates...)
	if limit > 0 && len(out) > limit {
		out = out[:limit] // honor the scan cap like a real SQL LIMIT
	}
	return out, nil
}

func (r *fakeRepo) listCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listCalls
}

func (r *fakeRepo) lastListLimit() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastLimit
}

var _ cl.DeviceCertRepository = (*fakeRepo)(nil)

// mutations returns the IssuedMutation values recorded by accepted fenced writes.
func (r *fakeRepo) mutations() []*cl.IssuedMutation {
	var out []*cl.IssuedMutation
	for _, e := range r.Effects() {
		if m, ok := e.Mutation.(*cl.IssuedMutation); ok {
			out = append(out, m)
		}
	}
	return out
}

// firstMutation returns the single recorded mutation or fails.
func firstMutation(t *testing.T, r *fakeRepo) *cl.IssuedMutation {
	t.Helper()
	muts := r.mutations()
	if len(muts) != 1 {
		t.Fatalf("recorded %d mutations, want exactly 1", len(muts))
	}
	return muts[0]
}

// activeCandidate builds a renewable (active) candidate with the given validity
// window. Caller controls due-ness via the window relative to the test clock.
func activeCandidate(t *testing.T, deviceID string, notBefore, notAfter time.Time) cl.Candidate {
	t.Helper()
	return activeCandidateForTenant(t, testTenant, deviceID, notBefore, notAfter)
}

// activeCandidateForTenant is activeCandidate scoped to an explicit tenant — used
// to prove two tenants sharing a deviceID get distinct fenced entity keys.
func activeCandidateForTenant(t *testing.T, tenantID, deviceID string, notBefore, notAfter time.Time) cl.Candidate {
	t.Helper()
	return cl.Candidate{
		DeviceID:   deviceID,
		TenantID:   tenant.TenantID(tenantID),
		IssuerID:   testIssuer,
		Serial:     "00aa",
		Epoch:      1,
		NotBefore:  notBefore,
		NotAfter:   notAfter,
		State:      cl.StateActive(),
		CommonName: deviceID,
		CSRDER:     deviceCSR(t, deviceID),
	}
}
