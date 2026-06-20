//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore"
	"github.com/ghbvf/gocell/corecells/accesscore/accesscoretest"
	accessmem "github.com/ghbvf/gocell/corecells/accesscore/mem"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/keystest"
	"github.com/ghbvf/gocell/framework/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/framework/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
	"github.com/ghbvf/gocell/framework/runtime/auth/session/storetest"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

const (
	accountlockoutJWTTTL        = 15 * time.Minute
	accountlockoutRefreshMaxAge = time.Hour
)

// TestJAccountlockoutAutoLockCycle implements the Docker-free VERIFY-06 anchor
// for journeys/J-accountlockout.yaml checkRef
// journey.J-accountlockout.auto-lock-cycle. The full threshold -> event ->
// admin-unlock wire proof remains in tests/integration/l2atomicity under the
// same test name, but that package may skip when Docker is unavailable.
//
// This test locks the public composition seam that must exist before that e2e
// can work: accesscore mem-mode Init must build sessionlogin plus identitymanage
// with accountlockout injected, and route registration must mount login,
// admin-lock, and admin-unlock contracts through generated handlers.
func TestJAccountlockoutAutoLockCycle(t *testing.T) {
	t.Parallel()

	clk := clock.Real()
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), clk)
	require.NoError(t, err)

	refreshStore, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         accountlockoutRefreshMaxAge,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	require.NoError(t, err)

	ks, _, _ := keystest.MustNewKeySet(clk)
	issuer, err := auth.NewJWTIssuer(ks, "gocell-accountlockout", accountlockoutJWTTTL, clk,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(ks, clk, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	casProtocol, err := cas.NewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))
	require.NoError(t, err)

	ac := accesscore.NewAccessCore(
		clk,
		accesscore.WithMemBundle(accessmem.NewBundle(clk)),
		accesscore.WithSessionStore(store),
		accesscore.WithRefreshStore(refreshStore),
		accesscore.WithJWTIssuer(issuer),
		accesscore.WithJWTVerifier(verifier),
		accesscore.WithEmitter(outbox.DemoCellEmitter()),
		accesscore.WithCASProtocol(casProtocol),
		accesscore.WithBootstrapAuth(func(next http.Handler) http.Handler { return next }),
		accesscoretest.MinCostPasswordHasherOption(),
	)

	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, ac.Init(context.Background(), rec))

	mux := newAccountlockoutRouteRecorder()
	for _, group := range rec.Snapshot().RouteGroups {
		require.NoError(t, group.Register(mux.scoped(group.Prefix)), "route group %s", group.Prefix)
	}

	assert.Contains(t, mux.contractIDs(), "http.auth.login.v1",
		"auto-lock starts at the login contract, where failed attempts are recorded")
	assert.Contains(t, mux.contractIDs(), "http.auth.user.lock.v1",
		"accountlockout must keep the admin lock contract mounted for locked-state parity")
	assert.Contains(t, mux.contractIDs(), "http.auth.user.unlock.v1",
		"admin unlock is the recovery leg asserted by J-accountlockout")
}

type accountlockoutRouteRecorder struct {
	prefix    string
	contracts *[]string
}

func newAccountlockoutRouteRecorder() *accountlockoutRouteRecorder {
	contracts := []string{}
	return &accountlockoutRouteRecorder{contracts: &contracts}
}

func (r *accountlockoutRouteRecorder) scoped(prefix string) *accountlockoutRouteRecorder {
	return &accountlockoutRouteRecorder{prefix: prefix, contracts: r.contracts}
}

func (r *accountlockoutRouteRecorder) contractIDs() []string {
	return append([]string(nil), (*r.contracts)...)
}

func (r *accountlockoutRouteRecorder) Handle(string, http.Handler) {}

func (r *accountlockoutRouteRecorder) Route(pattern string, fn func(cell.RouteMux)) {
	fn(r.scoped(r.prefix + pattern))
}

func (r *accountlockoutRouteRecorder) Mount(string, http.Handler) {}

func (r *accountlockoutRouteRecorder) Group(fn func(cell.RouteMux)) {
	fn(r)
}

func (r *accountlockoutRouteRecorder) With(...func(http.Handler) http.Handler) cell.RouteMux {
	return r
}

func (r *accountlockoutRouteRecorder) Prefix() string {
	return r.prefix
}

func (r *accountlockoutRouteRecorder) DeclareHTTPContract(spec contractspec.ContractSpec) error {
	*r.contracts = append(*r.contracts, spec.ID)
	return nil
}

func (r *accountlockoutRouteRecorder) DeclareAuthMeta(cell.AuthRouteMeta) error {
	return nil
}
