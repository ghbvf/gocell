package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestMount_AutoEnforceClients_InAllowlist_200 verifies that when a ContractSpec
// has Clients declared (e.g. ["accesscore"]) and the request carries a
// PrincipalService with CallerCellID in the allowlist, the handler returns 200.
//
// Spec: auth.Mount auto-enforces a RequireCallerCell policy when spec.Clients != nil.
func TestMount_AutoEnforceClients_InAllowlist_200(t *testing.T) {
	// Spec: wrapper.ContractSpec.Clients declares the allowed caller cells.
	spec := wrapper.ContractSpec{
		ID:        "http.auth.role.assign.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "POST",
		Path:      "/internal/v1/access/roles/assign",
		Clients:   []string{"accesscore"},
	}

	mux := http.NewServeMux()
	route := Route{
		Contract: spec,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	err := Mount(mux, route)
	require.NoError(t, err)

	// Request from an allowed caller (CallerCellID == "accesscore").
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign", nil)
	req = req.WithContext(TestServiceContext("accesscore"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"request from allowlisted caller should be permitted")
}

// TestMount_AutoEnforceClients_OutAllowlist_403 verifies that when a ContractSpec
// has Clients=["accesscore"] but the request carries CallerCellID="configcore",
// the handler returns 403.
//
// Spec: auto-enforced RequireCallerCell rejects callers not in spec.Clients.
func TestMount_AutoEnforceClients_OutAllowlist_403(t *testing.T) {
	spec := wrapper.ContractSpec{
		ID:        "http.auth.role.assign.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "POST",
		Path:      "/internal/v1/access/roles/assign",
		Clients:   []string{"accesscore"},
	}

	mux := http.NewServeMux()
	route := Route{
		Contract: spec,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler must not be called for rejected caller")
		}),
	}
	err := Mount(mux, route)
	require.NoError(t, err)

	// Request from a non-allowlisted caller.
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign", nil)
	req = req.WithContext(TestServiceContext("configcore"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"request from non-allowlisted caller should be forbidden")

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, string(errcode.ErrAuthForbidden), body.Error.Code)
}

// TestMount_AutoEnforceClients_ComposeWithPolicy verifies that when Clients AND
// Policy are both declared on a Route, both layers are enforced (AND semantics).
//
// Spec: Clients guard (RequireCallerCell) runs first; if the caller is in the
// allowlist, the route-level Policy runs next. Both must pass for 200.
func TestMount_AutoEnforceClients_ComposeWithPolicy(t *testing.T) {
	spec := wrapper.ContractSpec{
		ID:        "http.auth.role.assign.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "POST",
		Path:      "/internal/v1/access/roles/assign",
		Clients:   []string{"accesscore"},
	}

	// A Policy that always returns forbidden (simulating an extra layer guard).
	alwaysForbiddenPolicy := Policy(func(r *http.Request) error {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "extra policy denied")
	})

	mux := http.NewServeMux()
	route := Route{
		Contract: spec,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler must not be called when policy denies")
		}),
		Policy: alwaysForbiddenPolicy,
	}
	err := Mount(mux, route)
	require.NoError(t, err)

	// Even an allowlisted caller should be blocked if the extra Policy denies.
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign", nil)
	req = req.WithContext(TestServiceContext("accesscore"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Spec: Clients guard passes (caller in allowlist), Policy denies → 403.
	assert.Equal(t, http.StatusForbidden, rec.Code,
		"extra Policy layer must be enforced after Clients guard passes")
}
