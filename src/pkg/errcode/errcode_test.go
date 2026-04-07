package errcode

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		code    Code
		message string
		wantStr string
	}{
		{
			name:    "metadata invalid",
			code:    ErrMetadataInvalid,
			message: "cell.yaml missing required field",
			wantStr: "[ERR_METADATA_INVALID] cell.yaml missing required field",
		},
		{
			name:    "cell not found",
			code:    ErrCellNotFound,
			message: "cell access-core does not exist",
			wantStr: "[ERR_CELL_NOT_FOUND] cell access-core does not exist",
		},
		{
			name:    "dependency cycle",
			code:    ErrDependencyCycle,
			message: "a -> b -> a",
			wantStr: "[ERR_DEPENDENCY_CYCLE] a -> b -> a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New(tt.code, tt.message)
			assert.Equal(t, tt.code, err.Code)
			assert.Equal(t, tt.message, err.Message)
			assert.Nil(t, err.Details)
			assert.Nil(t, err.Cause)
			assert.Equal(t, tt.wantStr, err.Error())
		})
	}
}

func TestWrap(t *testing.T) {
	tests := []struct {
		name    string
		code    Code
		message string
		cause   error
		wantStr string
	}{
		{
			name:    "wrap stdlib error",
			code:    ErrValidationFailed,
			message: "schema check failed",
			cause:   errors.New("missing field 'id'"),
			wantStr: "[ERR_VALIDATION_FAILED] schema check failed: missing field 'id'",
		},
		{
			name:    "wrap errcode error",
			code:    ErrReferenceBroken,
			message: "contract ref invalid",
			cause:   New(ErrContractNotFound, "contract query/v1 not found"),
			wantStr: "[ERR_REFERENCE_BROKEN] contract ref invalid: [ERR_CONTRACT_NOT_FOUND] contract query/v1 not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Wrap(tt.code, tt.message, tt.cause)
			assert.Equal(t, tt.code, err.Code)
			assert.Equal(t, tt.message, err.Message)
			assert.Equal(t, tt.cause, err.Cause)
			assert.Equal(t, tt.wantStr, err.Error())

			// Unwrap chain
			assert.Equal(t, tt.cause, err.Unwrap())
			assert.ErrorIs(t, err, tt.cause)
		})
	}
}

func TestUnwrapNil(t *testing.T) {
	err := New(ErrCellNotFound, "no cause")
	assert.Nil(t, err.Unwrap())
}

func TestWithDetails(t *testing.T) {
	tests := []struct {
		name        string
		base        *Error
		details     map[string]any
		wantDetails map[string]any
	}{
		{
			name: "add details to error without existing details",
			base: New(ErrSliceNotFound, "slice not found"),
			details: map[string]any{
				"sliceId": "auth-login",
				"cellId":  "access-core",
			},
			wantDetails: map[string]any{
				"sliceId": "auth-login",
				"cellId":  "access-core",
			},
		},
		{
			name: "merge details preserving and overwriting",
			base: WithDetails(
				New(ErrMetadataInvalid, "invalid"),
				map[string]any{"field": "owner", "line": 10},
			),
			details: map[string]any{
				"line":       42,
				"suggestion": "add owner field",
			},
			wantDetails: map[string]any{
				"field":      "owner",
				"line":       42,
				"suggestion": "add owner field",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WithDetails(tt.base, tt.details)

			// WithDetails returns a new Error; original is unmodified.
			assert.NotSame(t, tt.base, result)
			assert.Equal(t, tt.base.Code, result.Code)
			assert.Equal(t, tt.base.Message, result.Message)
			assert.Equal(t, tt.wantDetails, result.Details)
		})
	}
}

func TestWithDetails_NilError(t *testing.T) {
	result := WithDetails(nil, map[string]any{"key": "val"})
	assert.Nil(t, result)
}

func TestWithDetailsDDoesNotMutateOriginal(t *testing.T) {
	original := WithDetails(New(ErrAssemblyNotFound, "not found"), map[string]any{"key": "val"})
	_ = WithDetails(original, map[string]any{"extra": "data"})

	// Original must be unchanged.
	assert.Equal(t, map[string]any{"key": "val"}, original.Details)
}

func TestErrorFormat(t *testing.T) {
	tests := []struct {
		name    string
		err     *Error
		wantStr string
	}{
		{
			name:    "without cause",
			err:     New(ErrLifecycleInvalid, "invalid transition"),
			wantStr: "[ERR_LIFECYCLE_INVALID] invalid transition",
		},
		{
			name:    "with cause",
			err:     Wrap(ErrValidationFailed, "field check", errors.New("name is empty")),
			wantStr: "[ERR_VALIDATION_FAILED] field check: name is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantStr, tt.err.Error())
		})
	}
}

func TestErrorsAsChain(t *testing.T) {
	inner := New(ErrContractNotFound, "missing contract")
	outer := Wrap(ErrReferenceBroken, "broken ref", inner)

	var target *Error
	assert.True(t, errors.As(outer, &target))
	assert.Equal(t, ErrReferenceBroken, target.Code)

	// Can also unwrap to the inner error.
	var inner2 *Error
	assert.True(t, errors.As(errors.Unwrap(outer), &inner2))
	assert.Equal(t, ErrContractNotFound, inner2.Code)
}

func TestSentinelCodes(t *testing.T) {
	codes := []Code{
		ErrMetadataInvalid,
		ErrMetadataNotFound,
		ErrCellNotFound,
		ErrSliceNotFound,
		ErrContractNotFound,
		ErrAssemblyNotFound,
		ErrLifecycleInvalid,
		ErrDependencyCycle,
		ErrValidationFailed,
		ErrReferenceBroken,
	}
	seen := make(map[Code]bool, len(codes))
	for _, c := range codes {
		assert.NotEmpty(t, string(c), "code must not be empty")
		assert.False(t, seen[c], "duplicate code: %s", c)
		seen[c] = true
	}
}
