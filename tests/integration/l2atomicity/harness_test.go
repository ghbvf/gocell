//go:build integration

package l2atomicity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	accesspg "github.com/ghbvf/gocell/cells/accesscore/postgres"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/state/cas"
	"github.com/ghbvf/gocell/tests/testutil"
)

// Bootstrap operator credentials for /api/v1/access/setup/admin Basic Auth.
const (
	bootstrapUsername = "l2-test-op"
	bootstrapPassword = "l2-test-pass-1!"
)

// Seed admin credentials provisioned by the harness via setup/admin.
// These are integration-test fixtures behind `//go:build integration` and
// are never compiled into production binaries; mirrors the same pattern
// used by cmd/corebundle/setup_pg_integration_test.go::sessionPGAdminPassword.
//
//nolint:gosec // G101 false positive: integration-test fixture, not a real credential.
const (
	adminUsername = "l2-admin"
	adminEmail    = "l2-admin@gocell.local"
	adminPassword = "L2AdminPass!99"
)

// httpClient is shared across tests. *http.Client is safe for concurrent use
// per the net/http documentation; tests do not mutate any client state and
// each test currently runs serially (no t.Parallel()), but reusing a single
// client keeps connection pooling consistent if parallelism is added later.
//
// Timeout sized for the race-detector lane under CI load: bcrypt at
// domain.BcryptCost=12 plus race instrumentation plus docker daemon
// contention on a shared CI runner can stretch individual setup/admin and
// login requests past 30s in pathological cases. 60s gives CI plenty of
// headroom while remaining well within the 15-minute race-pg-integration
// job budget.
//
// DisableKeepAlives ensures every request opens a fresh TCP connection.
// Without it, the connection pool can carry over half-closed connections
// from a previous test whose harness has already torn down its
// bootstrap+listener, surfacing as spurious "EOF" responses on the next
// test's first POST (observed under -race load when 8 harnesses are
// created and torn down serially).
var httpClient = &http.Client{
	Timeout:   testtime.D60s,
	Transport: &http.Transport{DisableKeepAlives: true},
}

// l2Harness boots a full PG-backed assembly (accesscore + configcore + auditcore)
// with three listeners (primary + internal + health-via-fallback). Provisions
// a seed admin so login can be exercised immediately.
type l2Harness struct {
	pool         *adapterpg.Pool
	base         string // primary HTTP base URL (e.g. http://127.0.0.1:1234)
	internalBase string // internal HTTP base URL (for /internal/v1/access/roles/*)
	ring         *auth.HMACKeyRing
}

// noopTxRunner executes fn directly without a real transaction.
// Used for configcore and auditcore which hold no PG state in this harness; accesscore uses the real adapterpg.TxManager.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = noopTxRunner{}

// allowAllLimiter satisfies auth.BootstrapRateLimiter without throttling.
type allowAllLimiter struct{}

func (allowAllLimiter) Allow(string) bool { return true }

// startPostgresContainer launches a PG testcontainer, registers termination via
// t.Cleanup, and returns the DSN.
func startPostgresContainer(t *testing.T) string {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("l2test"),
		tcpostgres.WithUsername("l2test"),
		tcpostgres.WithPassword("l2test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container")

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "failed to get connection string")

	t.Cleanup(func() {
		tctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
		defer cancel()
		if terr := container.Terminate(tctx); terr != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", terr)
		}
	})
	return dsn
}

// migrationsFS returns the canonical adapter migrations FS.
func migrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)
	return fsys
}

// localListener creates an ephemeral TCP listener bound to 127.0.0.1:0.
func localListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to create test listener")
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// newTestConsumerBase builds an in-memory ConsumerBase for the bootstrap.
func newTestConsumerBase(t testing.TB, clk clock.Clock) *outbox.ConsumerBase {
	t.Helper()
	cb, err := outbox.NewConsumerBase(
		idempotency.NewInMemClaimer(clk),
		outbox.ConsumerBaseConfig{},
		clk,
	)
	require.NoError(t, err)
	return cb
}

