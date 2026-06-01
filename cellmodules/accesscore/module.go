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
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/ratelimit"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	accesscell "github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/cells/accesscore/configgetter"
	accessmem "github.com/ghbvf/gocell/cells/accesscore/mem"
	accesspg "github.com/ghbvf/gocell/cells/accesscore/postgres"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/auth"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/state/cas"
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

// Provide resolves all accesscore-specific dependencies and returns the
// constructed cell, bootstrap options, and lifecycle resources.
//
// Reads GOCELL_BOOTSTRAP_ADMIN_USERNAME, GOCELL_BOOTSTRAP_ADMIN_PASSWORD,
// GOCELL_ACCESSCORE_CURSOR_KEY, GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY from
// the environment.
func (m module) Provide(
	_ context.Context, shared *composition.SharedDeps, in composition.ModuleExports,
) (cell.Cell, composition.ModuleExports, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	creds, err := loadBootstrapCredentials(
		os.Getenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME"),
		os.Getenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD"),
	)
	if err != nil {
		return nil, composition.ModuleExports{}, nil, nil, err
	}
	if creds.Username == nil {
		return nil, composition.ModuleExports{}, nil, nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"GOCELL_BOOTSTRAP_ADMIN_USERNAME and GOCELL_BOOTSTRAP_ADMIN_PASSWORD are required "+
				"to protect setup/admin endpoint")
	}

	accessOpts, sessionProto, err := buildAccessBaseOpts(shared)
	if err != nil {
		return nil, composition.ModuleExports{}, nil, nil, err
	}

	innerSessionStore, storageOpts, err := resolveAccessStorageOpts(shared, sessionProto, accessOpts)
	if err != nil {
		return nil, composition.ModuleExports{}, nil, nil, err
	}
	accessOpts = storageOpts

	sessionStore, err := wrapSessionStoreWithCache(innerSessionStore, shared, nil)
	if err != nil {
		return nil, composition.ModuleExports{}, nil, nil, err
	}
	accessOpts = append(accessOpts, accesscell.WithSessionStore(sessionStore))

	// auditcore hands the bootstrap ledger store down via the typed
	// ModuleExports return; a nil here means accesscore was registered before
	// auditcore (MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01) — fail fast.
	if in.BootstrapLedgerStore == nil {
		return nil, composition.ModuleExports{}, nil, nil, errcode.New(errcode.KindInternal,
			errcode.ErrCellInvalidConfig,
			"accesscore: requires auditcore's BootstrapLedgerStore export; "+
				"register auditcore before accesscore")
	}

	// Construct BEFORE ratelimit.New: observer is a pure nil-check (no
	// goroutine, no resource), so its fail-fast must precede the limiter
	// which spawns a cleanup goroutine needing ManagedResource teardown.
	bootstrapAuthObserver, err := audit.NewBootstrapAuthFailObserver(
		slog.Default(), in.BootstrapLedgerStore, shared.Clock,
	)
	if err != nil {
		return nil, composition.ModuleExports{}, nil, nil, fmt.Errorf("accesscore: build bootstrap audit observer: %w", err)
	}
	rlLimiter := ratelimit.New(ratelimit.Config{
		Rate:  bootstrapRateLimitPerSec,
		Burst: bootstrapRateLimitBurst,
	}, shared.Clock)
	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{Username: creds.Username, Password: creds.Password},
		rlLimiter,
		bootstrapAuthObserver,
	)
	accessOpts = append(accessOpts, accesscell.WithBootstrapAuth(bootstrapMW))

	c := accesscell.NewAccessCore(shared.Clock, accessOpts...)
	// The bootstrap rate limiter spawns a cleanup goroutine, so it must be
	// managed in two places (pg-cell-template Chapter 4 contract):
	//   - opts: bootstrap.WithManagedResource so bootstrap.Run closes it at
	//     phase10 shutdown during the normal run (the steady-state lifecycle).
	//   - 4th return value: so Builder.Build closes it (LIFO) if a *later*
	//     module's Provide fails before bootstrap.Run starts (rollback).
	// The same value flows through both; bootstrap registers it once (only the
	// success path reaches bootstrap.Run), the rollback path only fires on
	// pre-Run failure, so there is no double-close.
	limiterRes := bootstrapLimiterResource{lim: rlLimiter}
	return c, composition.ModuleExports{},
		[]bootstrap.Option{bootstrap.WithManagedResource(limiterRes)},
		[]kernellifecycle.ManagedResource{limiterRes}, nil
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
		accesscell.WithOutboxDeps(outbox.WrapPublisherForCell(shared.EventBus), nil),
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

// accessPostgresOptions builds the postgres-specific accesscore options.
func accessPostgresOptions(shared *composition.SharedDeps, sessionProto *session.Protocol) ([]accesscell.Option, session.Store, error) {
	if shared.PG == nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: postgres mode requires the postgres capability provider " +
			"(the composition root must provision the postgres capability on SharedDeps before composition.Build)")
	}
	db, poolErr := cellsecrets.PgxPoolFromProvider(shared.PG)
	if poolErr != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: %w", poolErr)
	}
	txMgr := shared.PG.TxManager()
	pgBundle, err := accesspg.NewBundle(db, txMgr, shared.Clock)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGBundle: %w", err)
	}
	pgSessionStore, err := adapterpg.NewSessionStore(db, txMgr, sessionProto, shared.Clock)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGSessionStore: %w", err)
	}
	pgRefreshStore, err := adapterpg.NewRefreshStore(
		db, txMgr,
		accesscell.DefaultRefreshPolicy(), shared.Clock, rand.Reader,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("AccessCoreModule: PGRefreshStore: %w", err)
	}
	accessOpts := []accesscell.Option{
		accesscell.WithOutboxDeps(nil, outbox.WrapWriterForCell(shared.PG.OutboxWriter())),
		accesscell.WithPGBundle(pgBundle),
		accesscell.WithRefreshStore(pgRefreshStore),
	}
	// Wire the ConfigGetter using shared.InternalHMACRing (promoted from
	// cmd-private internalGuard.ring onto composition.SharedDeps).
	if shared.InternalHMACRing != nil {
		internalBaseURL := cellsecrets.InternalAddrToBaseURL(shared.InternalHTTPAddr)
		accessOpts = append(
			accessOpts,
			configgetter.WithHTTP(internalBaseURL, shared.InternalHMACRing, shared.Clock),
		)
	}
	return accessOpts, pgSessionStore, nil
}

// resolveAccessStorageOpts selects postgres or memory storage options.
func resolveAccessStorageOpts(
	shared *composition.SharedDeps,
	sessionProto *session.Protocol,
	base []accesscell.Option,
) (session.Store, []accesscell.Option, error) {
	if shared.Topology.StorageBackend() == "postgres" {
		pgOpts, pgSessionStore, err := accessPostgresOptions(shared, sessionProto)
		if err != nil {
			return nil, nil, err
		}
		return pgSessionStore, append(base, pgOpts...), nil
	}
	sessionMemStore, err := session.NewMemStore(sessionProto, shared.Clock)
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore: session.NewMemStore: %w", err)
	}
	refreshMemStore, err := refreshmem.New(accesscell.DefaultRefreshPolicy(), shared.Clock, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore: refreshmem.New: %w", err)
	}
	base = append(
		base,
		accesscell.WithMemBundle(accessmem.NewBundle(shared.Clock)),
		accesscell.WithRefreshStore(refreshMemStore),
	)
	return sessionMemStore, base, nil
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
	wrapped, err := adapterredis.NewCachingSessionStore(inner, cache, ttl, logger)
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

var _ composition.CellModule = module{}
