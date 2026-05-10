//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
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
)

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
	dsn, dsnCleanup := setupPostgresForMain(t)
	defer dsnCleanup()

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

	pgDeps, err := accesscore.NewPGDeps(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)
	pgUserRepo, err := accesscore.NewPGUserRepository(pgDeps)
	require.NoError(t, err)
	pgRoleRepo, err := accesscore.NewPGRoleRepository(pgDeps)
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
	ac := accesscore.NewAccessCore(
		accesscore.WithClock(clock.Real()),
		// WithInMemoryDefaults installs all three repos as mem; the
		// subsequent WithUserRepository / WithRoleRepository overrides only
		// the two we backed with PG. Session repository stays mem (S3+S5
		// scope; runtime session.Store wiring lands in S4).
		accesscore.WithInMemoryDefaults(),
		accesscore.WithUserRepository(pgUserRepo),
		accesscore.WithRoleRepository(pgRoleRepo),
		accesscore.WithOutboxDeps(eb, nw),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(txMgr),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),
	)
	cc := configcore.NewConfigCore(
		configcore.WithClock(clock.Real()),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(eb, nw),
		configcore.WithTxManager(noopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)
	auc := auditcore.NewAuditCore(
		auditcore.WithClock(clock.Real()),
		auditcore.WithInMemoryDefaults(),
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey([]byte("test-hmac-key-32-bytes-long!!!!!")),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)

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
	defer func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		case <-time.After(testtime.SelectShutdown):
			t.Fatal("bootstrap did not shut down in time")
		}
	}()

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
	body := `{"username":"pg-admin","email":"pg-admin@example.com","password":"PgAdminPass!23"}`

	// 1. Fresh PG: POST → 201, admin row persisted.
	t.Run("create_admin_returns_201", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/setup/admin", strings.NewReader(body))
		req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
		req.Header.Set("Content-Type", "application/json")
		resp, err := setupHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
	})

	// 2. Retry: POST → 410, ErrSetupAlreadyInitialized (admin row counted in PG).
	t.Run("create_admin_retry_returns_410", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/setup/admin", strings.NewReader(body))
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
		resp, err := setupHTTPClient.Get(base + "/api/v1/access/setup/status")
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
