package mqtt

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestWaitConnected_CtxDone_DistinguishesDeadlineVsCancel locks the #1388
// deadline-vs-cancel split on the WaitConnected wait path (the bootstrap path is
// covered by TestConnection_ConnectDeadline_FailFast /
// TestConnection_LifecycleCtxCanceled_ReturnsCanceled). A zero-value Connection
// is in phaseConnecting with a nil stateCh, so the select can only fire on
// ctx.Done — exactly the branch under test, with no broker required.
func TestWaitConnected_CtxDone_DistinguishesDeadlineVsCancel(t *testing.T) {
	t.Parallel()

	t.Run("explicit cancel → ConnectCanceled", func(t *testing.T) {
		t.Parallel()
		c := &Connection{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // canceled before the call → ctx.Err() == context.Canceled

		err := c.WaitConnected(ctx)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, ErrAdapterMQTTConnectCanceled, ec.Code)
	})

	t.Run("deadline elapse → ConnectTimeout", func(t *testing.T) {
		t.Parallel()
		c := &Connection{}
		ctx, cancel := context.WithTimeout(context.Background(), testtime.D10ms)
		defer cancel()

		err := c.WaitConnected(ctx)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, ErrAdapterMQTTConnectTimeout, ec.Code)
	})
}
