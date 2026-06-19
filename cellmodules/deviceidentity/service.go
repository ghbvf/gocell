// Package deviceidentity provides the EST (RFC 7030) device certificate
// enrollment and renewal service implementation. It bridges the framework-owned
// http.deviceidentity.enroll.v1 and http.deviceidentity.renew.v1 contracts
// (ownerCell: _framework, ADR 202606130635-1939) to the certsigning pipeline.
//
// Auth model:
//   - Enroll: enrollment-credential bearer token (checked by enrollAuthMiddleware
//     before the handler runs); the credential carries tenant + device subject.
//   - Renew: device mTLS on DeviceMTLSListener; the peer client certificate
//     carries a device-identity URI SAN (spiffe://<tenant>/device/<id>) from
//     which the renewal handler recovers the device identity — no device registry
//     required (certsigning.ParseDeviceURISAN).
//
// SAN policy: the grant returned by Authorizer.AuthorizeEnroll allows exactly
// the device-self SPIFFE URI SAN (certsigning.DeviceURISAN). Any additional
// requested URI SAN, DNS SAN, or IP SAN makes NewAuthorizedCertRequest's subset
// check fail → 422. Email SANs are unsupported and are explicitly rejected early
// (→ 422) before reaching the signing pipeline.
//
// Supported usage strings (cert-manager vocabulary):
//   - "signing" / "digital signature" → x509.KeyUsageDigitalSignature
//   - "key encipherment"              → x509.KeyUsageKeyEncipherment
//   - "client auth"                   → x509.ExtKeyUsageClientAuth
//   - "server auth"                   → x509.ExtKeyUsageServerAuth
//
// Unknown usage strings are rejected → 422. Empty usages defaults to
// digital-signature + client-auth (device certificate baseline).
//
// PriorSerial (renew): echoed back in the response. Without a persistent cert
// store this PR cannot verify the serial was issued to the device — the mTLS
// client cert already proves possession of the private key for the existing cert.
package deviceidentity

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
	cacerts "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/cacerts/v1"
	enroll "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/enroll/v1"
	renew "github.com/ghbvf/gocell/generated/contracts/http/deviceidentity/renew/v1"
)

// Contract IDs are defined here so the composition root can pass them to
// bootstrap.WithFrameworkHTTPServing. The generated packages keep their
// contractSpec unexported; bootstrap reconciles these ids against
// assembly.frameworkContracts at startup — any drift fails fast.
const (
	enrollContractID  = "http.deviceidentity.enroll.v1"
	renewContractID   = "http.deviceidentity.renew.v1"
	cacertsContractID = "http.deviceidentity.cacerts.v1"
)

// Compile-time interface assertions.
var (
	_ enroll.Service  = (*Service)(nil)
	_ renew.Service   = (*Service)(nil)
	_ cacerts.Service = (*Service)(nil)
)

// Service implements the EST enrollment (enroll.Service) and renewal
// (renew.Service) contracts backed by certsigning.Signer + Authorizer.
type Service struct {
	clk        clock.Clock
	signer     certsigning.Signer
	authorizer certsigning.Authorizer
	verifier   *auth.EnrollmentCredentialVerifier
	issuerID   certsigning.IssuerID
}

// NewService constructs the deviceidentity Service. All dependencies are
// positional and fail-closed: clock.MustHaveClock panics on nil/typed-nil
// clock; nil signer, authorizer, or verifier returns a KindInvalid error at
// construction time (not deferred to the first request).
func NewService(
	clk clock.Clock,
	signer certsigning.Signer,
	authorizer certsigning.Authorizer,
	verifier *auth.EnrollmentCredentialVerifier,
	issuerID certsigning.IssuerID,
) (*Service, error) {
	clock.MustHaveClock(clk, "deviceidentity.NewService")
	if validation.IsNilInterface(signer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"deviceidentity.NewService: signer must not be nil")
	}
	if validation.IsNilInterface(authorizer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"deviceidentity.NewService: authorizer must not be nil")
	}
	if verifier == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"deviceidentity.NewService: verifier must not be nil")
	}
	if issuerID.IsZero() {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"deviceidentity.NewService: issuerID must not be zero")
	}
	return &Service{
		clk:        clk,
		signer:     signer,
		authorizer: authorizer,
		verifier:   verifier,
		issuerID:   issuerID,
	}, nil
}

