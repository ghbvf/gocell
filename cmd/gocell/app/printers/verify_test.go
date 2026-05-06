package printers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/verify"
	"github.com/ghbvf/gocell/pkg/errcode"
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
			Errors: []error{errcode.New(
				errcode.KindNotFound,
				errcode.ErrZeroTestMatch,
				"zero tests matched -run pattern",
			)},
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

func TestVerifyPrinter_ErrcodeUsesPublicMessage(t *testing.T) {
	result := &verify.VerifyResult{
		TargetID: "J-login",
		Passed:   false,
		Errors: []error{
			errcode.New(
				errcode.KindNotFound,
				errcode.ErrZeroTestMatch,
				"pattern matched no tests — check your YAML ref",
				errcode.WithInternal(`pattern="TestSecret" pkg=./cells token=hunter2`),
				errcode.WithDetails(slog.String("ref", "journey.J-login.auto")),
			),
		},
	}

	var text bytes.Buffer
	textPrinter, err := NewVerifyPrinter("text", &text)
	require.NoError(t, err)
	require.NoError(t, textPrinter.Print(result))
	require.Contains(t, text.String(), "ERR_ZERO_TEST_MATCH")
	require.Contains(t, text.String(), "pattern matched no tests — check your YAML ref")
	require.Contains(t, text.String(), `ref="journey.J-login.auto"`)
	require.NotContains(t, text.String(), "TestSecret")
	require.NotContains(t, text.String(), "hunter2")

	var rawJSON bytes.Buffer
	jsonPrinter, err := NewVerifyPrinter("json", &rawJSON)
	require.NoError(t, err)
	require.NoError(t, jsonPrinter.Print(result))
	var doc verifyResultJSON
	require.NoError(t, json.Unmarshal(rawJSON.Bytes(), &doc))
	require.Len(t, doc.Errors, 1)
	require.Equal(t, errcode.ErrZeroTestMatch, doc.Errors[0].Code)
	require.Equal(t, "pattern matched no tests — check your YAML ref", doc.Errors[0].Message)
	require.Equal(t, []errcode.PublicDetail{
		{Key: "ref", Value: "journey.J-login.auto"},
	}, doc.Errors[0].Details)
	require.NotContains(t, rawJSON.String(), "TestSecret")
	require.NotContains(t, rawJSON.String(), "hunter2")
}

func TestVerifyJSONPrinter_EmitsSkippedOnlyAndStructuredErrors(t *testing.T) {
	result := &verify.VerifyResult{
		TargetID: "accesscore/sessions",
		Passed:   false,
		Results: []verify.TestResult{
			{Name: "TestOnlySkip", Passed: false, SkippedOnly: true},
		},
		Errors: []error{
			errcode.New(
				errcode.KindNotFound,
				errcode.ErrZeroTestMatch,
				"pattern matched only skipped tests — replace stubs with executable checks",
				errcode.WithDetails(slog.String("pattern", "^TestOnlySkip$")),
			),
		},
	}

	var rawJSON bytes.Buffer
	jsonPrinter, err := NewVerifyPrinter("json", &rawJSON)
	require.NoError(t, err)
	require.NoError(t, jsonPrinter.Print(result))

	var doc verifyResultJSON
	require.NoError(t, json.Unmarshal(rawJSON.Bytes(), &doc))
	require.Len(t, doc.Results, 1)
	require.True(t, doc.Results[0].SkippedOnly)
	require.False(t, doc.Results[0].ZeroMatch)
	require.Len(t, doc.Errors, 1)
	require.Equal(t, errcode.ErrZeroTestMatch, doc.Errors[0].Code)
	require.Equal(t, "pattern matched only skipped tests — replace stubs with executable checks", doc.Errors[0].Message)
}

func TestVerifyJSONPrinter_InternalErrcodeUsesOperatorProjection(t *testing.T) {
	result := &verify.VerifyResult{
		TargetID: "generated",
		Passed:   false,
		Errors: []error{
			errcode.New(
				errcode.KindInternal,
				errcode.ErrTestExecution,
				"go test execution failed: dsn=postgres://user:secret@example/db",
				errcode.WithInternal("token=hunter2"),
				errcode.WithDetails(slog.String("pkg", "./cells/private")),
			),
		},
	}

	var rawJSON bytes.Buffer
	jsonPrinter, err := NewVerifyPrinter("json", &rawJSON)
	require.NoError(t, err)
	require.NoError(t, jsonPrinter.Print(result))

	var doc verifyResultJSON
	require.NoError(t, json.Unmarshal(rawJSON.Bytes(), &doc))
	require.Len(t, doc.Errors, 1)
	require.Equal(t, errcode.ErrInternal, doc.Errors[0].Code)
	require.Equal(t, "internal server error", doc.Errors[0].Message)
	require.Empty(t, doc.Errors[0].Details)
	require.Equal(t, errcode.ErrTestExecution, doc.Errors[0].SourceCode)
	require.Equal(t, 500, doc.Errors[0].Status)

	for _, leak := range []string{
		"go test execution failed",
		"postgres://",
		"secret",
		"hunter2",
		"./cells/private",
		"pkg",
	} {
		require.NotContains(t, rawJSON.String(), leak)
	}
}
