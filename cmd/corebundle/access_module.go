package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/ratelimit"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/cells/accesscore/configgetter"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

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

	accessOpts := []accesscore.Option{
		accesscore.WithClock(shared.Clock),
		accesscore.WithInMemoryDefaults(),
		// Publisher set unconditionally; outboxWriter set conditionally below.
		// cell.ResolveEmitter picks DirectEmitter(FailOpen) when writer is nil
		// (memory mode) and WriterEmitter when both pub+writer are non-nil (durable).
		accesscore.WithOutboxDeps(shared.EventBus, nil),
		accesscore.WithJWTIssuer(shared.JWTDeps.issuer),
		accesscore.WithJWTVerifier(shared.JWTDeps.verifier),
		accesscore.WithCursorCodec(cursorCodec),
		accesscore.WithMetricsProvider(shared.PromStack.metricProvider),
		accesscore.WithConfigEventCollector(shared.ConfigEventCollector),
		accesscore.WithRefreshGC(time.Hour, defaultRefreshGCRetention),
	}
	if shared.Topology.StorageBackend == "postgres" {
		if shared.SharedPGPool == nil {
			return nil, nil, nil, fmt.Errorf("AccessCoreModule: postgres mode requires SharedPGPool " +
				"(ConfigCoreModule must run before AccessCoreModule)")
		}
		writer := adapterpg.NewOutboxWriter(shared.Clock)
		txMgr := adapterpg.NewTxManager(shared.SharedPGPool)
		// Accumulative WithOutboxDeps: adds writer without replacing the publisher
		// set above. WithTxManager wires the TxRunner for L2 transactional atomicity.
		accessOpts = append(accessOpts,
			accesscore.WithOutboxDeps(nil, writer),
			accesscore.WithTxManager(txMgr),
		)
		// Build PGDeps once and share across all PG-backed repo factories so the
		// underlying pool/txRunner/clock are not repeated at each call site.
		// LAYER-10: PGDeps hides pgxpool.Pool behind the accesscore boundary.
		deps, err := accesscore.NewPGDeps(shared.SharedPGPool.DB(), txMgr, shared.Clock)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("AccessCoreModule: PGDeps: %w", err)
		}
		// Cell-private PG repos override the in-memory defaults installed by
		// WithInMemoryDefaults above (S3+S5: users/roles persisted in PG).
		//
		// HAZARD: session repository stays mem in S3+S5 even when PG storage backend
		// is selected. accesscore's PG-mode TxRunner writes user/role/outbox to PG
		// but session/refresh to mem — sessionlogin.persistSessionWithRefresh runs
		// mem writes inside a real PG tx. PG rollback does NOT unwind mem
		// session/refresh state. S4 wires the runtime session.Store + PG refresh
		// store and removes this hazard. Backlog: S4-PG-SESSION-REFRESH-WIRING-COMPLETE-01.
		pgUserRepo, err := accesscore.NewPGUserRepository(deps)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("AccessCoreModule: PGUserRepository: %w", err)
		}
		pgRoleRepo, err := accesscore.NewPGRoleRepository(deps)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("AccessCoreModule: PGRoleRepository: %w", err)
		}
		pgSetupLock, err := accesscore.NewPGSetupLock(deps)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("AccessCoreModule: PGSetupLock: %w", err)
		}
		accessOpts = append(accessOpts,
			accesscore.WithUserRepository(pgUserRepo),
			accesscore.WithRoleRepository(pgRoleRepo),
			accesscore.WithSetupLock(pgSetupLock),
		)
		// Wire the ConfigGetter for the configreceive slice to fetch entry values
		// from configcore's internal GET /internal/v1/config/{key} endpoint after
		// an upsert event (contract: http.config.internal.get.v1).
		// baseURL is constructed from InternalHTTPAddr. If the addr is a port-only
		// string (e.g. ":9090") we resolve to loopback; if host:port, prepend scheme.
		// The HMAC ring from InternalGuard is reused for outbound service-token signing.
		// If tests construct SharedDeps without InternalGuard, configreceive stays
		// in log-only mode.
		internalBaseURL := internalAddrToBaseURL(shared.InternalHTTPAddr)
		if shared.InternalGuard != nil {
			accessOpts = append(accessOpts,
				configgetter.WithHTTP(internalBaseURL, shared.InternalGuard.ring, shared.Clock),
			)
		}
	}
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
