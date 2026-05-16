package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/ratelimit"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/cells/accesscore/configgetter"
	accessmem "github.com/ghbvf/gocell/cells/accesscore/mem"
	accesspg "github.com/ghbvf/gocell/cells/accesscore/postgres"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// envSessionCacheTTL is the env knob that enables AUTH-CACHE-01 — a Redis
// read-through cache decorator over the wired session.Store. Unset / empty →
// disabled (default); valid positive Duration + Redis client constructed →
// enabled. Recommended value 30s; see .env.example.
const envSessionCacheTTL = "GOCELL_SESSION_CACHE_TTL"

// sessionCacheTTLMax bounds GOCELL_SESSION_CACHE_TTL. See
// adapters/redis.CachingSessionStore §Threat model — the cache TTL is the
// security floor for stale-after-revoke; values >30s widen the window beyond
// the documented threat model and are rejected as a wiring misconfiguration.
const sessionCacheTTLMax = 30 * time.Second

// sessionCacheNamespace is the Redis key namespace under which session cache
// entries live. Per the per-cell adapters/redis convention, the owning cell
// ID is used directly: final Redis keys look like
//
//	accesscore:session:<sessionID>
const sessionCacheNamespace adapterredis.KeyNamespace = "accesscore"

const defaultRefreshGCRetention = 24 * time.Hour

// AccessCoreModule wires accesscore: JWT issuer/verifier + EventBus + cursor
// codec, and the bootstrap Basic Auth protection for the setup/admin endpoint.
//
// ref: uber-go/fx fx.Module("accesscore", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type AccessCoreModule struct{}

// ID returns the stable identifier used in error messages.
func (AccessCoreModule) ID() string { return "accesscore" }

// Provide resolves all accesscore-specific dependencies and returns the
// constructed cell, bootstrap options, and lifecycle resources.
//
// Reads GOCELL_BOOTSTRAP_ADMIN_USERNAME, GOCELL_BOOTSTRAP_ADMIN_PASSWORD,
// GOCELL_ACCESSCORE_CURSOR_KEY, GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY from
// the environment.
func (m AccessCoreModule) Provide(
	_ context.Context, shared *SharedDeps,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	creds, err := loadBootstrapCredentials(
		os.Getenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME"),
		os.Getenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD"),
	)
	if err != nil {
		return nil, nil, nil, err
	}

	if creds.Username == nil {
		return nil, nil, nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"GOCELL_BOOTSTRAP_ADMIN_USERNAME and GOCELL_BOOTSTRAP_ADMIN_PASSWORD are required "+
				"to protect setup/admin endpoint")
	}

	// Cursor codec for accesscore: read env via LoadCursorKeys then build.
	accessPrimary, accessPrevious := LoadCursorKeys("ACCESSCORE")
	cursorCodec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: shared.Topology.AdapterMode,
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     accessPrimary,
		Previous:    accessPrevious,
		DevDefault:  "corebundle-access-cursor-key32!!",
		Label:       "access",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("accesscore cursor codec: %w", err)
	}

	// CAS Protocol for the ChangePassword narrow-scope concurrent-write guard
	// (S6 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01). Constructed here (composition
	// root) per CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest.
	casProto := cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))

	sessionProto := session.MustNewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)

	accessOpts := []accesscore.Option{
		accesscore.WithClock(shared.Clock),
		// Publisher set unconditionally; outboxWriter set conditionally below.
		// cell.ResolveEmitter picks DirectEmitter(FailOpen) when writer is nil
		// (memory mode) and WriterEmitter when both pub+writer are non-nil (durable).
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(shared.EventBus), nil),
		accesscore.WithJWTIssuer(shared.JWTDeps.issuer),
		accesscore.WithJWTVerifier(shared.JWTDeps.verifier),
		accesscore.WithCursorCodec(cursorCodec),
		accesscore.WithMetricsProvider(shared.PromStack.metricProvider),
		accesscore.WithConfigEventCollector(shared.ConfigEventCollector),
		accesscore.WithRefreshGC(time.Hour, defaultRefreshGCRetention),
		accesscore.WithCASProtocol(casProto),
	}
	var innerSessionStore session.Store
	if shared.Topology.StorageBackend == "postgres" {
		pgOpts, pgSessionStore, err := accessPostgresOptions(shared, sessionProto)
		if err != nil {
			return nil, nil, nil, err
		}
		innerSessionStore = pgSessionStore
		accessOpts = append(accessOpts, pgOpts...)
	} else {
		// mem mode: explicit construction so UserRepository + RoleRepository share
		// a single Store (required for cross-repo effective-admin invariant, S4.0).
		userMemStore := accessmem.NewStore(shared.Clock)
		sessionMemStore, err := session.NewMemStore(sessionProto, shared.Clock)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("accesscore: session.NewMemStore: %w", err)
		}
		refreshMemStore, err := refreshmem.New(accesscore.DefaultRefreshPolicy(), shared.Clock, nil)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("accesscore: refreshmem.New: %w", err)
		}
		innerSessionStore = sessionMemStore
		accessOpts = append(accessOpts,
			accesscore.WithUserRepository(userMemStore.UserRepository()),
			accesscore.WithRoleRepository(userMemStore.RoleRepository()),
			accesscore.WithRefreshStore(refreshMemStore),
		)
	}
	// AUTH-CACHE-01 (T5): wrap the session store with a Redis read-through
	// cache when env knob + Redis client are both present. Default-off — env
	// unset / empty / non-positive / un-parseable Duration / nil Redis client
	// all leave innerSessionStore unwrapped. See plan §S4c T5 and
	// docs/architecture/202605101400-adr-credential-session-protocol.md
	// (cache.miss / stale / outage are fail-safe; epoch invariant tolerates
	// stale ValidateView via user.AuthzEpoch live read).
	sessionStore, err := wrapSessionStoreWithCache(innerSessionStore, shared, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	accessOpts = append(accessOpts, accesscore.WithSessionStore(sessionStore))
	// Bootstrap credential auth + per-IP token bucket rate limiter protects
	// the setup/admin endpoint (ADR §D2 operator credential via env).
	//
	// Rate parameters (5 req/min sustained, burst 10) mirror nginx limit_req
	// defaults for credential endpoints: tight enough to defeat brute-force
	// enumeration, loose enough not to block legitimate operator retries.
	// Multi-pod deployments share the in-memory bucket per replica; a future
	// distributed limiter (Redis-backed) is logged as backlog
	// BOOTSTRAP-RATELIMIT-DISTRIBUTED-01.
	rlLimiter := ratelimit.New(ratelimit.Config{
		Rate:  bootstrapRateLimitPerSec, // 5 req/min ≈ 0.0833/sec
		Burst: bootstrapRateLimitBurst,
	}, shared.Clock)
	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{Username: creds.Username, Password: creds.Password},
		rlLimiter,
		bootstrapAuthFailLogger(slog.Default()),
	)
	accessOpts = append(accessOpts, accesscore.WithBootstrapAuth(bootstrapMW))

	c := accesscore.NewAccessCore(accessOpts...) //archtest:allow:clock-injection:via-slice WithClock prepended to accessOpts above
	// Bootstrap phase3b auto-discovers c.LifecycleHooks() — no WithWorkers needed.
	// rlLimiter owns a cleanup goroutine that exits on Close(); bind it to a
	// ManagedResource so phase10 LIFO teardown stops the goroutine cleanly.
	return c, nil, []kernellifecycle.ManagedResource{bootstrapLimiterResource{lim: rlLimiter}}, nil
}

