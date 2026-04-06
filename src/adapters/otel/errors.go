package otel

import "github.com/ghbvf/gocell/pkg/errcode"

// OTel adapter error codes.
const (
	// ErrAdapterOTelConfig indicates an invalid OTel configuration.
	ErrAdapterOTelConfig errcode.Code = "ERR_ADAPTER_OTEL_CONFIG"

	// ErrAdapterOTelInit indicates a failure during OTel provider initialization.
	ErrAdapterOTelInit errcode.Code = "ERR_ADAPTER_OTEL_INIT"

	// ErrAdapterOTelShutdown indicates a failure during OTel provider shutdown.
	ErrAdapterOTelShutdown errcode.Code = "ERR_ADAPTER_OTEL_SHUTDOWN"
)
