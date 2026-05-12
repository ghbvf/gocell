//go:build integration

// Package main — e2e regression test for A11 (OUTBOX-RELAY-WIRE-PG-01) + F1
// (PG→in-memory-eventbus envelope unwrap).
//
// Before A11, cmd/corebundle in GOCELL_CELL_ADAPTER_MODE=postgres created the
// outbox writer but never started the relay worker. Config publish events
// written to outbox_entries stalled indefinitely — PG mode was broken.
//
// Before F1, the relay wrapped payloads in an outboxMessage envelope and
// pushed them to the in-memory eventbus which did NOT unwrap; subscribers
// parsed the envelope as business payload, saw empty Action, and silently
// ACKed unknown-action events. The first version of this test accidentally
// bypassed the bug by injecting a RabbitMQ publisher (which DOES unwrap),
// so the test passed while production was broken.
//
// Current form exercises the REAL production path:
//   - Publisher passed to buildConfigCoreOpts is the in-memory eventbus `eb`
//     (matching cmd/corebundle/main.go:492).
//   - Subscription is registered on the same `eb` and asserts the received
//     Entry.Payload parses as a business event (action/key/version), which
//     requires the F1 envelope-unwrap fix to work.
//
// PR-CFG-B metadata-only model: event.config.entry-upserted.v1 payload carries
// only key+version (no value field). The A11+F1 regression guard still validates
// the full envelope-unwrap path; only the payload field set has changed.
// Subscribers MUST refetch via GET /api/v1/config/{key} to obtain the value.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/cells/accesscore/configgetter"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/state/cas"
	"github.com/ghbvf/gocell/tests/testutil"
)

const (
	// d90s is used as the outer e2e test timeout; not in the testtime table.
	d90s = 90 * time.Second
)

// configEntryUpsertedBusinessPayload is the business event shape that
// cells/configcore/slices/configsubscribe/service.go expects on
// event.config.entry-upserted.v1. If the relay's wire envelope reaches
// subscribers unwrapped (F1 bug), these fields will all be empty and the
// regression guard fires.
//
// PR-CFG-B metadata-only model: only key+version are present; value is omitted.
type configEntryUpsertedBusinessPayload struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
}

