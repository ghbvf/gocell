//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	accesspg "github.com/ghbvf/gocell/cells/accesscore/postgres"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/authtest"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

type setupPGHarness struct {
	pool *adapterpg.Pool
	base string
}

type failingSetupOutboxWriter struct {
	err error
}

func (w failingSetupOutboxWriter) Write(context.Context, outbox.Entry) error {
	return w.err
}

var _ outbox.Writer = failingSetupOutboxWriter{}

func newSetupPGHarness(t *testing.T, pgOutboxWriter outbox.Writer) *setupPGHarness {
	t.Helper()
	require.NotNil(t, pgOutboxWriter)

	dsn, dsnCleanup := setupPostgresForMain(t)
	t.Cleanup(dsnCleanup)

	ctx := context.Background()

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(ctx) })

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))

	// Schema-shape gate: confirms migrations 017-019 produced the expected
	// shape (S3+S5 deployment playbook). Failing here means a partial
	// migration / drift; bootstrap would refuse to start.
	require.NoError(t, adapterpg.VerifyExpectedShape(ctx, pool))

	txMgr := adapterpg.NewTxManager(pool)

	pgDeps, err := accesspg.NewDeps(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)
	pgUserRepo, err := accesspg.NewUserRepository(pgDeps)
	require.NoError(t, err)
	pgRoleRepo, err := accesspg.NewRoleRepository(pgDeps)
	require.NoError(t, err)
	pgSetupLock, err := accesspg.NewSetupLock(pgDeps)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	privKey, pubKey := authtest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
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
	// buildAccessCoreMemOptions provides session + refresh mem stores and a mem
	// user/role repo pair; the subsequent WithUserRepository / WithRoleRepository
	// calls override only those two with the PG-backed implementations.
	// Session/refresh stores remain in-memory for this harness (S3+S5 scope;
	// PG session/refresh wiring is exercised separately in the S4a PG sub-tests below).
	ac := accesscore.NewAccessCore(append(buildAccessCoreMemOptions(t, clock.Real()),
		accesscore.WithClock(clock.Real()),
		accesscore.WithUserRepository(pgUserRepo),
		accesscore.WithRoleRepository(pgRoleRepo),
		accesscore.WithSetupLock(pgSetupLock),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(pgOutboxWriter)),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(persistence.WrapForCell(txMgr)),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),

		accesscore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))),
	)...) //archtest:allow:clock-injection:via-slice buildAccessCoreMemOptions + WithClock prepended; spread prevents direct positional arg
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
	}, auditcoreLedgerOpts(t, []byte("test-hmac-key-32-bytes-long!!!!!"))...)...) //archtest:allow:clock-injection:via-slice WithClock is in the first slice arg passed to append; spread prevents direct positional arg

	asm := assembly.New(assembly.Config{ID: "setup-pg-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(),
			[]cell.ListenerAuth{celltest.MustAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, clock.Real())),
		bootstrap.WithShutdownTimeout(testtime.D2s),
	)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		case <-time.After(testtime.SelectShutdown):
			t.Fatal("bootstrap did not shut down in time")
		}
	})

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := setupHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "HTTP server did not become ready")

	return &setupPGHarness{pool: pool, base: "http://" + addr}
}

