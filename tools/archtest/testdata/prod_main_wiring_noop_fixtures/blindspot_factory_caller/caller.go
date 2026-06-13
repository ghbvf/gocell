//go:build archtest_fixture

// Package blindspotfactorycaller is the "composition root" half of the
// PROD-MAIN-WIRING-NOOP-REJECT-01 factory-wrapping blind-spot reverse self-test.
// It obtains a raw noop sink by calling a factory in ANOTHER package, never
// referencing a forbidden symbol directly. Scanning THIS package yields ZERO
// diagnostics — documenting and regression-pinning the permanent Go ceiling that
// the callsite scan does not follow cross-package call graphs (a helper that
// constructs the sink one package away is invisible). The runtime
// outbox.CheckNotNoop guard (durable mode) is the complementary defense for this
// gap; the in-memory-publisher variant is tracked at #1940.
package blindspotfactorycaller

import blindspotfactoryhelper "github.com/ghbvf/gocell/tools/archtest/testdata/prod_main_wiring_noop_fixtures/blindspot_factory_helper"

var _ = blindspotfactoryhelper.MakeNoopWriter()
