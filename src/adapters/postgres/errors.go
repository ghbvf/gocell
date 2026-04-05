package postgres

import "github.com/ghbvf/gocell/pkg/errcode"

// Adapter-level PostgreSQL error codes.
const (
	// ErrAdapterPGConnect indicates a connection failure to PostgreSQL.
	ErrAdapterPGConnect errcode.Code = "ERR_ADAPTER_PG_CONNECT"
	// ErrAdapterPGQuery indicates a query execution failure.
	ErrAdapterPGQuery errcode.Code = "ERR_ADAPTER_PG_QUERY"
	// ErrAdapterPGTx indicates a transaction failure.
	ErrAdapterPGTx errcode.Code = "ERR_ADAPTER_PG_TX"
	// ErrAdapterPGMigrate indicates a migration failure.
	ErrAdapterPGMigrate errcode.Code = "ERR_ADAPTER_PG_MIGRATE"
	// ErrAdapterPGNoTx indicates no transaction found in context.
	ErrAdapterPGNoTx errcode.Code = "ERR_ADAPTER_PG_NO_TX"
	// ErrAdapterPGNotFound indicates a record was not found.
	ErrAdapterPGNotFound errcode.Code = "ERR_ADAPTER_PG_NOT_FOUND"
)
