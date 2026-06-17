// Package accesscore is the platform composition module for the accesscore Cell.
// It implements [composition.CellModule] and wires all accesscore-specific
// dependencies from [composition.SharedDeps].
//
// This is a composition-root-layer package: it may import cells/, adapters/,
// and cellmodules/cellsecrets/. It must NOT be imported by cells/, runtime/, or
// adapters/.
//
// ref: uber-go/fx fx.Module("accesscore", ...) — self-contained module.
package accesscore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/ratelimit"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	"github.com/ghbvf/gocell/cellmodules/celltransport"
	accesscell "github.com/ghbvf/gocell/corecells/accesscore"
	"github.com/ghbvf/gocell/corecells/accesscore/configgetter"
	accessmem "github.com/ghbvf/gocell/corecells/accesscore/mem"
	accesspg "github.com/ghbvf/gocell/corecells/accesscore/postgres"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/worker"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/ctxutil"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	refreshmem "github.com/ghbvf/gocell/framework/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

// envSessionCacheTTL is the env knob that enables AUTH-CACHE-01.
const envSessionCacheTTL = "GOCELL_SESSION_CACHE_TTL"

// sessionCacheTTLMax bounds GOCELL_SESSION_CACHE_TTL.
const sessionCacheTTLMax = 30 * time.Second

// sessionCacheNamespace is the Redis key namespace for session cache entries.
const sessionCacheNamespace adapterredis.KeyNamespace = "accesscore"

const defaultRefreshGCRetention = 24 * time.Hour

// bootstrapRateLimitPerSec is 5 req/min expressed in per-second tokens.
const bootstrapRateLimitPerSec = 5.0 / 60.0

// bootstrapRateLimitBurst allows short legitimate retries.
const bootstrapRateLimitBurst = 10

type module struct{}

// Module returns a composition.CellModule that wires the accesscore Cell.
func Module() composition.CellModule { return module{} }

// ID returns the stable identifier used in error messages and logs.
func (module) ID() string { return "accesscore" }

// bootstrapAppendDetachedTimeout caps the audit-append emit write so a stalled
// PG outbox write cannot block the bootstrap rate-limited endpoint indefinitely.
// 2s mirrors the single-statement INSERT budget under healthy PG (same value as
// the prior direct-write observer in runtime/audit/bootstrap_observer.go).
const bootstrapAppendDetachedTimeout = 2 * time.Second

