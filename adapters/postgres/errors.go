package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// PostgreSQL adapter error codes.
const (
	// ErrAdapterPGConnect indicates a connection or pool initialization failure.
	ErrAdapterPGConnect errcode.Code = "ERR_ADAPTER_PG_CONNECT"

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
)

// PG SQLSTATE codes used by repo error classifiers. PG codes are stable
// identifiers and are not language-dependent, so ad-hoc string compares are
// the idiomatic Go convention.
//
// ref: https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	// SQLStateUniqueViolation is class 23 / 23505 (unique constraint).
	// pgconn.PgError.Code carries this value verbatim.
	SQLStateUniqueViolation = "23505"
	// SQLStateForeignKeyViolation is class 23 / 23503.
	SQLStateForeignKeyViolation = "23503"
	// SQLStateRaiseException is the catch-all class P0001 used by
	// PL/pgSQL `RAISE EXCEPTION` (e.g. last_admin_protected trigger).
	SQLStateRaiseException = "P0001"
)

// IsUniqueViolation reports whether err (or any error in its Unwrap chain)
// is a PG unique-constraint violation (SQLSTATE 23505). Repo callers wrap
// the result as a domain ErrAuth*Duplicate to keep the wire-level errcode
// stable across mem and PG backends.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == SQLStateUniqueViolation
}

// IsForeignKeyViolation reports whether err is a PG foreign-key violation
// (SQLSTATE 23503). Used by role_assignments to classify a delete that
// would orphan downstream rows.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == SQLStateForeignKeyViolation
}

// IsLastAdminProtected reports whether err is the PL/pgSQL exception raised
// by the effective_admin_invariant_fn trigger function
// (migrations/024_effective_admin_invariant.sql). Distinct from the bare
// SQLSTATE check because P0001 is a generic class — we also need the trigger
// sentinel in the MESSAGE field to avoid catching unrelated RAISE EXCEPTION
// sites.
//
// S4.0 (migration 024) renamed the trigger function from
// `last_admin_protected_fn` → `effective_admin_invariant_fn` and changed the
// message prefix accordingly. The 019 trigger / function are fully retired.
func IsLastAdminProtected(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != SQLStateRaiseException {
		return false
	}
	// Match prefix only — the trigger function emits
	// 'effective_admin_invariant: would leave the system with no effective admin'
	// (see migrations/024_effective_admin_invariant.sql).
	const triggerSentinel = "effective_admin_invariant"
	for i := 0; i+len(triggerSentinel) <= len(pgErr.Message); i++ {
		if pgErr.Message[i:i+len(triggerSentinel)] == triggerSentinel {
			return true
		}
	}
	return false
}
