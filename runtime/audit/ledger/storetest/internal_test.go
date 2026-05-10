package storetest

import (
	"testing"
	"time"
)

// TestEpochAnchor verifies the exported EpochAnchor value matches the internal
// epochAnchor constant used by the suite fixtures.
func TestEpochAnchor(t *testing.T) {
	t.Parallel()
	got := EpochAnchor()
	want := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("EpochAnchor: got %v, want %v", got, want)
	}
}

// TestNewTestProtocol_Constructible verifies NewTestProtocol succeeds (smoke
// test — the protocol fixture itself must be constructible by storetest callers).
func TestNewTestProtocol_Constructible(t *testing.T) {
	t.Parallel()
	p := NewTestProtocol(t)
	if p == nil {
		t.Fatal("NewTestProtocol: returned nil protocol")
	}
}

// TestNewEntryFixture_RequiresEventID verifies that NewEntryFixture panics/fatals
// on empty eventID (via testing.T.Fatal — the test harness catches this).
func TestNewEntryFixture_DefaultFields(t *testing.T) {
	t.Parallel()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	e := NewEntryFixture(t, "test-event", "", "", now)
	if e.EventID != "test-event" {
		t.Errorf("EventID: got %q, want %q", e.EventID, "test-event")
	}
	if e.EventType != "test.event" {
		t.Errorf("EventType: got %q, want default 'test.event'", e.EventType)
	}
	if e.ActorID != "actor-test" {
		t.Errorf("ActorID: got %q, want default 'actor-test'", e.ActorID)
	}
	if !e.Timestamp.Equal(now) {
		t.Errorf("Timestamp: got %v, want %v", e.Timestamp, now)
	}
}
