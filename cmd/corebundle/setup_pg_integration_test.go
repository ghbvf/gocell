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
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
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

	privKey, pubKey := auth.MustGenerateTestKeyPair()
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
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
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
type sessionPGHarness struct {
	pool *adapterpg.Pool
	base string
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
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
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

	return &sessionPGHarness{pool: pool, base: base}
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
//   - authz_epoch_at_issue = 0 (S4a: epoch not yet wired; default 0)
//   - refresh_tokens table has exactly 1 live row for that session_id
func TestSessionLogin_PGGoldenPath(t *testing.T) {
	ctx := context.Background()
	h := newSessionPGHarness(t)

	_, _, sessionID := sessionPGLogin(t, h.base, sessionPGAdminUsername, sessionPGAdminPassword)

	// Verify sessions row persisted correctly.
	var jti string
	var revokedAt *time.Time
	var authzEpoch int64
	err := h.pool.DB().QueryRow(ctx,
		`SELECT jti, authz_epoch_at_issue, revoked_at FROM sessions WHERE id = $1`,
		sessionID,
	).Scan(&jti, &authzEpoch, &revokedAt)
	require.NoError(t, err, "session row must exist after login")
	assert.NotEmpty(t, jti, "jti must be non-empty in sessions row")
	assert.Nil(t, revokedAt, "revoked_at must be NULL for an active session")
	assert.Equal(t, int64(0), authzEpoch, "authz_epoch_at_issue must be 0 (S4a scope: epoch wiring pending)")

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
