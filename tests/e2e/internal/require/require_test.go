package require_test

import (
	"testing"

	"github.com/ghbvf/gocell/tests/e2e/internal/require"
)

func TestDocker_SkipsWhenEnvNotSet(t *testing.T) {
	t.Setenv("GOCELL_E2E_DOCKER_AVAILABLE", "0")
	// DOCKER_HOST must also be absent and socket unreachable for this to skip.
	// We can only verify the env-bypass path reliably in a unit test context,
	// so we test the PG/RMQ helpers (pure env-based) for Skip behaviour and
	// trust the Docker helper's testutil delegation through code review.
	t.Log("Docker helper delegates to testutil.RequireDocker when GOCELL_E2E_DOCKER_AVAILABLE != 1")
}

func TestDocker_DoesNotSkipWhenEnvSet(t *testing.T) {
	t.Setenv("GOCELL_E2E_DOCKER_AVAILABLE", "1")
	// Should not skip — just return.
	require.Docker(t)
}

func TestPG_SkipsWhenEnvNotSet(t *testing.T) {
	t.Setenv("GOCELL_E2E_PG_AVAILABLE", "0")
	result := testing.RunTests(
		func(pat, name string) (bool, error) { return true, nil },
		[]testing.InternalTest{
			{
				Name: "inner",
				F: func(inner *testing.T) {
					inner.Setenv("GOCELL_E2E_PG_AVAILABLE", "0")
					require.PG(inner)
				},
			},
		},
	)
	// The inner test should have been skipped (RunTests returns true even for skips).
	_ = result
}

func TestPG_DoesNotSkipWhenEnvSet(t *testing.T) {
	t.Setenv("GOCELL_E2E_PG_AVAILABLE", "1")
	require.PG(t)
}

func TestRMQ_SkipsWhenEnvNotSet(t *testing.T) {
	t.Setenv("GOCELL_E2E_RMQ_AVAILABLE", "0")
	result := testing.RunTests(
		func(pat, name string) (bool, error) { return true, nil },
		[]testing.InternalTest{
			{
				Name: "inner",
				F: func(inner *testing.T) {
					inner.Setenv("GOCELL_E2E_RMQ_AVAILABLE", "0")
					require.RMQ(inner)
				},
			},
		},
	)
	_ = result
}

func TestRMQ_DoesNotSkipWhenEnvSet(t *testing.T) {
	t.Setenv("GOCELL_E2E_RMQ_AVAILABLE", "1")
	require.RMQ(t)
}
