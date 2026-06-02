//go:build integration

package l2atomicity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/auth/authtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/cells/accesscore/accesscoretest"
	accesspg "github.com/ghbvf/gocell/cells/accesscore/postgres"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/keystest"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// Username + email fixtures are non-credential; passwords are runtime-generated
// (see below) so the source contains no committed credential literal — even an
// inadvertent copy-paste into a production env cannot reuse a value that never
// existed in source.
const (
	bootstrapUsername = "l2-test-op"
	adminUsername     = "l2-admin"
	adminEmail        = "l2-admin@gocell.local"
)

// bootstrapPassword and adminPassword are initialized once per test process
// with crypto/rand-derived base64url strings (16 bytes → 22 ASCII chars,
// well within MinPasswordBytes=8 / MaxPasswordBytes=72 of
// cells/accesscore/slices/setup/service.go). Process-scoped (not per-harness)
// so tests reference them by name without threading the value through every
// helper.
var (
	bootstrapPassword = mustRandomPassword(16)
	adminPassword     = mustRandomPassword(16)
)

// mustRandomPassword returns a base64url-encoded random string suitable for
// the accesscore password policy. Panics on rand.Read failure — package init
// has no testing.T to call t.Fatal on, and a non-seeded RNG is unrecoverable.
func mustRandomPassword(byteLen int) string {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		panic("l2atomicity: crypto/rand.Read failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// mustRandom32Bytes returns 32 cryptographically random bytes for HMAC rings,
// cursor codecs, and audit chain keys (all 32-byte primitives in GoCell).
// Panics on RNG failure for the same reason as mustRandomPassword.
func mustRandom32Bytes() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("l2atomicity: crypto/rand.Read failed: " + err.Error())
	}
	return b
}

// httpClient is shared across tests. *http.Client is safe for concurrent use
// per the net/http documentation; tests do not mutate any client state and
// each test currently runs serially (no t.Parallel()), but reusing a single
// client keeps connection pooling consistent if parallelism is added later.
//
// Timeout sized for the race-detector lane under CI load: race instrumentation
// plus docker daemon contention on a shared CI runner can stretch individual
// setup/admin and login requests in pathological cases. The harness injects a
// MinCost password hasher (credential.NewTestHasher), so bcrypt is no longer a
// material contributor; 60s gives CI plenty of headroom while remaining well
// within the race-pg-integration job budget.
//
// DisableKeepAlives ensures every request opens a fresh TCP connection.
// Without it, the connection pool can carry over half-closed connections
// from a previous test whose harness has already torn down its
// bootstrap+listener, surfacing as spurious "EOF" responses on the next
// test's first POST (observed under -race load when 8 harnesses are
// created and torn down serially).
var httpClient = &http.Client{
	Timeout:   testtime.D60s,
	Transport: &http.Transport{DisableKeepAlives: true},
}

// l2Harness boots a full PG-backed assembly (accesscore + configcore + auditcore)
// with three listeners (primary + internal + dedicated health). Provisions
// a seed admin so login can be exercised immediately.
type l2Harness struct {
	pool         *adapterpg.Pool
	base         string // primary HTTP base URL (e.g. http://127.0.0.1:1234)
	internalBase string // internal HTTP base URL (for /internal/v1/access/roles/*)
	ring         *auth.HMACKeyRing
	// auditStore is the auditcore ledger.Store (in-memory) exposed so tests
	// can observe the subscriber-driven Append after a role.revoked /
	// session.created event lands. Demonstrates that PG outbox → relay →
	// in-process eventbus → auditcore consumer is functionally wired in
	// this harness; the relay+consumer link is the only mechanism by which
	// audit chain entries advance.
	auditStore ledger.Store
}

// noopTxRunner executes fn directly without a real transaction.
// Used for configcore and auditcore which hold no PG state in this harness; accesscore uses the real adapterpg.TxManager.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = noopTxRunner{}

// allowAllLimiter satisfies auth.BootstrapRateLimiter without throttling.
type allowAllLimiter struct{}

func (allowAllLimiter) Allow(string) bool { return true }

