package devicecmd

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/command"
)

// notifierTestTimeout is the maximum time a blocking Notify or receive is
// allowed to take in notifier tests.
const notifierTestTimeout = 2 * time.Second

// notifierNonBlockingTimeout is a short bound used to assert that Notify is
// non-blocking (the Enqueue-never-blocks invariant). A slow, stalling Notify
// must be caught well within this window.
const notifierNonBlockingTimeout = 200 * time.Millisecond

// notifierNoDeliveryWindow bounds the wait that asserts NO notification arrives
// (wrong-device / post-unsubscribe negative cases): long enough to catch an
// erroneous delivery, short enough to keep the test fast.
const notifierNoDeliveryWindow = 50 * time.Millisecond

// makeEntry builds a minimal command.Entry for the given deviceID.
func makeEntry(deviceID string) command.Entry {
	return command.Entry{
		ID:          "cmd-test",
		DeviceID:    deviceID,
		CommandType: "reboot",
		Status:      command.StatusPending,
	}
}

// TestNotifier_Subscribe_ReceivesMatchingDeviceID asserts that a subscriber
// receives notifications for its own deviceID.
func TestNotifier_Subscribe_ReceivesMatchingDeviceID(t *testing.T) {
	t.Parallel()
	n := NewNotifier()
	ch, cancel := n.Subscribe("device-1")
	defer cancel()

	n.Notify(context.Background(), makeEntry("device-1"))

	select {
	case e := <-ch:
		if e.DeviceID != "device-1" {
			t.Errorf("received entry DeviceID = %q, want device-1", e.DeviceID)
		}
	case <-time.After(notifierTestTimeout):
		t.Fatal("subscriber did not receive notification within timeout")
	}
}

// TestNotifier_Subscribe_DoesNotReceiveOtherDeviceID asserts that a subscriber
// does NOT receive notifications for a different deviceID.
func TestNotifier_Subscribe_DoesNotReceiveOtherDeviceID(t *testing.T) {
	t.Parallel()
	n := NewNotifier()
	ch, cancel := n.Subscribe("device-1")
	defer cancel()

	n.Notify(context.Background(), makeEntry("device-2"))

	select {
	case e := <-ch:
		t.Errorf("subscriber received unexpected entry %+v for different deviceID", e)
	case <-time.After(notifierNoDeliveryWindow):
		// Expected: no notification delivered.
	}
}

// TestNotifier_FullBuffer_DropsWithoutBlocking asserts that when a subscriber's
// channel is full, Notify returns promptly (best-effort, no blocking). This
// preserves the Enqueue-never-blocks invariant: a slow watcher must not stall
// command ingestion.
func TestNotifier_FullBuffer_DropsWithoutBlocking(t *testing.T) {
	t.Parallel()
	n := NewNotifier()
	ch, cancel := n.Subscribe("device-1")
	defer cancel()

	// Flood with notifierBufferSize+4 entries to overflow the buffer. All
	// Notify calls must return quickly (non-blocking select default).
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < notifierBufferSize+4; i++ {
			n.Notify(context.Background(), makeEntry("device-1"))
		}
	}()

	select {
	case <-done:
		// All Notify calls returned without blocking.
	case <-time.After(notifierNonBlockingTimeout):
		t.Fatal("Notify blocked when subscriber buffer was full (Enqueue-never-blocks violated)")
	}

	// Drain whatever landed in the buffer — should be ≤ notifierBufferSize entries.
	drained := 0
	for {
		select {
		case <-ch:
			drained++
		default:
			if drained > notifierBufferSize {
				t.Errorf("drained %d entries but buffer is only %d (impossible overflow)", drained, notifierBufferSize)
			}
			return
		}
	}
}

// TestNotifier_Cancel_UnsubscribesFromFutureNotifications asserts that after
// cancel() is called, subsequent Notify calls are not delivered to that channel.
func TestNotifier_Cancel_UnsubscribesFromFutureNotifications(t *testing.T) {
	t.Parallel()
	n := NewNotifier()
	ch, cancel := n.Subscribe("device-1")

	// Cancel first; then notify.
	cancel()

	// Drain any buffered entries that arrived before cancel (none expected here).
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case <-ch:
			default:
				return
			}
		}
	}()
	<-drainDone

	n.Notify(context.Background(), makeEntry("device-1"))

	// After unsubscribe, no new delivery must arrive.
	select {
	case e := <-ch:
		t.Errorf("received entry %+v after cancel — subscriber was not removed", e)
	case <-time.After(notifierNoDeliveryWindow):
		// Expected: no notification.
	}
}

// TestNotifier_Cancel_IsIdempotent asserts that calling cancel() twice does not
// panic or deadlock.
func TestNotifier_Cancel_IsIdempotent(t *testing.T) {
	t.Parallel()
	n := NewNotifier()
	_, cancel := n.Subscribe("device-1")
	cancel()
	cancel() // must not panic
}

// TestNotifier_ConcurrentSubscribeNotifyCancel verifies that concurrent
// Subscribe, Notify, and cancel calls do not race or deadlock. Multiple
// goroutines subscribe, receive notifications, and cancel simultaneously;
// correctness is verified under -race.
func TestNotifier_ConcurrentSubscribeNotifyCancel(t *testing.T) {
	t.Parallel()
	const goroutines = 8
	n := NewNotifier()
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := n.Subscribe("device-1")
			// Notify once on this goroutine's entry to drive concurrent fan-out.
			n.Notify(context.Background(), makeEntry("device-1"))
			// Drain whatever landed (may be 0 or more).
			select {
			case <-ch:
			default:
			}
			cancel()
		}()
	}
	// Coordinate: wait for all goroutines to complete, no sleep.
	wg.Wait()
	// After all goroutines canceled, the notifier must be empty (no leaked subs).
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.subs) != 0 {
		t.Errorf("after all cancels, subs map must be empty, got %v", n.subs)
	}
}

// TestNotifier_MultipleSubscribersForSameDevice asserts fan-out: two
// subscribers for the same deviceID both receive the notification.
func TestNotifier_MultipleSubscribersForSameDevice(t *testing.T) {
	t.Parallel()
	n := NewNotifier()
	ch1, cancel1 := n.Subscribe("device-1")
	ch2, cancel2 := n.Subscribe("device-1")
	defer cancel1()
	defer cancel2()

	n.Notify(context.Background(), makeEntry("device-1"))

	for _, ch := range []<-chan command.Entry{ch1, ch2} {
		select {
		case <-ch:
		case <-time.After(notifierTestTimeout):
			t.Fatal("one of the two subscribers did not receive the notification")
		}
	}
}
