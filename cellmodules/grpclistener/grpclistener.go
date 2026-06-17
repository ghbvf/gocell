// Package grpclistener is the composition-root helper for the platform-bundle gRPC
// listener: it resolves the listen address + transport security from the
// environment (GOCELL_GRPC_* convention) and builds the adapters/grpc server from a
// caller-supplied interceptor.Deps. It is shared by every platform composition root
// that bundles a cell serving a gRPC contract — cmd/corebundle, examples/ssobff,
// examples/corebundlestarter — because accesscore registers grpc.auth.session.verify.v1
// unconditionally (cell_gen.go, PR-11 #1154), so any assembly that boots accesscore
// MUST wire a gRPC listener or bootstrap fail-fasts (checkOrphanGRPCServices).
//
// Living in cellmodules/ (the composition-root layer that may depend on adapters/ +
// runtime/) keeps the env→TLS→server logic single-sourced rather than copied per
// root. examples/iotdevice keeps its own copy (a standalone example with its own
// GOCELL_IOTDEVICE_GRPC_* env prefix); folding it in is tracked separately.
package grpclistener

import (
	"fmt"
	"os"
	"strings"

	adaptersgrpc "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
)

const (
	// EnvAddr overrides the gRPC listen address (default DefaultAddr).
	EnvAddr = "GOCELL_GRPC_ADDR"
	// EnvTLSCertFile / EnvTLSKeyFile are PEM file paths for server TLS. Both must be
	// set together; the client CA enables mTLS.
	EnvTLSCertFile     = "GOCELL_GRPC_TLS_CERT_FILE"
	EnvTLSKeyFile      = "GOCELL_GRPC_TLS_KEY_FILE"
	EnvTLSClientCAFile = "GOCELL_GRPC_TLS_CLIENT_CA_FILE"
	// EnvAllowInsecure is the explicit opt-in required to run plaintext in durable
	// mode (e.g. behind a TLS-terminating sidecar).
	EnvAllowInsecure = "GOCELL_GRPC_ALLOW_INSECURE"

	// DefaultAddr is the gRPC listener's default bind address.
	DefaultAddr = ":9095"
)

// AddrFromEnv resolves the gRPC listen address from the environment, falling back
// to DefaultAddr. It is the SINGLE source for the address so the caller binds the
// same addr in both adaptersgrpc.Config.Addr and bootstrap's WithGRPCListener
// (#1737 F2): bootstrap pre-binds the WithGRPCListener addr and serves that
// pre-bound listener, so the adapter's Config.Addr never reaches net.Listen on the
// bootstrap path.
func AddrFromEnv() string {
	if addr := strings.TrimSpace(os.Getenv(EnvAddr)); addr != "" {
		return addr
	}
	return DefaultAddr
}

// ServerFromEnv builds the gRPC server, resolving transport security from the
// environment. addr is the resolved listen address (AddrFromEnv), passed in by the
// caller so the same value is also handed to WithGRPCListener. deps carries the
// interceptor wiring inputs: interceptor.NewServerInterceptors mints the ONE shared
// registrar + drain (#1752), builds both unary and stream chains from them, and
// adaptersgrpc.New binds them — so the composition root supplies neither registrar
// nor drain and "forgot the stream chain" is unrepresentable.
func ServerFromEnv(
	durabilityMode outbox.DurabilityMode,
	addr string,
	deps interceptor.Deps,
) (*adaptersgrpc.Server, error) {
	tlsCfg, err := tlsConfigFromEnv(durabilityMode)
	if err != nil {
		return nil, err
	}
	return adaptersgrpc.New(adaptersgrpc.Config{
		Addr:         addr,
		TLS:          tlsCfg,
		Interceptors: interceptor.NewServerInterceptors(deps),
	})
}

// tlsConfigFromEnv resolves the gRPC TLSConfig from the environment.
//
//   - cert+key set (optionally client CA) → server TLS / mTLS.
//   - no TLS material, demo mode → plaintext (the adapter logs a Warn on a
//     non-loopback plaintext bind).
//   - no TLS material, durable mode → require an explicit insecure opt-in, else
//     fail fast so a real deployment cannot ship plaintext by omission.
func tlsConfigFromEnv(durabilityMode outbox.DurabilityMode) (adaptersgrpc.TLSConfig, error) {
	certPath := strings.TrimSpace(os.Getenv(EnvTLSCertFile))
	keyPath := strings.TrimSpace(os.Getenv(EnvTLSKeyFile))
	caPath := strings.TrimSpace(os.Getenv(EnvTLSClientCAFile))

	if certPath != "" || keyPath != "" || caPath != "" {
		return tlsConfigFromPaths(certPath, keyPath, caPath)
	}

	// No TLS material: demo runs plaintext; durable mode must opt in explicitly.
	if durabilityMode == outbox.DurabilityDurable && !envTrue(EnvAllowInsecure) {
		return adaptersgrpc.TLSConfig{}, fmt.Errorf(
			"durable mode requires gRPC TLS (set %s + %s, optionally %s for mTLS) "+
				"or an explicit %s=true to run plaintext behind a TLS-terminating sidecar",
			EnvTLSCertFile, EnvTLSKeyFile, EnvTLSClientCAFile, EnvAllowInsecure,
		)
	}
	return adaptersgrpc.TLSConfig{AllowInsecure: true}, nil
}

// tlsConfigFromPaths reads the server cert/key (required) and optional client CA
// (mTLS) PEM files into a TLSConfig.
func tlsConfigFromPaths(certPath, keyPath, caPath string) (adaptersgrpc.TLSConfig, error) {
	if certPath == "" || keyPath == "" {
		return adaptersgrpc.TLSConfig{}, fmt.Errorf(
			"%s and %s must both be set to enable gRPC TLS", EnvTLSCertFile, EnvTLSKeyFile,
		)
	}
	certPEM, err := readPEMFile(EnvTLSCertFile, certPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	keyPEM, err := readPEMFile(EnvTLSKeyFile, keyPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	tlsCfg := adaptersgrpc.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM}
	if caPath == "" {
		return tlsCfg, nil
	}
	caPEM, err := readPEMFile(EnvTLSClientCAFile, caPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	tlsCfg.ClientCAPEM = caPEM
	return tlsCfg, nil
}

// readPEMFile reads a PEM file, wrapping read errors with the env var name.
func readPEMFile(envName, path string) ([]byte, error) {
	// G304: the path is operator-provided TLS material from env config, not
	// untrusted input — reading it is the intended behavior.
	pem, err := os.ReadFile(path) //nolint:gosec // operator-provided deployment config path
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", envName, err)
	}
	return pem, nil
}

// envTrue reports whether the named env var is set to "true" (case-insensitive).
func envTrue(key string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(key)), "true")
}
