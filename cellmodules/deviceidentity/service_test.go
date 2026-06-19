package deviceidentity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/keystest"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
	cacerts "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/cacerts/v1"
	enroll "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/enroll/v1"
	renew "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/renew/v1"
)

// ─── test constants ──────────────────────────────────────────────────────────

const (
	testTenantStr = "11111111-1111-1111-1111-111111111111"
	testDeviceID  = "device-abc"
	testIssuerID  = "ca-root"
	testEnrollAud = "gocell-est"
	testGrantTTL  = 2 * time.Hour
	// testParseUnderTTL is a requested duration comfortably under testGrantTTL,
	// used by TestParseTTL's "valid under maxTTL" case.
	testParseUnderTTL = 30 * time.Minute
)

// ─── test helpers ────────────────────────────────────────────────────────────

// testClk returns a fixed-time clockmock for deterministic tests.
func testClk(t *testing.T) clock.Clock {
	t.Helper()
	return clockmock.New(time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC))
}

// testIssuerIDVal returns a valid certsigning.IssuerID.
func testIssuerIDVal(t *testing.T) certsigning.IssuerID {
	t.Helper()
	id, err := certsigning.NewIssuerID(testIssuerID)
	if err != nil {
		t.Fatalf("NewIssuerID: %v", err)
	}
	return id
}

// testTenantID returns a valid tenant.TenantID.
func testTenantID(t *testing.T) tenant.TenantID {
	t.Helper()
	tid, err := tenant.ParseTenantID(testTenantStr)
	if err != nil {
		t.Fatalf("ParseTenantID: %v", err)
	}
	return tid
}

// testECKey generates a P256 key for test CSR/cert generation.
func testECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	return key
}

// testCSRB64 returns a base64-encoded PKCS#10 CSR DER.
func testCSRB64(t *testing.T) string {
	t.Helper()
	key := testECKey(t)
	der, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: testDeviceID}}, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

// testCertDER fabricates a self-signed certificate DER.
func testCertDER(t *testing.T) []byte {
	t.Helper()
	key := testECKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: testDeviceID},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return der
}

// testCertScope returns a valid CertScope.
func testCertScope(t *testing.T) certsigning.CertScope {
	t.Helper()
	dev, err := certsigning.NewDeviceID(testDeviceID)
	if err != nil {
		t.Fatalf("NewDeviceID: %v", err)
	}
	scope, err := certsigning.NewCertScope(testTenantID(t), testIssuerIDVal(t), dev)
	if err != nil {
		t.Fatalf("NewCertScope: %v", err)
	}
	return scope
}

// testIssuedCert builds a valid IssuedCert from a fabricated cert DER.
func testIssuedCert(t *testing.T) certsigning.IssuedCert {
	t.Helper()
	scope := testCertScope(t)
	issued, err := certsigning.NewIssuedCert(scope, testCertDER(t), nil, 0)
	if err != nil {
		t.Fatalf("NewIssuedCert: %v", err)
	}
	return issued
}

// newTestEnrollmentScheme creates an issuer+verifier pair for testing.
func newTestEnrollmentScheme(t *testing.T, clk clock.Clock) (*auth.EnrollmentCredentialIssuer, *auth.EnrollmentCredentialVerifier) {
	t.Helper()
	ks, _, _ := keystest.MustNewKeySet(clk)
	iss, err := auth.NewEnrollmentCredentialIssuer(ks, "gocell", clk,
		auth.WithIssuerAudiencesFromSlice([]string{testEnrollAud}))
	if err != nil {
		t.Fatalf("NewEnrollmentCredentialIssuer: %v", err)
	}
	jwtVer, err := auth.NewJWTVerifier(ks, clk, auth.WithExpectedAudiences(testEnrollAud))
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}
	ver, err := auth.NewEnrollmentCredentialVerifier(jwtVer)
	if err != nil {
		t.Fatalf("NewEnrollmentCredentialVerifier: %v", err)
	}
	return iss, ver
}

// ─── fakes ───────────────────────────────────────────────────────────────────

