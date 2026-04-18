//go:build integration

// Package main — e2e regression test for A11 (OUTBOX-RELAY-WIRE-PG-01) + F1
// (PG→in-memory-eventbus envelope unwrap).
//
// Before A11, cmd/core-bundle in GOCELL_CELL_ADAPTER_MODE=postgres created the
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
//     (matching cmd/core-bundle/main.go:492).
//   - Subscription is registered on the same `eb` and asserts the received
//     Entry.Payload parses as a business event (action/key/value), which
//     requires the F1 envelope-unwrap fix to work.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// configChangedBusinessPayload is the business event shape that
// cells/config-core/slices/configsubscribe/service.go expects. If the relay's
// wire envelope reaches subscribers unwrapped (F1 bug), these fields will all
// be empty and the regression guard fires.
type configChangedBusinessPayload struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

// TestOutboxE2E_PGMode_WriteToSubscribe is the combined A11 + F1 regression
// guard. It exercises the SHIPPED production path: in-memory eventbus is the
// relay publisher (no RabbitMQ in core-bundle today), a subscriber on the
// same bus must see business payloads, and the bundle must start/stop via
// bootstrap.WithWorkers lifecycle.
//
// Chain under test: HTTP publish → config-core WriteService (L2) → outbox_entries
//                   → OutboxRelay.publishAll (envelope) → eventbus (unwrap via F1)
//                   → subscriber handler receives business payload.
func TestOutboxE2E_PGMode_WriteToSubscribe(t *testing.T) {
	testutil.RequireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// --- Step 1: Start PG testcontainer (RMQ is NOT used by core-bundle today) ---
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
	migrator, err := adapterpg.NewMigrator(migrationPool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err, "create migrator")
	require.NoError(t, migrator.Up(ctx), "run migrations")
	migrationPool.Close()

	// --- Step 3: Build production-shaped bundle: eb is the relay publisher ---
	eb := eventbus.New()

	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")
	t.Setenv("GOCELL_PG_DSN", pgConnStr)

	cellAdapterMode, cellAdapterOpts, pgPool, relayWorker, err :=
		buildConfigCoreOpts(ctx, eb, kernelmetrics.NopProvider{})
	require.NoError(t, err, "buildConfigCoreOpts must succeed in postgres mode")
	require.Equal(t, "postgres", cellAdapterMode, "cell adapter mode must be postgres")
	require.NotNil(t, relayWorker,
		"A11 regression guard: buildConfigCoreOpts MUST return a non-nil relay worker in PG mode")
	require.NotNil(t, pgPool, "PG pool must be returned in postgres mode")
	t.Cleanup(pgPool.Close)

	// --- Step 4: Subscribe on the same eb BEFORE starting the bundle ---
	// This is the F1 regression guard: if the bus forwards envelope-wrapped
	// bytes as-is, the business payload parse below gets empty fields.
	const topic = "event.config.changed.v1"

	type received struct {
		entry   outbox.Entry
		payload configChangedBusinessPayload
		parsed  bool
	}
	var (
		recvs  []received
		recvMu sync.Mutex
	)

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = eb.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: "e2e-test"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
			var p configChangedBusinessPayload
			err := json.Unmarshal(e.Payload, &p)
			recvMu.Lock()
			recvs = append(recvs, received{entry: e, payload: p, parsed: err == nil})
			recvMu.Unlock()
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
	}()
	// Give subscriber goroutine a moment to register before first publish.
	time.Sleep(50 * time.Millisecond)

	// --- Step 5: Assemble cells ---
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", 15*time.Minute)
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	cursorCodec, err := query.NewCursorCodec([]byte("test-config-cursor-key-32bytes!!"))
	require.NoError(t, err)
	auditCursorCodec, err := query.NewCursorCodec([]byte("test-audit-cursor-key-32-bytes!!"))
	require.NoError(t, err)

	configOpts := append([]configcore.Option{
		configcore.WithPublisher(eb),
		configcore.WithCursorCodec(cursorCodec),
	}, cellAdapterOpts...)
	configCell := configcore.NewConfigCore(configOpts...)
	accessCell := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithSeedAdmin("admin", "adminpass"),
	)
	auditCell := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(hmacKey),
		auditcore.WithCursorCodec(auditCursorCodec),
	)

	asm := assembly.New(assembly.Config{ID: "e2e-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(configCell))
	require.NoError(t, asm.Register(accessCell))
	require.NoError(t, asm.Register(auditCell))

	// --- Step 6: Boot the assembly with the relay worker ---
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(ln),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(3*time.Second),
		bootstrap.WithPublicEndpoints([]string{
			"POST /api/v1/access/sessions/login",
			"POST /api/v1/access/sessions/refresh",
		}),
		// A11 regression guard: relayWorker came from buildConfigCoreOpts above —
		// not from a manual adapterpg.NewOutboxRelay call. If the production
		// wiring stops producing a relay worker, require.NotNil above fires.
		bootstrap.WithWorkers(relayWorker),
	)

	appErrCh := make(chan error, 1)
	appCtx, appCancel := context.WithCancel(ctx)
	go func() { appErrCh <- app.Run(appCtx) }()

	addr := ln.Addr().String()
	baseURL := "http://" + addr

	require.Eventually(t, func() bool {
		resp, err := http.Get(fmt.Sprintf("%s/healthz", baseURL))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond, "HTTP server must become ready")

	// --- Step 7: Drive HTTP requests ---
	token := loginAdmin(t, baseURL, "admin", "adminpass")
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
				(r.payload.Action == "created" || r.payload.Action == "updated" ||
					r.payload.Action == "published") {
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond,
		"A11+F1 regression guard: business payload with action/key must reach subscriber; "+
			"empty Action or missing Key indicates relay→eventbus envelope was not unwrapped")

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
	case <-time.After(10 * time.Second):
		t.Error("bootstrap did not shut down in time")
	}
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
	require.Equal(t, http.StatusOK, resp.StatusCode, "config publish must return 200")
}