// TestOutboxE2E_PGMode_WriteToSubscribe is the combined A11 + F1 regression
// guard. It exercises the SHIPPED production path: in-memory eventbus is the
// relay publisher (no RabbitMQ in corebundle today), a subscriber on the
// same bus must see business payloads, and the bundle must start/stop via
// bootstrap.WithWorkers lifecycle.
//
// Chain under test: HTTP publish → configcore WriteService (L2) → outbox_entries
//
//	→ OutboxRelay.publishAll (envelope) → eventbus (unwrap via F1)
//	→ subscriber handler receives business payload.
func TestOutboxE2E_PGMode_WriteToSubscribe(t *testing.T) {
	testutil.RequireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), d90s)
	defer cancel()

	// --- Step 1: Start PG testcontainer (RMQ is NOT used by corebundle today) ---
	pgContainer, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(func() {
		if err := pgContainer.Terminate(context.Background()); err != nil {
			t.Logf("WARN: postgres container terminate failed: %v", err)
		}
	})

	// --- Step 2: Apply migrations via a short-lived pool ---
	pgConnStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	migrationPool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: pgConnStr})
	require.NoError(t, err, "create migration PG pool")
	migrator, err := adapterpg.NewMigrator(migrationPool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err, "create migrator")
	require.NoError(t, migrator.Up(ctx), "run migrations")
	_ = migrationPool.Close(ctx)

	// --- Step 3: Build production-shaped bundle: eb is the relay publisher ---
	eb := eventbus.New(eventbus.WithClock(clock.Real()))

	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")

	modResult, err := buildConfigCoreOpts(ctx, ConfigCoreModuleConfig{
		Topology:         bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
		PGConfig:         adapterpg.Config{DSN: pgConnStr},
		Publisher:        eb,
		MetricsProvider:  kernelmetrics.NopProvider{},
		ValueTransformer: crypto.NoopTransformer{},
		Clock:            clock.Real(),
	})
	require.NoError(t, err, "buildConfigCoreOpts must succeed in postgres mode")
	pgRes := modResult.PGResource
	cellAdapterOpts := modResult.CellOptions
	relayBootstrapOpts := modResult.BootstrapOpts
	require.NotNil(t, pgRes,
		"A11 regression guard: buildConfigCoreOpts MUST return a non-nil ManagedResource in PG mode")
	// Relay is now registered via independent bootstrap opts, not via PGResource.Worker().
	require.NotEmpty(t, relayBootstrapOpts,
		"A11 regression guard: bootstrapOpts MUST carry relay ManagedResource in PG mode")
	t.Cleanup(func() { _ = pgRes.Close(context.Background()) })

	// --- Step 4: Subscribe on the same eb BEFORE starting the bundle ---
	// This is the F1 regression guard: if the bus forwards envelope-wrapped
	// bytes as-is, the business payload parse below gets empty fields.
	const topic = "event.config.entry-upserted.v1"

	type received struct {
		entry   outbox.Entry
		payload configEntryUpsertedBusinessPayload
		parsed  bool
	}
	var (
		recvs  []received
		recvMu sync.Mutex
	)

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = eb.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "e2e-test", CellID: "e2e-test"}, entryToSubHandler(func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			var p configEntryUpsertedBusinessPayload
			err := json.Unmarshal(e.Payload, &p)
			recvMu.Lock()
			recvs = append(recvs, received{entry: e, payload: p, parsed: err == nil})
			recvMu.Unlock()
			return outbox.Ack()
		}))
	}()
	// Give subscriber goroutine a moment to register before first publish.
	time.Sleep(testtime.MediumPoll) //archtest:allow:test-sleep wait for goroutine to enter blocking Subscribe; no started observable

	// --- Step 5: Assemble cells ---
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	cursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)
	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)

	// cellAdapterOpts already includes WithOutboxDeps(eb, pgWriter) from
	// buildConfigCoreOpts — no separate publisher wiring needed.
	// Go prevents mixing positional args and slice spread, so WithClock is
	// prepended into the slice; the allow-marker below documents this.
	cellAdapterOpts = append([]configcore.Option{
		configcore.WithClock(clock.Real()),
		configcore.WithCursorCodec(cursorCodec),
		configcore.WithMetricsProvider(kernelmetrics.NopProvider{}),
	}, cellAdapterOpts...)
	configCell := configcore.NewConfigCore(cellAdapterOpts...) //archtest:allow:clock-injection:via-slice options slice starts with WithClock(clock.Real()) prepended above; Go prevents mixing positional + spread

	// Wire accesscore with WithBootstrapAuth. The operator calls POST /setup/admin
	// with Basic Auth to provision the first admin (interactive mode, ADR §D5).
	e2eBootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(e2eAdminUsername),
			Password: []byte(e2eAdminBootstrapPassword),
		},
		setupTestAllowAllLimiter{},
		nil,
	)
	accessCell := accesscore.NewAccessCore(append(buildAccessCoreMemOptions(t, clock.Real()),
		accesscore.WithClock(clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), nil),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithMetricsProvider(kernelmetrics.NopProvider{}),
		accesscore.WithBootstrapAuth(e2eBootstrapMW),

		accesscore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))),
	)...) //archtest:allow:clock-injection:via-slice buildAccessCoreMemOptions + WithClock prepended; spread prevents direct positional arg
	auditCell := auditcore.NewAuditCore(append([]auditcore.Option{
		auditcore.WithClock(clock.Real()),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), nil),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(kernelmetrics.NopProvider{}),
	}, auditcoreLedgerOpts(t, hmacKey)...)...) //archtest:allow:clock-injection:via-slice WithClock is in the first slice arg passed to append; spread prevents direct positional arg

	asm := assembly.New(assembly.Config{ID: "e2e-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(configCell))
	require.NoError(t, asm.Register(accessCell))
	require.NoError(t, asm.Register(auditCell))

	// --- Step 6: Boot the assembly with the relay worker ---
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	baseOpts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithClock(asm.Clock()),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}, bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, asm.Clock())),
		bootstrap.WithShutdownTimeout(testtime.EventuallyDefault),
		// F3: public routes (login, refresh) and PasswordResetExempt routes
		// (change-password, logout) are declared via auth.Mount inside accesscore's
		// RegisterRoutes. PolicyJWTFromAssembly discovers the verifier lazily.
	}
	// A11 regression guard: relay is registered via relayBootstrapOpts from
	// buildConfigCoreOpts so its Worker/Close/Checkers lifecycle is independently
	// managed by bootstrap — not carried inside PGResource.Worker().
	app := newBootstrapFromOptions(append(baseOpts, relayBootstrapOpts...))

	appErrCh := make(chan error, 1)
	appCtx, appCancel := context.WithCancel(ctx)
	go func() { appErrCh <- app.Run(appCtx) }()

	addr := ln.Addr().String()
	baseURL := "http://" + addr

	waitForHealthy(t, addr)

	// --- Step 7: Drive HTTP requests ---
	// Operator provisions the first admin via POST /setup/admin with Basic Auth
	// (interactive mode, ADR §D5). The admin password is set directly in the body,
	// so passwordResetRequired=false — no change-password detour needed.
	provisionE2EAdmin(t, baseURL, e2eAdminUsername, e2eAdminBootstrapPassword)
	token := loginAdmin(t, baseURL, e2eAdminUsername, e2eAdminBootstrapPassword)

	createConfig(t, baseURL, token, "e2e.test.key", "e2e-value")
	publishConfig(t, baseURL, token, "e2e.test.key")

	// --- Step 8: Assert subscriber received a PARSED business payload ---
	// This is the combined A11 + F1 regression guard. Failure modes:
	//   - No events at all → A11 regression (relay not started)
	//   - Events arrive but parsed == false OR all fields empty → F1 regression
	//     (envelope not unwrapped; subscriber sees envelope shape)
	require.Eventually(t, func() bool {
		recvMu.Lock()
		defer recvMu.Unlock()
		for _, r := range recvs {
			if r.parsed && r.payload.Key == "e2e.test.key" &&
				r.payload.Version >= 1 {
				return true
			}
		}
		return false
	}, testtime.CtxLong, testtime.D200ms,
		"A11+F1 regression guard: entry-upserted business payload with key/version must reach subscriber (PR-CFG-B metadata-only model); "+
			"missing fields indicate relay→eventbus envelope was not unwrapped")

	// Additional diagnostic: list what actually arrived in case the above fails.
	recvMu.Lock()
	for i, r := range recvs {
		t.Logf("recv[%d]: parsed=%v payload=%+v entry.EventType=%q entry.ID=%q",
			i, r.parsed, r.payload, r.entry.EventType, r.entry.ID)
	}
	recvMu.Unlock()

	// --- Teardown ---
	appCancel()
	select {
	case err := <-appErrCh:
		assert.NoError(t, err, "bundle must shut down without error")
	case <-time.After(testtime.SelectAsyncSettle):
		t.Error("bootstrap did not shut down in time")
	}
}

