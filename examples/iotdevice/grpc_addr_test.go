package main

import "testing"

// TestGRPCAddrFromEnv guards the single-source addr (#1737 F2): the env override
// must be honored, with defaultGRPCAddr as the fallback. run.go passes the result
// to BOTH the adapter config and WithGRPCListener so the env var actually binds.
func TestGRPCAddrFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(envGRPCAddr, "")
		if got := grpcAddrFromEnv(); got != defaultGRPCAddr {
			t.Fatalf("grpcAddrFromEnv() = %q, want default %q", got, defaultGRPCAddr)
		}
	})
	t.Run("honors env override", func(t *testing.T) {
		t.Setenv(envGRPCAddr, "127.0.0.1:19099")
		if got := grpcAddrFromEnv(); got != "127.0.0.1:19099" {
			t.Fatalf("grpcAddrFromEnv() = %q, want 127.0.0.1:19099", got)
		}
	})
}