// accessPostgresOptions builds the postgres-specific accesscore options and
// also returns the constructed PG session store separately so the caller can
// optionally wrap it with AUTH-CACHE-01 Redis cache before binding via
// accesscore.WithSessionStore.
func accessPostgresOptions(shared *SharedDeps, sessionProto *session.Protocol) ([]accesscore.Option, session.Store, error) {
	if shared.SharedPGPool == nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: postgres mode requires SharedPGPool " +
			"(ConfigCoreModule must run before AccessCoreModule)")
	}
	writer := adapterpg.NewOutboxWriter(shared.Clock)
	txMgr := adapterpg.NewTxManager(shared.SharedPGPool)
	// Accumulative WithOutboxDeps: adds writer without replacing the publisher
	// set above. WithTxManager wires the TxRunner for L2 transactional atomicity.
	accessOpts := []accesscore.Option{
		accesscore.WithOutboxDeps(nil, outbox.WrapWriterForCell(writer)),
		accesscore.WithTxManager(persistence.WrapForCell(txMgr)),
	}
	// Build PG deps once and share across all PG-backed repo factories so the
	// underlying pool/txRunner/clock are not repeated at each call site.
	deps, err := accesspg.NewDeps(shared.SharedPGPool.DB(), txMgr, shared.Clock)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGDeps: %w", err)
	}
	pgUserRepo, err := accesspg.NewUserRepository(deps)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGUserRepository: %w", err)
	}
	pgRoleRepo, err := accesspg.NewRoleRepository(deps)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGRoleRepository: %w", err)
	}
	pgSetupLock, err := accesspg.NewSetupLock(deps)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGSetupLock: %w", err)
	}
	pgSessionStore, err := adapterpg.NewSessionStore(shared.SharedPGPool.DB(), txMgr, sessionProto, shared.Clock)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGSessionStore: %w", err)
	}
	pgRefreshStore, err := adapterpg.NewRefreshStore(
		shared.SharedPGPool.DB(), txMgr,
		accesscore.DefaultRefreshPolicy(), shared.Clock, rand.Reader,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGRefreshStore: %w", err)
	}
	accessOpts = append(accessOpts,
		accesscore.WithUserRepository(pgUserRepo),
		accesscore.WithRoleRepository(pgRoleRepo),
		accesscore.WithSetupLock(pgSetupLock),
		accesscore.WithRefreshStore(pgRefreshStore),
	)
	// Wire the ConfigGetter for the configreceive slice to fetch entry values
	// from configcore's internal GET /internal/v1/config/{key} endpoint after
	// an upsert event (contract: http.config.internal.get.v1).
	// baseURL is constructed from InternalHTTPAddr. If the addr is a port-only
	// string (e.g. ":9090") we resolve to loopback; if host:port, prepend scheme.
	// The HMAC ring from InternalGuard is reused for outbound service-token signing.
	// If tests construct SharedDeps without InternalGuard, configreceive stays
	// in log-only mode.
	if shared.InternalGuard != nil {
		internalBaseURL := internalAddrToBaseURL(shared.InternalHTTPAddr)
		accessOpts = append(accessOpts,
			configgetter.WithHTTP(internalBaseURL, shared.InternalGuard.ring, shared.Clock),
		)
	}
	return accessOpts, pgSessionStore, nil
}