// provisionE2EAdmin calls POST /api/v1/access/setup/admin with Basic Auth using
// the e2e operator credentials. The admin identity (username + password) is
// set directly in the request body — passwordResetRequired=false (ADR §D5).
func provisionE2EAdmin(t *testing.T, baseURL, username, password string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"email":    username + "@e2e.local",
		"password": password,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/access/setup/admin", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"setup/admin must return 201 Created with valid Basic Auth")
}

// loginAdmin posts to /api/v1/access/sessions/login and returns the access token.
func loginAdmin(t *testing.T, baseURL, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := http.Post(baseURL+"/api/v1/access/sessions/login", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "login must succeed (201 Created per sessionlogin contract)")

	var result struct {
		Data struct {
			AccessToken string `json:"accessToken"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotEmpty(t, result.Data.AccessToken, "access token must be present")
	return result.Data.AccessToken
}

// e2eAdminUsername / e2eAdminBootstrapPassword are the operator credentials used
// as both the Basic Auth header on POST /setup/admin and the admin identity
// created in the request body.
const (
	e2eAdminUsername          = "e2e-admin"
	e2eAdminBootstrapPassword = "e2e-admin-pass-1!"
)

// createConfig creates a config entry via POST /api/v1/config/.
func createConfig(t *testing.T, baseURL, token, key, value string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"key": key, "value": value})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/config/", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "config create must return 201")
}

// publishConfig publishes a config entry via POST /api/v1/config/{key}/publish.
func publishConfig(t *testing.T, baseURL, token, key string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/config/"+key+"/publish", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "config publish must return 201 (creates new ConfigVersion)")
}

// TestOutboxE2E_RefetchLoop_AccessCoreCallsInternalGet validates the closed
// loop: configcore.configwrite → PG outbox → relay → eventbus →
// accesscore.configreceive → ConfigGetter.GetEntry (HTTP call to internal
// endpoint) succeeds.
//
// Chain under test:
//
//	HTTP publish → configcore WriteService (L2) → outbox_entries
//	→ OutboxRelay.publishAll → eventbus → configreceive handler
//	→ HTTPConfigGetter.GetEntry → stub internal server → assertion
//
// The stub internal server records every GET /internal/v1/config/{key} request
// so the test can assert the closed loop completed without needing slog
// interception. The service token is signed with a test HMAC ring; the stub
// server records the request path and responds 200 with a minimal config
// entry JSON, completing the round-trip.
//
// This test guards PR-CFG-G1 commit 4: the accesscore configreceive slice
// now calls back to configcore after an entry-upserted event, closing the
// metadata-only refetch loop (PR-CFG-B) end-to-end.
func TestOutboxE2E_RefetchLoop_AccessCoreCallsInternalGet(t *testing.T) {
	testutil.RequireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), d90s)
	defer cancel()

	// --- Step 1: Start PG testcontainer ---
	pgContainer, err := tcpostgres.Run(ctx, testutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(func() {
		if err := pgContainer.Terminate(context.Background()); err != nil {
			t.Logf("WARN: postgres container terminate failed: %v", err)
		}
	})

	// --- Step 2: Apply migrations ---
	pgConnStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	migrationPool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: pgConnStr})
	require.NoError(t, err, "create migration PG pool")
	migrator, err := adapterpg.NewMigrator(migrationPool, testAdapterMigrationsFS(t), "schema_migrations")
	require.NoError(t, err, "create migrator")
	require.NoError(t, migrator.Up(ctx), "run migrations")
	_ = migrationPool.Close(ctx)

	// --- Step 3: Build production-shaped bundle ---
	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")

	modResult, err := buildConfigCoreOpts(ctx, ConfigCoreModuleConfig{
		Topology:         bootstrap.Topology{StorageBackend: "postgres", AdapterMode: "real"},
		PGConfig:         adapterpg.Config{DSN: pgConnStr},
		Publisher:        eb,
		MetricsProvider:  kernelmetrics.NopProvider{},
		ValueTransformer: crypto.NoopTransformer{},
		Clock:            clock.Real(),
	})
	require.NoError(t, err, "buildConfigCoreOpts must succeed in postgres mode")
	pgRes := modResult.PGResource
	cellAdapterOpts := modResult.CellOptions
	relayBootstrapOpts := modResult.BootstrapOpts
	require.NotNil(t, pgRes, "PGResource must be non-nil in postgres mode")
	require.NotEmpty(t, relayBootstrapOpts, "relay bootstrap opts must be non-empty in postgres mode")
	t.Cleanup(func() { _ = pgRes.Close(context.Background()) })

	// --- Step 4: Stub internal server —
	// Simulates GET /internal/v1/config/{key} — the endpoint that
	// accesscore.configreceive calls after receiving an upsert event.
	// Records: path, method, Authorization header — assertions verify the
	// refetch HTTP call uses correct verb + carries a service-token (the
	// real listener auth chain rejects unauthenticated callers).
	type refetchCall struct {
		path       string
		method     string
		authHeader string
	}
	refetchCh := make(chan refetchCall, 8)
	internalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refetchCh <- refetchCall{
			path:       r.URL.Path,
			method:     r.Method,
			authHeader: r.Header.Get("Authorization"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Response matches contracts/http/config/internal/get/v1 response
		// schema: {data: {id, key, value, sensitive, version, createdAt, updatedAt}}.
		// Asserting the refetch consumer can decode the full contract payload
		// guards against stub/contract drift.
		_, _ = w.Write([]byte(`{"data":{"id":"cfg-refetch-test","key":"refetch.test.key","value":"refetch-value","sensitive":false,"version":1,"createdAt":"2026-04-26T00:00:00Z","updatedAt":"2026-04-26T00:00:00Z"}}`))
	}))
	t.Cleanup(internalSrv.Close)

	// Create a test HMAC ring for service-token signing in HTTPConfigGetter.
	// The stub server does not verify the token; it just records the call.
	testRing, ringErr := auth.NewHMACKeyRing(
		[]byte("test-service-secret-32-bytes-xxx!"),
		nil,
	)
	require.NoError(t, ringErr, "create test HMAC ring")

	// --- Step 5: Assemble cells ---
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	cursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)
	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)

	// Go prevents mixing positional args and slice spread, so WithClock is
	// prepended into the slice; the allow-marker below documents this.
	cellAdapterOpts = append([]configcore.Option{
		configcore.WithClock(clock.Real()),
		configcore.WithCursorCodec(cursorCodec),
		configcore.WithMetricsProvider(kernelmetrics.NopProvider{}),
	}, cellAdapterOpts...)
	configCell := configcore.NewConfigCore(cellAdapterOpts...) //archtest:allow:clock-injection:via-slice options slice starts with WithClock(clock.Real()) prepended above; Go prevents mixing positional + spread

	// Wire accesscore with the HTTPConfigGetter pointing at the stub server.
	// After receiving an entry-upserted event, configreceive will call
	// internalSrv.URL + /internal/v1/config/{key}, and the stub records it.
	refetchBootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(e2eAdminUsername),
			Password: []byte(e2eAdminBootstrapPassword),
		},
		setupTestAllowAllLimiter{},
		nil,
	)
	accessCell := accesscore.NewAccessCore(append(buildAccessCoreMemOptions(t, clock.Real()),
		accesscore.WithClock(clock.Real()),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), nil),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithMetricsProvider(kernelmetrics.NopProvider{}),
		accesscore.WithBootstrapAuth(refetchBootstrapMW),
		configgetter.WithHTTP(internalSrv.URL, testRing, clock.Real()),

		accesscore.WithCASProtocol(cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))),
	)...) //archtest:allow:clock-injection:via-slice buildAccessCoreMemOptions + WithClock prepended; spread prevents direct positional arg
	auditCell := auditcore.NewAuditCore(append([]auditcore.Option{
		auditcore.WithClock(clock.Real()),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), nil),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(kernelmetrics.NopProvider{}),
	}, auditcoreLedgerOpts(t, hmacKey)...)...) //archtest:allow:clock-injection:via-slice WithClock is in the first slice arg passed to append; spread prevents direct positional arg

	asm := assembly.New(assembly.Config{ID: "e2e-refetch-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(configCell))
	require.NoError(t, asm.Register(accessCell))
	require.NoError(t, asm.Register(auditCell))

	// --- Step 6: Boot the assembly ---
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	baseOpts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithClock(asm.Clock()),
		bootstrap.WithListener(cell.PrimaryListener, ln.Addr().String(), []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}, bootstrap.WithListenerNet(ln)),
		withCorebundleTestInternalListener(t, newCorebundleLocalListener(t)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newCorebundleTestConsumerBase(t, asm.Clock())),
		bootstrap.WithShutdownTimeout(testtime.EventuallyDefault),
	}
	app := newBootstrapFromOptions(append(baseOpts, relayBootstrapOpts...))

	appErrCh := make(chan error, 1)
	appCtx, appCancel := context.WithCancel(ctx)
	go func() { appErrCh <- app.Run(appCtx) }()

	addr := ln.Addr().String()
	baseURL := "http://" + addr
	waitForHealthy(t, addr)

	// --- Step 7: Authenticate as admin ---
	// Operator provisions the first admin via POST /setup/admin with Basic Auth
	// (interactive mode, ADR §D5). Login directly — no change-password needed.
	provisionE2EAdmin(t, baseURL, e2eAdminUsername, e2eAdminBootstrapPassword)
	token := loginAdmin(t, baseURL, e2eAdminUsername, e2eAdminBootstrapPassword)

	// --- Step 8: Publish a config entry — triggers the refetch closed loop ---
	const refetchKey = "refetch.test.key"
	createConfig(t, baseURL, token, refetchKey, "refetch-value")
	publishConfig(t, baseURL, token, refetchKey)

	// --- Step 9: Assert the closed loop completed ---
	// The relay delivers the entry-upserted event → configreceive calls
	// HTTPConfigGetter.GetEntry → stub server records the request.
	//
	// Captures the first call matching the expected path; subsequent
	// asserts validate semantics (method + auth header). Eventually waits
	// up to 15s for the relay→eventbus→configreceive→ConfigGetter pipeline.
	var captured refetchCall
	require.Eventually(t, func() bool {
		select {
		case call := <-refetchCh:
			if call.path == "/internal/v1/config/"+refetchKey {
				captured = call
				return true
			}
		default:
		}
		return false
	}, testtime.D15s, testtime.SlowPoll,
		"refetch closed loop: accesscore.configreceive must call GET /internal/v1/config/%s "+
			"within 15s of publish; missing call indicates relay→eventbus→configreceive→ConfigGetter "+
			"pipeline is broken (PR-CFG-G1 refetch loop guard)",
		refetchKey)
	// Method must be GET (HTTPConfigGetter.GetEntry uses http.MethodGet).
	assert.Equal(t, http.MethodGet, captured.method,
		"refetch must use GET — wrong method indicates HTTPConfigGetter regression")
	// Authorization header must carry a ServiceToken — the real internal
	// listener auth chain rejects requests without a service-token; the stub
	// does not verify the signature but asserting the header prefix catches
	// auth-chain regressions (e.g. ring not wired, token never minted).
	assert.True(t, strings.HasPrefix(captured.authHeader, "ServiceToken "),
		"refetch Authorization header must start with \"ServiceToken \"; got %q — "+
			"indicates HTTPConfigGetter is not signing requests with the configured ring",
		captured.authHeader)

	// --- Teardown ---
	appCancel()
	select {
	case err := <-appErrCh:
		assert.NoError(t, err, "bundle must shut down without error")
	case <-time.After(testtime.SelectAsyncSettle):
		t.Error("bootstrap did not shut down in time")
	}
}
