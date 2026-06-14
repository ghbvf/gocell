package metadata

import "testing"

func TestValidateHTTPHeaders(t *testing.T) {
	truthy := true
	minLen := 1

	tests := []struct {
		name      string
		headers   map[string]ParamSchema
		wantKinds []HeaderViolationKind // nil = expect no violations
	}{
		{
			name:      "nil map ok",
			headers:   nil,
			wantKinds: nil,
		},
		{
			name:      "valid string header (required + uuid format accepted)",
			headers:   map[string]ParamSchema{"X-Tenant-ID": {Type: "string", Format: "uuid", Required: &truthy}},
			wantKinds: nil,
		},
		{
			name:      "missing type rejected",
			headers:   map[string]ParamSchema{"X-Tenant-ID": {}},
			wantKinds: []HeaderViolationKind{HeaderViolationType},
		},
		{
			name:      "integer type rejected (populate-only emits string)",
			headers:   map[string]ParamSchema{"X-Tenant-ID": {Type: "integer"}},
			wantKinds: []HeaderViolationKind{HeaderViolationType},
		},
		{
			name:      "invalid name rejected",
			headers:   map[string]ParamSchema{"X Tenant": {Type: "string"}},
			wantKinds: []HeaderViolationKind{HeaderViolationName},
		},
		{
			name:      "length constraint rejected",
			headers:   map[string]ParamSchema{"X-Tenant-ID": {Type: "string", MinLength: &minLen}},
			wantKinds: []HeaderViolationKind{HeaderViolationConstraint},
		},
		{
			name: "case-insensitive duplicate rejected",
			headers: map[string]ParamSchema{
				"X-Tenant-ID": {Type: "string"},
				"x-tenant-id": {Type: "string"},
			},
			wantKinds: []HeaderViolationKind{HeaderViolationDuplicate},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateHTTPHeaders(tc.headers)
			if len(got) != len(tc.wantKinds) {
				t.Fatalf("ValidateHTTPHeaders(%v) = %d violations %+v, want %d", tc.headers, len(got), got, len(tc.wantKinds))
			}
			for i, k := range tc.wantKinds {
				if got[i].Kind != k {
					t.Errorf("violation[%d].Kind = %d, want %d (msg=%q)", i, got[i].Kind, k, got[i].Message)
				}
			}
		})
	}
}

// TestValidateHTTPHeaders_DuplicateReportsLaterName confirms the surviving
// (lexicographically-first) declaration is not flagged; only the duplicate is.
func TestValidateHTTPHeaders_DuplicateReportsLaterName(t *testing.T) {
	got := ValidateHTTPHeaders(map[string]ParamSchema{
		"X-Tenant-ID": {Type: "string"},
		"x-tenant-id": {Type: "string"},
	})
	if len(got) != 1 || got[0].Kind != HeaderViolationDuplicate {
		t.Fatalf("want 1 duplicate violation, got %+v", got)
	}
	if got[0].Header != "x-tenant-id" {
		t.Errorf("duplicate violation Header = %q, want %q (the later-sorted name)", got[0].Header, "x-tenant-id")
	}
}
