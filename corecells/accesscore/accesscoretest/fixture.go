package accesscoretest

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	accesscoremem "github.com/ghbvf/gocell/corecells/accesscore/mem"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/framework/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
	sessiontest "github.com/ghbvf/gocell/framework/runtime/auth/session/sessiontest"
)

// UserStatus is the public mirror of domain.UserStatus used by SeededUserView
// so external test packages do not need to import internal/domain to read
// user state. SeedUser always produces Active users; tests that need
// Locked/Suspended state must drive a service operation (Lock / Suspend)
// after seeding so the credential-event funnel runs correctly.
type UserStatus string

const (
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
	UserStatusLocked    UserStatus = "locked"
)

// SeededUser is the value-type input for AccessFixture.SeedUser. It mirrors
// the public field set of internal/domain.User that test authors need at
// seed time — all four fields are required.
//
// Active status and AuthzEpoch=1 are implied (domain.NewUser defaults).
// Non-Active state must come from a service operation invoked after seeding.
type SeededUser struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string // bcrypt-formatted; tests can use "$2a$04$dummy"
}

// SeededRole is the value-type input for AccessFixture.SeedRole. Permissions
// are not modeled here — current journeys do not seed them; add a
// SeededPermission type when needed.
type SeededRole struct {
	ID   string
	Name string
}

// SeededUserView is the value-type output returned by AccessFixture.GetUser.
// It surfaces the seed fields plus rehydrated authz/lifecycle state so test
// assertions don't need to import internal/domain.
type SeededUserView struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
	Status       UserStatus
	AuthzEpoch   int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SeededRoleView is the value-type output returned by AccessFixture.GetRole /
// AccessFixture.UserRoles.
type SeededRoleView struct {
	ID   string
	Name string
}

// DefaultFixtureTenantID is the canonical test tenant UUID used by AccessFixture
// for all tenant-scoped repo operations. Tests that need multi-tenant isolation
// should use the TenantID field on fixture calls directly.
var DefaultFixtureTenantID = func() tenant.TenantID {
	t, err := tenant.ParseTenantID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		panic(panicregister.Approved("accesscoretest-fixture-tenant",
			errcode.Assertion("accesscoretest: invalid DefaultFixtureTenantID: %v", err)))
	}
	return t
}()

// AccessFixture is the canonical in-memory test aggregate for accesscore.
// It wraps a single mem.Bundle so the four store-paired primitives
// (UserRepository, RoleRepository, SetupLock, TxRunner) all originate from
// the same underlying mem.Store — preventing the store-pairing footgun that
// caused PR #595.
//
// In addition, the fixture holds singleton session.MemStore and refresh.Store
// instances so that test-built services (e.g. via BuildIdentityManageService)
// share session/refresh state with the credential invalidator. Round-2 review
// of PR #845 added this extension: the invalidator used to build its own
// independent session/refresh stores via internal/testutil, silently
// isolating it from any sessionlogin.Service that callers might later inject
// as TokenIssuer.
//
// All tenant-scoped repo operations use DefaultFixtureTenantID unless the
// caller passes a specific tenant (#1337 PR-2).
//
// Use NewAccessFixture to construct; never embed the zero value.
type AccessFixture struct {
	bundle       accesscoremem.Bundle
	sessionStore *session.MemStore
	refreshStore refresh.Store
	clk          clock.Clock
	// TenantID scopes all repo operations. Defaults to DefaultFixtureTenantID.
	TenantID tenant.TenantID
}

// NewAccessFixture constructs an AccessFixture backed by mem.NewBundle(clk).
// session/refresh stores use the canonical test protocol that
// internal/testutil.RealSessionRepo / RealRefreshStore use so behavior
// matches existing in-package unit tests.
func NewAccessFixture(t *testing.T, clk clock.Clock) *AccessFixture {
	t.Helper()
	sess, err := session.NewMemStore(sessiontest.Protocol(), clk)
	if err != nil {
		t.Fatalf("NewAccessFixture: session.NewMemStore: %v", err)
	}
	ref, err := refreshmem.New(
		refresh.Policy{
			ReuseInterval:  time.Second,
			MaxAge:         time.Hour,
			MaxIdle:        refresh.DefaultMaxIdle,
			GraceMaxReuses: refresh.DefaultGraceMaxReuses,
		},
		clk, nil,
	)
	if err != nil {
		t.Fatalf("NewAccessFixture: refreshmem.New: %v", err)
	}
	return &AccessFixture{
		bundle:       accesscoremem.NewBundle(clk),
		sessionStore: sess,
		refreshStore: ref,
		clk:          clk,
		TenantID:     DefaultFixtureTenantID,
	}
}

