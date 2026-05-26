package saga

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

const (
	testDefTimeout      = testtime.D30s
	testNegativeTimeout = -testtime.D1s
)

// noopStepFunc is a minimal valid StepFunc used in table tests.
var noopStepFunc StepFunc = func(_ context.Context, _ *Instance, _ []byte) ([]byte, error) {
	return nil, nil
}

// noopCompensateFunc is a minimal valid CompensateFunc used in table tests.
var noopCompensateFunc CompensateFunc = func(_ context.Context, _ *Instance, _ []byte) error {
	return nil
}

// requireErrKind asserts that err is a non-nil *errcode.Error with the
// expected Kind, following the pattern in status_test.go / instance_test.go.
func requireErrKind(t *testing.T, err error, kind errcode.Kind) {
	t.Helper()
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "expected *errcode.Error, got %T: %v", err, err)
	assert.Equal(t, kind, ecErr.Kind, "expected Kind %v, got %v", kind, ecErr.Kind)
}

// ---------------------------------------------------------------------------
// Definition.Validate tests
// ---------------------------------------------------------------------------

func TestDefinition_Validate(t *testing.T) {
	t.Parallel()

	validStep := Step{
		Name: idutil.SafeID("step-a"),
		Run:  noopStepFunc,
	}

	tests := []struct {
		name     string
		build    func() *Definition
		wantErr  bool
		wantKind errcode.Kind
	}{
		{
			name: "happy path 1 step run only",
			build: func() *Definition {
				return &Definition{
					ID:    idutil.SafeID("saga-1"),
					Steps: []Step{validStep},
				}
			},
		},
		{
			name: "happy path 3 steps",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-multi"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc},
						{Name: idutil.SafeID("s2"), Run: noopStepFunc},
						{Name: idutil.SafeID("s3"), Run: noopStepFunc},
					},
					Timeout: testDefTimeout,
				}
			},
		},
		{
			name: "empty ID",
			build: func() *Definition {
				return &Definition{
					ID:    idutil.SafeID(""),
					Steps: []Step{validStep},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "zero steps",
			build: func() *Definition {
				return &Definition{
					ID:    idutil.SafeID("saga-x"),
					Steps: nil,
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "step name empty first step",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-y"),
					Steps: []Step{
						{Name: idutil.SafeID(""), Run: noopStepFunc},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "step name empty second step",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-z"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc},
						{Name: idutil.SafeID(""), Run: noopStepFunc},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "step name unsafe characters",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-unsafe-name"),
					Steps: []Step{
						{Name: idutil.SafeID("has space"), Run: noopStepFunc},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "step name unsafe characters second step",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-unsafe-name-2"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc},
						{Name: idutil.SafeID("bad!char"), Run: noopStepFunc},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "duplicate step names",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-dup"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc},
						{Name: idutil.SafeID("s1"), Run: noopStepFunc},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindConflict,
		},
		{
			name: "step Run nil",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-nilrun"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: nil},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "step Run nil second step",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-nilrun2"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc},
						{Name: idutil.SafeID("s2"), Run: nil},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "negative step timeout",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-neg-step-to"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc, Timeout: testNegativeTimeout},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "negative definition timeout",
			build: func() *Definition {
				return &Definition{
					ID:      idutil.SafeID("saga-neg-def-to"),
					Steps:   []Step{{Name: idutil.SafeID("s1"), Run: noopStepFunc}},
					Timeout: testNegativeTimeout,
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "zero definition timeout is OK",
			build: func() *Definition {
				return &Definition{
					ID:      idutil.SafeID("saga-zero-to"),
					Steps:   []Step{{Name: idutil.SafeID("s1"), Run: noopStepFunc}},
					Timeout: 0,
				}
			},
		},
		{
			name: "zero step timeout is OK",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-zero-step-to"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc, Timeout: 0},
					},
				}
			},
		},
		{
			name: "step with Compensate is OK",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-compensate-ok"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc, Compensate: noopCompensateFunc},
					},
				}
			},
		},
		{
			name: "nil Compensate is OK",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-nil-compensate"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc, Compensate: nil},
					},
				}
			},
		},
		{
			name: "step with valid RetryPolicy is OK",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-step-retry-ok"),
					Steps: []Step{
						{
							Name: idutil.SafeID("s1"), Run: noopStepFunc,
							RetryPolicy: RetryPolicy{MaxAttempts: 3, BaseInterval: testtime.D1s, MaxInterval: testtime.D30s},
						},
					},
				}
			},
		},
		{
			name: "definition with valid RetryPolicy is OK",
			build: func() *Definition {
				return &Definition{
					ID:          idutil.SafeID("saga-def-retry-ok"),
					Steps:       []Step{{Name: idutil.SafeID("s1"), Run: noopStepFunc}},
					RetryPolicy: RetryPolicy{MaxAttempts: 5, BaseInterval: testtime.D1s, MaxInterval: testtime.D30s},
				}
			},
		},
		{
			name: "step RetryPolicy invalid propagates KindInvalid",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-step-retry-bad"),
					Steps: []Step{
						{Name: idutil.SafeID("s1"), Run: noopStepFunc, RetryPolicy: RetryPolicy{MaxAttempts: -1}},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "step RetryPolicy interval inversion propagates KindInvalid",
			build: func() *Definition {
				return &Definition{
					ID: idutil.SafeID("saga-step-retry-inv"),
					Steps: []Step{
						{
							Name: idutil.SafeID("s1"), Run: noopStepFunc,
							RetryPolicy: RetryPolicy{MaxAttempts: 3, BaseInterval: testtime.D30s, MaxInterval: testtime.D1s},
						},
					},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "definition RetryPolicy invalid propagates KindInvalid",
			build: func() *Definition {
				return &Definition{
					ID:          idutil.SafeID("saga-def-retry-bad"),
					Steps:       []Step{{Name: idutil.SafeID("s1"), Run: noopStepFunc}},
					RetryPolicy: RetryPolicy{MaxAttempts: -2},
				}
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			def := tc.build()
			err := def.Validate()
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			requireErrKind(t, err, tc.wantKind)
		})
	}
}

func TestDefinition_Len(t *testing.T) {
	t.Parallel()
	def := &Definition{
		ID: idutil.SafeID("saga-len"),
		Steps: []Step{
			{Name: idutil.SafeID("s1"), Run: noopStepFunc},
			{Name: idutil.SafeID("s2"), Run: noopStepFunc},
			{Name: idutil.SafeID("s3"), Run: noopStepFunc},
		},
	}
	assert.Equal(t, 3, def.Len())

	empty := &Definition{ID: idutil.SafeID("saga-empty")}
	assert.Equal(t, 0, empty.Len())
}

// ---------------------------------------------------------------------------
// InMemoryRegistry tests
// ---------------------------------------------------------------------------

func makeValidDef(id string) *Definition {
	return &Definition{
		ID:    idutil.SafeID(id),
		Steps: []Step{{Name: idutil.SafeID("step-1"), Run: noopStepFunc}},
	}
}

func TestNewInMemoryRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		defs     []*Definition
		wantErr  bool
		wantKind errcode.Kind
	}{
		{
			name: "zero definitions",
			defs: nil,
		},
		{
			name: "single definition",
			defs: []*Definition{makeValidDef("saga-a")},
		},
		{
			name: "multiple definitions",
			defs: []*Definition{
				makeValidDef("saga-a"),
				makeValidDef("saga-b"),
				makeValidDef("saga-c"),
			},
		},
		{
			name: "duplicate IDs returns conflict",
			defs: []*Definition{
				makeValidDef("saga-dup"),
				makeValidDef("saga-dup"),
			},
			wantErr:  true,
			wantKind: errcode.KindConflict,
		},
		{
			name: "invalid definition propagates KindInvalid",
			defs: []*Definition{
				{
					ID:    idutil.SafeID("saga-invalid"),
					Steps: nil, // empty steps → KindInvalid
				},
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name:     "nil definition returns KindInvalid",
			defs:     []*Definition{nil},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
		{
			name: "nil definition in second slot returns KindInvalid",
			defs: []*Definition{
				makeValidDef("saga-a"),
				nil,
			},
			wantErr:  true,
			wantKind: errcode.KindInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, err := NewInMemoryRegistry(tc.defs...)
			if tc.wantErr {
				requireErrKind(t, err, tc.wantKind)
				assert.Nil(t, reg)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, reg)
		})
	}
}

