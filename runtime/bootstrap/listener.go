package bootstrap

// listener.go — WithListener declarative API for runtime/bootstrap.
//
// ref: go-kratos/kratos transport/http/server.go — ServerOption pattern (opts applied to server config struct)
// ref: zeromicro/go-zero rest/server.go — per-listener option composability

import (
	"crypto/tls"
	"net"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
)

// listenerConfig holds the resolved configuration for a single physical
// HTTP listener. Populated by WithListener and its sub-options; validated
// by Bootstrap.phase0ValidateOptions before any component starts.
type listenerConfig struct {
	ref       cell.ListenerRef
	addr      string
	authChain []cell.ListenerAuth // authentication plan chain; nil means no auth (AuthNone behaviour)
	net       net.Listener        // optional: pre-bound listener for tests
	tls       *tls.Config         // optional: TLS termination config
	shutGrace time.Duration       // optional: overrides global shutdownTimeout
}

// ListenerOption configures a single listener within WithListener.
type ListenerOption func(*listenerConfig)

// WithListenerNet injects a pre-bound net.Listener into the listener config.
// When set, the addr argument to WithListener is used only for logging — the
// socket is already bound. Passing nil stores nil; phase0 validation rejects
// a nil net listener only when addr is also empty.
func WithListenerNet(ln net.Listener) ListenerOption {
	return func(c *listenerConfig) {
		c.net = ln
	}
}

// WithListenerTLS sets the TLS configuration for the listener. Passing nil
// stores nil; phase0 ignores a nil TLS config (plain-text mode).
func WithListenerTLS(cfg *tls.Config) ListenerOption {
	return func(c *listenerConfig) {
		c.tls = cfg
	}
}

// WithListenerShutdownGrace sets the graceful shutdown duration for this
// listener. A negative value is stored as-is; phase0 rejects negative
// shutdown grace durations. Zero means inherit the global shutdownTimeout.
func WithListenerShutdownGrace(d time.Duration) ListenerOption {
	return func(c *listenerConfig) {
		c.shutGrace = d
	}
}

// WithListener declares a physical HTTP listener and appends it to the
// Bootstrap's listenerConfigs map. Registering the same ref twice is a
// phase0 error (duplicate listener declaration).
//
// authChain is the ordered slice of ListenerAuth plans applied uniformly to
// every route mounted on this listener. RouteGroups inherit this chain — there
// is no group-level override (PR269 round-3: cells needing a different scheme
// must declare their routes on a different listener). Listener auth is explicit:
// a nil or empty chain is a phase0 error. Pass
// []cell.ListenerAuth{cell.AuthNone{}} for a deliberate no-auth listener
// (e.g. HealthListener behind a Kubernetes probe path). Route-specific policy
// and exemptions still live on auth.Mount.
//
// authChain semantics: when chain contains both AuthJWT and non-JWT plans,
// AuthJWT must be at position 0 (validated at phase0). Runtime execution
// order is non-JWT guards (mTLS / ServiceToken) first as outer layer, then
// JWT as the innermost auth check. Declared order is opposite to runtime
// execution order; this is intentional — outer transport guards run before
// the JWT cryptographic check.
//
// ref: go-kratos/kratos transport/http/server.go — options applied before server start.
func WithListener(ref cell.ListenerRef, addr string, authChain []cell.ListenerAuth, opts ...ListenerOption) Option {
	return func(b *Bootstrap) {
		if b.listenerConfigs == nil {
			b.listenerConfigs = make(map[cell.ListenerRef]listenerConfig)
		}
		// CORR-02: track duplicate refs for phase0 validation.
		if _, exists := b.listenerConfigs[ref]; exists {
			b.duplicateListenerRefs = append(b.duplicateListenerRefs, ref)
		}
		cfg := listenerConfig{
			ref:       ref,
			addr:      addr,
			authChain: authChain,
		}
		for _, o := range opts {
			o(&cfg)
		}
		b.listenerConfigs[ref] = cfg
	}
}
