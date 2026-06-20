package auth

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// TestNewDeviceSubjectDescriptor_Valid verifies that a well-formed device
// descriptor carries the expected Kind, Sub, and Tenant values.
func TestNewDeviceSubjectDescriptor_Valid(t *testing.T) {
	const (
		tenantStr = "11111111-1111-1111-1111-111111111111"
		deviceStr = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	d, err := NewDeviceSubjectDescriptor(tenantStr, deviceStr)
	if err != nil {
		t.Fatalf("NewDeviceSubjectDescriptor: unexpected error: %v", err)
	}
	if d.Kind() != PrincipalDevice.String() {
		t.Errorf("Kind() = %q, want %q", d.Kind(), PrincipalDevice.String())
	}
	if d.Sub() != deviceStr {
		t.Errorf("Sub() = %q, want %q", d.Sub(), deviceStr)
	}
	// Tenant must be canonical (lowercase).
	if d.Tenant() != tenantStr {
		t.Errorf("Tenant() = %q, want canonical %q", d.Tenant(), tenantStr)
	}
}

// TestNewDeviceSubjectDescriptor_UppercaseTenantNormalized ensures an
// uppercase-UUID tenant is normalized to lowercase canonical form.
func TestNewDeviceSubjectDescriptor_UppercaseTenantNormalized(t *testing.T) {
	const (
		upperTenant = "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA"
		wantTenant  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		deviceStr   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	d, err := NewDeviceSubjectDescriptor(upperTenant, deviceStr)
	if err != nil {
		t.Fatalf("NewDeviceSubjectDescriptor: unexpected error: %v", err)
	}
	if d.Tenant() != wantTenant {
		t.Errorf("Tenant() = %q, want canonical %q", d.Tenant(), wantTenant)
	}
}

// TestNewDeviceSubjectDescriptor_UppercaseDeviceNormalized is the F7 regression:
// an uppercase-UUID device sub must be normalized to lowercase canonical form so
// it byte-for-byte matches the resource side of the PDP ownership rule
// (subject.sub == resource.id, which pdpauthz canonicalizes via the same
// httputil.ParseCanonicalUUID). Without normalization an uppercase device UUID
// would fail the equality and be wrongly denied enrollment.
func TestNewDeviceSubjectDescriptor_UppercaseDeviceNormalized(t *testing.T) {
	const (
		tenantStr  = "11111111-1111-1111-1111-111111111111"
		upperDev   = "ABCDABCD-ABCD-ABCD-ABCD-ABCDABCDABCD"
		wantDevice = "abcdabcd-abcd-abcd-abcd-abcdabcdabcd"
	)
	d, err := NewDeviceSubjectDescriptor(tenantStr, upperDev)
	if err != nil {
		t.Fatalf("NewDeviceSubjectDescriptor: unexpected error: %v", err)
	}
	if d.Sub() != wantDevice {
		t.Errorf("Sub() = %q, want canonical lowercase %q", d.Sub(), wantDevice)
	}
}

// TestNewDeviceSubjectDescriptor_NonUUIDDevicePassthrough locks the documented
// behavior that a non-UUID device sub (the constructor permits any non-empty id)
// passes through unchanged — canonicalization only applies to UUID subs.
func TestNewDeviceSubjectDescriptor_NonUUIDDevicePassthrough(t *testing.T) {
	const (
		tenantStr = "11111111-1111-1111-1111-111111111111"
		deviceStr = "Device-Alpha-01"
	)
	d, err := NewDeviceSubjectDescriptor(tenantStr, deviceStr)
	if err != nil {
		t.Fatalf("NewDeviceSubjectDescriptor: unexpected error: %v", err)
	}
	if d.Sub() != deviceStr {
		t.Errorf("Sub() = %q, want non-UUID sub unchanged %q", d.Sub(), deviceStr)
	}
}

// TestNewDeviceSubjectDescriptor_EmptyDeviceID verifies that an empty deviceID
// is rejected with a KindInvalid errcode.
func TestNewDeviceSubjectDescriptor_EmptyDeviceID(t *testing.T) {
	const tenantStr = "22222222-2222-2222-2222-222222222222"
	_, err := NewDeviceSubjectDescriptor(tenantStr, "")
	if err == nil {
		t.Fatal("NewDeviceSubjectDescriptor with empty deviceID: expected error, got nil")
	}
	var ecErr *errcode.Error
	if !isErrAs(err, &ecErr) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ecErr.Kind != errcode.KindInvalid {
		t.Errorf("Kind = %v, want KindInvalid", ecErr.Kind)
	}
}

// TestNewDeviceSubjectDescriptor_EmptyTenantID verifies that an empty tenantID
// is rejected fail-closed.
func TestNewDeviceSubjectDescriptor_EmptyTenantID(t *testing.T) {
	const deviceStr = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	_, err := NewDeviceSubjectDescriptor("", deviceStr)
	if err == nil {
		t.Fatal("NewDeviceSubjectDescriptor with empty tenantID: expected error, got nil")
	}
}

// TestNewDeviceSubjectDescriptor_InvalidTenantID verifies that a non-UUID
// tenantID is rejected fail-closed.
func TestNewDeviceSubjectDescriptor_InvalidTenantID(t *testing.T) {
	const deviceStr = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	_, err := NewDeviceSubjectDescriptor("not-a-uuid", deviceStr)
	if err == nil {
		t.Fatal("NewDeviceSubjectDescriptor with invalid tenantID: expected error, got nil")
	}
}

// TestNewDeviceSubjectDescriptor_NilUUIDTenantRejected verifies that the
// reserved nil UUID is rejected.
func TestNewDeviceSubjectDescriptor_NilUUIDTenantRejected(t *testing.T) {
	const deviceStr = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	_, err := NewDeviceSubjectDescriptor("00000000-0000-0000-0000-000000000000", deviceStr)
	if err == nil {
		t.Fatal("NewDeviceSubjectDescriptor with nil UUID tenant: expected error, got nil")
	}
}

// TestSubjectDescriptor_ZeroValue verifies that the zero value returns empty
// strings from all accessors — it is invalid and callers must not use it.
func TestSubjectDescriptor_ZeroValue(t *testing.T) {
	var d SubjectDescriptor
	if d.Kind() != "" {
		t.Errorf("zero SubjectDescriptor.Kind() = %q, want empty", d.Kind())
	}
	if d.Sub() != "" {
		t.Errorf("zero SubjectDescriptor.Sub() = %q, want empty", d.Sub())
	}
	if d.Tenant() != "" {
		t.Errorf("zero SubjectDescriptor.Tenant() = %q, want empty", d.Tenant())
	}
}

// isErrAs is a simple errors.As without importing errors (avoids cycle risk).
func isErrAs(err error, target **errcode.Error) bool {
	if err == nil {
		return false
	}
	e := &errcode.Error{}
	if errors.As(err, &e) {
		*target = e
		return true
	}
	// Unwrap one level.
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return isErrAs(u.Unwrap(), target)
	}
	return false
}
