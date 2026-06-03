package contractspec_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
)

func TestContractSpec_HTTPSpec_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		spec    contractspec.ContractSpec
		wantErr bool
	}{
		{"happy — full http spec", contractspec.ContractSpec{
			ID: "http.auth.login.v1", Kind: cellvocab.ContractHTTP, Transport: "http",
			Method: "POST", Path: "/api/v1/auth/login",
		}, false},
		{"empty id rejected", contractspec.ContractSpec{Kind: cellvocab.ContractHTTP, Transport: "http", Method: "POST", Path: "/x"}, true},
		{"empty kind rejected", contractspec.ContractSpec{ID: "a", Transport: "http", Method: "POST", Path: "/x"}, true},
		{"empty transport rejected", contractspec.ContractSpec{ID: "a", Kind: cellvocab.ContractHTTP, Method: "POST", Path: "/x"}, true},
		{"http kind requires method", contractspec.ContractSpec{ID: "a", Kind: cellvocab.ContractHTTP, Transport: "http", Path: "/x"}, true},
		{"http kind requires path", contractspec.ContractSpec{ID: "a", Kind: cellvocab.ContractHTTP, Transport: "http", Method: "POST"}, true},
		{"path must start with slash", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractHTTP, Transport: "http", Method: "POST", Path: "nope",
		}, true},
		{"method must be upper case", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractHTTP, Transport: "http", Method: "post", Path: "/x",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %+v, got nil", tc.spec)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error %v for %+v", err, tc.spec)
			}
		})
	}
}

// TestContractSpec_EventSpec_Validate verifies ContractSpec validation for
// event kind: topic is required, HTTP fields are rejected.
func TestContractSpec_EventSpec_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		spec    contractspec.ContractSpec
		wantErr bool
	}{
		{"happy — event spec", contractspec.ContractSpec{
			ID: "event.session.revoked.v1", Kind: cellvocab.ContractEvent, Transport: "amqp",
			Topic: "session.revoked.v1",
		}, false},
		{"happy — event mqtt primary transport", contractspec.ContractSpec{
			ID: "event.device-registered.v1", Kind: cellvocab.ContractEvent, Transport: "mqtt",
			Topic: "event.device-registered.v1",
		}, false},
		{"event kind requires topic", contractspec.ContractSpec{ID: "a", Kind: cellvocab.ContractEvent, Transport: "amqp"}, true},
		{"event spec with http fields rejected", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractEvent, Transport: "amqp", Topic: "t", Method: "POST",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %+v, got nil", tc.spec)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error %v for %+v", err, tc.spec)
			}
		})
	}
}

// TestContractSpec_CommandProjectionSaga_Validate verifies that command,
// projection, and saga kinds pass Validate when ID/Kind/Transport are
// populated; the kind-specific validation surface is intentionally minimal
// until future PRs add command/projection transports. Saga is orchestrated
// via the runtime Coordinator (kernel/saga), not a wire transport, but the
// kind-agnostic Transport check on line 86 still applies.
func TestContractSpec_CommandProjectionSaga_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec contractspec.ContractSpec
	}{
		{"command kind no extra fields", contractspec.ContractSpec{
			ID: "command.device.enqueue.v1", Kind: cellvocab.ContractCommand, Transport: "internal",
		}},
		{"projection kind no extra fields", contractspec.ContractSpec{
			ID: "projection.access.users.v1", Kind: cellvocab.ContractProjection, Transport: "internal",
		}},
		{"saga kind with transport passes", contractspec.ContractSpec{
			ID: "saga.order.checkout.v1", Kind: cellvocab.ContractSaga, Transport: "internal",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); err != nil {
				t.Fatalf("expected no error for valid %s spec, got %v", tc.spec.Kind, err)
			}
		})
	}
}