// Provide resolves all accesscore-specific dependencies and returns the
// constructed cell, bootstrap options, and lifecycle resources.
//
// Reads GOCELL_BOOTSTRAP_ADMIN_USERNAME, GOCELL_BOOTSTRAP_ADMIN_PASSWORD,
// GOCELL_ACCESSCORE_CURSOR_KEY, GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY, and
// GOCELL_ACCESSCORE_IP_HASH_SALT (real-mode required, ≥32 bytes; #1488) from
// the environment.
func (m module) Provide(
	_ context.Context, shared *composition.SharedDeps,
) (composition.ModuleResult, error) {
	creds, err := loadBootstrapCredentials(
		os.Getenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME"),
		os.Getenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD"),
	)
	if err != nil {
		return composition.ModuleResult{}, err
	}
	if creds.Username == nil {
		return composition.ModuleResult{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"GOCELL_BOOTSTRAP_ADMIN_USERNAME and GOCELL_BOOTSTRAP_ADMIN_PASSWORD are required "+
				"to protect setup/admin endpoint")
	}

	accessOpts, sessionProto, err := buildAccessBaseOpts(shared)
	if err != nil {
		return composition.ModuleResult{}, err
	}

	innerSessionStore, storageOpts, transportResources, err := resolveAccessStorageOpts(shared, sessionProto, accessOpts)
	if err != nil {
		return composition.ModuleResult{}, err
	}
	accessOpts = storageOpts

	sessionStore, err := wrapSessionStoreWithCache(innerSessionStore, shared, nil)
	if err != nil {
		return composition.ModuleResult{}, err
	}
	accessOpts = append(accessOpts, accesscell.WithSessionStore(sessionStore))

	rlLimiter := ratelimit.New(ratelimit.Config{
		Rate:  bootstrapRateLimitPerSec,
		Burst: bootstrapRateLimitBurst,
	}, shared.Clock)

	// Per-deployment secret salt for the keyed client-IP hash (#1488). Loaded
	// here in the composition root so the cell never holds the secret; real mode
	// fails fast if unset, dev falls back to a registered demo key.
	ipHashSalt, err := cellsecrets.BuildHMACKey(cellsecrets.HMACKeyConfig{
		AdapterMode: shared.Topology.AdapterMode(),
		EnvName:     "GOCELL_ACCESSCORE_IP_HASH_SALT",
		Primary:     os.Getenv("GOCELL_ACCESSCORE_IP_HASH_SALT"),
		DevDefault:  bootstrapIPHashSaltDevDefault,
	})
	if err != nil {
		return composition.ModuleResult{}, fmt.Errorf("accesscore: bootstrap IP-hash salt: %w", err)
	}
	if len(ipHashSalt) < redaction.MinIPHashSaltBytes {
		// A short salt silently defeats the keyed-hash secrecy (the IPv4 space is
		// brute-forceable), so fail fast rather than ship a reversible IP hash.
		return composition.ModuleResult{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore: GOCELL_ACCESSCORE_IP_HASH_SALT must be at least 32 bytes")
	}

	// Bootstrap auth-fail observer (Wave-1 #1423 event-based decoupling).
	// See newBootstrapAuthObserver for the lazy atomic.Pointer semantics (C7).
	var cellAtomicPtr atomic.Pointer[accesscell.AccessCore]
	bootstrapAuthObserver := newBootstrapAuthObserver(slog.Default(), &cellAtomicPtr, ipHashSalt)

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{Username: creds.Username, Password: creds.Password},
		rlLimiter,
		bootstrapAuthObserver,
	)
	accessOpts = append(accessOpts, accesscell.WithBootstrapAuth(bootstrapMW))

	c := accesscell.NewAccessCore(shared.Clock, accessOpts...)
	// Store the cell pointer atomically so the observer closure can reach
	// RecordBootstrapAuthFail. Store runs before Build returns; observer fires
	// only after HTTP servers start (post-Init), so Load always sees non-nil.
	cellAtomicPtr.Store(c)

	// The bootstrap rate limiter spawns a cleanup goroutine, so it is a
	// ManagedResource. Return it ONLY in ModuleResult.Resources (single source,
	// PR #591 / #1420): Builder.Build derives BOTH the steady-state
	// bootstrap.WithManagedResource registration (phase10 LIFO Close on the
	// normal run) AND the pre-Run rollback Close (if a later module's Provide
	// fails before bootstrap.Run starts) from that one entry. The module does not
	// call bootstrap.WithManagedResource itself (WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01).
	limiterRes := bootstrapLimiterResource{lim: rlLimiter}
	// transportResources carries the remote config-getter peer readiness probe in
	// split topology (#2251 P2.7); nil/empty when configcore is co-located. Build
	// derives BOTH the steady-state WithManagedResource registration AND the
	// pre-Run rollback stack from ModuleResult.Resources (single source).
	resources := append([]kernellifecycle.ManagedResource{limiterRes}, transportResources...)
	return composition.ModuleResult{
		Cell:      c,
		Resources: resources,
	}, nil
}

// buildAccessBaseOpts builds the base accesscore options and session protocol.
// Extracted from Provide to keep its cognitive complexity within the limit.
func buildAccessBaseOpts(shared *composition.SharedDeps) ([]accesscell.Option, *session.Protocol, error) {
	accessPrimary, accessPrevious := cellsecrets.LoadCursorKeys("ACCESSCORE")
	cursorCodec, err := cellsecrets.BuildCursorCodec(cellsecrets.CursorCodecConfig{
		AdapterMode: shared.Topology.AdapterMode(),
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     accessPrimary,
		Previous:    accessPrevious,
		DevDefault:  "corebundle-access-cursor-key32!!",
		Label:       "access",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore cursor codec: %w", err)
	}

	// CAS Protocol for ChangePassword concurrent-write guard
	// (CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest).
	casProto, err := cas.NewProtocol(cas.WithVersionField(accesscell.PasswordVersionField))
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore cas protocol: %w", err)
	}

	sessionProto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore session protocol: %w", err)
	}

	lockoutMetrics, err := auth.NewAccountLockoutMetrics(shared.MetricsProvider)
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore: register account-lockout metrics: %w", err)
	}

	opts := []accesscell.Option{
		accesscell.WithOutboxDeps(outbox.WrapPublisherForCell(shared.Publisher), nil),
		accesscell.WithJWTIssuer(shared.JWTIssuer),
		accesscell.WithJWTVerifier(shared.JWTVerifier),
		accesscell.WithCursorCodec(cursorCodec),
		accesscell.WithMetricsProvider(shared.MetricsProvider),
		accesscell.WithConfigEventCollector(shared.ConfigEventCollector),
		accesscell.WithLockoutMetrics(lockoutMetrics),
		accesscell.WithRefreshGC(time.Hour, defaultRefreshGCRetention),
		accesscell.WithCASProtocol(casProto),
	}
	return opts, sessionProto, nil
}

