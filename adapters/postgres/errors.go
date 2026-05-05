package postgres

import "github.com/ghbvf/gocell/pkg/errcode"

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
)
