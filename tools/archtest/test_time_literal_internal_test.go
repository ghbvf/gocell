// Internal unit tests for the TEST-TIME-LITERAL-01 file filter.
//
// The literal-detection predicate (`scanProdDurationAST` /
// `isLiteralDurationExpr` / `exprIsTimeDuration`) is shared with
// PROD-DURATION-CONST-01 and already covered by
// `prod_duration_const_internal_test.go` + `prod_duration_fixtures_test.go`.
// Here we only exercise the *file filter* `testTimeLiteralIncludeAbs`, which
// is the only piece that diverges from PROD-DURATION-CONST-01.
//
// ref: docs/plans/202605011500-029-master-roadmap.md G6
package archtest

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTestTimeLiteralIncludeAbs(t *testing.T) {
	t.Parallel()
	// All test cases use a synthetic root so the test does not depend on
	// the running module's checkout layout.
	root := "/repo"

	cases := []struct {
		name string
		rel  string
		want bool
	}{
		// Canonical *_test.go in any layer is included.
		{"runtime test", "runtime/auth/auth_test.go", true},
		{"cells test", "cells/accesscore/initialadmin/cleaner_test.go", true},
		{"adapters test", "adapters/rabbitmq/subscriber_test.go", true},
		{"kernel test", "kernel/outbox/consumer_base_test.go", true},
		{"cmd test", "cmd/corebundle/auth_integration_test.go", true},
		{"examples test", "examples/ssobff/walkthrough_test.go", true},
		{"pkg test", "pkg/errcode/errcode_test.go", true},
		{"tests integration", "tests/integration/outbox_fullchain_test.go", true},

		// Conformance helpers (driver-conformance suites) are test code.
		{"locktest conformance", "runtime/distlock/locktest/conformance.go", true},
		{"outboxtest conformance", "runtime/storage/outboxtest/conformance.go", true},
		{"storetest conformance", "runtime/auth/refresh/storetest/conformance.go", true},
		{"healthtest conformance", "runtime/http/health/healthtest/conformance.go", true},

		// Test-helper packages: every Go file under them is test code.
		{"locktest fake_clock", "runtime/distlock/locktest/fake_clock.go", true},
		{"outboxtest harness", "runtime/storage/outboxtest/harness.go", true},
		{"storetest suite", "runtime/auth/refresh/storetest/suite.go", true},
		{"contracttest fixture", "pkg/contracttest/fixture.go", true},

		// Production code is NOT included (PROD-DURATION-CONST-01 covers it).
		{"runtime production", "runtime/auth/auth.go", false},
		{"cells production", "cells/accesscore/cell.go", false},
		{"adapters production", "adapters/rabbitmq/subscriber.go", false},
		{"kernel production", "kernel/outbox/consumer_base.go", false},
		{"cmd production main", "cmd/corebundle/main.go", false},

		// Hard exclusions take precedence over inclusion predicates.
		{"archtest self", "tools/archtest/test_time_literal_test.go", false},
		{"archtest fixtures", "tools/archtest/testdata/test_time_literal_fixtures/x/y_test.go", false},
		{"vendor test", "vendor/github.com/x/y/y_test.go", false},
		{"generated test", "generated/x/y_test.go", false},
		{"testdata under runtime", "runtime/foo/testdata/inner/x_test.go", false},

		// Outside-module paths are excluded.
		{"absolute outside", "/usr/local/lib/foo_test.go", false},
		{"parent outside", "../sibling/foo_test.go", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			abs := filepath.Join(root, filepath.FromSlash(tc.rel))
			if filepath.IsAbs(tc.rel) {
				abs = tc.rel
			}
			got := testTimeLiteralIncludeAbs(root, abs)
			assert.Equal(t, tc.want, got, "rel=%s", tc.rel)
		})
	}
}
