package certsigning_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// testGrantTTL is a site-specific granted max TTL. Per TEST-TIME-LITERAL-01, a
// time.Duration expression containing a literal must be a package-level const.
const testGrantTTL = 2 * time.Hour

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