// localListener creates an ephemeral TCP listener bound to 127.0.0.1:0.
func localListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to create test listener")
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// newTestConsumerBase builds an in-memory ConsumerBase for the bootstrap.
func newTestConsumerBase(t testing.TB, clk clock.Clock) *outbox.ConsumerBase {
	t.Helper()
	cb, err := outbox.NewConsumerBase(
		idempotency.NewInMemClaimer(clk),
		outbox.ConsumerBaseConfig{},
		clk,
	)
	require.NoError(t, err)
	return cb
}

// buildAuditcoreLedgerOpts builds the auditcore ledger options for the harness
// and returns the underlying ledger.Store so tests can observe Append events
// via Tail() after relay+consumer settles.
func buildAuditcoreLedgerOpts(t testing.TB, hmacKey []byte) ([]auditcore.Option, ledger.Store) {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err, "audit namespace parse")
	proto, err := ledger.NewProtocol(
		ns,
		hmacKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err, "audit protocol construction")
	store, err := ledger.NewMemStore(proto, clock.Real())
	require.NoError(t, err, "audit mem store construction")
	return []auditcore.Option{
		auditcore.WithLedgerProtocol(proto),
		auditcore.WithLedgerStore(store),
	}, store
}

// newL2Harness boots the full assembly. PG-backed user/role/session/refresh
// stores; in-memory configcore/auditcore. Seeds a single admin via setup/admin.
// Each test calls this fresh rather than sharing via TestMain; race-detector +
// PG container time budget is acceptable at the current 7-test scale and
// per-test isolation simplifies state reset.
func newL2Harness(t *testing.T) *l2Harness {
	return newL2HarnessWithWriter(t, nil)
}

// newL2HarnessWithWriter allows callers to inject a custom outbox.Writer for the
// accesscore cell. Pass nil to use the default adapterpg.NewOutboxWriter.
//
// The body is composed of focused helpers (buildPGStores / buildAuthLayer /
// buildCells / runBootstrap / seedAdmin) to keep this entry point readable and
// each step independently auditable. The same shape mirrors cmd/corebundle's
// composition root (SharedDeps + composition.Build + buildAssembly + bootstrap.New).
func newL2HarnessWithWriter(t *testing.T, pgOutboxOverride outbox.Writer) *l2Harness {
	t.Helper()
	h := bootL2Assembly(t, pgOutboxOverride)
	seedAdmin(t, h.base)
	return h
}

// newL2HarnessNoSeed boots the full assembly but SKIPS seedAdmin. Use this
// when the test itself drives POST /api/v1/access/setup/admin so that the
// setup path is exercised under a controlled outbox writer (e.g. with a
// selectiveFailWriter). Calling seedAdmin after boot would be non-idempotent
// if the test already POSTed setup/admin.
func newL2HarnessNoSeed(t *testing.T, pgOutboxOverride outbox.Writer) *l2Harness {
	t.Helper()
	return bootL2Assembly(t, pgOutboxOverride)
}

