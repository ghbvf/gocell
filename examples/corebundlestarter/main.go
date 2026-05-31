// Package main is the entry point for the corebundlestarter example.
//
// This example demonstrates how an external "independent repository" can
// assemble the three GoCell platform cells (accesscore, auditcore, configcore)
// using the public runtime/composition API and cellmodules/<cell>.Module()
// constructors — with zero external infrastructure (memory mode).
//
// It is the M11 dogfood target for issue #1085
// (CellModule / SharedDeps / Builder / App public API).
//
// Usage:
//
//	GOCELL_BOOTSTRAP_ADMIN_USERNAME=admin GOCELL_BOOTSTRAP_ADMIN_PASSWORD=adminpass1 \
//	go run ./examples/corebundlestarter
//
// Note: GOCELL_JWT_ISSUER, GOCELL_JWT_AUDIENCE, and GOCELL_SERVICE_SECRET are
// NOT consumed by this example. The starter uses ephemeral in-process JWT keys
// and a hardcoded dev HMAC key (devServiceSecret const in run.go) for
// memory-mode demo purposes. Those env vars are only relevant to cmd/corebundle
// (the production binary).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ghbvf/gocell/runtime/shutdown"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	ctx, cancel := shutdown.NotifyContext(context.Background())
	defer cancel()

	if err := run(ctx); err != nil {
		slog.Error("corebundlestarter: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	return runStarter(ctx)
}