// accessPostgresOptions builds the postgres-specific accesscore options. It also
// returns any readiness ManagedResources the config-getter transport contributes
// (a remote-peer probe in split topology; nil when co-located) so Provide can
// surface them via ModuleResult.Resources (#2251 P2.7).
func accessPostgresOptions(
	shared *composition.SharedDeps, sessionProto *session.Protocol,
) ([]accesscell.Option, session.Store, []kernellifecycle.ManagedResource, error) {
	if shared.PG == nil {
		return nil, nil, nil, fmt.Errorf("AccessCoreModule: postgres mode requires the postgres capability provider " +
			"(the composition root must provision the postgres capability on SharedDeps before composition.Build)")
	}
	db, poolErr := cellsecrets.PgxPoolFromProvider(shared.PG)
	if poolErr != nil {
		return nil, nil, nil, fmt.Errorf("AccessCoreModule: %w", poolErr)
	}
	txMgr := shared.PG.TxManager()
	pgBundle, err := accesspg.NewBundle(db, txMgr, shared.Clock)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("AccessCoreModule: PGBundle: %w", err)
	}
	pgSessionStore, err := adapterpg.NewSessionStore(db, txMgr, sessionProto, shared.Clock)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("AccessCoreModule: PGSessionStore: %w", err)
	}
	pgRefreshStore, err := adapterpg.NewRefreshStore(
		db, txMgr,
		accesscell.DefaultRefreshPolicy(), shared.Clock, rand.Reader,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("AccessCoreModule: PGRefreshStore: %w", err)
	}
	accessOpts := []accesscell.Option{
		accesscell.WithOutboxDeps(nil, outbox.WrapWriterForCell(shared.PG.OutboxWriter())),
		accesscell.WithPGBundle(pgBundle),
		accesscell.WithRefreshStore(pgRefreshStore),
	}
	// Wire the ConfigGetter through the CellTransport seam (US4 #1963 / US5 #1966).
	// signing uses shared.InternalServiceKeyring (promoted from cmd-private
	// internalGuard.ring onto composition.SharedDeps); the transport carries the
	// signed request to configcore's internal handler. accesscore signs with its
	// own per-cell subkey (#2153) — the keyring resolves "accesscore" internally.
	var resources []kernellifecycle.ManagedResource
	if shared.InternalServiceKeyring != nil {
		opts, res, err := wireConfigGetter(shared, accessOpts)
		if err != nil {
			return nil, nil, nil, err
		}
		accessOpts = opts
		resources = res
	}
	return accessOpts, pgSessionStore, resources, nil
}

// configProviderCell is the cell that provides the internal config-get contract
// (http.config.internal.get.v1) accesscore consumes.
const configProviderCell = "configcore"

// wireConfigGetter selects the config getter transport by configcore's placement
// in the deployment topology via [celltransport.Resolve] (US5 #1966):
//   - co-located → inProc transport (zero-copy in-process dispatch).
//   - remote → RemoteHTTPTransport targeting the declared endpoint.
//   - un-classified → KindInternal fail-fast (defense-in-depth; TOPO-11 prevents
//     this at static-analysis time).
//
// Previously (US4) the remote path fail-fast'ed with a placeholder error; US5
// replaces that with the real celltransport.Resolve which handles both cases.
func wireConfigGetter(
	shared *composition.SharedDeps, accessOpts []accesscell.Option,
) ([]accesscell.Option, []kernellifecycle.ManagedResource, error) {
	topo, err := bootstrap.NewDeploymentTopology(shared.DeploymentTopology)
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore: deployment topology: %w", err)
	}
	// celltransport.Resolve is the single topology-gated entry for CellTransport
	// selection (CELLTRANSPORT-SELECT-FUNNEL-01). The SHARED cross-cell observability
	// bundle (shared.TransportObs: transport metrics + tracer, minted once by
	// composition.Builder) is threaded as ONE value so a split-topology remote call
	// emits cell_transport_requests_total{transport_mode=remote} AND produces spans
	// with the SAME tracer bootstrap wires for in-process calls — closing the ADR D4
	// span half (#2251 P1.3, was a nil-tracer follow-up under #1966). Resolve also
	// returns a TCP-dial readiness ManagedResource for the remote peer so an
	// unreachable configcore degrades this cell's /readyz (#2251 P2.7); it is
	// surfaced via ModuleResult.Resources by the Provide caller.
	ct, resources, err := celltransport.Resolve(topo, configProviderCell,
		shared.InProcessTransport, shared.Clock, shared.TransportObs)
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore: celltransport.Resolve: %w", err)
	}
	return append(accessOpts,
		configgetter.WithTransport(ct, shared.InternalServiceKeyring, shared.Clock)), resources, nil
}

