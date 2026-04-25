package router

import (
	"testing"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyPolicyCoverage(t *testing.T) {
	tests := []struct {
		name        string
		routes      []routeKey            // registered routes from chi.Walk
		metas       []kcell.AuthRouteMeta // declared auth metas
		whitelist   []string              // whitelist patterns
		wantErr     bool
		errContains string // substring in error message
	}{
		{
			name: "all_declared_no_error",
			routes: []routeKey{
				{Method: "GET", Path: "/api/v1/users"},
				{Method: "POST", Path: "/api/v1/users"},
			},
			metas: []kcell.AuthRouteMeta{
				{Method: "GET", Path: "/api/v1/users"},
				{Method: "POST", Path: "/api/v1/users"},
			},
			wantErr: false,
		},
		{
			name: "missing_policy_returns_error",
			routes: []routeKey{
				{Method: "GET", Path: "/api/v1/users"},
				{Method: "POST", Path: "/api/v1/orders"},
			},
			metas: []kcell.AuthRouteMeta{
				{Method: "GET", Path: "/api/v1/users"},
				// POST /api/v1/orders intentionally missing
			},
			wantErr:     true,
			errContains: "POST /api/v1/orders",
		},
		{
			name: "public_route_auto_exempt",
			routes: []routeKey{
				{Method: "POST", Path: "/api/v1/auth/login"},
			},
			metas: []kcell.AuthRouteMeta{
				{Method: "POST", Path: "/api/v1/auth/login", Public: true},
			},
			wantErr: false,
		},
		{
			name: "delegated_route_auto_exempt",
			routes: []routeKey{
				{Method: "GET", Path: "/internal/v1/service"},
			},
			metas: []kcell.AuthRouteMeta{
				{Method: "GET", Path: "/internal/v1/service"},
			},
			wantErr: false,
		},
		{
			name: "whitelisted_exact_match",
			routes: []routeKey{
				{Method: "GET", Path: "/debug/pprof"},
			},
			metas:     []kcell.AuthRouteMeta{},
			whitelist: []string{"GET /debug/pprof"},
			wantErr:   false,
		},
		{
			name: "whitelisted_prefix_match",
			routes: []routeKey{
				{Method: "GET", Path: "/debug/pprof"},
				{Method: "GET", Path: "/debug/vars"},
			},
			metas:     []kcell.AuthRouteMeta{},
			whitelist: []string{"/debug/*"},
			wantErr:   false,
		},
		{
			name: "multiple_missing_actionable_error",
			routes: []routeKey{
				{Method: "GET", Path: "/api/v1/items"},
				{Method: "DELETE", Path: "/api/v1/items/{id}"},
				{Method: "POST", Path: "/api/v1/items"},
			},
			metas: []kcell.AuthRouteMeta{
				{Method: "POST", Path: "/api/v1/items"},
				// GET /api/v1/items and DELETE /api/v1/items/{id} missing
			},
			wantErr:     true,
			errContains: "2 route(s)",
		},
		{
			name:    "empty_routes_no_error",
			routes:  []routeKey{},
			metas:   []kcell.AuthRouteMeta{},
			wantErr: false,
		},
		{
			name: "malformed_whitelist_entry_silently_ignored",
			routes: []routeKey{
				{Method: "GET", Path: "/api/v1/secret"},
			},
			metas:       []kcell.AuthRouteMeta{},
			whitelist:   []string{"INVALID_ENTRY", "just-a-word"},
			wantErr:     true,
			errContains: "GET /api/v1/secret",
		},
		{
			name: "head_auto_covered_by_get",
			routes: []routeKey{
				{Method: "GET", Path: "/api/v1/resources"},
				{Method: "HEAD", Path: "/api/v1/resources"},
			},
			metas: []kcell.AuthRouteMeta{
				{Method: "GET", Path: "/api/v1/resources"},
				// HEAD is not declared but auto-covered by GET per RFC 7231 §4.3.2
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyPolicyCoverage(tc.routes, tc.metas, tc.whitelist)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
