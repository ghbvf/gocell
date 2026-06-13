package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// Compile-time assertions: Pool implements both lifecycle interfaces.
var (
	_ lifecycle.ContextCloser   = (*Pool)(nil)
	_ lifecycle.ManagedResource = (*Pool)(nil)
)

// Pool readiness-probe names. healthz.ProbeName-typed consts funneled by
// archtest PROBENAME-SEALED-FUNNEL-01 (snake_case + _ready).
const (
	// ProbeReady probes basic pool liveness (Ping).
	ProbeReady healthz.ProbeName = "postgres_ready"
	// ProbeIndexesValidReady probes that all expected indexes are valid.
	ProbeIndexesValidReady healthz.ProbeName = "postgres_indexes_valid_ready"
	// ProbeAppRoleRestrictedReady probes that the serving connection's current_user
	// is neither a superuser nor BYPASSRLS, so the FORCE ROW LEVEL SECURITY
	// tenant_isolation policies (migrations 052/053) are actually enforced at
	// runtime (#1676 [F-B11]). Registered only when Config.RequireRestrictedRole is
	// set — a serving pool; admin/utility pools (e.g. tools/pg-migrate) leave it off.
	ProbeAppRoleRestrictedReady healthz.ProbeName = "postgres_app_role_restricted_ready"
	// ProbeAuditAdminRestrictedReady probes that the OPTIONAL cross-tenant audit
	// admin pool's current_user is neither a superuser nor BYPASSRLS (#1810). The
	// gocell_audit_admin role reads every tenant via a role-scoped permissive RLS
	// SELECT policy (migration 065), NOT via BYPASSRLS — so it must stay NOBYPASSRLS
	// and minimally privileged. This distinctly-named probe (vs the serving pool's
	// postgres_app_role_restricted_ready) asserts that at runtime AND makes the
	// optional admin pool's liveness visible on /readyz; it reuses
	// Pool.AppRoleRestrictedCheck. Registered by the auditcore module's admin-pool
	// ManagedResource only when GOCELL_AUDIT_ADMIN_DSN is provisioned.
	ProbeAuditAdminRestrictedReady healthz.ProbeName = "postgres_audit_admin_restricted_ready"
)

// Default pool configuration values.
const (
	defaultMaxConns       = 10
	defaultIdleTimeout    = 5 * time.Minute
	defaultMaxLifetime    = 1 * time.Hour
	defaultHealthTimeout  = 5 * time.Second
	defaultConnectTimeout = 5 * time.Second
)

// Config holds PostgreSQL connection pool settings.
// All fields must be set explicitly by the caller; there is no global env-reading
// constructor.
type Config struct {
	// DSN is the PostgreSQL connection string (e.g.
	// "postgres://user:pass@localhost:5432/dbname?sslmode=disable").
	DSN string

	// MaxConns is the maximum number of connections in the pool.
	// Default (applied by applyDefaults): 10.
	MaxConns int32

	// IdleTimeout is how long an idle connection may remain in the pool.
	// Default (applied by applyDefaults): 5m.
	IdleTimeout time.Duration

	// MaxLifetime is the maximum lifetime of a connection.
	// Default (applied by applyDefaults): 1h.
	MaxLifetime time.Duration

	// ConnectTimeout bounds a single connection-establishment attempt
	// (TCP+TLS+startup) at the pgconn layer. It applies to the pool's initial
	// connect AND every subsequent on-demand connection top-up at runtime —
	// without it, pgxpool falls back to its 2 min internal default and a TCP
	// SYN-blackhole route can hang any acquire for that long.
	//
	// The total budget for NewPool itself (parse + initial dial + Ping) is
	// the caller's ctx; this field only governs per-connection handshakes.
	//
	// Precedence: when set > 0, the value is written to
	// pgconn.Config.ConnectTimeout unconditionally, overriding any
	// connect_timeout=N in the DSN. Default (applied by applyDefaults): 5s.
	ConnectTimeout time.Duration

	// RequireRestrictedRole declares this pool as a SERVING pool for
	// RLS-protected tables: when true, Probes() additionally exposes
	// ProbeAppRoleRestrictedReady, which fails /readyz if the connection's
	// current_user is a superuser or carries BYPASSRLS (either bypasses the
	// FORCE ROW LEVEL SECURITY tenant_isolation policies, making them a runtime
	// no-op — #1676 [F-B11]). Leave false for admin/migration pools (e.g.
	// tools/pg-migrate) and utility pools, which legitimately connect as an
	// owner/superuser. This is a present capability axis (serving vs admin), not
	// a backward-compat toggle: corebundle's serving pool sets it true because
	// its schema (migrations 052/053) always carries RLS.
	RequireRestrictedRole bool
}

