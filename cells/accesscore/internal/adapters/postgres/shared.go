// Package postgres provides a PostgreSQL implementation of accesscore internal ports.
package postgres

import "github.com/ghbvf/gocell/pkg/errcode"

// errAdapterPGQuery is the shared error code for unexpected PostgreSQL query failures
// across all accesscore PG repositories (user, session, role). Mirrors the
// adapter-level sentinel (adapters/postgres ErrAdapterPGQuery = "ERR_ADAPTER_PG_QUERY")
// so monitoring can group all PG query failures under a single code regardless of
// which repo generated them.
//
// Unexported: only for use within this package. The adapter package cannot be
// re-imported because cells/ must not depend on adapters/ (depguard cells-isolation rule).
const errAdapterPGQuery errcode.Code = "ERR_ADAPTER_PG_QUERY"

// pgForeignKeyViolation is the PostgreSQL SQLSTATE code for foreign_key_violation.
// Triggered when an INSERT/UPDATE references a non-existent parent row.
// Added alongside migration 020_role_assignments_fk.sql which introduces FK
// constraints on role_assignments (user_id → users, role_id → roles).
const pgForeignKeyViolation = "23503"
