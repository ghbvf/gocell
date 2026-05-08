//go:build integration

package main

// TestSetupOrphan_PGE2E_RecoveryResumes and TestSetupOrphan_PGE2E_NoOrphan_NormalProvision
// are provisioner-layer PG e2e tests (A26-R4).
//
// Design: HTTP assembly with PG user + role repos (cells/accesscore/postgres.WithPool)
// and in-memory session repo (WithInMemoryDefaults provides the session repo).
// No RabbitMQ or outbox relay — outbox.NoopWriter{} + eventbus.New suffice.
//
// The orphan scenario: a previous run crashed after inserting a user row into
// the users table but before writing role_assignments. The next setup POST must
// NOT create a duplicate user. The response must signal the conflict (409 / 410).
//
// ref: cells/accesscore/internal/adminprovision/provisioner.go (Ensure logic)
// ref: adapters/postgres/migrations/017_users.sql + 019_roles.sql

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/tests/testutil"
)

// orphanPGPool starts a PG container, applies all migrations, and returns
// a connected Pool with a cleanup function. The caller owns the cleanup call.
func orphanPGPool(t *testing.T) (*adapterpg.Pool, func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("orphantest"),
		tcpostgres.WithUsername("orphantest"),
		tcpostgres.WithPassword("orphantest"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container for orphan test")

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "failed to get DSN")

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err, "failed to create pool")

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err, "NewMigrator must succeed")
	require.NoError(t, migrator.Up(ctx), "Up() must apply all migrations")

	cleanup := func() {
		_ = pool.Close(ctx)
		if terr := container.Terminate(ctx); terr != nil {
			t.Logf("WARN: failed to terminate orphan container: %v", terr)
		}
	}
	return pool, cleanup
}

// bootOrphanAssembly boots an HTTP assembly with PG user + role repos
// and in-memory session repo. Returns the listener address and a shutdown func.
//
// Session is in-memory to avoid needing the PG refresh_tokens table interactions
// that require a full TxRunner / outbox wiring chain.
func bootOrphanAssembly(t *testing.T, pool *adapterpg.Pool) (addr string, shutdown func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	clk := clock.Real()
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clk)
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "orphan-test", testtime.D15min, clk,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clk, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(eventbus.WithClock(clk))
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(setupTestBootstrapUsername),
			Password: []byte(setupTestBootstrapPassword),
		},
		setupTestAllowAllLimiter{},
		nil,
	)

	// PG repos for user, session, and role (from cells/accesscore/postgres bridge).
	// Refresh store uses in-memory (mem flag from WithInMemoryDefaults) to avoid
	// PG refresh_tokens table interactions — not relevant to orphan recovery.
	// Option application order: WithInMemoryDefaults sets useInMemoryDefaults=true
	// and mem user/role; pgRepoOpts then overrides user + session + role with PG.
	// Init() sees useInMemoryDefaults=true + refreshStore=nil → creates mem refresh store.
	pgRepoOpts, err := accesspg.WithPool(pool.DB(), clk)
	require.NoError(t, err, "accesspg.WithPool must succeed")

	txMgr := adapterpg.NewTxManager(pool)

	acOpts := []accesscore.Option{
		accesscore.WithInMemoryDefaults(), // sets useInMemoryDefaults flag; overridden below
		accesscore.WithClock(clk),
		accesscore.WithOutboxDeps(eb, nw),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(txMgr),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),
	}
	acOpts = append(acOpts, pgRepoOpts...)    // override user+session+role with PG repos
	ac := accesscore.NewAccessCore(acOpts...) //archtest:allow:clock-injection:via-slice WithClock included in acOpts slice above; Go spread syntax prevents positional+spread mix

	cc := configcore.NewConfigCore(
		configcore.WithClock(clk),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(eb, nw),
		configcore.WithTxManager(noopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)
	auc := auditcore.NewAuditCore(
		auditcore.WithClock(clk),
		auditcore.WithInMemoryDefaults(),
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey([]byte("test-hmac-key-32-bytes-long!!!!!")),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	asm := assembly.New(assembly.Config{
		ID:             "orphan-test",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clk,
	})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithClock(clk),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(),
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, clk)),
		bootstrap.WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	listenAddr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, hErr := setupHTTPClient.Get(fmt.Sprintf("http://%s/healthz", listenAddr))
		if hErr != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testtime.CtxLong, testtime.MediumPoll, "HTTP server did not become ready")

	shutdown = func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		case <-time.After(testtime.SelectShutdown):
			t.Log("WARN: bootstrap did not shut down in time")
		}
	}
	return listenAddr, shutdown
}