// applyDefaults fills zero-valued fields with default values.
func (c *Config) applyDefaults() {
	if c.MaxConns <= 0 {
		c.MaxConns = defaultMaxConns
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.MaxLifetime <= 0 {
		c.MaxLifetime = defaultMaxLifetime
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = defaultConnectTimeout
	}
}

// Pool wraps a pgxpool.Pool with health checking and lifecycle management.
// It implements lifecycle.ManagedResource directly, so callers can pass a
// *Pool to bootstrap.WithManagedResource without a wrapper.
type Pool struct {
	inner  *pgxpool.Pool
	config Config

	// checkerHealthFnForTest is non-nil only in unit tests; it replaces
	// Pool.Health in Checkers() so probes can be exercised without a real DB.
	checkerHealthFnForTest func(ctx context.Context) error
}

// NewPool creates a new connection pool from the supplied Config.
// It validates the DSN, applies defaults, and pings the database to confirm
// connectivity.
func NewPool(ctx context.Context, cfg Config) (*Pool, error) {
	cfg.applyDefaults()

	if cfg.DSN == "" {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterPGConnect, "postgres DSN is empty")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGConnect, "postgres: parse DSN", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MaxConnIdleTime = cfg.IdleTimeout
	poolCfg.MaxConnLifetime = cfg.MaxLifetime
	// Override DSN connect_timeout (if any). applyDefaults guarantees
	// cfg.ConnectTimeout > 0 here, so this write is unconditional.
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	// Defense-in-depth tenant-checkout guard (PR-3 #1341): fail any tenant-scoped
	// connection checkout that bypasses RunInTx (see tenantScopePrepareConn).
	poolCfg.PrepareConn = tenantScopePrepareConn

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGConnect, "postgres: create pool", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, classifyPGConnectError(err, "initial ping")
	}

	slog.Info("postgres pool connected",
		slog.String("host", poolCfg.ConnConfig.Host),
		slog.Int("port", int(poolCfg.ConnConfig.Port)),
		slog.Int("max_conns", int(cfg.MaxConns)),
		slog.Duration("connect_timeout", cfg.ConnectTimeout),
	)

	return &Pool{inner: pool, config: cfg}, nil
}

