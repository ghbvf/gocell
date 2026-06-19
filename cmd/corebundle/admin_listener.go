package main

// admin_listener.go — the corebundle operator control-plane (AdminListener) wiring
// for the framework audit chain verify endpoint (#1755).
//
// The AdminListener is declared ONLY when operator credentials are present in the
// environment, mirroring examples/todoorder: the default corebundle deployment
// stays AdminListener-free (the verify endpoint then stays dormant, exactly as the
// projection rebuild endpoint already ships in corebundle). Because the verify
// endpoint is enabled in the SAME operator-credentials block, provisioning the
// audit admin pool (GOCELL_AUDIT_ADMIN_DSN, which enables #1810 super-admin reads)
// WITHOUT operator credentials leaves the verifier injected but the endpoint
// dormant — no AdminListener, no #1810 regression.

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/ghbvf/gocell/adapters/ratelimit"
	"github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/clock"
)

const (
	operatorAdminUsernameEnv = "GOCELL_OPERATOR_ADMIN_USERNAME"
	operatorAdminPasswordEnv = "GOCELL_OPERATOR_ADMIN_PASSWORD"
	adminHTTPAddrEnv         = "GOCELL_ADMIN_HTTP_ADDR"
	// defaultAdminHTTPAddr binds the admin control-plane to loopback: network
	// isolation + operator credentials form a defense-in-depth pair (cell.AdminListener).
	defaultAdminHTTPAddr = "127.0.0.1:9092"
)

// adminHTTPAddr resolves the AdminListener bind address from GOCELL_ADMIN_HTTP_ADDR,
// defaulting to a loopback port. Only consulted when operator credentials enable
// the admin plane. When an override is set and its host is non-loopback, a Warn is
// emitted once (defense-in-depth note, ADR §D6) — the operator may front it with a
// reverse proxy, so the override is still returned.
func adminHTTPAddr() string {
	a := strings.TrimSpace(os.Getenv(adminHTTPAddrEnv))
	if a == "" {
		return defaultAdminHTTPAddr
	}
	// Best-effort non-loopback warning: parse host, skip if empty or unparseable.
	if host, _, err := net.SplitHostPort(a); err == nil && host != "" {
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
			// Log the canonical parsed IP (ip.String()), never the raw env string —
			// it is the value we just validated and cannot carry log-injection
			// control characters. GOCELL_ADMIN_HTTP_ADDR is operator deployment
			// config, not untrusted external input; gosec G706's taint tracker
			// over-approximates by propagating through net.ParseIP→String().
			//nolint:gosec // G706: ip.String() is a sanitized canonical IP from validated operator config, not untrusted input
			slog.Warn("admin control-plane bound to non-loopback address (defense-in-depth: ensure it is fronted by a reverse proxy or firewall)",
				slog.String("host", ip.String()))
		}
	}
	return a
}

// operatorAuthFromEnv builds the AdminListener operator-credential auth plan
// (AuthOperator) from GOCELL_OPERATOR_ADMIN_USERNAME / _PASSWORD. When either is
// unset the operator control-plane is left unconfigured (ok=false) so the default
// corebundle deployment starts without an admin port. When set, the plan is gated
// by a per-IP token-bucket rate limiter (defeats credential brute-force) and a
// slog observer surfaces 401/429 operator-auth failures for brute-force visibility.
func operatorAuthFromEnv(clk clock.Clock) (auth.AuthOperator, bool, error) {
	username := strings.TrimSpace(os.Getenv(operatorAdminUsernameEnv))
	password := os.Getenv(operatorAdminPasswordEnv) // not trimmed — passwords may contain whitespace
	if username == "" || password == "" {
		return auth.AuthOperator{}, false, nil
	}
	limiter := ratelimit.New(ratelimit.Config{Rate: 1, Burst: 5}, clk)
	plan, err := auth.NewAuthOperator([]byte(username), []byte(password), limiter, operatorAuthFailObserver)
	if err != nil {
		return auth.AuthOperator{}, false, fmt.Errorf("build operator admin auth: %w", err)
	}
	return plan, true, nil
}

// operatorAuthFailObserver routes operator-auth failures (401/429) to a warn log so
// brute-force attempts against the admin plane are visible in production.
func operatorAuthFailObserver(ctx context.Context, reason string) {
	slog.WarnContext(ctx, "operator admin auth failed", slog.String("reason", reason))
}
