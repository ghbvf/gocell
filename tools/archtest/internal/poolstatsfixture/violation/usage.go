//go:build archtest_fixture

// Package violation is a deliberate KERNEL-POOLSTATS-LOCATION-01b negative
// fixture loaded only when the archtest_fixture build tag is set.
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestKERNEL_POOLSTATS_LOCATION_01b_ScannerDetectsViolation via
//
//	archtest.Run(t, archtest.Fixture(archtest.FixtureOpts{Tests: false},
//	    []string{"./tools/archtest/internal/poolstatsfixture/violation"}), rule)
//
// The scan must report the non-stdlib import below as a violation because
// kernel/observability/poolstats must be import-zero (stdlib only).
package violation

import _ "github.com/stretchr/testify/assert"
