package postgres

import "github.com/ghbvf/gocell/pkg/errcode"

// Error codes for the PostgreSQL adapter.
const (
	// ErrAdapterPGNoTx indicates an operation that requires a transaction
	// context was called without one.
	ErrAdapterPGNoTx errcode.Code = "ERR_ADAPTER_PG_NO_TX"

	// ErrAdapterPGQuery indicates a query execution failure.
	ErrAdapterPGQuery errcode.Code = "ERR_ADAPTER_PG_QUERY"

	// ErrAdapterPGPublish indicates a failure to publish an outbox entry
	// to the message broker.
	ErrAdapterPGPublish errcode.Code = "ERR_ADAPTER_PG_PUBLISH"

	// ErrAdapterPGMarshal indicates a failure to serialize an outbox entry.
	ErrAdapterPGMarshal errcode.Code = "ERR_ADAPTER_PG_MARSHAL"
)