// fakeSigner is a test double for certsigning.Signer. It returns a
// pre-built IssuedCert on success, or the configured error on Sign calls.
type fakeSigner struct {
	issued      certsigning.IssuedCert
	err         error
	trustBundle [][]byte
}

func (f *fakeSigner) Sign(_ context.Context, _ certsigning.AuthorizedCertRequest) (certsigning.IssuedCert, error) {
	return f.issued, f.err
}

func (f *fakeSigner) TrustBundle(_ context.Context) ([][]byte, error) {
	return f.trustBundle, f.err
}

// fakeAuthorizer is a test double for certsigning.Authorizer.
// When granted == true, it returns a single-SAN grant for the device SPIFFE URI.
type fakeAuthorizer struct {
	granted   bool
	grantSANs certsigning.SubjectAltNames // used when granted == true
	err       error
}

func (f *fakeAuthorizer) AuthorizeEnroll(ctx context.Context, claim certsigning.EnrollmentClaim) (certsigning.SignConstraints, error) {
	if f.err != nil {
		return certsigning.SignConstraints{}, f.err
	}
	if !f.granted {
		return certsigning.SignConstraints{}, nil
	}
	return certsigning.NewSignConstraints(testGrantTTL, f.grantSANs)
}

// newGrantedAuthorizer returns a fakeAuthorizer that grants only the device-self
// SPIFFE URI SAN for the test scope.
func newGrantedAuthorizer(t *testing.T) *fakeAuthorizer {
	t.Helper()
	scope := testCertScope(t)
	deviceURI := certsigning.DeviceURISAN(scope)
	allowedSANs, err := certsigning.NewSubjectAltNames(nil, nil, []*url.URL{deviceURI})
	if err != nil {
		t.Fatalf("NewSubjectAltNames for grant: %v", err)
	}
	return &fakeAuthorizer{granted: true, grantSANs: allowedSANs}
}

// newService builds a Service with fakes for unit testing.
func newService(
	t *testing.T, clk clock.Clock, signer certsigning.Signer, authorizer certsigning.Authorizer, verifier *auth.EnrollmentCredentialVerifier,
) *Service {
	t.Helper()
	issID := testIssuerIDVal(t)
	svc, err := NewService(clk, signer, authorizer, verifier, issID)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// enrollCtx builds a context with a verifiedEnrollment for the test device.
func enrollCtx(t *testing.T) context.Context {
	t.Helper()
	return withVerifiedEnrollment(context.Background(), verifiedEnrollment{
		tenant:  testTenantID(t),
		subject: testDeviceID,
	})
}

// ─── Cacerts ───────────────────────────────────────────────────────────────────

func TestCacerts(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)

	t.Run("happy returns base64 PKCS#7 trust bundle", func(t *testing.T) {
		t.Parallel()
		signer := &fakeSigner{trustBundle: [][]byte{testCertDER(t)}}
		svc := newService(t, clk, signer, newGrantedAuthorizer(t), ver)

		resp, err := svc.Cacerts(context.Background(), &cacerts.Request{})
		if err != nil {
			t.Fatalf("Cacerts: unexpected error: %v", err)
		}
		ok, isOK := resp.(cacerts.Cacerts200JSONResponse)
		if !isOK {
			t.Fatalf("expected Cacerts200JSONResponse, got %T", resp)
		}
		if ok.Data == nil || ok.Data.TrustBundle == "" {
			t.Fatal("expected non-empty trustBundle")
		}
		der, decErr := base64.StdEncoding.DecodeString(ok.Data.TrustBundle)
		if decErr != nil {
			t.Fatalf("trustBundle is not valid base64: %v", decErr)
		}
		if len(der) == 0 {
			t.Fatal("decoded trustBundle is empty")
		}
	})

	t.Run("empty bundle returns 503", func(t *testing.T) {
		t.Parallel()
		svc := newService(t, clk, &fakeSigner{}, newGrantedAuthorizer(t), ver)
		resp, err := svc.Cacerts(context.Background(), &cacerts.Request{})
		if err != nil {
			t.Fatalf("Cacerts: unexpected error: %v", err)
		}
		if _, isErr := resp.(cacerts.Cacerts503ErrorResponse); !isErr {
			t.Fatalf("expected Cacerts503ErrorResponse, got %T", resp)
		}
	})

	t.Run("signer error returns 503", func(t *testing.T) {
		t.Parallel()
		svc := newService(t, clk, &fakeSigner{err: errors.New("CA down")}, newGrantedAuthorizer(t), ver)
		resp, err := svc.Cacerts(context.Background(), &cacerts.Request{})
		if err != nil {
			t.Fatalf("Cacerts: unexpected error: %v", err)
		}
		if _, isErr := resp.(cacerts.Cacerts503ErrorResponse); !isErr {
			t.Fatalf("expected Cacerts503ErrorResponse, got %T", resp)
		}
	})
}

