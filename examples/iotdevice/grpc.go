// grpc.go owns the environment-driven configuration of the iotdevice gRPC
// listener (address + transport security). It mirrors the env-loading style of
// auth.go / mqtt.go: read env, validate, fail fast — no silent fallback.
//
// The demo runs plaintext out of the box (matching the HTTP :8083 listener).
// Durable mode (GOCELL_IOTDEVICE_DSN set) must not ship plaintext by omission:
// it requires either TLS material or an explicit insecure opt-in, the same
// fail-fast posture buildCursorCodec uses for the cursor key.
package main

import (
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"

	adaptersgrpc "github.com/ghbvf/gocell/adapters/grpc"
	"github.com/ghbvf/gocell/kernel/outbox"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
)

const (
	// envGRPCAddr overrides the gRPC listen address (default defaultGRPCAddr).
	envGRPCAddr = "GOCELL_IOTDEVICE_GRPC_ADDR"
	// envGRPCTLSCertFile / envGRPCTLSKeyFile are PEM file paths for server TLS.
	// Both must be set together; the client CA enables mTLS.
	envGRPCTLSCertFile     = "GOCELL_IOTDEVICE_GRPC_TLS_CERT_FILE"
	envGRPCTLSKeyFile      = "GOCELL_IOTDEVICE_GRPC_TLS_KEY_FILE"
	envGRPCTLSClientCAFile = "GOCELL_IOTDEVICE_GRPC_TLS_CLIENT_CA_FILE"
	// envGRPCAllowInsecure is the explicit opt-in required to run plaintext in
	// durable mode (e.g. behind a TLS-terminating sidecar).
	envGRPCAllowInsecure = "GOCELL_IOTDEVICE_GRPC_ALLOW_INSECURE"

	defaultGRPCAddr = ":8084"
)

// grpcAddrFromEnv resolves the gRPC listen address from the environment, falling
// back to defaultGRPCAddr. It is the SINGLE source for the address so the caller
// binds the same addr in both adaptersgrpc.Config.Addr and bootstrap's
// WithGRPCListener (#1737 F2): bootstrap pre-binds the WithGRPCListener addr and
// serves that pre-bound listener, so the adapter's Config.Addr never reaches
// net.Listen on the bootstrap path — previously GOCELL_IOTDEVICE_GRPC_ADDR
// reached only Config.Addr and was silently ignored.
func grpcAddrFromEnv() string {
	if addr := strings.TrimSpace(os.Getenv(envGRPCAddr)); addr != "" {
		return addr
	}
	return defaultGRPCAddr
}

// newGRPCServerFromEnv builds the gRPC server, resolving transport security from
// the environment. addr is the resolved listen address (grpcAddrFromEnv), passed
// in by the caller so the same value is also handed to WithGRPCListener.
// serverOpts carries the interceptor chains (assembled by the caller so the JWT
// verifier / metrics collector stay in run.go). reg is the shared method→cellID
// registrar (Option 3 #1152): the same instance whose CellIDForMethod the chain's
// cell-attribution interceptor reads, bound to the server here via Config.Registrar.
// drain is the shared drain signal (Option 3 #1153): the SAME instance the stream
// chain's StreamDrain interceptor observes, bound here via Config.Drain so
// GracefulStop's trigger cancels in-flight streams.
func newGRPCServerFromEnv(
	durabilityMode outbox.DurabilityMode,
	addr string,
	reg *runtimegrpc.ServiceRegistrar,
	drain *runtimegrpc.DrainSignal,
	serverOpts []grpc.ServerOption,
) (*adaptersgrpc.Server, error) {
	tlsCfg, err := grpcTLSConfigFromEnv(durabilityMode)
	if err != nil {
		return nil, err
	}
	return adaptersgrpc.New(adaptersgrpc.Config{
		Addr:          addr,
		TLS:           tlsCfg,
		ServerOptions: serverOpts,
		Registrar:     reg,
		Drain:         drain,
	})
}

// grpcTLSConfigFromEnv resolves the gRPC TLSConfig from the environment.
//
//   - cert+key set (optionally client CA) → server TLS / mTLS.
//   - no TLS material, demo mode → plaintext (the adapter logs a Warn on a
//     non-loopback plaintext bind).
//   - no TLS material, durable mode → require an explicit insecure opt-in,
//     else fail fast so a real deployment cannot ship plaintext by omission.
func grpcTLSConfigFromEnv(durabilityMode outbox.DurabilityMode) (adaptersgrpc.TLSConfig, error) {
	certPath := strings.TrimSpace(os.Getenv(envGRPCTLSCertFile))
	keyPath := strings.TrimSpace(os.Getenv(envGRPCTLSKeyFile))
	caPath := strings.TrimSpace(os.Getenv(envGRPCTLSClientCAFile))

	if certPath != "" || keyPath != "" || caPath != "" {
		return tlsConfigFromPaths(certPath, keyPath, caPath)
	}

	// No TLS material: demo runs plaintext; durable mode must opt in explicitly.
	if durabilityMode == outbox.DurabilityDurable && !envTrue(envGRPCAllowInsecure) {
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
	certPEM, err := readPEMFile(envGRPCTLSCertFile, certPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	keyPEM, err := readPEMFile(envGRPCTLSKeyFile, keyPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	tlsCfg := adaptersgrpc.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM}
	if caPath == "" {
		return tlsCfg, nil
	}
	caPEM, err := readPEMFile(envGRPCTLSClientCAFile, caPath)
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
