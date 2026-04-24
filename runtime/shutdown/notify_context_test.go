package shutdown

import (
	"context"
	"testing"
)

func TestNotifyContext_CancelStopsContext(t *testing.T) {
	ctx, stop := NotifyContext(context.Background())
	stop()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("NotifyContext stop function did not cancel the returned context")
	}
}
