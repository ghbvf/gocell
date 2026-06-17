// grpc.go owns the environment-driven configuration of the corebundle gRPC
// listener (address + transport security). accesscore serves the
// grpc.auth.session.verify.v1 contract on cell.PrimaryListener (PR-11 #1154 — the
// first platform-cell gRPC service), so a gRPC listener with that ref MUST be
// wired or bootstrap phase7b fails fast (checkOrphanGRPCServices). The grpc listener
// is therefore always-on in corebundle: the cell registers the service
// unconditionally in cell_gen.go, so there is no per-slice runtime toggle — only the
// listen address is env-configurable.
//
// The demo runs plaintext out of the box (matching the HTTP primary listener).
// Durable / real mode must not ship plaintext by omission: it requires either TLS
// material or an explicit insecure opt-in, the same fail-fast posture the rest of
// corebundle uses for production control-plane material.
//
// This mirrors examples/iotdevice/grpc.go structurally; the env-var names differ
// (GOCELL_GRPC_* vs GOCELL_IOTDEVICE_GRPC_*), so the two are intentionally separate
// rather than sharing a helper for a second site (per the "三处才抽" rule).
package main

import (
	"fmt"
	"os"
	"strings"

	adaptersgrpc "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
)

const (
	// envGRPCAddr overrides the gRPC listen address (default defaultGRPCAddr).
	envGRPCAddr = "GOCELL_GRPC_ADDR"
	// envGRPCTLSCertFile / envGRPCTLSKeyFile are PEM file paths for server TLS.
	// Both must be set together; the client CA enables mTLS.
	envGRPCTLSCertFile     = "GOCELL_GRPC_TLS_CERT_FILE"
	envGRPCTLSKeyFile      = "GOCELL_GRPC_TLS_KEY_FILE"
	envGRPCTLSClientCAFile = "GOCELL_GRPC_TLS_CLIENT_CA_FILE"
	// envGRPCAllowInsecure is the explicit opt-in required to run plaintext in
	// durable mode (e.g. behind a TLS-terminating sidecar).
	envGRPCAllowInsecure = "GOCELL_GRPC_ALLOW_INSECURE"

	defaultGRPCAddr = ":9095"
)

// grpcAddrFromEnv resolves the gRPC listen address from the environment, falling
// back to defaultGRPCAddr. It is the SINGLE source for the address so the caller
// binds the same addr in both adaptersgrpc.Config.Addr and bootstrap's
// WithGRPCListener (#1737 F2): bootstrap pre-binds the WithGRPCListener addr and
// serves that pre-bound listener, so the adapter's Config.Addr never reaches
// net.Listen on the bootstrap path.
func grpcAddrFromEnv() string {
	if addr := strings.TrimSpace(os.Getenv(envGRPCAddr)); addr != "" {
		return addr
	}
	return defaultGRPCAddr
}

// newGRPCServerFromEnv builds the gRPC server, resolving transport security from
// the environment. addr is the resolved listen address (grpcAddrFromEnv), passed
// in by the caller so the same value is also handed to WithGRPCListener. grpcDeps
// carries the interceptor wiring inputs: interceptor.NewServerInterceptors mints
// the ONE shared registrar + drain (#1752), builds both unary and stream chains
// from them, and adaptersgrpc.New binds them — so the composition root supplies
// neither registrar nor drain and "forgot the stream chain" is unrepresentable.
func newGRPCServerFromEnv(
	durabilityMode outbox.DurabilityMode,
	addr string,
	grpcDeps interceptor.Deps,
) (*adaptersgrpc.Server, error) {
	tlsCfg, err := grpcTLSConfigFromEnv(durabilityMode)
	if err != nil {
		return nil, err
	}
	return adaptersgrpc.New(adaptersgrpc.Config{
		Addr:         addr,
		TLS:          tlsCfg,
		Interceptors: interceptor.NewServerInterceptors(grpcDeps),
	})
}

// grpcTLSConfigFromEnv resolves the gRPC TLSConfig from the environment.
//
//   - cert+key set (optionally client CA) → server TLS / mTLS.
//   - no TLS material, demo mode → plaintext (the adapter logs a Warn on a
//     non-loopback plaintext bind).
//   - no TLS material, durable mode → require an explicit insecure opt-in, else
//     fail fast so a real deployment cannot ship plaintext by omission.
func grpcTLSConfigFromEnv(durabilityMode outbox.DurabilityMode) (adaptersgrpc.TLSConfig, error) {
	certPath := strings.TrimSpace(os.Getenv(envGRPCTLSCertFile))
	keyPath := strings.TrimSpace(os.Getenv(envGRPCTLSKeyFile))
	caPath := strings.TrimSpace(os.Getenv(envGRPCTLSClientCAFile))

	if certPath != "" || keyPath != "" || caPath != "" {
		return tlsConfigFromPaths(certPath, keyPath, caPath)
	}

	// No TLS material: demo runs plaintext; durable mode must opt in explicitly.
	if durabilityMode == outbox.DurabilityDurable && !grpcEnvTrue(envGRPCAllowInsecure) {
		return adaptersgrpc.TLSConfig{}, fmt.Errorf(
			"durable mode requires gRPC TLS (set %s + %s, optionally %s for mTLS) "+
				"or an explicit %s=true to run plaintext behind a TLS-terminating sidecar",
			envGRPCTLSCertFile, envGRPCTLSKeyFile, envGRPCTLSClientCAFile, envGRPCAllowInsecure,
		)
	}
	return adaptersgrpc.TLSConfig{AllowInsecure: true}, nil
}

// tlsConfigFromPaths reads the server cert/key (required) and optional client CA
// (mTLS) PEM files into a TLSConfig.
func tlsConfigFromPaths(certPath, keyPath, caPath string) (adaptersgrpc.TLSConfig, error) {
	if certPath == "" || keyPath == "" {
		return adaptersgrpc.TLSConfig{}, fmt.Errorf(
			"%s and %s must both be set to enable gRPC TLS", envGRPCTLSCertFile, envGRPCTLSKeyFile,
		)
	}
	certPEM, err := readGRPCPEMFile(envGRPCTLSCertFile, certPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	keyPEM, err := readGRPCPEMFile(envGRPCTLSKeyFile, keyPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	tlsCfg := adaptersgrpc.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM}
	if caPath == "" {
		return tlsCfg, nil
	}
	caPEM, err := readGRPCPEMFile(envGRPCTLSClientCAFile, caPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	tlsCfg.ClientCAPEM = caPEM
	return tlsCfg, nil
}

// readGRPCPEMFile reads a PEM file, wrapping read errors with the env var name.
func readGRPCPEMFile(envName, path string) ([]byte, error) {
	// G304: the path is operator-provided TLS material from env config, not
	// untrusted input — reading it is the intended behavior.
	pem, err := os.ReadFile(path) //nolint:gosec // operator-provided deployment config path
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", envName, err)
	}
	return pem, nil
}

// grpcEnvTrue reports whether the named env var is set to "true" (case-insensitive).
func grpcEnvTrue(key string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(key)), "true")
}