// ─── ctx key for verified enrollment identity ────────────────────────────────

// verifiedEnrollmentKey is the unexported ctx key type for a verified enrollment.
type verifiedEnrollmentKey struct{}

// verifiedEnrollment is an unexported value type carrying the result of a
// successful enrollment-credential verification. A zero verifiedEnrollment has
// an empty tenant and subject, which the Enroll handler rejects fail-closed via
// the ok bool from verifiedEnrollmentFrom.
type verifiedEnrollment struct {
	tenant  tenant.TenantID
	subject string
}

// withVerifiedEnrollment stores ve in ctx under the unexported key.
// Exported only within the package (unexported type) so tests can inject a
// pre-built verifiedEnrollment without holding a real enrollment credential —
// enabling unit testing of Enroll business logic independently of the
// enrollment-credential JWT scheme.
func withVerifiedEnrollment(ctx context.Context, ve verifiedEnrollment) context.Context {
	return context.WithValue(ctx, verifiedEnrollmentKey{}, ve)
}

// verifiedEnrollmentFrom recovers the verifiedEnrollment from ctx.
// ok is false when the middleware did not run (defense-in-depth guard in Enroll).
func verifiedEnrollmentFrom(ctx context.Context) (verifiedEnrollment, bool) {
	ve, ok := ctx.Value(verifiedEnrollmentKey{}).(verifiedEnrollment)
	return ve, ok
}

// ─── Enrollment-credential middleware ────────────────────────────────────────

const (
	msgMissingBearer = "enrollment credential required"
	msgInvalidBearer = "enrollment credential invalid"
)

// enrollAuthMiddleware returns an http.Handler middleware that extracts and
// verifies the enrollment-credential Bearer token. On success it stores the
// verified identity in the request context and calls next. On failure it writes
// a 401 and does NOT call next (fail-closed).
func (s *Service) enrollAuthMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearerToken(r)
			if token == "" {
				slog.WarnContext(r.Context(), "enrollment credential missing",
					slog.String("reason", "no bearer token in Authorization header"))
				httputil.WriteError(r.Context(), w,
					errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgMissingBearer))
				return
			}
			id, err := s.verifier.Verify(r.Context(), token)
			if err != nil {
				slog.WarnContext(r.Context(), "enrollment credential verification failed",
					slog.String("reason", "bearer token invalid or expired"))
				httputil.WriteError(r.Context(), w,
					errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgInvalidBearer))
				return
			}
			ctx := withVerifiedEnrollment(r.Context(), verifiedEnrollment{
				tenant:  id.Tenant(),
				subject: id.Subject(),
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearerToken extracts a Bearer token from the Authorization header.
// Returns empty string when the header is absent or does not carry a Bearer
// scheme (case-insensitive).
func extractBearerToken(r *http.Request) string {
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		return ""
	}
	const prefix = "bearer "
	if !strings.HasPrefix(strings.ToLower(hdr), prefix) {
		return ""
	}
	return strings.TrimSpace(hdr[len(prefix):])
}

// ─── Enroll ──────────────────────────────────────────────────────────────────

// Enroll implements enroll.Service. It reads the verified enrollment identity
// from the request context (injected by enrollAuthMiddleware) and calls the
// shared signing pipeline.
//
// The deviceID from the credential is AUTHORITATIVE — if the request body also
// carries req.DeviceID it must match (or be empty) to prevent impersonation.
func (s *Service) Enroll(ctx context.Context, req *enroll.Request) (enroll.EnrollResponseObject, error) {
	ve, ok := verifiedEnrollmentFrom(ctx)
	if !ok {
		return enroll.Enroll401ErrorResponse{
			Body: *errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgMissingBearer),
		}, nil
	}

	deviceID := ve.subject
	if req.DeviceID != "" && req.DeviceID != deviceID {
		return enroll.Enroll422ErrorResponse{
			Body: *errcode.New(errcode.KindUnprocessable, errcode.ErrValidationFailed,
				"device id mismatch: request deviceId does not match enrollment credential subject"),
		}, nil
	}

	var dnsNames, ipStrs, uriStrs, emailAddrs []string
	if req.SubjectAltNames != nil {
		dnsNames = req.SubjectAltNames.DNSNames
		ipStrs = req.SubjectAltNames.IPAddresses
		uriStrs = req.SubjectAltNames.Uris
		emailAddrs = req.SubjectAltNames.EmailAddresses
	}

	issued, signErr := s.signIdentity(
		ctx, enrollErrFactory{}, ve.tenant, deviceID,
		req.Csr, req.RequestedDuration, req.Usages,
		dnsNames, ipStrs, uriStrs, emailAddrs,
	)
	if signErr != nil {
		var e enrollTypedErr
		if errors.As(signErr, &e) {
			return e.resp, nil
		}
		return nil, signErr
	}

	chain, err := degenerateCertsOnly(fullChainDER(issued))
	if err != nil {
		return nil, err
	}
	return enroll.Enroll201JSONResponse{
		Data: &enroll.ResponseData{
			Certificate: base64.StdEncoding.EncodeToString(issued.DER()),
			Chain:       base64.StdEncoding.EncodeToString(chain),
			CertRef: &enroll.ResponseDataCertRef{
				Issuer: issued.Scope().Issuer().String(),
				Serial: issued.Serial().String(),
			},
			NotBefore: issued.NotBefore().UTC().Format(time.RFC3339),
			NotAfter:  issued.NotAfter().UTC().Format(time.RFC3339),
			Epoch:     epochInt64(issued.Epoch()),
			Status:    enroll.ResponseDataStatusIssued,
			DeviceID:  deviceID,
		},
	}, nil
}