// tenantScopePrepareConn is the pgxpool PrepareConn hook providing DEFENSE IN
// DEPTH for PR-3 row-level security (#1341). It is NOT the primary enforcement —
// that is the DB-side FORCE ROW LEVEL SECURITY policy (a checkout that never sets
// app.tenant_id sees 0 rows, fail-closed) plus the PR-2 typed TenantID repo
// parameter (compile-time). This hook turns one specific liveness bug LOUD: a
// deliberately tenant-scoped context (tenant.WithScope was called) that acquires
// a connection WITHOUT going through TxRunner.RunInTx — i.e. business code that
// declared a tenant scope but then ran a statement on a raw pool connection,
// skipping the SET LOCAL app.tenant_id injection.
//
// Keyed on tenant.ScopeFromContext (the deliberate scope signal), NOT on
// ctxkeys.TenantID: a post-auth principal context legitimately reads non-RLS
// tables on raw pool connections, so keying on the principal carrier would
// false-positive. RunInTx stamps txAcquireMarker on its Begin context, so a
// scoped checkout originating from RunInTx is allowed; only a scoped checkout
// lacking the marker is rejected.
//
// PrepareConn return contract (pgx v5): returning (true, err) keeps the
// connection healthy in the pool and fails ONLY the instigating query with err
// — a clean fail-fast with no connection churn (unlike the deprecated
// BeforeAcquire bool hook, whose false return destroys connections and yields a
// generic "too many failed attempts" pool error).
//
// Feasibility note (honest, see ADR): this hook does NOT catch code that strips
// the tenant scope and runs raw SQL — that path acquires unscoped and is allowed
// here, with the DB RLS 0-row policy as the backstop. It is defense-in-depth, not
// a replacement for the RLS policy or the typed parameter.
func tenantScopePrepareConn(ctx context.Context, _ *pgx.Conn) (bool, error) {
	if _, scoped := tenant.ScopeFromContext(ctx); scoped && !hasTxAcquireIntent(ctx) {
		return true, errcode.New(errcode.KindInternal, ErrAdapterPGQuery,
			"postgres: tenant-scoped connection checkout outside RunInTx; tenant-scoped "+
				"data access must go through TxRunner.RunInTx so the RLS app.tenant_id GUC "+
				"(SET LOCAL) is set before any statement runs")
	}
	return true, nil
}

// DB returns the underlying pgxpool.Pool for direct access.
func (p *Pool) DB() *pgxpool.Pool {
	return p.inner
}

// Health performs a ping against the database and returns nil if healthy.
func (p *Pool) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, defaultHealthTimeout)
	defer cancel()

	if err := p.inner.Ping(ctx); err != nil {
		return classifyPGConnectError(err, "health check")
	}
	return nil
}

// AppRoleRestrictedCheck verifies that the pool's serving connection runs as a
// role that ROW LEVEL SECURITY actually constrains: neither a superuser nor a
// BYPASSRLS role. Either bypasses RLS regardless of FORCE, so the tenant_isolation
// policies (migrations 052/053) would silently leak across tenants at runtime.
//
// It backs the ProbeAppRoleRestrictedReady readyz probe (#1676 [F-B11]) and runs
// only on pools that opt in via Config.RequireRestrictedRole. The catalog read is
// SELECT-only on pg_roles (readable by any role) keyed on current_user, so it is
// safe for the restricted serving role itself to execute.
func (p *Pool) AppRoleRestrictedCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, defaultHealthTimeout)
	defer cancel()

	var rolsuper, rolbypassrls bool
	if err := p.inner.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&rolsuper, &rolbypassrls); err != nil {
		// A missing current_user row or a read failure is itself a precondition
		// failure: we cannot prove the serving role is restricted.
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGRoleBypassRLS,
			"postgres: serving-role RLS precondition probe failed", err)
	}
	return appRoleRestrictedResult(rolsuper, rolbypassrls)
}

// appRoleRestrictedResult is the pure (DB-free) verdict for the serving-role RLS
// precondition: a role is acceptable only when it is neither a superuser nor
// BYPASSRLS. Split out so the decision is table-testable without a real database.
func appRoleRestrictedResult(rolsuper, rolbypassrls bool) error {
	if rolsuper || rolbypassrls {
		return errcode.New(errcode.KindInternal, ErrAdapterPGRoleBypassRLS,
			"postgres: serving role bypasses row-level security (superuser or BYPASSRLS); "+
				"FORCE ROW LEVEL SECURITY tenant isolation is not enforced at runtime — "+
				"serve from a NOSUPERUSER NOBYPASSRLS non-owner role")
	}
	return nil
}

