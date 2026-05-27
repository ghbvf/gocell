// Package bootstrap provides a unified application lifecycle manager for GoCell.
//
// It orchestrates config loading, assembly init/start, HTTP serving, event
// subscriptions, background workers, and graceful shutdown in a single Run call.
//
// Example — note PrimaryListener carries a real auth chain (JWT discovered from
// the assembly's authProvider Cell at phase4). AuthNone is reserved for
// loopback-isolated listeners such as HealthListener. SEC-FAIL-CLOSED: nil
// authChain is rejected at phase0 (`ErrListenerAuthChainMissing`); explicit
// no-auth must use `auth.AuthNone{}`.
//
//	clk := clock.Real()
//	jwtAuth, err := auth.NewAuthJWTFromAssembly(asm)
//	if err != nil { /* handle */ }
//	app := bootstrap.New(
//	    clk,
//	    bootstrap.WithAssembly(asm),
//	    bootstrap.WithListener(cell.PrimaryListener, ":8080",
//	        []auth.ListenerAuth{jwtAuth}),
//	    bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9091",
//	        []auth.ListenerAuth{auth.AuthNone{}}), // loopback-isolated
//	    bootstrap.WithPublisher(pub),
//	    bootstrap.WithSubscriber(sub),
//	)
//	if err := app.Run(ctx); err != nil { ... }
package bootstrap
