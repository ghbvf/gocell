// Package missing_funnel_red proves that a _NotFound test missing any
// errcodetest funnel call is rejected. Line 12 is flagged.
package missing_funnel_red

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFoo_NotFound(t *testing.T) {
	err := errors.New("simulated")
	require.Error(t, err)
}
