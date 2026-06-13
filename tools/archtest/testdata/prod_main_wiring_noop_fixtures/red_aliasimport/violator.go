//go:build archtest_fixture

// Package redaliasimport is a RED fixture for PROD-MAIN-WIRING-NOOP-REJECT-01
// blind-spot self-test (import alias): it imports kernel/outbox under the alias
// `kout` and calls kout.NewNoopEmitter(). It proves go/types resolution is
// alias-proof — the import alias does not hide the forbidden symbol. Expect one
// diagnostic.
package redaliasimport

import kout "github.com/ghbvf/gocell/kernel/outbox"

var _ = kout.NewNoopEmitter()