// TestContractSpec_GRPCSpec_Validate verifies ContractSpec validation for
// grpc kind: Service + Method + Proto are required (nested GRPCEndpointSpec),
// Proto must be rooted under contracts/grpc/, StreamingType (when present) must
// be one of the metadata enum, and HTTP/event fields are rejected on a grpc
// spec. These mirror the contract.schema.json grpc if/then block and governance
// FMT-37 so the three validation surfaces agree.
func TestContractSpec_GRPCSpec_Validate(t *testing.T) {
	t.Parallel()
	const proto = "contracts/grpc/device/command/v1/device_command.proto"
	full := func() *contractspec.GRPCEndpointSpec {
		return &contractspec.GRPCEndpointSpec{
			Service: "device.command.v1.DeviceCommandService",
			Method:  "IssueCommand", Proto: proto,
		}
	}
	cases := []struct {
		name    string
		spec    contractspec.ContractSpec
		wantErr bool
	}{
		{"happy — full grpc spec", contractspec.ContractSpec{
			ID: "grpc.device.command.v1", Kind: cellvocab.ContractGRPC, Transport: "grpc",
			GRPC: full(),
		}, false},
		{"happy — explicit streamingType", contractspec.ContractSpec{
			ID: "grpc.device.command.v1", Kind: cellvocab.ContractGRPC, Transport: "grpc",
			GRPC: &contractspec.GRPCEndpointSpec{
				Service: "s.v1.S", Method: "M", Proto: proto, StreamingType: "server-stream",
			},
		}, false},
		{"happy — explicit unary streamingType", contractspec.ContractSpec{
			// "unary" is a real enum member, distinct from the omitted default;
			// both must be accepted (see metadata.GRPCStreamingTypeEnum).
			ID: "grpc.device.command.v1", Kind: cellvocab.ContractGRPC, Transport: "grpc",
			GRPC: &contractspec.GRPCEndpointSpec{
				Service: "s.v1.S", Method: "M", Proto: proto, StreamingType: "unary",
			},
		}, false},
		{"grpc kind requires GRPC block", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractGRPC, Transport: "grpc",
		}, true},
		{"grpc kind requires service", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractGRPC, Transport: "grpc",
			GRPC: &contractspec.GRPCEndpointSpec{Method: "IssueCommand", Proto: proto},
		}, true},
		{"grpc kind requires method", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractGRPC, Transport: "grpc",
			GRPC: &contractspec.GRPCEndpointSpec{Service: "s.v1.S", Proto: proto},
		}, true},
		{"grpc kind requires proto", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractGRPC, Transport: "grpc",
			GRPC: &contractspec.GRPCEndpointSpec{Service: "s.v1.S", Method: "M"},
		}, true},
		{"grpc proto must be rooted under contracts/grpc/", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractGRPC, Transport: "grpc",
			GRPC: &contractspec.GRPCEndpointSpec{Service: "s.v1.S", Method: "M", Proto: "proto/x.proto"},
		}, true},
		{"grpc invalid streamingType rejected", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractGRPC, Transport: "grpc",
			GRPC: &contractspec.GRPCEndpointSpec{Service: "s.v1.S", Method: "M", Proto: proto, StreamingType: "duplex"},
		}, true},
		{"grpc spec with http Method rejected", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractGRPC, Transport: "grpc", Method: "POST",
			GRPC: full(),
		}, true},
		{"grpc spec with event Topic rejected", contractspec.ContractSpec{
			ID: "a", Kind: cellvocab.ContractGRPC, Transport: "grpc", Topic: "t",
			GRPC: full(),
		}, true},
		{"http spec carrying GRPC block rejected", contractspec.ContractSpec{
			ID: "http.a.b.v1", Kind: cellvocab.ContractHTTP, Transport: "http", Method: "GET", Path: "/x",
			GRPC: full(),
		}, true},
		{"event spec carrying GRPC block rejected", contractspec.ContractSpec{
			ID: "event.a.b.v1", Kind: cellvocab.ContractEvent, Transport: "amqp", Topic: "t",
			GRPC: full(),
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %+v, got nil", tc.spec)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error %v for %+v", err, tc.spec)
			}
		})
	}
}

// TestContractSpec_GRPCInfo verifies the GRPCInfo accessor returns the nested
// grpc endpoint spec for a grpc contract and nil otherwise.
func TestContractSpec_GRPCInfo(t *testing.T) {
	t.Parallel()
	grpcSpec := &contractspec.GRPCEndpointSpec{Service: "s", Method: "m"}
	withGRPC := contractspec.ContractSpec{
		ID: "grpc.x.y.v1", Kind: cellvocab.ContractGRPC, Transport: "grpc", GRPC: grpcSpec,
	}
	if got := withGRPC.GRPCInfo(); got != grpcSpec {
		t.Fatalf("GRPCInfo() = %v, want %v", got, grpcSpec)
	}
	httpSpec := contractspec.ContractSpec{ID: "http.a.b.v1", Kind: cellvocab.ContractHTTP, Transport: "http", Method: "GET", Path: "/x"}
	if got := httpSpec.GRPCInfo(); got != nil {
		t.Fatalf("GRPCInfo() on http spec = %v, want nil", got)
	}
}

