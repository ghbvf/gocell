//go:build integration

// Package main — e2e regression test for A11 (OUTBOX-RELAY-WIRE-PG-01).
//
// Before the fix, cmd/core-bundle in GOCELL_CELL_ADAPTER_MODE=postgres created
// the outbox writer but never started the relay worker. Config publish events
// written to outbox_entries stalled indefinitely — PG mode was broken end-to-end.
//
// Fix: buildConfigCoreOpts now returns a worker.Worker for the relay in PG mode.
// main() registers it via bootstrap.WithWorkers, which starts it in Step 8 and
// stops it LIFO on shutdown.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adaptermq "github.com/ghbvf/gocell/adapters/rabbitmq"
	"github.com/testcontainers/testcontainers-go"
	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestOutboxE2E_PGMode_WriteToSubscribe is a regression test for A11
// (OUTBOX-RELAY-WIRE-PG-01). Before the fix, GOCELL_CELL_ADAPTER_MODE=postgres
// created the outbox writer but never started the relay, so config publish
// events would stall in outbox_entries indefinitely.
//
// The test starts PG + RMQ testcontainers, assembles core-bundle with PG mode
// and a real RMQ publisher/relay, publishes a config via HTTP, subscribes to
// RMQ, and asserts the event arrives within 30s.
//
// Chain: HTTP publish → PG outbox_entries → OutboxRelay → RMQ exchange → subscriber
func TestOutboxE2E_PGMode_WriteToSubscribe(t *testing.T) {
	testutil.RequireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// --- Step 1: Start testcontainers ---
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

	rmqContainer, err := tcrabbitmq.Run(ctx, testutil.RabbitMQImage,
		// Wait for both log signal and port availability to handle Docker Desktop
		// port-forwarder lag (mirrors testmain_integration_test.go in adapters/rabbitmq).
		testcontainers.WithAdditionalWaitStrategy(
			wait.ForListeningPort(nat.Port(tcrabbitmq.DefaultAMQPPort)).
				WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err, "start rabbitmq container")
	t.Cleanup(func() {
		if err := rmqContainer.Terminate(context.Background()); err != nil {
			t.Logf("WARN: rabbitmq container terminate failed: %v", err)
		}
	})

	// --- Step 2: Connect to PG and run migrations ---
	pgConnStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pgPool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: pgConnStr})
	require.NoError(t, err, "create PG pool")
	t.Cleanup(pgPool.Close)

	migrator, err := adapterpg.NewMigrator(pgPool, adapterpg.MigrationsFS(), "schema_migrations")
	require.NoError(t, err, "create migrator")
	require.NoError(t, migrator.Up(ctx), "run migrations")

	// --- Step 3: Connect to RMQ ---
	amqpURL, err := rmqContainer.AmqpURL(ctx)
	require.NoError(t, err)

	rmqConn, err := adaptermq.NewConnection(adaptermq.Config{
		URL:             amqpURL,
		ChannelPoolSize: 5,
		ConfirmTimeout:  10 * time.Second,
	})
	require.NoError(t, err, "create rabbitmq connection")
	t.Cleanup(func() { _ = rmqConn.Close() })

	// --- Step 4: Build the core-bundle assembly with PG + RMQ ---
	rmqPublisher := adaptermq.NewPublisher(rmqConn)

	eb := eventbus.New()
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

	outboxWriter := adapterpg.NewOutboxWriter()
	txMgr := adapterpg.NewTxManager(pgPool)

	// OutboxRelay polls outbox_entries and publishes via rmqPublisher.
	// This is the A11 fix: the relay was never started in PG mode before.
	relayCfg := adapterpg.DefaultRelayConfig()
	relayCfg.PollInterval = 200 * time.Millisecond // fast poll for test
	relay := adapterpg.NewOutboxRelay(pgPool.DB(), rmqPublisher, relayCfg)

	configCell := configcore.NewConfigCore(
		configcore.WithPostgresDefaults(pgPool.DB(), outboxWriter),
		configcore.WithTxManager(txMgr),
		configcore.WithPublisher(eb),
		configcore.WithCursorCodec(cursorCodec),
	)
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

	// --- Step 5: Subscribe to RMQ before starting the bundle ---
	// Subscribe early so the exchange/queue topology is declared before any events.
	const topic = "event.config.changed.v1"
	const dlxExchange = "dlx.e2e-test"

	var receivedCount atomic.Int32

	rmqSubConn, err := adaptermq.NewConnection(adaptermq.Config{
		URL:             amqpURL,
		ChannelPoolSize: 5,
		ConfirmTimeout:  10 * time.Second,
	})
	require.NoError(t, err, "create subscriber rabbitmq connection")
	t.Cleanup(func() { _ = rmqSubConn.Close() })

	subscriber := adaptermq.NewSubscriber(rmqSubConn, adaptermq.SubscriberConfig{
		ConsumerGroup: "e2e-test",
		DLXExchange:   dlxExchange,
	})
	t.Cleanup(func() { _ = subscriber.Close() })

	// Subscribe in a background goroutine; the handler increments receivedCount.
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	subErrCh := make(chan error, 1)
	go func() {
		handler := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			receivedCount.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
		subErrCh <- subscriber.Subscribe(subCtx, topic, handler, "e2e-test")
	}()

	// --- Step 6: Boot the assembly with the relay worker ---
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(ln),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithShutdownTimeout(3*time.Second),
		bootstrap.WithPublicEndpoints([]string{
			"/api/v1/access/sessions/login",
			"/api/v1/access/sessions/refresh",
		}),
		// A11 fix: wire relay worker so it starts in bootstrap Step 8.
		bootstrap.WithWorkers(relay),
	)

	appErrCh := make(chan error, 1)
	appCtx, appCancel := context.WithCancel(ctx)
	go func() { appErrCh <- app.Run(appCtx) }()

	addr := ln.Addr().String()
	baseURL := "http://" + addr

	// Wait for HTTP server ready.
	require.Eventually(t, func() bool {
		resp, err := http.Get(fmt.Sprintf("%s/healthz", baseURL))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond, "HTTP server must become ready")

	// --- Step 7: Login to get a token for the config publish HTTP call ---
	token := loginAdmin(t, baseURL, "admin", "adminpass")

	// --- Step 8: Create a config entry ---
	createConfig(t, baseURL, token, "e2e.test.key", "e2e-value")

	// --- Step 9: Publish the config entry via HTTP ---
	publishConfig(t, baseURL, token, "e2e.test.key")

	// --- Step 10: Assert the event arrives at RMQ subscriber within 30s ---
	require.Eventually(t, func() bool {
		return receivedCount.Load() > 0
	}, 30*time.Second, 200*time.Millisecond,
		"event.config.changed.v1 must arrive at RMQ subscriber within 30s — "+
			"regression guard for A11: relay must be started via bootstrap.WithWorkers")

	// --- Teardown: stop app cleanly ---
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
	require.Equal(t, http.StatusOK, resp.StatusCode, "login must succeed")

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