// buildAuditcoreLedgerOpts builds the auditcore ledger options for the harness.
func buildAuditcoreLedgerOpts(t testing.TB, hmacKey []byte) []auditcore.Option {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err, "audit namespace parse")
	proto, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "audit protocol construction")
	store, err := ledger.NewMemStore(proto, clock.Real())
	require.NoError(t, err, "audit mem store construction")
	return []auditcore.Option{
		auditcore.WithLedgerProtocol(proto),
		auditcore.WithLedgerStore(store),
	}
}

// newL2Harness boots the full assembly. PG-backed user/role/session/refresh
// stores; in-memory configcore/auditcore. Seeds a single admin via setup/admin.
// Each test calls this fresh rather than sharing via TestMain; race-detector +
// PG container time budget is acceptable at the current 7-test scale and
// per-test isolation simplifies state reset.
func newL2Harness(t *testing.T) *l2Harness {
	return newL2HarnessWithWriter(t, nil)
}

// newL2HarnessWithWriter allows callers to inject a custom outbox.Writer for the
// accesscore cell. Pass nil to use the default adapterpg.NewOutboxWriter.
//
// The body is composed of focused helpers (buildPGStores / buildAuthLayer /
// buildCells / runBootstrap / seedAdmin) to keep this entry point readable and
// each step independently auditable. The same shape mirrors cmd/corebundle's
// composition root (SharedDeps + BuildApp + buildAssembly + bootstrap.New).
func newL2HarnessWithWriter(t *testing.T, pgOutboxOverride outbox.Writer) *l2Harness {
	t.Helper()

	pg := buildPGStores(t)
	authDeps := buildAuthLayer(t)
	primaryLn := localListener(t)
	internalLn := localListener(t)
	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	pgOutboxWriter := pickOutboxWriter(pgOutboxOverride)

	ac, cc, auc := buildCells(t, pg, authDeps, eb, pgOutboxWriter)

	// DurabilityDemo: uses in-process eventbus instead of PG outbox relay,
	// which is sufficient to cover L2 cross-cell cascade in this integration
	// harness.
	asm := assembly.New(assembly.Config{ID: "l2-atomicity-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	runBootstrap(t, asm, primaryLn, internalLn, eb, authDeps)
	base := "http://" + primaryLn.Addr().String()
	waitForHealthz(t, base)
	seedAdmin(t, base)

	return &l2Harness{
		pool:         pg.pool,
		base:         base,
		internalBase: "http://" + internalLn.Addr().String(),
		ring:         authDeps.ring,
	}
}

// pgStores bundles the PG-backed accesscore stores plus the pool / tx manager
// that all of them share. The store wiring is exposed as a slice of
// accesscore.Option values because the underlying repository types live in
// `cells/accesscore/internal/ports`, which is not importable from this test
// package; storing options sidesteps the visibility constraint without
// leaking unexported types.
type pgStores struct {
	pool      *adapterpg.Pool
	txMgr     *adapterpg.TxManager
	storeOpts []accesscore.Option
}

// buildPGStores spins up the PG container, runs migrations, seeds the editor
// role used by cascade tests, then constructs the accesscore PG store family.
func buildPGStores(t *testing.T) *pgStores {
	t.Helper()
	ctx := context.Background()
	dsn := startPostgresContainer(t)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(ctx) })

	migrator, err := adapterpg.NewMigrator(pool, migrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))
	require.NoError(t, adapterpg.VerifyExpectedShape(ctx, pool))

	// Migration 019 creates the `roles` table but only seeds the admin role
	// (via the adminprovision setup flow). RBAC tests assigning non-admin
	// roles need the role row to exist or AssignToUser fails with FK
	// violation → ErrAuthRoleNotFound. Seed an "editor" role so cascade tests
	// have a non-admin role to assign / revoke.
	_, err = pool.DB().Exec(ctx,
		`INSERT INTO roles (id, name) VALUES ('editor', 'editor') ON CONFLICT (id) DO NOTHING`)
	require.NoError(t, err, "seed editor role")

	txMgr := adapterpg.NewTxManager(pool)

	pgDeps, err := accesspg.NewDeps(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)
	userRepo, err := accesspg.NewUserRepository(pgDeps)
	require.NoError(t, err)
	roleRepo, err := accesspg.NewRoleRepository(pgDeps)
	require.NoError(t, err)
	setupLock, err := accesspg.NewSetupLock(pgDeps)
	require.NoError(t, err)

	sessionProto := session.MustNewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	sessionStore, err := adapterpg.NewSessionStore(pool.DB(), txMgr, sessionProto, clock.Real())
	require.NoError(t, err)
	refreshStore, err := adapterpg.NewRefreshStore(pool.DB(), txMgr, accesscore.DefaultRefreshPolicy(), clock.Real(), rand.Reader)
	require.NoError(t, err)

	return &pgStores{
		pool:  pool,
		txMgr: txMgr,
		storeOpts: []accesscore.Option{
			accesscore.WithUserRepository(userRepo),
			accesscore.WithRoleRepository(roleRepo),
			accesscore.WithSessionStore(sessionStore),
			accesscore.WithRefreshStore(refreshStore),
			accesscore.WithSetupLock(setupLock),
		},
	}
}

