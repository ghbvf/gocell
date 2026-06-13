// INVARIANT: GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01
//
// Package-internal archtest: it lives in tools/codegen/contractgen (not
// tools/archtest) because the truth source — checkGRPCProtoCollisions /
// protoRegistry / ReadProtoServiceInfo — is reachable only from inside this
// package. It is therefore NOT in scope for ARCHTEST-VERIFY-COVERAGE-01, which
// scans tools/archtest only.
//
// Scope after #1688 (contractgen emits ZERO artifacts for kind=grpc — buf's
// generated pb.<Svc>Server is the sole proto-derived server contract, ADR
// 202605260000 §D5): the only surviving concern is that a given proto service is
// registered ONCE across all contracts, with a single import path — i.e. the
// .proto file is the single source of a service's identity. That is enforced by
// the checkGRPCProtoCollisions pre-pass (generator.go), which feeds every
// codegen:true grpc contract's ReadProtoServiceInfo into a protoRegistry and
// fails fast on a duplicate (proto-package, service) or a divergent import path
// for the same service.
//
//	C4 (Medium, collision uniqueness) — TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_Collision*
//	   feed two synthesized service registrations and assert protoRegistry.register
//	   rejects a duplicate (proto-package, service) and a divergent import for the
//	   same service. Non-vacuous despite the single synth fixture (which alone
//	   never collides). Method-level collision tracking was removed in #1655 (one
//	   contract owns the whole service; the .proto is the source of truth for the
//	   method set).
//
// Funnel rating after #1688: Medium (C4 runtime registry guard). The former
// downstream-Hard carriers C2 (golden byte-lock of the rendered grpc stub) and
// C3 (rendered import == proto oracle), plus the Medium C1 (GRPCEndpointSpec
// single sanctioned constructor), are RETIRED — they protected the proto import
// + message types emitted into iface_gen.go, and that rendered stub no longer
// exists (contractgen builds no grpc IR and renders no grpc file). The
// protection target was removed, not weakened (AI-robust: delete when the
// premise is gone). The tracked single-contract Go ceiling (gh #1525) for the
// rendered literal is moot for the same reason.
//
// Blind spots of the chosen tool + reverse self-check:
//   - protoRegistry.register is exercised directly with synthesized
//     ProtoServiceInfo; the distinct-service-OK case proves the collision check
//     does not over-reject (it is the live control against a vacuous green).
package contractgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// --- C4: registry collision uniqueness ----------------------------------------
//
// With service-level granularity (#1655) the ownership unit is a whole proto
// service. The collision classes are:
//   - two different contracts claiming the same (proto-package, service) → "already registered"
//   - two contracts pointing the same service to divergent import paths → "divergent import"

func sampleProtoServiceInfo() ProtoServiceInfo {
	return ProtoServiceInfo{
		ProtoPackage: "device.command.v1",
		ImportPath:   "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1",
		Alias:        "commandv1",
		Methods: []ProtoMethodInfo{
			{Name: "IssueCommand", RequestType: "IssueCommandRequest", ResponseType: "IssueCommandResponse"},
		},
	}
}

func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_CollisionDistinctServicesOK(t *testing.T) {
	t.Parallel()
	r := newProtoRegistry()
	infoA := sampleProtoServiceInfo()
	if err := r.register("grpc.a", "device.command.v1.DeviceCommandService", infoA); err != nil {
		t.Fatalf("first register: %v", err)
	}
	infoB := ProtoServiceInfo{
		ProtoPackage: "device.command.v1",
		ImportPath:   "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1",
		Alias:        "commandv1",
		Methods:      []ProtoMethodInfo{{Name: "RevokeCommand"}},
	}
	if err := r.register("grpc.b", "device.command.v1.StatusService", infoB); err != nil {
		t.Fatalf("distinct service in same package must be allowed: %v", err)
	}
}

func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_CollisionSameService(t *testing.T) {
	t.Parallel()
	r := newProtoRegistry()
	if err := r.register("grpc.a", "device.command.v1.DeviceCommandService", sampleProtoServiceInfo()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := r.register("grpc.b", "device.command.v1.DeviceCommandService", sampleProtoServiceInfo())
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate (package,service) collision, got %v", err)
	}
}

func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_CollisionDivergentImport(t *testing.T) {
	t.Parallel()
	r := newProtoRegistry()
	if err := r.register("grpc.a", "device.command.v1.DeviceCommandService", sampleProtoServiceInfo()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	other := sampleProtoServiceInfo()
	other.ImportPath = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1/evil"
	err := r.register("grpc.b", "device.command.v1.DeviceCommandService", other)
	if err == nil || !strings.Contains(err.Error(), "divergent import") {
		t.Fatalf("expected divergent-import collision, got %v", err)
	}
}

// TestGRPCMethodOverlay_ReferentialIntegrity exercises the contractgen pre-pass
// guard for the per-method public overlay (#1675): each endpoints.grpc.methods[]
// name must be a member of the proto's method set. This is the Hard codegen-funnel
// gate for referential integrity (kernel/governance cannot read the proto, so
// FMT-41 only does the metadata-pure guards — this is the parallel gate).
func TestGRPCMethodOverlay_ReferentialIntegrity(t *testing.T) {
	t.Parallel()
	info := sampleProtoServiceInfo() // proto methods: [IssueCommand]

	// Valid: overlay name ∈ proto method set.
	if err := validateGRPCMethodOverlay("grpc.a",
		[]metadata.GRPCMethodMeta{{Name: "IssueCommand", Public: true}}, info); err != nil {
		t.Fatalf("valid overlay rejected: %v", err)
	}

	// Invalid: overlay name ∉ proto method set — must name the contract + the
	// unknown method.
	err := validateGRPCMethodOverlay("grpc.a",
		[]metadata.GRPCMethodMeta{{Name: "Bogus", Public: true}}, info)
	if err == nil || !strings.Contains(err.Error(), "Bogus") || !strings.Contains(err.Error(), "grpc.a") {
		t.Fatalf("expected unknown-method rejection naming contract+method, got %v", err)
	}

	// Empty overlay is a no-op (no methods to validate).
	if err := validateGRPCMethodOverlay("grpc.a", nil, info); err != nil {
		t.Fatalf("nil overlay rejected: %v", err)
	}
}
