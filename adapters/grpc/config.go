package grpc

import (
	"net"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/grpc/interceptor"
)

const (
	// defaultShutdownTimeout is the default GracefulStop budget.
	defaultShutdownTimeout = 30 * time.Second
)

// TLSConfig holds PEM-encoded TLS materials for the gRPC server.
//
// Zero value TLSConfig{} is invalid and is rejected by Config.validate() with
// ErrAdapterGRPCConfigInvalid (V5 fail-closed — neither plaintext nor TLS).
//
// Three modes are supported:
//   - Plaintext (dev or mesh-sidecar): set AllowInsecure = true; leave all PEM
//     fields nil. The bind address is not restricted to loopback (see
//     AllowInsecure field doc); the explicit opt-in is the only fail-closed gate.
//   - Server-side TLS: set CertPEM + KeyPEM; leave ClientCAPEM nil.
//   - Mutual TLS (mTLS): set CertPEM + KeyPEM + ClientCAPEM.
//
// AllowInsecure and any PEM material are mutually exclusive (V2 validation).
// Omitting both AllowInsecure and PEM fields is rejected fail-closed (V5).
type TLSConfig struct {
	// AllowInsecure enables plaintext (no TLS). Explicit opt-in (dev or
	// mesh-sidecar). Must not be combined with CertPEM, KeyPEM, or ClientCAPEM.
	//
	// The listen address is deliberately NOT restricted to loopback when
	// AllowInsecure is set. Binding plaintext on a non-loopback address (e.g.
	// 0.0.0.0) is a legitimate production posture behind a service-mesh sidecar
	// that terminates mTLS at the pod boundary and forwards cleartext over the
	// loopback-equivalent pod network. The fail-closed guarantee here is solely
	// the explicit opt-in: plaintext is impossible unless the operator sets this
	// flag (Config.validate V5 rejects the zero value). A loopback-only check
	// would break the sidecar topology and is intentionally omitted.
	//
	// Serving plaintext on a non-loopback address emits a startup slog.Warn (once
	// per Serve) flagging the posture — observability, not a gate. This is
	// expected in the sidecar topology; see warnIfInsecureNonLoopback (server.go).
	AllowInsecure bool

	// CertPEM is the PEM-encoded server leaf certificate. Required for TLS/mTLS.
	CertPEM []byte

	// KeyPEM is the PEM-encoded server private key. Required for TLS/mTLS.
	KeyPEM []byte

	// ClientCAPEM is the PEM-encoded CA bundle for client certificate verification.
	// Non-empty enables mTLS (RequireAndVerifyClientCert).
	ClientCAPEM []byte
}

// Config holds the gRPC server configuration.
type Config struct {
	// Addr is the listen address (e.g. ":9000"). Required.
	Addr string

	// ShutdownTimeout bounds the GracefulStop phase. Defaults to 30s.
	ShutdownTimeout time.Duration

	// TLS configures transport security. See TLSConfig for the three supported modes.
	TLS TLSConfig

	// Interceptors are the single gRPC wiring object. New always derives BOTH
	// unary and streaming interceptor chains from this one Deps value and binds
	// Interceptors.Registrar to the underlying grpc.Server. That makes it
	// impossible for a composition root to serve a streaming RPC while forgetting
	// interceptor.NewStreamChain, or to pass a different Registrar/Drain instance
	// to the adapter than the chains observe (#1752/#1153 Hard closure).
	Interceptors interceptor.Deps
}

// applyDefaults fills zero-value fields with their defaults.
func (c *Config) applyDefaults() {
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = defaultShutdownTimeout
	}
}

