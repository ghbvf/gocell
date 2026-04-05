// Package bootstrap provides a unified application lifecycle manager for GoCell.
//
// It orchestrates config loading, assembly init/start, HTTP serving, event
// subscriptions, background workers, and graceful shutdown in a single Run call.
//
// Example:
//
//	app := bootstrap.New(
//	    bootstrap.WithAssembly(asm),
//	    bootstrap.WithHTTPAddr(":8080"),
//	    bootstrap.WithEventBus(eb),
//	)
//	if err := app.Run(ctx); err != nil { ... }
package bootstrap
