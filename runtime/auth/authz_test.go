package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// canonicalUUID is a known canonical lowercase UUID used across B4 tests.
const canonicalUUID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

// uppercaseUUID is the same UUID in UPPERCASE — must still match canonical.
const uppercaseUUID = "6BA7B810-9DAD-11D1-80B4-00C04FD430C8"

// compactUUID is the same UUID without dashes — must still match canonical.
const compactUUID = "6ba7b8109dad11d180b400c04fd430c8"

func TestRequireSelfOrRole(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		targetID string
		roles    []string
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:     "self-access allowed",
			ctx:      withPrincipalCtx("user-1", nil),
			targetID: "user-1",
			roles:    []string{"admin"},
			wantErr:  false,
		},
		{
			name:     "admin bypass allowed",
			ctx:      withPrincipalCtx("user-2", []string{"admin"}),
			targetID: "user-1",
			roles:    []string{"admin"},
			wantErr:  false,
		},
		{
			name:     "different user no admin denied",
			ctx:      withPrincipalCtx("user-2", []string{"viewer"}),
			targetID: "user-1",
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:     "missing subject denied",
			ctx:      context.Background(),
			targetID: "user-1",
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name:     "empty targetID denied",
			ctx:      withPrincipalCtx("user-1", nil),
			targetID: "",
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:     "multiple bypass roles second matches",
			ctx:      withPrincipalCtx("user-2", []string{"operator"}),
			targetID: "user-1",
			roles:    []string{"admin", "operator"},
			wantErr:  false,
		},
		{
			name:     "no bypass roles specified only self allowed",
			ctx:      withPrincipalCtx("user-2", []string{"admin"}),
			targetID: "user-1",
			roles:    nil,
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireSelfOrRole(tc.ctx, tc.targetID, tc.roles...)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, tc.wantCode, ecErr.Code)
		})
	}
}

func TestRequireAnyRole(t *testing.T) {
	tests := []struct {
		name     string
		ctx      context.Context
		roles    []string
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:    "admin role allowed",
			ctx:     withPrincipalCtx("user-1", []string{"admin"}),
			roles:   []string{"admin"},
			wantErr: false,
		},
		{
			name:    "second role matches",
			ctx:     withPrincipalCtx("user-1", []string{"operator"}),
			roles:   []string{"admin", "operator"},
			wantErr: false,
		},
		{
			name:     "no matching role denied",
			ctx:      withPrincipalCtx("user-1", []string{"viewer"}),
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:     "no roles in principal denied",
			ctx:      withPrincipalCtx("user-1", nil),
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:     "missing principal denied",
			ctx:      context.Background(),
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name:     "empty string subject — ErrAuthUnauthorized (subject invariant enforced at authz entry)",
			ctx:      withPrincipalCtx("", nil),
			roles:    []string{"admin"},
			wantErr:  true,
			wantCode: errcode.ErrAuthUnauthorized,
		},
		{
			name:     "empty required roles denied",
			ctx:      withPrincipalCtx("user-1", []string{"admin"}),
			roles:    nil,
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireAnyRole(tc.ctx, tc.roles...)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, tc.wantCode, ecErr.Code)
		})
	}
}

func TestRequireSelfOrRole_EmptyTargetID_LogsWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx := withLogger(context.Background(), logger)
	ctx = WithPrincipal(ctx, &Principal{
		Kind:    PrincipalUser,
		Subject: "user-1",
		Roles:   []string{"admin"},
	})

	err := RequireSelfOrRole(ctx, "", "admin")
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "empty targetID")
}

// TestRequireAnyRole_ServicePrincipalAlsoWorks verifies that a service Principal
// (Kind=PrincipalService) is accepted by RequireAnyRole when its role matches.
func TestRequireAnyRole_ServicePrincipalAlsoWorks(t *testing.T) {
	ctx := WithPrincipal(context.Background(), &Principal{
		Kind:    PrincipalService,
		Subject: ServiceNameInternal,
		Roles:   []string{RoleInternalAdmin},
	})
	err := RequireAnyRole(ctx, RoleInternalAdmin)
	assert.NoError(t, err)
}

// TestRequireAnyRole_NoPrincipal_Unauthorized verifies that a ctx with no
// Principal returns ErrAuthUnauthorized (401 domain error).
func TestRequireAnyRole_NoPrincipal_Unauthorized(t *testing.T) {
	err := RequireAnyRole(context.Background(), "admin")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthUnauthorized, ecErr.Code)
}

// TestRequireAnyRole_PrincipalWithoutRole_Forbidden verifies that a Principal
// present in ctx but lacking the required role returns ErrAuthForbidden (403).
func TestRequireAnyRole_PrincipalWithoutRole_Forbidden(t *testing.T) {
	ctx := WithPrincipal(context.Background(), &Principal{
		Kind:    PrincipalUser,
		Subject: "user-1",
		Roles:   []string{"viewer"},
	})
	err := RequireAnyRole(ctx, "admin")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthForbidden, ecErr.Code)
}