// bootL2Assembly constructs and starts the full PG-backed assembly (accesscore
// + configcore + auditcore) and waits for /healthz to be ready. It does NOT
// seed an admin — callers are responsible for calling seedAdmin or driving
// setup/admin directly.
func bootL2Assembly(t *testing.T, pgOutboxOverride outbox.Writer) *l2Harness {
	t.Helper()

	pg := buildPGStores(t)
	authDeps := buildAuthLayer(t)
	primaryLn := localListener(t)
	internalLn := localListener(t)
	healthLn := localListener(t)
	eb := eventbus.New(clock.Real())
	pgOutboxWriter := pickOutboxWriter(pgOutboxOverride)

	ac, cc, auc, auditStore := buildCells(t, pg, authDeps, eb, pgOutboxWriter)

	// Wire a PG outbox relay so events committed by accesscore (via the PG
	// outbox writer) are drained from outbox_entries and published into the
	// in-process eventbus where auditcore subscribes — exercising the
	// producer → relay → publisher → consumer chain on the in-process
	// transport (not the broker). The full broker path is not yet covered.
	relayCfg := outboxruntime.DefaultRelayConfig()
	pgOutboxStore := adapterpg.NewOutboxStore(pg.pool.DB(), clock.Real())
	relayWorker := outboxruntime.NewRelay(clock.Real(), pgOutboxStore, eb, relayCfg)

	// DurabilityDemo only describes the assembly construction mode; the
	// relay above is the durable bridge between PG outbox_entries and the
	// in-process eventbus.
	asm := assembly.New(clock.Real(), assembly.Config{ID: "l2-atomicity-test", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Register(ac))
	require.NoError(t, asm.Register(cc))
	require.NoError(t, asm.Register(auc))

	runBootstrap(t, asm, listenerSet{primary: primaryLn, internal: internalLn, health: healthLn}, eb, authDeps, relayWorker)
	base := "http://" + primaryLn.Addr().String()
	healthBase := "http://" + healthLn.Addr().String()
	waitForHealthz(t, healthBase, base)

	return &l2Harness{
		pool:         pg.pool,
		base:         base,
		internalBase: "http://" + internalLn.Addr().String(),
		ring:         authDeps.ring,
		auditStore:   auditStore,
	}
}

// pgStores bundles the PG-backed accesscore stores plus the pool / tx manager
// that all of them share. The store wiring is exposed as a slice of
// accesscore.Option values because the underlying repository types live in
// `cells/accesscore/internal/ports`, which is not importable from this test
// package; storing options sidesteps the visibility constraint without
// leaking unexported types.
type pgStores struct {
	pool      *adapterpg.Pool
	txMgr     *adapterpg.TxManager
	storeOpts []accesscore.Option
}

// buildPGStores clones a fresh per-test database from the shared, pre-migrated
// template (migrations + "editor" role already applied — see testmain_test.go),
// then constructs the accesscore PG store family against it.
func buildPGStores(t *testing.T) *pgStores {
	t.Helper()
	ctx := context.Background()
	dsn := sharedPG.CloneDSN(t)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close(ctx) })

	txMgr := adapterpg.NewTxManager(pool)

	pgBundle, err := accesspg.NewBundle(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	sessionProto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	require.NoError(t, err)
	sessionStore, err := adapterpg.NewSessionStore(pool.DB(), txMgr, sessionProto, clock.Real())
	require.NoError(t, err)
	refreshStore, err := adapterpg.NewRefreshStore(pool.DB(), txMgr, accesscore.DefaultRefreshPolicy(), clock.Real(), rand.Reader)
	require.NoError(t, err)

	return &pgStores{
		pool:  pool,
		txMgr: txMgr,
		storeOpts: []accesscore.Option{
			accesscore.WithPGBundle(pgBundle),
			accesscore.WithSessionStore(sessionStore),
			accesscore.WithRefreshStore(refreshStore),
		},
	}
}

// authLayer bundles the auth-side dependencies wired into both listeners and
// the accesscore cell.
type authLayer struct {
	ring        *auth.HMACKeyRing
	nonceStore  kauth.NonceStore
	jwtIssuer   *auth.JWTIssuer
	jwtVerifier *auth.JWTVerifier
	bootstrapMW func(http.Handler) http.Handler
}

// buildAuthLayer constructs the internal-listener HMAC ring + nonce store, the
// JWT issuer/verifier pair, and the bootstrap (setup/admin) middleware. All
// values are test-only; the HMAC key + cursor keys are registered in
// cmd/corebundle/demo_keys.wellKnownDemoKeys so rejectDemoKey blocks them in
// real-mode startup.
func buildAuthLayer(t *testing.T) *authLayer {
	t.Helper()
	ring, err := auth.NewHMACKeyRing(mustRandom32Bytes(), nil)
	require.NoError(t, err)
	nonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "test", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(bootstrapUsername),
			Password: []byte(bootstrapPassword),
		},
		allowAllLimiter{},
		nil,
	)

	return &authLayer{
		ring:        ring,
		nonceStore:  nonceStore,
		jwtIssuer:   jwtIssuer,
		jwtVerifier: jwtVerifier,
		bootstrapMW: bootstrapMW,
	}
}

