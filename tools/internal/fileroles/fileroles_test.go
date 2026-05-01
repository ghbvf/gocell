package fileroles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsTestCodeAndIsProductionCode_Disjoint verifies that for every
// module-relative path, at most one of IsTestCode and IsProductionCode
// returns true. The two predicates partition the gate scope into
// non-overlapping "test" and "production" subsets so that PROD-DURATION-
// CONST-01 and TEST-TIME-LITERAL-01 never double-scan or miss a file.
func TestIsTestCodeAndIsProductionCode_Disjoint(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, rel string }{
		{"runtime test", "runtime/auth/auth_test.go"},
		{"runtime production", "runtime/auth/auth.go"},
		{"adapters test", "adapters/rabbitmq/subscriber_test.go"},
		{"adapters production", "adapters/rabbitmq/subscriber.go"},
		{"kernel test", "kernel/outbox/consumer_base_test.go"},
		{"kernel production", "kernel/outbox/consumer_base.go"},
		{"cells test", "cells/accesscore/cell_test.go"},
		{"cells production", "cells/accesscore/cell.go"},
		{"pkg test", "pkg/errcode/errcode_test.go"},
		{"pkg production", "pkg/errcode/errcode.go"},
		{"cmd test", "cmd/corebundle/auth_integration_test.go"},
		{"cmd production main", "cmd/corebundle/main.go"},
		{"locktest helper", "runtime/distlock/locktest/fake_clock.go"},
		{"locktest conformance", "runtime/distlock/locktest/conformance.go"},
		{"commandtest helper", "kernel/command/commandtest/inmem.go"},
		{"contracttest helper", "pkg/contracttest/fixture.go"},
		{"examples production", "examples/ssobff/main.go"},
		{"examples test", "examples/ssobff/walkthrough_test.go"},
		{"vendor test", "vendor/github.com/x/y/y_test.go"},
		{"vendor production", "vendor/github.com/x/y/y.go"},
		{"generated production", "generated/x/y.go"},
		{"generated test", "generated/x/y_test.go"},
		{"testdata top-level", "testdata/foo.go"},
		{"testdata top-level test", "testdata/foo_test.go"},
		{"testdata under runtime", "runtime/foo/testdata/inner/x.go"},
		{"archtest self", "tools/archtest/test_time_literal_test.go"},
		{"archtest fixture", "tools/archtest/testdata/x/y_test.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isTest := IsTestCode(tc.rel)
			isProd := IsProductionCode(tc.rel)
			if isTest && isProd {
				t.Fatalf("rel=%q classified as BOTH test and production", tc.rel)
			}
		})
	}
}

func TestIsTestCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rel  string
		want bool
	}{
		{"canonical test", "runtime/auth/auth_test.go", true},
		{"conformance helper", "adapters/redis/conformance.go", true},
		{"locktest fake_clock", "runtime/distlock/locktest/fake_clock.go", true},
		{"outboxtest harness", "runtime/storage/outboxtest/harness.go", true},
		{"storetest suite", "runtime/auth/refresh/storetest/suite.go", true},
		{"healthtest probe", "runtime/http/health/healthtest/healthtest.go", true},
		{"contracttest fixture", "pkg/contracttest/fixture.go", true},
		{"commandtest inmem", "kernel/command/commandtest/inmem.go", true},

		{"production main", "cmd/corebundle/main.go", false},
		{"production code", "kernel/outbox/consumer_base.go", false},

		{"archtest self", "tools/archtest/test_time_literal_test.go", false},
		{"archtest fixture under testdata", "tools/archtest/testdata/x/y_test.go", false},
		{"vendor test", "vendor/github.com/x/y/y_test.go", false},
		{"generated test", "generated/x/y_test.go", false},
		{"top-level testdata test", "testdata/foo_test.go", false},
		{"nested testdata test", "runtime/foo/testdata/inner/x_test.go", false},

		{"empty rel", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsTestCode(tc.rel), "rel=%q", tc.rel)
		})
	}
}

func TestIsProductionCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rel  string
		want bool
	}{
		{"runtime production", "runtime/auth/auth.go", true},
		{"adapters production", "adapters/rabbitmq/subscriber.go", true},
		{"kernel production", "kernel/outbox/consumer_base.go", true},
		{"cells production", "cells/accesscore/cell.go", true},
		{"pkg production", "pkg/errcode/errcode.go", true},
		{"cmd production main", "cmd/corebundle/main.go", true},
		{"tools production", "tools/internal/prodscan/patterns.go", true},

		{"runtime test", "runtime/auth/auth_test.go", false},
		{"conformance helper", "adapters/redis/conformance.go", false},
		{"locktest helper", "runtime/distlock/locktest/fake_clock.go", false},
		{"commandtest helper", "kernel/command/commandtest/inmem.go", false},

		{"examples (test or prod)", "examples/ssobff/main.go", false},
		{"examples test", "examples/ssobff/walkthrough_test.go", false},

		{"archtest self", "tools/archtest/test_time_literal_test.go", false},
		{"vendor production", "vendor/github.com/x/y/y.go", false},
		{"generated production", "generated/x/y.go", false},
		{"top-level testdata", "testdata/foo.go", false},
		{"nested testdata", "runtime/foo/testdata/inner/x.go", false},

		{"empty rel", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsProductionCode(tc.rel), "rel=%q", tc.rel)
		})
	}
}
