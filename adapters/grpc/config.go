package grpc

import (
	"net"
	"time"

	"google.golang.org/grpc"

	"github.com/ghbvf/gocell/pkg/errcode"
	runtimegrpc "github.com/ghbvf/gocell/runtime/grpc"
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

	// ServerOptions are extra grpc.ServerOptions appended after the TLS
	// credentials option. The composition root (runtime/bootstrap) uses this to
	// inject the unary interceptor chain (runtime/grpc/interceptor.NewUnaryChain).
	// It is not a business-bypass seam: cells/ cannot import adapters/ (layering
	// rule), so only composition roots ever construct a Config.
	ServerOptions []grpc.ServerOption

	// Registrar is the shared method→cellID registry (Option 3, #1152). The
	// composition root creates it FIRST (runtimegrpc.NewServiceRegistrar()) and
	// hands reg.CellIDForMethod to the interceptor chain's cell-attribution
	// interceptor — the chain is composed before this server exists. New binds the
	// constructed *grpc.Server to it via BindServer. Required (no self-construct
	// fallback): if the adapter minted its own registrar, it would differ from the
	// one the chain reads and attribution would silently degrade to _runtime.
	Registrar *runtimegrpc.ServiceRegistrar
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

	hasCert := len(c.TLS.CertPEM) > 0
	hasKey := len(c.TLS.KeyPEM) > 0
	hasCA := len(c.TLS.ClientCAPEM) > 0

	// V2: AllowInsecure and TLS material are mutually exclusive — caller error.
	if c.TLS.AllowInsecure && (hasCert || hasKey || hasCA) {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: AllowInsecure and TLS material (CertPEM/KeyPEM/ClientCAPEM) are mutually exclusive; "+
				"use AllowInsecure for plaintext-only mode or supply PEM material for TLS")
	}

	if !c.TLS.AllowInsecure {
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
	}

	// V6: Registrar is required (Option 3 #1152) — the shared method→cellID source
	// the interceptor chain reads. No self-construct fallback: a missing one is a
	// composition-root wiring bug (fail-closed) that would otherwise silently
	// degrade cell attribution to _runtime. Checked last so the Addr/TLS format
	// errors above surface first (they are the common misconfigurations).
	if c.Registrar == nil {
		return errcode.New(errcode.KindInvalid, ErrAdapterGRPCConfigInvalid,
			"grpc: Registrar is required; create it with runtimegrpc.NewServiceRegistrar() at the "+
				"composition root and pass the same instance to both Config.Registrar and "+
				"interceptor.Deps.Registrar")
	}

	return nil
}
