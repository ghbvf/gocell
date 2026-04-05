package cell

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLevelRoundTrip(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"L0", L0},
		{"L1", L1},
		{"L2", L2},
		{"L3", L3},
		{"L4", L4},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseLevel(tt.input)
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
			_, err := ParseLevel(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}

func TestLevelStringOutOfRange(t *testing.T) {
	l := Level(99)
	assert.Contains(t, l.String(), "Level(99)")
}

func TestLevelConstants(t *testing.T) {
	assert.Equal(t, Level(0), L0)
	assert.Equal(t, Level(1), L1)
	assert.Equal(t, Level(2), L2)
	assert.Equal(t, Level(3), L3)
	assert.Equal(t, Level(4), L4)
}

func TestCellTypeValues(t *testing.T) {
	assert.Equal(t, CellType("core"), CellTypeCore)
	assert.Equal(t, CellType("edge"), CellTypeEdge)
	assert.Equal(t, CellType("support"), CellTypeSupport)
}

func TestContractKindValues(t *testing.T) {
	assert.Equal(t, ContractKind("http"), ContractHTTP)
	assert.Equal(t, ContractKind("event"), ContractEvent)
	assert.Equal(t, ContractKind("command"), ContractCommand)
	assert.Equal(t, ContractKind("projection"), ContractProjection)
}

func TestContractRoleValues(t *testing.T) {
	assert.Equal(t, ContractRole("serve"), RoleServe)
	assert.Equal(t, ContractRole("call"), RoleCall)
	assert.Equal(t, ContractRole("publish"), RolePublish)
	assert.Equal(t, ContractRole("subscribe"), RoleSubscribe)
	assert.Equal(t, ContractRole("handle"), RoleHandle)
	assert.Equal(t, ContractRole("invoke"), RoleInvoke)
	assert.Equal(t, ContractRole("provide"), RoleProvide)
	assert.Equal(t, ContractRole("read"), RoleRead)
}

func TestLifecycleValues(t *testing.T) {
	assert.Equal(t, Lifecycle("draft"), LifecycleDraft)
	assert.Equal(t, Lifecycle("active"), LifecycleActive)
	assert.Equal(t, Lifecycle("deprecated"), LifecycleDeprecated)
}

// ---------------------------------------------------------------------------
// ParseCellType
// ---------------------------------------------------------------------------

func TestParseCellTypeRoundTrip(t *testing.T) {
	tests := []struct {
		input string
		want  CellType
	}{
		{"core", CellTypeCore},
		{"edge", CellTypeEdge},
		{"support", CellTypeSupport},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseCellType(tt.input)
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
			_, err := ParseCellType(tt.input)
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
		want  ContractKind
	}{
		{"http", ContractHTTP},
		{"event", ContractEvent},
		{"command", ContractCommand},
		{"projection", ContractProjection},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseContractKind(tt.input)
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
			_, err := ParseContractKind(tt.input)
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
		want  ContractRole
	}{
		{"serve", RoleServe},
		{"call", RoleCall},
		{"publish", RolePublish},
		{"subscribe", RoleSubscribe},
		{"handle", RoleHandle},
		{"invoke", RoleInvoke},
		{"provide", RoleProvide},
		{"read", RoleRead},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseContractRole(tt.input)
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
			_, err := ParseContractRole(tt.input)
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
		want  Lifecycle
	}{
		{"draft", LifecycleDraft},
		{"active", LifecycleActive},
		{"deprecated", LifecycleDeprecated},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseLifecycle(tt.input)
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
			_, err := ParseLifecycle(tt.input)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
		})
	}
}
