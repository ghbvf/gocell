//go:build integration

package integration

// TestInternalRPC_CallerCell_* tests validate the end-to-end service-token
// caller-cell identity enforcement on the InternalListener for the configread
// internal endpoint (GET /internal/v1/config/{key}).
//
// The chain under test:
//
//	ServiceTokenMiddleware (HMAC verification + replay guard)
//	→ RequireCallerCell policy (auto-enforced via ContractSpec.Clients)
//	→ configread.Handler.HandleGet
//
// Setup uses a real bootstrap (configcore + accesscore + auditcore) with
// in-memory storage, a fresh HMACKeyRing, and InMemoryNonceStore — no Docker
// required. Each top-level test boots its own app instance to keep NonceStores
// hermetic (replay-protection state does not leak between tests).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	accessmem "github.com/ghbvf/gocell/cells/accesscore/mem"
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
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/keystest"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// callerCellHTTPClient is a shared HTTP client for caller-cell integration tests.
var callerCellHTTPClient = &http.Client{Timeout: testtime.D2s}

// callerCellNoopTxRunner executes fn directly without a real transaction.
// Satisfies persistence.TxRunner for in-memory/demo-mode tests.
type callerCellNoopTxRunner struct{}

func (callerCellNoopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = callerCellNoopTxRunner{}

// callerCellAllowAllLimiter satisfies auth.BootstrapRateLimiter for test
// assemblies that don't exercise rate-limiter behavior. Required because
// auth.bootstrap:true contracts (setup/admin) demand a non-nil
// WithBootstrapAuth wiring at composition time.
type callerCellAllowAllLimiter struct{}

func (callerCellAllowAllLimiter) Allow(string) bool { return true }

// freshCallerCellSecret returns a cryptographically random 32-byte test secret
// (prefixed with "ts-" to distinguish it from demo keys that loadKeySet rejects).
func freshCallerCellSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "ts-" + hex.EncodeToString(b)
}

// callerCellApp holds the running test app state for caller-cell E2E tests.
type callerCellApp struct {
	internalAddr string
	ring         *auth.HMACKeyRing
}

// startCallerCellApp boots a minimal corebundle with an InternalListener
// protected by ServiceTokenMiddleware. Returns the app state and a cancel
// function. The bootstrap is shut down via t.Cleanup.
func startCallerCellApp(t *testing.T) *callerCellApp {
	t.Helper()

	secret := freshCallerCellSecret(t)
	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	require.NoError(t, err)

	nonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	primaryLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = primaryLn.Close() })

	internalLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = internalLn.Close() })

	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "caller-cell-test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(),
		auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec([]byte("callercell-audit-key-32-bytes!!!"))
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec([]byte("callercell-cfg-key---32-bytes-x!!"))
	require.NoError(t, err)
	accessCursorCodec, err := query.NewCursorCodec([]byte("callercell-acc-key---32-bytes-y!!"))
	require.NoError(t, err)

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte("caller-cell-operator"),
			Password: []byte("caller-cell-op-pass!"),
		},
		callerCellAllowAllLimiter{},
		nil,
	)
	acMemStore := accessmem.NewStore(clock.Real())
	acSessionProto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	require.NoError(t, err)
	acSessionStore, err := session.NewMemStore(acSessionProto, clock.Real())
	require.NoError(t, err)
	acRefreshStore, err := refreshmem.New(accesscore.DefaultRefreshPolicy(), clock.Real(), nil)
	require.NoError(t, err)
	ac := accesscore.NewAccessCore(
		accesscore.WithClock(clock.Real()),
		accesscore.WithUserRepository(acMemStore.UserRepository()),
		accesscore.WithRoleRepository(acMemStore.RoleRepository()),
		accesscore.WithSessionStore(acSessionStore),
		accesscore.WithRefreshStore(acRefreshStore),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(persistence.WrapForCell(callerCellNoopTxRunner{})),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithCursorCodec(accessCursorCodec),
		accesscore.WithBootstrapAuth(bootstrapMW),

		accesscore.WithCASProtocol(mustNewCASProtocol(t, accesscore.PasswordVersionField)),
	)
	cc := configcore.NewConfigCore(
		configcore.WithClock(clock.Real()),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		configcore.WithTxManager(persistence.WrapForCell(callerCellNoopTxRunner{})),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),

		configcore.WithCASProtocol(mustNewCASProtocol(t, configcore.VersionField)),
	)
	callerCellAuditNS, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	callerCellAuditProto, err := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("callercell-hmac-key-32-bytes!!!!!")),
		ledger.WithNamespace(callerCellAuditNS),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	callerCellAuditStore, err := ledger.NewMemStore(callerCellAuditProto, clock.Real())
	require.NoError(t, err)
	auc := auditcore.NewAuditCore(
		auditcore.WithClock(clock.Real()),
		auditcore.WithLedgerProtocol(callerCellAuditProto),
		auditcore.WithLedgerStore(callerCellAuditStore),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		auditcore.WithTxManager(persistence.WrapForCell(callerCellNoopTxRunner{})),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	asm := assembly.New(assembly.Config{
		ID:             "caller-cell-test",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(
			cell.PrimaryListener,
			primaryLn.Addr().String(),
			[]cell.ListenerAuth{celltest.MustAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(primaryLn),
		),
		bootstrap.WithListener(
			cell.InternalListener,
			internalLn.Addr().String(),
			[]cell.ListenerAuth{celltest.MustAuthServiceToken(nonceStore, ring)},
			bootstrap.WithListenerNet(internalLn),
		),
		bootstrap.WithPublisher(eb),
		bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newIntegrationTestConsumerBase(t, clock.Real())),
		bootstrap.WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testtime.SelectShutdown):
			t.Log("warning: caller-cell bootstrap did not shut down in time")
		}
	})

	// Wait for the primary listener to serve /healthz (signals full bootstrap).
	primaryAddr := primaryLn.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := callerCellHTTPClient.Get(fmt.Sprintf("http://%s/healthz", primaryAddr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "caller-cell bootstrap did not become ready")

	return &callerCellApp{
		internalAddr: internalLn.Addr().String(),
		ring:         ring,
	}
}

