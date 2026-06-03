package contractgen

// command_builder_test.go — builder tests for kind=command full generator (Batch B, #1044).
// Covers: spec.Command population, fail-closed schemaRef guards, DTO emission.
// The old graceful-skip stub tests (TestBuildContractSpec_CommandKind_Skips /
// _GracefulSkip) were deleted with the full generator: a codegen command with
// missing/absent schemaRefs is now rejected (fail-closed), not silently stubbed —
// see TestBuildCommandSpec_NoSchemaRefs / _MissingRequestSchemaRef / _MissingResponseSchemaRef.

import (
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestBuildCommandSpec_FullSpec verifies that kind=command with both
// schemaRefs.request and schemaRefs.response produces a fully-populated
// spec.Command and the expected Request+Response DTOs.
func TestBuildCommandSpec_FullSpec(t *testing.T) {
	t.Parallel()
	testDir := filepath.Join("testdata", "synth", "synth_command")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse synth_command fixture: %v", err)
	}

	const contractID = "command.synth.do.v1"
	spec, err := buildContractSpec(absTestDir, p, contractID)
	if err != nil {
		t.Fatalf("buildContractSpec(%q): %v", contractID, err)
	}
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}

	// Kind and exclusion checks.
	if spec.Kind != "command" {
		t.Errorf("spec.Kind = %q, want %q", spec.Kind, "command")
	}
	if spec.Endpoint != nil {
		t.Errorf("spec.Endpoint should be nil for kind=command, got non-nil")
	}
	if spec.Event != nil {
		t.Errorf("spec.Event should be nil for kind=command, got non-nil")
	}
	if spec.Saga != nil {
		t.Errorf("spec.Saga should be nil for kind=command, got non-nil")
	}

	// Command spec must be populated.
	cmd := spec.Command
	if cmd == nil {
		t.Fatal("spec.Command must be non-nil for kind=command with schemaRefs")
	}
	if cmd.DispatchID != contractID {
		t.Errorf("DispatchID = %q, want %q", cmd.DispatchID, contractID)
	}
	// domainLastSegment("command.synth.do.v1") = "do" → goPascalCase = "Do" → "HandleDo"
	if cmd.HandlerMethod != "HandleDo" {
		t.Errorf("HandlerMethod = %q, want %q", cmd.HandlerMethod, "HandleDo")
	}
	if cmd.RequestGoType != "Request" {
		t.Errorf("RequestGoType = %q, want %q", cmd.RequestGoType, "Request")
	}
	if cmd.ResponseGoType != "Response" {
		t.Errorf("ResponseGoType = %q, want %q", cmd.ResponseGoType, "Response")
	}

	// DTOs must include both Request and Response.
	if !hasDTONamed(spec.DTOs, "Request") {
		names := make([]string, 0, len(spec.DTOs))
		for _, d := range spec.DTOs {
			names = append(names, d.Name)
		}
		t.Errorf("spec.DTOs missing 'Request'; got: %v", names)
	}
	if !hasDTONamed(spec.DTOs, "Response") {
		names := make([]string, 0, len(spec.DTOs))
		for _, d := range spec.DTOs {
			names = append(names, d.Name)
		}
		t.Errorf("spec.DTOs missing 'Response'; got: %v", names)
	}
}

// TestBuildCommandSpec_MissingRequestSchemaRef verifies that a kind=command
// contract with only schemaRefs.response (no request) is rejected at codegen
// time. This is the codegen Hard half of COMMAND-CONTRACT-SCHEMA-REF-01.
func TestBuildCommandSpec_MissingRequestSchemaRef(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"command.device.provision.v1": {
				ID:         "command.device.provision.v1",
				Kind:       "command",
				Codegen:    true,
				Transports: []string{"internal"},
				File:       "contracts/command/device/provision/v1/contract.yaml",
				SchemaRefs: metadata.SchemaRefsMeta{
					Response: "response.schema.json",
					// Request deliberately absent.
				},
			},
		},
	}
	_, err := buildContractSpec("", p, "command.device.provision.v1")
	if err == nil {
		t.Fatal("expected error for missing schemaRefs.request, got nil")
	}
}

// TestBuildCommandSpec_MissingResponseSchemaRef verifies that a kind=command
// contract with only schemaRefs.request (no response) is rejected at codegen time.
func TestBuildCommandSpec_MissingResponseSchemaRef(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"command.device.provision.v1": {
				ID:         "command.device.provision.v1",
				Kind:       "command",
				Codegen:    true,
				Transports: []string{"internal"},
				File:       "contracts/command/device/provision/v1/contract.yaml",
				SchemaRefs: metadata.SchemaRefsMeta{
					Request: "request.schema.json",
					// Response deliberately absent.
				},
			},
		},
	}
	_, err := buildContractSpec("", p, "command.device.provision.v1")
	if err == nil {
		t.Fatal("expected error for missing schemaRefs.response, got nil")
	}
}

// TestBuildCommandSpec_NoSchemaRefs verifies that a codegen kind=command contract
// with NO schemaRefs at all is REJECTED (fail-closed) — not silently accepted as a
// "stub". A codegen command must declare both request and response schemas; missing
// both is a misconfiguration, caught here (codegen Hard half of
// COMMAND-CONTRACT-SCHEMA-REF-01) as well as by the governance rule at validate-time.
// There is no static-no-op/graceful-skip path.
func TestBuildCommandSpec_NoSchemaRefs(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"command.device.provision.v1": {
				ID:         "command.device.provision.v1",
				Kind:       "command",
				Codegen:    true,
				Transports: []string{"internal"},
				File:       "contracts/command/device/provision/v1/contract.yaml",
				// No SchemaRefs — must be rejected, not stubbed.
			},
		},
	}
	_, err := buildContractSpec("", p, "command.device.provision.v1")
	if err == nil {
		t.Fatal("expected error for a codegen command with no schemaRefs, got nil")
	}
}

// TestBuildCommandSpec_HandlerMethodDerivation tests domainLastSegment +
// goPascalCase derivation for the HandlerMethod with different contract IDs.
func TestBuildCommandSpec_HandlerMethodDerivation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		contractID string
		want       string
	}{
		// domainLastSegment takes the second-to-last dot-segment.
		{"command.synth.do.v1", "HandleDo"},
		{"command.device.enqueue.v1", "HandleEnqueue"},
		{"command.device-command.enqueue.v1", "HandleEnqueue"},
		{"command.provisioning.issue-certificate.v1", "HandleIssueCertificate"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.contractID, func(t *testing.T) {
			t.Parallel()
			last := domainLastSegment(tc.contractID)
			got := "Handle" + goPascalCase(last)
			if got != tc.want {
				t.Errorf("domainLastSegment(%q) = %q → HandlerMethod = %q, want %q",
					tc.contractID, last, got, tc.want)
			}
		})
	}
}
