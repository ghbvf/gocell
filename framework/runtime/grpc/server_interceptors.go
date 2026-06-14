package grpc

import (
	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// ServerInterceptors is the adapter-consumable gRPC interceptor bundle.
//
// It deliberately lives in runtime/grpc, not adapters/grpc, so adapters/grpc
// does not import runtime/grpc/interceptor (GRPC-ADAPTER-LAYER-01). The normal
// constructor is interceptor.NewServerInterceptors(deps), which fixes the bundle
// to the unary + stream chains built from one deps value.
type ServerInterceptors struct {
	options   []grpc.ServerOption
	registrar *ServiceRegistrar
	drain     *DrainSignal
}

// NewServerInterceptorsBundle packages pre-built server options with the shared
// registrar and drain signal. It is intentionally low-level: composition roots
// should call interceptor.NewServerInterceptors(deps), which supplies the full
// unary + stream chain pair from one deps object.
func NewServerInterceptorsBundle(
	options []grpc.ServerOption,
	registrar *ServiceRegistrar,
	drain *DrainSignal,
) ServerInterceptors {
	return ServerInterceptors{
		options:   append([]grpc.ServerOption(nil), options...),
		registrar: registrar,
		drain:     drain,
	}
}

// Validate reports whether b has enough state for adapters/grpc.New to build a
// server without silently dropping attribution, auth, or stream drain.
func (b ServerInterceptors) Validate() error {
	if len(b.options) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"grpc: ServerInterceptors must contain the unary and stream interceptor chains")
	}
	if b.registrar == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"grpc: ServerInterceptors registrar is required")
	}
	if err := b.drain.Validate(); err != nil {
		return err
	}
	return nil
}

// ServerOptions returns a defensive copy of the bundled grpc.ServerOptions.
func (b ServerInterceptors) ServerOptions() []grpc.ServerOption {
	return append([]grpc.ServerOption(nil), b.options...)
}

// Registrar returns the shared method attribution registrar.
func (b ServerInterceptors) Registrar() *ServiceRegistrar { return b.registrar }

// Drain returns the shared graceful-stop drain signal.
func (b ServerInterceptors) Drain() *DrainSignal { return b.drain }
