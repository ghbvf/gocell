//go:build archtest_fixture

// Package blindspotfactoryhelper is the cross-package helper half of the
// PROD-MAIN-WIRING-NOOP-REJECT-01 factory-wrapping blind-spot reverse self-test.
// It constructs a raw outbox.NoopWriter inside an exported factory, one package
// away from any composition root. See blindspot_factory_caller for the negative
// assertion this enables.
package blindspotfactoryhelper

import "github.com/ghbvf/gocell/kernel/outbox"

// MakeNoopWriter wraps the raw noop sink construction one package away from the
// caller, so a callsite scan over the caller never sees the forbidden symbol.
func MakeNoopWriter() outbox.Writer { return outbox.NoopWriter{} }