// TestSetupEndpoints_FirstRunFlow_PG mirrors TestSetupEndpoints_FirstRunFlow
// but with a real PostgreSQL container backing the accesscore user / role
// repositories. Closes A26-R4 (SETUP-ORPHAN-E2E-01) and validates the
// cmd/corebundle PG wiring path landed in S3+S5 (access_module.go).
//
// Steps mirror the mem variant exactly:
//
//  1. POST /api/v1/access/setup/admin (Basic Auth) → 201 + user persisted in PG
//  2. POST /api/v1/access/setup/admin (again)      → 410 ERR_SETUP_ALREADY_INITIALIZED
//  3. GET  /api/v1/access/setup/status             → {hasAdmin:true} (PG row counted)
//
// Diff vs. mem variant: WithInMemoryDefaults stays (initialises
// SessionRepository in mem; PG session.Store is library-only in S3+S5 per
// the plan, S4 wires it into the cell), but UserRepository / RoleRepository
// are overridden via WithUserRepository / WithRoleRepository to point at
// the PG implementations. TxManager is a real adapterpg.NewTxManager(pool)
// so setup.Service's L2 atomicity (user write + outbox emit) actually
// commits a transaction.
func TestSetupEndpoints_FirstRunFlow_PG(t *testing.T) {
	ctx := context.Background()
	h := newSetupPGHarness(t, adapterpg.NewOutboxWriter(clock.Real()))
	body := `{"username":"pg-admin","email":"pg-admin@example.com","password":"PgAdminPass!23"}`

	// 1. Fresh PG: POST → 201, admin row persisted.
	t.Run("create_admin_returns_201", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, h.base+"/api/v1/access/setup/admin", strings.NewReader(body))
		req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
		req.Header.Set("Content-Type", "application/json")
		resp, err := setupHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusCreated, resp.StatusCode)

		var eventType, status string
		var payload []byte
		err = h.pool.DB().QueryRow(ctx, `
SELECT event_type, payload, status
FROM outbox_entries
WHERE event_type = $1`,
			"event.user.created.v1",
		).Scan(&eventType, &payload, &status)
		require.NoError(t, err, "setup admin must commit a durable user.created outbox row")
		assert.Equal(t, "event.user.created.v1", eventType)
		assert.Equal(t, "pending", status)
		var eventPayload struct {
			UserID   string `json:"userId"`
			Username string `json:"username"`
			ActorID  string `json:"actorId"`
		}
		require.NoError(t, json.Unmarshal(payload, &eventPayload))
		assert.NotEmpty(t, eventPayload.UserID)
		assert.Equal(t, "pg-admin", eventPayload.Username)
		assert.Equal(t, "system", eventPayload.ActorID)
	})

	// 2. Retry: POST → 410, ErrSetupAlreadyInitialized (admin row counted in PG).
	t.Run("create_admin_retry_returns_410", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, h.base+"/api/v1/access/setup/admin", strings.NewReader(body))
		req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
		req.Header.Set("Content-Type", "application/json")
		resp, err := setupHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusGone, resp.StatusCode)
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
		assert.Equal(t, "ERR_SETUP_ALREADY_INITIALIZED", envelope.Error.Code)
	})

	// 3. Status: hasAdmin=true (count(admin) > 0 from PG).
	t.Run("status_after_returns_true", func(t *testing.T) {
		resp, err := setupHTTPClient.Get(h.base + "/api/v1/access/setup/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var status struct {
			Data struct {
				HasAdmin bool `json:"hasAdmin"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
		assert.True(t, status.Data.HasAdmin, "PG-backed RoleRepository.CountByRole(admin) must report 1+ admins")
	})
}

func TestSetupEndpoints_FirstRunFlow_PG_OutboxFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	h := newSetupPGHarness(t, failingSetupOutboxWriter{err: errors.New("injected setup outbox failure")})
	body := `{"username":"pg-admin","email":"pg-admin@example.com","password":"PgAdminPass!23"}`

	req, _ := http.NewRequest(http.MethodPost, h.base+"/api/v1/access/setup/admin", strings.NewReader(body))
	req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
	req.Header.Set("Content-Type", "application/json")
	resp, err := setupHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	var userCount int
	err = h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM users WHERE username = $1`, "pg-admin").Scan(&userCount)
	require.NoError(t, err)
	assert.Equal(t, 0, userCount, "failed outbox write must roll back setup user row")

	var roleAssignmentCount int
	err = h.pool.DB().QueryRow(ctx, `SELECT count(*) FROM role_assignments`).Scan(&roleAssignmentCount)
	require.NoError(t, err)
	assert.Equal(t, 0, roleAssignmentCount, "failed outbox write must roll back admin role assignment")

	var outboxCount int
	err = h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM outbox_entries WHERE event_type = $1`,
		"event.user.created.v1",
	).Scan(&outboxCount)
	require.NoError(t, err)
	assert.Equal(t, 0, outboxCount, "failed setup transaction must not commit an outbox row")
}

// sessionPGHarness extends setupPGHarness with PG session + refresh stores
// (S4a wiring). All four repositories are PG-backed; the harness provisions
// a bootstrap admin so login/refresh/logout paths can be exercised.
//
// internalBase and ring are populated by newSessionPGHarnessWithWriter so that
// tests making /internal/v1/* calls can generate valid service tokens.
type sessionPGHarness struct {
	pool         *adapterpg.Pool
	base         string
	internalBase string
	ring         *auth.HMACKeyRing
}

// newSessionPGHarness boots a full PG-backed assembly: user/role/session/refresh
// stores all backed by Postgres. Provisions a "session-admin" user via setup/admin
// so the caller can immediately call login without additional setup.
func newSessionPGHarness(t *testing.T) *sessionPGHarness {
	return newSessionPGHarnessWithWriter(t, nil)
}

// newSessionPGHarnessWithWriter is like newSessionPGHarness but allows callers to
// inject a custom outbox.Writer for the accesscore cell. Pass nil to use the
// default adapterpg.NewOutboxWriter (durable PG writer).
func newSessionPGHarnessWithWriter(t *testing.T, pgOutboxOverride outbox.Writer) *sessionPGHarness {
	t.Helper()

	dsn, dsnCleanup := setupPostgresForMain(t)
	t.Cleanup(dsnCleanup)

	ctx := context.Background()

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(ctx) })

	migrator, err := adapterpg.NewMigrator(pool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx))
	require.NoError(t, adapterpg.VerifyExpectedShape(ctx, pool))

	// Migration 019 creates the `roles` table but only seeds the "admin" role
	// (via the adminprovision setup flow). RBAC tests assigning non-admin
	// roles need the role row to exist or AssignToUser fails with FK
	// violation → ErrAuthRoleNotFound (404). Seed "editor" so S4b RBAC tests
	// have a non-admin role to assign and revoke.
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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// Internal listener with its own ring so tests can generate service tokens.
	internalLn := newCorebundleLocalListener(t)
	internalRing, err := auth.NewHMACKeyRing([]byte("test-secret-32-bytes-long-padding!"), nil)
	require.NoError(t, err)
	internalNonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)
	internalGuardForHarness := &internalGuard{
		ring:       internalRing,
		nonceStore: internalNonceStore,
		mw:         func(h http.Handler) http.Handler { return h },
	}

	privKey, pubKey := authtest.MustGenerateKeyPair()
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
	}, auditcoreLedgerOpts(t, []byte("test-hmac-key-32-bytes-long!!!!!"))...)...) //archtest:allow:clock-injection:via-slice WithClock is in the first slice arg passed to append; spread prevents direct positional arg

	asm := assembly.New(assembly.Config{ID: "session-pg-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(),
			[]cell.ListenerAuth{celltest.MustAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(ln)),
		bootstrap.WithListener(cell.InternalListener, internalLn.Addr().String(),
			buildInternalAuthChain(internalGuardForHarness),
			bootstrap.WithListenerNet(internalLn)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, clock.Real())),
		bootstrap.WithShutdownTimeout(testtime.D2s),
	)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		case <-time.After(testtime.SelectShutdown):
			t.Fatal("bootstrap did not shut down in time")
		}
	})

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := setupHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "HTTP server did not become ready")

	base := "http://" + addr

	// Provision the first admin so login can proceed immediately.
	adminBody, _ := json.Marshal(map[string]string{
		"username": sessionPGAdminUsername,
		"email":    "session-admin@pg-test.local",
		"password": sessionPGAdminPassword,
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/setup/admin", bytes.NewReader(adminBody))
	req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
	req.Header.Set("Content-Type", "application/json")
	resp, err := setupHTTPClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "sessionPGHarness: admin provisioning must succeed")

	return &sessionPGHarness{
		pool:         pool,
		base:         base,
		internalBase: "http://" + internalLn.Addr().String(),
		ring:         internalRing,
	}
}

// sessionPGLogin calls POST /api/v1/access/sessions/login and returns the full
// response body fields needed by the S4a PG sub-tests.
func sessionPGLogin(t *testing.T, base, username, password string) (accessToken, refreshToken, sessionID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := setupHTTPClient.Post(base+"/api/v1/access/sessions/login", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "login must return 201")
	var result struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			SessionId    string `json:"sessionId"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotEmpty(t, result.Data.AccessToken, "accessToken must be present")
	require.NotEmpty(t, result.Data.RefreshToken, "refreshToken must be present")
	require.NotEmpty(t, result.Data.SessionId, "sessionId must be present")
	return result.Data.AccessToken, result.Data.RefreshToken, result.Data.SessionId
}

// sessionPGRefresh calls POST /api/v1/access/sessions/refresh and returns the
// new access token, refresh token, and session ID.
func sessionPGRefresh(t *testing.T, base, refreshToken string) (newAccessToken, newRefreshToken, newSessionID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"refreshToken": refreshToken})
	resp, err := setupHTTPClient.Post(base+"/api/v1/access/sessions/refresh", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	// contracts/http/auth/refresh/v1/contract.yaml declares successStatus: 200
	// (refresh = same authorization grant, no new resource created).
	require.Equal(t, http.StatusOK, resp.StatusCode, "refresh must return 200")
	var result struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			SessionId    string `json:"sessionId"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotEmpty(t, result.Data.AccessToken, "new accessToken must be present")
	require.NotEmpty(t, result.Data.RefreshToken, "new refreshToken must be present")
	require.NotEmpty(t, result.Data.SessionId, "new sessionId must be present")
	return result.Data.AccessToken, result.Data.RefreshToken, result.Data.SessionId
}

// sessionPGLogout calls DELETE /api/v1/access/sessions/{sessionID}.
func sessionPGLogout(t *testing.T, base, accessToken, sessionID string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, base+"/api/v1/access/sessions/"+sessionID, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := setupHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "logout must return 204")
}

const (
	sessionPGAdminUsername = "session-admin"
	sessionPGAdminPassword = "SessionAdminPass!99"
)

// TestSessionLogin_PGGoldenPath verifies that Login durably persists a session
// row and a refresh_token row in Postgres (S4a session/refresh PG wiring).
//
// Assertions:
//   - sessions table has exactly 1 row for the subject
//   - revoked_at IS NULL (session is active)
//   - jti is non-empty
//   - refresh_tokens table has exactly 1 live row for that session_id
//
// Note: authz_epoch_at_issue column was dropped in S4b migration 025; epoch
// ordering is now enforced via the JWT claim layer.
func TestSessionLogin_PGGoldenPath(t *testing.T) {
	ctx := context.Background()
	h := newSessionPGHarness(t)

	_, _, sessionID := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)

	// Verify sessions row persisted correctly.
	var jti string
	var revokedAt *time.Time
	err := h.pool.DB().QueryRow(ctx,
		`SELECT jti, revoked_at FROM sessions WHERE id = $1`,
		sessionID,
	).Scan(&jti, &revokedAt)
	require.NoError(t, err, "session row must exist after login")
	assert.NotEmpty(t, jti, "jti must be non-empty in sessions row")
	assert.Nil(t, revokedAt, "revoked_at must be NULL for an active session")

	// Verify refresh_token row exists for this session.
	var rtCount int
	err = h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM refresh_tokens WHERE session_id = $1 AND rotated_at IS NULL AND revoked_at IS NULL`,
		sessionID,
	).Scan(&rtCount)
	require.NoError(t, err)
	assert.Equal(t, 1, rtCount, "exactly 1 live refresh_token row must exist after login")
}

