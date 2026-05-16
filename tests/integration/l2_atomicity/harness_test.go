//go:build integration

package l2_atomicity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
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
const (
	adminUsername = "l2-admin"
	adminEmail    = "l2-admin@gocell.local"
	adminPassword = "L2AdminPass!99"
)

// HTTP client timeout sized for the race-detector lane under CI load:
// bcrypt at domain.BcryptCost=12 plus race instrumentation plus docker
// daemon contention on a shared CI runner can stretch individual setup/admin
// and login requests past 30s in pathological cases (observed up to ~3s
// per request × cascading retries). 60s gives CI plenty of headroom while
// remaining well within the 15-minute race-pg-integration job budget.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// l2Harness boots a full PG-backed assembly (accesscore + configcore + auditcore)
// with three listeners (primary + internal + health-via-fallback). Provisions
// a seed admin so login can be exercised immediately.
type l2Harness struct {
	pool         *adapterpg.Pool
	base         string // primary HTTP base URL (e.g. http://127.0.0.1:1234)
	internalBase string // internal HTTP base URL (for /internal/v1/access/roles/*)
	ring         *auth.HMACKeyRing
}

// noopTxRunner executes fn directly without a real transaction; used for
// configcore + auditcore which do not exercise PG persistence in this harness.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = noopTxRunner{}

// allowAllLimiter satisfies auth.BootstrapRateLimiter without throttling.
type allowAllLimiter struct{}

func (allowAllLimiter) Allow(string) bool { return true }

// startPostgresContainer launches a PG testcontainer and returns the DSN and cleanup.
func startPostgresContainer(t *testing.T) (string, func()) {
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

	cleanup := func() {
		if terr := container.Terminate(ctx); terr != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", terr)
		}
	}
	return dsn, cleanup
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
func newL2Harness(t *testing.T) *l2Harness {
	return newL2HarnessWithWriter(t, nil)
}

// newL2HarnessWithWriter allows callers to inject a custom outbox.Writer for the
// accesscore cell. Pass nil to use the default adapterpg.NewOutboxWriter.
func newL2HarnessWithWriter(t *testing.T, pgOutboxOverride outbox.Writer) *l2Harness {
	t.Helper()

	dsn, dsnCleanup := startPostgresContainer(t)
	t.Cleanup(dsnCleanup)

	ctx := context.Background()

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
	pgUserRepo, err := accesspg.NewUserRepository(pgDeps)
	require.NoError(t, err)
	pgRoleRepo, err := accesspg.NewRoleRepository(pgDeps)
	require.NoError(t, err)
	pgSetupLock, err := accesspg.NewSetupLock(pgDeps)
	require.NoError(t, err)

	sessionProto := session.MustNewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	pgSessionStore, err := adapterpg.NewSessionStore(pool.DB(), txMgr, sessionProto, clock.Real())
	require.NoError(t, err)
	pgRefreshStore, err := adapterpg.NewRefreshStore(pool.DB(), txMgr, accesscore.DefaultRefreshPolicy(), clock.Real(), rand.Reader)
	require.NoError(t, err)

	primaryLn := localListener(t)
	internalLn := localListener(t)

	internalRing, err := auth.NewHMACKeyRing([]byte("l2-test-secret-32-bytes-padding!!"), nil)
	require.NoError(t, err)
	internalNonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	var pgOutboxWriter outbox.Writer
	if pgOutboxOverride != nil {
		pgOutboxWriter = pgOutboxOverride
	} else {
		pgOutboxWriter = adapterpg.NewOutboxWriter(clock.Real())
	}
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("l2-audit-cursor-key-32-bytes!!!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("l2-config-cursor-key-32-bytes!!!"))
	require.NoError(t, err)

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(bootstrapUsername),
			Password: []byte(bootstrapPassword),
		},
		allowAllLimiter{},
		nil,
	)

	ac := accesscore.NewAccessCore(
		accesscore.WithClock(clock.Real()),
		accesscore.WithUserRepository(pgUserRepo),
		accesscore.WithRoleRepository(pgRoleRepo),
		accesscore.WithSessionStore(pgSessionStore),
		accesscore.WithRefreshStore(pgRefreshStore),
		accesscore.WithSetupLock(pgSetupLock),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(pgOutboxWriter)),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(persistence.WrapForCell(txMgr)),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),
		accesscore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))),
	)
	cc := configcore.NewConfigCore(
		configcore.WithClock(clock.Real()),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		configcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
		configcore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(configcore.VersionField))),
	)
	auc := auditcore.NewAuditCore(append([]auditcore.Option{
		auditcore.WithClock(clock.Real()),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		auditcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	}, buildAuditcoreLedgerOpts(t, []byte("l2-test-hmac-key-32-bytes-pad!!!"))...)...) //archtest:allow:clock-injection:via-slice WithClock is in the first slice arg passed to append; spread prevents direct positional arg

	asm := assembly.New(assembly.Config{ID: "l2-atomicity-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, primaryLn.Addr().String(),
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(primaryLn)),
		bootstrap.WithListener(cell.InternalListener, internalLn.Addr().String(),
			[]cell.ListenerAuth{cell.MustNewAuthServiceToken(internalNonceStore, internalRing)},
			bootstrap.WithListenerNet(internalLn)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newTestConsumerBase(t, clock.Real())),
		// Race-detector overhead inflates pending request completion. The
		// shutdown timeout must be ≥ httpClient.Timeout so the last in-flight
		// request can finish before forced close. 20s gives the longest
		// observed (~17s) request a safety margin without bloating CI time.
		bootstrap.WithShutdownTimeout(20*time.Second),
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
		case <-time.After(30 * time.Second):
			t.Fatal("bootstrap did not shut down in time")
		}
	})

	addr := primaryLn.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := httpClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "HTTP server did not become ready")

	base := "http://" + addr

	// Provision the initial admin so login is immediately available.
	adminBody, _ := json.Marshal(map[string]string{
		"username": adminUsername,
		"email":    adminEmail,
		"password": adminPassword,
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/setup/admin", bytes.NewReader(adminBody))
	req.SetBasicAuth(bootstrapUsername, bootstrapPassword)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "harness: admin provisioning must succeed")

	return &l2Harness{
		pool:         pool,
		base:         base,
		internalBase: "http://" + internalLn.Addr().String(),
		ring:         internalRing,
	}
}
