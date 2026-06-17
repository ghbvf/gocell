package interceptor

// auth_reason_test.go — #2008 F6: machine-readable gRPC auth/authz deny reasons.
//
// Covers the sealed denyReason registry (closed set, unique, non-empty), the
// deniedStatus ErrorInfo envelope (code + message + reason + domain + non-PII
// metadata), and the reason mapping of the two error classifiers.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// errorInfoOf extracts the google.rpc.ErrorInfo detail from a gRPC status error.
func errorInfoOf(t *testing.T, err error) *errdetails.ErrorInfo {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok, "must be a gRPC status error")
	for _, d := range st.Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			return ei
		}
	}
	t.Fatalf("status carries no ErrorInfo detail: %v", err)
	return nil
}

// TestDenyReasonRegistry_UniqueNonEmptyClosedSet asserts the sealed reason set is
// well-formed (unique, non-empty) and equals the documented closed vocabulary —
// adding/renaming a reason is a deliberate edit that must update this golden (and
// the client-facing contract).
func TestDenyReasonRegistry_UniqueNonEmptyClosedSet(t *testing.T) {
	t.Parallel()
	got := make([]string, 0, len(allDenyReasons))
	seen := map[string]bool{}
	for _, r := range allDenyReasons {
		s := r.String()
		require.NotEmpty(t, s, "reason spelling must be non-empty")
		require.False(t, seen[s], "duplicate reason spelling %q", s)
		seen[s] = true
		got = append(got, s)
	}
	want := []string{
		"INVALID_AUTH_METADATA", "INVALID_TOKEN", "AUTHN_SERVICE_UNAVAILABLE",
		"PASSWORD_RESET_REQUIRED", "AUTHENTICATION_REQUIRED", "NO_PERMISSION_MAPPING",
		"AUTHZ_NOT_WIRED", "INSUFFICIENT_PERMISSIONS", "OBLIGATIONS_UNSUPPORTED",
		"PDP_UNAVAILABLE", "AUTHORIZATION_DENIED",
		// #2207: F3 fail-closed resource extraction failure.
		"RESOURCE_UNRESOLVED",
	}
	assert.ElementsMatch(t, want, got, "allDenyReasons must equal the documented closed set")
}

// TestDeniedStatus_CarriesErrorInfo verifies the envelope: canonical code + human
// message for humans, ErrorInfo.Reason/Domain/Metadata for clients.
func TestDeniedStatus_CarriesErrorInfo(t *testing.T) {
	t.Parallel()
	err := deniedStatus(codes.PermissionDenied, "nope", reasonInsufficientPermissions,
		denyMeta("/svc/M", "perm:x"))
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st.Code())
	assert.Equal(t, "nope", st.Message())
	ei := errorInfoOf(t, err)
	assert.Equal(t, "INSUFFICIENT_PERMISSIONS", ei.GetReason())
	assert.Equal(t, denyReasonDomain, ei.GetDomain())
	assert.Equal(t, "/svc/M", ei.GetMetadata()["method"])
	assert.Equal(t, "perm:x", ei.GetMetadata()["permission"])
}

// TestDenyMeta_OmitsEmptyPermission verifies the pre-PDP authn denials (no resolved
// permission) carry only the method, never an empty permission key, and never PII.
func TestDenyMeta_OmitsEmptyPermission(t *testing.T) {
	t.Parallel()
	md := denyMeta("/svc/M", "")
	_, hasPerm := md["permission"]
	assert.False(t, hasPerm, "empty permission must be omitted")
	assert.Equal(t, "/svc/M", md["method"])
}

// TestPdpErrorToStatus_Reasons verifies the PDP-error classifier maps a store outage
// to Unavailable/PDP_UNAVAILABLE and any other error to PermissionDenied/AUTHORIZATION_DENIED.
func TestPdpErrorToStatus_Reasons(t *testing.T) {
	t.Parallel()
	unavail := errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable, "store down")
	st, ok := status.FromError(pdpErrorToStatus(unavail, "/svc/M", "perm:x"))
	require.True(t, ok)
	assert.Equal(t, codes.Unavailable, st.Code())
	assert.Equal(t, "PDP_UNAVAILABLE", errorInfoOf(t, st.Err()).GetReason())

	st2, ok := status.FromError(pdpErrorToStatus(errors.New("boom"), "/svc/M", "perm:x"))
	require.True(t, ok)
	assert.Equal(t, codes.PermissionDenied, st2.Code())
	assert.Equal(t, "AUTHORIZATION_DENIED", errorInfoOf(t, st2.Err()).GetReason())
}

// TestAuthErrorToStatus_Reasons verifies the verifier-error classifier maps an infra
// outage to Unavailable/AUTHN_SERVICE_UNAVAILABLE and any other failure to
// Unauthenticated/INVALID_TOKEN.
func TestAuthErrorToStatus_Reasons(t *testing.T) {
	t.Parallel()
	unavail := errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable, "verifier down")
	st, ok := status.FromError(authErrorToStatus(unavail, "/svc/M"))
	require.True(t, ok)
	assert.Equal(t, codes.Unavailable, st.Code())
	assert.Equal(t, "AUTHN_SERVICE_UNAVAILABLE", errorInfoOf(t, st.Err()).GetReason())

	st2, ok := status.FromError(authErrorToStatus(errors.New("bad token"), "/svc/M"))
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st2.Code())
	assert.Equal(t, "INVALID_TOKEN", errorInfoOf(t, st2.Err()).GetReason())
}