// authLayer bundles the auth-side dependencies wired into both listeners and
// the accesscore cell.
type authLayer struct {
	ring        *auth.HMACKeyRing
	nonceStore  auth.NonceStore
	jwtIssuer   *auth.JWTIssuer
	jwtVerifier *auth.JWTVerifier
	bootstrapMW func(http.Handler) http.Handler
}

// buildAuthLayer constructs the internal-listener HMAC ring + nonce store, the
// JWT issuer/verifier pair, and the bootstrap (setup/admin) middleware. All
// values are test-only; the HMAC key + cursor keys are registered in
// cmd/corebundle/demo_keys.wellKnownDemoKeys so rejectDemoKey blocks them in
// real-mode startup.
func buildAuthLayer(t *testing.T) *authLayer {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("l2-test-secret-32-bytes-padding!!"), nil)
	require.NoError(t, err)
	nonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(bootstrapUsername),
			Password: []byte(bootstrapPassword),
		},
		allowAllLimiter{},
		nil,
	)

	return &authLayer{
		ring:        ring,
		nonceStore:  nonceStore,
		jwtIssuer:   jwtIssuer,
		jwtVerifier: jwtVerifier,
		bootstrapMW: bootstrapMW,
	}
}

// pickOutboxWriter returns the caller-supplied override or the default durable
// PG outbox writer.
func pickOutboxWriter(override outbox.Writer) outbox.Writer {
	if override != nil {
		return override
	}
	return adapterpg.NewOutboxWriter(clock.Real())
}

// buildCells constructs accesscore + configcore + auditcore. Only accesscore
// holds PG state in this harness; configcore + auditcore stay in-memory.
func buildCells(
	t *testing.T,
	pg *pgStores,
	a *authLayer,
	eb *eventbus.InMemoryEventBus,
	pgOutboxWriter outbox.Writer,
) (*accesscore.AccessCore, *configcore.ConfigCore, *auditcore.AuditCore) {
	t.Helper()

	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("l2-audit-cursor-key-32-bytes!!!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("l2-config-cursor-key-32-bytes!!!"))
	require.NoError(t, err)

	ac := accesscore.NewAccessCore(append(pg.storeOpts,
		accesscore.WithClock(clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(pgOutboxWriter)),
		accesscore.WithJWTIssuer(a.jwtIssuer),
		accesscore.WithJWTVerifier(a.jwtVerifier),
		accesscore.WithTxManager(persistence.WrapForCell(pg.txMgr)),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(a.bootstrapMW),
		accesscore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))),
	)...) //archtest:allow:clock-injection:via-slice WithClock spread via append; no positional arg
	cc := configcore.NewConfigCore(
		configcore.WithClock(clock.Real()),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		configcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
		configcore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(configcore.VersionField))),
	)
	auditHMACKey := []byte("l2-test-hmac-key-32-bytes-pad!!!")
	//archtest:allow:clock-injection:via-slice WithClock in first slice arg
	auc := auditcore.NewAuditCore(append([]auditcore.Option{
		auditcore.WithClock(clock.Real()),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		auditcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	}, buildAuditcoreLedgerOpts(t, auditHMACKey)...)...)

	return ac, cc, auc
}

