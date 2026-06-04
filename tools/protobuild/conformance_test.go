package protobuild_test

import (
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	conformancev1 "github.com/ghbvf/gocell/generated/contracts/grpc/conformance/v1"
)

// TestConformanceFixtureCompilesAndRoundTrips proves the buf proto toolchain
// output is usable, not just present: the generated message marshals/unmarshals
// through the main module's protobuf runtime, and the generated grpc service
// descriptor + Register func link against the main module's grpc runtime. A
// toolchain regression (incompatible protoc-gen-go / protoc-gen-go-grpc /
// runtime version triple) or a stale checked-in .pb.go surfaces here as a
// failing test, complementing the CI regenerate-and-diff gate
// (hack/verify-codegen-proto.sh).
func TestConformanceFixtureCompilesAndRoundTrips(t *testing.T) {
	t.Parallel()

	// protoc-gen-go message round-trip through the protobuf runtime.
	const token = "conformance-token"
	raw, err := proto.Marshal(&conformancev1.CheckRequest{Token: token})
	if err != nil {
		t.Fatalf("marshal CheckRequest: %v", err)
	}
	got := &conformancev1.CheckRequest{}
	if err := proto.Unmarshal(raw, got); err != nil {
		t.Fatalf("unmarshal CheckRequest: %v", err)
	}
	if got.GetToken() != token {
		t.Fatalf("round-trip token = %q, want %q", got.GetToken(), token)
	}

	// protoc-gen-go-grpc service descriptor carries the proto-derived FQN and
	// links against the grpc runtime.
	if name, want := conformancev1.ConformanceService_ServiceDesc.ServiceName, "conformance.v1.ConformanceService"; name != want {
		t.Fatalf("ServiceName = %q, want %q", name, want)
	}

	// RegisterConformanceServiceServer registers onto a real *grpc.Server — the
	// exact call shape a cell's GRPCServiceSpec.Register callback uses (PR-7).
	// This proves the generated Register func + service descriptor work against
	// the grpc runtime, not just that they type-check.
	srv := grpc.NewServer()
	conformancev1.RegisterConformanceServiceServer(srv, conformancev1.UnimplementedConformanceServiceServer{})
	info, ok := srv.GetServiceInfo()["conformance.v1.ConformanceService"]
	if !ok {
		t.Fatalf("ConformanceService not registered on grpc.Server; got services %v", srv.GetServiceInfo())
	}
	// The registered descriptor must expose exactly the proto's RPC set; a codegen
	// regression that drops or renames a method surfaces here, not just at compile time.
	if len(info.Methods) != 1 || info.Methods[0].Name != "Check" {
		t.Fatalf("ConformanceService methods = %+v, want exactly [Check]", info.Methods)
	}
}
