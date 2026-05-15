package errutil_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/testutil/errutil"
)

func TestFlattenJoined_Nil(t *testing.T) {
	assert.Nil(t, errutil.FlattenJoined(nil))
}

func TestFlattenJoined_SingleLeaf(t *testing.T) {
	err := errors.New("single")
	leaves := errutil.FlattenJoined(err)
	assert.Equal(t, []error{err}, leaves)
}

func TestFlattenJoined_FlatJoin(t *testing.T) {
	a := errors.New("a")
	b := errors.New("b")
	c := errors.New("c")
	joined := errors.Join(a, b, c)
	leaves := errutil.FlattenJoined(joined)
	assert.Equal(t, []error{a, b, c}, leaves)
}

func TestFlattenJoined_NestedJoin(t *testing.T) {
	a := errors.New("a")
	b := errors.New("b")
	c := errors.New("c")
	inner := errors.Join(a, b)
	outer := errors.Join(inner, c)
	leaves := errutil.FlattenJoined(outer)
	// depth-first left-to-right: a, b, c
	assert.Equal(t, []error{a, b, c}, leaves)
}

func TestFlattenJoined_WrappedError_TreatedAsLeaf(t *testing.T) {
	cause := errors.New("cause")
	wrapped := errors.Join(cause)
	// errors.Join([cause]) implements Unwrap() []error, so cause is the leaf.
	leaves := errutil.FlattenJoined(wrapped)
	assert.Len(t, leaves, 1)
	assert.ErrorIs(t, leaves[0], cause)
}