// enrollTypedErr is a sentinel error wrapper that carries a typed enroll
// response object. signIdentity returns this to signal a 4xx response that
// should be returned to the client (not a framework 5xx).
type enrollTypedErr struct {
	resp enroll.EnrollResponseObject
}

func (e enrollTypedErr) Error() string { return "enrollment error response" }

// renewTypedErr is the renew-side equivalent of enrollTypedErr.
type renewTypedErr struct {
	resp renew.RenewResponseObject
}

func (e renewTypedErr) Error() string { return "renewal error response" }

// fullChainDER returns the certificate path leaf→root: the issued leaf DER
// followed by the issuing chain (intermediate(s) → root). The `chain` response
// field is a PKCS#7 certs-only blob of this FULL path (RFC 7030 §4.2 returns the
// issued certificate together with its chain), so a client receives a
// self-contained path in one field. It is never empty (the leaf is always
// present), so degenerateCertsOnly never rejects it even when the issuing chain
// is empty (a single-tier CA).
func fullChainDER(issued certsigning.IssuedCert) [][]byte {
	return append([][]byte{issued.DER()}, issued.Chain()...)
}

// epochInt64 converts the unsigned renewal epoch to the int64 the response DTO
// uses, clamping at math.MaxInt64. The renewal epoch is a small monotonic
// counter that never realistically approaches int64 max; the clamp is a
// defensive bound that makes the conversion provably overflow-free (gosec G115).
func epochInt64(e uint64) int64 {
	if e > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(e)
}

// ─── Renew ───────────────────────────────────────────────────────────────────

