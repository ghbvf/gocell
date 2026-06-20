package main

import "testing"

func TestDeploymentInfoFromEnv(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("GOCELL_DEPLOYED_AT", "")
		got := deploymentInfoFromEnv()
		if got.Available || got.LastDeployedAt != nil || got.Source != "unavailable" {
			t.Fatalf("deploymentInfoFromEnv unset = %+v", got)
		}
	})

	t.Run("valid rfc3339 normalizes to utc", func(t *testing.T) {
		t.Setenv("GOCELL_DEPLOYED_AT", "2026-06-19T17:00:00+08:00")
		got := deploymentInfoFromEnv()
		if !got.Available || got.LastDeployedAt == nil || got.Source != "env:GOCELL_DEPLOYED_AT" {
			t.Fatalf("deploymentInfoFromEnv valid = %+v", got)
		}
		if *got.LastDeployedAt != "2026-06-19T09:00:00Z" {
			t.Fatalf("lastDeployedAt = %q, want normalized UTC timestamp", *got.LastDeployedAt)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Setenv("GOCELL_DEPLOYED_AT", "not-a-time")
		got := deploymentInfoFromEnv()
		if got.Available || got.LastDeployedAt != nil || got.Source != "invalid:GOCELL_DEPLOYED_AT" {
			t.Fatalf("deploymentInfoFromEnv invalid = %+v", got)
		}
	})
}
