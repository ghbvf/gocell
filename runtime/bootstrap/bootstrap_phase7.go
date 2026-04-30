package bootstrap

// bootstrap_phase7.go contains the HTTP listener runtime used by Run phase7.
// The code stays in package bootstrap so tests can keep white-box coverage of
// listener ordering, socket ownership, shutdown attribution, and ctx budgeting.
//
// ref: go-kratos/kratos app.go - per-server goroutine lifecycle.
// ref: go-kratos/kratos transport/http/server.go - http.ErrServerClosed handling.
// ref: kubernetes/apiserver pkg/server/secure_serving.go - pre-bound listeners.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
)

const (
	// defaultBootstrapHTTPReadHeaderTimeout is the http.Server ReadHeaderTimeout.
	// Prevents Slowloris attacks by bounding how long a client can take to send
	// request headers.
	defaultBootstrapHTTPReadHeaderTimeout = 10 * time.Second
	// defaultBootstrapHTTPReadTimeout is the http.Server ReadTimeout.
	defaultBootstrapHTTPReadTimeout = 30 * time.Second
	// defaultBootstrapHTTPWriteTimeout is the http.Server WriteTimeout.
	defaultBootstrapHTTPWriteTimeout = 30 * time.Second
	// defaultBootstrapHTTPIdleTimeout is the http.Server IdleTimeout for keep-alive
	// connections.
	defaultBootstrapHTTPIdleTimeout = 60 * time.Second
)

// boundServer holds a resolved HTTP server and its associated listener.
type boundServer struct {
	name      string
	srv       *http.Server
	ln        net.Listener
	owned     bool          // true when bootstrap bound the socket (not caller-injected)
	shutGrace time.Duration // 0 means inherit the global shutdownTimeout
	authDesc  string        // OPS-09: auth chain description for startup log
	stopped   chan struct{} // closed by the Serve goroutine when it exits
}

// phase7StartHTTPServer creates and starts N http.Servers, one per declared
// listener. Sockets are pre-bound synchronously so port conflicts surface
// before any goroutine starts.
func (b *Bootstrap) phase7StartHTTPServer(s *phaseState) error {
	servers, err := b.phase7BindListeners(s)
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		return nil
	}
	httpErrCh := b.phase7ServeAll(servers)
	s.httpErrCh = httpErrCh
	shutTasks := boundServersToTasks(servers)
	// HTTP intake is NOT registered into the LIFO teardown chain. It runs as an
	// explicit drain stage in phase10 BEFORE the LIFO loop so that in-flight
	// requests can write through to still-healthy backends (workers / event
	// router / assembly) before those backends shut down.
	// ref: kube-apiserver genericapiserver.go RunWithContext shutdown signal graph
	//      (NotAcceptingNewRequest → InFlightRequestsDrained → stopHttpServerCtx).
	s.httpDrain = func(c context.Context) error {
		return shutdownAllServers(c, shutTasks)
	}
	return nil
}

