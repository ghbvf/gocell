// grpc.go owns the iotdevice-specific gRPC listener namespace. The shared
// env→TLS→server implementation lives in cellmodules/grpclistener so this
// example stays in lockstep with platform composition roots while retaining its
// standalone GOCELL_IOTDEVICE_GRPC_* env prefix.
package main

import (
	adaptersgrpc "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/cellmodules/grpclistener"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
)

const (
	envGRPCAddr     = "GOCELL_IOTDEVICE_GRPC_ADDR"
	defaultGRPCAddr = ":8084"
)

var iotDeviceGRPCEnv = grpclistener.EnvConfig{
	Prefix:      "GOCELL_IOTDEVICE_GRPC",
	DefaultAddr: defaultGRPCAddr,
}

// grpcAddrFromEnv resolves the gRPC listen address from the iotdevice env
// namespace. run.go passes the result to BOTH the adapter config and
// WithGRPCListener so the env var actually binds (#1737 F2).
func grpcAddrFromEnv() string {
	return grpclistener.AddrFromEnv(iotDeviceGRPCEnv)
}

// newGRPCServerFromEnv builds the gRPC server using the shared listener helper
// with the iotdevice env namespace.
func newGRPCServerFromEnv(
	durabilityMode outbox.DurabilityMode,
	addr string,
	grpcDeps interceptor.Deps,
) (*adaptersgrpc.Server, error) {
	return grpclistener.ServerFromEnv(iotDeviceGRPCEnv, durabilityMode, addr, grpcDeps)
}
