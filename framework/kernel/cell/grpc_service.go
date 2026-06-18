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
// Sealing the field (permanent-ceiling won't-do) is tracked as gh #1582 —
// same family as #851/#893/#1282.
//
// ref: zeromicro/go-zero zrpc/internal/rpcserver.go — RegisterFn func(*grpc.Server) (Form B precedent)
// ref: go-kratos/kratos transport/grpc/server.go — pb.RegisterXxxServer before Start

import (
	"github.com/ghbvf/gocell/framework/pkg/errcode"
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
	// The canonical implementation (runtime/grpc.ServiceRegistrar) always returns
	// nil; contract violations (bad callback type, duplicate ServiceName) panic
	// via panicregister.Approved.
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

	// PublicMethods lists the FULL method names (/{proto.Service}/{Method}) this
	// service exposes that are JWT-exempt (#1675). Derived by cellgen from the
	// contract's endpoints.grpc.methods[] overlay (the public:true entries),
	// composed identically to the registrar's own attribution key so the runtime
	// registrar can aggregate them into the auth interceptor's bypass set. Empty
	// → every RPC of this service is authed (the fail-closed default). It is a
	// data field (not a callback), so kernel can hold it without a grpc import.
	//
	// This field is cellgen-OWNED: declare public RPCs in the contract's
	// endpoints.grpc.methods[] (public:true), not here. Hand-writing PublicMethods
	// in a cell's Init bypasses the contractgen pre-pass that validates each name
	// against the .proto method set — a typo would silently mark a non-existent
	// method public (inert) or, worse, drift from the contract. Generated
	// cell_gen.go is the only sanctioned writer.
	PublicMethods []string

	// MethodPermissions maps each non-public FULL method name
	// (/{proto.Service}/{Method}) to the ABAC action string it requires (#2008),
	// e.g. "/device.command.v1.DeviceCommandService/IssueCommand" → "device:command".
	// Derived by cellgen from the contract's endpoints.grpc.methods[] overlay (the
	// permission entries), keyed identically to PublicMethods and the registrar's
	// attribution map. The runtime registrar resolves each string to a sealed
	// authz.Permission (fail-fast on an unknown action) and the auth interceptor
	// gates the RPC against it via the PDP. It is a data field (raw strings, not
	// authz.Permission) so kernel can hold it without importing framework/pkg/authz
	// — string→Permission resolution happens in the runtime layer.
	//
	// Strict fail-closed (#2008): a non-public method MUST appear here or it is
	// DENIED at the gate. This field is cellgen-OWNED for the same reason as
	// PublicMethods — the cellgen completeness pre-pass guarantees every authed
	// proto method has a permission entry, which hand-writing would bypass.
	MethodPermissions map[string]string

	// MethodResources maps each owner-scoped FULL method name
	// (/{proto.Service}/{Method}) to the REQUEST MESSAGE field name (proto field,
	// snake_case, e.g. "device_id") whose string value is extracted and forwarded
	// as the PDP resource for per-message ownership authz (#2207). The interceptor
	// extracts the field via protoreflect on the first received message and
	// canonicalizes it (ParseCanonicalUUID) before forwarding as the PDP resource,
	// enabling the ownership rule (subject.sub == resource.id) to fire.
	//
	// Methods NOT in this map use fullMethod as the resource (coarse, current
	// behavior). Methods IN this map MUST also appear in MethodPermissions (the
	// permission gate always runs; resource extraction only changes which resource
	// the PDP sees). Derived by cellgen from endpoints.grpc.methods[].resource
	// alongside MethodPermissions — keyed identically to the registrar's maps.
	//
	// This field is cellgen-OWNED: declare owner-scoped resource extraction via
	// endpoints.grpc.methods[].resource, not here. Hand-writing bypasses the
	// cellgen cross-check (owner-scoped permission MUST declare resource).
	// See GRPCServiceSpec.MethodPermissions godoc for the parallel constraint.
	MethodResources map[string]string
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
