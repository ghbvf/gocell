package postgres

import "github.com/ghbvf/gocell/pkg/errcode"

// PostgreSQL adapter error codes.
const (
	// ErrAdapterPGConnect indicates a connection or pool initialization failure.
	ErrAdapterPGConnect errcode.Code = "ERR_ADAPTER_PG_CONNECT"

	// ErrAdapterPGQuery indicates a query execution failure.
	ErrAdapterPGQuery errcode.Code = "ERR_ADAPTER_PG_QUERY"

	// ErrAdapterPGTxTimeout indicates a transaction exceeded its deadline or was
	// aborted due to context cancellation.
	ErrAdapterPGTxTimeout errcode.Code = "ERR_ADAPTER_PG_TX_TIMEOUT"

	// ErrAdapterPGMigrate indicates a migration execution or tracking failure.
	ErrAdapterPGMigrate errcode.Code = "ERR_ADAPTER_PG_MIGRATE"
)