// Renew implements renew.Service. It recovers the device identity from the
// verified mTLS client certificate injected as ctxkeys.PeerIdentity by the
// DeviceMTLSListener's TLS middleware, then calls the shared signing pipeline.
func (s *Service) Renew(ctx context.Context, req *renew.Request) (renew.RenewResponseObject, error) {
	peer, ok := ctxkeys.PeerIdentityFrom(ctx)
	if !ok {
		return renew.Renew401ErrorResponse{
			Body: *errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
				"mTLS client certificate required"),
		}, nil
	}

	tenantStr, deviceID, found := deviceIdentityFromPeer(peer)
	if !found {
		return renew.Renew401ErrorResponse{
			Body: *errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
				"client certificate carries no device-identity URI SAN"),
		}, nil
	}

	if req.DeviceID != "" && req.DeviceID != deviceID {
		return renew.Renew422ErrorResponse{
			Body: *errcode.New(errcode.KindUnprocessable, errcode.ErrValidationFailed,
				"device id mismatch: request deviceId does not match client certificate"),
		}, nil
	}

	var dnsNames, ipStrs, uriStrs, emailAddrs []string
	if req.SubjectAltNames != nil {
		dnsNames = req.SubjectAltNames.DNSNames
		ipStrs = req.SubjectAltNames.IPAddresses
		uriStrs = req.SubjectAltNames.Uris
		emailAddrs = req.SubjectAltNames.EmailAddresses
	}

	// tenant.ParseTenantID validates the raw string extracted from the wire URI
	// SAN; a parse error means the peer cert carries a non-canonical tenant —
	// map to 401 (typed response) rather than a framework 5xx Go error.
	tenantID, parseErr := tenant.ParseTenantID(tenantStr)
	if parseErr != nil {
		return renew.Renew401ErrorResponse{ //nolint:nilerr // parse error → typed 401 response, not a framework 5xx
			Body: *errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
				"client certificate carries invalid tenant identity"),
		}, nil
	}

	issued, signErr := s.signIdentity(
		ctx, renewErrFactory{}, tenantID, deviceID,
		req.Csr, req.RequestedDuration, req.Usages,
		dnsNames, ipStrs, uriStrs, emailAddrs,
	)
	if signErr != nil {
		var e renewTypedErr
		if errors.As(signErr, &e) {
			return e.resp, nil
		}
		return nil, signErr
	}

	chain, err := degenerateCertsOnly(fullChainDER(issued))
	if err != nil {
		return nil, err
	}
	return renew.Renew200JSONResponse{
		Data: &renew.ResponseData{
			Certificate: base64.StdEncoding.EncodeToString(issued.DER()),
			Chain:       base64.StdEncoding.EncodeToString(chain),
			CertRef: &renew.ResponseDataCertRef{
				Issuer: issued.Scope().Issuer().String(),
				Serial: issued.Serial().String(),
			},
			NotBefore:   issued.NotBefore().UTC().Format(time.RFC3339),
			NotAfter:    issued.NotAfter().UTC().Format(time.RFC3339),
			Epoch:       epochInt64(issued.Epoch()),
			Status:      renew.ResponseDataStatusIssued,
			DeviceID:    deviceID,
			PriorSerial: req.PriorSerial,
		},
	}, nil
}

// deviceIdentityFromPeer recovers the (rawTenant, deviceID) from the first
// device-identity URI SAN in the peer certificate.
func deviceIdentityFromPeer(peer ctxkeys.PeerIdentity) (tenantStr, deviceID string, ok bool) {
	for _, u := range peer.URIs {
		t, d, parsed := certsigning.ParseDeviceURISAN(u)
		if parsed {
			return t, d, true
		}
	}
	return "", "", false
}

// ─── Shared signing pipeline ─────────────────────────────────────────────────

// signIdentityErr is the shared error type the signing pipeline returns for
// typed 4xx responses. Enroll and Renew each type-assert to their own variant
// (enrollTypedErr / renewTypedErr). Framework 5xx is returned as a plain error.
//
// The pipeline takes a signErrFactory to construct the correct variant without
// the pipeline itself importing both generated packages' response types.
type signErrFactory interface {
	badRequest(e *errcode.Error) error
	unprocessable(e *errcode.Error) error
	forbidden(e *errcode.Error) error
	unavailable(e *errcode.Error) error
}

// enrollErrFactory builds enrollTypedErr values.
type enrollErrFactory struct{}

func (enrollErrFactory) badRequest(e *errcode.Error) error {
	return enrollTypedErr{enroll.Enroll400ErrorResponse{Body: *e}}
}

func (enrollErrFactory) unprocessable(e *errcode.Error) error {
	return enrollTypedErr{enroll.Enroll422ErrorResponse{Body: *e}}
}

func (enrollErrFactory) forbidden(e *errcode.Error) error {
	return enrollTypedErr{enroll.Enroll403ErrorResponse{Body: *e}}
}

func (enrollErrFactory) unavailable(e *errcode.Error) error {
	return enrollTypedErr{enroll.Enroll503ErrorResponse{Body: *e}}
}