// TestSessionLogin_OutboxFailureRollsBackPGRows verifies that when the outbox
// Write call fails inside the login transaction, the entire transaction rolls
// back and neither a sessions row nor a refresh_tokens row is committed to PG.
//
// Design: the harness injects a one-shot failing outbox.Writer. The admin
// provisioning step (setup/admin) uses the same failing writer, so it will also
// fail. To work around this, we use the failingSetupOutboxWriter only for the
// session login step: we boot two harnesses — the first with the real PG writer
// to provision the admin, the second with the failing writer to test login
// rollback. Both harnesses share separate PG containers (newSessionPGHarnessWithWriter
// calls setupPostgresForMain internally).
//
// Note: admin provisioning also goes through the outbox path, so a fully
// failing writer would prevent the admin from being created. We therefore use a
// one-shot writer that succeeds for the first call (setup/admin) and fails for
// subsequent calls (session login emit).
func TestSessionLogin_OutboxFailureRollsBackPGRows(t *testing.T) {
	ctx := context.Background()

	// oneshot writer: succeeds on the first Write (setup admin), injects failure
	// on all subsequent writes (login outbox emit inside session tx). The writer
	// records every Write call so the test can prove the failure was injected at
	// the session.created emit, not at admin provisioning or some earlier path.
	oneshotWriter := &oneshotFailOutboxWriter{
		firstOK: true,
		failErr: errors.New("injected login outbox failure"),
	}
	h := newSessionPGHarnessWithWriter(t, oneshotWriter)

	// Snapshot counts after harness setup (1 admin provisioned — no sessions yet).
	var sessionsBefore, rtBefore int
	err := h.pool.DB().QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessionsBefore)
	require.NoError(t, err)
	err = h.pool.DB().QueryRow(ctx, `SELECT count(*) FROM refresh_tokens`).Scan(&rtBefore)
	require.NoError(t, err)
	assert.Equal(t, 0, sessionsBefore, "no sessions before first login")
	assert.Equal(t, 0, rtBefore, "no refresh_tokens before first login")

	// Snapshot the Write calls made during admin provisioning. The login attempt
	// must add exactly one more Write call whose EventType is the session.created
	// topic — proving the failure was injected at the session.created emit step.
	adminWriteCount := oneshotWriter.CallCount()
	require.Greater(t, adminWriteCount, 0,
		"admin provisioning must call Write at least once (event.user.created.v1 / event.role.assigned.v1)")

	// Attempt login with correct credentials — outbox Write will fail inside the
	// tx, so the whole tx (session + refresh_token rows) must roll back.
	body, _ := json.Marshal(map[string]string{"username": sessionPGAdminUsername, "password": sessionPGAdminPassword})
	resp, loginErr := setupHTTPClient.Post(h.base+"/api/v1/access/sessions/login", "application/json", bytes.NewReader(body))
	require.NoError(t, loginErr)
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	// HTTP layer should surface the rollback as a 5xx (KindUnavailable / KindInternal);
	// the precise code is not pinned because rollback handling may evolve, but 201
	// (success) is forbidden — that would mean the tx committed despite Write failure.
	assert.NotEqual(t, http.StatusCreated, resp.StatusCode,
		"login with injected outbox failure must not return 201 (tx rolled back); body=%s", string(respBody))
	assert.GreaterOrEqual(t, resp.StatusCode, 500,
		"login outbox failure must surface as 5xx, not as 4xx; body=%s", string(respBody))

	// Spy-side assertion: the failing Write must have been the session.created
	// emit. Without this, the test could pass when the failure is at any earlier
	// or unrelated Write call (e.g., admin provisioning regression).
	calls := oneshotWriter.Calls()
	require.Greater(t, len(calls), adminWriteCount,
		"login attempt must have triggered an additional Write call after admin provisioning")
	failedCall := calls[adminWriteCount]
	assert.Equal(t, "event.session.created.v1", failedCall.EventType,
		"the failing Write must be the session.created emit (not admin provisioning or another path)")

	var sessionsAfter, rtAfter int
	err = h.pool.DB().QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessionsAfter)
	require.NoError(t, err)
	err = h.pool.DB().QueryRow(ctx, `SELECT count(*) FROM refresh_tokens`).Scan(&rtAfter)
	require.NoError(t, err)

	assert.Equal(t, sessionsBefore, sessionsAfter,
		"sessions count must not increase when outbox write fails inside login tx")
	assert.Equal(t, rtBefore, rtAfter,
		"refresh_tokens count must not increase when outbox write fails inside login tx")
}

