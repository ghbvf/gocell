package printers

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/verify"
	"github.com/stretchr/testify/require"
)

var verifyGoldenCases = []struct {
	name   string
	result *verify.VerifyResult
}{
	{
		name: "passed_simple",
		result: &verify.VerifyResult{
			TargetID: "accesscore/sessions",
			Passed:   true,
			Results: []verify.TestResult{
				{Name: "TestLogin", Passed: true},
			},
		},
	},
	{
		name: "failed_with_output",
		result: &verify.VerifyResult{
			TargetID: "accesscore/sessions",
			Passed:   false,
			Results: []verify.TestResult{
				{
					Name:   "TestLogin",
					Passed: false,
					Output: "expected 200\ngot 500\nresponse body: internal error",
				},
			},
		},
	},
	{
		name: "manual_pending",
		result: &verify.VerifyResult{
			TargetID: "J-ssologin",
			Passed:   false,
			ManualPending: []string{
				"operator confirms IdP redirect lands on /login",
				"operator validates audit log entry",
			},
		},
	},
	{
		name: "zero_match",
		result: &verify.VerifyResult{
			TargetID: "accesscore/sessions",
			Passed:   false,
			Results: []verify.TestResult{
				{Name: "TestNonexistent", Passed: false, ZeroMatch: true},
			},
			Errors: []error{errors.New("zero tests matched -run pattern")},
		},
	},
	{
		name: "multi_test_mixed",
		result: &verify.VerifyResult{
			TargetID: "accesscore/all",
			Passed:   false,
			Results: []verify.TestResult{
				{Name: "TestA", Passed: true},
				{Name: "TestB", Passed: false, Output: "assertion failed"},
				{Name: "TestC", Passed: true},
			},
			Errors:        []error{errors.New("subprocess exit 1")},
			ManualPending: []string{"manual smoke test pending"},
		},
	},
}

func TestVerifyGolden_Text(t *testing.T) {
	for _, tc := range verifyGoldenCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p, err := NewVerifyPrinter("text", &buf)
			require.NoError(t, err)
			require.NoError(t, p.Print(tc.result))
			assertGolden(t, "text", "verify_"+tc.name+".txt", buf.Bytes())
		})
	}
}

func TestVerifyGolden_JSON(t *testing.T) {
	for _, tc := range verifyGoldenCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p, err := NewVerifyPrinter("json", &buf)
			require.NoError(t, err)
			require.NoError(t, p.Print(tc.result))
			assertGolden(t, "json", "verify_"+tc.name+".json", buf.Bytes())
		})
	}
}

func TestVerifyPrinter_DefaultFormatIsText(t *testing.T) {
	var buf bytes.Buffer
	p, err := NewVerifyPrinter("", &buf)
	require.NoError(t, err)
	require.NoError(t, p.Print(&verify.VerifyResult{TargetID: "x", Passed: true}))
	require.Contains(t, buf.String(), "PASSED")
}

func TestSupportedVerifyFormats(t *testing.T) {
	require.Equal(t, []string{"text", "json"}, SupportedVerifyFormats())
}