// doSetupAdminPOST sends POST /api/v1/access/setup/admin with basic auth.
func doSetupAdminPOST(t *testing.T, baseURL, username, email, password string) *http.Response {
	t.Helper()
	payload := fmt.Sprintf(`{"username":%q,"email":%q,"password":%q}`, username, email, password)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		baseURL+"/api/v1/access/setup/admin", strings.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
	resp, err := setupHTTPClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestSetupOrphan_PGE2E_RecoveryResumes verifies the orphan recovery scenario
// through the real HTTP setup/admin endpoint backed by a PostgreSQL user + role repo.
//
// Orphan condition: a user row exists in the users table (creation_source='setup')
// but no row exists in role_assignments for that user. The setup POST should NOT
// create a second user row. The provisioner returns 409 (ErrAuthUserDuplicate)
// because the username is taken and no admin role has been assigned yet — this is
// by design: the operator must use a different username or recover manually.
//
// What we assert:
//  1. POST /setup/admin with the orphaned username returns an error (409 or similar).
//  2. The users table still has exactly one row for that username (no duplicate).
//  3. A fresh POST with a different username succeeds (201) and creates user + role.
func TestSetupOrphan_PGE2E_RecoveryResumes(t *testing.T) {
	pool, cleanup := orphanPGPool(t)
	defer cleanup()

	ctx := context.Background()

	// Seed the orphan user directly via SQL (simulates crash after user create,
	// before role assignment).
	orphanUsername := "orphan-admin"
	orphanEmail := "orphan-admin@local"
	orphanID := "00000000-0000-0000-0001-000000000001"
	stubHash := "$2a$10$stub000000000000000000000000000000000000000000000stub"
	now := time.Now().UTC().Truncate(time.Microsecond)

	_, err := pool.DB().Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, false, 'active', 'setup', $5, $5)`,
		orphanID, orphanUsername, orphanEmail, stubHash, now,
	)
	require.NoError(t, err, "seeding orphan user must succeed")

	// Verify no role_assignments row.
	var assignCount int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM role_assignments WHERE user_id = $1", orphanID,
	).Scan(&assignCount))
	assert.Equal(t, 0, assignCount, "orphan has no role assignment")

	addr, shutdown := bootOrphanAssembly(t, pool)
	defer shutdown()
	base := "http://" + addr

	// POST with orphanUsername must fail — provisioner detects true username
	// conflict (username exists, no admin role yet) and returns 409.
	resp := doSetupAdminPOST(t, base, orphanUsername, "other@local", "SecretPass!23")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	assert.NotEqual(t, http.StatusCreated, resp.StatusCode,
		"setup POST with orphaned username must NOT succeed (got body: %s)", string(raw))

	// Assert: still only ONE row in users for orphanUsername.
	var userCount int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM users WHERE username = $1", orphanUsername,
	).Scan(&userCount))
	assert.Equal(t, 1, userCount,
		"users table must have exactly one row for orphanUsername (no duplicate created)")

	// Now try a different username — this is the operator recovery path.
	resp2 := doSetupAdminPOST(t, base, "recovery-admin", "recovery@local", "SecretPass!23")
	defer resp2.Body.Close()
	// Expect 201 Created for the new username.
	var body2 struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&body2))
	assert.Equal(t, http.StatusCreated, resp2.StatusCode,
		"setup POST with a different username must succeed after orphan")
	assert.Equal(t, "recovery-admin", body2.Data.Username)

	// Assert: recovery-admin now has a role_assignments row.
	var roleCount int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM role_assignments WHERE role_id = 'admin'",
	).Scan(&roleCount))
	assert.Equal(t, 1, roleCount, "role_assignments must have exactly one admin row")

	// Assert: total users == 2 (orphan + recovery-admin).
	var totalUsers int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM users",
	).Scan(&totalUsers))
	assert.Equal(t, 2, totalUsers,
		"users table must have exactly two rows: orphan + recovery-admin")
}

// TestSetupOrphan_PGE2E_RaceLoser_Returns410 verifies the concurrent race-loser scenario:
// an admin user + role assignment already exist in PG (simulating another replica winning
// the race). The setup POST must return 410 + ERR_SETUP_ALREADY_INITIALIZED, not
// 409 + ERR_AUTH_ROLE_DUPLICATE.
//
// This covers the provisioner path where AssignToUser would receive ErrAuthRoleDuplicate
// from the DB partial unique index idx_role_assignments_single_admin. The provisioner
// folds this to OutcomeRaceSkipped which the setup service maps to 410.
func TestSetupOrphan_PGE2E_RaceLoser_Returns410(t *testing.T) {
	pool, cleanup := orphanPGPool(t)
	defer cleanup()

	ctx := context.Background()

	// Seed a complete admin: user row + role_assignments row (winner state).
	winnerID := "00000000-0000-0000-0002-000000000001"
	winnerUsername := "winner-admin"
	winnerEmail := "winner-admin@local"
	stubHash := "$2a$10$stub000000000000000000000000000000000000000000000stub"
	now := time.Now().UTC().Truncate(time.Microsecond)

	_, err := pool.DB().Exec(ctx,
		`INSERT INTO users (id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, false, 'active', 'setup', $5, $5)`,
		winnerID, winnerUsername, winnerEmail, stubHash, now,
	)
	require.NoError(t, err, "seeding winner user must succeed")

	// Ensure admin role exists.
	_, err = pool.DB().Exec(ctx,
		`INSERT INTO roles (id, name, created_at) VALUES ('admin', 'admin', $1) ON CONFLICT DO NOTHING`,
		now,
	)
	require.NoError(t, err, "seeding admin role must succeed")

	// Assign admin role to winner.
	_, err = pool.DB().Exec(ctx,
		`INSERT INTO role_assignments (user_id, role_id, assigned_at) VALUES ($1, 'admin', $2)`,
		winnerID, now,
	)
	require.NoError(t, err, "seeding role assignment must succeed")

	addr, shutdown := bootOrphanAssembly(t, pool)
	defer shutdown()
	base := "http://" + addr

	// POST /setup/admin — admin already fully provisioned (fast-path or race path).
	// Must return 410 + ERR_SETUP_ALREADY_INITIALIZED, NOT 409 + ERR_AUTH_ROLE_DUPLICATE.
	resp := doSetupAdminPOST(t, base, "loser-admin", "loser@local", "SecretPass!23")
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusGone, resp.StatusCode,
		"race-loser setup POST must return 410 Gone, got body: %s", string(raw))
	assert.Contains(t, string(raw), "ERR_SETUP_ALREADY_INITIALIZED",
		"race-loser must receive ERR_SETUP_ALREADY_INITIALIZED, not ERR_AUTH_ROLE_DUPLICATE; body: %s", string(raw))

	// Assert: users table has exactly one row (no extra row for loser-admin).
	var totalUsers int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM users",
	).Scan(&totalUsers))
	assert.Equal(t, 1, totalUsers,
		"users table must have exactly one row (winner only), got %d", totalUsers)
}

// TestSetupOrphan_PGE2E_NoOrphan_NormalProvision verifies the clean-DB path:
// setup POST succeeds (201), creates one user row + one role_assignments row,
// and subsequent setup POST returns 410 (already initialized).
func TestSetupOrphan_PGE2E_NoOrphan_NormalProvision(t *testing.T) {
	pool, cleanup := orphanPGPool(t)
	defer cleanup()

	ctx := context.Background()

	addr, shutdown := bootOrphanAssembly(t, pool)
	defer shutdown()
	base := "http://" + addr

	// Clean DB → setup POST must succeed.
	resp := doSetupAdminPOST(t, base, "first-admin", "first@local", "SecretPass!23")
	defer resp.Body.Close()
	var body struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, http.StatusCreated, resp.StatusCode,
		"clean DB: first setup POST must return 201")
	assert.Equal(t, "first-admin", body.Data.Username)
	assert.NotEmpty(t, body.Data.ID, "user ID must be set")

	// Assert: one user row.
	var userCount int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM users WHERE username = 'first-admin'",
	).Scan(&userCount))
	assert.Equal(t, 1, userCount, "users must have one row for first-admin")

	// Assert: one admin role_assignments row.
	var roleCount int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM role_assignments WHERE role_id = 'admin'",
	).Scan(&roleCount))
	assert.Equal(t, 1, roleCount, "role_assignments must have one admin row")

	// Second POST must return 410 (already initialized).
	resp2 := doSetupAdminPOST(t, base, "second-admin", "second@local", "AnotherPass!99")
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusGone, resp2.StatusCode,
		"second setup POST must return 410 Gone (already initialized)")
	raw2, _ := io.ReadAll(resp2.Body)
	assert.Contains(t, string(raw2), "ERR_SETUP_ALREADY_INITIALIZED")

	// Assert: still only one user row (no new user created on 410).
	var totalUsers int
	require.NoError(t, pool.DB().QueryRow(ctx,
		"SELECT count(*) FROM users",
	).Scan(&totalUsers))
	assert.Equal(t, 1, totalUsers, "users must have exactly one row after 410 rejection")
}
