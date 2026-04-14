package prometheus

import "github.com/ghbvf/gocell/pkg/errcode"

// Prometheus adapter error codes.
const (
	// ErrAdapterPromConfig indicates an invalid Prometheus configuration.
	ErrAdapterPromConfig errcode.Code = "ERR_ADAPTER_PROM_CONFIG"

	// ErrAdapterPromRegister indicates a failure to register Prometheus metrics.
	ErrAdapterPromRegister errcode.Code = "ERR_ADAPTER_PROM_REGISTER"
)
