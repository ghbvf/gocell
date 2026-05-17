//go:build integration

package main

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

	"github.com/google/uuid"
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
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// setupTestBootstrapUsername / Password are the operator credentials wired
// into accesscore.WithBootstrapAuth for this integration test. They are also
// the credentials the test sends in the Basic Auth header on POST
// /setup/admin requests — ADR §D5: env creds authenticate the operator,
// request body defines the admin identity.
const (
	setupTestBootstrapUsername = "setup-test-op"
	setupTestBootstrapPassword = "setup-test-pass-1!"
)

// setupTestAllowAllLimiter satisfies auth.BootstrapRateLimiter without
// throttling. AUTH-AUTHTEST-B archtest forbids cells/examples from importing
// runtime/auth/authtest, so we keep an equivalent fake in each test file
// rather than introducing a shared helper.
type setupTestAllowAllLimiter struct{}

func (setupTestAllowAllLimiter) Allow(string) bool { return true }

// setupHTTPClient uses a longer timeout than the shared testHTTPClient because
// bcrypt at domain.BcryptCost=12 takes ~1-2s per password hash, which exceeds
// the 2s client-default when the CPU is contended by parallel test packages.
var setupHTTPClient = &http.Client{Timeout: testtime.SelectAsyncSettle}

// TestSetupEndpoints_FirstRunFlow boots a real assembly (accesscore+configcore+auditcore)
// and walks the interactive first-run admin flow end-to-end:
//
//  1. GET /api/v1/access/setup/status            → {hasAdmin:false}  (no JWT required)
//  2. POST /api/v1/access/setup/admin            → 201 + user body
//  3. POST /api/v1/access/setup/admin (again)    → 410 ERR_SETUP_ALREADY_INITIALIZED
//  4. GET /api/v1/access/setup/status            → {hasAdmin:true}
//  5. POST /api/v1/access/sessions/login  → 201 with access/refresh tokens
//
// Step 5 proves the setup-created admin can actually authenticate — i.e. the
// password was hashed and persisted correctly by bcrypt round-trip.
func TestSetupEndpoints_FirstRunFlow(t *testing.T) {
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

	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)

	pg := newAuthTestPGCoreDeps(t)

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(setupTestBootstrapUsername),
			Password: []byte(setupTestBootstrapPassword),
		},
		setupTestAllowAllLimiter{},
		nil,
	)
	ac := accesscore.NewAccessCore(append(buildAccessCorePGOptions(t, pg.pool, pg.txMgr),
		accesscore.WithClock(clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(adapterpg.NewOutboxWriter(clock.Real()))),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(persistence.WrapForCell(pg.txMgr)),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),

		accesscore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))),
	)...) //archtest:allow:clock-injection:via-slice buildAccessCorePGOptions + WithClock prepended; spread prevents direct positional arg
	cc := configcore.NewConfigCore(buildConfigCorePGOptions(t, pg.pool, pg.txMgr, eb, configCursorCodec)...)
	auc := auditcore.NewAuditCore(append([]auditcore.Option{
		auditcore.WithClock(clock.Real()),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), nil),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	}, auditcoreLedgerPGOpts(t, pg.pool, pg.txMgr, []byte("test-hmac-key-32-bytes-long!!!!!"))...)...) //archtest:allow:clock-injection:via-slice WithClock is in the first slice arg passed to append; spread prevents direct positional arg

	asm := assembly.New(assembly.Config{ID: "setup-test", DurabilityMode: cell.DurabilityDurable, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}, bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, clock.Real())),
		bootstrap.WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
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

	// 1. Fresh system: hasAdmin=false (endpoint is Public — no Authorization header).
	t.Run("status_before_returns_false", func(t *testing.T) {
		resp, err := setupHTTPClient.Get(base + "/api/v1/access/setup/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "setup/status must be Public (not 401)")
		var body struct {
			Data struct {
				HasAdmin bool `json:"hasAdmin"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.False(t, body.Data.HasAdmin)
	})

	// 2a. POST without Basic Auth must 401 — proves the closed contract: the
	//     bootstrap middleware is wired in front of the generated handler, and
	//     ERR_AUTH_BOOTSTRAP_FAILED is the canonical envelope (no oracle).
	t.Run("create_admin_no_auth_returns_401", func(t *testing.T) {
		payload := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			base+"/api/v1/access/setup/admin", strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		// Intentionally no SetBasicAuth.
		resp, err := setupHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"setup/admin without Basic Auth must 401 (ADR §D1 closed contract)")
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(raw), "ERR_AUTH_BOOTSTRAP_FAILED")
	})

	// 2b. Create first admin (with Basic Auth).
	password := "SecretPass!23"
	t.Run("create_admin_returns_201", func(t *testing.T) {
		payload := `{"username":"root","email":"root@local","password":"` + password + `"}`
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			base+"/api/v1/access/setup/admin", strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
		resp, err := setupHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusCreated, resp.StatusCode, "first setup/admin POST must return 201")
		var body struct {
			Data struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "root", body.Data.Username)
		_, idErr := uuid.Parse(body.Data.ID)
		assert.NoError(t, idErr, "user id must be a canonical UUID (PR-A45)")
	})

	// 3. Second POST (with Basic Auth) must 410 Gone — one-shot lifecycle. The
	//    Basic Auth still needs to pass; 401 short-circuits before 410.
	t.Run("second_create_returns_410", func(t *testing.T) {
		payload := `{"username":"root2","email":"other@local","password":"AnotherPass!99"}`
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			base+"/api/v1/access/setup/admin", strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
		resp, err := setupHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusGone, resp.StatusCode)
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(raw), "ERR_SETUP_ALREADY_INITIALIZED")
		assert.Contains(t, string(raw), `"key":"nextAction","value":"login"`)
	})

	// 4. Status now reports hasAdmin=true.
	t.Run("status_after_returns_true", func(t *testing.T) {
		resp, err := setupHTTPClient.Get(base + "/api/v1/access/setup/status")
		require.NoError(t, err)
		defer resp.Body.Close()
		var body struct {
			Data struct {
				HasAdmin bool `json:"hasAdmin"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.True(t, body.Data.HasAdmin)
	})

	// 5. Created admin can login with the password they chose — confirms bcrypt
	//    round-trip and role assignment both succeeded.
	t.Run("created_admin_can_login", func(t *testing.T) {
		payload := `{"username":"root","password":"` + password + `"}`
		resp, err := setupHTTPClient.Post(base+"/api/v1/access/sessions/login",
			"application/json", strings.NewReader(payload))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusCreated, resp.StatusCode, "setup-created admin must be able to login")
		var body struct {
			Data struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			} `json:"data"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.NotEmpty(t, body.Data.AccessToken)
		assert.NotEmpty(t, body.Data.RefreshToken)
	})
}

// setupTestBlockAfterNLimiter is a rate limiter that allows the first N requests
// and blocks all subsequent ones. Used to test 429 behavior without long waits.
type setupTestBlockAfterNLimiter struct {
	remaining int
}

func (l *setupTestBlockAfterNLimiter) Allow(string) bool {
	if l.remaining > 0 {
		l.remaining--
		return true
	}
	return false
}

// TestSetupAdminBootstrap_RateLimited_Returns429 verifies that when the
// bootstrap rate limiter is exhausted, POST /api/v1/access/setup/admin returns
// 429 with a Retry-After header. Uses a capacity=2 limiter so the test is fast.
// F7 RED until Wave 1 (F1: onAuthFail rate_limited) and assembly wiring are complete.
func TestSetupAdminBootstrap_RateLimited_Returns429(t *testing.T) {
	const capacity = 2

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

	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)

	pg := newAuthTestPGCoreDeps(t)

	limiter := &setupTestBlockAfterNLimiter{remaining: capacity}
	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(setupTestBootstrapUsername),
			Password: []byte(setupTestBootstrapPassword),
		},
		limiter,
		nil,
	)

	ac := accesscore.NewAccessCore(append(buildAccessCorePGOptions(t, pg.pool, pg.txMgr),
		accesscore.WithClock(clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(adapterpg.NewOutboxWriter(clock.Real()))),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(persistence.WrapForCell(pg.txMgr)),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),

		accesscore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))),
	)...) //archtest:allow:clock-injection:via-slice buildAccessCorePGOptions + WithClock prepended; spread prevents direct positional arg
	cc := configcore.NewConfigCore(buildConfigCorePGOptions(t, pg.pool, pg.txMgr, eb, configCursorCodec)...)
	auc := auditcore.NewAuditCore(append([]auditcore.Option{
		auditcore.WithClock(clock.Real()),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), nil),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	}, auditcoreLedgerPGOpts(t, pg.pool, pg.txMgr, []byte("test-hmac-key-32-bytes-long!!!!!"))...)...) //archtest:allow:clock-injection:via-slice WithClock is in the first slice arg passed to append; spread prevents direct positional arg

	asm := assembly.New(assembly.Config{ID: "ratelimit-test", DurabilityMode: cell.DurabilityDurable, Clock: clock.Real()})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}, bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, clock.Real())),
		bootstrap.WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
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

	// Exhaust the capacity (2 allowed requests).
	for i := 0; i < capacity; i++ {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			base+"/api/v1/access/setup/admin", strings.NewReader(`{"username":"op","email":"op@x","password":"Pass!1234"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
		resp, err := setupHTTPClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
	}

	// Next request must be rate-limited.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		base+"/api/v1/access/setup/admin", strings.NewReader(`{"username":"op","email":"op@x","password":"Pass!1234"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(setupTestBootstrapUsername, setupTestBootstrapPassword)
	resp, err := setupHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "exhausted limiter must return 429")
	assert.NotEmpty(t, resp.Header.Get("Retry-After"), "429 response must carry Retry-After header")
}
