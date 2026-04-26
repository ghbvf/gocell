package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInternalAddrToBaseURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "empty address defaults to loopback 9090",
			addr: "",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "port-only resolves to loopback",
			addr: ":9090",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "port-only non-standard port",
			addr: ":9191",
			want: "http://127.0.0.1:9191",
		},
		{
			name: "explicit loopback address unchanged",
			addr: "127.0.0.1:9090",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "0.0.0.0:port normalised to 127.0.0.1 (defense against misconfiguration)",
			addr: "0.0.0.0:9090",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "0.0.0.0:non-standard port normalised",
			addr: "0.0.0.0:9191",
			want: "http://127.0.0.1:9191",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := internalAddrToBaseURL(tt.addr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAccessCoreModule_InvalidAdminProvisionMode_FailsFast(t *testing.T) {
	shared := buildTestSharedDeps(t)
	t.Setenv(AdminProvisionModeEnv, "bootstrp")

	_, _, _, err := AccessCoreModule{}.Provide(context.Background(), shared)
	require.Error(t, err)
	assert.Contains(t, err.Error(), AdminProvisionModeEnv)
	assert.Contains(t, err.Error(), "bootstrp")
}

func TestAccessCoreModule_ForceBootstrapDoesNotMaskInvalidProvisionMode(t *testing.T) {
	shared := buildTestSharedDeps(t)
	t.Setenv(AdminProvisionModeEnv, "bootstrp")

	_, _, _, err := AccessCoreModule{ForceBootstrap: true}.Provide(context.Background(), shared)
	require.Error(t, err)
	assert.Contains(t, err.Error(), AdminProvisionModeEnv)
	assert.Contains(t, err.Error(), "bootstrp")
}
