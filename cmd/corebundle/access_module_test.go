package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