// resolveAccessStorageOpts selects postgres or memory storage options. It also
// returns any readiness ManagedResources the transport seam contributes (remote
// config-getter peer probe in postgres split topology; nil in memory mode where
// no cross-cell transport is wired) so Provide can surface them (#2251 P2.7).
func resolveAccessStorageOpts(
	shared *composition.SharedDeps,
	sessionProto *session.Protocol,
	base []accesscell.Option,
) (session.Store, []accesscell.Option, []kernellifecycle.ManagedResource, error) {
	if shared.Topology.StorageBackend() == "postgres" {
		pgOpts, pgSessionStore, resources, err := accessPostgresOptions(shared, sessionProto)
		if err != nil {
			return nil, nil, nil, err
		}
		return pgSessionStore, append(base, pgOpts...), resources, nil
	}
	sessionMemStore, err := session.NewMemStore(sessionProto, shared.Clock)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("accesscore: session.NewMemStore: %w", err)
	}
	refreshMemStore, err := refreshmem.New(accesscell.DefaultRefreshPolicy(), shared.Clock, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("accesscore: refreshmem.New: %w", err)
	}
	base = append(
		base,
		accesscell.WithMemBundle(accessmem.NewBundle(shared.Clock)),
		accesscell.WithRefreshStore(refreshMemStore),
	)
	return sessionMemStore, base, nil, nil
}

// wrapSessionStoreWithCache decides whether to wrap inner with the AUTH-CACHE-01
// Redis cache decorator.
func wrapSessionStoreWithCache(inner session.Store, shared *composition.SharedDeps, logger *slog.Logger) (session.Store, error) {
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
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("GOCELL_SESSION_CACHE_TTL=%s exceeds max %s", ttl, sessionCacheTTLMax))))
	}
	if shared.Redis == nil {
		logger.Warn("accesscore: session cache disabled — GOCELL_SESSION_CACHE_TTL set but no Redis client " +
			"configured (set GOCELL_REDIS_ADDR or GOCELL_REDIS_CLUSTER_ADDRS)")
		return inner, nil
	}
	redisClient, ok := shared.Redis.Client().(*adapterredis.Client)
	if !ok {
		return nil, fmt.Errorf("accesscore: session cache: redis provider client is not *adapterredis.Client (got %T)",
			shared.Redis.Client())
	}
	cache, err := adapterredis.NewCache(redisClient, sessionCacheNamespace)
	if err != nil {
		return nil, fmt.Errorf("accesscore: session cache: %w", err)
	}
	cacheMetrics, err := obmetrics.NewSessionCacheCollector(shared.MetricsProvider, string(sessionCacheNamespace))
	if err != nil {
		return nil, fmt.Errorf("accesscore: session cache metrics: %w", err)
	}
	wrapped, err := adapterredis.NewCachingSessionStore(inner, cache, ttl, logger, cacheMetrics)
	if err != nil {
		return nil, fmt.Errorf("accesscore: session cache: %w", err)
	}
	logger.Info("accesscore: session cache enabled",
		slog.Duration("ttl", ttl),
		slog.String("namespace", string(sessionCacheNamespace)),
		slog.String("inner_store", fmt.Sprintf("%T", inner)))
	return wrapped, nil
}

// BootstrapAdminCredentials holds the env-driven credentials for the initial
// admin setup endpoint.
type BootstrapAdminCredentials struct {
	Username []byte
	Password []byte
}