// TestRequireSelfOrRole_NoPrincipal_Unauthorized verifies that a ctx with no
// Principal returns ErrAuthUnauthorized.
func TestRequireSelfOrRole_NoPrincipal_Unauthorized(t *testing.T) {
	err := RequireSelfOrRole(context.Background(), "user-1", "admin")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthUnauthorized, ecErr.Code)
}

// TestRequireSelfOrRole_SelfMatch_Ok verifies that a Principal whose Subject
// matches targetSubject returns nil regardless of roles.
func TestRequireSelfOrRole_SelfMatch_Ok(t *testing.T) {
	ctx := WithPrincipal(context.Background(), &Principal{
		Kind:    PrincipalUser,
		Subject: "user-42",
		Roles:   nil,
	})
	err := RequireSelfOrRole(ctx, "user-42", "admin")
	assert.NoError(t, err)
}

// TestRequireSelfOrRole_RoleMatch_Ok verifies that a Principal whose Subject
// does not match but holds the required role returns nil.
func TestRequireSelfOrRole_RoleMatch_Ok(t *testing.T) {
	ctx := WithPrincipal(context.Background(), &Principal{
		Kind:    PrincipalUser,
		Subject: "user-99",
		Roles:   []string{"admin"},
	})
	err := RequireSelfOrRole(ctx, "user-42", "admin")
	assert.NoError(t, err)
}

// TestRequireAnyRole_EmptyUserSubject_Unauthorized verifies that a PrincipalUser
// with an empty Subject is rejected with ErrAuthUnauthorized by RequireAnyRole
// (G1.B authz-entry defence). This guards against JWTs with missing "sub" claims
// bypassing the primary authenticator check and reaching authz with empty subject.
func TestRequireAnyRole_EmptyUserSubject_Unauthorized(t *testing.T) {
	ctx := WithPrincipal(context.Background(), &Principal{
		Kind:    PrincipalUser,
		Subject: "",
		Roles:   []string{"admin"},
	})
	err := RequireAnyRole(ctx, "admin")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthUnauthorized, ecErr.Code)
}

// TestRequireSelfOrRole_EmptyUserSubject_Unauthorized verifies that a PrincipalUser
// with an empty Subject is rejected with ErrAuthUnauthorized by RequireSelfOrRole
// (G1.B authz-entry defence).
func TestRequireSelfOrRole_EmptyUserSubject_Unauthorized(t *testing.T) {
	ctx := WithPrincipal(context.Background(), &Principal{
		Kind:    PrincipalUser,
		Subject: "",
		Roles:   []string{"admin"},
	})
	err := RequireSelfOrRole(ctx, "user-42", "admin")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthUnauthorized, ecErr.Code)
}

// TestRequireSelfOrRole_UUIDNormalization verifies B4: both p.Subject and
// targetID are normalized to canonical lowercase UUID before comparison, so
// that different format variants of the same UUID are treated as equal.
//
// Existing SelfOr tests (Finding 11) continue to cover the path-param side;
// these cases cover the p.Subject side.
func TestRequireSelfOrRole_UUIDNormalization(t *testing.T) {
	tests := []struct {
		name     string
		subject  string // value placed in p.Subject
		targetID string // value passed as targetID
		wantErr  bool
	}{
		{
			// Regression: canonical subject + canonical target → Allow (unchanged).
			name:     "both canonical lowercase → Allow",
			subject:  canonicalUUID,
			targetID: canonicalUUID,
			wantErr:  false,
		},
		{
			// B4: p.Subject arrives from an external IdP in UPPERCASE; handler
			// edge already normalized path-param to canonical — must still Allow.
			name:     "UPPERCASE subject, lowercase canonical target → Allow",
			subject:  uppercaseUUID,
			targetID: canonicalUUID,
			wantErr:  false,
		},
		{
			// B4: targetID arrives in compact form (no dashes); p.Subject is
			// canonical lowercase — must still Allow.
			name:     "canonical subject, compact-no-dashes target → Allow",
			subject:  canonicalUUID,
			targetID: compactUUID,
			wantErr:  false,
		},
		{
			// PR-A45 round-4: brace-wrapped subjects (length 38) used to be
			// silently normalized by google/uuid.Parse. ParseCanonicalUUID rejects
			// them, so the raw "{...}" subject is compared verbatim against the
			// canonical target and never matches → Forbidden.
			name:     "brace-wrapped subject, canonical target → Forbidden",
			subject:  "{" + canonicalUUID + "}",
			targetID: canonicalUUID,
			wantErr:  true,
		},
		{
			// urn:uuid: prefixed target (length 45) — same rationale.
			name:     "canonical subject, urn:uuid prefixed target → Forbidden",
			subject:  canonicalUUID,
			targetID: "urn:uuid:" + canonicalUUID,
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithPrincipal(context.Background(), &Principal{
				Kind:    PrincipalUser,
				Subject: tc.subject,
				Roles:   nil,
			})
			err := RequireSelfOrRole(ctx, tc.targetID)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// withPrincipalCtx builds a context carrying a PrincipalUser with the given
// subject and roles. Used by authz_test table-driven tests.
func withPrincipalCtx(subject string, roles []string) context.Context {
	return WithPrincipal(context.Background(), &Principal{
		Kind:    PrincipalUser,
		Subject: subject,
		Roles:   append([]string(nil), roles...),
	})
}