// auditAdminRoleResult is the pure (DB-free) verdict combining role-attribute
// and SELECT-capability checks for the audit admin pool. It is split out so
// the combined decision is table-testable without a real database.
//
// The audit admin role must be: (1) non-superuser and non-BYPASSRLS (it reads
// via its role-scoped permissive RLS policy, NOT via privilege bypass — ADR
// #1676), AND (2) capable of SELECT on audit_entries (a missing GRANT renders
// the admin pool useless and must be caught at composition time — #1810 F3/F4).
func auditAdminRoleResult(rolsuper, rolbypassrls, canSelect bool) error {
	if err := appRoleRestrictedResult(rolsuper, rolbypassrls); err != nil {
		return err
	}
	if !canSelect {
		return errcode.New(errcode.KindInternal, ErrAdapterPGAuditAdminSelectCheck,
			"postgres: audit admin role lacks SELECT privilege on audit_entries; "+
				"grant SELECT on audit_entries to gocell_audit_admin (migration 065 must "+
				"have run and the GRANT applied before the admin pool is provisioned)")
	}
	return nil
}

// AuditAdminReadyCheck is a combined readiness check for the optional
// cross-tenant audit admin pool (#1810). It asserts both:
//
//  1. The pool's current_user is neither a superuser nor BYPASSRLS (role-attribute
//     check, reusing appRoleRestrictedResult). The gocell_audit_admin role reads
//     cross-tenant via a role-scoped permissive RLS SELECT policy (migration 065),
//     NOT via BYPASSRLS, so it must stay NOSUPERUSER NOBYPASSRLS (ADR #1676).
//
//  2. The pool's current_user has SELECT privilege on audit_entries
//     (SELECT has_table_privilege(current_user, 'audit_entries', 'SELECT')).
//     A missing GRANT means the admin pool passes the role-attribute probe but
//     fails every cross-tenant query at runtime — readyz must catch this.
//
// This single method is reused at both composition-time fail-fast
// (buildCrossTenantStore calls it once on startup) and in the
// ProbeAuditAdminRestrictedReady /readyz probe (auditAdminPoolResource.Probes).
func (p *Pool) AuditAdminReadyCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, defaultHealthTimeout)
	defer cancel()

	var rolsuper, rolbypassrls, canSelect bool
	row := p.inner.QueryRow(ctx, `
		SELECT
			r.rolsuper,
			r.rolbypassrls,
			has_table_privilege(current_user, 'audit_entries', 'SELECT')
		FROM pg_roles r
		WHERE r.rolname = current_user
	`)
	if err := row.Scan(&rolsuper, &rolbypassrls, &canSelect); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGAuditAdminSelectCheck,
			"postgres: audit admin role readiness probe failed", err)
	}
	return auditAdminRoleResult(rolsuper, rolbypassrls, canSelect)
}

// Close gracefully shuts down the connection pool, bounded by ctx.
//
// pgxpool.Pool.Close() performs a synchronous drain with no context parameter.
// Close wraps it in a goroutine so the caller's shutdown budget is honored;
// if ctx expires, the pool's connection resources are abandoned (process-exit
// cleanup semantics, acceptable under orchestrator-restart SLO).
//
// Close is idempotent: calling it on an already-closed pool is safe.
//
// ref: uber-go/fx app.go StopTimeout — ctx as shared shutdown budget.
// ref: uber-go/fx lifecycle OnStop(ctx) — ContextCloser pattern.
func (p *Pool) Close(ctx context.Context) error {
	return adapterutil.CloseWithDeadline(ctx, "postgres", func() error {
		if p.inner == nil {
			return nil
		}
		p.inner.Close()
		return nil
	})
}

