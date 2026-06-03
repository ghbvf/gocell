package cell

// grpc_service.go — GRPCServiceSpec declaration type (GAP-1 PR-7 [#1150]).
//
// Layering: kernel/ must NOT import google.golang.org/grpc. The Register field
// therefore holds any (the zero-import representation of a callback). The expected
// dynamic type is func(grpc.ServiceRegistrar); the type-assert is performed by the
// runtime layer (runtime/grpc.ServiceRegistrar.Register).
//
// This is the symmetric counterpart of RouteGroup (HTTP) and follows the same
// write-side-accumulate / read-side-drain split: cells declare GRPCServiceSpec
// during Init via reg.GRPCService(spec), and bootstrap drains GRPCServices from
// the RegistrySnapshot in phase7b before calling grpcServeAll.
//
// AI-robust note: Register is `any` — not a named interface — because kernel⊥grpc
// makes naming func(grpc.ServiceRegistrar) impossible. The archtest
// GRPC-CELL-REGISTRAR-LAYER-01 reflect-locks this field to stay untyped any;
// the kernel⊥grpc import-ban itself is enforced by depguard + kernel_internal_dag_test.go.
// Sealing the field (permanent-ceiling won't-do) is tracked as a gh issue in the
// archtest godoc — same family as #851/#893/#1282.
//
// ref: zeromicro/go-zero zrpc/internal/rpcserver.go — RegisterFn func(*grpc.Server) (Form B precedent)
// ref: go-kratos/kratos transport/grpc/server.go — pb.RegisterXxxServer before Start

import (
	"github.com/ghbvf/gocell/pkg/errcode"
)

// GRPCServiceRegistrar is the narrow cell-service-registration contract exposed
// by adapters/grpc.Server and consumed by runtime/bootstrap's drain (GAP-1 PR-7
// [#1150]). It lives in kernel/cell so both layers can import it without cycles:
// kernel/ is the bottom of the dependency graph.
//
// *runtime/grpc.ServiceRegistrar satisfies this interface structurally.
type GRPCServiceRegistrar interface {
	// Register invokes the spec.Register callback on a cellScopedRegistrar
	// interceptor, recording method→cellID attribution for each service.
	Register(spec GRPCServiceSpec) error
}

// GRPCServiceSpec declares a gRPC service that a cell wants to expose on a
// specific listener. The registration callback (Register) is the Form B idiom
// popularized by go-zero (RegisterFn) and go-kratos (pb.RegisterXxxServer):
// the cell supplies a closure that calls the generated pb.RegisterXxxServer
// helper, which internally calls grpc.ServiceRegistrar.RegisterService with the
// fully-populated *ServiceDesc.
//
// Kernel holds Register as any (not func(grpc.ServiceRegistrar)) because kernel/
// must not import google.golang.org/grpc. The type-assert from any to the
// concrete func type happens in runtime/grpc.ServiceRegistrar.Register; a wrong
// dynamic type panics via panicregister.Approved("grpc-registrar-bad-register-fn", …).
//
// # Usage in Cell.Init
//
//	func (c *MyCell) Init(ctx context.Context, reg cell.Registrar) error {
//	    return reg.GRPCService(cell.GRPCServiceSpec{
//	        ContractID: "grpc.myservice.v1",
//	        CellID:     c.ID(),
//	        Listener:   cell.PrimaryListener,
//	        Register:   func(r grpc.ServiceRegistrar) {
//	            pb.RegisterMyServiceServer(r, c.svc)
//	        },
//	    })
//	}
type GRPCServiceSpec struct {
	// ContractID is the stable contract identifier (matches contract.yaml id).
	// Used for deduplication: a cell may not register the same ContractID twice.
	ContractID string

	// CellID is the cell that owns this service. bootstrap asserts spec.CellID
	// equals the snapshot key when draining (mirrors the subscription drain check).
	CellID string

	// Listener identifies the target gRPC listener (must match a ref passed to
	// WithGRPCListener). Symmetric with RouteGroup.Listener for HTTP.
	Listener ListenerRef

	// Register holds a func(grpc.ServiceRegistrar) callback. Kernel stores it as
	// any to avoid a grpc import dependency. The runtime layer type-asserts it.
	// A nil value fails Validate; a non-func value panics in runtime/grpc at
	// registration time (panicregister.Approved("grpc-registrar-bad-register-fn",…)).
	Register any
}

// Validate returns a non-nil error when any required field is missing.
// Called by RegistryRecorder.GRPCService before accumulating the spec.
func (s GRPCServiceSpec) Validate() error {
	if s.ContractID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"GRPCServiceSpec: ContractID must not be empty")
	}
	if s.CellID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"GRPCServiceSpec: CellID must not be empty")
	}
	if s.Listener.IsZero() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"GRPCServiceSpec: Listener must not be the zero ListenerRef; use cell.PrimaryListener or another declared ref")
	}
	if s.Register == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"GRPCServiceSpec: Register must not be nil; supply a func(grpc.ServiceRegistrar) callback")
	}
	return nil
}