// renewErrFactory builds renewTypedErr values.
type renewErrFactory struct{}

func (renewErrFactory) badRequest(e *errcode.Error) error {
	return renewTypedErr{renew.Renew400ErrorResponse{Body: *e}}
}

func (renewErrFactory) unprocessable(e *errcode.Error) error {
	return renewTypedErr{renew.Renew422ErrorResponse{Body: *e}}
}

func (renewErrFactory) forbidden(e *errcode.Error) error {
	return renewTypedErr{renew.Renew403ErrorResponse{Body: *e}}
}

func (renewErrFactory) unavailable(e *errcode.Error) error {
	return renewTypedErr{renew.Renew503ErrorResponse{Body: *e}}
}

// signIdentity is the shared EST signing pipeline for both Enroll and Renew.
// It returns (issued, nil) on success, (zero, typedErr) for 4xx responses, and
// (zero, plainErr) for framework 5xx (caller returns as Go error).
//
// The fac parameter determines whether 4xx errors are wrapped as enrollTypedErr
// or renewTypedErr so callers can type-switch correctly.
//
// Complexity is kept ≤15 by delegating to buildScope, buildSANs, buildUsages,
// and parseTTL helpers.
func (s *Service) signIdentity(
	ctx context.Context,
	fac signErrFactory,
	tenantID tenant.TenantID,
	deviceIDStr string,
	csrB64 string,
	requestedDuration string,
	usages []string,
	dnsNames, ipStrs, uriStrs, emailAddrs []string,
) (certsigning.IssuedCert, error) {
	// (a) Build scope
	scope, err := buildScope(tenantID, s.issuerID, deviceIDStr)
	if err != nil {
		return certsigning.IssuedCert{}, fac.badRequest(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid cert scope"))
	}

	// (b) Build subject (commonName = deviceID)
	subject, err := certsigning.NewDeviceSubject(tenantID, scope.Device(), deviceIDStr)
	if err != nil {
		return certsigning.IssuedCert{}, fac.badRequest(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid device subject"))
	}

	// (c) Build enrollment claim
	claim, err := certsigning.NewEnrollmentClaim(scope, subject)
	if err != nil {
		return certsigning.IssuedCert{}, fac.badRequest(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid enrollment claim"))
	}

	// (d) Authorize
	grant, err := s.authorizeGrant(ctx, fac, claim)
	if err != nil {
		return certsigning.IssuedCert{}, err
	}

	// (e) Decode CSR
	csrDER, decErr := base64.StdEncoding.DecodeString(csrB64)
	if decErr != nil {
		return certsigning.IssuedCert{}, fac.badRequest(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "csr is not valid base64"))
	}

	// (f) Build SANs (email rejected early; device-self SAN prepended)
	sans, sanErr := buildSANs(fac, scope, dnsNames, ipStrs, uriStrs, emailAddrs)
	if sanErr != nil {
		return certsigning.IssuedCert{}, sanErr
	}

	// (g) Map usages
	ku, kuErr := buildUsages(fac, usages)
	if kuErr != nil {
		return certsigning.IssuedCert{}, kuErr
	}

	// (h) Parse and clamp TTL
	ttl, ttlErr := parseTTL(fac, requestedDuration, grant.MaxTTL())
	if ttlErr != nil {
		return certsigning.IssuedCert{}, ttlErr
	}

	// (i) Build CertRequest
	certReq, err := certsigning.NewCertRequest(scope, subject, csrDER, sans, ku, ttl)
	if err != nil {
		return certsigning.IssuedCert{}, fac.badRequest(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid cert request"))
	}

	// (j+k) Authorize the request (SAN subset + TTL ≤ MaxTTL) and sign.
	return s.authorizeAndSign(ctx, fac, certReq, grant)
}

// authorizeGrant runs AuthorizeEnroll and classifies the outcome: a transient
// PDP-unavailable error → typed 503; a non-granted decision → typed 403; any
// other error → framework 5xx (plain error, caller returns as Go error).
func (s *Service) authorizeGrant(
	ctx context.Context, fac signErrFactory, claim certsigning.EnrollmentClaim,
) (certsigning.SignConstraints, error) {
	grant, err := s.authorizer.AuthorizeEnroll(ctx, claim)
	if err != nil {
		if errKindIs(err, errcode.KindUnavailable) {
			slog.ErrorContext(ctx, "authorization service unavailable",
				slog.String("op", "authorizeGrant"))
			return certsigning.SignConstraints{}, fac.unavailable(
				errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "authorization service unavailable"),
			)
		}
		return certsigning.SignConstraints{}, err // framework 5xx
	}
	if !grant.Granted() {
		return certsigning.SignConstraints{}, fac.forbidden(
			errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "enrollment not authorized"),
		)
	}
	return grant, nil
}

// authorizeAndSign funnels the request through NewAuthorizedCertRequest (SAN
// subset + TTL ≤ MaxTTL enforcement) then Signer.Sign, classifying failures:
// constraint violation → typed 422; denied grant → typed 403; signer-unavailable
// → typed 503; any other signer error → framework 5xx.
func (s *Service) authorizeAndSign(
	ctx context.Context, fac signErrFactory, certReq certsigning.CertRequest, grant certsigning.SignConstraints,
) (certsigning.IssuedCert, error) {
	authReq, err := certsigning.NewAuthorizedCertRequest(certReq, grant)
	if err != nil {
		if errCodeIs(err, errcode.ErrCertConstraintViolation) {
			return certsigning.IssuedCert{}, fac.unprocessable(
				errcode.New(errcode.KindUnprocessable, errcode.ErrValidationFailed, "cert request exceeds signing constraints"),
			)
		}
		return certsigning.IssuedCert{}, fac.forbidden(
			errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "enrollment not authorized"),
		)
	}
	issued, err := s.signer.Sign(ctx, authReq)
	if err != nil {
		if errKindIs(err, errcode.KindUnavailable) {
			slog.ErrorContext(ctx, "signing service unavailable",
				slog.String("op", "authorizeAndSign"))
			return certsigning.IssuedCert{}, fac.unavailable(
				errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "signing service unavailable"),
			)
		}
		return certsigning.IssuedCert{}, err // framework 5xx
	}
	return issued, nil
}

