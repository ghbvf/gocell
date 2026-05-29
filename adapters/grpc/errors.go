package grpc

import "github.com/ghbvf/gocell/pkg/errcode"

// gRPC adapter error codes.
const (
	// ErrAdapterGRPCConfigInvalid indicates a configuration validation failure
	// (missing address, conflicting TLS/insecure flags, missing cert/key).
	ErrAdapterGRPCConfigInvalid errcode.Code = "ERR_ADAPTER_GRPC_CONFIG_INVALID"

	// ErrAdapterGRPCTLSConfig indicates a TLS configuration build failure
	// (PEM parse error, cert/key mismatch, CA pool error).
	ErrAdapterGRPCTLSConfig errcode.Code = "ERR_ADAPTER_GRPC_TLS_CONFIG"

	// ErrAdapterGRPCListen indicates a net.Listen failure at server startup.
	ErrAdapterGRPCListen errcode.Code = "ERR_ADAPTER_GRPC_LISTEN"

	// ErrAdapterGRPCServe indicates a grpcServer.Serve failure (non-graceful).
	ErrAdapterGRPCServe errcode.Code = "ERR_ADAPTER_GRPC_SERVE"
)
