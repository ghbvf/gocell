package certsigning_test

import (
	"context"
	"crypto/x509"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// Site-specific durations. Per TEST-TIME-LITERAL-01 a time.Duration expression
// containing a literal must be a package-level const.
const (
	testGrantTTL = 2 * time.Hour
	testTightTTL = 30 * time.Minute // < the requests' 1h TTL, to trip the TTL ceiling
)

func TestNewEnrollmentClaim(t *testing.T) {
	t.Parallel()
	scope := mustScope(t)
	dev, _ := cs.NewDeviceID("device-1")
	subject, _ := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, "device-1")

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		claim, err := cs.NewEnrollmentClaim(scope, subject)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if claim.Scope() != scope || claim.Subject() != subject {
			t.Error("claim accessors mismatch")
		}
	})
	t.Run("zero scope", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewEnrollmentClaim(cs.CertScope{}, subject)
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("zero subject", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewEnrollmentClaim(scope, cs.DeviceSubject{})
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("cross-device subject rejected", func(t *testing.T) {
		t.Parallel()
		devB, _ := cs.NewDeviceID("device-2")
		subjB, _ := cs.NewDeviceSubject(mustTenant(t, testTenant), devB, "device-2")
		_, err := cs.NewEnrollmentClaim(scope, subjB) // scope device-1 vs subject device-2
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
	t.Run("cross-tenant subject rejected", func(t *testing.T) {
		t.Parallel()
		subjB, _ := cs.NewDeviceSubject(mustTenant(t, testTenantB), dev, "device-1")
		_, err := cs.NewEnrollmentClaim(scope, subjB) // scope tenant-A vs subject tenant-B
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
}

func TestSignConstraintsFailClosed(t *testing.T) {
	t.Parallel()

	// The zero value is the fail-closed deny: a Signer must refuse unless Granted.
	var zero cs.SignConstraints
	if zero.Granted() {
		t.Fatal("zero SignConstraints must NOT be granted (fail-closed)")
	}

	t.Run("non-positive ttl rejected", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewSignConstraints(0, cs.SubjectAltNames{})
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})

	t.Run("granted constraints", func(t *testing.T) {
		t.Parallel()
		sans, _ := cs.NewSubjectAltNames([]string{"device-1.example"}, nil, nil)
		c, err := cs.NewSignConstraints(testGrantTTL, sans)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if !c.Granted() || c.MaxTTL() != testGrantTTL {
			t.Error("granted constraints accessors mismatch")
		}
		if c.AllowedSANs().IsEmpty() {
			t.Error("allowed SANs should carry the entry")
		}
	})
}

// denyAuthorizer is a fake Authorizer that denies by returning the zero
// (non-granted) SignConstraints — proving the interface's nil-grant fail-closed
// contract is expressible without an error.
type denyAuthorizer struct{}

func (denyAuthorizer) AuthorizeEnroll(context.Context, cs.EnrollmentClaim) (cs.SignConstraints, error) {
	return cs.SignConstraints{}, nil
}

// grantAuthorizer is a fake Authorizer that grants a bounded constraint.
type grantAuthorizer struct{ maxTTL time.Duration }

func (g grantAuthorizer) AuthorizeEnroll(context.Context, cs.EnrollmentClaim) (cs.SignConstraints, error) {
	return cs.NewSignConstraints(g.maxTTL, cs.SubjectAltNames{})
}

func TestAuthorizerContract(t *testing.T) {
	t.Parallel()
	scope := mustScope(t)
	dev, _ := cs.NewDeviceID("device-1")
	subject, _ := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, "device-1")
	claim, _ := cs.NewEnrollmentClaim(scope, subject)

	var deny cs.Authorizer = denyAuthorizer{}
	got, err := deny.AuthorizeEnroll(context.Background(), claim)
	if err != nil {
		t.Fatalf("deny authorizer error: %v", err)
	}
	if got.Granted() {
		t.Error("deny authorizer must yield a non-granted constraint (fail-closed)")
	}

	var grant cs.Authorizer = grantAuthorizer{maxTTL: time.Hour}
	got, err = grant.AuthorizeEnroll(context.Background(), claim)
	if err != nil || !got.Granted() {
		t.Fatalf("grant authorizer: %v granted=%v", err, got.Granted())
	}
}

func TestNewAuthorizedCertRequest(t *testing.T) {
	t.Parallel()
	scope := mustScope(t)
	dev, _ := cs.NewDeviceID("device-1")
	subject, _ := cs.NewDeviceSubject(mustTenant(t, testTenant), dev, "device-1")
	usages, _ := cs.NewKeyUsages(x509.KeyUsageDigitalSignature)
	sans, _ := cs.NewSubjectAltNames([]string{"device-1.example"}, nil, nil)
	req, err := cs.NewCertRequest(scope, subject, testCSRDER(t), sans, usages, time.Hour)
	if err != nil {
		t.Fatalf("req: %v", err)
	}

	t.Run("granted within constraints", func(t *testing.T) {
		t.Parallel()
		grant, _ := cs.NewSignConstraints(testGrantTTL, sans)
		auth, err := cs.NewAuthorizedCertRequest(req, grant)
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if auth.Request().Scope() != scope {
			t.Error("authorized request must carry the underlying request")
		}
	})
	t.Run("not granted denied", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewAuthorizedCertRequest(req, cs.SignConstraints{}) // zero = not granted
		assertCode(t, err, errcode.ErrCertAuthorizeDenied)
	})
	t.Run("ttl exceeds granted max", func(t *testing.T) {
		t.Parallel()
		grant, _ := cs.NewSignConstraints(testTightTTL, sans) // 30m < req 1h
		_, err := cs.NewAuthorizedCertRequest(req, grant)
		assertCode(t, err, errcode.ErrCertConstraintViolation)
	})
	t.Run("san outside granted allowance", func(t *testing.T) {
		t.Parallel()
		grant, _ := cs.NewSignConstraints(testGrantTTL, cs.SubjectAltNames{}) // empty = deny-by-default
		_, err := cs.NewAuthorizedCertRequest(req, grant)
		assertCode(t, err, errcode.ErrCertConstraintViolation)
	})
	t.Run("ip and uri SAN allowance enforced", func(t *testing.T) {
		t.Parallel()
		ip := net.ParseIP("10.1.2.3")
		uri := &url.URL{Scheme: "spiffe", Host: "td", Path: "/dev"}
		reqSANs, _ := cs.NewSubjectAltNames([]string{"device-1.example"}, []net.IP{ip}, []*url.URL{uri})
		r, err := cs.NewCertRequest(scope, subject, testCSRDER(t), reqSANs, usages, time.Hour)
		if err != nil {
			t.Fatalf("req: %v", err)
		}
		// Allowing exactly the requested DNS+IP+URI → granted.
		okGrant, _ := cs.NewSignConstraints(testGrantTTL, reqSANs)
		if _, err := cs.NewAuthorizedCertRequest(r, okGrant); err != nil {
			t.Errorf("IP/URI within allowance should be granted: %v", err)
		}
		// Allowing only DNS (no IP/URI) → the IP SAN is outside allowance → violation.
		dnsOnly, _ := cs.NewSubjectAltNames([]string{"device-1.example"}, nil, nil)
		_, err = cs.NewAuthorizedCertRequest(r, mustGrant(t, dnsOnly))
		assertCode(t, err, errcode.ErrCertConstraintViolation)
	})
	t.Run("zero request rejected", func(t *testing.T) {
		t.Parallel()
		_, err := cs.NewAuthorizedCertRequest(cs.CertRequest{}, mustGrant(t, sans))
		assertCode(t, err, errcode.ErrCertRequestInvalid)
	})
}

// mustGrant builds a granted SignConstraints for tests.
func mustGrant(t *testing.T, allowed cs.SubjectAltNames) cs.SignConstraints {
	t.Helper()
	g, err := cs.NewSignConstraints(testGrantTTL, allowed)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	return g
}