// phase7BindListeners pre-binds all declared listeners synchronously. If any
// bind fails, already-owned sockets are closed before returning the error.
//
// CORR-06: sort by ref.String() so bind order is deterministic across runs
// and log lines appear in a consistent order for operators.
// SEC-11: http.Server is constructed with ReadTimeout, WriteTimeout, and
// IdleTimeout to prevent Slowloris / slow-write DoS attacks.
// OPS-06: emit slog.Info after each successful bind (listener + addr + auth).
// OPS-07: emit slog.Warn when a non-loopback listener binds with AuthNone or empty auth chain.
func (b *Bootstrap) phase7BindListeners(s *phaseState) ([]boundServer, error) {
	refs := make([]cell.ListenerRef, 0, len(b.listenerConfigs))
	for ref := range b.listenerConfigs {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })

	var servers []boundServer
	for _, ref := range refs {
		cfg := b.listenerConfigs[ref]
		rtr, ok := s.routers[ref]
		if !ok {
			return nil, fmt.Errorf("bootstrap: no router for listener %q", ref.String())
		}
		ln, owned, err := resolveListener(cfg)
		if err != nil {
			closeOwnedSockets(servers)
			slog.Error("bootstrap: failed to bind HTTP listener",
				slog.String("listener", ref.String()),
				slog.String("addr", cfg.addr),
				slog.Any("error", err))
			return nil, fmt.Errorf("bootstrap: listen %s %s: %w", ref.String(), cfg.addr, err)
		}

		authDesc := describeAuthChain(cfg.authChain)
		slog.Info("bootstrap: HTTP listener bound",
			slog.String("listener", ref.String()),
			slog.String("addr", ln.Addr().String()),
			slog.String("auth", authDesc))

		// OPS-07 / F7 round-3: warn when a non-loopback address is served with
		// AuthNone or an empty auth chain.
		if authDesc == "none" || authDesc == "" {
			if tcpAddr, ok2 := ln.Addr().(*net.TCPAddr); ok2 && !tcpAddr.IP.IsLoopback() {
				slog.Warn("bootstrap: listener bound to non-loopback address without auth; ensure network-level isolation",
					slog.String("listener", ref.String()),
					slog.String("addr", ln.Addr().String()),
					slog.Bool("wildcard_bind", tcpAddr.IP.IsUnspecified()),
					slog.Bool("explicit_auth_none", explicitAuthNone(cfg.authChain)))
			}
		}

		servers = append(servers, boundServer{
			name: ref.String(),
			srv: &http.Server{
				Handler:           rtr.Handler(),
				ReadHeaderTimeout: defaultBootstrapHTTPReadHeaderTimeout,
				ReadTimeout:       defaultBootstrapHTTPReadTimeout,
				WriteTimeout:      defaultBootstrapHTTPWriteTimeout,
				IdleTimeout:       defaultBootstrapHTTPIdleTimeout,
			},
			ln:        ln,
			owned:     owned,
			shutGrace: cfg.shutGrace,
			authDesc:  authDesc,
			stopped:   make(chan struct{}),
		})
	}
	return servers, nil
}

// closeOwnedSockets closes all sockets that bootstrap owns (i.e. not caller-injected).
func closeOwnedSockets(servers []boundServer) {
	for _, prev := range servers {
		if prev.owned {
			_ = prev.ln.Close()
		}
	}
}

// phase7ServeAll starts all servers in background goroutines and returns a
// channel that receives errors and is closed when all servers have stopped.
func (b *Bootstrap) phase7ServeAll(servers []boundServer) chan error {
	n := len(servers)
	httpErrCh := make(chan error, n)
	pending := int32(n)
	for _, bs := range servers {
		go func() {
			defer func() {
				closeServerStopped(bs.stopped)
				if atomic.AddInt32(&pending, -1) == 0 {
					close(httpErrCh)
				}
			}()
			slog.Info("bootstrap: HTTP server starting",
				slog.String("listener", bs.name),
				slog.String("addr", bs.ln.Addr().String()),
				slog.String("auth", bs.authDesc))
			err := bs.srv.Serve(bs.ln)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				httpErrCh <- fmt.Errorf("%s listener: %w", bs.name, err)
			}
		}()
	}
	return httpErrCh
}

func closeServerStopped(stopped chan struct{}) {
	if stopped != nil {
		close(stopped)
	}
}

// resolveListener returns the net.Listener for a listenerConfig. When cfg.net
// is set, the caller owns the socket. Otherwise bootstrap binds a TCP socket.
func resolveListener(cfg listenerConfig) (ln net.Listener, owned bool, err error) {
	if cfg.net != nil {
		return cfg.net, false, nil
	}
	tcpLn, listenErr := net.Listen("tcp", cfg.addr)
	if listenErr != nil {
		return nil, false, listenErr
	}
	if cfg.tls != nil {
		return tls.NewListener(tcpLn, cfg.tls), true, nil
	}
	return tcpLn, true, nil
}