// TestInternalRPC_AccessCoreCallsConfigRead_GuardPassed_404KeyNotFound verifies
// that a service token carrying callerCell="accesscore" (which is declared in
// ContractSpec.Clients for GET /internal/v1/config/{key}) passes the
// ServiceTokenMiddleware guard and RequireCallerCell policy, reaching the
// handler. The handler returns 404 (key not seeded) which proves both guards
// passed — neither short-circuited to 401 or 403.
func TestInternalRPC_AccessCoreCallsConfigRead_GuardPassed_404KeyNotFound(t *testing.T) {
	app := startCallerCellApp(t)

	token := auth.GenerateServiceToken(app.ring, "accesscore",
		http.MethodGet, "/internal/v1/config/no-such-key", "", time.Now())
	require.NotEmpty(t, token, "token generation must succeed for a valid callerCell")

	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://%s/internal/v1/config/no-such-key", app.internalAddr), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "ServiceToken "+token)

	resp, err := callerCellHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 404 = guard + policy passed, handler replied "key not found".
	// 401 or 403 would mean a guard/policy rejected the request.
	assert.Equal(t, http.StatusNotFound, resp.StatusCode,
		"accesscore caller must pass guard+RequireCallerCell; handler returns 404 (key not seeded)")
}

// TestInternalRPC_ConfigCoreCallsConfigRead_Denied_403 verifies that a service
// token carrying callerCell="configcore" is rejected by RequireCallerCell with
// 403 because "configcore" is not in contract.clients=["accesscore"].
func TestInternalRPC_ConfigCoreCallsConfigRead_Denied_403(t *testing.T) {
	app := startCallerCellApp(t)

	token := auth.GenerateServiceToken(app.ring, "configcore",
		http.MethodGet, "/internal/v1/config/any-key", "", time.Now())
	require.NotEmpty(t, token)

	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://%s/internal/v1/config/any-key", app.internalAddr), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "ServiceToken "+token)

	resp, err := callerCellHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 403 = HMAC valid, but RequireCallerCell rejects "configcore" not in allowlist.
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"configcore is not in contract.clients=[accesscore]; RequireCallerCell must return 403")
	assertErrCode(t, resp, "ERR_AUTH_FORBIDDEN")
}