// ─── NewService ──────────────────────────────────────────────────────────────

func TestNewService_FailFast(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	issued := testIssuedCert(t)
	signer := &fakeSigner{issued: issued}
	authorizer := newGrantedAuthorizer(t)
	issID := testIssuerIDVal(t)

	t.Run("nil_signer", func(t *testing.T) {
		_, err := NewService(clk, nil, authorizer, ver, issID)
		if err == nil {
			t.Fatal("expected error for nil signer")
		}
	})
	t.Run("nil_authorizer", func(t *testing.T) {
		_, err := NewService(clk, signer, nil, ver, issID)
		if err == nil {
			t.Fatal("expected error for nil authorizer")
		}
	})
	t.Run("nil_verifier", func(t *testing.T) {
		_, err := NewService(clk, signer, authorizer, nil, issID)
		if err == nil {
			t.Fatal("expected error for nil verifier")
		}
	})
	t.Run("zero_issuerID", func(t *testing.T) {
		_, err := NewService(clk, signer, authorizer, ver, certsigning.IssuerID{})
		if err == nil {
			t.Fatal("expected error for zero issuerID")
		}
	})
}

// ─── Enroll ──────────────────────────────────────────────────────────────────

func TestEnroll_Happy(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	issued := testIssuedCert(t)
	svc := newService(t, clk, &fakeSigner{issued: issued}, newGrantedAuthorizer(t), ver)

	ctx := enrollCtx(t)
	resp, err := svc.Enroll(ctx, &enroll.Request{
		Csr: testCSRB64(t),
	})
	if err != nil {
		t.Fatalf("Enroll: unexpected error: %v", err)
	}
	r, ok := resp.(enroll.Enroll201JSONResponse)
	if !ok {
		t.Fatalf("expected 201 response, got %T", resp)
	}
	if r.Data == nil {
		t.Fatal("response data is nil")
	}
	if r.Data.DeviceID != testDeviceID {
		t.Errorf("deviceId = %q, want %q", r.Data.DeviceID, testDeviceID)
	}
	if r.Data.Status != enroll.ResponseDataStatusIssued {
		t.Errorf("status = %q, want issued", r.Data.Status)
	}
}

