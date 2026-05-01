// Package table_field_violates verifies that a Duration literal hidden in a
// table-driven test struct slice element is caught by TEST-TIME-LITERAL-01.
// 2 violations expected: the two `Timeout: <lit>` fields.
package table_field_violates

import (
	"testing"
	"time"
)

type testCase struct {
	Name    string
	Timeout time.Duration
}

func TestTable(t *testing.T) {
	cases := []testCase{
		{Name: "fast", Timeout: 50 * time.Millisecond},
		{Name: "slow", Timeout: 5 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			_ = c.Timeout
		})
	}
}
