package cellvocab_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestParseLevelRoundTrip(t *testing.T) {
	tests := []struct {
		input string
		want  cellvocab.Level
	}{
		{"L0", cellvocab.L0},
		{"L1", cellvocab.L1},
		{"L2", cellvocab.L2},
		{"L3", cellvocab.L3},
		{"L4", cellvocab.L4},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := cellvocab.ParseLevel(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.input, got.String())
		})
	}
}

func TestParseLevelInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"lowercase", "l0"},
		{"no prefix", "0"},
		{"out of range", "L5"},
		{"garbage", "foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cellvocab.ParseLevel(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

func TestLevelStringOutOfRange(t *testing.T) {
	l := cellvocab.Level(99)
	assert.Contains(t, l.String(), "Level(99)")
}

func TestLevelConstants(t *testing.T) {
	assert.Equal(t, cellvocab.Level(0), cellvocab.L0)
	assert.Equal(t, cellvocab.Level(1), cellvocab.L1)
	assert.Equal(t, cellvocab.Level(2), cellvocab.L2)
	assert.Equal(t, cellvocab.Level(3), cellvocab.L3)
	assert.Equal(t, cellvocab.Level(4), cellvocab.L4)
}

func TestCellTypeValues(t *testing.T) {
	assert.Equal(t, cellvocab.CellType("core"), cellvocab.CellTypeCore)
	assert.Equal(t, cellvocab.CellType("edge"), cellvocab.CellTypeEdge)
	assert.Equal(t, cellvocab.CellType("support"), cellvocab.CellTypeSupport)
}

func TestContractKindValues(t *testing.T) {
	assert.Equal(t, cellvocab.ContractKind("http"), cellvocab.ContractHTTP)
	assert.Equal(t, cellvocab.ContractKind("event"), cellvocab.ContractEvent)
	assert.Equal(t, cellvocab.ContractKind("command"), cellvocab.ContractCommand)
	assert.Equal(t, cellvocab.ContractKind("projection"), cellvocab.ContractProjection)
}

func TestContractRoleValues(t *testing.T) {
	assert.Equal(t, cellvocab.ContractRole("serve"), cellvocab.RoleServe)
	assert.Equal(t, cellvocab.ContractRole("call"), cellvocab.RoleCall)
	assert.Equal(t, cellvocab.ContractRole("publish"), cellvocab.RolePublish)
	assert.Equal(t, cellvocab.ContractRole("subscribe"), cellvocab.RoleSubscribe)
	assert.Equal(t, cellvocab.ContractRole("handle"), cellvocab.RoleHandle)
	assert.Equal(t, cellvocab.ContractRole("invoke"), cellvocab.RoleInvoke)
	assert.Equal(t, cellvocab.ContractRole("provide"), cellvocab.RoleProvide)
	assert.Equal(t, cellvocab.ContractRole("read"), cellvocab.RoleRead)
}

func TestLifecycleValues(t *testing.T) {
	assert.Equal(t, cellvocab.Lifecycle("draft"), cellvocab.LifecycleDraft)
	assert.Equal(t, cellvocab.Lifecycle("active"), cellvocab.LifecycleActive)
	assert.Equal(t, cellvocab.Lifecycle("deprecated"), cellvocab.LifecycleDeprecated)
}

// ---------------------------------------------------------------------------
// ParseCellType
// ---------------------------------------------------------------------------

func TestParseCellTypeRoundTrip(t *testing.T) {
	tests := []struct {
		input string
		want  cellvocab.CellType
	}{
		{"core", cellvocab.CellTypeCore},
		{"edge", cellvocab.CellTypeEdge},
		{"support", cellvocab.CellTypeSupport},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := cellvocab.ParseCellType(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.input, string(got))
		})
	}
}

func TestParseCellTypeInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"uppercase", "Core"},
		{"unknown", "gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cellvocab.ParseCellType(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// ParseContractKind
// ---------------------------------------------------------------------------

func TestParseContractKindRoundTrip(t *testing.T) {
	tests := []struct {
		input string
		want  cellvocab.ContractKind
	}{
		{"http", cellvocab.ContractHTTP},
		{"event", cellvocab.ContractEvent},
		{"command", cellvocab.ContractCommand},
		{"projection", cellvocab.ContractProjection},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := cellvocab.ParseContractKind(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.input, string(got))
		})
	}
}

func TestParseContractKindInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"uppercase", "HTTP"},
		{"unknown", "grpc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cellvocab.ParseContractKind(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// ParseContractRole
// ---------------------------------------------------------------------------

func TestParseContractRoleRoundTrip(t *testing.T) {
	tests := []struct {
		input string
		want  cellvocab.ContractRole
	}{
		{"serve", cellvocab.RoleServe},
		{"call", cellvocab.RoleCall},
		{"publish", cellvocab.RolePublish},
		{"subscribe", cellvocab.RoleSubscribe},
		{"handle", cellvocab.RoleHandle},
		{"invoke", cellvocab.RoleInvoke},
		{"provide", cellvocab.RoleProvide},
		{"read", cellvocab.RoleRead},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := cellvocab.ParseContractRole(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.input, string(got))
		})
	}
}

func TestParseContractRoleInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"uppercase", "Serve"},
		{"unknown", "emit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cellvocab.ParseContractRole(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// ParseLifecycle
// ---------------------------------------------------------------------------

func TestParseLifecycleRoundTrip(t *testing.T) {
	tests := []struct {
		input string
		want  cellvocab.Lifecycle
	}{
		{"draft", cellvocab.LifecycleDraft},
		{"active", cellvocab.LifecycleActive},
		{"deprecated", cellvocab.LifecycleDeprecated},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := cellvocab.ParseLifecycle(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.input, string(got))
		})
	}
}

func TestParseLifecycleInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"uppercase", "Active"},
		{"unknown", "archived"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cellvocab.ParseLifecycle(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}