// loadBootstrapCredentials reads admin credentials from env, validates them,
// and returns BootstrapAdminCredentials with nil fields when both are empty.
func loadBootstrapCredentials(username, password string) (BootstrapAdminCredentials, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)

	usernameSet := username != ""
	passwordSet := password != ""

	if usernameSet != passwordSet {
		return BootstrapAdminCredentials{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"GOCELL_BOOTSTRAP_ADMIN_USERNAME and GOCELL_BOOTSTRAP_ADMIN_PASSWORD "+
				"must both be set or both be empty")
	}

	if !usernameSet {
		return BootstrapAdminCredentials{}, nil
	}

	for _, r := range username {
		if unicode.IsControl(r) {
			return BootstrapAdminCredentials{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"GOCELL_BOOTSTRAP_ADMIN_USERNAME must not contain control characters")
		}
	}

	if len(password) < 8 {
		return BootstrapAdminCredentials{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"GOCELL_BOOTSTRAP_ADMIN_PASSWORD must be at least 8 bytes")
	}

	return BootstrapAdminCredentials{
		Username: []byte(username),
		Password: []byte(password),
	}, nil
}

// bootstrapLimiterResource adapts the rate limiter to the ManagedResource
// contract so phase10 shutdown stops the cleanup goroutine.
type bootstrapLimiterResource struct{ lim *ratelimit.Limiter }

func (bootstrapLimiterResource) Probes() []healthz.Probe {
	return nil
}
func (bootstrapLimiterResource) Worker() worker.Worker { return nil }
func (r bootstrapLimiterResource) Close(ctx context.Context) error {
	return r.lim.Close(ctx)
}

// bootstrapIPHashSaltDevDefault is the dev-mode fallback salt for the keyed
// client-IP hash. It is registered in cellsecrets.wellKnownDemoKeys so real mode
// rejects it (must set GOCELL_ACCESSCORE_IP_HASH_SALT to a fresh secret).
const bootstrapIPHashSaltDevDefault = "dev-ip-hash-salt-accesscore-32b!"

// newBootstrapAuthObserver returns an auth.BootstrapAuthFailObserver that:
//   - hashes the client IP once with the keyed, non-reversible redaction.HashIP
//     and uses that single value for BOTH the slog field and the wire payload
//     (#1488: the plaintext IP never crosses the outbox/broker/DLX boundary),
//   - lazily loads the cell via cellPtr (C7: atomic.Pointer forward reference),
//   - calls RecordBootstrapAuthFail with a 2s detached timeout.
//
// salt is the per-deployment secret (loaded by the caller via cellsecrets);
// the cell stays secret-free. cellPtr must be non-nil; *cellPtr is stored by the
// caller immediately after NewAccessCore returns. The observer fires only after
// HTTP servers start (post-Init), so *cellPtr is always non-nil by the time Load
// is called.
func newBootstrapAuthObserver(
	logger *slog.Logger,
	cellPtr *atomic.Pointer[accesscell.AccessCore],
	salt []byte,
) auth.BootstrapAuthFailObserver {
	return func(ctx context.Context, reason string) {
		ip, _ := ctxkeys.RealIPFrom(ctx)
		// Single keyed, non-reversible hash for both slog and the replayable
		// payload — no plaintext IP leaves this closure (#1488).
		ipHash := redaction.HashIP(salt, ip)
		logger.ErrorContext(ctx, "bootstrap_auth_failed",
			slog.String("namespace", "bootstrap"),
			slog.String("reason", reason),
			slog.String("client_ip_hash", ipHash.String()))
		c := cellPtr.Load()
		if c == nil {
			logger.ErrorContext(ctx, "bootstrap_audit_append_failed",
				slog.String("namespace", "bootstrap"),
				slog.String("auth_reason", reason),
				slog.String("failure", "cell not yet initialized"),
				slog.String("client_ip_hash", ipHash.String()))
			return
		}
		appendCtx, cancel := ctxutil.WithDetachedTimeout(ctx, bootstrapAppendDetachedTimeout)
		defer cancel()
		if err := c.RecordBootstrapAuthFail(appendCtx, reason, ipHash); err != nil {
			logger.ErrorContext(ctx, "bootstrap_audit_append_failed",
				slog.String("namespace", "bootstrap"),
				slog.String("auth_reason", reason),
				slog.String("client_ip_hash", ipHash.String()),
				slog.Bool("timeout", errors.Is(err, context.DeadlineExceeded)),
				slog.Any("error", err))
		}
	}
}

var _ composition.CellModule = module{}