// pickOutboxWriter returns the caller-supplied override or the default durable
// PG outbox writer.
func pickOutboxWriter(override outbox.Writer) outbox.Writer {
	if override != nil {
		return override
	}
	return adapterpg.NewOutboxWriter(clock.Real())
}

// buildCells constructs accesscore + configcore + auditcore. Only accesscore
// holds PG state in this harness; configcore + auditcore stay in-memory.
// The audit ledger.Store is returned alongside the cells so tests can
// observe the subscriber-driven Append after relay+consumer settle.
func buildCells(
	t *testing.T,
	pg *pgStores,
	a *authLayer,
	eb *eventbus.InMemoryEventBus,
	pgOutboxWriter outbox.Writer,
) (*accesscore.AccessCore, *configcore.ConfigCore, *auditcore.AuditCore, ledger.Store) {
	t.Helper()

	var nw outbox.Writer = outbox.NoopWriter{}

	auditCursorCodec, err := query.NewCursorCodec(mustRandom32Bytes())
	require.NoError(t, err)
	configCursorCodec, err := query.NewCursorCodec(mustRandom32Bytes())
	require.NoError(t, err)

	ac := accesscore.NewAccessCore(clock.Real(), append(
		pg.storeOpts,
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(pgOutboxWriter)),
		accesscore.WithJWTIssuer(a.jwtIssuer),
		accesscore.WithJWTVerifier(a.jwtVerifier),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
		accesscore.WithBootstrapAuth(a.bootstrapMW),
		accesscore.WithCASProtocol(mustNewCASProtocol(t, accesscore.PasswordVersionField)),
		// Low-cost hasher so seedAdmin + login don't pay bcrypt cost-12 per test.
		accesscoretest.MinCostPasswordHasherOption(),
	)...)
	cc := configcore.NewConfigCore(clock.Real(),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		configcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
		configcore.WithCASProtocol(mustNewCASProtocol(t, configcore.VersionField)),
	)
	auditHMACKey := mustRandom32Bytes()
	auditLedgerOpts, auditStore := buildAuditcoreLedgerOpts(t, auditHMACKey)
	auc := auditcore.NewAuditCore(clock.Real(), append([]auditcore.Option{
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(nw)),
		auditcore.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	}, auditLedgerOpts...)...)

	return ac, cc, auc, auditStore
}

// listenerSet bundles the three bootstrap listeners so runBootstrap stays
// within the 7-parameter limit (go:S107).
type listenerSet struct {
	primary  net.Listener
	internal net.Listener
	health   net.Listener
}

// runBootstrap launches bootstrap.App on the supplied listeners and registers
// the LIFO cleanup that drains it gracefully. The relay is registered as a
// ManagedResource so bootstrap drives its Start/Close lifecycle.
func runBootstrap(
	t *testing.T,
	asm *assembly.CoreAssembly,
	lns listenerSet,
	eb *eventbus.InMemoryEventBus,
	a *authLayer,
	relayWorker *outboxruntime.Relay,
) {
	t.Helper()
	app := bootstrap.New(clock.Real(),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, lns.primary.Addr().String(),
			[]kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
			bootstrap.WithListenerNet(lns.primary)),
		bootstrap.WithListener(cell.InternalListener, lns.internal.Addr().String(),
			[]kauth.ListenerAuth{authtest.MustAuthServiceToken(a.nonceStore, a.ring)},
			bootstrap.WithListenerNet(lns.internal)),
		bootstrap.WithListener(cell.HealthListener, lns.health.Addr().String(),
			[]kauth.ListenerAuth{kauth.AuthNone{}},
			bootstrap.WithListenerNet(lns.health)),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(newTestConsumerBase(t, clock.Real())),
		bootstrap.WithRelay(relayWorker),
		// Invariant: ShutdownTimeout ≥ httpClient.Timeout. The last in-flight
		// request must be allowed to finish (or its own timeout fire) before
		// bootstrap forces a close, otherwise we get spurious EOF mid-request
		// even when the server is healthy. httpClient.Timeout is 60s under
		// the race-detector lane; we match it (no headroom is harmless — the
		// graceful drain returns immediately once all sockets are quiet).
		bootstrap.WithShutdownTimeout(testtime.D60s),
	)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-done:
			assert.NoError(t, runErr)
		// Cleanup window must exceed bootstrap WithShutdownTimeout so we
		// observe the graceful-drain outcome (success or its error) rather
		// than time out the harness before bootstrap reports.
		// Cleanup window > bootstrap.WithShutdownTimeout (= testtime.D60s) so
		// we observe the graceful-drain outcome rather than time out the
		// harness first. 90s gives the LIFO unwind room for slow Docker
		// terminations on contended runners.
		case <-time.After(testtime.D60s + testtime.D30s):
			t.Fatal("bootstrap did not shut down in time")
		}
	})
}

