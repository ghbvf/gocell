//go:build archtest_fixture

// Package tearctxparentfixture is the RED/GREEN fixture for
// PHASE10-TEARCTX-PARENT-CHAIN-GUARD-01.
//
// It provides:
//   - violatingPhase: a phase10-like function that directly calls
//     context.WithTimeout inside its body (should be caught by rule ①).
//   - violatingHelper: a freshShutdownCtx-like function that parents on a
//     non-Background ctx (should be caught by rule ②).
//   - compliantPhase + compliantHelper: the correct pattern that must pass
//     both rules without any diagnostic.
//
// This fixture must NOT be importable from production code. The
// //go:build archtest_fixture directive ensures it is excluded from all
// standard and integration builds.
package tearctxparentfixture

import (
	"context"
	"time"
)

// FixtureBootstrap is a minimal replica of Bootstrap used so the fixture
// compiles without importing the real runtime/bootstrap package.
type FixtureBootstrap struct {
	shutdownTimeout time.Duration //nolint:unused // read only via AST-loaded fixture funcs (themselves dead to the compiler)
}

// --- RED fixtures ---

// violatingPhase is a phase10-like function that calls context.WithTimeout
// directly instead of delegating to a helper. Rule ① (funnel scan) must
// flag this.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func (b *FixtureBootstrap) violatingPhase() {
	// VIOLATION: direct context.WithTimeout call inside a phase10-like function.
	ctx, cancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
	defer cancel()
	_ = ctx
}

// violatingHelper is a freshShutdownCtx-like function that parents on a
// non-Background context instead of context.Background(). Rule ② (Hard core)
// must flag this because Args[0] is not a context.Background() call.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func (b *FixtureBootstrap) violatingHelper(parent context.Context) (context.Context, context.CancelFunc) {
	// VIOLATION: parent is not context.Background().
	return context.WithTimeout(parent, b.shutdownTimeout)
}

// violatingHelperTODO is a freshShutdownCtx-like function that parents on
// context.TODO() instead of context.Background(). Rule ② must flag this too —
// only context.Background() is the sanctioned root parent; TODO is a
// placeholder, not an intentional root.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func (b *FixtureBootstrap) violatingHelperTODO() (context.Context, context.CancelFunc) {
	// VIOLATION: parent is context.TODO(), not context.Background().
	return context.WithTimeout(context.TODO(), b.shutdownTimeout)
}

// --- GREEN fixtures ---

// compliantHelper is the correct freshShutdownCtx-like function: it always
// parents on context.Background(), satisfying rule ②.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func (b *FixtureBootstrap) compliantHelper() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), b.shutdownTimeout)
}

// compliantPhase is the correct phase10-like function: it delegates to
// compliantHelper instead of calling context.WithTimeout directly, satisfying
// rule ①.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func (b *FixtureBootstrap) compliantPhase() {
	ctx, cancel := b.compliantHelper()
	defer cancel()
	_ = ctx
}