// oneshotFailOutboxWriter succeeds on the first Write (allowing admin provisioning)
// and injects a failure on all subsequent writes (session login emit). It also
// records every Write entry so tests can assert the failure was injected at the
// expected event type.
type oneshotFailOutboxWriter struct {
	mu      sync.Mutex
	firstOK bool
	failErr error
	calls   []outbox.Entry
}

func (w *oneshotFailOutboxWriter) Write(_ context.Context, entry outbox.Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, entry)
	if w.firstOK {
		w.firstOK = false
		return nil
	}
	return w.failErr
}

// Calls returns a snapshot of every Write call this writer has received, in
// arrival order. Callers use the returned slice to assert which event triggered
// the injected failure (transactional-outbox spy pattern).
func (w *oneshotFailOutboxWriter) Calls() []outbox.Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	snapshot := make([]outbox.Entry, len(w.calls))
	copy(snapshot, w.calls)
	return snapshot
}

// CallCount returns the number of Write calls received so far without copying
// the underlying slice — cheaper for snapshot-and-compare assertions.
func (w *oneshotFailOutboxWriter) CallCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.calls)
}

// TestSessionRefresh_PreservesPGSessionRow verifies that a Refresh call does
// NOT mutate the session row. session.ID is stable from login to logout
// (OAuth2 RFC 6749 §6 + OIDC Back-Channel Logout sid stability + ory-fosite /
// zitadel / keycloak alignment). The refresh chain rotates; the session row
// stays as login set it.
func TestSessionRefresh_PreservesPGSessionRow(t *testing.T) {
	ctx := context.Background()
	h := newSessionPGHarness(t)

	_, refreshTok, sessionID := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)

	// Capture row before refresh.
	var oldJTI string
	var oldExpiresAt time.Time
	var oldCreatedAt time.Time
	err := h.pool.DB().QueryRow(ctx,
		`SELECT jti, expires_at, created_at FROM sessions WHERE id = $1`, sessionID).
		Scan(&oldJTI, &oldExpiresAt, &oldCreatedAt)
	require.NoError(t, err, "session row must exist before refresh")

	newAccessTok, _, refreshSessionID := sessionPGRefresh(t, h.base, refreshTok)
	require.NotEmpty(t, newAccessTok)
	assert.Equal(t, sessionID, refreshSessionID, "refresh must return the stable session ID")

	// Session row must be unchanged after refresh.
	var jtiAfter string
	var revokedAtAfter *time.Time
	var expiresAtAfter time.Time
	var createdAtAfter time.Time
	err = h.pool.DB().QueryRow(ctx,
		`SELECT jti, revoked_at, expires_at, created_at FROM sessions WHERE id = $1`, sessionID).
		Scan(&jtiAfter, &revokedAtAfter, &expiresAtAfter, &createdAtAfter)
	require.NoError(t, err, "session row must still exist after refresh")
	assert.Nil(t, revokedAtAfter, "session revoked_at must remain NULL after refresh")
	assert.Equal(t, oldJTI, jtiAfter, "session jti unchanged by refresh (login-time fingerprint)")
	assert.Equal(t, oldExpiresAt.UTC(), expiresAtAfter.UTC(), "session expires_at unchanged by refresh")
	assert.Equal(t, oldCreatedAt.UTC(), createdAtAfter.UTC(), "session created_at unchanged by refresh")

	// No additional session rows must be created by refresh.
	var sessionCount int
	err = h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE subject_id::text IN
		     (SELECT subject_id::text FROM sessions WHERE id = $1)`, sessionID).Scan(&sessionCount)
	require.NoError(t, err)
	assert.Equal(t, 1, sessionCount, "refresh must not append additional session rows")
}

// TestSessionRefresh_TwoHops_PG is the reproduction test for the PR #482 P1
// chain-rotation bug: a second refresh hop must succeed using the wire token
// returned by the first refresh. Before the fix, the second hop failed
// because the refresh chain pointed at a revoked (new) session UUID; after
// the fix (session.ID stable across refresh), the chain stays consistent.
func TestSessionRefresh_TwoHops_PG(t *testing.T) {
	ctx := context.Background()
	h := newSessionPGHarness(t)

	_, wire1, sessionID := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)

	access2, wire2, sess2 := sessionPGRefresh(t, h.base, wire1)
	require.NotEmpty(t, access2, "first refresh must succeed and return an access token")
	require.NotEmpty(t, wire2, "first refresh must return a new wire token")
	assert.Equal(t, sessionID, sess2, "session ID stable after first refresh")

	access3, wire3, sess3 := sessionPGRefresh(t, h.base, wire2)
	require.NotEmpty(t, access3, "second refresh must succeed using the rotated wire token (PR #482 P1)")
	require.NotEmpty(t, wire3, "second refresh must return a new wire token")
	assert.NotEqual(t, wire2, wire3, "second hop yields a distinct wire token")
	assert.Equal(t, sessionID, sess3, "session ID stable after second refresh")

	// Session row must still be the same single row.
	var sessionRowCount int
	err := h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE id = $1 AND revoked_at IS NULL`, sessionID).Scan(&sessionRowCount)
	require.NoError(t, err)
	assert.Equal(t, 1, sessionRowCount, "exactly 1 active session row after two refresh hops")
}