// runBootstrap launches bootstrap.App on the supplied listeners and registers
// the LIFO cleanup that drains it gracefully.
func runBootstrap(
	t *testing.T,
	asm *assembly.CoreAssembly,
	primaryLn, internalLn net.Listener,
	eb *eventbus.InMemoryEventBus,
	a *authLayer,
) {
	t.Helper()
	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, primaryLn.Addr().String(),
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(primaryLn)),
		bootstrap.WithListener(cell.InternalListener, internalLn.Addr().String(),
			[]cell.ListenerAuth{cell.MustNewAuthServiceToken(a.nonceStore, a.ring)},
			bootstrap.WithListenerNet(internalLn)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newTestConsumerBase(t, clock.Real())),
		// Race-detector overhead inflates pending request completion. The
		// shutdown timeout must be ≥ httpClient.Timeout so the last in-flight
		// request can finish before forced close. 20s gives the longest
		// observed (~17s) request a safety margin without bloating CI time.
		bootstrap.WithShutdownTimeout(testtime.D20s),
	)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		// Cleanup window must exceed bootstrap WithShutdownTimeout so we
		// observe the graceful-drain outcome (success or its error) rather
		// than time out the harness before bootstrap reports.
		case <-time.After(testtime.D30s):
			t.Fatal("bootstrap did not shut down in time")
		}
	})
}

// waitForHealthz polls the primary listener until both /healthz and the
// accesscore setup-status route return 200. Probing the setup-status route
// (not just /healthz) ensures the bootstrap has completed phase5 route
// mount + FinalizeAuth, so the subsequent setup/admin POST is guaranteed
// to land on a wired handler rather than racing against mux finalization
// (race-detector + concurrent load occasionally surfaced "EOF" responses
// when only /healthz was probed).
func waitForHealthz(t *testing.T, base string) {
	t.Helper()
	require.Eventually(t, func() bool {
		if !httpGetOK(base + "/healthz") {
			return false
		}
		return httpGetOK(base + "/api/v1/access/setup/status")
	}, testtime.EventuallyLong, testtime.MediumPoll, "HTTP server did not become ready")
}

// httpGetOK is a tiny helper that returns true iff GET returns 200.
func httpGetOK(url string) bool {
	resp, err := httpClient.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// seedAdmin provisions the initial admin via POST /api/v1/access/setup/admin so
// login is immediately available to tests.
//
// The single eligible-for-retry failure mode is io.EOF: under -race + heavy
// Docker contention on slow machines the bcrypt-bound handler can complete
// server-side (200 logged) while the client's TCP socket has already
// surfaced a transport-level EOF. Retry exactly once with a fresh
// connection so we don't hide a real failure but tolerate the rare
// transport flake. Any other error type fails fast.
func seedAdmin(t *testing.T, base string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": adminUsername,
		"email":    adminEmail,
		"password": adminPassword,
	})
	postBody := func() *bytes.Reader { return bytes.NewReader(body) }

	resp, err := postSetupAdmin(base, postBody())
	if errors.Is(err, io.EOF) {
		t.Logf("seedAdmin: transient io.EOF on first attempt; retrying once")
		resp, err = postSetupAdmin(base, postBody())
	}
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "harness: admin provisioning must succeed")
}

func postSetupAdmin(base string, body *bytes.Reader) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/setup/admin", body)
	req.SetBasicAuth(bootstrapUsername, bootstrapPassword)
	req.Header.Set("Content-Type", "application/json")
	return httpClient.Do(req)
}
