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
	"sync/atomic"
	"testing"
	"time"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/auth/authtest"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/ctxutil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/keystest"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
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
// throttling. The authtest helper lives under runtime/internal/authtest (Go
// internal/ rule refuses cmd/ imports at compile time, see issue #638), so we
// keep an equivalent fake in each test file rather than introducing a shared
// helper.
type setupTestAllowAllLimiter struct{}

func (setupTestAllowAllLimiter) Allow(string) bool { return true }

// setupHTTPClient uses a longer timeout than the shared testHTTPClient because
// bcrypt at credential.ProductionCost=12 takes ~1-2s per password hash, which
// exceeds the 2s client-default when the CPU is contended by parallel test
// packages.
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
	healthLn := newCorebundleLocalListener(t)

	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(clock.Real())
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)

	// Two physically isolated audit chains (issue #1121): auditcore writes
	// relay events on namespace="auditcore"; the bootstrap observer writes
	// 401/429 events on namespace="bootstrap". auditquery reads from both via
	// a ledger.MultiStore so the test can still assert that bootstrap.auth.fail
	// entries are visible end-to-end.
	auditHMAC := []byte("test-hmac-key-32-bytes-long!!!!!")
	auditProto := buildTestAuditProtocol(t, auditHMAC)
	auditStore := buildTestAuditStore(t, auditProto)
	bootstrapHMAC := []byte("test-bootstrap-hmac-key-32bytes!")
	bootstrapRaw, bootstrapWrapped := buildTestBootstrapAuditChain(t, bootstrapHMAC)
	multiStore, err := ledger.NewMultiStore(auditStore, bootstrapRaw)
	require.NoError(t, err)

	// Wave-1 #1423: the bootstrap auth-fail observer no longer writes auditcore's
	// ledger directly. It emits event.auth.bootstrap-failed.v1 via accesscore's
	// setup service (RecordBootstrapAuthFail); auditcore subscribes and writes the
	// bootstrap-namespace ledger. C7: atomic.Pointer eliminates the unsynchronized
	// late-assignment; Store runs before bootstrap.Run, Load fires post-Init.
	var acAtomicPtr atomic.Pointer[accesscore.AccessCore]
	bootstrapAuthObserver := auth.BootstrapAuthFailObserver(func(ctx context.Context, reason string) {
		ip, _ := ctxkeys.RealIPFrom(ctx)
		ac := acAtomicPtr.Load()
		if ac == nil {
			return
		}
		appendCtx, cancel := ctxutil.WithDetachedTimeout(ctx, testtime.D2s)
		defer cancel()
		_ = ac.RecordBootstrapAuthFail(appendCtx, reason, ip)
	})

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(setupTestBootstrapUsername),
			Password: []byte(setupTestBootstrapPassword),
		},
		setupTestAllowAllLimiter{},
		bootstrapAuthObserver,
	)
	ac := accesscore.NewAccessCore(clock.Real(), append(buildAccessCoreMemOptions(t, clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),

		accesscore.WithCASProtocol(mustNewCASProtocol(t, accesscore.PasswordVersionField)),
	)...)
	acAtomicPtr.Store(ac)
	cc := configcore.NewConfigCore(clock.Real(),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		configcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),

		configcore.WithCASProtocol(mustNewCASProtocol(t, configcore.VersionField)),
	)
	auc := auditcore.NewAuditCore(clock.Real(),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		auditcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
		auditcore.WithLedgerProtocol(auditProto),
		auditcore.WithLedgerStore(auditStore),
		auditcore.WithQueryStore(multiStore),
		auditcore.WithBootstrapStore(bootstrapWrapped),
	)

	asm := assembly.New(clock.Real(), assembly.Config{ID: "setup-test", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(clock.Real(),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(), []kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)}, bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithListener(cell.HealthListener, healthLn.Addr().String(), []kauth.ListenerAuth{kauth.AuthNone{}},
			bootstrap.WithListenerNet(healthLn)),
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
	testwait.External(t, "corebundle-setup-completed", func() bool {
		resp, err := setupHTTPClient.Get(fmt.Sprintf("http://%s/healthz", healthLn.Addr().String()))
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
	//     M4: also asserts the audit hash-chain captures reason=missing_header
	//     (BOOTSTRAP-AUDIT-CHAIN-WIRING-01, plan 039 W1-2). Before the funnel
	//     wiring this test passed nil observer and the 401 path silently
	//     dropped on the floor.
	t.Run("create_admin_no_auth_returns_401_writes_audit_chain", func(t *testing.T) {
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

		// #1423: the bootstrap auth-fail audit entry is now written asynchronously
		// (observer emits event.auth.bootstrap-failed.v1 → auditcore subscriber
		// writes the bootstrap-namespace ledger), so poll until it lands.
		testwait.External(t, "bootstrap-missing-header-audit", func() bool {
			es, qerr := multiStore.Query(context.Background(),
				ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
				query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
			return qerr == nil && len(es) >= 1
		}, testtime.EventuallyDefault, testtime.MediumPoll,
			"401 missing-header path must write a bootstrap.auth.fail ledger entry (async)")
		entries, err := multiStore.Query(context.Background(),
			ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
			query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(entries), 1)
		var payloadStruct struct {
			Reason   string `json:"reason"`
			ClientIP string `json:"clientIp"`
		}
		require.NoError(t, json.Unmarshal(entries[0].Payload, &payloadStruct))
		assert.Equal(t, "missing_header", payloadStruct.Reason,
			"first failure (no Basic Auth) must record reason=missing_header")
	})

	// 2b. POST with wrong credentials must 401 — same wire shape but observer
	//     records reason=wrong_credentials. Adds the second hash-chain entry,
	//     proving each rejected attempt is independently captured.
	t.Run("create_admin_wrong_password_returns_401_writes_audit_chain", func(t *testing.T) {
		payload := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			base+"/api/v1/access/setup/admin", strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(setupTestBootstrapUsername, "wrong-password-not-the-real-one")
		resp, err := setupHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"setup/admin with wrong Basic Auth password must 401")

		// #1423: async delivery — poll until both failure events have been
		// consumed and appended to the chain.
		testwait.External(t, "bootstrap-wrong-creds-audit", func() bool {
			es, qerr := multiStore.Query(context.Background(),
				ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
				query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
			return qerr == nil && len(es) >= 2
		}, testtime.EventuallyDefault, testtime.MediumPoll,
			"wrong-credentials path must add a second bootstrap.auth.fail entry (async)")
		entries, err := multiStore.Query(context.Background(),
			ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
			query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(entries), 2)
		// Scan reasons regardless of result ordering — the contract is that BOTH
		// reasons appear in the chain at this point, not the ordering itself.
		var seen []string
		for _, e := range entries {
			var p struct {
				Reason string `json:"reason"`
			}
			require.NoError(t, json.Unmarshal(e.Payload, &p))
			seen = append(seen, p.Reason)
		}
		assert.Contains(t, seen, "missing_header",
			"first failure (no Basic Auth) must remain in the chain")
		assert.Contains(t, seen, "wrong_credentials",
			"second failure (wrong Basic Auth) must record reason=wrong_credentials")
	})

	// 2c. Create first admin (with correct Basic Auth).
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

// TestSetupAdminBootstrap_RateLimited_Returns429AndWritesAuditChain verifies
// the rate-limit path end-to-end: the 4th POST returns 429 + Retry-After AND
// the bootstrap auth-fail observer writes a "bootstrap.auth.fail" entry
// into the auditcore ledger (BOOTSTRAP-AUDIT-CHAIN-WIRING-01, plan 039 W1-2).
// The capacity=2 limiter keeps the test fast; the assertion on
// ledger.Query closes the F7-RED gap from the pre-PR shape where the rate-
// limited path silently dropped on the floor.
func TestSetupAdminBootstrap_RateLimited_Returns429AndWritesAuditChain(t *testing.T) {
	const capacity = 2

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	healthLn := newCorebundleLocalListener(t)

	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(clock.Real())
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)

	// Build the audit ledger protocol + store inline so the test holds a
	// reference to the store for the post-fact Query assertion. This replaces
	// the auditcoreLedgerOpts(...) helper which hides the store inside the
	// returned Option slice.
	auditProtocol := buildTestAuditProtocol(t, []byte("test-hmac-key-32-bytes-long!!!!!"))
	auditStore := buildTestAuditStore(t, auditProtocol)
	// Bootstrap chain (namespace="bootstrap"), physically isolated per #1121.
	bootstrapRaw, bootstrapWrapped := buildTestBootstrapAuditChain(t,
		[]byte("test-bootstrap-hmac-key-32bytes!"))
	multiStore, err := ledger.NewMultiStore(auditStore, bootstrapRaw)
	require.NoError(t, err)

	// Wave-1 #1423: event-based observer (see TestSetupEndpoints_FirstRunFlow).
	// C7: atomic.Pointer eliminates the unsynchronized late-assignment.
	var acAtomicPtr2 atomic.Pointer[accesscore.AccessCore]
	bootstrapObserver := auth.BootstrapAuthFailObserver(func(ctx context.Context, reason string) {
		ip, _ := ctxkeys.RealIPFrom(ctx)
		ac := acAtomicPtr2.Load()
		if ac == nil {
			return
		}
		appendCtx, cancel := ctxutil.WithDetachedTimeout(ctx, testtime.D2s)
		defer cancel()
		_ = ac.RecordBootstrapAuthFail(appendCtx, reason, ip)
	})

	limiter := &setupTestBlockAfterNLimiter{remaining: capacity}
	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(setupTestBootstrapUsername),
			Password: []byte(setupTestBootstrapPassword),
		},
		limiter,
		bootstrapObserver,
	)

	ac := accesscore.NewAccessCore(clock.Real(), append(buildAccessCoreMemOptions(t, clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(bootstrapMW),

		accesscore.WithCASProtocol(mustNewCASProtocol(t, accesscore.PasswordVersionField)),
	)...)
	acAtomicPtr2.Store(ac)
	cc := configcore.NewConfigCore(clock.Real(),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		configcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),

		configcore.WithCASProtocol(mustNewCASProtocol(t, configcore.VersionField)),
	)
	auc := auditcore.NewAuditCore(clock.Real(),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		auditcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
		auditcore.WithLedgerProtocol(auditProtocol),
		auditcore.WithLedgerStore(auditStore),
		auditcore.WithQueryStore(multiStore),
		auditcore.WithBootstrapStore(bootstrapWrapped),
	)

	asm := assembly.New(clock.Real(), assembly.Config{ID: "ratelimit-test", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(clock.Real(),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(), []kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)}, bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithListener(cell.HealthListener, healthLn.Addr().String(), []kauth.ListenerAuth{kauth.AuthNone{}},
			bootstrap.WithListenerNet(healthLn)),
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
	testwait.External(t, "corebundle-setup-completed", func() bool {
		resp, err := setupHTTPClient.Get(fmt.Sprintf("http://%s/healthz", healthLn.Addr().String()))
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

	// #1423: the rate-limited path emits event.auth.bootstrap-failed.v1; auditcore
	// subscribes and appends to the bootstrap-namespace ledger asynchronously, so
	// poll until it lands. The two prior (authenticated) requests pass bootstrap
	// auth and produce no auth-fail event, so exactly one entry is expected.
	testwait.External(t, "bootstrap-ratelimited-audit", func() bool {
		es, qe := multiStore.Query(context.Background(),
			ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
			query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
		return qe == nil && len(es) >= 1
	}, testtime.EventuallyDefault, testtime.MediumPoll,
		"rate-limited path must write a bootstrap.auth.fail ledger entry (async)")
	entries, qerr := multiStore.Query(context.Background(),
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort()})
	require.NoError(t, qerr)
	// F24: require.Len (exactly 1) is safe here because the test sends a single
	// rate-limited request. The two prior requests passed Bootstrap auth and
	// produced no auth-fail event. Serial execution means exactly 1 entry.
	require.Len(t, entries, 1, "exactly one rate_limited entry expected")

	var payload struct {
		Reason   string `json:"reason"`
		ClientIP string `json:"clientIp"`
	}
	require.NoError(t, json.Unmarshal(entries[0].Payload, &payload))
	assert.Equal(t, "rate_limited", payload.Reason)
	assert.Equal(t, "system:bootstrap", entries[0].ActorID)
}