// wrapSessionStoreWithCache decides whether to wrap inner with the
// AUTH-CACHE-01 Redis cache decorator. The decision is fail-safe by default:
//
//   - GOCELL_SESSION_CACHE_TTL unset or empty → return inner unchanged.
//   - TTL parse failure or non-positive → slog.Warn + return inner unchanged.
//   - TTL >30s → fail-fast with ErrValidationFailed (wiring misconfig, not env issue).
//   - No Redis client constructed (Redis envs not set) → slog.Warn + return
//     inner unchanged.
//   - Cache primitive construction fails → propagate as startup error
//     (mis-namespaced KeyNamespace is a wiring bug, not a runtime tolerance).
//
// Successful path returns a *CachingSessionStore around inner.
//
// logger may be nil; the function falls back to slog.Default(), matching the
// nil-fallback convention used by cell service constructors (sessionlogin /
// sessionlogout / identitymanage / etc.). Tests inject a buffer-backed logger
// directly to assert on captured records without touching global state.
func wrapSessionStoreWithCache(inner session.Store, shared *SharedDeps, logger *slog.Logger) (session.Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	raw := strings.TrimSpace(os.Getenv(envSessionCacheTTL))
	if raw == "" {
		return inner, nil
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		logger.Warn("accesscore: session cache disabled — GOCELL_SESSION_CACHE_TTL not a valid Duration",
			slog.String("value", raw),
			slog.Any("error", err))
		return inner, nil
	}
	if ttl <= 0 {
		logger.Warn("accesscore: session cache disabled — GOCELL_SESSION_CACHE_TTL must be positive",
			slog.String("value", raw))
		return inner, nil
	}
	if ttl > sessionCacheTTLMax {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore: GOCELL_SESSION_CACHE_TTL exceeds documented maximum",
			errcode.WithInternal(fmt.Sprintf("GOCELL_SESSION_CACHE_TTL=%s exceeds max %s", ttl, sessionCacheTTLMax)))
	}
	if shared.RedisClient == nil {
		logger.Warn("accesscore: session cache disabled — GOCELL_SESSION_CACHE_TTL set but no Redis client " +
			"configured (set GOCELL_REDIS_ADDR or GOCELL_REDIS_CLUSTER_ADDRS)")
		return inner, nil
	}
	cache, err := adapterredis.NewCache(shared.RedisClient, sessionCacheNamespace)
	if err != nil {
		return nil, fmt.Errorf("accesscore: session cache: %w", err)
	}
	wrapped, err := adapterredis.NewCachingSessionStore(inner, cache, ttl, logger)
	if err != nil {
		return nil, fmt.Errorf("accesscore: session cache: %w", err)
	}
	logger.Info("accesscore: session cache enabled",
		slog.Duration("ttl", ttl),
		slog.String("namespace", string(sessionCacheNamespace)))
	return wrapped, nil
}

// bootstrapRateLimitPerSec is 5 req/min expressed in per-second tokens — the
// nginx limit_req default for credential-bearing endpoints.
const bootstrapRateLimitPerSec = 5.0 / 60.0