// sessionPGLockUser calls POST /api/v1/access/users/{userID}/lock with the
// given admin access token and returns the HTTP status code.
//
// The Lock endpoint has an empty request schema but the generated handler
// calls DecodeJSONStrict, which rejects nil bodies with 400. Send "{}" so the
// decoder sees a valid empty object.
func sessionPGLockUser(t *testing.T, base, adminAccessToken, userID string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/api/v1/access/users/"+userID+"/lock", bytes.NewReader([]byte("{}")))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+adminAccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := setupHTTPClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// sessionPGQueryUserIDByUsername queries the users table for the UUID of the
// given username. Used by S4b tests to obtain the userID for lock/unlock calls.
func sessionPGQueryUserIDByUsername(t *testing.T, h *sessionPGHarness, username string) string {
	t.Helper()
	var userID string
	err := h.pool.DB().QueryRow(context.Background(),
		`SELECT id FROM users WHERE username = $1`, username).Scan(&userID)
	require.NoError(t, err, "user row must exist for username=%s", username)
	return userID
}

// afterFailOutboxWriter is an outbox.Writer that succeeds on the first N writes
// and injects a failure on all subsequent writes. It also records all entries.
type afterFailOutboxWriter struct {
	mu        sync.Mutex
	succeedN  int
	failErr   error
	callCount int
	calls     []outbox.Entry
}

func (w *afterFailOutboxWriter) Write(_ context.Context, entry outbox.Entry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, entry)
	w.callCount++
	if w.callCount <= w.succeedN {
		return nil
	}
	return w.failErr
}

func (w *afterFailOutboxWriter) CallCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.callCount
}