// buildScope constructs the CertScope from the typed tenant, issuer, and device.
func buildScope(tenantID tenant.TenantID, issuerID certsigning.IssuerID, deviceIDStr string) (certsigning.CertScope, error) {
	dev, err := certsigning.NewDeviceID(deviceIDStr)
	if err != nil {
		return certsigning.CertScope{}, err
	}
	scope, err := certsigning.NewCertScope(tenantID, issuerID, dev)
	if err != nil {
		return certsigning.CertScope{}, err
	}
	return scope, nil
}

// buildSANs builds the SubjectAltNames. Always prepends the device-self SPIFFE
// URI SAN. Email SANs are rejected early (422). Additional URI/DNS/IP SANs are
// forwarded; NewAuthorizedCertRequest's subset check rejects them (→ 422) if
// they are not in the grant's AllowedSANs.
func buildSANs(
	fac signErrFactory, scope certsigning.CertScope, dnsNames, ipStrs, uriStrs, emailAddrs []string,
) (certsigning.SubjectAltNames, error) {
	if len(emailAddrs) > 0 {
		return certsigning.SubjectAltNames{}, fac.unprocessable(
			errcode.New(errcode.KindUnprocessable, errcode.ErrValidationFailed, "email SANs are not supported"),
		)
	}

	uris := []*url.URL{certsigning.DeviceURISAN(scope)}
	for _, uStr := range uriStrs {
		u, err := url.Parse(uStr)
		if err != nil {
			return certsigning.SubjectAltNames{}, fac.badRequest(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid URI SAN"))
		}
		uris = append(uris, u)
	}

	var ips []net.IP
	for _, ipStr := range ipStrs {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return certsigning.SubjectAltNames{}, fac.badRequest(
				errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid IP address SAN"),
			)
		}
		ips = append(ips, ip)
	}

	sans, err := certsigning.NewSubjectAltNames(dnsNames, ips, uris)
	if err != nil {
		return certsigning.SubjectAltNames{}, fac.badRequest(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid subject alt names"),
		)
	}
	return sans, nil
}