// TxRunner returns the bundle-paired store-bound CellTxManager. Use this when
// wiring services that require a real atomic TxManager (e.g. identitymanage
// Create → lock-scoped read-modify-write).
func (f *AccessFixture) TxRunner() persistence.CellTxManager { return f.bundle.TxRunner() }

// SeedUser persists u via domain.NewUser → UserRepository.Create. The
// resulting user is Active with AuthzEpoch=1; tests that need non-Active
// state must drive a service operation (Lock / Suspend / Delete) after
// seeding so the credential-event funnel runs.
func (f *AccessFixture) SeedUser(ctx context.Context, u SeededUser) error {
	user, err := domain.NewUser(u.Username, u.Email, u.PasswordHash, f.clk.Now())
	if err != nil {
		return err
	}
	user.ID = u.ID
	return f.bundle.UserRepository().Create(ctx, f.TenantID, user)
}

// SeedRole persists role directly via the bundle-paired RoleRepository.
// AccessFixture does not expose the raw RoleRepository.
func (f *AccessFixture) SeedRole(ctx context.Context, r SeededRole) error {
	return f.bundle.RoleRepository().Create(ctx, f.TenantID, &domain.Role{ID: r.ID, Name: r.Name})
}

// SeedAssignment assigns the role to the user via RoleRepository.AssignToUser.
// Returns an error if either the user or role does not exist in the store.
func (f *AccessFixture) SeedAssignment(ctx context.Context, userID, roleID string) error {
	_, err := f.bundle.RoleRepository().AssignToUser(ctx, f.TenantID, userID, roleID)
	return err
}

// GetUser returns the seeded user as a SeededUserView. External tests use
// this to assert that service operations updated user state without needing
// to import internal/domain. Uses SystemRowVisibility — fixture reads are
// trusted test infrastructure with no subject-self filtering.
func (f *AccessFixture) GetUser(ctx context.Context, id string) (SeededUserView, error) {
	u, err := f.bundle.UserRepository().GetByIDInTenant(ctx, f.TenantID, tenant.SystemRowVisibility(), id)
	if err != nil {
		return SeededUserView{}, err
	}
	return userView(u), nil
}

// GetRole returns the seeded role as a SeededRoleView.
func (f *AccessFixture) GetRole(ctx context.Context, id string) (SeededRoleView, error) {
	r, err := f.bundle.RoleRepository().GetByID(ctx, f.TenantID, id)
	if err != nil {
		return SeededRoleView{}, err
	}
	return SeededRoleView{ID: r.ID, Name: r.Name}, nil
}

// UserRoles returns the roles currently assigned to the given user. Uses
// SystemRowVisibility — fixture reads are trusted test infrastructure with no
// subject-self filtering.
func (f *AccessFixture) UserRoles(ctx context.Context, userID string) ([]SeededRoleView, error) {
	roles, err := f.bundle.RoleRepository().GetByUserID(ctx, f.TenantID, tenant.SystemRowVisibility(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]SeededRoleView, 0, len(roles))
	for _, r := range roles {
		out = append(out, SeededRoleView{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

func mapStatusFromDomain(s domain.UserStatus) UserStatus {
	switch s {
	case domain.StatusSuspended:
		return UserStatusSuspended
	case domain.StatusLocked:
		return UserStatusLocked
	default:
		return UserStatusActive
	}
}

func userView(u *domain.User) SeededUserView {
	return SeededUserView{
		ID:           u.ID,
		Username:     u.Username,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Status:       mapStatusFromDomain(u.Status()),
		AuthzEpoch:   u.AuthzEpoch(),
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}
