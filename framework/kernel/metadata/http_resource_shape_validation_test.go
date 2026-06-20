package metadata

import "testing"

// TestValidateHTTPResourceShape covers the #2355 owner/self-scoped shape oracle shared by
// the contractgen Hard gate and FMT-42: each rule + the well-formed cases, including the
// resource ∈ pathParams referential-integrity check JSON Schema cannot express.
func TestValidateHTTPResourceShape(t *testing.T) {
	pp := map[string]ParamSchema{"id": {}}
	cases := []struct {
		name      string
		h         *HTTPTransportMeta
		wantKinds []HTTPResourceShapeViolationKind
	}{
		{"nil ok", nil, nil},
		{"coarse ok", &HTTPTransportMeta{Permission: "config:read"}, nil},
		{"owner ok", &HTTPTransportMeta{Permission: "user:read", Resource: "id", PathParams: pp}, nil},
		{"self ok", &HTTPTransportMeta{Permission: "access:decide", SelfScoped: true}, nil},
		// DELIBERATE non-port of FMT-41: owner-scoped action without resource (admin route) is OK.
		{"owner-scoped permission without resource ok", &HTTPTransportMeta{Permission: "user:write"}, nil},
		{
			"resource without permission", &HTTPTransportMeta{Resource: "id", PathParams: pp},
			[]HTTPResourceShapeViolationKind{HTTPResourceWithoutPermission},
		},
		{
			"selfScoped without permission", &HTTPTransportMeta{SelfScoped: true},
			[]HTTPResourceShapeViolationKind{HTTPSelfScopedWithoutPermission},
		},
		{
			"resource and selfScoped mutex", &HTTPTransportMeta{Permission: "user:read", Resource: "id", SelfScoped: true, PathParams: pp},
			[]HTTPResourceShapeViolationKind{HTTPResourceSelfScopedMutex},
		},
		{
			"resource not in pathParams", &HTTPTransportMeta{Permission: "user:read", Resource: "userId", PathParams: pp},
			[]HTTPResourceShapeViolationKind{HTTPResourceNotInPathParams},
		},
		{
			"resource with no pathParams declared", &HTTPTransportMeta{Permission: "user:read", Resource: "id"},
			[]HTTPResourceShapeViolationKind{HTTPResourceNotInPathParams},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viols := ValidateHTTPResourceShape(tc.h)
			if len(viols) != len(tc.wantKinds) {
				t.Fatalf("got %d violations %+v, want %d %v", len(viols), viols, len(tc.wantKinds), tc.wantKinds)
			}
			for i, want := range tc.wantKinds {
				if viols[i].Kind != want {
					t.Errorf("violation[%d] kind = %d, want %d", i, viols[i].Kind, want)
				}
				if viols[i].Message == "" {
					t.Errorf("violation[%d] has empty Message", i)
				}
			}
		})
	}
}