// validate checks the configuration for required fields and conflicting options.
// It does NOT perform network I/O; TLS credential building happens in New.
func (c *Config) validate() error {
	// V1: Addr is required — caller error.
	if c.Addr == "" {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: Addr is required; set Config.Addr to a listen address (e.g. \":9000\")")
	}

	// V1b: Addr must be syntactically valid host:port — caller error. Classify
	// the malformed-address failure here at config time rather than letting it
	// surface later from net.Listen as an infrastructure (ErrAdapterGRPCListen)
	// error, which would mislead operators about the failure domain.
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return errcode.Wrap(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: Addr is not a valid host:port", err)
	}

	// V1c: ShutdownTimeout must not be negative — caller error. A negative
	// budget would make the drain ctx expire immediately, silently degrading
	// every graceful stop into a hard Stop(). Zero is valid (applyDefaults fills
	// the 30s default); only an explicit negative is rejected.
	if c.ShutdownTimeout < 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: ShutdownTimeout must not be negative; leave it zero for the default or set a positive duration")
	}

	// V2–V5: transport-security shape (mutual exclusion + fail-closed + required PEM).
	if err := c.validateTLS(); err != nil {
		return err
	}

	// V6: Interceptors.Registrar is required (Option 3 #1152) — the shared
	// method→cellID source the adapter binds and both chains read. No
	// self-construct fallback: a missing one is a composition-root wiring bug
	// (fail-closed) that would otherwise silently degrade cell attribution to
	// _runtime. Checked after Addr/TLS so common misconfigurations surface first.
	if c.Interceptors.Registrar == nil {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: Registrar is required; create it with runtimegrpc.NewServiceRegistrar() at the "+
				"composition root and pass it as Config.Interceptors.Registrar")
	}

	// V6b: Interceptors.CellIDClosedSet is required by both chains. Without it,
	// every attributed cell would be rejected from the metrics label closed set
	// and relabeled to _runtime.
	if len(c.Interceptors.CellIDClosedSet) == 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: Interceptors.CellIDClosedSet is required; pass the assembly cell-id set")
	}

	// V7: Interceptors.Drain is required (Option 3, #1153) — the shared drain
	// signal the stream interceptor chain binds in-flight streams to and the
	// adapter triggers at GracefulStop start. Since New owns both chain
	// construction and adapter binding, one Deps value guarantees same-instance
	// wiring. Validate (nil-receiver safe) also rejects a zero-value
	// new(DrainSignal), which has a nil cancel and would otherwise panic.
	if c.Interceptors.Drain.Validate() != nil {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: Drain is required; create it with runtimegrpc.NewDrainSignal() at the "+
				"composition root (a nil or zero-value DrainSignal is rejected — it would panic "+
				"at GracefulStop) and pass it as Config.Interceptors.Drain. A unary-only "+
				"server still wires it — the trigger is a harmless no-op when no StreamDrain "+
				"interceptor consumes it")
	}

	return nil
}

// validateTLS checks the transport-security shape (V2–V5): AllowInsecure and TLS
// material are mutually exclusive (V2); when not plaintext, some material must be
// present (V5) and CertPEM (V3) + KeyPEM (V4) are required. Extracted from
// validate to keep its cognitive complexity within budget.
func (c *Config) validateTLS() error {
	hasCert := len(c.TLS.CertPEM) > 0
	hasKey := len(c.TLS.KeyPEM) > 0
	hasCA := len(c.TLS.ClientCAPEM) > 0

	// V2: AllowInsecure and TLS material are mutually exclusive — caller error.
	if c.TLS.AllowInsecure && (hasCert || hasKey || hasCA) {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: AllowInsecure and TLS material (CertPEM/KeyPEM/ClientCAPEM) are mutually exclusive; "+
				"use AllowInsecure for plaintext-only mode or supply PEM material for TLS")
	}
	if c.TLS.AllowInsecure {
		return nil
	}

	// V5: fail-closed — neither plaintext nor TLS configured — caller error.
	if !hasCert && !hasKey && !hasCA {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: no TLS configuration; set AllowInsecure=true for plaintext (dev or mesh-sidecar) "+
				"or supply CertPEM+KeyPEM for TLS")
	}
	// V3: CertPEM required when any TLS material is present — caller error.
	if !hasCert {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: TLS.CertPEM is required when configuring TLS; "+
				"supply the PEM-encoded server certificate")
	}
	// V4: KeyPEM required when any TLS material is present — caller error.
	if !hasKey {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: TLS.KeyPEM is required when configuring TLS; "+
				"supply the PEM-encoded server private key")
	}
	return nil
}