// buildUsages maps cert-manager-vocabulary usage strings to KeyUsages.
// Empty usages defaults to DigitalSignature + ClientAuth.
func buildUsages(fac signErrFactory, usages []string) (certsigning.KeyUsages, error) {
	if len(usages) == 0 {
		ku, err := certsigning.NewKeyUsages(x509.KeyUsageDigitalSignature, x509.ExtKeyUsageClientAuth)
		if err != nil {
			return certsigning.KeyUsages{}, fac.badRequest(
				errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "default key usages invalid"),
			)
		}
		return ku, nil
	}
	return mapUsages(fac, usages)
}

// mapUsages translates cert-manager-vocabulary usage strings into KeyUsages.
func mapUsages(fac signErrFactory, usages []string) (certsigning.KeyUsages, error) {
	var keyUsage x509.KeyUsage
	var extKeyUsages []x509.ExtKeyUsage
	for _, u := range usages {
		switch strings.ToLower(strings.TrimSpace(u)) {
		case "signing", "digital signature":
			keyUsage |= x509.KeyUsageDigitalSignature
		case "key encipherment":
			keyUsage |= x509.KeyUsageKeyEncipherment
		case "client auth":
			extKeyUsages = append(extKeyUsages, x509.ExtKeyUsageClientAuth)
		case "server auth":
			extKeyUsages = append(extKeyUsages, x509.ExtKeyUsageServerAuth)
		default:
			return certsigning.KeyUsages{}, fac.unprocessable(
				errcode.New(errcode.KindUnprocessable, errcode.ErrValidationFailed, "unsupported key usage",
					errcode.WithInternal(errcode.InternalAttr("keyUsage", u))),
			)
		}
	}
	if keyUsage == 0 {
		// Only extended usages specified — default basic usage to DigitalSignature
		// so NewKeyUsages doesn't reject a zero keyUsage.
		keyUsage = x509.KeyUsageDigitalSignature
	}
	ku, err := certsigning.NewKeyUsages(keyUsage, extKeyUsages...)
	if err != nil {
		return certsigning.KeyUsages{}, fac.badRequest(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "key usages invalid"),
		)
	}
	return ku, nil
}

// parseTTL parses requestedDuration and clamps to maxTTL.
// Empty string → maxTTL directly. Invalid format → 400. Non-positive → maxTTL.
func parseTTL(fac signErrFactory, requestedDuration string, maxTTL time.Duration) (time.Duration, error) {
	if requestedDuration == "" {
		return maxTTL, nil
	}
	ttl, err := time.ParseDuration(requestedDuration)
	if err != nil {
		return 0, fac.badRequest(
			errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid requestedDuration: must be a valid Go duration string"),
		)
	}
	if ttl <= 0 || ttl > maxTTL {
		return maxTTL, nil
	}
	return ttl, nil
}

// ─── Error helpers ────────────────────────────────────────────────────────────

// errKindIs reports whether err is an *errcode.Error with the given Kind.
func errKindIs(err error, kind errcode.Kind) bool {
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return false
	}
	return ec.Kind == kind
}

// errCodeIs reports whether err is an *errcode.Error with the given Code.
func errCodeIs(err error, code errcode.Code) bool {
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return false
	}
	return ec.Code == code
}

// ─── Route builders ──────────────────────────────────────────────────────────

// EnrollRoute returns the bootstrap.FrameworkServedRoute for
// http.deviceidentity.enroll.v1 (POST /api/v1/deviceidentity/enroll).
//
// The route is mounted on PrimaryListener with enrollAuthMiddleware: the
// enrollment-credential bearer token is verified in the middleware before the
// handler body runs. The enroll contract is public:true (no listener JWT gate),
// so the generated handler takes no policy — application-layer auth is the
// middleware + the in-handler certsigning Authorizer (device:enroll).
func (s *Service) EnrollRoute() bootstrap.FrameworkServedRoute {
	h := enroll.NewHandler(s)
	return bootstrap.FrameworkServedRoute{
		ContractID: enrollContractID,
		Group: kcell.RouteGroup{
			Listener:   kcell.PrimaryListener,
			Middleware: []func(http.Handler) http.Handler{s.enrollAuthMiddleware()},
			Register: func(mux kcell.RouteMux) error {
				return h.RegisterRoutes(mux)
			},
		},
	}
}