func TestInMemoryRegistry_Lookup(t *testing.T) {
	t.Parallel()

	defA := makeValidDef("saga-alpha")
	defB := makeValidDef("saga-beta")

	t.Run("empty registry all lookups miss", func(t *testing.T) {
		t.Parallel()
		reg, err := NewInMemoryRegistry()
		require.NoError(t, err)
		got, ok := reg.Lookup(idutil.SafeID("anything"))
		assert.False(t, ok)
		assert.Nil(t, got)
	})

	t.Run("single definition lookup hit returns pointer equality", func(t *testing.T) {
		t.Parallel()
		reg, err := NewInMemoryRegistry(defA)
		require.NoError(t, err)
		got, ok := reg.Lookup(defA.ID)
		assert.True(t, ok)
		assert.Same(t, defA, got)
	})

	t.Run("multiple definitions each lookup works", func(t *testing.T) {
		t.Parallel()
		reg, err := NewInMemoryRegistry(defA, defB)
		require.NoError(t, err)

		gotA, okA := reg.Lookup(defA.ID)
		assert.True(t, okA)
		assert.Same(t, defA, gotA)

		gotB, okB := reg.Lookup(defB.ID)
		assert.True(t, okB)
		assert.Same(t, defB, gotB)
	})

	t.Run("lookup unknown ID returns false", func(t *testing.T) {
		t.Parallel()
		reg, err := NewInMemoryRegistry(defA)
		require.NoError(t, err)
		got, ok := reg.Lookup(idutil.SafeID("saga-unknown"))
		assert.False(t, ok)
		assert.Nil(t, got)
	})
}

func TestInMemoryRegistry_ConcurrentLookup(t *testing.T) {
	t.Parallel()

	defs := []*Definition{
		makeValidDef("saga-c1"),
		makeValidDef("saga-c2"),
		makeValidDef("saga-c3"),
	}
	reg, err := NewInMemoryRegistry(defs...)
	require.NoError(t, err)

	const goroutines = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			id := defs[n%len(defs)].ID
			got, ok := reg.Lookup(id)
			assert.True(t, ok)
			assert.NotNil(t, got)
		}(i)
	}
	wg.Wait()
}

// Ensure InMemoryRegistry satisfies the Resolver interface at compile time.
var _ Resolver = (*InMemoryRegistry)(nil)
