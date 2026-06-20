// Package grpclistener is the composition-root helper for gRPC listeners: it
// resolves the listen address + transport security from a caller-selected env
// prefix and builds the adapters/grpc server from a caller-supplied
// interceptor.Deps. It is shared by every composition root that bundles a cell
// serving a gRPC contract because those roots MUST wire a gRPC listener or
// bootstrap fail-fasts (checkOrphanGRPCServices).
//
// The package has its own narrow module under cellmodules/ so standalone
// examples can depend on this ability helper without inheriting the wider
// platform cellmodules module graph. Platform roots use PlatformEnv
// (GOCELL_GRPC_*); standalone examples can pass their own EnvConfig prefix while
// retaining the same TLS and fail-closed behavior.
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

// EnvConfig selects the gRPC listener env namespace and fallback bind address.
type EnvConfig struct {
	Prefix      string
	DefaultAddr string
}

// PlatformEnv is the default platform-bundle gRPC listener env namespace.
var PlatformEnv = EnvConfig{Prefix: "GOCELL_GRPC", DefaultAddr: DefaultAddr}

// AddrFromEnv resolves the gRPC listen address from the environment, falling back
// to env.DefaultAddr. It is the SINGLE source for the address so the caller binds
// the same addr in both adaptersgrpc.Config.Addr and bootstrap's WithGRPCListener
// (#1737 F2): bootstrap pre-binds the WithGRPCListener addr and serves that
// pre-bound listener, so the adapter's Config.Addr never reaches net.Listen on the
// bootstrap path.
func AddrFromEnv(env EnvConfig) string {
	if addr := strings.TrimSpace(os.Getenv(env.envName("ADDR"))); addr != "" {
		return addr
	}
	return env.withDefaults().DefaultAddr
}

// ServerFromEnv builds the gRPC server, resolving transport security from the
// environment. addr is the resolved listen address (AddrFromEnv), passed in by the
// caller so the same value is also handed to WithGRPCListener. deps carries the
// interceptor wiring inputs: interceptor.NewServerInterceptors mints the ONE shared
// registrar + drain (#1752), builds both unary and stream chains from them, and
// adaptersgrpc.New binds them — so the composition root supplies neither registrar
// nor drain and "forgot the stream chain" is unrepresentable.
func ServerFromEnv(
	env EnvConfig,
	durabilityMode outbox.DurabilityMode,
	addr string,
	deps interceptor.Deps,
) (*adaptersgrpc.Server, error) {
	tlsCfg, err := tlsConfigFromEnv(env, durabilityMode)
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
func tlsConfigFromEnv(env EnvConfig, durabilityMode outbox.DurabilityMode) (adaptersgrpc.TLSConfig, error) {
	env = env.withDefaults()
	certEnv := env.envName("TLS_CERT_FILE")
	keyEnv := env.envName("TLS_KEY_FILE")
	caEnv := env.envName("TLS_CLIENT_CA_FILE")
	allowInsecureEnv := env.envName("ALLOW_INSECURE")

	certPath := strings.TrimSpace(os.Getenv(certEnv))
	keyPath := strings.TrimSpace(os.Getenv(keyEnv))
	caPath := strings.TrimSpace(os.Getenv(caEnv))

	if certPath != "" || keyPath != "" || caPath != "" {
		return tlsConfigFromPaths(certEnv, keyEnv, caEnv, certPath, keyPath, caPath)
	}

	// No TLS material: demo runs plaintext; durable mode must opt in explicitly.
	if durabilityMode == outbox.DurabilityDurable && !envTrue(allowInsecureEnv) {
		return adaptersgrpc.TLSConfig{}, fmt.Errorf(
			"durable mode requires gRPC TLS (set %s + %s, optionally %s for mTLS) "+
				"or an explicit %s=true to run plaintext behind a TLS-terminating sidecar",
			certEnv, keyEnv, caEnv, allowInsecureEnv,
		)
	}
	return adaptersgrpc.TLSConfig{AllowInsecure: true}, nil
}

// tlsConfigFromPaths reads the server cert/key (required) and optional client CA
// (mTLS) PEM files into a TLSConfig.
func tlsConfigFromPaths(
	certEnv, keyEnv, caEnv string,
	certPath, keyPath, caPath string,
) (adaptersgrpc.TLSConfig, error) {
	if certPath == "" || keyPath == "" {
		return adaptersgrpc.TLSConfig{}, fmt.Errorf(
			"%s and %s must both be set to enable gRPC TLS", certEnv, keyEnv,
		)
	}
	certPEM, err := readPEMFile(certEnv, certPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	keyPEM, err := readPEMFile(keyEnv, keyPath)
	if err != nil {
		return adaptersgrpc.TLSConfig{}, err
	}
	tlsCfg := adaptersgrpc.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM}
	if caPath == "" {
		return tlsCfg, nil
	}
	caPEM, err := readPEMFile(caEnv, caPath)
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

func (env EnvConfig) withDefaults() EnvConfig {
	if strings.TrimSpace(env.Prefix) == "" {
		env.Prefix = PlatformEnv.Prefix
	}
	if strings.TrimSpace(env.DefaultAddr) == "" {
		env.DefaultAddr = PlatformEnv.DefaultAddr
	}
	return env
}

func (env EnvConfig) envName(suffix string) string {
	env = env.withDefaults()
	return strings.TrimRight(env.Prefix, "_") + "_" + suffix
}