// bootstrapRateLimitBurst allows short legitimate retries (operator typo
// followed by correction) without immediately tripping the limiter.
const bootstrapRateLimitBurst = 10

// bootstrapAuthFailLogger returns the onAuthFail observer wired into the
// bootstrap middleware. logger is injected (not slog.Default) so tests assert
// on a captured handler without mutating global state — composition root passes
// slog.Default(); tests pass a buffer-backed logger.
// client_ip is empty when the context carries no real IP (health checks, unit
// tests without middleware that sets ctxkeys.RealIP).
// Audit cell integration is tracked as backlog BOOTSTRAP-AUDIT-CHAIN-WIRING-01.
func bootstrapAuthFailLogger(logger *slog.Logger) auth.BootstrapAuthFailObserver {
	return func(ctx context.Context, reason string) {
		ip, _ := ctxkeys.RealIPFrom(ctx)
		logger.ErrorContext(ctx, "bootstrap_auth_failed",
			slog.String("event", "bootstrap_auth_failed"),
			slog.String("reason", reason),
			slog.String("client_ip", ip))
	}
}

// bootstrapLimiterResource adapts the rate limiter to the ManagedResource
// contract so phase10 shutdown stops the cleanup goroutine.
type bootstrapLimiterResource struct{ lim *ratelimit.Limiter }

func (bootstrapLimiterResource) Checkers() map[string]func(context.Context) error {
	return nil
}
func (bootstrapLimiterResource) Worker() worker.Worker { return nil }
func (r bootstrapLimiterResource) Close(ctx context.Context) error {
	return r.lim.Close(ctx)
}

var _ CellModule = AccessCoreModule{}

// internalAddrToBaseURL converts a bind address to an HTTP base URL suitable
// for the internal HTTP client. Port-only addresses (e.g. ":9090") are resolved
// to "http://127.0.0.1:9090"; host:port addresses get "http://" prepended.
// As a defensive measure, "0.0.0.0:port" bind addresses are normalised to
// "127.0.0.1:port" so the ConfigGetter always connects on loopback regardless
// of how the listener was configured (prevents accidental bridge-network routing
// when a container misconfigures GOCELL_HTTP_INTERNAL_ADDR=0.0.0.0:9090).
// Used to construct the ConfigGetter base URL from SharedDeps.InternalHTTPAddr.
func internalAddrToBaseURL(addr string) string {
	if addr == "" {
		return "http://127.0.0.1:9090"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	// Normalise 0.0.0.0:port → 127.0.0.1:port (defense against misconfiguration).
	if after, ok := strings.CutPrefix(addr, "0.0.0.0:"); ok {
		return "http://127.0.0.1:" + after
	}
	return "http://" + addr
}

// BootstrapAdminCredentials holds the env-driven credentials for the initial
// admin setup endpoint.
type BootstrapAdminCredentials struct {
	Username []byte
	Password []byte
}

// loadBootstrapCredentials reads GOCELL_BOOTSTRAP_ADMIN_USERNAME and
// GOCELL_BOOTSTRAP_ADMIN_PASSWORD from env, trims whitespace (K8s secret
// files commonly append a trailing newline), and validates:
//   - both set or both empty (XOR fail-fast)
//   - username non-empty after trim + no control chars
//   - password ≥ 8 bytes after trim
//
// Returns BootstrapAdminCredentials with nil fields when both are empty
// (indicating credentials were not configured).
//
// ref: keycloak/keycloak KC_BOOTSTRAP_ADMIN_USERNAME (one-shot env)
// ref: minio/minio internal/auth/credentials.go (length fail-fast)
func loadBootstrapCredentials(username, password string) (BootstrapAdminCredentials, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)

	usernameSet := username != ""
	passwordSet := password != ""

	// XOR: both must be set or both must be empty.
	if usernameSet != passwordSet {
		return BootstrapAdminCredentials{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"GOCELL_BOOTSTRAP_ADMIN_USERNAME and GOCELL_BOOTSTRAP_ADMIN_PASSWORD "+
				"must both be set or both be empty")
	}

	// Both empty — credentials not configured.
	if !usernameSet {
		return BootstrapAdminCredentials{}, nil
	}

	// Validate username: no control characters.
	for _, r := range username {
		if unicode.IsControl(r) {
			return BootstrapAdminCredentials{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"GOCELL_BOOTSTRAP_ADMIN_USERNAME must not contain control characters")
		}
	}

	// Validate password: minimum 8 bytes.
	if len(password) < 8 {
		return BootstrapAdminCredentials{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"GOCELL_BOOTSTRAP_ADMIN_PASSWORD must be at least 8 bytes")
	}

	return BootstrapAdminCredentials{
		Username: []byte(username),
		Password: []byte(password),
	}, nil
}