// RenewRoute returns the bootstrap.FrameworkServedRoute for
// http.deviceidentity.renew.v1 (POST /api/v1/deviceidentity/renew).
//
// Mounted on DeviceMTLSListener whose TLS middleware validates the device
// client certificate and injects ctxkeys.PeerIdentity into the request context.
func (s *Service) RenewRoute() bootstrap.FrameworkServedRoute {
	h := renew.NewHandler(s)
	return bootstrap.FrameworkServedRoute{
		ContractID: renewContractID,
		Group: kcell.RouteGroup{
			Listener: kcell.DeviceMTLSListener,
			Register: func(mux kcell.RouteMux) error {
				return h.RegisterRoutes(mux)
			},
		},
	}
}

// ─── Cacerts ───────────────────────────────────────────────────────────────────

// Cacerts implements cacerts.Service. It returns the issuing CA trust bundle as a
// PKCS#7 certs-only (RFC 5652 §5.1 degenerate SignedData) blob, base64-encoded in
// the JSON envelope, so a device can establish trust BEFORE it enrolls (RFC 7030
// §4.1). The endpoint is public (unauthenticated): the bundle is public CA
// material and a device has no credential yet at trust-acquisition time.
func (s *Service) Cacerts(ctx context.Context, _ *cacerts.Request) (cacerts.CacertsResponseObject, error) {
	p7, failure := s.trustBundlePKCS7(ctx)
	if failure != nil {
		return failure, nil
	}
	return cacerts.Cacerts200JSONResponse{
		Data: &cacerts.ResponseData{
			TrustBundle: base64.StdEncoding.EncodeToString(p7),
		},
	}, nil
}

// msgTrustBundleUnavailable is the const wire message for a cacerts 503 (the
// underlying cause — store error, empty bundle, or PKCS#7 encode failure — is a
// server-side CA fault, never client-attributable; details stay server-side).
const msgTrustBundleUnavailable = "CA trust bundle is not available"

// trustBundlePKCS7 fetches the CA trust bundle and encodes it as a PKCS#7
// certs-only blob. On any failure (store error, empty bundle, or encode failure)
// it returns the contract-declared typed 503 response and nil bytes; on success
// it returns the encoded bytes and a nil response. Returning the typed response
// (not a Go error) keeps the Cacerts nil-error return clean and routes every
// CA-side fault to the declared 503 — a malformed bundle is a server fault, not a
// client 400 (which a bare errcode.KindInvalid from the encoder would map to).
func (s *Service) trustBundlePKCS7(ctx context.Context) ([]byte, cacerts.CacertsResponseObject) {
	bundle, err := s.signer.TrustBundle(ctx)
	if err != nil || len(bundle) == 0 {
		return nil, unavailableCacerts()
	}
	p7, err := degenerateCertsOnly(bundle)
	if err != nil {
		return nil, unavailableCacerts()
	}
	return p7, nil
}

// unavailableCacerts builds the cacerts 503 typed response.
func unavailableCacerts() cacerts.Cacerts503ErrorResponse {
	return cacerts.Cacerts503ErrorResponse{
		Body: *errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, msgTrustBundleUnavailable),
	}
}

// CacertsRoute returns the bootstrap.FrameworkServedRoute for
// http.deviceidentity.cacerts.v1 (GET /api/v1/deviceidentity/cacerts).
//
// Mounted on PrimaryListener with NO auth middleware: the contract is public:true
// (RFC 7030 §4.1 trust distribution is unauthenticated — a device must fetch the
// trust anchors before it possesses any credential).
func (s *Service) CacertsRoute() bootstrap.FrameworkServedRoute {
	h := cacerts.NewHandler(s)
	return bootstrap.FrameworkServedRoute{
		ContractID: cacertsContractID,
		Group: kcell.RouteGroup{
			Listener: kcell.PrimaryListener,
			Register: func(mux kcell.RouteMux) error {
				return h.RegisterRoutes(mux)
			},
		},
	}
}