// waitForHealthz polls until both /healthz (on healthBase, the dedicated health
// listener) and the accesscore setup-status route (on primaryBase) return 200.
// Probing the setup-status route (not just /healthz) ensures bootstrap has
// completed phase5 route mount + FinalizeAuth, so the subsequent setup/admin
// POST is guaranteed to land on a wired handler rather than racing against mux
// finalization (race-detector + concurrent load occasionally surfaced "EOF"
// responses when only /healthz was probed).
func waitForHealthz(t *testing.T, healthBase, primaryBase string) {
	t.Helper()
	testwait.External(t, "l2-harness-server-ready", func() bool {
		if !httpGetOK(healthBase + "/healthz") {
			return false
		}
		return httpGetOK(primaryBase + "/api/v1/access/setup/status")
	}, testtime.EventuallyLong, testtime.MediumPoll, "HTTP server did not become ready")
}

// httpGetOK is a tiny helper that returns true iff GET returns 200.
func httpGetOK(url string) bool {
	resp, err := httpClient.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// seedAdmin provisions the initial admin via POST /api/v1/access/setup/admin
// so login is immediately available to tests.
//
// Failure mode handling: under -race + heavy Docker contention the bcrypt-
// bound handler can complete server-side (201 logged) while the client's
// TCP socket has already surfaced a transport-level io.EOF. On EOF we
// re-probe /api/v1/access/setup/status — if hasAdmin=true the first POST
// landed and we accept it as success (setup/admin is non-idempotent;
// re-POSTing yields 410 ERR_SETUP_ALREADY_INITIALIZED which would otherwise
// fail the test). If hasAdmin=false the first POST never landed and we
// retry once with a fresh connection. Any non-EOF error fails fast so real
// bugs remain visible.
func seedAdmin(t *testing.T, base string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": adminUsername,
		"email":    adminEmail,
		"password": adminPassword,
	})
	postBody := func() *bytes.Reader { return bytes.NewReader(body) }

	resp, err := postSetupAdmin(base, postBody())
	if errors.Is(err, io.EOF) {
		if setupHasAdmin(t, base) {
			t.Logf("seedAdmin: transient io.EOF on first POST but setup-status confirms admin exists; accepting")
			return
		}
		t.Logf("seedAdmin: transient io.EOF on first POST and setup-status reports no admin; retrying once")
		resp, err = postSetupAdmin(base, postBody())
	}
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "harness: admin provisioning must succeed")
}

// setupHasAdmin queries GET /api/v1/access/setup/status and returns true iff
// the response confirms an admin already exists. Used by seedAdmin to make
// the EOF retry idempotent.
func setupHasAdmin(t *testing.T, base string) bool {
	t.Helper()
	resp, err := httpClient.Get(base + "/api/v1/access/setup/status")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return false
	}
	defer resp.Body.Close()
	var parsed struct {
		Data struct {
			HasAdmin bool `json:"hasAdmin"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return false
	}
	return parsed.Data.HasAdmin
}

func postSetupAdmin(base string, body *bytes.Reader) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/access/setup/admin", body)
	req.SetBasicAuth(bootstrapUsername, bootstrapPassword)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", l2TestTenantID)
	return httpClient.Do(req)
}

func mustNewCASProtocol(t *testing.T, versionField string) *cas.Protocol {
	t.Helper()
	p, err := cas.NewProtocol(cas.WithVersionField(versionField))
	if err != nil {
		t.Fatalf("cas.NewProtocol: %v", err)
	}
	return p
}