// Probes returns the pool's typed readiness probes contributing to /readyz:
//
//  1. ProbeReady (= "postgres_ready") — pings the PG pool connection via
//     Pool.Health.
//  2. ProbeIndexesValidReady (= "postgres_indexes_valid_ready") — calls
//     InvalidIndexCheck to surface any indexes left invalid by an interrupted
//     CREATE INDEX CONCURRENTLY.
//  3. ProbeAppRoleRestrictedReady (= "postgres_app_role_restricted_ready") —
//     present ONLY when Config.RequireRestrictedRole is set (a serving pool):
//     verifies current_user is neither superuser nor BYPASSRLS so FORCE RLS is
//     effective at runtime (#1676 [F-B11]). Admin/migration pools leave the flag
//     off and so do not register it.
//
// Every probe caps its inner wait at adapterutil.DefaultProbeTimeout (5 s)
// so a slow PG does not hold the /readyz response indefinitely.
//
// ref: kubernetes/kubernetes pkg/util/healthz — named health checkers.
// ref: uber-go/fx app.go StopTimeout — shared shutdown budget via ctx.
func (p *Pool) Probes() []healthz.Probe {
	healthFn := p.checkerHealthFnForTest
	if healthFn == nil {
		healthFn = p.Health
	}
	probes := []healthz.Probe{
		adapterutil.HealthToProbe(ProbeReady, healthFn, adapterutil.DefaultProbeTimeout),
		adapterutil.HealthToProbe(ProbeIndexesValidReady, func(ctx context.Context) error {
			return InvalidIndexCheck(ctx, p)
		}, adapterutil.DefaultProbeTimeout),
	}
	if p.config.RequireRestrictedRole {
		probes = append(probes,
			adapterutil.HealthToProbe(ProbeAppRoleRestrictedReady, p.AppRoleRestrictedCheck,
				adapterutil.DefaultProbeTimeout))
	}
	return probes
}

// Worker returns nil — Pool has no background goroutine. The outbox relay is
// registered as a separate ManagedResource via bootstrap.WithManagedResource
// so its lifecycle is independently managed.
func (p *Pool) Worker() kworker.Worker {
	return nil
}

// PoolStats holds structured connection pool statistics.
//
// ref: pgxpool Stat() — adopted same field set for operational dashboards
// and Prometheus/OTel metric collectors.
type PoolStats struct {
	AcquireCount            int64         `json:"acquireCount"`
	AcquireDuration         time.Duration `json:"acquireDuration"`
	AcquiredConns           int32         `json:"acquiredConns"`
	CanceledAcquireCount    int64         `json:"canceledAcquireCount"`
	ConstructingConns       int32         `json:"constructingConns"`
	EmptyAcquireCount       int64         `json:"emptyAcquireCount"`
	IdleConns               int32         `json:"idleConns"`
	MaxConns                int32         `json:"maxConns"`
	TotalConns              int32         `json:"totalConns"`
	NewConnsCount           int64         `json:"newConnsCount"`
	MaxLifetimeDestroyCount int64         `json:"maxLifetimeDestroyCount"`
	MaxIdleDestroyCount     int64         `json:"maxIdleDestroyCount"`
}

// PoolStats returns structured pool statistics suitable for metrics collection
// and operational dashboards. Returns zero-value PoolStats if the pool is not
// initialized (defensive guard, consistent with Redis adapter pattern).
func (p *Pool) PoolStats() PoolStats {
	if p.inner == nil {
		return PoolStats{}
	}
	s := p.inner.Stat()
	return PoolStats{
		AcquireCount:            s.AcquireCount(),
		AcquireDuration:         s.AcquireDuration(),
		AcquiredConns:           s.AcquiredConns(),
		CanceledAcquireCount:    s.CanceledAcquireCount(),
		ConstructingConns:       s.ConstructingConns(),
		EmptyAcquireCount:       s.EmptyAcquireCount(),
		IdleConns:               s.IdleConns(),
		MaxConns:                s.MaxConns(),
		TotalConns:              s.TotalConns(),
		NewConnsCount:           s.NewConnsCount(),
		MaxLifetimeDestroyCount: s.MaxLifetimeDestroyCount(),
		MaxIdleDestroyCount:     s.MaxIdleDestroyCount(),
	}
}

// Stats returns pool statistics as a formatted string for diagnostics.
func (p *Pool) Stats() string {
	if p == nil || p.inner == nil {
		return "pool not initialized"
	}
	s := p.inner.Stat()
	return fmt.Sprintf(
		"total=%d idle=%d acquired=%d constructing=%d max=%d",
		s.TotalConns(), s.IdleConns(), s.AcquiredConns(),
		s.ConstructingConns(), s.MaxConns(),
	)
}
