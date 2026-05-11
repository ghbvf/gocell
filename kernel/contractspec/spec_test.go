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
		{"path must start with slash", contractspec.ContractSpec{ID: "a", Kind: cellvocab.ContractHTTP, Transport: "http", Method: "POST", Path: "nope"}, true},
		{"method must be upper case", contractspec.ContractSpec{ID: "a", Kind: cellvocab.ContractHTTP, Transport: "http", Method: "post", Path: "/x"}, true},
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

// TestContractSpec_CommandProjection_Validate verifies that command and
// projection kinds pass Validate when ID/Kind/Transport are populated; the
// kind-specific validation surface is intentionally minimal until future
// PRs add command/projection transports.
func TestContractSpec_CommandProjection_Validate(t *testing.T) {
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); err != nil {
				t.Fatalf("expected no error for valid %s spec, got %v", tc.spec.Kind, err)
			}
		})
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
// invalid cell-ID strings are rejected by validateHTTP → isCellIDLike.
// Note: validateHTTP applies strings.ToLower before calling isCellIDLike, so
// uppercase-only violations (e.g. "Abc") are normalised and pass. Only
// characters that remain illegal after lowercasing (digits-first, hyphens-first,
// underscores, punctuation, empty) trigger the error.
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
		{"valid single letter", []string{"a"}, false},
		{"valid lowercase with digits and hyphens", []string{"ab-1-cd"}, false},
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
