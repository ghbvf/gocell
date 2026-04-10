package verify

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		want    resolvedRef
		wantErr bool
	}{
		{
			name: "journey ref",
			ref:  "journey.J-sso-login.session-revoke",
			want: resolvedRef{Kind: "journey", Pkg: "", RunPattern: "SessionRevoke"},
		},
		{
			name: "smoke ref",
			ref:  "smoke.access-core.startup",
			want: resolvedRef{Kind: "smoke", Pkg: "./cells/access-core/...", RunPattern: "Startup"},
		},
		{
			name: "unit ref",
			ref:  "unit.session-login.service",
			want: resolvedRef{Kind: "unit", RunPattern: "Service"},
		},
		{
			name: "contract ref with dotted ID",
			ref:  "contract.http.auth.login.v1.serve",
			want: resolvedRef{Kind: "contract", RunPattern: "HttpAuthLoginV1Serve"},
		},
		{
			name: "contract ref simple",
			ref:  "contract.event-bus.publish",
			want: resolvedRef{Kind: "contract", RunPattern: "EventBusPublish"},
		},
		{
			name:    "smoke with path traversal cellID",
			ref:     "smoke.../../etc.x",
			wantErr: true,
		},
		{
			name:    "too few segments",
			ref:     "journey.only-two",
			wantErr: true,
		},
		{
			name:    "empty suffix",
			ref:     "journey.J-foo.",
			wantErr: true,
		},
		{
			name:    "unknown prefix",
			ref:     "invalid.foo.bar",
			wantErr: true,
		},
		{
			name:    "empty string",
			ref:     "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveRef(tt.ref)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