// TestS4b_CredentialEvent_InvalidatesAccessJWT verifies the full S4b epoch
// funnel path:
//
//  1. Login to obtain an access JWT at epoch=1 (NewUser baseline, S4d).
//  2. Call Lock on the user — credentialinvalidate funnel atomically bumps
//     authz_epoch 1→2, revokes sessions, and revokes refresh tokens.
//  3. Assert PG: users.authz_epoch = 2.
//  4. Assert PG: sessions.revoked_at IS NOT NULL for the subject.
//  5. Replay the epoch=1 access JWT against any JWT-guarded endpoint → 401
//     ERR_AUTH_INVALID_TOKEN (epoch mismatch detected in enforceSessionState).
//  6. Close the PG pool to simulate a DB outage; replay same JWT → the session
//     lookup fails with KindUnavailable, which the errcode projection collapses
//     to the Kind public code ERR_SERVICE_UNAVAILABLE on wire (5xx strip rule —
//     the source code ErrAuthServiceUnavailable is kept only in server logs).
func TestS4b_CredentialEvent_InvalidatesAccessJWT(t *testing.T) {
	ctx := context.Background()
	h := newSessionPGHarness(t)

	// 1. Login as admin — admin's JWT is used only to authorize the create+lock
	// calls below. Locking the admin themselves would trip the
	// effective_admin_invariant (403 ErrAuthLastAdminProtected) because the
	// harness only provisions a single admin; the funnel-bump assertions need
	// a non-admin victim, so we create a regular user first.
	adminAccessTok, _, _ := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)

	// 2. Create a non-admin user via POST /api/v1/access/users (admin policy).
	const victimUsername = "session-victim"
	const victimPassword = "VictimPass!99"
	createBody, _ := json.Marshal(map[string]string{
		"username": victimUsername,
		"email":    "victim@pg-test.local",
		"password": victimPassword,
	})
	createReq, _ := http.NewRequest(http.MethodPost, h.base+"/api/v1/access/users", bytes.NewReader(createBody))
	createReq.Header.Set("Authorization", "Bearer "+adminAccessTok)
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := setupHTTPClient.Do(createReq)
	require.NoError(t, err)
	_ = createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode, "victim user creation must return 201")

	// 3. Login as the victim user to obtain an access JWT (epoch=1, NewUser baseline).
	victimAccessTok, _, _ := sessionPGLogin(t, h.base, victimUsername, victimPassword)
	victimID := sessionPGQueryUserIDByUsername(t, h, victimUsername)

	// 4. Lock the victim user via POST /api/v1/access/users/{id}/lock (admin policy).
	// Admin token authorizes; victim is non-admin so last-admin invariant is not engaged.
	lockStatus := sessionPGLockUser(t, h.base, adminAccessTok, victimID)
	assert.Equal(t, http.StatusOK, lockStatus,
		"Lock of non-admin victim must return 200")

	// 5. PG assertion: victim.authz_epoch must be exactly 2 after Lock.
	// NewUser sets baseline epoch=1 (S4d); credentialinvalidate funnel bumps 1→2 on Lock.
	var epoch int64
	err = h.pool.DB().QueryRow(ctx,
		`SELECT authz_epoch FROM users WHERE id = $1`, victimID).Scan(&epoch)
	require.NoError(t, err, "victim users row must exist")
	assert.Equal(t, int64(2), epoch,
		"victim.authz_epoch must be 2 after Lock (NewUser baseline 1 + credentialinvalidate funnel bumped once)")

	// 6. PG assertion: all victim sessions must be revoked.
	var activeSessionCount int
	err = h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE subject_id = $1 AND revoked_at IS NULL`, victimID).
		Scan(&activeSessionCount)
	require.NoError(t, err)
	assert.Equal(t, 0, activeSessionCount,
		"all victim sessions must be revoked after Lock; active session count must be 0")

	// 7. Replay the victim's epoch=1 access JWT → 401 (epoch mismatch: user.authz_epoch=2 vs JWT epoch=1).
	// Use GET /api/v1/access/users/{id} as the target — any JWT-guarded endpoint works.
	replayReq, _ := http.NewRequest(http.MethodGet, h.base+"/api/v1/access/users/"+victimID, nil)
	replayReq.Header.Set("Authorization", "Bearer "+victimAccessTok)
	replayResp, err := setupHTTPClient.Do(replayReq)
	require.NoError(t, err)
	replayBody, _ := io.ReadAll(replayResp.Body)
	_ = replayResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, replayResp.StatusCode,
		"victim's epoch=1 JWT replayed after epoch bump to 2 must be rejected with 401; body=%s", replayBody)
	var replayEnvelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(replayBody, &replayEnvelope))
	// AuthMiddleware maps every JWT verification failure (signature / expiry /
	// intent / epoch / session-state) to the generic ERR_AUTH_UNAUTHORIZED to
	// prevent token-state enumeration. Granular failure reasons stay in slog
	// and the auth_token_verify_total `reason` label only — see
	// runtime/auth/middleware.go::handleAuthRequest godoc.
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", replayEnvelope.Error.Code,
		"epoch mismatch must surface as the generic ERR_AUTH_UNAUTHORIZED (enumeration defense)")

	// 8. Close the PG pool (simulates DB outage) — session/user lookup returns
	// KindUnavailable. errcode.project() strips the granular source code on
	// 5xx responses, so the wire code collapses to the Kind public code
	// ERR_SERVICE_UNAVAILABLE; ErrAuthServiceUnavailable remains visible in
	// structured server logs. See ADR 202605051730-adr-errcode-message-pii-safety.
	require.NoError(t, h.pool.Close(ctx), "pool.Close must not error")

	dbDownReq, _ := http.NewRequest(http.MethodGet, h.base+"/api/v1/access/users/"+victimID, nil)
	dbDownReq.Header.Set("Authorization", "Bearer "+victimAccessTok)
	dbDownResp, err := setupHTTPClient.Do(dbDownReq)
	require.NoError(t, err)
	dbDownBody, _ := io.ReadAll(dbDownResp.Body)
	_ = dbDownResp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, dbDownResp.StatusCode,
		"after DB outage session lookup must surface as 503; body=%s", dbDownBody)
	var dbDownEnvelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(dbDownBody, &dbDownEnvelope))
	assert.Equal(t, "ERR_SERVICE_UNAVAILABLE", dbDownEnvelope.Error.Code,
		"5xx wire code must be the Kind public code ERR_SERVICE_UNAVAILABLE; "+
			"the granular ErrAuthServiceUnavailable source is stripped on wire and kept only in logs; body=%s", dbDownBody)
}

// TestS4b_RefreshReuse_CascadesEpochAndSession verifies that exhausting the
// refresh grace window then replaying the consumed parent token triggers the
// reuse cascade: atomic epoch bump + all-sessions revoke via the
// credentialinvalidate funnel.
//
//  1. Login → obtain access token A1, refresh token R1, session S1.
//  2. Refresh R1 → obtain A2, R2 (R1 is now rotated, parent of R2).
//  3. Replay R1 GraceMaxReuses (=3) times — each replay is within the
//     ReuseInterval grace window, so each succeeds with new child tokens
//     while incrementing R1.used_times to 3. This models the "client retried
//     because the previous refresh response was lost" scenario the grace
//     window is designed to tolerate.
//  4. Replay R1 a 4th time → used_times >= GraceMaxReuses → handleRotatedRow
//     fires reuse_detected/grace_exhausted → 401 ERR_AUTH_REFRESH_FAILED and
//     the cascade revoke runs.
//  5. PG assertion: authz_epoch is bumped (reuse-cascade funnel ran).
//  6. PG assertion: sessions.revoked_at IS NOT NULL for all subject sessions.
func TestS4b_RefreshReuse_CascadesEpochAndSession(t *testing.T) {
	ctx := context.Background()
	h := newSessionPGHarness(t)

	// 1. Login.
	_, refreshTok1, sessionID := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)
	userID := sessionPGQueryUserIDByUsername(t, h, sessionPGAdminUsername)

	// 2. First refresh — rotates R1, issues R2. R1.rotated_at is set; R1.used_times stays 0
	// (handleRotatedRow's grace-counter increment only fires when row.rotated_at != nil at
	// validation time, which happens on subsequent re-presentations of R1).
	_, refreshTok2, _ := sessionPGRefresh(t, h.base, refreshTok1)
	require.NotEmpty(t, refreshTok2, "first refresh must succeed")

	// Snapshot epoch before reuse cascade.
	var epochBefore int64
	err := h.pool.DB().QueryRow(ctx,
		`SELECT authz_epoch FROM users WHERE id = $1`, userID).Scan(&epochBefore)
	require.NoError(t, err)

	// 3. Replay R1 GraceMaxReuses times — each succeeds with new child tokens
	// (grace retry path: client thinks the previous refresh response was lost
	// and retries with the same parent). Each iteration bumps R1.used_times.
	for i := 0; i < refresh.DefaultGraceMaxReuses; i++ {
		retryBody, _ := json.Marshal(map[string]string{"refreshToken": refreshTok1})
		retryResp, retryErr := setupHTTPClient.Post(h.base+"/api/v1/access/sessions/refresh",
			"application/json", bytes.NewReader(retryBody))
		require.NoError(t, retryErr)
		retryRespBody, _ := io.ReadAll(retryResp.Body)
		_ = retryResp.Body.Close()
		require.Equal(t, http.StatusOK, retryResp.StatusCode,
			"grace retry %d must succeed (within ReuseInterval, used_times < GraceMaxReuses); body=%s",
			i+1, retryRespBody)
	}

	// 4. (GraceMaxReuses+1)th replay of R1 → grace_exhausted → 401 + cascade.
	reusedBody, _ := json.Marshal(map[string]string{"refreshToken": refreshTok1})
	resp, err := setupHTTPClient.Post(h.base+"/api/v1/access/sessions/refresh",
		"application/json", bytes.NewReader(reusedBody))
	require.NoError(t, err)
	reuseRespBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"grace-exhausted replay must be rejected with 401; body=%s", reuseRespBody)
	var reuseEnvelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(reuseRespBody, &reuseEnvelope))
	assert.Equal(t, "ERR_AUTH_REFRESH_FAILED", reuseEnvelope.Error.Code,
		"refresh reuse must return ERR_AUTH_REFRESH_FAILED")

	// 4. PG assertion: authz_epoch must be bumped by the reuse-cascade funnel.
	require.Eventually(t, func() bool {
		var epoch int64
		qErr := h.pool.DB().QueryRow(ctx,
			`SELECT authz_epoch FROM users WHERE id = $1`, userID).Scan(&epoch)
		return qErr == nil && epoch > epochBefore
	}, testtime.EventuallyDefault, testtime.D10ms,
		"authz_epoch must be bumped after refresh reuse cascade (epochBefore=%d)", epochBefore)

	// 5. PG assertion: all sessions for the subject revoked.
	var activeSessionCount int
	err = h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE subject_id = $1 AND revoked_at IS NULL`,
		userID).Scan(&activeSessionCount)
	require.NoError(t, err)
	assert.Equal(t, 0, activeSessionCount,
		"session %s and all peer sessions must be revoked after refresh reuse cascade", sessionID)
}

