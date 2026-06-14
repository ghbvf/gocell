package postgres

import (
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// PostgreSQL adapter error codes.
const (
	// ErrAdapterPGConnect indicates a connection or pool initialization failure.
	ErrAdapterPGConnect errcode.Code = "ERR_ADAPTER_PG_CONNECT"

	// ErrAdapterPGConnectTimeout indicates a connection attempt exceeded the
	// adapter-level ConnectTimeout budget (default 5s). Distinct from
	// ErrAdapterPGConnect so operators can route timeout-vs-refusal differently;
	// always routed via errcode.WrapInfra → IsTransient(err) == true.
	ErrAdapterPGConnectTimeout errcode.Code = "ERR_ADAPTER_PG_CONNECT_TIMEOUT"

	// ErrAdapterPGQuery indicates a query execution failure.
	ErrAdapterPGQuery errcode.Code = "ERR_ADAPTER_PG_QUERY"

	// ErrAdapterPGMigrate indicates a migration execution or tracking failure.
	ErrAdapterPGMigrate errcode.Code = "ERR_ADAPTER_PG_MIGRATE"

	// ErrAdapterPGNoTx indicates outbox.Writer.Write was called outside a transaction.
	ErrAdapterPGNoTx errcode.Code = "ERR_ADAPTER_PG_NO_TX"

	// ErrAdapterPGMarshal indicates a JSON marshal failure for outbox entry.
	ErrAdapterPGMarshal errcode.Code = "ERR_ADAPTER_PG_MARSHAL"

	// ErrAdapterPGPublish indicates the outbox relay failed to publish an entry.
	ErrAdapterPGPublish errcode.Code = "ERR_ADAPTER_PG_PUBLISH"

	// ErrAdapterPGSchemaMismatch indicates the DB schema version does not match
	// the expected version derived from the embedded migration files.
	ErrAdapterPGSchemaMismatch errcode.Code = "ERR_ADAPTER_PG_SCHEMA_MISMATCH"

	// ErrAdapterPGSchemaShape indicates the DB schema's column / table shape
	// does not match the expected shape after migration. Distinct from
	// ErrAdapterPGSchemaMismatch (version-level) so operators can route
	// "binary expected sessions.jti but DB still has sessions.access_token"
	// (partial migration) separately from "binary is at version N+1 vs DB at N".
	ErrAdapterPGSchemaShape errcode.Code = "ERR_ADAPTER_PG_SCHEMA_SHAPE"

	// ErrAdapterPGInvalidIndex signals one or more `pg_index.indisvalid = false`
	// indexes detected at startup. Replaces the prior warn-continue behavior
	// (B2-X-03) — invalid indexes typically indicate an aborted CREATE INDEX
	// CONCURRENTLY and must be DROPped manually before the binary may proceed.
	ErrAdapterPGInvalidIndex errcode.Code = "ERR_ADAPTER_PG_INVALID_INDEX"

	// ErrAdapterPGRoleBypassRLS signals that the serving connection's current_user
	// is a superuser or carries BYPASSRLS — either bypasses ROW LEVEL SECURITY,
	// making the FORCE RLS tenant_isolation policies (migrations 052/053) a runtime
	// no-op. Surfaced by the postgres_app_role_restricted_ready precondition probe
	// (#1676 [F-B11]); a serving pool that trips this must NOT be considered ready.
	//nolint:gosec // G101 false positive: errcode sentinel naming the PG BYPASSRLS role attribute, not a credential
	ErrAdapterPGRoleBypassRLS errcode.Code = "ERR_ADAPTER_PG_ROLE_BYPASS_RLS"

	// ErrAdapterPGAuditAdminSelectCheck signals that the audit admin pool's
	// current_user lacks SELECT privilege on audit_entries, or that the privilege
	// check itself failed. Surfaced by Pool.AuditAdminReadyCheck (#1810 F4): the
	// gocell_audit_admin role must be able to SELECT on audit_entries (via its
	// role-scoped permissive RLS policy, migration 065) — a missing GRANT would
	// cause the admin pool to pass the role-attribute probe yet fail every
	// cross-tenant read request at runtime.
	ErrAdapterPGAuditAdminSelectCheck errcode.Code = "ERR_ADAPTER_PG_AUDIT_ADMIN_SELECT_CHECK"
)
