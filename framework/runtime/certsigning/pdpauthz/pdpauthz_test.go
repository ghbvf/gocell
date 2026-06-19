package pdpauthz_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
	"github.com/ghbvf/gocell/framework/runtime/certsigning/pdpauthz"
)

// testMaxTTL is the operator-configured issuance ceiling used in all tests.
const testMaxTTL = 24 * time.Hour

// Test UUID values (canonical form). Use distinct UUIDs so tests catch
// resource mis-routing bugs.
const (
	testTenantID = "11111111-1111-1111-1111-111111111111"
	testDeviceID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

// fakeSubjectAuthorizer is a test-double for auth.SubjectAuthorizer.
// It captures the most recent call arguments and delegates to the configured fn.
type fakeSubjectAuthorizer struct {
	fn func(ctx context.Context, subject auth.SubjectDescriptor, resource, action string) (authz.Decision, error)

	// captured values from the most recent AuthorizeAs call.
	gotDescriptor auth.SubjectDescriptor
	gotResource   string
	gotAction     string
}

func (f *fakeSubjectAuthorizer) AuthorizeAs(
	ctx context.Context, subject auth.SubjectDescriptor, resource, action string,
) (authz.Decision, error) {
	f.gotDescriptor = subject
	f.gotResource = resource
	f.gotAction = action
	return f.fn(ctx, subject, resource, action)
}

// mustClaim builds a valid EnrollmentClaim for the canonical test device.
func mustClaim(t *testing.T) certsigning.EnrollmentClaim {
	t.Helper()
	tid, err := tenant.ParseTenantID(testTenantID)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	iss, err := certsigning.NewIssuerID("ca-root")
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	dev, err := certsigning.NewDeviceID(testDeviceID)
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	scope, err := certsigning.NewCertScope(tid, iss, dev)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	subject, err := certsigning.NewDeviceSubject(tid, dev, testDeviceID)
	if err != nil {
		t.Fatalf("subject: %v", err)
	}
	claim, err := certsigning.NewEnrollmentClaim(scope, subject)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return claim
}

// mustScope builds the canonical test CertScope (same dimensions mustClaim uses),
// so a test can compute the expected device-self identity SAN.
func mustScope(t *testing.T) certsigning.CertScope {
	t.Helper()
	tid, err := tenant.ParseTenantID(testTenantID)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	iss, err := certsigning.NewIssuerID("ca-root")
	if err != nil {
		t.Fatalf("issuer: %v", err)
	}
	dev, err := certsigning.NewDeviceID(testDeviceID)
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	scope, err := certsigning.NewCertScope(tid, iss, dev)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	return scope
}

// zeroObligations returns an Obligations with zero RowScope and empty FieldMask.
// Used for Allow decisions that carry no mandatory PEP duties.
func zeroObligations() authz.Obligations {
	return authz.Obligations{}
}

// nonZeroObligations returns an Obligations that is NOT zero — used to trigger
// the obligation fail-closed path. RowScope=1 (tenant.RowScopeSelf) is valid.
func nonZeroObligations() authz.Obligations {
	return authz.Obligations{RowScope: tenant.RowScopeSelf}
}

// makeAllow builds a valid Allow Decision with the given obligations.
func makeAllow(t *testing.T, obl authz.Obligations) authz.Decision {
	t.Helper()
	dec, err := authz.Allow(obl)
	if err != nil {
		t.Fatalf("authz.Allow: %v", err)
	}
	return dec
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()

	t.Run("nil pdp interface rejected", func(t *testing.T) {
		t.Parallel()
		_, err := pdpauthz.New(nil, testMaxTTL)
		if err == nil {
			t.Fatal("expected error for nil pdp, got nil")
		}
		var ec *errcode.Error
		if !errors.As(err, &ec) {
			t.Fatalf("error %v is not *errcode.Error", err)
		}
		if ec.Kind != errcode.KindInvalid {
			t.Errorf("kind = %v, want KindInvalid", ec.Kind)
		}
	})

	t.Run("typed nil pdp rejected", func(t *testing.T) {
		t.Parallel()
		// A typed nil interface value ((*fakeSubjectAuthorizer)(nil)) must also
		// be rejected — validation.IsNilInterface covers this case.
		var typedNil *fakeSubjectAuthorizer
		_, err := pdpauthz.New(typedNil, testMaxTTL)
		if err == nil {
			t.Fatal("expected error for typed-nil pdp, got nil")
		}
	})

	t.Run("zero maxTTL rejected", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return authz.Deny("unused"), nil
		}}
		_, err := pdpauthz.New(fake, 0)
		if err == nil {
			t.Fatal("expected error for zero maxTTL, got nil")
		}
		var ec *errcode.Error
		if !errors.As(err, &ec) {
			t.Fatalf("error %v is not *errcode.Error", err)
		}
		if ec.Kind != errcode.KindInvalid {
			t.Errorf("kind = %v, want KindInvalid", ec.Kind)
		}
	})

	t.Run("negative maxTTL rejected", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return authz.Deny("unused"), nil
		}}
		_, err := pdpauthz.New(fake, -time.Second)
		if err == nil {
			t.Fatal("expected error for negative maxTTL, got nil")
		}
	})

	t.Run("valid construction succeeds", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return authz.Deny("unused"), nil
		}}
		a, err := pdpauthz.New(fake, testMaxTTL)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if a == nil {
			t.Fatal("expected non-nil Authorizer")
		}
	})
}