// TestInternalRPC_WrongEndpointForCaller_403 verifies that a caller with a
// valid HMAC service token (callerCell="auditcore") is rejected by
// RequireCallerCell with 403 because "auditcore" is not in
// contract.clients=["accesscore"] for GET /internal/v1/config/{key}.
func TestInternalRPC_WrongEndpointForCaller_403(t *testing.T) {
	app := startCallerCellApp(t)

	// auditcore is a valid cell ID and passes HMAC + format checks,
	// but it is not in the configread contract.clients allowlist.
	token := auth.GenerateServiceToken(app.ring, "auditcore",
		http.MethodGet, "/internal/v1/config/any-key", "", time.Now())
	require.NotEmpty(t, token)

	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://%s/internal/v1/config/any-key", app.internalAddr), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "ServiceToken "+token)

	resp, err := callerCellHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"auditcore is not in configread contract.clients; RequireCallerCell must return 403")
	assertErrCode(t, resp, "ERR_AUTH_FORBIDDEN")
}

// TestInternalRPC_EmptyCallerCellRejected verifies that a 4-part token with an
// empty callerCell segment is rejected by ServiceTokenMiddleware with 401.
//
// GenerateServiceToken returns "" for empty callerCell, so we craft the token
// manually: ts:nonce::deadbeefMAC — the empty callerCell segment triggers
// "caller cell missing" in verifyServiceTokenPayload.
func TestInternalRPC_EmptyCallerCellRejected(t *testing.T) {
	app := startCallerCellApp(t)

	// Craft a syntactically valid 4-part token with an empty callerCell segment.
	// The MAC is intentionally wrong (deadbeef) — the parser first checks format,
	// then callerCell presence before MAC; either way the result must be 401.
	ts := fmt.Sprintf("%d", time.Now().Unix())
	nonceBytes := make([]byte, 16)
	_, err := rand.Read(nonceBytes)
	require.NoError(t, err)
	nonce := hex.EncodeToString(nonceBytes)
	// ts:nonce::deadbeef... — callerCell segment is empty.
	fakeMAC := strings.Repeat("ab", 32) // 64 hex chars for plausible HMAC-SHA256 len
	crafted := ts + ":" + nonce + ":" + "" + ":" + fakeMAC

	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://%s/internal/v1/config/any-key", app.internalAddr), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "ServiceToken "+crafted)

	resp, err := callerCellHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"token with empty callerCell segment must be rejected with 401")
	assertErrCode(t, resp, "ERR_AUTH_UNAUTHORIZED")
}

// TestInternalRPC_TamperedCallerCellRejected verifies that a valid token for
// callerCell="accesscore" whose callerCell segment has been replaced with
// "configcore" (while keeping the original MAC) is rejected with 401.
//
// The HMAC covers the callerCell field in buildServiceTokenMessage, so any
// substitution invalidates the signature.
func TestInternalRPC_TamperedCallerCellRejected(t *testing.T) {
	app := startCallerCellApp(t)

	// Generate a legitimate token for "accesscore".
	original := auth.GenerateServiceToken(app.ring, "accesscore",
		http.MethodGet, "/internal/v1/config/any-key", "", time.Now())
	require.NotEmpty(t, original)

	// Token format: ts:nonce:callerCell:mac — 4 colon-separated segments.
	// SplitN with n=4 handles callerCell values that contain no colons safely.
	parts := strings.SplitN(original, ":", 4)
	require.Len(t, parts, 4, "generated token must have 4 colon-separated parts")

	// Replace the callerCell segment (index 2) with "configcore".
	// The original MAC was computed with "accesscore" as the callerCell;
	// changing it here produces a MAC mismatch → 401 from HMAC verification.
	parts[2] = "configcore"
	tampered := strings.Join(parts, ":")

	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://%s/internal/v1/config/any-key", app.internalAddr), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "ServiceToken "+tampered)

	resp, err := callerCellHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"tampered callerCell segment invalidates the HMAC; ServiceTokenMiddleware must return 401")
}

// assertErrCode reads the response body and asserts that the JSON error
// envelope contains the expected errcode value in error.code.
func assertErrCode(t *testing.T, resp *http.Response, wantCode string) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope),
		"response body must be JSON error envelope: %s", string(body))
	assert.Equal(t, wantCode, envelope.Error.Code,
		"error.code mismatch in response body: %s", string(body))
}

func mustNewCASProtocol(t *testing.T, versionField string) *cas.Protocol {
	t.Helper()
	p, err := cas.NewProtocol(cas.WithVersionField(versionField))
	if err != nil {
		t.Fatalf("cas.NewProtocol: %v", err)
	}
	return p
}
