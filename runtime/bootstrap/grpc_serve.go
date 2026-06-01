package bootstrap

// grpc_serve.go — gRPC listener runtime (Run phase7b serve + phase10 stage2
// drain), GAP-1 PR-5. Mirrors the HTTP phase7 shape (bootstrap_phase7.go):
// sockets are pre-bound synchronously so port conflicts surface before any
// goroutine starts, one serve goroutine per server, and a drain hook consumed by
// phase10 stage2 BEFORE LIFO teardown so in-flight RPCs drain while backends
// (workers / event router / assembly) are still alive — symmetric with HTTP.
//
// Layering: this file holds no adapters/grpc dependency; it drives the
// composition-root-supplied GRPCServer interface only.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
)

// boundGRPC pairs a resolved gRPC listener config with its bound socket.
type boundGRPC struct {
	cfg   grpcListenerConfig
	lis   net.Listener
	owned bool // true when bootstrap bound the socket (not caller-injected)
}

// phase7bStartGRPCServers binds every declared gRPC listener synchronously, then
// starts one serve goroutine per server and wires the drain hook (s.grpcDrain)
// and error channel (s.grpcErrCh). serveCtx is the long-lived run context; drain
// is driven explicitly by GRPCServer.Close in phase10 stage2, not by serveCtx
// cancellation (serveCtx cancel at stage4 is only a safety net).
//
// On a bind failure the already-bound gRPC sockets are closed and the HTTP
// servers started in phase7 are drained before the error is returned — HTTP
// drain is an explicit phase10 stage (not a LIFO teardown), so rollback would
// otherwise leak the serving HTTP goroutines.
func (b *Bootstrap) phase7bStartGRPCServers(serveCtx context.Context, s *phaseState) error {
	if len(b.grpcListenerConfigs) == 0 {
		return nil
	}

	bounds := make([]boundGRPC, 0, len(b.grpcListenerConfigs))
	for _, gc := range b.grpcListenerConfigs {
		lis, owned, err := resolveGRPCListener(gc)
		if err != nil {
			closeOwnedGRPCSockets(bounds)
			b.drainHTTPOnGRPCStartFailure(s)
			slog.Error("bootstrap: failed to bind gRPC listener",
				slog.String("addr", gc.addr), slog.Any("error", err))
			return fmt.Errorf("bootstrap: grpc listen %s: %w", gc.addr, err)
		}
		slog.Info("bootstrap: gRPC listener bound",
			slog.String("addr", lis.Addr().String()), slog.Bool("owned", owned))
		bounds = append(bounds, boundGRPC{cfg: gc, lis: lis, owned: owned})
	}

	s.grpcErrCh = b.grpcServeAll(serveCtx, bounds)
	s.grpcDrain = func(ctx context.Context) error { return drainAllGRPCServers(ctx, bounds) }
	return nil
}

// resolveGRPCListener returns the net.Listener for a gRPC listener config. When
// cfg.net is set the caller owns the socket; otherwise bootstrap binds a TCP
// socket on cfg.addr. Mirrors resolveListener (HTTP).
func resolveGRPCListener(cfg grpcListenerConfig) (ln net.Listener, owned bool, err error) {
	if cfg.net != nil {
		return cfg.net, false, nil
	}
	tcpLn, listenErr := net.Listen("tcp", cfg.addr)
	if listenErr != nil {
		return nil, false, listenErr
	}
	return tcpLn, true, nil
}

// closeOwnedGRPCSockets closes sockets bootstrap bound (not caller-injected).
func closeOwnedGRPCSockets(bounds []boundGRPC) {
	for _, bd := range bounds {
		if bd.owned {
			_ = bd.lis.Close()
		}
	}
}

// grpcServeAll starts each server in a background goroutine and returns a
// channel that receives non-graceful serve errors and is closed when all
// servers have stopped. Mirrors phase7ServeAll (HTTP). GRPCServer.Serve already
// maps a graceful stop to a nil return, so only abnormal errors are surfaced.
func (b *Bootstrap) grpcServeAll(serveCtx context.Context, bounds []boundGRPC) chan error {
	n := len(bounds)
	grpcErrCh := make(chan error, n)
	// maxListeners (math.MaxInt32) makes the int→int32 narrowing total and keeps
	// gosec G115 happy; in practice this cap is never reached (a handful of
	// listeners), mirroring phase7ServeAll (HTTP).
	if n > maxListeners {
		n = maxListeners
	}
	pending := int32(n)
	for _, bd := range bounds {
		go func() {
			defer func() {
				if atomic.AddInt32(&pending, -1) == 0 {
					close(grpcErrCh)
				}
			}()
			addr := bd.lis.Addr().String()
			slog.Info("bootstrap: gRPC server starting", slog.String("addr", addr))
			if err := bd.cfg.server.Serve(serveCtx, bd.lis); err != nil {
				grpcErrCh <- fmt.Errorf("grpc %s: %w", addr, err)
			}
		}()
	}
	return grpcErrCh
}

// drainAllGRPCServers drains all gRPC servers in parallel, each bounded by its
// per-listener shutdown budget (shutTimeout) derived from parent via
// shutdownCtxFor. Mirrors shutdownAllServers (HTTP). Close is idempotent, so a
// server whose serve goroutine already exited drains as a no-op.
func drainAllGRPCServers(parent context.Context, bounds []boundGRPC) error {
	slog.Info("bootstrap: draining gRPC servers")
	resultCh := make(chan error, len(bounds))
	for _, bd := range bounds {
		go func() {
			ctx, cancel := shutdownCtxFor(parent, bd.cfg.shutGrace)
			defer cancel()
			err := bd.cfg.server.Close(ctx)
			if err != nil {
				slog.Error("bootstrap: gRPC server drain failed",
					slog.String("addr", bd.cfg.addr), slog.Any("error", err))
				err = fmt.Errorf("grpc %s drain: %w", bd.cfg.addr, err)
			} else {
				slog.Info("bootstrap: gRPC server drained", slog.String("addr", bd.cfg.addr))
			}
			resultCh <- err
		}()
	}
	var errs []error
	for range bounds {
		if err := <-resultCh; err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// drainHTTPOnGRPCStartFailure drains the HTTP servers started in phase7 when a
// gRPC bind failure aborts startup before phase9. HTTP intake is an explicit
// phase10 stage (s.httpDrain), not a LIFO teardown, so the rollback that follows
// this error would otherwise leave the HTTP serve goroutines running.
func (b *Bootstrap) drainHTTPOnGRPCStartFailure(s *phaseState) {
	if s.httpDrain == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
	defer cancel()
	if err := s.httpDrain(ctx); err != nil {
		slog.Warn("bootstrap: HTTP drain after gRPC start failure returned error",
			slog.Any("error", err))
	}
	s.httpDrain = nil // already drained; prevent any later re-drain
}