func TestAuthorizeEnroll(t *testing.T) {
	t.Parallel()

	t.Run("Allow with zero obligations grants SignConstraints", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return makeAllow(t, zeroObligations()), nil
		}}
		a, err := pdpauthz.New(fake, testMaxTTL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		sc, err := a.AuthorizeEnroll(context.Background(), mustClaim(t))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !sc.Granted() {
			t.Error("expected Granted()==true on Allow decision")
		}
		if sc.MaxTTL() != testMaxTTL {
			t.Errorf("MaxTTL = %v, want %v", sc.MaxTTL(), testMaxTTL)
		}
		// AllowedSANs must be exactly the device-self identity SAN
		// (spiffe://<tenant>/device/<deviceID>): a renewal front-end recovers the
		// device identity from it, and any OTHER requested SAN is rejected
		// (deny-by-default subset-of check in NewAuthorizedCertRequest).
		uris := sc.AllowedSANs().URIs()
		if len(uris) != 1 {
			t.Fatalf("AllowedSANs URIs = %d, want 1 (device-self identity SAN)", len(uris))
		}
		wantSAN := certsigning.DeviceURISAN(mustScope(t)).String()
		if uris[0].String() != wantSAN {
			t.Errorf("AllowedSANs URI = %q, want %q", uris[0].String(), wantSAN)
		}
	})

	t.Run("Deny returns non-granted SignConstraints with nil error", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return authz.Deny("no matching allow rule"), nil
		}}
		a, err := pdpauthz.New(fake, testMaxTTL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		sc, err := a.AuthorizeEnroll(context.Background(), mustClaim(t))
		if err != nil {
			t.Errorf("Deny must return nil error, got: %v", err)
		}
		if sc.Granted() {
			t.Error("Deny must return non-granted SignConstraints (fail-closed)")
		}
	})

	t.Run("PDP error propagates, SignConstraints not granted", func(t *testing.T) {
		t.Parallel()
		pdpErr := errors.New("store unavailable")
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return authz.Decision{}, pdpErr
		}}
		a, err := pdpauthz.New(fake, testMaxTTL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		sc, err := a.AuthorizeEnroll(context.Background(), mustClaim(t))
		if err == nil {
			t.Fatal("expected error from PDP to be propagated, got nil")
		}
		if !errors.Is(err, pdpErr) {
			t.Errorf("err = %v, want wrapping %v", err, pdpErr)
		}
		if sc.Granted() {
			t.Error("on PDP error, SignConstraints must not be granted")
		}
	})

	t.Run("Allow with non-zero obligation is denied (fail-closed)", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return makeAllow(t, nonZeroObligations()), nil
		}}
		a, err := pdpauthz.New(fake, testMaxTTL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		sc, err := a.AuthorizeEnroll(context.Background(), mustClaim(t))
		if err != nil {
			t.Errorf("obligation fail-closed must return nil error, got: %v", err)
		}
		if sc.Granted() {
			t.Error("Allow with non-zero obligation must be denied (cert PEP cannot enforce obligation)")
		}
	})

	t.Run("PDP receives canonical device UUID as resource", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return makeAllow(t, zeroObligations()), nil
		}}
		a, err := pdpauthz.New(fake, testMaxTTL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		_, _ = a.AuthorizeEnroll(context.Background(), mustClaim(t))

		// testDeviceID is already canonical UUID; after ParseCanonicalUUID it
		// stays the same (lowercase dashed form).
		wantResource := testDeviceID
		if fake.gotResource != wantResource {
			t.Errorf("resource = %q, want %q", fake.gotResource, wantResource)
		}
	})

	t.Run("PDP receives device:enroll action", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return makeAllow(t, zeroObligations()), nil
		}}
		a, err := pdpauthz.New(fake, testMaxTTL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		_, _ = a.AuthorizeEnroll(context.Background(), mustClaim(t))

		wantAction := authz.PermDeviceEnroll().String()
		if fake.gotAction != wantAction {
			t.Errorf("action = %q, want %q", fake.gotAction, wantAction)
		}
	})

	t.Run("PDP receives device-kind SubjectDescriptor", func(t *testing.T) {
		t.Parallel()
		fake := &fakeSubjectAuthorizer{fn: func(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
			return makeAllow(t, zeroObligations()), nil
		}}
		a, err := pdpauthz.New(fake, testMaxTTL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		_, _ = a.AuthorizeEnroll(context.Background(), mustClaim(t))

		if fake.gotDescriptor.Kind() != auth.PrincipalDevice.String() {
			t.Errorf("descriptor.Kind = %q, want %q", fake.gotDescriptor.Kind(), auth.PrincipalDevice.String())
		}
		if fake.gotDescriptor.Sub() != testDeviceID {
			t.Errorf("descriptor.Sub = %q, want %q", fake.gotDescriptor.Sub(), testDeviceID)
		}
		if fake.gotDescriptor.Tenant() != testTenantID {
			t.Errorf("descriptor.Tenant = %q, want %q", fake.gotDescriptor.Tenant(), testTenantID)
		}
	})
}
