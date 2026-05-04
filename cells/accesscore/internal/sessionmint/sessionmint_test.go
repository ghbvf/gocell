package sessionmint

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// stubIssuer implements TokenIssuer for tests. Per-intent failure injection
// lets us cover MintAccess's access-token error branch without
// constructing a broken keyset.
type stubIssuer struct {
	accessToken string
	accessErr   error
}

func (s *stubIssuer) Issue(intent auth.TokenIntent, _ string, _ auth.IssueOptions) (string, error) {
	switch intent {
	case auth.TokenIntentAccess:
		return s.accessToken, s.accessErr
	default:
		return "", errors.New("stubIssuer: unknown intent")
	}
}

// stubRoleRepo is a ports.RoleRepository test stub. Only GetByUserID is
// exercised; other methods panic to catch accidental reliance from MintAccess.
type stubRoleRepo struct {
	roles []*domain.Role
	err   error
}

func (s *stubRoleRepo) GetByID(_ context.Context, _ string) (*domain.Role, error) {
	panic("unused")
}

func (s *stubRoleRepo) GetByUserID(_ context.Context, _ string) ([]*domain.Role, error) {
	return s.roles, s.err
}
func (s *stubRoleRepo) Create(_ context.Context, _ *domain.Role) error            { panic("unused") }
func (s *stubRoleRepo) AssignToUser(_ context.Context, _, _ string) (bool, error) { panic("unused") }
func (s *stubRoleRepo) RemoveFromUser(_ context.Context, _, _ string) error       { panic("unused") }
func (s *stubRoleRepo) RemoveFromUserIfNotLast(_ context.Context, _, _ string) (bool, error) {
	panic("unused")
}
func (s *stubRoleRepo) CountByRole(_ context.Context, _ string) (int, error) { panic("unused") }
func (s *stubRoleRepo) ListByUserID(_ context.Context, _ string, _ query.ListParams) ([]*domain.Role, error) {
	panic("unused")
}

var _ ports.RoleRepository = (*stubRoleRepo)(nil)

func newTestIssuer(t *testing.T) (*auth.JWTIssuer, *auth.KeySet) {
	t.Helper()
	keySet, _, _ := auth.MustNewTestKeySet(clock.Real())
	issuer, err := auth.NewJWTIssuer(keySet, "gocell-accesscore", auth.DefaultAccessTokenTTL, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	return issuer, keySet
}

func TestMintAccess_Success(t *testing.T) {
	issuer, _ := newTestIssuer(t)
	deps := Deps{
		Issuer: issuer,
		RoleRepo: &stubRoleRepo{roles: []*domain.Role{
			{ID: "r1", Name: "admin"},
			{ID: "r2", Name: "auditor"},
		}},
		Clk: clock.Real(),
	}
	req := Request{UserID: "usr-1", SessionID: "sess-1", PasswordResetRequired: false}

	res, err := MintAccess(context.Background(), deps, req)
	require.NoError(t, err)
	assert.NotEmpty(t, res.AccessToken, "access token must be signed")
	assert.Equal(t, []string{"admin", "auditor"}, res.Roles)
	assert.WithinDuration(t, time.Now().Add(auth.DefaultAccessTokenTTL), res.ExpiresAt, time.Second)
}

func TestMintAccess_RoleFetchFailure_ReturnsErrAuthRoleFetchFailed(t *testing.T) {
	issuer, _ := newTestIssuer(t)
	repoErr := errors.New("db down")
	deps := Deps{
		Issuer:   issuer,
		RoleRepo: &stubRoleRepo{err: repoErr},
		Clk:      clock.Real(),
	}
	req := Request{UserID: "usr-1", SessionID: "sess-1"}

	res, err := MintAccess(context.Background(), deps, req)
	require.Error(t, err)
	assert.Empty(t, res.AccessToken, "no access token on failure")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleFetchFailed, ec.Code)
	assert.Equal(t, errcode.CategoryInfra, ec.Category,
		"must be CategoryInfra so IsInfraError classifies it correctly")
	assert.ErrorIs(t, err, repoErr, "original cause must be preserved in chain")
}

func TestMintAccess_EmptyRoles_StillMints(t *testing.T) {
	// A user with no roles is a valid business state; MintAccess must NOT
	// treat it as an error — only repo errors are fail-closed.
	issuer, _ := newTestIssuer(t)
	deps := Deps{
		Issuer:   issuer,
		RoleRepo: &stubRoleRepo{roles: []*domain.Role{}},
		Clk:      clock.Real(),
	}
	req := Request{UserID: "usr-noroles", SessionID: "sess-1"}

	res, err := MintAccess(context.Background(), deps, req)
	require.NoError(t, err)
	assert.NotEmpty(t, res.AccessToken)
	assert.Empty(t, res.Roles)
}

func TestMintAccess_DeterministicClock(t *testing.T) {
	issuer, _ := newTestIssuer(t)
	fixed := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	deps := Deps{
		Issuer:   issuer,
		RoleRepo: &stubRoleRepo{roles: []*domain.Role{{ID: "r1", Name: "admin"}}},
		Clk:      clockmock.New(fixed),
	}
	req := Request{UserID: "usr-1", SessionID: "sess-1"}

	res, err := MintAccess(context.Background(), deps, req)
	require.NoError(t, err)
	assert.Equal(t, fixed.Add(auth.DefaultAccessTokenTTL), res.ExpiresAt)
}

func TestMintAccess_PasswordResetFlagPropagates(t *testing.T) {
	issuer, keySet := newTestIssuer(t)
	deps := Deps{
		Issuer:   issuer,
		RoleRepo: &stubRoleRepo{roles: []*domain.Role{{ID: "r1", Name: "admin"}}},
		Clk:      clock.Real(),
	}
	req := Request{UserID: "usr-1", SessionID: "sess-1", PasswordResetRequired: true}

	res, err := MintAccess(context.Background(), deps, req)
	require.NoError(t, err)

	// Share the issuer's keySet with the verifier so signature validates.
	verifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), res.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.True(t, claims.PasswordResetRequired,
		"access token must carry password_reset_required=true when requested")
}

// TestMintAccess_AccessTokenIssueFailure asserts that when the Issuer's access-token
// Issue call fails, MintAccess wraps it with a recognizable prefix.
func TestMintAccess_AccessTokenIssueFailure(t *testing.T) {
	accessErr := errors.New("access signing broken")
	deps := Deps{
		Issuer:   &stubIssuer{accessErr: accessErr},
		RoleRepo: &stubRoleRepo{roles: []*domain.Role{{ID: "r1", Name: "admin"}}},
		Clk:      clock.Real(),
	}
	req := Request{UserID: "usr-1", SessionID: "sess-1"}

	res, err := MintAccess(context.Background(), deps, req)
	require.Error(t, err)
	assert.Empty(t, res.AccessToken)
	assert.ErrorIs(t, err, accessErr, "original cause must propagate")
	assert.Contains(t, err.Error(), "issue access token")
}