// TestS4b_RbacRevoke_SameTxAtomicity verifies that when the outbox Write fails
// inside the rbacassign.Revoke transaction, the entire transaction rolls back:
// the role assignment is NOT removed and the authz_epoch is NOT bumped.
//
//  1. Set up harness with an afterFailOutboxWriter that succeeds for admin
//     provisioning writes and fails on the role.revoked outbox write.
//  2. Login as admin; obtain user ID.
//  3. Assign role "editor" to admin user.
//  4. Attempt to revoke the role → outbox write fails → tx rollback → 500.
//  5. PG assertion: role_assignments row for (userID, "editor") still present.
//  6. PG assertion: authz_epoch unchanged (funnel did not commit).
func TestS4b_RbacRevoke_SameTxAtomicity(t *testing.T) {
	ctx := context.Background()

	// Admin provisioning emits event.user.created.v1 + event.role.assigned.v1 = 2 writes.
	// Role assign emits event.role.assigned.v1 = 1 write (write #3 — succeeds).
	// Role revoke emits event.role.revoked.v1 = 1 write (write #4 — injected failure).
	const adminProvisionWrites = 2
	const roleAssignWrites = 1
	const succeedN = adminProvisionWrites + roleAssignWrites

	failWriter := &afterFailOutboxWriter{
		succeedN: succeedN,
		failErr:  errors.New("injected role.revoked outbox failure"),
	}
	h := newSessionPGHarnessWithWriter(t, failWriter)

	// 2. Login as admin.
	accessTok, _, _ := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)
	userID := sessionPGQueryUserIDByUsername(t, h, sessionPGAdminUsername)

	// Snapshot epoch before operations.
	var epochBefore int64
	err := h.pool.DB().QueryRow(ctx,
		`SELECT authz_epoch FROM users WHERE id = $1`, userID).Scan(&epochBefore)
	require.NoError(t, err)

	// 3. Assign "editor" role via internal listener.
	assignBody, _ := json.Marshal(map[string]string{"userId": userID, "roleId": "editor"})
	assignToken := auth.GenerateServiceToken(h.ring, "accesscore", http.MethodPost, "/internal/v1/access/roles/assign", "", time.Now())
	assignReq, _ := http.NewRequest(http.MethodPost, h.internalBase+"/internal/v1/access/roles/assign",
		bytes.NewReader(assignBody))
	assignReq.Header.Set("Authorization", "ServiceToken "+assignToken)
	assignReq.Header.Set("Content-Type", "application/json")
	assignResp, err := setupHTTPClient.Do(assignReq)
	require.NoError(t, err)
	assignRespBody, _ := io.ReadAll(assignResp.Body)
	_ = assignResp.Body.Close()
	require.Equal(t, http.StatusCreated, assignResp.StatusCode,
		"role assign must succeed (outbox write #3 is within succeedN=%d); body=%s",
		succeedN, assignRespBody)

	// Confirm role assignment is in PG before revoke.
	var roleCount int
	err = h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM role_assignments WHERE user_id = $1 AND role_id = 'editor'`,
		userID).Scan(&roleCount)
	require.NoError(t, err)
	assert.Equal(t, 1, roleCount, "editor role assignment must be present after assign")

	// 4. Revoke "editor" role → outbox write fails → tx rollback → 500.
	revokeBody, _ := json.Marshal(map[string]string{"userId": userID, "roleId": "editor"})
	revokeToken := auth.GenerateServiceToken(h.ring, "accesscore", http.MethodPost, "/internal/v1/access/roles/revoke", "", time.Now())
	revokeReq, _ := http.NewRequest(http.MethodPost, h.internalBase+"/internal/v1/access/roles/revoke",
		bytes.NewReader(revokeBody))
	revokeReq.Header.Set("Authorization", "ServiceToken "+revokeToken)
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeResp, err := setupHTTPClient.Do(revokeReq)
	require.NoError(t, err)
	revokeRespBody, _ := io.ReadAll(revokeResp.Body)
	_ = revokeResp.Body.Close()
	assert.GreaterOrEqual(t, revokeResp.StatusCode, 500,
		"role revoke with injected outbox failure must return 5xx; body=%s", revokeRespBody)

	// 5. PG assertion: role assignment must still be present (tx rolled back).
	err = h.pool.DB().QueryRow(ctx,
		`SELECT count(*) FROM role_assignments WHERE user_id = $1 AND role_id = 'editor'`,
		userID).Scan(&roleCount)
	require.NoError(t, err)
	assert.Equal(t, 1, roleCount,
		"editor role assignment must still be present after tx rollback (outbox failure)")

	// 6. PG assertion: authz_epoch must not have advanced (funnel did not commit).
	var epochAfter int64
	err = h.pool.DB().QueryRow(ctx,
		`SELECT authz_epoch FROM users WHERE id = $1`, userID).Scan(&epochAfter)
	require.NoError(t, err)
	assert.Equal(t, epochBefore, epochAfter,
		"authz_epoch must be unchanged after aborted revoke tx (epochBefore=%d, epochAfter=%d)",
		epochBefore, epochAfter)

	// Confirm the injected failure was triggered at the expected write.
	assert.Greater(t, failWriter.CallCount(), succeedN,
		"afterFailOutboxWriter must have been triggered past succeedN=%d writes", succeedN)

	_ = accessTok // used only for harness warmup; lock/revoke flow tested via internal listener
}

// TestS4b_RoleChangeConsumer_NoRedundantRevoke verifies that the
// sessionlogout.Consumer does NOT call RevokeForSubject when it receives a
// role.revoked event — the credential invalidation is already handled by
// rbacassign.Revoke via the credentialinvalidate funnel in the same tx.
//
// Strategy: after a successful role revoke (funnel runs once → epoch bumped
// from baseline 1 to 2), wait for the in-memory eventbus to deliver the
// role.revoked outbox event to the sessionlogout consumer. Then assert
// authz_epoch is still exactly 2 — not 3 — proving the consumer did not call
// the funnel a second time.
//
// This is the anti-double-bump guard for S4b plan §3.2.
func TestS4b_RoleChangeConsumer_NoRedundantRevoke(t *testing.T) {
	ctx := context.Background()
	h := newSessionPGHarness(t)

	accessTok, _, _ := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)
	userID := sessionPGQueryUserIDByUsername(t, h, sessionPGAdminUsername)

	// Assign "editor" role so revoke has something to act on.
	assignBody, _ := json.Marshal(map[string]string{"userId": userID, "roleId": "editor"})
	assignToken := auth.GenerateServiceToken(h.ring, "accesscore", http.MethodPost, "/internal/v1/access/roles/assign", "", time.Now())
	assignReq, _ := http.NewRequest(http.MethodPost, h.internalBase+"/internal/v1/access/roles/assign",
		bytes.NewReader(assignBody))
	assignReq.Header.Set("Authorization", "ServiceToken "+assignToken)
	assignReq.Header.Set("Content-Type", "application/json")
	assignResp, err := setupHTTPClient.Do(assignReq)
	require.NoError(t, err)
	_ = assignResp.Body.Close()
	require.Equal(t, http.StatusCreated, assignResp.StatusCode, "role assign must succeed")

	// Snapshot epoch — must be 1 after assign (NewUser baseline 1; assign is additive, no bump).
	var epochBeforeRevoke int64
	err = h.pool.DB().QueryRow(ctx,
		`SELECT authz_epoch FROM users WHERE id = $1`, userID).Scan(&epochBeforeRevoke)
	require.NoError(t, err)
	assert.Equal(t, int64(1), epochBeforeRevoke,
		"authz_epoch must still be 1 after role assign (NewUser baseline 1; assign additive, no credential invalidation)")

	// Revoke the role — funnel runs once: epoch bumped 1→2.
	revokeBody, _ := json.Marshal(map[string]string{"userId": userID, "roleId": "editor"})
	revokeToken := auth.GenerateServiceToken(h.ring, "accesscore", http.MethodPost, "/internal/v1/access/roles/revoke", "", time.Now())
	revokeReq, _ := http.NewRequest(http.MethodPost, h.internalBase+"/internal/v1/access/roles/revoke",
		bytes.NewReader(revokeBody))
	revokeReq.Header.Set("Authorization", "ServiceToken "+revokeToken)
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeResp, err := setupHTTPClient.Do(revokeReq)
	require.NoError(t, err)
	revokeRespBody, _ := io.ReadAll(revokeResp.Body)
	_ = revokeResp.Body.Close()
	require.Equal(t, http.StatusOK, revokeResp.StatusCode,
		"role revoke must succeed; body=%s", revokeRespBody)

	// The funnel ran exactly once (inside rbacassign tx). Wait briefly for the
	// in-memory eventbus to deliver the role.revoked event to the sessionlogout
	// consumer. After consumer processing, epoch must be exactly 2 — not 3.
	// The consumer only logs + Acks; it does NOT call the funnel again.
	require.Eventually(t, func() bool {
		var epoch int64
		qErr := h.pool.DB().QueryRow(ctx,
			`SELECT authz_epoch FROM users WHERE id = $1`, userID).Scan(&epoch)
		return qErr == nil && epoch >= 2
	}, testtime.EventuallyDefault, testtime.D10ms,
		"authz_epoch must be ≥ 2 after role revoke (baseline 1 + funnel bump)")

	// Small stabilization wait — if the consumer were to call the funnel a
	// second time, epoch would become 3. We assert it stays exactly 2. No
	// observable signal exists for a "negative" event (consumer does not bump),
	// so a bounded sleep is the only available stabilization primitive.
	time.Sleep(testtime.ShortSleep) //archtest:allow:test-sleep negative-assertion stabilization

	var epochFinal int64
	err = h.pool.DB().QueryRow(ctx,
		`SELECT authz_epoch FROM users WHERE id = $1`, userID).Scan(&epochFinal)
	require.NoError(t, err)
	assert.Equal(t, int64(2), epochFinal,
		"authz_epoch must be exactly 2 after role revoke — consumer must NOT call funnel again "+
			"(anti-double-bump guard, S4b plan §3.2); got %d", epochFinal)

	_ = accessTok // used only to confirm login works before revoke
}

// TestSessionLogout_RevokesPGRow verifies that a Logout call sets revoked_at on
// the sessions row for the given session ID.
func TestSessionLogout_RevokesPGRow(t *testing.T) {
	ctx := context.Background()
	h := newSessionPGHarness(t)

	accessTok, _, sessionID := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)

	// Confirm row is active before logout.
	var revokedAtBefore *time.Time
	err := h.pool.DB().QueryRow(ctx,
		`SELECT revoked_at FROM sessions WHERE id = $1`, sessionID).Scan(&revokedAtBefore)
	require.NoError(t, err, "session row must exist after login")
	assert.Nil(t, revokedAtBefore, "revoked_at must be NULL before logout")

	sessionPGLogout(t, h.base, accessTok, sessionID)

	// Row must now have revoked_at set.
	var revokedAtAfter *time.Time
	err = h.pool.DB().QueryRow(ctx,
		`SELECT revoked_at FROM sessions WHERE id = $1`, sessionID).Scan(&revokedAtAfter)
	require.NoError(t, err, "session row must still exist after logout (append-only)")
	assert.NotNil(t, revokedAtAfter, "revoked_at must be non-null after Logout")
}