// TestContractSpec_UnknownKind_Validate verifies that an unrecognized kind
// is rejected with a kind-specific error message.
func TestContractSpec_UnknownKind_Validate(t *testing.T) {
	t.Parallel()
	spec := contractspec.ContractSpec{
		ID: "x", Kind: cellvocab.ContractKind("websocket"), Transport: "ws",
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

// TestContractSpec_Validate_InternalRequiresClients verifies that an http ContractSpec
// with a /internal/v1/* path and nil Clients fails validation.
//
// Spec: all internal endpoints must declare Clients (the allowed callers);
// a nil/empty Clients on an internal path is a misconfiguration.
func TestContractSpec_Validate_InternalRequiresClients(t *testing.T) {
	t.Parallel()
	// Spec: Path=/internal/v1/foo + Clients=nil → error
	spec := contractspec.ContractSpec{
		ID:        "http.test.internal.v1",
		Kind:      cellvocab.ContractHTTP,
		Transport: "http",
		Method:    "POST",
		Path:      "/internal/v1/foo",
		Clients:   nil, // missing required caller allowlist for internal endpoints
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected error: /internal/v1/* path without Clients must be rejected")
	}
}

// TestContractSpec_Validate_NonInternalRejectsClients verifies that a non-internal
// path with Clients set fails validation.
//
// Spec: only /internal/v1/* endpoints should declare Clients; public API endpoints
// must not carry a Clients allowlist (the allowlist has no meaning for public routes).
func TestContractSpec_Validate_NonInternalRejectsClients(t *testing.T) {
	t.Parallel()
	// Spec: Path=/api/v1/foo + Clients=["x"] → error
	spec := contractspec.ContractSpec{
		ID:        "http.test.api.v1",
		Kind:      cellvocab.ContractHTTP,
		Transport: "http",
		Method:    "GET",
		Path:      "/api/v1/foo",
		Clients:   []string{"x"}, // Clients on non-internal path → rejected
	}
	err := spec.Validate()
	if err == nil {
		t.Fatal("expected error: Clients must not be set on non-internal paths")
	}
}

// TestContractSpec_Validate_InternalWithClientsOK verifies that an internal
// ContractSpec with Clients declared passes validation.
//
// Spec: Path=/internal/v1/foo + Clients=["accesscore"] → nil.
func TestContractSpec_Validate_InternalWithClientsOK(t *testing.T) {
	t.Parallel()
	spec := contractspec.ContractSpec{
		ID:        "http.test.internal.v1",
		Kind:      cellvocab.ContractHTTP,
		Transport: "http",
		Method:    "POST",
		Path:      "/internal/v1/foo",
		Clients:   []string{"accesscore"}, // valid: internal path with declared caller
	}
	err := spec.Validate()
	if err != nil {
		t.Fatalf("expected no error for valid internal spec with Clients, got: %v", err)
	}
}

// TestContractSpec_Validate_InvalidClientID tests that Clients containing
// invalid cell-ID strings are rejected by validateHTTP → metadata.MatchCellID.
// metadata.MatchCellID enforces metadata.CellIDPattern (^[a-z][a-z0-9]+$):
// ≥2 chars, lowercase ASCII letter first, then lowercase letters or digits,
// no dashes or underscores. Uppercase first characters (e.g. "Accesscore") are
// rejected. Single-character IDs are rejected (pattern requires ≥2 chars).
// IDs containing dashes are rejected (no-dash concatenation convention FMT-16).
func TestContractSpec_Validate_InvalidClientID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		clients []string
		wantErr bool
	}{
		{"empty string client", []string{""}, true},
		{"starts with digit", []string{"1abc"}, true},
		{"starts with hyphen", []string{"-abc"}, true},
		{"contains underscore", []string{"ab_c"}, true},
		{"contains exclamation", []string{"ab!c"}, true},
		{"single letter rejected — pattern requires ≥2 chars", []string{"a"}, true},
		{"dash rejected — no-dash convention FMT-16", []string{"ab-1-cd"}, true},
		{"dash rejected — simple", []string{"foo-bar"}, true},
		{"valid lowercase two chars", []string{"ab"}, false},
		{"valid lowercase with digits no dash", []string{"ab1cd"}, false},
		{"valid full id", []string{"accesscore"}, false},
		{"uppercase rejected", []string{"Accesscore"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := contractspec.ContractSpec{
				ID:        "http.test.internal.v1",
				Kind:      cellvocab.ContractHTTP,
				Transport: "http",
				Method:    "GET",
				Path:      "/internal/v1/foo",
				Clients:   tc.clients,
			}
			err := spec.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for Clients=%v, got nil", tc.clients)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for Clients=%v: %v", tc.clients, err)
			}
		})
	}
}