func TestEnroll_MissingVerifiedEnrollment_Returns401(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	// No verifiedEnrollment in context — Enroll must return 401.
	resp, err := svc.Enroll(context.Background(), &enroll.Request{Csr: testCSRB64(t)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(enroll.Enroll401ErrorResponse); !ok {
		t.Errorf("expected 401, got %T", resp)
	}
}

func TestEnroll_DeviceIDMismatch_Returns422(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	ctx := enrollCtx(t)
	resp, err := svc.Enroll(ctx, &enroll.Request{
		Csr:      testCSRB64(t),
		DeviceID: "wrong-device-id",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(enroll.Enroll422ErrorResponse); !ok {
		t.Errorf("expected 422, got %T", resp)
	}
}

func TestEnroll_BadBase64CSR_Returns400(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	ctx := enrollCtx(t)
	resp, err := svc.Enroll(ctx, &enroll.Request{Csr: "!!not-valid-base64!!"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(enroll.Enroll400ErrorResponse); !ok {
		t.Errorf("expected 400, got %T", resp)
	}
}

func TestEnroll_NotGranted_Returns403(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	// Authorizer that returns non-granted SignConstraints (no error).
	authorizer := &fakeAuthorizer{granted: false}
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, authorizer, ver)

	ctx := enrollCtx(t)
	resp, err := svc.Enroll(ctx, &enroll.Request{Csr: testCSRB64(t)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(enroll.Enroll403ErrorResponse); !ok {
		t.Errorf("expected 403, got %T", resp)
	}
}

func TestEnroll_ExtraDNSSAN_Returns422(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	ctx := enrollCtx(t)
	// Grant allows only device-self SPIFFE URI SAN, but request adds a DNS SAN.
	resp, err := svc.Enroll(ctx, &enroll.Request{
		Csr: testCSRB64(t),
		SubjectAltNames: &enroll.RequestSubjectAltNames{
			DNSNames: []string{"extra.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// NewAuthorizedCertRequest subset check rejects extra DNS SAN → 422.
	if _, ok := resp.(enroll.Enroll422ErrorResponse); !ok {
		t.Errorf("expected 422, got %T", resp)
	}
}

func TestEnroll_EmailAddresses_Returns422(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	ctx := enrollCtx(t)
	resp, err := svc.Enroll(ctx, &enroll.Request{
		Csr: testCSRB64(t),
		SubjectAltNames: &enroll.RequestSubjectAltNames{
			EmailAddresses: []string{"user@example.com"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(enroll.Enroll422ErrorResponse); !ok {
		t.Errorf("expected 422 for email SANs, got %T", resp)
	}
}

func TestEnroll_SignerUnavailable_Returns503(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	unavailErr := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "signer down")
	svc := newService(t, clk, &fakeSigner{err: unavailErr}, newGrantedAuthorizer(t), ver)

	ctx := enrollCtx(t)
	resp, err := svc.Enroll(ctx, &enroll.Request{Csr: testCSRB64(t)})
	if err != nil {
		t.Fatalf("unexpected Go error (should be 503 response): %v", err)
	}
	if _, ok := resp.(enroll.Enroll503ErrorResponse); !ok {
		t.Errorf("expected 503, got %T", resp)
	}
}

func TestEnroll_AuthorizerUnavailable_Returns503(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	unavailErr := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "authorizer down")
	authorizer := &fakeAuthorizer{err: unavailErr}
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, authorizer, ver)

	ctx := enrollCtx(t)
	resp, err := svc.Enroll(ctx, &enroll.Request{Csr: testCSRB64(t)})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if _, ok := resp.(enroll.Enroll503ErrorResponse); !ok {
		t.Errorf("expected 503, got %T", resp)
	}
}

func TestEnroll_UnknownUsage_Returns422(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	ctx := enrollCtx(t)
	resp, err := svc.Enroll(ctx, &enroll.Request{
		Csr:    testCSRB64(t),
		Usages: []string{"unknown-usage"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(enroll.Enroll422ErrorResponse); !ok {
		t.Errorf("expected 422 for unknown usage, got %T", resp)
	}
}

// ─── Enroll middleware ────────────────────────────────────────────────────────

func TestEnrollAuthMiddleware_ValidCredential_SetsContext(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	iss, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	// Issue a real enrollment credential.
	token, err := iss.Issue(testTenantStr, testDeviceID)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	var capturedCtx context.Context
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCtx = r.Context()
		w.WriteHeader(http.StatusOK)
	})

	middleware := svc.enrollAuthMiddleware()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	middleware(handler).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("middleware blocked valid token: %d", w.Code)
	}
	ve, ok := verifiedEnrollmentFrom(capturedCtx)
	if !ok {
		t.Fatal("verifiedEnrollment not set in context")
	}
	if ve.subject != testDeviceID {
		t.Errorf("subject = %q, want %q", ve.subject, testDeviceID)
	}
	if ve.tenant.String() != testTenantStr {
		t.Errorf("tenant = %q, want %q", ve.tenant.String(), testTenantStr)
	}
}

func TestEnrollAuthMiddleware_MissingToken_Returns401(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
	})
	middleware := svc.enrollAuthMiddleware()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	middleware(handler).ServeHTTP(w, r)

	if handlerCalled {
		t.Fatal("handler must not be called when token is missing")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestEnrollAuthMiddleware_InvalidToken_Returns401(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	middleware := svc.enrollAuthMiddleware()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer bogus-token")
	w := httptest.NewRecorder()
	middleware(handler).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ─── Renew ───────────────────────────────────────────────────────────────────

// testDeviceURISAN returns the SPIFFE URI SAN for the test scope.
func testDeviceURISAN(t *testing.T) *url.URL {
	t.Helper()
	scope := testCertScope(t)
	return certsigning.DeviceURISAN(scope)
}

// peerCtxWithDeviceURI builds a context with a ctxkeys.PeerIdentity carrying
// the test device's SPIFFE URI SAN.
func peerCtxWithDeviceURI(t *testing.T) context.Context {
	t.Helper()
	peer := ctxkeys.PeerIdentity{
		URIs: []*url.URL{testDeviceURISAN(t)},
	}
	return ctxkeys.WithPeerIdentity(context.Background(), peer)
}

func TestRenew_Happy(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	issued := testIssuedCert(t)
	svc := newService(t, clk, &fakeSigner{issued: issued}, newGrantedAuthorizer(t), ver)

	ctx := peerCtxWithDeviceURI(t)
	resp, err := svc.Renew(ctx, &renew.Request{
		Csr:         testCSRB64(t),
		PriorSerial: "deadbeef",
	})
	if err != nil {
		t.Fatalf("Renew: unexpected error: %v", err)
	}
	r, ok := resp.(renew.Renew200JSONResponse)
	if !ok {
		t.Fatalf("expected 200 response, got %T", resp)
	}
	if r.Data == nil {
		t.Fatal("response data is nil")
	}
	if r.Data.DeviceID != testDeviceID {
		t.Errorf("deviceId = %q, want %q", r.Data.DeviceID, testDeviceID)
	}
	if r.Data.PriorSerial != "deadbeef" {
		t.Errorf("priorSerial = %q, want %q", r.Data.PriorSerial, "deadbeef")
	}
	if r.Data.Status != renew.ResponseDataStatusIssued {
		t.Errorf("status = %q, want issued", r.Data.Status)
	}
}

func TestRenew_NoPeerIdentity_Returns401(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	resp, err := svc.Renew(context.Background(), &renew.Request{Csr: testCSRB64(t)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(renew.Renew401ErrorResponse); !ok {
		t.Errorf("expected 401, got %T", resp)
	}
}

func TestRenew_NoDeviceURISAN_Returns401(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	// Peer identity with no URIs (no device-identity URI SAN).
	peer := ctxkeys.PeerIdentity{URIs: nil}
	ctx := ctxkeys.WithPeerIdentity(context.Background(), peer)
	resp, err := svc.Renew(ctx, &renew.Request{Csr: testCSRB64(t)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(renew.Renew401ErrorResponse); !ok {
		t.Errorf("expected 401, got %T", resp)
	}
}

func TestRenew_DeviceIDMismatch_Returns422(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	ctx := peerCtxWithDeviceURI(t)
	resp, err := svc.Renew(ctx, &renew.Request{
		Csr:      testCSRB64(t),
		DeviceID: "different-device",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(renew.Renew422ErrorResponse); !ok {
		t.Errorf("expected 422, got %T", resp)
	}
}

func TestRenew_SignerUnavailable_Returns503(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	unavailErr := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "signer down")
	svc := newService(t, clk, &fakeSigner{err: unavailErr}, newGrantedAuthorizer(t), ver)

	ctx := peerCtxWithDeviceURI(t)
	resp, err := svc.Renew(ctx, &renew.Request{Csr: testCSRB64(t)})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if _, ok := resp.(renew.Renew503ErrorResponse); !ok {
		t.Errorf("expected 503, got %T", resp)
	}
}

func TestRenew_ExtraDNSSAN_Returns422(t *testing.T) {
	t.Parallel()
	clk := testClk(t)
	_, ver := newTestEnrollmentScheme(t, clk)
	svc := newService(t, clk, &fakeSigner{issued: testIssuedCert(t)}, newGrantedAuthorizer(t), ver)

	ctx := peerCtxWithDeviceURI(t)
	resp, err := svc.Renew(ctx, &renew.Request{
		Csr: testCSRB64(t),
		SubjectAltNames: &renew.RequestSubjectAltNames{
			DNSNames: []string{"extra.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := resp.(renew.Renew422ErrorResponse); !ok {
		t.Errorf("expected 422 for extra DNS SAN, got %T", resp)
	}
}

// ─── extractBearerToken ───────────────────────────────────────────────────────

func TestExtractBearerToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"bearer", "Bearer sometoken", "sometoken"},
		{"lowercase bearer", "bearer sometoken", "sometoken"},
		{"mixed case", "BEARER sometoken", "sometoken"},
		{"no scheme", "sometoken", ""},
		{"basic scheme", "Basic dXNlcjpwYXNz", ""},
		{"bearer with spaces", "Bearer  token-with-leading-space", "token-with-leading-space"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			got := extractBearerToken(r)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── errKindIs / errCodeIs ───────────────────────────────────────────────────

func TestErrKindIs(t *testing.T) {
	t.Parallel()
	err := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "down")
	if !errKindIs(err, errcode.KindUnavailable) {
		t.Error("errKindIs: should return true for matching kind")
	}
	if errKindIs(err, errcode.KindInvalid) {
		t.Error("errKindIs: should return false for non-matching kind")
	}
	if errKindIs(errors.New("plain"), errcode.KindUnavailable) {
		t.Error("errKindIs: should return false for non-errcode error")
	}
}

func TestErrCodeIs(t *testing.T) {
	t.Parallel()
	err := errcode.New(errcode.KindPermissionDenied, errcode.ErrCertConstraintViolation, "exceeded")
	if !errCodeIs(err, errcode.ErrCertConstraintViolation) {
		t.Error("errCodeIs: should return true for matching code")
	}
	if errCodeIs(err, errcode.ErrCertAuthorizeDenied) {
		t.Error("errCodeIs: should return false for non-matching code")
	}
}

// ─── parseTTL ─────────────────────────────────────────────────────────────────

func TestParseTTL(t *testing.T) {
	t.Parallel()
	fac := enrollErrFactory{}
	maxTTL := testGrantTTL

	tests := []struct {
		name    string
		input   string
		wantTTL time.Duration
		wantErr bool
	}{
		{"empty uses maxTTL", "", maxTTL, false},
		{"valid under maxTTL", "30m", testParseUnderTTL, false},
		{"exceeds maxTTL clamped", "24h", maxTTL, false},
		{"zero clamped to maxTTL", "0s", maxTTL, false},
		{"negative clamped to maxTTL", "-1h", maxTTL, false},
		{"invalid format", "not-a-duration", 0, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseTTL(fac, tc.input, maxTTL)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantTTL {
				t.Errorf("TTL = %v, want %v", got, tc.wantTTL)
			}
		})
	}
}

// ─── buildUsages ─────────────────────────────────────────────────────────────

func TestBuildUsages(t *testing.T) {
	t.Parallel()
	fac := enrollErrFactory{}
	tests := []struct {
		name    string
		usages  []string
		wantErr bool
	}{
		{"empty defaults", []string{}, false},
		{"signing", []string{"signing"}, false},
		{"digital signature", []string{"digital signature"}, false},
		{"key encipherment", []string{"key encipherment"}, false},
		{"client auth", []string{"client auth"}, false},
		{"server auth", []string{"server auth"}, false},
		{"multiple valid", []string{"signing", "client auth"}, false},
		{"unknown", []string{"bad usage"}, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := buildUsages(fac, tc.usages)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
