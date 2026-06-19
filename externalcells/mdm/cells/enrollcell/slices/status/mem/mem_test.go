package mem

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/runtime/certlifecycle"

	"github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status"
)

var fixedNow = time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)

func newClock() *clockmock.FakeClock { return clockmock.New(fixedNow) }

func sampleRecord(deviceID string, epoch int64) status.CertRecord {
	return status.CertRecord{
		DeviceID:  deviceID,
		Issuer:    "CN=TestCA",
		Serial:    "01",
		State:     certlifecycle.StateActive(),
		NotBefore: fixedNow,
		NotAfter:  fixedNow.Add(365 * 24 * time.Hour),
		Epoch:     epoch,
	}
}

// TestActiveByDeviceID_Unknown verifies that a lookup for an unknown deviceID
// returns (CertRecord{}, false, nil).
func TestActiveByDeviceID_Unknown(t *testing.T) {
	repo := New(newClock())
	rec, ok, err := repo.ActiveByDeviceID(context.Background(), "unknown-device")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Errorf("expected ok=false for unknown device, got record=%+v", rec)
	}
}

// TestActiveByDeviceID_FieldConsistency verifies that after Put the returned
// record's fields match exactly what was inserted.
func TestActiveByDeviceID_FieldConsistency(t *testing.T) {
	repo := New(newClock())
	renewal := fixedNow.Add(300 * 24 * time.Hour)
	want := status.CertRecord{
		DeviceID:    "dev-1",
		Issuer:      "CN=MDM-CA",
		Serial:      "ABCD",
		State:       certlifecycle.StateActive(),
		NotBefore:   fixedNow,
		NotAfter:    fixedNow.Add(365 * 24 * time.Hour),
		Epoch:       7,
		RenewalTime: &renewal,
	}
	repo.Put(want)

	got, ok, err := repo.ActiveByDeviceID(context.Background(), "dev-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true after Put")
	}
	if got.DeviceID != want.DeviceID {
		t.Errorf("DeviceID: got %q, want %q", got.DeviceID, want.DeviceID)
	}
	if got.Issuer != want.Issuer {
		t.Errorf("Issuer: got %q, want %q", got.Issuer, want.Issuer)
	}
	if got.Serial != want.Serial {
		t.Errorf("Serial: got %q, want %q", got.Serial, want.Serial)
	}
	if got.State != want.State {
		t.Errorf("State: got %v, want %v", got.State, want.State)
	}
	if !got.NotBefore.Equal(want.NotBefore) {
		t.Errorf("NotBefore: got %v, want %v", got.NotBefore, want.NotBefore)
	}
	if !got.NotAfter.Equal(want.NotAfter) {
		t.Errorf("NotAfter: got %v, want %v", got.NotAfter, want.NotAfter)
	}
	if got.Epoch != want.Epoch {
		t.Errorf("Epoch: got %d, want %d", got.Epoch, want.Epoch)
	}
	if got.RenewalTime == nil || !got.RenewalTime.Equal(*want.RenewalTime) {
		t.Errorf("RenewalTime: got %v, want %v", got.RenewalTime, want.RenewalTime)
	}
}

// TestActiveByDeviceID_HighestEpoch verifies that when multiple records exist for
// a device, ActiveByDeviceID returns the one with the highest Epoch.
func TestActiveByDeviceID_HighestEpoch(t *testing.T) {
	repo := New(newClock())
	repo.Put(sampleRecord("dev-multi", 1))
	repo.Put(sampleRecord("dev-multi", 5))
	repo.Put(sampleRecord("dev-multi", 3))

	got, ok, err := repo.ActiveByDeviceID(context.Background(), "dev-multi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.Epoch != 5 {
		t.Errorf("expected Epoch=5 (highest), got %d", got.Epoch)
	}
}

// TestNew_NilClockPanic verifies that New(nil) panics (MustHaveClock enforcement).
func TestNew_NilClockPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("New(nil) did not panic, want panic from MustHaveClock")
		}
	}()
	New(nil)
}
